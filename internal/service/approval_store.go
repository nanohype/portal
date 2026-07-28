package service

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker"
)

// approvalTx is the transactional surface an approval decision needs, and
// nothing else.
//
// The decision is the most safety-critical transaction in the product: it
// releases or kills a gated apply, and every statement in it can fail. Taking a
// concrete *repository.Queries and *pgxpool.Pool made those failures
// unreachable from a test — provoking one means making a single statement fail
// while the others succeed on a connection already holding row locks, which no
// amount of fixture setup can express. The result was a set of error paths that
// existed, mattered, and had never once been executed.
//
// Narrowing to an interface is the same treatment auth.WorkspaceRoleResolver
// already uses. Production still runs pgx end to end; the difference is that
// "what happens when the audit write fails after the run has been transitioned"
// is now a question the suite can ask.
type approvalTx interface {
	GetRunInWorkspaceForUpdate(ctx context.Context, arg repository.GetRunInWorkspaceParams) (repository.Run, error)
	CreateApproval(ctx context.Context, arg repository.CreateApprovalParams) (repository.Approval, error)
	ReclaimWorkspaceForRun(ctx context.Context, id, orgID, runID string) (string, error)
	MarkRunApproved(ctx context.Context, id, status string) (repository.Run, error)
	UpdateRunStatus(ctx context.Context, arg repository.UpdateRunStatusParams) (repository.Run, error)
	CreateAuditLog(ctx context.Context, arg repository.CreateAuditLogParams) (repository.AuditLog, error)

	// EnqueueApply inserts the apply job on this same transaction, so an
	// approval that commits always has its job and one that rolls back never
	// leaves an orphan.
	EnqueueApply(ctx context.Context, args worker.RunJobArgs) error

	Commit(ctx context.Context) error
	// Rollback is deferred unconditionally; after a successful Commit it is a
	// no-op, which is the pgx contract this relies on.
	Rollback(ctx context.Context) error
}

// approvalStore opens the transaction above.
type approvalStore interface {
	BeginApproval(ctx context.Context) (approvalTx, error)
}

// pgApprovalStore is the production implementation: a real pgx transaction with
// the tx-scoped queries handle and the River client bound to it.
type pgApprovalStore struct {
	queries *repository.Queries
	db      *pgxpool.Pool
	river   func() *river.Client[pgx.Tx]
}

func (s *pgApprovalStore) BeginApproval(ctx context.Context) (approvalTx, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &pgApprovalTx{
		Queries: s.queries.WithTx(tx),
		tx:      tx,
		river:   s.river,
	}, nil
}

// pgApprovalTx embeds the tx-scoped Queries, so every query method on the
// interface is satisfied by the generated code rather than re-declared here.
type pgApprovalTx struct {
	*repository.Queries
	tx    pgx.Tx
	river func() *river.Client[pgx.Tx]
}

func (t *pgApprovalTx) EnqueueApply(ctx context.Context, args worker.RunJobArgs) error {
	client := t.river()
	if client == nil {
		// No River client wired (migrations, seeding, tests of other paths).
		// The run is already 'queued'; the reaper's hand-off picks it up.
		return nil
	}
	_, err := client.InsertTx(ctx, t.tx, args, nil)
	return err
}

func (t *pgApprovalTx) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t *pgApprovalTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

// runHandoff frees a finished run's workspace slot and claims the next pending
// run for it.
//
// This runs after the decision commits, and deliberately so: the slot belongs
// to a run that is now done either way, and a failure here costs a wedged
// workspace until the reaper rather than a lost decision. That is also why it
// needs its own seam — it is the one part of the rejection path that is
// invisible from the transaction, so without it "a rejected run hands its
// workspace on" is a claim no test can make.
type runHandoff interface {
	ReleaseAndHandOff(ctx context.Context, workspaceID, orgID, runID string)
}

type pgRunHandoff struct {
	queries *repository.Queries
	db      *pgxpool.Pool
	river   func() *river.Client[pgx.Tx]
}

func (h *pgRunHandoff) ReleaseAndHandOff(ctx context.Context, workspaceID, orgID, runID string) {
	if err := h.queries.ReleaseWorkspaceRun(ctx, workspaceID, orgID, runID); err != nil {
		slog.Error("failed to release workspace run slot after rejection",
			"error", err, "workspace_id", workspaceID, "run_id", runID)
	}
	if client := h.river(); client != nil {
		worker.ClaimAndEnqueueNextRun(ctx, h.queries, h.db, client, workspaceID, slog.Default())
	}
}
