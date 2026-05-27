package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/stxkxs/tofui/internal/git"
	"github.com/stxkxs/tofui/internal/repository"
)

// TenantApplyJobArgs is the River job that drives the tenants-repo write
// path. The handler creates a `tenant_operations` row in `pending` and
// enqueues this with the row's ID; the worker loads the row, renders or
// removes the rendered manifest, commits + pushes, and transitions the row
// to `committed` or `failed`.
type TenantApplyJobArgs struct {
	OperationID string `json:"operation_id"`
	OrgID       string `json:"org_id"`
}

func (TenantApplyJobArgs) Kind() string { return "tenant_apply" }

func (TenantApplyJobArgs) InsertOpts() river.InsertOpts {
	// Each operation maps to exactly one job; if the job fails we record
	// the error in the row and let the user click "Retry" rather than
	// have River silently re-run with the same inputs.
	return river.InsertOpts{MaxAttempts: 1}
}

// TenantOperationCompleter is a function-type adapter so the worker doesn't
// import internal/service. cmd/worker wires it to TenantService methods.
type TenantOperationLoader func(ctx context.Context, id, orgID string) (repository.TenantOperation, error)
type TenantOperationCompleter func(ctx context.Context, id, orgID, status, sha, errMsg string) error

// ChartRenderer abstracts the helm render call. The caller's adapter loads
// the named chart from the cache and renders against the supplied values.
type ChartRenderer func(chartName, releaseName, namespace string, values map[string]interface{}) (string, error)

type TenantApplyJobWorker struct {
	river.WorkerDefaults[TenantApplyJobArgs]
	queries     *repository.Queries
	loadOp      TenantOperationLoader
	completeOp  TenantOperationCompleter
	render      ChartRenderer
	tenantsRepo *git.Repo
	repoMu      *sync.Mutex
	tenantsRef  string
	author      git.Author
	riverClient *river.Client[pgx.Tx]
	db          *pgxpool.Pool
}

// TenantApplyDeps bundles the shared infrastructure the worker needs.
// `repoMu` serializes write access to `tenantsRepo` (one workdir on disk;
// concurrent operations would step on each other's commits). All workers
// share the single mutex so creates + deletes interleave safely.
type TenantApplyDeps struct {
	Queries     *repository.Queries
	LoadOp      TenantOperationLoader
	CompleteOp  TenantOperationCompleter
	Render      ChartRenderer
	TenantsRepo *git.Repo
	RepoMu      *sync.Mutex
	TenantsRef  string // branch in tenants repo (typically "main")
	Author      git.Author
}

func NewTenantApplyJobWorker(d TenantApplyDeps) *TenantApplyJobWorker {
	ref := d.TenantsRef
	if ref == "" {
		ref = "main"
	}
	return &TenantApplyJobWorker{
		queries:     d.Queries,
		loadOp:      d.LoadOp,
		completeOp:  d.CompleteOp,
		render:      d.Render,
		tenantsRepo: d.TenantsRepo,
		repoMu:      d.RepoMu,
		tenantsRef:  ref,
		author:      d.Author,
	}
}

func (w *TenantApplyJobWorker) SetRiverClient(client *river.Client[pgx.Tx], db *pgxpool.Pool) {
	w.riverClient = client
	w.db = db
}

func (w *TenantApplyJobWorker) Timeout(*river.Job[TenantApplyJobArgs]) time.Duration {
	return 3 * time.Minute
}

