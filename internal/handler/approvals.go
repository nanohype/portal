package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
	"github.com/nanohype/portal/internal/worker"
)

type ApprovalHandler struct {
	queries     *repository.Queries
	db          *pgxpool.Pool
	riverClient *river.Client[pgx.Tx]
	auditSvc    *service.AuditService
}

func NewApprovalHandler(queries *repository.Queries, db *pgxpool.Pool, auditSvc *service.AuditService) *ApprovalHandler {
	return &ApprovalHandler{queries: queries, db: db, auditSvc: auditSvc}
}

func (h *ApprovalHandler) SetRiverClient(client *river.Client[pgx.Tx]) {
	h.riverClient = client
}

type ApprovalRequest struct {
	Status  string `json:"status"` // "approved" or "rejected"
	Comment string `json:"comment"`
}

func (h *ApprovalHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	runID := chi.URLParam(r, "runID")

	if _, err := h.queries.GetRun(r.Context(), repository.GetRunParams{
		ID: runID, OrgID: userCtx.OrgID,
	}); err != nil {
		respond.Error(w, http.StatusNotFound, "run not found")
		return
	}

	approvals, err := h.queries.ListApprovalsByRun(r.Context(), runID)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list approvals")
		return
	}

	respond.List(w, approvals)
}

func (h *ApprovalHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	runID := chi.URLParam(r, "runID")

	var req ApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Status != "approved" && req.Status != "rejected" {
		respond.Error(w, http.StatusBadRequest, "status must be 'approved' or 'rejected'")
		return
	}

	// Use a transaction with SELECT FOR UPDATE to prevent concurrent approvals
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		slog.Error("failed to begin approval tx", "error", err)
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to process approval")
		return
	}
	defer tx.Rollback(r.Context())

	txQueries := h.queries.WithTx(tx)

	// Lock the run row to prevent concurrent approval race
	run, err := txQueries.GetRunForUpdate(r.Context(), repository.GetRunParams{ID: runID, OrgID: userCtx.OrgID})
	if err != nil {
		respond.Error(w, http.StatusNotFound, "run not found")
		return
	}

	if run.Status != "planned" && run.Status != "awaiting_approval" {
		respond.Error(w, http.StatusConflict, "run is not awaiting approval")
		return
	}

	// Create approval record within the transaction
	approval, err := txQueries.CreateApproval(r.Context(), repository.CreateApprovalParams{
		ID:      ulid.Make().String(),
		RunID:   runID,
		OrgID:   userCtx.OrgID,
		UserID:  userCtx.UserID,
		Status:  req.Status,
		Comment: req.Comment,
	})
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to create approval")
		return
	}

	if req.Status == "approved" {
		// Update run status within the same transaction
		if _, err := txQueries.UpdateRunStatus(r.Context(), repository.UpdateRunStatusParams{
			ID: runID, Status: "queued",
		}); err != nil {
			slog.Error("failed to update run status", "error", err)
			respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to start apply")
			return
		}

		// Enqueue apply job within the transaction if river client available
		if h.riverClient != nil {
			_, err = h.riverClient.InsertTx(r.Context(), tx, worker.RunJobArgs{
				RunID:       runID,
				WorkspaceID: run.WorkspaceID,
				OrgID:       run.OrgID,
				Operation:   "apply",
			}, nil)
			if err != nil {
				slog.Error("failed to enqueue apply job", "error", err)
				respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to start apply")
				return
			}
		}
	} else {
		// Rejected - mark run as discarded within transaction
		if _, err := txQueries.UpdateRunStatus(r.Context(), repository.UpdateRunStatusParams{
			ID: runID, Status: "discarded",
		}); err != nil {
			slog.Error("failed to discard run", "error", err, "run_id", runID)
			respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to discard run")
			return
		}
	}

	// Commit the transaction (status check + approval + status update are atomic)
	if err := tx.Commit(r.Context()); err != nil {
		slog.Error("failed to commit approval tx", "error", err)
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to process approval")
		return
	}

	// Post-commit: audit log and workspace cleanup (outside transaction)
	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "approval.create", EntityType: "approval", EntityID: approval.ID,
		After: approval, IPAddress: ip, UserAgent: ua,
	})

	if req.Status == "rejected" {
		// Unlock workspace and enqueue next run after rejection
		if err := h.queries.SetWorkspaceCurrentRun(r.Context(), repository.SetWorkspaceCurrentRunParams{
			ID: run.WorkspaceID, OrgID: run.OrgID, CurrentRunID: nil,
		}); err != nil {
			slog.Error("failed to clear workspace current run", "error", err, "workspace_id", run.WorkspaceID)
		}
		if err := h.enqueueNextPendingRun(r.Context(), run.WorkspaceID); err != nil {
			slog.Error("failed to enqueue next pending run after rejection", "error", err, "run_id", runID, "workspace_id", run.WorkspaceID)
		}
	}

	respond.JSON(w, http.StatusCreated, approval)
}

func (h *ApprovalHandler) enqueueNextPendingRun(ctx context.Context, workspaceID string) error {
	if h.riverClient == nil {
		return nil
	}

	nextRun, err := h.queries.GetNextPendingRun(ctx, workspaceID)
	if err != nil {
		return nil // no pending runs
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx for next pending run: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = h.riverClient.InsertTx(ctx, tx, worker.RunJobArgs{
		RunID:       nextRun.ID,
		WorkspaceID: nextRun.WorkspaceID,
		OrgID:       nextRun.OrgID,
		Operation:   nextRun.Operation,
	}, nil)
	if err != nil {
		return fmt.Errorf("insert next pending run job: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit next pending run job: %w", err)
	}

	return nil
}
