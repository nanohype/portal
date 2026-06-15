package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

// Sync runs one pass over the committed operations: auto-register every
// provision whose Cluster XR is ready, close out every deprovision whose XR is
// gone, and leave the rest for a later tick. Returns counts for logging —
// completed = a terminal transition this tick (registered or deprovisioned).
func (s *ClusterProvisionWatchService) Sync(ctx context.Context) (completed, pending int, err error) {
	ops, err := s.queries.ListClusterOperationsByStatus(ctx, "committed")
	if err != nil {
		return 0, 0, fmt.Errorf("list committed operations: %w", err)
	}
	for _, op := range ops {
		var done bool
		var rErr error
		switch op.Operation {
		case "provision":
			done, rErr = s.reconcile(ctx, op)
		case "deprovision":
			done, rErr = s.reconcileDeprovision(ctx, op)
		default:
			continue
		}
		switch {
		case rErr != nil:
			slog.Warn("cluster watch-back: reconcile op", "op", op.ID, "name", op.Name, "operation", op.Operation, "error", rErr)
		case done:
			completed++
		default:
			pending++
		}
	}
	return completed, pending, nil
}

// reconcile registers one op's cluster if its XR is ready. (done=false, err=nil)
// means "not applied or not ready yet — try the next tick".
func (s *ClusterProvisionWatchService) reconcile(ctx context.Context, op repository.ClusterOperation) (bool, error) {
	xr, err := s.hub.Resource(k8s.ClusterGVR).Namespace(op.Team).Get(ctx, op.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// ArgoCD hasn't applied the Cluster CR yet — wait. Unless this op has been
		// committed long past the worst-case vend, in which case the CR is never
		// coming (watcher off on a local order, the order abandoned, or the CR
		// deleted out-of-band on the hub): reap it so it leaves the committed
		// working set instead of being re-reconciled fruitlessly every tick.
		if time.Since(op.CreatedAt) > provisionReapAfter {
			return s.reapStuckProvision(ctx, op)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get Cluster XR %s/%s: %w", op.Team, op.Name, err)
	}
	st := clusterStatus(xr.Object)
	if !st.ready() {
		// Still vending — project the tofu build phase (tofu_running, carrying any
		// current tofu error) from the composed Workspaces so the timeline moves.
		s.projectTofuPhase(ctx, op)
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

// provisionReapAfter bounds how long a committed provision may have no Cluster
// XR before the watcher gives up on it. It must comfortably exceed the worst-case
// vend (cluster-stack + cluster-bootstrap converge in well under an hour), so a
// slow-but-live build is never reaped — only a CR that genuinely never applied.
const provisionReapAfter = 2 * time.Hour

// reapStuckProvision marks a committed provision terminal when its Cluster XR
// never appeared past provisionReapAfter. status=failed, so the frontend stops
// polling and surfaces op.error; the reason makes clear it expired rather than
// hard-failed. Mirrors reconcileDeprovision's terminal write — phase first
// (best-effort), then the guarded status flip.
func (s *ClusterProvisionWatchService) reapStuckProvision(ctx context.Context, op repository.ClusterOperation) (bool, error) {
	const reason = "provision expired: the Cluster CR never appeared on the hub after commit — ArgoCD isn't syncing the clusters repo, the order was abandoned, or the CR was deleted out-of-band. Re-order to retry."
	s.setVendPhase(ctx, op, "failed", reason)
	if err := s.queries.ExpireClusterOperation(ctx, repository.ExpireClusterOperationParams{
		ID: op.ID, OrgID: op.OrgID, Error: reason, CompletedAt: time.Now(),
	}); err != nil {
		return false, fmt.Errorf("expire stuck provision op: %w", err)
	}
	slog.Info("provision watch-back: reaped stuck provision (Cluster XR never applied)",
		"op", op.ID, "name", op.Name, "age", time.Since(op.CreatedAt).Round(time.Minute))
	return true, nil
}

// reconcileDeprovision closes out a committed deprovision once Crossplane has
// torn the cluster down. The Cluster XR going NotFound on the hub IS the
// completion signal — ArgoCD prunes the CR, Crossplane runs tofu destroy on the
// composed Workspaces, and only when they're gone does the XR disappear. While
// the XR still exists, teardown is in flight: project a regressible
// 'deprovisioning' phase carrying any tofu-destroy error so a wedged teardown is
// visible. (done=false, err=nil) means "still tearing down — try the next tick".
func (s *ClusterProvisionWatchService) reconcileDeprovision(ctx context.Context, op repository.ClusterOperation) (bool, error) {
	_, err := s.hub.Resource(k8s.ClusterGVR).Namespace(op.Team).Get(ctx, op.Name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("get Cluster XR %s/%s: %w", op.Team, op.Name, err)
	}
	if err == nil {
		// XR still present — teardown in progress. Surface destroy progress/errors.
		s.projectDeprovisionPhase(ctx, op)
		return false, nil
	}
	// XR gone — teardown complete. Record the terminal phase first (best-effort),
	// then flip the op; the status query is guarded WHERE status='committed', so a
	// retry next tick is harmless if either write hiccups.
	s.setVendPhase(ctx, op, "deprovisioned", "")
	if err := s.queries.DeprovisionClusterOperation(ctx, repository.DeprovisionClusterOperationParams{
		ID: op.ID, OrgID: op.OrgID, CompletedAt: time.Now(),
	}); err != nil {
		return false, fmt.Errorf("close deprovision op: %w", err)
	}
	slog.Info("cluster watch-back: cluster deprovisioned", "op", op.ID, "name", op.Name)
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
	xrReady     bool // the XR's own Ready condition (function-auto-ready)
}

// ready reports whether the vend is fully done: the EKS endpoint, CA, and
// cluster name are published (so portal can register + auth) AND the XR's own
// Ready condition is True. The endpoint/CA/name publish as soon as the cluster-
// STACK Workspace applies, but the XR isn't Ready until the cluster-BOOTSTRAP
// Workspace also converges (function-auto-ready). Gating on both keeps "active"
// honest and lets the timeline observe the bootstrap phase (and any failure)
// before the op leaves the committed working set.
func (st clusterXRStatus) ready() bool {
	return st.endpoint != "" && st.caData != "" && st.clusterName != "" && st.xrReady
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
	for _, c := range conditions(obj) {
		if c.condType == "Ready" && c.status == "True" {
			st.xrReady = true
		}
	}
	return st
}

// projectTofuPhase reads the cluster's provider-opentofu Workspaces on the hub
// and advances the vend timeline to tofu_running while they build, carrying the
// current tofu error (if any) as the phase detail. Best-effort and REGRESSIBLE:
// it re-writes each tick so the detail tracks the latest tofu error — a transient
// ReconcileError clears when the Workspace recovers, a genuine one persists. It
// writes no terminal "tofu_failed"; provider-opentofu retries, so a single error
// snapshot must not permanently brand a vend failed. The definitive failed state
// is reserved for the portal-side commit failure.
func (s *ClusterProvisionWatchService) projectTofuPhase(ctx context.Context, op repository.ClusterOperation) {
	building, errMsg := tofuState(s.workspaceConditions(ctx, op))
	if !building {
		return // nothing composed yet — leave the timeline at committed
	}
	s.setVendPhase(ctx, op, "tofu_running", truncate(errMsg, 1000))
}

// projectDeprovisionPhase advances the timeline to 'deprovisioning' while the
// cluster's Workspaces are being torn down, carrying any current tofu-destroy
// error as a regressible detail (same model as the build phase). The XR's
// presence already told reconcileDeprovision we're tearing down, so the phase is
// written regardless of whether the Workspaces are still readable.
func (s *ClusterProvisionWatchService) projectDeprovisionPhase(ctx context.Context, op repository.ClusterOperation) {
	_, errMsg := tofuState(s.workspaceConditions(ctx, op))
	s.setVendPhase(ctx, op, "deprovisioning", truncate(errMsg, 1000))
}

// workspaceConditions reads the .status.conditions of the provider-opentofu
// Workspaces the Cluster XR composes (<name>-stack, <name>-bootstrap) on the
// hub — one slice per Workspace that currently exists. Missing Workspaces (not
// composed yet, or already destroyed) are skipped.
func (s *ClusterProvisionWatchService) workspaceConditions(ctx context.Context, op repository.ClusterOperation) [][]condition {
	var present [][]condition
	for _, suffix := range []string{"stack", "bootstrap"} {
		ws, err := s.hub.Resource(k8s.WorkspaceGVR).Namespace(op.Team).Get(ctx, op.Name+"-"+suffix, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue // not composed yet, or already torn down
		}
		if err != nil {
			slog.Warn("cluster watch-back: get workspace", "workspace", op.Name+"-"+suffix, "error", err)
			continue
		}
		present = append(present, conditions(ws.Object))
	}
	return present
}

// setVendPhase records one timeline checkpoint, best-effort — the status row is
// the verdict, so a projection write must never block the watch-back.
func (s *ClusterProvisionWatchService) setVendPhase(ctx context.Context, op repository.ClusterOperation, phase, detail string) {
	raw, err := vendPhaseFragment(phase, detail, time.Now().UTC())
	if err != nil {
		slog.Warn("provision watch-back: marshal vend phase", "op", op.ID, "error", err)
		return
	}
	if err := s.queries.SetVendPhase(ctx, op.ID, op.OrgID, raw); err != nil {
		slog.Warn("provision watch-back: set vend phase", "op", op.ID, "phase", phase, "error", err)
	}
}

type condition struct {
	condType string
	status   string
	reason   string
	message  string
}

// conditions defensively extracts .status.conditions[] from an unstructured
// object — status may be absent, and conditions missing or oddly shaped while
// Crossplane is mid-reconcile.
func conditions(obj map[string]interface{}) []condition {
	status, ok := obj["status"].(map[string]interface{})
	if !ok {
		return nil
	}
	raw, ok := status["conditions"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]condition, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		var c condition
		if v, ok := m["type"].(string); ok {
			c.condType = v
		}
		if v, ok := m["status"].(string); ok {
			c.status = v
		}
		if v, ok := m["reason"].(string); ok {
			c.reason = v
		}
		if v, ok := m["message"].(string); ok {
			c.message = v
		}
		out = append(out, c)
	}
	return out
}

// reconcileError returns the message of a failed reconcile condition. provider-
// opentofu surfaces a tofu apply error on Synced (sync mode) or LastAsyncOperation
// (async mode), and Ready=False can carry one too; the reason-contains-"Error"
// guard skips normal building states (Creating/Reconciling). The result is the
// CURRENT error, informational — not a terminal verdict, since the provider retries.
func reconcileError(conds []condition) string {
	for _, c := range conds {
		switch c.condType {
		case "Synced", "LastAsyncOperation", "Ready":
		default:
			continue
		}
		if c.status == "False" && strings.Contains(c.reason, "Error") {
			if c.message != "" {
				return c.message
			}
			return c.reason
		}
	}
	return ""
}

// tofuState classifies the build from the conditions of the Workspaces that EXIST
// on the hub (one entry per present Workspace). building becomes true once any
// Workspace is composed; errMsg is the current reconcile error if any (surfaced
// as the running phase's detail, not a terminal failure). Empty input means
// nothing is composed yet, so the timeline stays at committed.
func tofuState(present [][]condition) (building bool, errMsg string) {
	if len(present) == 0 {
		return false, ""
	}
	for _, conds := range present {
		if msg := reconcileError(conds); msg != "" {
			return true, msg
		}
	}
	return true, ""
}

// truncate caps a (possibly multi-byte) string to n runes for storage.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func isUniquePGViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
