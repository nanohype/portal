package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/stxkxs/tofui/internal/aws"
	"github.com/stxkxs/tofui/internal/k8s"
	"github.com/stxkxs/tofui/internal/repository"
)

// ClusterConnectionTestJobArgs is the River job arg for verifying that
// stored cluster credentials still work. Enqueued by the cluster handler on
// create/update and from the manual "Test connection" endpoint. The worker
// transitions connection_status pending → connecting → connected/failed.
type ClusterConnectionTestJobArgs struct {
	ClusterID string `json:"cluster_id"`
	OrgID     string `json:"org_id"`
}

func (ClusterConnectionTestJobArgs) Kind() string { return "cluster_connection_test" }

func (ClusterConnectionTestJobArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 3,
	}
}

// ClusterDecryptor + ClusterStatusUpdater are function types to avoid
// importing internal/service from this package (same approach
// PipelineStageJobWorker uses to avoid the cycle). The cmd/worker wiring
// supplies adapters.
type ClusterDecryptor func(c repository.Cluster) (k8s.SlimConfig, error)
type ClusterStatusUpdater func(ctx context.Context, id, orgID, status, errMsg, k8sVersion string, nodeCount int32) error

type ClusterConnectionTestJobWorker struct {
	river.WorkerDefaults[ClusterConnectionTestJobArgs]
	queries     *repository.Queries
	decrypt     ClusterDecryptor
	updateState ClusterStatusUpdater
	aws         *aws.Provider
	k8s         *k8s.ClientCache
	riverClient *river.Client[pgx.Tx]
	db          *pgxpool.Pool
}

func NewClusterConnectionTestJobWorker(
	queries *repository.Queries,
	decrypt ClusterDecryptor,
	updateState ClusterStatusUpdater,
	awsProvider *aws.Provider,
	k8sCache *k8s.ClientCache,
) *ClusterConnectionTestJobWorker {
	return &ClusterConnectionTestJobWorker{
		queries:     queries,
		decrypt:     decrypt,
		updateState: updateState,
		aws:         awsProvider,
		k8s:         k8sCache,
	}
}

func (w *ClusterConnectionTestJobWorker) SetRiverClient(client *river.Client[pgx.Tx], db *pgxpool.Pool) {
	w.riverClient = client
	w.db = db
}

func (w *ClusterConnectionTestJobWorker) Timeout(*river.Job[ClusterConnectionTestJobArgs]) time.Duration {
	return 2 * time.Minute
}

func (w *ClusterConnectionTestJobWorker) Work(ctx context.Context, job *river.Job[ClusterConnectionTestJobArgs]) error {
	logger := slog.With(
		"job", "cluster_connection_test",
		"cluster_id", job.Args.ClusterID,
		"org_id", job.Args.OrgID,
	)

	cluster, err := w.queries.GetCluster(ctx, repository.GetClusterParams{
		ID: job.Args.ClusterID, OrgID: job.Args.OrgID,
	})
	if err != nil {
		logger.Error("load cluster", "error", err)
		return fmt.Errorf("load cluster: %w", err)
	}

	// Move to "connecting" so the UI shows progress immediately. Failures
	// after this point set the status to "failed"; success sets it to
	// "connected" alongside the captured summary.
	if err := w.updateState(ctx, cluster.ID, cluster.OrgID, "connecting", "", "", 0); err != nil {
		logger.Error("set status to connecting", "error", err)
		// Continue anyway — the actual probe is the load-bearing work
	}

	account, err := w.queries.GetAccount(ctx, repository.GetAccountParams{
		ID: cluster.AccountID, OrgID: cluster.OrgID,
	})
	if err != nil {
		return w.fail(ctx, cluster, logger, fmt.Errorf("load parent account: %w", err))
	}

	// Prove the cross-account assume-role works. If this fails the k8s probe
	// will too (and with a less useful error message), so check it first.
	if w.aws != nil {
		if _, err := w.aws.VerifyAssumeRole(ctx, account.AssumeRoleARN, "", account.DefaultRegion); err != nil {
			return w.fail(ctx, cluster, logger, fmt.Errorf("aws assume-role failed: %w", err))
		}
	} else {
		logger.Warn("aws provider not configured; skipping sts verification")
	}

	creds, err := w.decrypt(cluster)
	if err != nil {
		return w.fail(ctx, cluster, logger, fmt.Errorf("decrypt cluster credentials: %w", err))
	}

	client, err := w.k8s.Get(cluster.ID, creds)
	if err != nil {
		return w.fail(ctx, cluster, logger, fmt.Errorf("build kubernetes client: %w", err))
	}

	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	summary, err := k8s.Probe(probeCtx, client)
	if err != nil {
		return w.fail(ctx, cluster, logger, fmt.Errorf("probe cluster: %w", err))
	}

	if err := w.updateState(ctx, cluster.ID, cluster.OrgID, "connected", "", summary.ServerVersion, int32(summary.NodeCount)); err != nil {
		logger.Error("set status to connected", "error", err)
		return fmt.Errorf("update cluster status: %w", err)
	}
	logger.Info("cluster reachable", "k8s_version", summary.ServerVersion, "nodes", summary.NodeCount)
	return nil
}

func (w *ClusterConnectionTestJobWorker) fail(ctx context.Context, cluster repository.Cluster, logger *slog.Logger, err error) error {
	logger.Warn("connection test failed", "error", err)
	if updateErr := w.updateState(ctx, cluster.ID, cluster.OrgID, "failed", err.Error(), "", 0); updateErr != nil {
		logger.Error("set status to failed", "error", updateErr)
	}
	// Returning nil prevents River from retrying — we've already recorded the
	// failure in the cluster row. The user can re-trigger via the UI when
	// they've fixed whatever was wrong.
	return nil
}
