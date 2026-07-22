package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

// approvalAuditRecord is the snake_case projection of an approval row stored
// in the audit trail. The decision commits inside this service's transaction,
// so the audit shape lives here rather than at the HTTP boundary.
type approvalAuditRecord struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	OrgID     string    `json:"org_id"`
	UserID    string    `json:"user_id"`
	Status    string    `json:"status"`
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
}

// List returns a run's approvals. The run is keyed on the workspace the caller
// was authorized against as well as their org, so a run from another workspace
// is a 404, not an empty list.
func (s *ApprovalService) List(ctx context.Context, runID, workspaceID, orgID string) ([]repository.Approval, error) {
	if _, err := s.queries.GetRunInWorkspace(ctx, repository.GetRunInWorkspaceParams{
		ID: runID, WorkspaceID: workspaceID, OrgID: orgID,
	}); err != nil {
		return nil, apperr.NotFound("run not found")
	}
	return s.queries.ListApprovalsByRun(ctx, runID)
}

// Create records an approve/reject decision. The run is locked FOR UPDATE, its
// status is guarded (must be planned / awaiting_approval), the approval row is
// written, the run is transitioned (queued on approval, discarded on rejection),
// and on approval the workspace's run slot is claimed and the apply job enqueued
// — all in one transaction, so two concurrent approvals can't both apply, and an
// approved apply can't start alongside whatever took the workspace while the run
// was parked. status must be "approved" or "rejected" (the handler validates the
// request shape).
//
// On rejection, the run is done with its workspace, so the workspace run slot is
// released (only if this run still holds it) and the next pending run is claimed
// + enqueued atomically — the same hand-off RunService.Cancel and the worker's
// finish paths use, so a concurrent reject + cancel can't double-enqueue.
//
// The signer is not compared against the run's creator, and that is the model:
// requires_approval means an admin was involved, not that two people were.
// Signing your own plan gains nothing — the route already sits at
// ActionApplyProd, and anyone who clears it may POST {"operation":"apply"} on
// the same workspace directly (handler/run.go), so refusing a self-approval
// would close a door while leaving the wall open. Separation of duties would be
// a different design: it needs the direct-apply path to go too, and it locks
// out an org with one admin, so it is not something to bolt on here.
func (s *ApprovalService) Create(ctx context.Context, runID, workspaceID, orgID, userID, status, comment, ipAddress, userAgent string) (repository.Approval, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return repository.Approval{}, fmt.Errorf("begin approval tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	// Lock the run row to serialize concurrent approvals. Keyed on the
	// workspace the caller was authorized against, so an approval can only
	// release a run of that workspace.
	run, err := qtx.GetRunInWorkspaceForUpdate(ctx, repository.GetRunInWorkspaceParams{
		ID: runID, WorkspaceID: workspaceID, OrgID: orgID,
	})
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
		// Take the workspace's single run slot before enqueueing, in this
		// transaction. Parking at awaiting_approval released the slot and handed
		// it to the next pending run, so by the time an admin signs, something
		// else may be running against this workspace's state — and nothing
		// downstream would stop the approved apply joining it. Plain-tofu
		// workspaces restore their state per run from object storage with no
		// backend lock, so two concurrent runs simply race and the later
		// CreateStateVersion wins.
		//
		// Losing the claim is not a failure: the run keeps its approval and stays
		// 'pending', and the hand-off that runs on every release
		// (ClaimAndEnqueueNextRun) picks it up as the oldest pending run. Either
		// way the operation moves to 'apply', which is what the signature
		// authorized and what both enqueue paths read.
		//
		// This is the one path that reclaims rather than claims. Releasing the
		// slot on the way to awaiting_approval is best-effort, so the plan being
		// signed may still be holding its own slot; under the strict claim it
		// would be parked behind itself until the reaper freed it. The reclaim is
		// bounded to this transaction, which is already exclusive on the run —
		// the row is held FOR UPDATE and only 'planned' / 'awaiting_approval' gets
		// this far, so a second approval of the same run is refused above rather
		// than reaching a second enqueue.
		queued := true
		if _, err := qtx.ReclaimWorkspaceForRun(ctx, run.WorkspaceID, run.OrgID, runID); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return repository.Approval{}, fmt.Errorf("claim workspace for approved run: %w", err)
			}
			queued = false
			slog.Info("workspace has an active run, approved apply stays pending",
				"workspace_id", run.WorkspaceID, "run_id", runID)
		}

		runStatus := "pending"
		if queued {
			runStatus = "queued"
		}
		if _, err := qtx.MarkRunApproved(ctx, runID, runStatus); err != nil {
			return repository.Approval{}, fmt.Errorf("transition approved run to %s: %w", runStatus, err)
		}
		if queued && s.riverClient != nil {
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
		After: approvalAuditRecord{
			ID:        approval.ID,
			RunID:     approval.RunID,
			OrgID:     approval.OrgID,
			UserID:    approval.UserID,
			Status:    approval.Status,
			Comment:   approval.Comment,
			CreatedAt: approval.CreatedAt,
		},
		IPAddress: ipAddress, UserAgent: userAgent,
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
