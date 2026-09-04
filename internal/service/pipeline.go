package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/conv"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker"
)

// pipelineCancelStore is the reads and writes cancelling a pipeline run makes.
//
// It is an interface because every one of these writes settles a row that
// nothing else sweeps: a stage or a pipeline left non-terminal under a cancelled
// run stays that way, and GetActivePipelineRunForPipeline then refuses every
// later run of the pipeline. A concrete *repository.Queries puts the whole
// function behind a database, and a gate that skips when the database is absent
// reports green for a rule it did not check.
type pipelineCancelStore interface {
	GetPipelineRun(ctx context.Context, arg repository.GetPipelineRunParams) (repository.PipelineRun, error)
	ListPipelineRunStages(ctx context.Context, pipelineRunID string) ([]repository.PipelineRunStageWithWorkspace, error)
	FinishPipelineRunStage(ctx context.Context, id, status string) (repository.PipelineRunStage, error)
	CancelPendingPipelineRunStages(ctx context.Context, pipelineRunID string) error
	FinishPipelineRun(ctx context.Context, id, status string) (repository.PipelineRun, error)
}

// runCanceller stops the workspace run a live stage is waiting on.
type runCanceller interface {
	Cancel(ctx context.Context, runID, workspaceID, orgID string) (repository.Run, error)
}

type PipelineService struct {
	queries     *repository.Queries
	db          *pgxpool.Pool
	runSvc      *RunService
	riverClient *river.Client[pgx.Tx]

	cancels   pipelineCancelStore
	cancelRun runCanceller
}

func NewPipelineService(queries *repository.Queries, db *pgxpool.Pool, runSvc *RunService) *PipelineService {
	s := &PipelineService{queries: queries, db: db, runSvc: runSvc, cancels: queries}
	if runSvc != nil {
		s.cancelRun = runSvc
	}
	return s
}

func (s *PipelineService) SetRiverClient(client *river.Client[pgx.Tx]) {
	s.riverClient = client
}

type CreatePipelineStageInput struct {
	WorkspaceID string `json:"workspace_id"`
	AutoApply   bool   `json:"auto_apply"`
	OnFailure   string `json:"on_failure"`
}

