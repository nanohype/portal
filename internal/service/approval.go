package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker"
)

// ApprovalService owns the run-approval write path: a status-guarded transaction
// that records the decision, transitions the run, and (on approval) enqueues the
// apply job on the same tx so concurrent decisions can't double-apply. It's the
// business logic that used to live inline in ApprovalHandler.
type ApprovalService struct {
	queries     *repository.Queries
	db          *pgxpool.Pool
	auditSvc    *AuditService
	riverClient *river.Client[pgx.Tx]
}

func NewApprovalService(queries *repository.Queries, db *pgxpool.Pool, auditSvc *AuditService) *ApprovalService {
	return &ApprovalService{queries: queries, db: db, auditSvc: auditSvc}
}

func (s *ApprovalService) SetRiverClient(client *river.Client[pgx.Tx]) {
	s.riverClient = client
}

// List returns a run's approvals, scoped to the caller's org. A run the org
// can't see is a 404, not an empty list.
func (s *ApprovalService) List(ctx context.Context, runID, orgID string) ([]repository.Approval, error) {
	if _, err := s.queries.GetRun(ctx, repository.GetRunParams{ID: runID, OrgID: orgID}); err != nil {
		return nil, apperr.NotFound("run not found")
	}
	return s.queries.ListApprovalsByRun(ctx, runID)
}

// Create records an approve/reject decision. The run is locked FOR UPDATE, its
// status is guarded (must be planned / awaiting_approval), the approval row is
// written, the run is transitioned (queued on approval, discarded on rejection),
// and on approval the apply job is enqueued — all in one transaction, so two
// concurrent approvals can't both apply. status must be "approved" or "rejected"
// (the handler validates the request shape).
//
// On rejection, the run is done with its workspace, so the workspace run slot is
// released (only if this run still holds it) and the next pending run is claimed
// + enqueued atomically — the same hand-off RunService.Cancel and the worker's
// finish paths use, so a concurrent reject + cancel can't double-enqueue.
func (s *ApprovalService) Create(ctx context.Context, runID, orgID, userID, status, comment, ipAddress, userAgent string) (repository.Approval, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return repository.Approval{}, fmt.Errorf("begin approval tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	// Lock the run row to serialize concurrent approvals.
	run, err := qtx.GetRunForUpdate(ctx, repository.GetRunParams{ID: runID, OrgID: orgID})
	if err != nil {
		return repository.Approval{}, apperr.NotFound("run not found")
	}
	if run.Status != "planned" && run.Status != "awaiting_approval" {
		return repository.Approval{}, apperr.Conflict("run is not awaiting approval")
	}

	approval, err := qtx.CreateApproval(ctx, repository.CreateApprovalParams{
		ID:      ulid.Make().String(),
		RunID:   runID,
		OrgID:   orgID,
		UserID:  userID,
		Status:  status,
		Comment: comment,
	})
	if err != nil {
		return repository.Approval{}, fmt.Errorf("create approval: %w", err)
	}

	if status == "approved" {
		if _, err := qtx.UpdateRunStatus(ctx, repository.UpdateRunStatusParams{ID: runID, Status: "queued"}); err != nil {
			return repository.Approval{}, fmt.Errorf("update run status to queued: %w", err)
		}
		if s.riverClient != nil {
			if _, err := s.riverClient.InsertTx(ctx, tx, worker.RunJobArgs{
				RunID:       runID,
				WorkspaceID: run.WorkspaceID,
				OrgID:       run.OrgID,
				Operation:   "apply",
			}, nil); err != nil {
				return repository.Approval{}, fmt.Errorf("enqueue apply job: %w", err)
			}
		}
	} else {
		if _, err := qtx.UpdateRunStatus(ctx, repository.UpdateRunStatusParams{ID: runID, Status: "discarded"}); err != nil {
			return repository.Approval{}, fmt.Errorf("discard run: %w", err)
		}
	}

	// Record the decision on the same transaction. An approve/reject is a
	// compliance-relevant, irreversible decision (it releases or kills a gated
	// apply), so the audit row must commit or roll back with it — never stand
	// without its record. A failed audit write aborts the whole decision.
	if err := s.auditSvc.LogTx(ctx, qtx, AuditEntry{
		OrgID: orgID, UserID: userID,
		Action: "approval.create", EntityType: "approval", EntityID: approval.ID,
		After: approval, IPAddress: ipAddress, UserAgent: userAgent,
	}); err != nil {
		return repository.Approval{}, fmt.Errorf("write approval audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return repository.Approval{}, fmt.Errorf("commit approval tx: %w", err)
	}

	// Post-commit: a rejected run is finished — free its slot and hand off.
	if status == "rejected" {
		if err := s.queries.ReleaseWorkspaceRun(ctx, run.WorkspaceID, run.OrgID, runID); err != nil {
			slog.Error("failed to release workspace run slot after rejection", "error", err, "workspace_id", run.WorkspaceID, "run_id", runID)
		}
		if s.riverClient != nil {
			worker.ClaimAndEnqueueNextRun(ctx, s.queries, s.db, s.riverClient, run.WorkspaceID, slog.Default())
		}
	}

	return approval, nil
}
