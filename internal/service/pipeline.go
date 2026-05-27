package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/storage"
	"github.com/nanohype/portal/internal/tfstate"
	"github.com/nanohype/portal/internal/worker"
)

type PipelineService struct {
	queries     *repository.Queries
	db          *pgxpool.Pool
	runSvc      *RunService
	storage     *storage.S3Storage
	riverClient *river.Client[pgx.Tx]
}

func NewPipelineService(queries *repository.Queries, db *pgxpool.Pool, runSvc *RunService, store *storage.S3Storage) *PipelineService {
	return &PipelineService{queries: queries, db: db, runSvc: runSvc, storage: store}
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

func (s *PipelineService) StartRun(ctx context.Context, pipelineID, orgID, createdBy string) (repository.PipelineRun, error) {
	// Check for active run
	_, err := s.queries.GetActivePipelineRunForPipeline(ctx, pipelineID, orgID)
	if err == nil {
		return repository.PipelineRun{}, fmt.Errorf("pipeline already has an active run")
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
	pr, err := s.queries.GetPipelineRun(ctx, repository.GetPipelineRunParams{ID: pipelineRunID, OrgID: orgID})
	if err != nil {
		return repository.PipelineRun{}, fmt.Errorf("get pipeline run: %w", err)
	}
	if pr.Status != "running" {
		return repository.PipelineRun{}, fmt.Errorf("pipeline run is not running")
	}

	// Cancel any running workspace run for the current stage
	stages, err := s.queries.ListPipelineRunStages(ctx, pipelineRunID)
	if err != nil {
		return repository.PipelineRun{}, fmt.Errorf("list stages: %w", err)
	}
	for _, stage := range stages {
		if stage.Status == "running" && stage.RunID != nil {
			if _, err := s.runSvc.Cancel(ctx, *stage.RunID, orgID); err != nil {
				slog.Warn("failed to cancel workspace run in pipeline", "run_id", *stage.RunID, "error", err)
			}
		}
	}

	// Cancel pending stages
	if err := s.queries.CancelPendingPipelineRunStages(ctx, pipelineRunID); err != nil {
		slog.Error("failed to cancel pending pipeline stages", "error", err)
	}

	// Mark pipeline run as cancelled
	updated, err := s.queries.FinishPipelineRun(ctx, pipelineRunID, "cancelled")
	if err != nil {
		return repository.PipelineRun{}, fmt.Errorf("finish pipeline run: %w", err)
	}

	return updated, nil
}

func (s *PipelineService) GetRun(ctx context.Context, id, orgID string) (repository.PipelineRun, error) {
	return s.queries.GetPipelineRun(ctx, repository.GetPipelineRunParams{ID: id, OrgID: orgID})
}

func (s *PipelineService) ListRuns(ctx context.Context, pipelineID, orgID string, page, perPage int) ([]repository.PipelineRun, int64, error) {
	offset := int32((page - 1) * perPage)
	runs, err := s.queries.ListPipelineRuns(ctx, repository.ListPipelineRunsParams{
		PipelineID: pipelineID, OrgID: orgID, Limit: int32(perPage), Offset: offset,
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

// ImportOutputsBetweenWorkspaces imports outputs from the source workspace's state
// as terraform variables into the target workspace.
func ImportOutputsBetweenWorkspaces(ctx context.Context, queries *repository.Queries, store *storage.S3Storage, sourceWorkspaceID, targetWorkspaceID, orgID string) error {
	if store == nil {
		return fmt.Errorf("storage not configured")
	}

	sv, err := queries.GetLatestStateVersion(ctx, repository.GetLatestStateVersionParams{
		WorkspaceID: sourceWorkspaceID, OrgID: orgID,
	})
	if err != nil {
		return fmt.Errorf("source workspace has no state: %w", err)
	}

	data, err := store.GetState(ctx, sv.StateURL)
	if err != nil {
		return fmt.Errorf("failed to fetch source state: %w", err)
	}

	outputs, err := tfstate.ParseOutputs(data)
	if err != nil {
		return fmt.Errorf("failed to parse outputs: %w", err)
	}

	if len(outputs) == 0 {
		slog.Info("no outputs to import", "source_workspace", sourceWorkspaceID, "target_workspace", targetWorkspaceID)
		return nil
	}

	existing, err := queries.ListWorkspaceVariables(ctx, repository.ListWorkspaceVariablesParams{
		WorkspaceID: targetWorkspaceID, OrgID: orgID,
	})
	if err != nil {
		return fmt.Errorf("failed to list target variables: %w", err)
	}
	existingByKey := make(map[string]repository.WorkspaceVariable, len(existing))
	for _, v := range existing {
		if v.Category == "terraform" {
			existingByKey[v.Key] = v
		}
	}

	for _, out := range outputs {
		if out.Sensitive {
			continue
		}

		var valueStr string
		switch v := out.Value.(type) {
		case string:
			valueStr = v
		default:
			b, _ := json.Marshal(v)
			valueStr = string(b)
		}

		desc := fmt.Sprintf("Imported from pipeline stage output (%s)", out.Type)

		if ev, exists := existingByKey[out.Name]; exists {
			_, err = queries.UpdateWorkspaceVariable(ctx, repository.UpdateWorkspaceVariableParams{
				ID: ev.ID, OrgID: orgID, Value: valueStr, Sensitive: false, Description: desc,
			})
		} else {
			_, err = queries.CreateWorkspaceVariable(ctx, repository.CreateWorkspaceVariableParams{
				ID:          ulid.Make().String(),
				WorkspaceID: targetWorkspaceID,
				OrgID:       orgID,
				Key:         out.Name,
				Value:       valueStr,
				Sensitive:   false,
				Category:    "terraform",
				Description: desc,
			})
		}
		if err != nil {
			slog.Warn("failed to import output as variable", "key", out.Name, "error", err)
		}
	}

	slog.Info("imported outputs between workspaces", "source", sourceWorkspaceID, "target", targetWorkspaceID, "outputs", len(outputs))
	return nil
}
