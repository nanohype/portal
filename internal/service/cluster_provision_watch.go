package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	"github.com/nanohype/portal/internal/clusterspec"
	"github.com/nanohype/portal/internal/k8s"
	"github.com/nanohype/portal/internal/repository"
)

// ClusterProvisionWatchService closes the vend loop. For each committed
// provision op it reads the eks-fleet Cluster XR on the hub, and once that
// cluster's EKS endpoint + CA are up it auto-registers a portal cluster (as
// eks_iam — no stored token) and flips the op to 'active'. The new cluster then
// flows through the existing connection-test + tenant-watch surfaces with no
// extra wiring. Modeled on ArgoCDSyncService: a periodic in-cluster sync rather
// than a River job, since the cluster_operations rows carry all durable state.
type ClusterProvisionWatchService struct {
	clusters *ClusterService
	queries  *repository.Queries
	hub      dynamic.Interface
}

func NewClusterProvisionWatchService(clusters *ClusterService, queries *repository.Queries, hub dynamic.Interface) *ClusterProvisionWatchService {
	return &ClusterProvisionWatchService{clusters: clusters, queries: queries, hub: hub}
}

// Sync runs one pass: auto-register every committed provision op whose Cluster
// XR is ready, and leave the rest for a later tick. Returns counts for logging.
func (s *ClusterProvisionWatchService) Sync(ctx context.Context) (registered, pending int, err error) {
	ops, err := s.queries.ListClusterOperationsByStatus(ctx, "committed")
	if err != nil {
		return 0, 0, fmt.Errorf("list committed operations: %w", err)
	}
	for _, op := range ops {
		if op.Operation != "provision" {
			continue
		}
		done, rErr := s.reconcile(ctx, op)
		switch {
		case rErr != nil:
			slog.Warn("provision watch-back: reconcile op", "op", op.ID, "name", op.Name, "error", rErr)
		case done:
			registered++
		default:
			pending++
		}
	}
	return registered, pending, nil
}

// reconcile registers one op's cluster if its XR is ready. (done=false, err=nil)
// means "not applied or not ready yet — try the next tick".
func (s *ClusterProvisionWatchService) reconcile(ctx context.Context, op repository.ClusterOperation) (bool, error) {
	xr, err := s.hub.Resource(k8s.ClusterGVR).Namespace(op.Team).Get(ctx, op.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil // ArgoCD hasn't applied the Cluster CR yet
	}
	if err != nil {
		return false, fmt.Errorf("get Cluster XR %s/%s: %w", op.Team, op.Name, err)
	}
	st := clusterStatus(xr.Object)
	if !st.ready() {
		return false, nil // endpoint/CA/name not all populated yet — still vending
	}

	clusterID, err := s.register(ctx, op, st)
	if err != nil {
		return false, err
	}
	if err := s.queries.ActivateClusterOperation(ctx, repository.ActivateClusterOperationParams{
		ID: op.ID, OrgID: op.OrgID, ClusterID: clusterID, CompletedAt: time.Now(),
	}); err != nil {
		return false, fmt.Errorf("activate op: %w", err)
	}
	slog.Info("provision watch-back: cluster registered", "op", op.ID, "name", op.Name, "cluster_id", clusterID)
	return true, nil
}

func (s *ClusterProvisionWatchService) register(ctx context.Context, op repository.ClusterOperation, st clusterXRStatus) (string, error) {
	var in clusterspec.Input
	if err := json.Unmarshal(op.SpecJSON, &in); err != nil {
		return "", fmt.Errorf("decode op spec: %w", err)
	}
	acct, err := s.queries.GetAccountByAWSID(ctx, repository.GetAccountByAWSIDParams{OrgID: op.OrgID, AWSAccountID: in.Account})
	if err != nil {
		return "", fmt.Errorf("no portal account for AWS account %s: %w", in.Account, err)
	}
	caPEM, err := base64.StdEncoding.DecodeString(st.caData)
	if err != nil {
		return "", fmt.Errorf("decode cluster CA: %w", err)
	}
	region := in.Region
	if region == "" {
		region = acct.DefaultRegion
	}

	created, err := s.clusters.Create(ctx, CreateClusterParams{
		OrgID:          op.OrgID,
		AccountID:      acct.ID,
		Name:           op.Name,
		Description:    fmt.Sprintf("Auto-registered from cluster-order %s", op.ID),
		Environment:    op.Environment,
		APIEndpoint:    st.endpoint,
		CABundle:       string(caPEM),
		Region:         region,
		AuthMode:       AuthModeEKSIAM,
		EKSClusterName: st.clusterName,
		CreatedBy:      op.CreatedBy,
	})
	if err != nil {
		// A prior tick registered the cluster but crashed before activating the
		// op — recover the existing row so the op still links + activates.
		if isUniquePGViolation(err) {
			existing, getErr := s.queries.GetClusterByName(ctx, repository.GetClusterByNameParams{OrgID: op.OrgID, Name: op.Name})
			if getErr != nil {
				return "", fmt.Errorf("cluster %q exists but lookup failed: %w", op.Name, getErr)
			}
			return existing.ID, nil
		}
		return "", fmt.Errorf("register cluster: %w", err)
	}

	// Kick a connection test so the cluster flips to 'connected' and the
	// existing tenant-watcher picks it up — no per-cluster wiring needed.
	if err := s.clusters.EnqueueConnectionTest(ctx, created.ID, op.OrgID); err != nil {
		slog.Warn("provision watch-back: enqueue connection test", "cluster_id", created.ID, "error", err)
	}
	return created.ID, nil
}

type clusterXRStatus struct {
	endpoint    string
	caData      string // base64-encoded PEM, as EKS reports it
	clusterName string
}

// ready reports whether the vended cluster's API is reachable: all three of the
// EKS endpoint, CA, and (token-binding) cluster name must be populated before
// portal can register + auth to it.
func (st clusterXRStatus) ready() bool {
	return st.endpoint != "" && st.caData != "" && st.clusterName != ""
}

// clusterStatus pulls the fields the watch-back needs from an eks-fleet Cluster
// XR's .status, defensively — status is absent or partially populated while
// Crossplane is still reconciling.
func clusterStatus(obj map[string]interface{}) clusterXRStatus {
	var st clusterXRStatus
	status, ok := obj["status"].(map[string]interface{})
	if !ok {
		return st
	}
	if v, ok := status["clusterEndpoint"].(string); ok {
		st.endpoint = v
	}
	if v, ok := status["certificateAuthorityData"].(string); ok {
		st.caData = v
	}
	if v, ok := status["clusterName"].(string); ok {
		st.clusterName = v
	}
	return st
}

func isUniquePGViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