func (s *PipelineService) Create(ctx context.Context, orgID, name, description, createdBy string, stages []CreatePipelineStageInput) (repository.Pipeline, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return repository.Pipeline{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	txq := s.queries.WithTx(tx)

	pipeline, err := txq.CreatePipeline(ctx, repository.CreatePipelineParams{
		ID:          ulid.Make().String(),
		OrgID:       orgID,
		Name:        name,
		Description: description,
		CreatedBy:   createdBy,
	})
	if err != nil {
		return repository.Pipeline{}, fmt.Errorf("create pipeline: %w", err)
	}

	for i, stage := range stages {
		if err := s.stageWorkspaceInOrg(ctx, txq, stage.WorkspaceID, orgID); err != nil {
			return repository.Pipeline{}, err
		}
		onFailure := stage.OnFailure
		if onFailure == "" {
			onFailure = "stop"
		}
		_, err := txq.CreatePipelineStage(ctx, repository.CreatePipelineStageParams{
			ID:          ulid.Make().String(),
			PipelineID:  pipeline.ID,
			WorkspaceID: stage.WorkspaceID,
			StageOrder:  int32(i),
			AutoApply:   stage.AutoApply,
			OnFailure:   onFailure,
		})
		if err != nil {
			return repository.Pipeline{}, fmt.Errorf("create stage %d: %w", i, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return repository.Pipeline{}, fmt.Errorf("commit: %w", err)
	}

	return pipeline, nil
}

// stageWorkspaceInOrg keeps a pipeline pointed at workspaces its own org owns.
// pipeline_stages.workspace_id has an unscoped foreign key, so without this a
// stage could name another org's workspace: it would never execute (the run
// claim is org-scoped) but it would sit in the pipeline detail response
// disclosing that the id exists.
func (s *PipelineService) stageWorkspaceInOrg(ctx context.Context, q *repository.Queries, workspaceID, orgID string) error {
	if _, err := q.GetWorkspace(ctx, repository.GetWorkspaceParams{ID: workspaceID, OrgID: orgID}); err != nil {
		return apperr.Wrap(apperr.KindNotFound, "stage workspace not found", err)
	}
	return nil
}

func (s *PipelineService) Get(ctx context.Context, id, orgID string) (repository.Pipeline, error) {
	return s.queries.GetPipeline(ctx, repository.GetPipelineParams{ID: id, OrgID: orgID})
}

func (s *PipelineService) List(ctx context.Context, orgID string) ([]repository.Pipeline, error) {
	return s.queries.ListPipelines(ctx, orgID)
}

func (s *PipelineService) Update(ctx context.Context, id, orgID, name, description string, stages []CreatePipelineStageInput) (repository.Pipeline, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return repository.Pipeline{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	txq := s.queries.WithTx(tx)

	pipeline, err := txq.UpdatePipeline(ctx, repository.UpdatePipelineParams{
		ID: id, OrgID: orgID, Name: name, Description: description,
	})
	if err != nil {
		return repository.Pipeline{}, fmt.Errorf("update pipeline: %w", err)
	}

	// Replace stages if provided
	if stages != nil {
		if err := txq.DeletePipelineStages(ctx, id); err != nil {
			return repository.Pipeline{}, fmt.Errorf("delete stages: %w", err)
		}
		for i, stage := range stages {
			if err := s.stageWorkspaceInOrg(ctx, txq, stage.WorkspaceID, orgID); err != nil {
				return repository.Pipeline{}, err
			}
			onFailure := stage.OnFailure
			if onFailure == "" {
				onFailure = "stop"
			}
			_, err := txq.CreatePipelineStage(ctx, repository.CreatePipelineStageParams{
				ID:          ulid.Make().String(),
				PipelineID:  id,
				WorkspaceID: stage.WorkspaceID,
				StageOrder:  int32(i),
				AutoApply:   stage.AutoApply,
				OnFailure:   onFailure,
			})
			if err != nil {
				return repository.Pipeline{}, fmt.Errorf("create stage %d: %w", i, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return repository.Pipeline{}, fmt.Errorf("commit: %w", err)
	}

	return pipeline, nil
}

func (s *PipelineService) Delete(ctx context.Context, id, orgID string) error {
	hasActive, err := s.queries.HasActivePipelineRuns(ctx, id)
	if err != nil {
		return fmt.Errorf("check active runs: %w", err)
	}
	if hasActive {
		return fmt.Errorf("pipeline has active runs")
	}
	return s.queries.DeletePipeline(ctx, repository.DeletePipelineParams{ID: id, OrgID: orgID})
}

func (s *PipelineService) ListStages(ctx context.Context, pipelineID string) ([]repository.PipelineStageWithWorkspace, error) {
	return s.queries.ListPipelineStages(ctx, pipelineID)
}

// StageWorkspaceGates returns, for the workspaces a submitted stage list points
// at, which of them exist in this org and which of those gate their applies.
// The pipeline write path uses it to decide the bar for a stage that carries
// auto_apply, and to refuse a stage naming a workspace this org does not have.
func (s *PipelineService) StageWorkspaceGates(ctx context.Context, orgID string, workspaceIDs []string) ([]repository.WorkspaceGateRow, error) {
	return s.queries.ListWorkspaceGates(ctx, orgID, workspaceIDs)
}

func (s *PipelineService) StartRun(ctx context.Context, pipelineID, orgID, createdBy string) (repository.PipelineRun, error) {
	// One run of a pipeline at a time. Its stages hand outputs to each other
	// through the target workspace's variables, so two runs of the same pipeline
	// write the same keys and each plans against whichever landed last.
	//
	// pgx.ErrNoRows is the answer "nothing is running". Any other error leaves
	// the question unanswered, and starting on an unanswered question is the one
	// outcome the guard exists to prevent.
	_, err := s.queries.GetActivePipelineRunForPipeline(ctx, pipelineID, orgID)
	if err == nil {
		return repository.PipelineRun{}, apperr.Conflict("pipeline already has an active run")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return repository.PipelineRun{}, fmt.Errorf("check whether this pipeline already has an active run: %w", err)
	}

	// Get stages
	stages, err := s.queries.ListPipelineStages(ctx, pipelineID)
	if err != nil {
		return repository.PipelineRun{}, fmt.Errorf("list stages: %w", err)
	}
	if len(stages) == 0 {
		return repository.PipelineRun{}, fmt.Errorf("pipeline has no stages")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return repository.PipelineRun{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	txq := s.queries.WithTx(tx)

	pipelineRun, err := txq.CreatePipelineRun(ctx, repository.CreatePipelineRunParams{
		ID:          ulid.Make().String(),
		PipelineID:  pipelineID,
		OrgID:       orgID,
		TotalStages: int32(len(stages)),
		CreatedBy:   createdBy,
	})
	if err != nil {
		return repository.PipelineRun{}, fmt.Errorf("create pipeline run: %w", err)
	}

	// Create run stages
	for _, stage := range stages {
		_, err := txq.CreatePipelineRunStage(ctx, repository.CreatePipelineRunStageParams{
			ID:            ulid.Make().String(),
			PipelineRunID: pipelineRun.ID,
			StageID:       stage.ID,
			WorkspaceID:   stage.WorkspaceID,
			StageOrder:    stage.StageOrder,
			AutoApply:     stage.AutoApply,
			OnFailure:     stage.OnFailure,
		})
		if err != nil {
			return repository.PipelineRun{}, fmt.Errorf("create run stage %d: %w", stage.StageOrder, err)
		}
	}

	// Enqueue first stage job
	if s.riverClient != nil {
		_, err = s.riverClient.InsertTx(ctx, tx, worker.PipelineStageJobArgs{
			PipelineRunID: pipelineRun.ID,
			StageOrder:    0,
			OrgID:         orgID,
			CreatedBy:     createdBy,
		}, nil)
		if err != nil {
			return repository.PipelineRun{}, fmt.Errorf("enqueue first stage: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return repository.PipelineRun{}, fmt.Errorf("commit: %w", err)
	}

	return pipelineRun, nil
}

func (s *PipelineService) CancelRun(ctx context.Context, pipelineRunID, orgID string) (repository.PipelineRun, error) {
	pr, err := s.cancels.GetPipelineRun(ctx, repository.GetPipelineRunParams{ID: pipelineRunID, OrgID: orgID})
	if err != nil {
		return repository.PipelineRun{}, fmt.Errorf("get pipeline run: %w", err)
	}
	if pr.Status != "running" {
		return repository.PipelineRun{}, fmt.Errorf("pipeline run is not running")
	}

	// Cancel any running workspace run for the current stage
	stages, err := s.cancels.ListPipelineRunStages(ctx, pipelineRunID)
	if err != nil {
		return repository.PipelineRun{}, fmt.Errorf("list stages: %w", err)
	}
	for _, stage := range stages {
		if stage.Status == "running" && stage.RunID != nil {
			if _, err := s.cancelRun.Cancel(ctx, *stage.RunID, stage.WorkspaceID, orgID); err != nil {
				slog.Warn("failed to cancel workspace run in pipeline", "run_id", *stage.RunID, "error", err)
			}
			// The running stage is not in the set CancelPendingPipelineRunStages
			// matches, which is 'pending' and 'importing_outputs'. Its run has
			// just been cancelled and will never move again, so leaving the stage
			// 'running' shows a cancelled pipeline with work still in progress —
			// the detail view returns this column verbatim.
			if _, err := s.cancels.FinishPipelineRunStage(ctx, stage.ID, "cancelled"); err != nil {
				slog.Error("failed to cancel the running pipeline stage", "stage_id", stage.ID, "error", err,
					"consequence", "the stage reads 'running' under a cancelled pipeline and nothing sweeps it")
			}
		}
	}

	// Cancel pending stages
	if err := s.cancels.CancelPendingPipelineRunStages(ctx, pipelineRunID); err != nil {
		slog.Error("failed to cancel pending pipeline stages", "error", err)
	}

	// Mark pipeline run as cancelled
	updated, err := s.cancels.FinishPipelineRun(ctx, pipelineRunID, "cancelled")
	if err != nil {
		return repository.PipelineRun{}, fmt.Errorf("finish pipeline run: %w", err)
	}

	return updated, nil
}

func (s *PipelineService) GetRun(ctx context.Context, id, orgID string) (repository.PipelineRun, error) {
	return s.queries.GetPipelineRun(ctx, repository.GetPipelineRunParams{ID: id, OrgID: orgID})
}

func (s *PipelineService) ListRuns(ctx context.Context, pipelineID, orgID string, page, perPage int) ([]repository.PipelineRun, int64, error) {
	offset := conv.Int32((page - 1) * perPage)
	runs, err := s.queries.ListPipelineRuns(ctx, repository.ListPipelineRunsParams{
		PipelineID: pipelineID, OrgID: orgID, Limit: conv.Int32(perPage), Offset: offset,
	})
	if err != nil {
		return nil, 0, err
	}
	count, err := s.queries.CountPipelineRuns(ctx, pipelineID, orgID)
	if err != nil {
		return nil, 0, err
	}
	return runs, count, nil
}

func (s *PipelineService) ListRunStages(ctx context.Context, pipelineRunID string) ([]repository.PipelineRunStageWithWorkspace, error) {
	return s.queries.ListPipelineRunStages(ctx, pipelineRunID)
}
