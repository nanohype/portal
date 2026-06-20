package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	"github.com/nanohype/portal/internal/aws"
	"github.com/nanohype/portal/internal/k8s"
	"github.com/nanohype/portal/internal/repository"
)

// clusterHealthConcurrency bounds the per-cluster fan-out. The work is
// I/O-bound (a hub Application GET + an EKS describe), so a modest pool keeps
// wall-clock flat as cluster count grows without hammering the hub API or STS.
const clusterHealthConcurrency = 12

// clusterHealthTargetCap mirrors the LIMIT in ListClusterHealthTargets. It's a
// generous ceiling for the "many clusters, not hyperscale" target; if a sweep
// ever hits it, log loudly rather than silently dropping clusters.
const clusterHealthTargetCap = 10000

// ClusterHealthService is the steady-state per-cluster health projector. Running
// in-cluster on the hub, every tick it reads — for each registered cluster — the
// per-cluster ArgoCD Application on the hub (cluster-<environment>-<name>, the
// name the eks-gitops clusters appset templates) and, for eks_iam clusters, the
// EKS control plane via eks:DescribeCluster. It writes those onto the cluster row
// (mirroring the connection-test projection), so the cluster surface shows live
// ArgoCD + control-plane health without a probe in the request path.
//
// Distinct from ClusterProvisionWatchService: that one watches IN-FLIGHT vends
// and stops once a cluster goes active; this one runs continuously over every
// registered cluster. It reads the HUB (Applications) + AWS, never the spoke, so
// a cluster portal can't connect to can still report ArgoCD/control-plane health.
type ClusterHealthService struct {
	clusters *ClusterService
	accounts *AccountService
	queries  *repository.Queries
	hub      dynamic.Interface
	aws      *aws.Provider // optional — nil disables the EKS control-plane read
	argocdNS string        // namespace the per-cluster Applications live in (hub)
}

func NewClusterHealthService(clusters *ClusterService, accounts *AccountService, queries *repository.Queries, hub dynamic.Interface, awsProvider *aws.Provider, argocdNS string) *ClusterHealthService {
	if argocdNS == "" {
		argocdNS = "argocd"
	}
	return &ClusterHealthService{clusters: clusters, accounts: accounts, queries: queries, hub: hub, aws: awsProvider, argocdNS: argocdNS}
}

// Sync runs one pass over every registered cluster. Per-cluster failures are
// logged and skipped (best-effort projection); only a failure to list the
// targets aborts the pass.
func (s *ClusterHealthService) Sync(ctx context.Context) (int, error) {
	targets, err := s.queries.ListClusterHealthTargets(ctx)
	if err != nil {
		return 0, fmt.Errorf("list cluster health targets: %w", err)
	}
	if len(targets) >= clusterHealthTargetCap {
		slog.Warn("cluster health sweep hit the target cap; some clusters may be unwatched",
			"cap", clusterHealthTargetCap)
	}
	// Per-cluster work is independent and I/O-bound, so fan out with a bounded
	// pool instead of a serial pass — wall-clock no longer grows linearly with
	// cluster count, and the assume-role config cache means the EKS describes
	// share temporary credentials. Each reconcile logs its own errors and never
	// returns one, so Wait can't fail.
	var g errgroup.Group
	g.SetLimit(clusterHealthConcurrency)
	for _, t := range targets {
		g.Go(func() error {
			// Bound each cluster's external calls (the hub Application GET and the
			// EKS DescribeCluster) so a hung hub apiserver or EKS/STS endpoint
			// can't pin one of the bounded errgroup slots indefinitely and starve
			// successive ticks. Matches the 30s convention used in discovery.go.
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			s.reconcileArgoCD(cctx, t)
			if s.aws != nil && t.AuthMode == AuthModeEKSIAM && t.EKSClusterName != "" {
				s.reconcileEKS(cctx, t)
			}
			return nil
		})
	}
	_ = g.Wait()
	return len(targets), nil
}

// reconcileArgoCD reads the cluster's per-cluster ArgoCD Application on the hub
// and records its sync + health. NotFound is DEFINITIVE (no per-cluster
// Application — a hand-registered cluster, or one whose CR was pruned), so it
// clears the fields to empty. A transient read error skips the write so the
// last-known values survive.
func (s *ClusterHealthService) reconcileArgoCD(ctx context.Context, t repository.ClusterHealthTarget) {
	appName := fmt.Sprintf("cluster-%s-%s", t.Environment, t.Name)
	app, err := s.hub.Resource(k8s.ApplicationGVR).Namespace(s.argocdNS).Get(ctx, appName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err := s.clusters.SetArgoCDHealth(ctx, t.ID, t.OrgID, "", ""); err != nil {
			slog.Warn("cluster health: clear argocd", "cluster", t.ID, "error", err)
		}
		return
	}
	if err != nil {
		slog.Warn("cluster health: get application", "app", appName, "error", err)
		return // transient — preserve last-known
	}
	sync, health := applicationHealth(app.Object)
	if err := s.clusters.SetArgoCDHealth(ctx, t.ID, t.OrgID, sync, health); err != nil {
		slog.Warn("cluster health: set argocd", "cluster", t.ID, "error", err)
	}
}

// reconcileEKS records the EKS control-plane status + platform version. The
// assume-role must hold eks:DescribeCluster; until that IAM is granted the call
// returns AccessDenied, which is treated as "unknown" — logged at debug and the
// prior values preserved, so the rest of the health projection still works.
func (s *ClusterHealthService) reconcileEKS(ctx context.Context, t repository.ClusterHealthTarget) {
	account, err := s.queries.GetAccount(ctx, repository.GetAccountParams{ID: t.AccountID, OrgID: t.OrgID})
	if err != nil {
		slog.Warn("cluster health: load account", "cluster", t.ID, "error", err)
		return
	}
	if account.AssumeRoleARN == "" {
		return // no role to assume — nothing to describe
	}
	externalID, err := s.accounts.DecryptExternalID(account)
	if err != nil {
		slog.Debug("cluster health: decrypt external_id", "cluster", t.ID, "error", err)
		return // graceful — preserve last-known
	}
	st, err := s.aws.DescribeCluster(ctx, account.AssumeRoleARN, externalID, t.Region, t.EKSClusterName)
	if err != nil {
		slog.Debug("cluster health: describe cluster", "cluster", t.ID, "error", err)
		return // graceful (commonly AccessDenied) — preserve last-known
	}
	if err := s.clusters.SetControlPlane(ctx, t.ID, t.OrgID, st.Status, st.PlatformVersion); err != nil {
		slog.Warn("cluster health: set control plane", "cluster", t.ID, "error", err)
	}
}

// applicationHealth defensively extracts an ArgoCD Application's sync + health
// status strings from its unstructured .status — both may be absent early in a
// reconcile or oddly shaped.
func applicationHealth(obj map[string]interface{}) (sync, health string) {
	status, ok := obj["status"].(map[string]interface{})
	if !ok {
		return "", ""
	}
	return nestedString(status, "sync", "status"), nestedString(status, "health", "status")
}

// nestedString walks a chain of map keys and returns the terminal string, or ""
// if any hop is missing or not the expected type.
func nestedString(m map[string]interface{}, keys ...string) string {
	cur := m
	for i, k := range keys {
		if i == len(keys)-1 {
			s, _ := cur[k].(string)
			return s
		}
		next, ok := cur[k].(map[string]interface{})
		if !ok {
			return ""
		}
		cur = next
	}
	return ""
}
