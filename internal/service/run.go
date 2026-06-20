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

	"github.com/nanohype/portal/internal/conv"
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
	offset := conv.Int32((page - 1) * perPage)

	runs, err := s.queries.ListRunsByWorkspace(ctx, repository.ListRunsByWorkspaceParams{
		WorkspaceID: workspaceID,
		OrgID:       orgID,
		Limit:       conv.Int32(perPage),
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

	// Atomically claim the workspace's single run slot, then enqueue in the same
	// transaction. Only the run that wins the slot is enqueued; concurrent Creates
	// that lose stay 'pending' and are picked up when the active run releases.
	// This conditional claim closes the check-then-act race where two runs could
	// otherwise execute against the same tofu state at once.
	if s.riverClient != nil {
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return run, fmt.Errorf("run %s created but failed to begin enqueue tx: %w", runID, err)
		}
		defer tx.Rollback(ctx)
		qtx := s.queries.WithTx(tx)

		if _, err := qtx.ClaimWorkspaceForRun(ctx, params.WorkspaceID, params.OrgID, runID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Another run holds the slot — this run stays pending.
				slog.Info("workspace has active run, new run will stay pending", "workspace_id", params.WorkspaceID, "run_id", runID)
				return run, nil
			}
			return run, fmt.Errorf("run %s created but failed to claim workspace: %w", runID, err)
		}

		if _, err := s.riverClient.InsertTx(ctx, tx, worker.RunJobArgs{
			RunID:             runID,
			WorkspaceID:       params.WorkspaceID,
			OrgID:             params.OrgID,
			Operation:         params.Operation,
			Imports:           params.Imports,
			AutoApplyOverride: params.AutoApplyOverride,
		}, nil); err != nil {
			return run, fmt.Errorf("run %s created but failed to enqueue job: %w", runID, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return run, fmt.Errorf("run %s created but failed to commit enqueue tx: %w", runID, err)
		}
	}

	return run, nil
}

func (s *RunService) Cancel(ctx context.Context, runID, orgID string) (repository.Run, error) {
	run, err := s.queries.CancelRun(ctx, runID, orgID)
	if err != nil {
		return repository.Run{}, err
	}

	// Release the slot only if this run actually held it — cancelling a queued
	// run must not free the active run's slot. Then atomically hand off to the
	// next pending run (the same claim-and-enqueue the worker's finish path uses,
	// so a concurrent cancel + finish can't double-enqueue the next run).
	if err := s.queries.ReleaseWorkspaceRun(ctx, run.WorkspaceID, orgID, runID); err != nil {
		slog.Error("failed to release workspace run slot after cancel", "error", err, "workspace_id", run.WorkspaceID, "run_id", runID)
	}
	if s.riverClient != nil {
		worker.ClaimAndEnqueueNextRun(ctx, s.queries, s.db, s.riverClient, run.WorkspaceID, slog.Default())
	}

	return run, nil
}
