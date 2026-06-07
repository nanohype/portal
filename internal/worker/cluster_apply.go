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
	loadOp       ClusterOperationLoader
	completeOp   ClusterOperationCompleter
	clustersRepo *git.Repo
	repoMu       *sync.Mutex
	clustersRef  string
	author       git.Author
	hubRoleArn   string
	riverClient  *river.Client[pgx.Tx]
	db           *pgxpool.Pool
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
}

func NewClusterApplyJobWorker(d ClusterApplyDeps) *ClusterApplyJobWorker {
	ref := d.ClustersRef
	if ref == "" {
		ref = "main"
	}
	return &ClusterApplyJobWorker{
		loadOp:       d.LoadOp,
		completeOp:   d.CompleteOp,
		clustersRepo: d.ClustersRepo,
		repoMu:       d.RepoMu,
		clustersRef:  ref,
		author:       d.Author,
		hubRoleArn:   d.HubRoleArn,
	}
}

func (w *ClusterApplyJobWorker) SetRiverClient(client *river.Client[pgx.Tx], db *pgxpool.Pool) {
	w.riverClient = client
	w.db = db
}

func (w *ClusterApplyJobWorker) Timeout(*river.Job[ClusterApplyJobArgs]) time.Duration {
	return 3 * time.Minute
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
		var input clusterspec.Input
		if len(op.SpecJSON) > 0 {
			if err := json.Unmarshal(op.SpecJSON, &input); err != nil {
				return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("unmarshal spec: %w", err))
			}
		}
		// cross-account vends need the hub role trusted on the spoke (see
		// clusterspec.WithCrossAccountBootstrap); same-account is a no-op.
		input = input.WithCrossAccountBootstrap(w.hubRoleArn)
		manifest, err := input.Render()
		if err != nil {
			return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("render Cluster CR: %w", err))
		}
		if err := w.clustersRepo.WriteFile(relPath, []byte(manifest)); err != nil {
			return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("write manifest: %w", err))
		}
		commitMsg = fmt.Sprintf("cluster: provision %s (%s)\n\nWritten by portal on behalf of %s (operation %s).",
			op.Name, op.Environment, op.CreatedBy, op.ID)
	case "deprovision":
		if err := w.clustersRepo.RemoveFile(relPath); err != nil {
			return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("remove manifest: %w", err))
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
	if updateErr := w.completeOp(ctx, opID, orgID, "failed", "", err.Error()); updateErr != nil {
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
