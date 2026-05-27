package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/logstream"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker"
)

type RunService struct {
	queries     *repository.Queries
	db          *pgxpool.Pool
	streamer    logstream.Streamer
	riverClient *river.Client[pgx.Tx]
}

func NewRunService(queries *repository.Queries, db *pgxpool.Pool, streamer logstream.Streamer) *RunService {
	return &RunService{queries: queries, db: db, streamer: streamer}
}

func (s *RunService) SetRiverClient(client *river.Client[pgx.Tx]) {
	s.riverClient = client
}

type CreateRunParams struct {
	WorkspaceID       string
	OrgID             string
	Operation         string
	CreatedBy         string
	CommitSHA         string
	Imports           []worker.ImportResource
	AutoApplyOverride *bool
}

func (s *RunService) List(ctx context.Context, workspaceID, orgID string, page, perPage int) ([]any, int64, error) {
	offset := int32((page - 1) * perPage)

	runs, err := s.queries.ListRunsByWorkspace(ctx, repository.ListRunsByWorkspaceParams{
		WorkspaceID: workspaceID,
		OrgID:       orgID,
		Limit:       int32(perPage),
		Offset:      offset,
	})
	if err != nil {
		return nil, 0, err
	}

	count, err := s.queries.CountRunsByWorkspace(ctx, repository.CountRunsByWorkspaceParams{
		WorkspaceID: workspaceID,
		OrgID:       orgID,
	})
	if err != nil {
		return nil, 0, err
	}

	result := make([]any, len(runs))
	for i, r := range runs {
		result[i] = r
	}

	return result, count, nil
}

func (s *RunService) Get(ctx context.Context, id, orgID string) (repository.Run, error) {
	return s.queries.GetRun(ctx, repository.GetRunParams{
		ID:    id,
		OrgID: orgID,
	})
}

func (s *RunService) Create(ctx context.Context, params CreateRunParams) (repository.Run, error) {
	runID := ulid.Make().String()

	// Create run in database
	run, err := s.queries.CreateRun(ctx, repository.CreateRunParams{
		ID:          runID,
		WorkspaceID: params.WorkspaceID,
		OrgID:       params.OrgID,
		Operation:   params.Operation,
		Status:      "pending",
		CreatedBy:   params.CreatedBy,
		CommitSHA:   params.CommitSHA,
	})
	if err != nil {
		return repository.Run{}, err
	}

	// Only enqueue if no other active run exists for this workspace
	if s.riverClient != nil {
		activeRun, err := s.queries.GetActiveRunForWorkspace(ctx, params.WorkspaceID)
		if err != nil || activeRun.ID == runID {
			// No active run (pgx.ErrNoRows), or only the one we just created — safe to enqueue
			tx, err := s.db.Begin(ctx)
			if err != nil {
				return run, fmt.Errorf("run %s created but failed to begin enqueue tx: %w", runID, err)
			}
			defer tx.Rollback(ctx)

			_, err = s.riverClient.InsertTx(ctx, tx, worker.RunJobArgs{
				RunID:             runID,
				WorkspaceID:       params.WorkspaceID,
				OrgID:             params.OrgID,
				Operation:         params.Operation,
				Imports:           params.Imports,
				AutoApplyOverride: params.AutoApplyOverride,
			}, nil)
			if err != nil {
				return run, fmt.Errorf("run %s created but failed to enqueue job: %w", runID, err)
			}

			if err := tx.Commit(ctx); err != nil {
				return run, fmt.Errorf("run %s created but failed to commit enqueue tx: %w", runID, err)
			}
		} else {
			slog.Info("workspace has active run, new run will stay pending", "workspace_id", params.WorkspaceID, "run_id", runID)
		}
	}

	return run, nil
}

func (s *RunService) Cancel(ctx context.Context, runID, orgID string) (repository.Run, error) {
	run, err := s.queries.CancelRun(ctx, runID, orgID)
	if err != nil {
		return repository.Run{}, err
	}

	// Clear current_run_id on workspace
	if err := s.queries.SetWorkspaceCurrentRun(ctx, repository.SetWorkspaceCurrentRunParams{
		ID: run.WorkspaceID, OrgID: orgID, CurrentRunID: nil,
	}); err != nil {
		slog.Error("failed to clear workspace current run after cancel", "error", err, "workspace_id", run.WorkspaceID, "run_id", runID)
	}

	// Enqueue next pending run if any
	if s.riverClient != nil {
		nextRun, err := s.queries.GetNextPendingRun(ctx, run.WorkspaceID)
		if err == nil {
			tx, err := s.db.Begin(ctx)
			if err != nil {
				slog.Error("failed to begin tx for next pending run after cancel", "error", err, "run_id", runID, "workspace_id", run.WorkspaceID)
				return run, nil
			}
			_, insErr := s.riverClient.InsertTx(ctx, tx, worker.RunJobArgs{
				RunID:       nextRun.ID,
				WorkspaceID: nextRun.WorkspaceID,
				OrgID:       nextRun.OrgID,
				Operation:   nextRun.Operation,
			}, nil)
			if insErr != nil {
				slog.Error("failed to enqueue next pending run after cancel", "error", insErr, "run_id", runID, "next_run_id", nextRun.ID)
				tx.Rollback(ctx)
			} else if err := tx.Commit(ctx); err != nil {
				slog.Error("failed to commit next pending run enqueue after cancel", "error", err, "run_id", runID, "next_run_id", nextRun.ID)
			}
		}
	}

	return run, nil
}
