package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/stxkxs/tofui/internal/repository"
	"github.com/stxkxs/tofui/internal/storage"
)

// PipelineStageJobArgs is the River job argument for processing a pipeline stage.
type PipelineStageJobArgs struct {
	PipelineRunID string `json:"pipeline_run_id"`
	StageOrder    int32  `json:"stage_order"`
	OrgID         string `json:"org_id"`
	CreatedBy     string `json:"created_by"`
}

func (PipelineStageJobArgs) Kind() string { return "pipeline_stage" }

func (PipelineStageJobArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:    "default",
		Priority: 2,
	}
}

// RunCreatorFunc creates a workspace run. Avoids import cycle with service package.
type RunCreatorFunc func(ctx context.Context, workspaceID, orgID, operation, createdBy string, autoApplyOverride *bool) (repository.Run, error)

// OutputImporter is a function that imports outputs between workspaces.
type OutputImporter func(ctx context.Context, queries *repository.Queries, store *storage.S3Storage, sourceWorkspaceID, targetWorkspaceID, orgID string) error

type PipelineStageJobWorker struct {
	river.WorkerDefaults[PipelineStageJobArgs]
	queries       *repository.Queries
	createRun     RunCreatorFunc
	importOutputs OutputImporter
	storage       *storage.S3Storage
	riverClient   *river.Client[pgx.Tx]
	db            *pgxpool.Pool
}

func NewPipelineStageJobWorker(queries *repository.Queries, createRun RunCreatorFunc, importOutputs OutputImporter, store *storage.S3Storage) *PipelineStageJobWorker {
	return &PipelineStageJobWorker{
		queries:       queries,
		createRun:     createRun,
		importOutputs: importOutputs,
		storage:       store,
	}
}

func (w *PipelineStageJobWorker) SetRiverClient(client *river.Client[pgx.Tx], db *pgxpool.Pool) {
	w.riverClient = client
	w.db = db
}

func (w *PipelineStageJobWorker) Timeout(*river.Job[PipelineStageJobArgs]) time.Duration {
	return 5 * time.Minute
}

func (w *PipelineStageJobWorker) Work(ctx context.Context, job *river.Job[PipelineStageJobArgs]) error {
	args := job.Args
	logger := slog.With("pipeline_run_id", args.PipelineRunID, "stage_order", args.StageOrder)
	logger.Info("processing pipeline stage")

	// Get pipeline run
	pr, err := w.queries.GetPipelineRun(ctx, repository.GetPipelineRunParams{ID: args.PipelineRunID, OrgID: args.OrgID})
	if err != nil {
		return fmt.Errorf("get pipeline run: %w", err)
	}

	// Check if pipeline was cancelled
	if pr.Status != "running" {
		logger.Info("pipeline run not running, skipping stage", "status", pr.Status)
		return nil
	}

	// Get this stage
	stage, err := w.queries.GetPipelineRunStageByOrder(ctx, args.PipelineRunID, args.StageOrder)
	if err != nil {
		return fmt.Errorf("get pipeline run stage: %w", err)
	}

	// If stage already processed, skip
	if stage.Status != "pending" {
		logger.Info("stage already processed, skipping", "status", stage.Status)
		return nil
	}

	// Import outputs from previous stage if not the first stage
	if args.StageOrder > 0 {
		if _, err := w.queries.StartPipelineRunStage(ctx, stage.ID, "importing_outputs"); err != nil {
			return fmt.Errorf("update stage status to importing_outputs: %w", err)
		}

		prevStage, err := w.queries.GetPipelineRunStageByOrder(ctx, args.PipelineRunID, args.StageOrder-1)
		if err != nil {
			w.failStage(ctx, stage, pr, logger, fmt.Errorf("get previous stage: %w", err))
			return nil
		}

		if w.importOutputs != nil {
			if err := w.importOutputs(ctx, w.queries, w.storage, prevStage.WorkspaceID, stage.WorkspaceID, args.OrgID); err != nil {
				logger.Warn("output import failed (continuing)", "error", err)
			}
		}
	}

	// Update pipeline run current stage
	w.queries.UpdatePipelineRunStatus(ctx, repository.UpdatePipelineRunStatusParams{
		ID: args.PipelineRunID, Status: "running", CurrentStage: args.StageOrder,
	})

	// Create workspace run
	run, err := w.createRun(ctx, stage.WorkspaceID, args.OrgID, "plan", args.CreatedBy, &stage.AutoApply)
	if err != nil {
		w.failStage(ctx, stage, pr, logger, fmt.Errorf("create workspace run: %w", err))
		return nil
	}

	// Link run to stage
	if err := w.queries.SetPipelineRunStageRunID(ctx, stage.ID, run.ID); err != nil {
		return fmt.Errorf("set stage run_id: %w", err)
	}

	logger.Info("pipeline stage run created", "run_id", run.ID, "workspace_id", stage.WorkspaceID)
	return nil
}

func (w *PipelineStageJobWorker) failStage(ctx context.Context, stage repository.PipelineRunStage, pr repository.PipelineRun, logger *slog.Logger, err error) {
	logger.Error("pipeline stage failed", "error", err)

	w.queries.FinishPipelineRunStage(ctx, stage.ID, "errored")

	if stage.OnFailure == "continue" {
		w.enqueueNextStage(ctx, pr, stage.StageOrder, logger)
	} else {
		w.queries.CancelPendingPipelineRunStages(ctx, pr.ID)
		w.queries.FinishPipelineRun(ctx, pr.ID, "errored")
	}
}

func (w *PipelineStageJobWorker) enqueueNextStage(ctx context.Context, pr repository.PipelineRun, currentOrder int32, logger *slog.Logger) {
	nextOrder := currentOrder + 1
	if nextOrder >= pr.TotalStages {
		w.queries.FinishPipelineRun(ctx, pr.ID, "completed")
		logger.Info("pipeline completed")
		return
	}

	if w.riverClient == nil || w.db == nil {
		return
	}

	tx, err := w.db.Begin(ctx)
	if err != nil {
		logger.Error("failed to begin tx for next pipeline stage", "error", err)
		return
	}
	defer tx.Rollback(ctx)

	_, err = w.riverClient.InsertTx(ctx, tx, PipelineStageJobArgs{
		PipelineRunID: pr.ID,
		StageOrder:    nextOrder,
		OrgID:         pr.OrgID,
		CreatedBy:     pr.CreatedBy,
	}, nil)
	if err != nil {
		logger.Error("failed to enqueue next pipeline stage", "error", err, "next_order", nextOrder)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("failed to commit next pipeline stage", "error", err)
		return
	}

	logger.Info("enqueued next pipeline stage", "next_order", nextOrder)
}
