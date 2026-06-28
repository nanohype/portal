package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	tofuaws "github.com/nanohype/portal/internal/aws"
	"github.com/nanohype/portal/internal/clusterspec"
	"github.com/nanohype/portal/internal/k8s"
	"github.com/nanohype/portal/internal/repository"
)

// unwedgeExternalID is the ExternalId the fleet-unwedge role's trust policy
// requires (see landing-zone components/aws/fleet-unwedge). It's a fixed string,
// not a per-account secret: the real gate is that the role trusts only portal's
// role ARN and carries a delete-scoped permissions boundary.
const unwedgeExternalID = "eks-fleet-unwedge"

// unwedgePendingAnnotation is crossplane's marker for a managed resource whose
// external create is in flight. If the create call never reports back, it sticks
// — and crossplane then refuses both create and delete, since it can't tell
// whether the external resources exist. That stuck Workspace is the wedge.
const unwedgePendingAnnotation = "crossplane.io/external-create-pending"

// ClusterUnwedgeJobArgs is the River job behind the break-glass teardown. The
// owner-gated handler records a `cluster_operations` row (operation="unwedge")
// carrying the original provision spec, then enqueues this with the row's ID. The
// worker verifies the spoke's Workspace is genuinely wedged, tears its tagged AWS
// resources down through the unwedge role, drops the Workspace finalizers so the
// condemned object garbage-collects, and transitions the row terminal.
type ClusterUnwedgeJobArgs struct {
	OperationID string `json:"operation_id"`
	OrgID       string `json:"org_id"`
}

func (ClusterUnwedgeJobArgs) Kind() string { return "cluster_unwedge" }

func (ClusterUnwedgeJobArgs) InsertOpts() river.InsertOpts {
	// One job per operation; on failure the error lands on the row and the
	// operator decides whether to retry — a teardown must never silently re-run.
	return river.InsertOpts{MaxAttempts: 1}
}

// ClusterPhaseRecorder advances an operation's vend_phases timeline mid-run. Like
// the loader/completer it's a function adapter so the worker doesn't import
// internal/service; cmd/worker wires it to ClusterOrderService.RecordPhase.
type ClusterPhaseRecorder func(ctx context.Context, id, orgID, phase, detail string) error

// UnwedgeProvider assumes the workload account's fleet-unwedge role and returns a
// teardown bound to it. The one method keeps the worker testable and off the SDK.
type UnwedgeProvider interface {
	AssumeTeardown(ctx context.Context, roleARN, externalID, region string) (tofuaws.TeardownAPI, error)
}

type ClusterUnwedgeJobWorker struct {
	river.WorkerDefaults[ClusterUnwedgeJobArgs]
	loadOp      ClusterOperationLoader
	completeOp  ClusterOperationCompleter
	recordPhase ClusterPhaseRecorder
	provider    UnwedgeProvider
	hub         dynamic.Interface // the hub cluster; nil off-cluster (dev)
}

type ClusterUnwedgeDeps struct {
	LoadOp      ClusterOperationLoader
	CompleteOp  ClusterOperationCompleter
	RecordPhase ClusterPhaseRecorder
	Provider    UnwedgeProvider
}

func NewClusterUnwedgeJobWorker(d ClusterUnwedgeDeps) *ClusterUnwedgeJobWorker {
	return &ClusterUnwedgeJobWorker{
		loadOp:      d.LoadOp,
		completeOp:  d.CompleteOp,
		recordPhase: d.RecordPhase,
		provider:    d.Provider,
	}
}

// SetHubClient binds the hub dynamic client once it's built. Off the hub (dev)
// it stays nil and Work fails with a clear message — unwedge can only run where
// the wedged Workspaces live.
func (w *ClusterUnwedgeJobWorker) SetHubClient(hub dynamic.Interface) { w.hub = hub }

func (w *ClusterUnwedgeJobWorker) Timeout(*river.Job[ClusterUnwedgeJobArgs]) time.Duration {
	// The teardown blocks on async EKS/NAT deletes (the engine's per-resource
	// waiters cap at 20m each); give the whole run generous headroom.
	return 45 * time.Minute
}

