package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/clusterspec"
	"github.com/nanohype/portal/internal/git"
	"github.com/nanohype/portal/internal/repository"
)

// ClusterApplyJobArgs is the River job that drives the clusters-repo write path
// (the cluster-vend order desk). The handler creates a `cluster_operations` row
// in `pending` and enqueues this with the row's ID; the worker loads the row,
// templates the eks-fleet Cluster CR from its spec_json (provision) or removes
// the manifest (deprovision), commits + pushes, and transitions the row to
// `committed` or `failed`. The hub's ArgoCD then reconciles the CR.
type ClusterApplyJobArgs struct {
	OperationID string `json:"operation_id"`
	OrgID       string `json:"org_id"`
}

func (ClusterApplyJobArgs) Kind() string { return "cluster_apply" }

func (ClusterApplyJobArgs) InsertOpts() river.InsertOpts {
	// One job per operation; on failure we record the error on the row and let
	// the user click "Retry" rather than silently re-running.
	return river.InsertOpts{MaxAttempts: 1}
}

// ClusterOperationLoader / ClusterOperationCompleter are function-type adapters
// so the worker doesn't import internal/service. cmd/worker wires them to
// ClusterOrderService methods.
type ClusterOperationLoader func(ctx context.Context, id, orgID string) (repository.ClusterOperation, error)
type ClusterOperationCompleter func(ctx context.Context, id, orgID, status, sha, errMsg string) error

type ClusterApplyJobWorker struct {
	river.WorkerDefaults[ClusterApplyJobArgs]
	loadOp         ClusterOperationLoader
	completeOp     ClusterOperationCompleter
	clustersRepo   *git.Repo
	repoMu         *sync.Mutex
	clustersRef    string
	author         git.Author
	hubRoleArn     string
	tenantsRepoURL string
	riverClient    *river.Client[pgx.Tx]
	db             *pgxpool.Pool
}

// ClusterApplyDeps bundles the shared infrastructure the worker needs. RepoMu
// serializes write access to clustersRepo (one workdir on disk). It can be the
// SAME mutex the tenant-apply worker uses if they share a process — both write
// distinct repos, so a shared lock is merely conservative, not required.
type ClusterApplyDeps struct {
	LoadOp       ClusterOperationLoader
	CompleteOp   ClusterOperationCompleter
	ClustersRepo *git.Repo
	RepoMu       *sync.Mutex
	ClustersRef  string // branch in clusters repo (typically "main")
	Author       git.Author
	HubRoleArn   string // eks-fleet-crossplane role ARN; stamped onto cross-account vends
	// TenantsRepoURL is the tenants GitOps repo portal commits tenant manifests
	// to — the same GITOPS_TENANTS_REPO_URL the tenant-apply worker pushes to.
	// Stamped onto every vend so the cluster's ArgoCD can pull them back.
	TenantsRepoURL string
}

func NewClusterApplyJobWorker(d ClusterApplyDeps) *ClusterApplyJobWorker {
	ref := d.ClustersRef
	if ref == "" {
		ref = "main"
	}
	return &ClusterApplyJobWorker{
		loadOp:         d.LoadOp,
		completeOp:     d.CompleteOp,
		clustersRepo:   d.ClustersRepo,
		repoMu:         d.RepoMu,
		clustersRef:    ref,
		author:         d.Author,
		hubRoleArn:     d.HubRoleArn,
		tenantsRepoURL: d.TenantsRepoURL,
	}
}

func (w *ClusterApplyJobWorker) SetRiverClient(client *river.Client[pgx.Tx], db *pgxpool.Pool) {
	w.riverClient = client
	w.db = db
}

func (w *ClusterApplyJobWorker) Timeout(*river.Job[ClusterApplyJobArgs]) time.Duration {
	return 3 * time.Minute
}

// provisionManifest turns an ordered spec into the Cluster CR that gets
// committed. It is a method rather than three lines inside Work so the stamps
// can be tested: neither one changes anything the order desk can see, and both
// produce a cluster that comes up healthy and is quietly missing a capability —
// which is not a failure any test of Work's happy path would notice.
//
// Deleting the call from Work is a compile error rather than a silent
// regression, which is the other half of that.
func (w *ClusterApplyJobWorker) provisionManifest(specJSON []byte) (string, error) {
	var input clusterspec.Input
	if len(specJSON) > 0 {
		if err := json.Unmarshal(specJSON, &input); err != nil {
			return "", fmt.Errorf("unmarshal spec: %w", err)
		}
	}
	// cross-account vends need the hub role trusted on the spoke (see
	// clusterspec.WithCrossAccountBootstrap); same-account is a no-op.
	input = input.WithCrossAccountBootstrap(w.hubRoleArn)
	// The tenants repo is portal's own, so it is stamped here rather than
	// ordered. The spoke-role half is stamped at enqueue, where the ordering
	// account's row is already in hand.
	input = input.WithPortalWiring("", w.tenantsRepoURL)

	manifest, err := input.Render()
	if err != nil {
		return "", fmt.Errorf("render Cluster CR: %w", err)
	}
	return manifest, nil
}