func (w *TenantApplyJobWorker) Work(ctx context.Context, job *river.Job[TenantApplyJobArgs]) error {
	logger := slog.With(
		"job", "tenant_apply",
		"operation_id", job.Args.OperationID,
		"org_id", job.Args.OrgID,
	)

	if w.tenantsRepo == nil {
		return w.fail(ctx, job.Args.OperationID, job.Args.OrgID, logger,
			fmt.Errorf("tenants repo not configured (set GITOPS_TENANTS_REPO_URL + GITOPS_SSH_KEY_PATH)"))
	}

	op, err := w.loadOp(ctx, job.Args.OperationID, job.Args.OrgID)
	if err != nil {
		return fmt.Errorf("load operation: %w", err)
	}

	cluster, err := w.queries.GetCluster(ctx, repository.GetClusterParams{
		ID: op.ClusterID, OrgID: op.OrgID,
	})
	if err != nil {
		return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("load cluster: %w", err))
	}

	// Single global lock: only one tenant-apply job at a time touches the
	// tenants workdir. The lock is cheap (each op is ~1s) and avoids
	// fighting over the disk + git index.
	w.repoMu.Lock()
	defer w.repoMu.Unlock()

	if err := w.tenantsRepo.CloneOrPull(ctx, w.tenantsRef); err != nil {
		return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("sync tenants repo: %w", err))
	}

	relPath := tenantManifestPath(cluster.Name, op.TenantName)
	var commitMsg string
	switch op.Operation {
	case "create":
		var values map[string]interface{}
		if len(op.ValuesJSON) > 0 {
			if err := json.Unmarshal(op.ValuesJSON, &values); err != nil {
				return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("unmarshal values: %w", err))
			}
		}
		manifest, err := w.render("tenant", op.TenantName, op.TenantName, values)
		if err != nil {
			return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("render chart: %w", err))
		}
		if err := w.tenantsRepo.WriteFile(relPath, []byte(manifest)); err != nil {
			return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("write manifest: %w", err))
		}
		commitMsg = fmt.Sprintf("tenant: create %s on %s\n\nWritten by tofui on behalf of %s (operation %s).",
			op.TenantName, cluster.Name, op.CreatedBy, op.ID)
	case "delete":
		if err := w.tenantsRepo.RemoveFile(relPath); err != nil {
			return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("remove manifest: %w", err))
		}
		commitMsg = fmt.Sprintf("tenant: delete %s from %s\n\nDeleted by tofui on behalf of %s (operation %s).",
			op.TenantName, cluster.Name, op.CreatedBy, op.ID)
	default:
		return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("unknown operation kind: %s", op.Operation))
	}

	sha, err := w.tenantsRepo.Commit(commitMsg, w.author)
	if err != nil {
		return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("commit: %w", err))
	}
	if sha == "" {
		// Clean working tree — nothing changed. Treat as success (idempotency).
		logger.Info("tenant apply was a no-op (working tree clean)", "operation", op.Operation, "tenant", op.TenantName)
		if err := w.completeOp(ctx, op.ID, op.OrgID, "committed", "", ""); err != nil {
			logger.Error("complete no-op operation", "error", err)
		}
		return nil
	}

	if err := w.tenantsRepo.Push(ctx); err != nil {
		return w.fail(ctx, op.ID, op.OrgID, logger, fmt.Errorf("push: %w", err))
	}

	if err := w.completeOp(ctx, op.ID, op.OrgID, "committed", sha, ""); err != nil {
		logger.Error("complete operation row", "error", err)
		return fmt.Errorf("complete operation: %w", err)
	}
	logger.Info("tenant apply succeeded", "operation", op.Operation, "tenant", op.TenantName, "sha", sha)
	return nil
}

func (w *TenantApplyJobWorker) fail(ctx context.Context, opID, orgID string, logger *slog.Logger, err error) error {
	logger.Warn("tenant apply failed", "error", err)
	if updateErr := w.completeOp(ctx, opID, orgID, "failed", "", err.Error()); updateErr != nil {
		logger.Error("record failure on operation row", "error", updateErr)
	}
	// Return nil so River doesn't retry — the failure is already in the
	// operation row, and the user gets an explicit "Retry" affordance.
	return nil
}

// tenantManifestPath is the canonical place a tenant's rendered manifest
// lives in the tenants repo. Cluster name + tenant name are sanitized to
// avoid path traversal — git.Repo.WriteFile rejects those anyway, but
// sanitizing here means we never even attempt a bad write.
func tenantManifestPath(clusterName, tenantName string) string {
	return path.Join("tenants", sanitizePathSegment(clusterName), sanitizePathSegment(tenantName)+".yaml")
}

// sanitizePathSegment strips characters that don't belong in a filesystem
// path. Conservative allowlist: lowercase ASCII alphanumeric, hyphen, dot,
// underscore. Any other byte becomes '-'. Empty inputs become "unknown" so
// callers always get a valid segment back.
func sanitizePathSegment(s string) string {
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "unknown"
	}
	return out
}
