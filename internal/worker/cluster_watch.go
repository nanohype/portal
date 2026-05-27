package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nanohype/portal/internal/k8s"
	"github.com/nanohype/portal/internal/repository"
)

// ClusterWatchJobArgs walks one cluster's EAP custom resources (currently
// just Tenants) and reconciles the DB inventory. Enqueued periodically from
// cmd/worker/main.go by a dispatch goroutine that ticks every 60s for each
// connected cluster.
type ClusterWatchJobArgs struct {
	ClusterID string `json:"cluster_id"`
	OrgID     string `json:"org_id"`
}

func (ClusterWatchJobArgs) Kind() string { return "cluster_watch" }

func (ClusterWatchJobArgs) InsertOpts() river.InsertOpts {
	// One in-flight watch per cluster at a time; if a tick fires while the
	// last one is still running we just drop the duplicate rather than
	// piling up backlog.
	return river.InsertOpts{
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: 30 * time.Second,
		},
		MaxAttempts: 1,
	}
}

// TenantSnapshot mirrors service.TenantSnapshot but lives here so workers
// don't import service. The reconciler adapter converts.
type TenantSnapshot struct {
	Name   string
	Phase  string
	Spec   json.RawMessage
	Status json.RawMessage
}

// TenantReconciler is the function-type adapter the worker calls to write
// back observed tenants. cmd/worker wires it to TenantService.Reconcile.
type TenantReconciler func(ctx context.Context, orgID, clusterID string, observed []TenantSnapshot) (upserts int, deletes int, err error)

type ClusterWatchJobWorker struct {
	river.WorkerDefaults[ClusterWatchJobArgs]
	queries     *repository.Queries
	decrypt     ClusterDecryptor
	reconcile   TenantReconciler
	riverClient *river.Client[pgx.Tx]
	db          *pgxpool.Pool
}

func NewClusterWatchJobWorker(
	queries *repository.Queries,
	decrypt ClusterDecryptor,
	reconcile TenantReconciler,
) *ClusterWatchJobWorker {
	return &ClusterWatchJobWorker{
		queries:   queries,
		decrypt:   decrypt,
		reconcile: reconcile,
	}
}

func (w *ClusterWatchJobWorker) SetRiverClient(client *river.Client[pgx.Tx], db *pgxpool.Pool) {
	w.riverClient = client
	w.db = db
}

func (w *ClusterWatchJobWorker) Timeout(*river.Job[ClusterWatchJobArgs]) time.Duration {
	return 90 * time.Second
}

func (w *ClusterWatchJobWorker) Work(ctx context.Context, job *river.Job[ClusterWatchJobArgs]) error {
	logger := slog.With(
		"job", "cluster_watch",
		"cluster_id", job.Args.ClusterID,
		"org_id", job.Args.OrgID,
	)

	cluster, err := w.queries.GetCluster(ctx, repository.GetClusterParams{
		ID: job.Args.ClusterID, OrgID: job.Args.OrgID,
	})
	if err != nil {
		return fmt.Errorf("load cluster: %w", err)
	}

	// Skip clusters that aren't connected — the connection-test job is the
	// path that recovers from network/credential failures, not this one.
	if cluster.ConnectionStatus != "connected" {
		logger.Debug("skipping watch: cluster not connected", "status", cluster.ConnectionStatus)
		return nil
	}

	creds, err := w.decrypt(cluster)
	if err != nil {
		logger.Warn("decrypt cluster credentials", "error", err)
		return fmt.Errorf("decrypt: %w", err)
	}

	dynClient, err := k8s.BuildDynamicClient(creds)
	if err != nil {
		logger.Warn("build dynamic client", "error", err)
		return fmt.Errorf("dynamic client: %w", err)
	}

	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	list, err := dynClient.Resource(k8s.TenantGVR).List(listCtx, metav1.ListOptions{})
	if err != nil {
		logger.Warn("list tenants", "error", err)
		return fmt.Errorf("list tenants: %w", err)
	}

	observed := make([]TenantSnapshot, 0, len(list.Items))
	for _, item := range list.Items {
		spec, _ := json.Marshal(item.Object["spec"])
		status, _ := json.Marshal(item.Object["status"])

		// .status.phase isn't a CRD invariant — extract defensively. If it's
		// missing or malformed we just record "" and let the UI fall back to
		// an "Unknown" badge.
		var phase string
		if s, ok := item.Object["status"].(map[string]interface{}); ok {
			if p, ok := s["phase"].(string); ok {
				phase = p
			}
		}

		observed = append(observed, TenantSnapshot{
			Name:   item.GetName(),
			Phase:  phase,
			Spec:   spec,
			Status: status,
		})
	}

	upserts, deletes, err := w.reconcile(ctx, job.Args.OrgID, job.Args.ClusterID, observed)
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	logger.Info("watch tick complete", "observed", len(observed), "upserts", upserts, "deletes", deletes)
	return nil
}