func (w *ClusterApplyJobWorker) Work(ctx context.Context, job *river.Job[ClusterApplyJobArgs]) error {
	logger := slog.With(
		"job", "cluster_apply",
		"operation_id", job.Args.OperationID,
		"org_id", job.Args.OrgID,
	)

	if w.clustersRepo == nil {
		return w.fail(ctx, job.Args.OperationID, job.Args.OrgID, logger,
			fmt.Errorf("clusters repo not configured (set GITOPS_CLUSTERS_REPO_URL + GITOPS_SSH_KEY_PATH)"))
	}

	op, err := w.loadOp(ctx, job.Args.OperationID, job.Args.OrgID)
	if err != nil {
		return fmt.Errorf("load operation: %w", err)
	}

	w.repoMu.Lock()
	defer w.repoMu.Unlock()

	if err := w.clustersRepo.CloneOrPull(ctx, w.clustersRef); err != nil {
		return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("sync clusters repo: %w", err))
	}

	relPath := clusterManifestPath(op.Environment, op.Name)
	var commitMsg string
	switch op.Operation {
	case "provision":
		manifest, err := w.provisionManifest(op.SpecJSON)
		if err != nil {
			return w.fail(ctx, op.ID, op.OrgID, logger, err)
		}
		if err := w.clustersRepo.WriteFile(relPath, []byte(manifest)); err != nil {
			return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("write manifest: %w", err))
		}
		commitMsg = fmt.Sprintf("cluster: provision %s (%s)\n\nWritten by portal on behalf of %s (operation %s).",
			op.Name, op.Environment, op.CreatedBy, op.ID)
	case "deprovision":
		removed, err := w.clustersRepo.RemoveFile(relPath)
		if err != nil {
			return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("remove manifest: %w", err))
		}
		if !removed {
			// Nothing at that path means portal cannot tear this cluster down —
			// the manifest it would delete is the only thing that makes ArgoCD
			// prune the Cluster CR and Crossplane run destroy. Left to run, the
			// commit finds a clean tree, the op completes, and the watch-back
			// reads the absent XR as "teardown complete": a green timeline over
			// an EKS cluster that is still running and still billing. Fail
			// instead, naming the path, so the gap is visible.
			return w.fail(ctx, op.ID, op.OrgID, logger,
				fmt.Errorf("no manifest at %s: nothing was torn down; the cluster may still be running under a different name or environment", relPath))
		}
		commitMsg = fmt.Sprintf("cluster: deprovision %s (%s)\n\nDeleted by portal on behalf of %s (operation %s).",
			op.Name, op.Environment, op.CreatedBy, op.ID)
	default:
		return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("unknown operation kind: %s", op.Operation))
	}

	sha, err := w.clustersRepo.Commit(commitMsg, w.author)
	if err != nil {
		return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("commit: %w", err))
	}
	if sha == "" {
		logger.Info("cluster apply was a no-op (working tree clean)", "operation", op.Operation, "cluster", op.Name)
		if err := w.completeOp(ctx, op.ID, op.OrgID, "committed", "", ""); err != nil {
			logger.Error("complete no-op operation", "error", err)
		}
		return nil
	}

	if err := w.clustersRepo.Push(ctx); err != nil {
		return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("push: %w", err))
	}

	if err := w.completeOp(ctx, op.ID, op.OrgID, "committed", sha, ""); err != nil {
		logger.Error("complete operation row", "error", err)
		return fmt.Errorf("complete operation: %w", err)
	}
	logger.Info("cluster apply succeeded", "operation", op.Operation, "cluster", op.Name, "sha", sha)
	return nil
}

func (w *ClusterApplyJobWorker) fail(ctx context.Context, opID, orgID string, logger *slog.Logger, err error) error {
	logger.Warn("cluster apply failed", "error", err)
	// Job ctx may already be cancelled (SIGTERM / River timeout). Write status
	// on a detached context so the op does not sit at pending forever.
	writeCtx, cancel := durableContext(ctx)
	defer cancel()
	if updateErr := w.completeOp(writeCtx, opID, orgID, "failed", "", err.Error()); updateErr != nil {
		logger.Error("record failure on operation row", "error", updateErr)
	}
	// Return nil so River doesn't retry — the failure is on the row, and the user
	// gets an explicit "Retry" affordance.
	return nil
}

// clusterManifestPath is where a cluster's CR lives in the clusters repo. Env +
// name are sanitized (git.Repo.WriteFile rejects traversal anyway). Reuses
// sanitizePathSegment from tenant_apply.go (same package).
func clusterManifestPath(environment, name string) string {
	return path.Join("clusters", sanitizePathSegment(environment), sanitizePathSegment(name)+".yaml")
}