func (w *ClusterUnwedgeJobWorker) Work(ctx context.Context, job *river.Job[ClusterUnwedgeJobArgs]) error {
	logger := slog.With("job", "cluster_unwedge", "operation_id", job.Args.OperationID, "org_id", job.Args.OrgID)

	op, err := w.loadOp(ctx, job.Args.OperationID, job.Args.OrgID)
	if err != nil {
		return fmt.Errorf("load operation: %w", err)
	}

	if w.hub == nil {
		return w.fail(ctx, op, logger, fmt.Errorf("unwedge requires the worker running in-cluster on the hub"))
	}
	if w.provider == nil {
		return w.fail(ctx, op, logger, fmt.Errorf("aws provider not configured"))
	}

	// The unwedge op carries the original provision spec, the only place the
	// workload account + region live for a spoke that never finished provisioning
	// (so it never became a registered cluster).
	var spec clusterspec.Input
	if len(op.SpecJSON) > 0 {
		if err := json.Unmarshal(op.SpecJSON, &spec); err != nil {
			return w.fail(ctx, op, logger, fmt.Errorf("unmarshal spec: %w", err))
		}
	}
	if spec.Account == "" || spec.Region == "" {
		return w.fail(ctx, op, logger, fmt.Errorf("operation spec has no account/region; cannot locate the workload account"))
	}
	env := spec.EffectiveEnvironment()

	// Safety gate: only ever act on a Workspace the control plane has already
	// condemned and that is genuinely stuck. The stack Workspace is the spoke's
	// substrate; verify it before touching any AWS.
	stack, err := w.hub.Resource(k8s.WorkspaceGVR).Namespace(op.Team).Get(ctx, op.Name+"-stack", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return w.fail(ctx, op, logger, fmt.Errorf("workspace %s-stack not found in %s; nothing to unwedge", op.Name, op.Team))
	}
	if err != nil {
		return w.fail(ctx, op, logger, fmt.Errorf("get workspace %s-stack: %w", op.Name, err))
	}
	if err := verifyWedged(stack); err != nil {
		return w.fail(ctx, op, logger, err)
	}
	w.phase(ctx, op, logger, "verified", "workspace is wedged on external-create-pending and marked for deletion")

	roleARN := fmt.Sprintf("arn:aws:iam::%s:role/eks-fleet/%s-eks-fleet-unwedge", spec.Account, env)
	clusterTag := env + "-" + op.Name

	api, err := w.provider.AssumeTeardown(ctx, roleARN, unwedgeExternalID, spec.Region)
	if err != nil {
		return w.fail(ctx, op, logger, fmt.Errorf("assume unwedge role %s: %w", roleARN, err))
	}

	w.phase(ctx, op, logger, "tearing-down", "deleting tagged AWS resources for "+clusterTag)
	res, err := tofuaws.Teardown(ctx, api, clusterTag)
	if err != nil {
		// The engine stalled or ran out of passes with resources still standing —
		// the operator falls back to the runbook rather than us pretending success.
		return w.fail(ctx, op, logger, fmt.Errorf("teardown %s (%d deleted, %d remaining): %w", clusterTag, res.Deleted, len(res.Remaining), err))
	}
	w.phase(ctx, op, logger, "torn-down", fmt.Sprintf("removed %d AWS resources", res.Deleted))

	// AWS is clean; release the Workspaces so crossplane can complete the delete.
	if err := w.dropFinalizers(ctx, op.Team, op.Name); err != nil {
		return w.fail(ctx, op, logger, fmt.Errorf("drop workspace finalizers: %w", err))
	}

	if err := w.completeOp(ctx, op.ID, op.OrgID, "deprovisioned", "", ""); err != nil {
		logger.Error("complete unwedge operation row", "error", err)
		return fmt.Errorf("complete operation: %w", err)
	}
	logger.Info("cluster unwedged", "cluster", op.Name, "environment", env, "deleted", res.Deleted)
	return nil
}

// verifyWedged is the safety gate. Unwedge only ever touches a Workspace that is
// (a) marked for deletion and (b) stuck on external-create-pending. Requiring the
// deletionTimestamp means dropping the finalizer can only release an object the
// control plane already condemned — never strand or churn one the Cluster XR
// still wants reconciled.
func verifyWedged(ws *unstructured.Unstructured) error {
	if ws.GetDeletionTimestamp() == nil {
		return fmt.Errorf("workspace %s is not being deleted; deprovision the cluster first — unwedge only clears a stuck teardown, it does not start one", ws.GetName())
	}
	if _, pending := ws.GetAnnotations()[unwedgePendingAnnotation]; !pending {
		return fmt.Errorf("workspace %s is not wedged on %s; let the normal teardown run", ws.GetName(), unwedgePendingAnnotation)
	}
	return nil
}

// dropFinalizers clears the finalizers on the spoke's stack + bootstrap
// Workspaces so the (already condemned) objects garbage-collect. The bootstrap
// Workspace only exists for cross-account vends; a missing one is fine.
func (w *ClusterUnwedgeJobWorker) dropFinalizers(ctx context.Context, team, name string) error {
	const patch = `{"metadata":{"finalizers":[]}}`
	for _, suffix := range []string{"stack", "bootstrap"} {
		wsName := name + "-" + suffix
		_, err := w.hub.Resource(k8s.WorkspaceGVR).Namespace(team).
			Patch(ctx, wsName, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("patch %s: %w", wsName, err)
		}
	}
	return nil
}

// phase records a timeline checkpoint, best-effort — the operation's status row
// is the verdict, so a projection-write hiccup must never fail a teardown that
// actually progressed.
func (w *ClusterUnwedgeJobWorker) phase(ctx context.Context, op repository.ClusterOperation, logger *slog.Logger, phase, detail string) {
	if w.recordPhase == nil {
		return
	}
	if err := w.recordPhase(ctx, op.ID, op.OrgID, phase, detail); err != nil {
		logger.Warn("record unwedge phase", "phase", phase, "error", err)
	}
}

func (w *ClusterUnwedgeJobWorker) fail(ctx context.Context, op repository.ClusterOperation, logger *slog.Logger, err error) error {
	logger.Warn("cluster unwedge failed", "error", err)
	if updateErr := w.completeOp(ctx, op.ID, op.OrgID, "failed", "", err.Error()); updateErr != nil {
		logger.Error("record failure on operation row", "error", updateErr)
	}
	// Return nil so River doesn't retry — the failure is on the row, and the
	// operator gets an explicit decision (fix + re-trigger, or fall to the runbook).
	return nil
}
