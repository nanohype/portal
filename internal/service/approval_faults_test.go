package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker"
)

// Fault injection for the approval decision.
//
// Every statement inside this transaction can fail, and each failure has a
// different consequence: a run transitioned but not audited, an approval
// recorded but never enqueued, a decision that commits without its compliance
// record. Against a concrete *repository.Queries and *pgxpool.Pool none of it
// was reachable — provoking one statement to fail while the others succeed on a
// connection already holding row locks is not something fixture setup can
// express. The DB-backed tests next door cover the happy paths and the
// concurrency guarantees; these cover what happens when a step does not work.

var errInjected = errors.New("injected failure")

type fakeApprovalTx struct {
	run repository.Run

	// failOn names the method that returns errInjected.
	failOn string
	// reclaimNoRows makes the workspace claim lose the race, which is a
	// documented non-failure: the run stays pending for the hand-off.
	reclaimNoRows bool

	calls      []string
	committed  bool
	rolledBack bool
}

func (f *fakeApprovalTx) record(name string) error {
	f.calls = append(f.calls, name)
	if f.failOn == name {
		return errInjected
	}
	return nil
}

func (f *fakeApprovalTx) did(name string) bool {
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

func (f *fakeApprovalTx) GetRunInWorkspaceForUpdate(_ context.Context, _ repository.GetRunInWorkspaceParams) (repository.Run, error) {
	if err := f.record("GetRunInWorkspaceForUpdate"); err != nil {
		return repository.Run{}, err
	}
	return f.run, nil
}

func (f *fakeApprovalTx) CreateApproval(_ context.Context, arg repository.CreateApprovalParams) (repository.Approval, error) {
	if err := f.record("CreateApproval"); err != nil {
		return repository.Approval{}, err
	}
	return repository.Approval{ID: arg.ID, RunID: arg.RunID, OrgID: arg.OrgID, UserID: arg.UserID, Status: arg.Status, Comment: arg.Comment}, nil
}

func (f *fakeApprovalTx) ReclaimWorkspaceForRun(_ context.Context, _, _, _ string) (string, error) {
	if err := f.record("ReclaimWorkspaceForRun"); err != nil {
		return "", err
	}
	if f.reclaimNoRows {
		return "", pgx.ErrNoRows
	}
	return "ws", nil
}

func (f *fakeApprovalTx) MarkRunApproved(_ context.Context, _, status string) (repository.Run, error) {
	f.calls = append(f.calls, "MarkRunApproved:"+status)
	if f.failOn == "MarkRunApproved" {
		return repository.Run{}, errInjected
	}
	return repository.Run{Status: status}, nil
}

func (f *fakeApprovalTx) UpdateRunStatus(_ context.Context, arg repository.UpdateRunStatusParams) (repository.Run, error) {
	f.calls = append(f.calls, "UpdateRunStatus:"+arg.Status)
	if f.failOn == "UpdateRunStatus" {
		return repository.Run{}, errInjected
	}
	return repository.Run{Status: arg.Status}, nil
}

func (f *fakeApprovalTx) CreateAuditLog(_ context.Context, _ repository.CreateAuditLogParams) (repository.AuditLog, error) {
	if err := f.record("CreateAuditLog"); err != nil {
		return repository.AuditLog{}, err
	}
	return repository.AuditLog{}, nil
}

func (f *fakeApprovalTx) EnqueueApply(_ context.Context, _ worker.RunJobArgs) error {
	return f.record("EnqueueApply")
}

func (f *fakeApprovalTx) Commit(_ context.Context) error {
	if err := f.record("Commit"); err != nil {
		return err
	}
	f.committed = true
	return nil
}

func (f *fakeApprovalTx) Rollback(_ context.Context) error {
	f.rolledBack = true
	return nil
}

// fakeHandoff records the post-commit slot release.
type fakeHandoff struct {
	calls []string
}

func (h *fakeHandoff) ReleaseAndHandOff(_ context.Context, workspaceID, _, runID string) {
	h.calls = append(h.calls, workspaceID+"/"+runID)
}

type fakeApprovalStore struct {
	tx       *fakeApprovalTx
	beginErr error
}

func (s *fakeApprovalStore) BeginApproval(_ context.Context) (approvalTx, error) {
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	return s.tx, nil
}

// approvalWithFaults builds a service whose decision transaction is the fake.
// queries/db stay nil deliberately: every path exercised here returns before
// the post-commit hand-off that would touch them, so a nil there is a guard —
// if a change makes one of these paths reach the pool, the test panics rather
// than passing quietly.
func approvalWithFaults(tx *fakeApprovalTx, beginErr error) (*ApprovalService, *fakeHandoff) {
	h := &fakeHandoff{}
	return &ApprovalService{
		auditSvc: &AuditService{},
		store:    &fakeApprovalStore{tx: tx, beginErr: beginErr},
		handoff:  h,
	}, h
}

func plannedRun() repository.Run {
	return repository.Run{ID: "run_1", Status: "planned", WorkspaceID: "ws_1", OrgID: "org_1"}
}

func approve(s *ApprovalService) (repository.Approval, error) {
	return s.Create(context.Background(), "run_1", "ws_1", "org_1", "user_1", "approved", "lgtm", "127.0.0.1", "test")
}

// approveWith swallows the harness handle for the cases that do not inspect it.
func approveWith(s *ApprovalService, _ *fakeHandoff) (repository.Approval, error) {
	return approve(s)
}

func TestApprovalCreate_BeginFailureIsReported(t *testing.T) {
	_, err := approveWith(approvalWithFaults(&fakeApprovalTx{run: plannedRun()}, errInjected))
	if err == nil || !strings.Contains(err.Error(), "begin approval tx") {
		t.Fatalf("err = %v, want it to name the failing step", err)
	}
}

func TestApprovalCreate_MissingRunIsNotFound(t *testing.T) {
	tx := &fakeApprovalTx{run: plannedRun(), failOn: "GetRunInWorkspaceForUpdate"}
	_, err := approveWith(approvalWithFaults(tx, nil))
	if apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("kind = %v, want NotFound", apperr.KindOf(err))
	}
	if tx.committed {
		t.Error("committed a decision for a run it could not read")
	}
}

func TestApprovalCreate_RunNotAwaitingApprovalIsConflict(t *testing.T) {
	// The status guard is what stops a second approval of an already-applied
	// run from enqueueing a second apply.
	for _, status := range []string{"queued", "applying", "applied", "discarded"} {
		t.Run(status, func(t *testing.T) {
			run := plannedRun()
			run.Status = status
			tx := &fakeApprovalTx{run: run}
			_, err := approveWith(approvalWithFaults(tx, nil))

			if apperr.KindOf(err) != apperr.KindConflict {
				t.Fatalf("kind = %v, want Conflict", apperr.KindOf(err))
			}
			if tx.did("CreateApproval") {
				t.Error("wrote an approval row for a run that was not awaiting one")
			}
			if tx.committed {
				t.Error("committed a decision the guard rejected")
			}
		})
	}
}

func TestApprovalCreate_AcceptsBothAwaitingStates(t *testing.T) {
	for _, status := range []string{"planned", "awaiting_approval"} {
		t.Run(status, func(t *testing.T) {
			run := plannedRun()
			run.Status = status
			tx := &fakeApprovalTx{run: run}
			if _, err := approveWith(approvalWithFaults(tx, nil)); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if !tx.committed {
				t.Error("a valid approval did not commit")
			}
		})
	}
}

func TestApprovalCreate_FailedStepsAbortTheDecision(t *testing.T) {
	// Each of these is a statement inside the transaction. None of them may
	// leave a committed decision behind, because a half-applied approval is a
	// run whose recorded state and whose actual state disagree.
	for _, tc := range []struct {
		failOn string
		want   string
	}{
		{"CreateApproval", "create approval"},
		{"ReclaimWorkspaceForRun", "claim workspace for approved run"},
		{"MarkRunApproved", "transition approved run"},
		{"EnqueueApply", "enqueue apply job"},
		{"CreateAuditLog", "write approval audit"},
		{"Commit", "commit approval tx"},
	} {
		t.Run(tc.failOn, func(t *testing.T) {
			tx := &fakeApprovalTx{run: plannedRun(), failOn: tc.failOn}
			_, err := approveWith(approvalWithFaults(tx, nil))

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to name %q", err, tc.want)
			}
			if !errors.Is(err, errInjected) {
				t.Errorf("err dropped its cause: %v", err)
			}
			if tx.committed {
				t.Error("committed despite a failed step")
			}
			if !tx.rolledBack {
				t.Error("did not roll back")
			}
		})
	}
}

func TestApprovalCreate_AuditFailureAbortsAnOtherwiseCompleteDecision(t *testing.T) {
	// The sharpest case, and the reason the audit row is written on this
	// transaction rather than after it. By the time the audit write runs, the
	// approval row exists, the workspace is claimed, the run says 'queued' and
	// the apply job is enqueued — everything the operator asked for. If the
	// audit write were best-effort, a gated production apply would run with no
	// record of who released it.
	tx := &fakeApprovalTx{run: plannedRun(), failOn: "CreateAuditLog"}
	_, err := approveWith(approvalWithFaults(tx, nil))

	if err == nil {
		t.Fatal("a decision with no audit record reported success")
	}
	if !tx.did("EnqueueApply") {
		t.Fatal("test no longer reaches the audit write after the enqueue")
	}
	if tx.committed {
		t.Error("an unaudited approval was committed")
	}
}

func TestApprovalCreate_ApprovedRunClaimsTheSlotAndEnqueues(t *testing.T) {
	tx := &fakeApprovalTx{run: plannedRun()}
	if _, err := approveWith(approvalWithFaults(tx, nil)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if !tx.did("MarkRunApproved:queued") {
		t.Errorf("run was not transitioned to queued: %v", tx.calls)
	}
	if !tx.did("EnqueueApply") {
		t.Error("approved run did not enqueue its apply")
	}
	if !tx.committed {
		t.Error("decision did not commit")
	}
}

func TestApprovalCreate_LosingTheSlotParksTheRunInsteadOfFailing(t *testing.T) {
	// Documented behaviour: something else took the workspace while the plan
	// was parked. The approval still stands and the run waits as 'pending' for
	// the hand-off — it must not enqueue, because that is the double-apply the
	// claim exists to prevent.
	tx := &fakeApprovalTx{run: plannedRun(), reclaimNoRows: true}
	if _, err := approveWith(approvalWithFaults(tx, nil)); err != nil {
		t.Fatalf("losing the claim is not a failure, got %v", err)
	}

	if !tx.did("MarkRunApproved:pending") {
		t.Errorf("run should park as pending: %v", tx.calls)
	}
	if tx.did("EnqueueApply") {
		t.Error("enqueued an apply while another run holds the workspace")
	}
	if !tx.committed {
		t.Error("the approval itself should still commit")
	}
}

func TestApprovalCreate_RejectionDiscardsTheRunAndNeverEnqueues(t *testing.T) {
	// Rejection fails at the audit write on purpose: it exercises the discard
	// transition without reaching the post-commit hand-off, which needs a real
	// pool. The happy-path rejection is covered by the DB-backed suite.
	tx := &fakeApprovalTx{run: plannedRun(), failOn: "CreateAuditLog"}
	s, _ := approvalWithFaults(tx, nil)
	_, _ = s.Create(context.Background(), "run_1", "ws_1", "org_1", "user_1", "rejected", "no", "", "")

	if !tx.did("UpdateRunStatus:discarded") {
		t.Errorf("rejected run was not discarded: %v", tx.calls)
	}
	if tx.did("EnqueueApply") {
		t.Error("a rejected run enqueued an apply")
	}
	if tx.did("ReclaimWorkspaceForRun") {
		t.Error("a rejected run claimed the workspace slot")
	}
}

func TestApprovalCreate_DiscardFailureAbortsTheRejection(t *testing.T) {
	tx := &fakeApprovalTx{run: plannedRun(), failOn: "UpdateRunStatus"}
	s, _ := approvalWithFaults(tx, nil)
	_, err := s.Create(context.Background(), "run_1", "ws_1", "org_1", "user_1", "rejected", "no", "", "")

	if err == nil || !strings.Contains(err.Error(), "discard run") {
		t.Fatalf("err = %v, want it to name the discard", err)
	}
	if tx.committed {
		t.Error("committed a rejection that never discarded the run")
	}
}

func TestApprovalCreate_RejectionHandsTheWorkspaceOn(t *testing.T) {
	// The full rejection, commit included. The slot must be released and the
	// next pending run claimed, or the workspace stays wedged behind a run that
	// is already discarded until the reaper notices.
	tx := &fakeApprovalTx{run: plannedRun()}
	s, handoff := approvalWithFaults(tx, nil)

	if _, err := s.Create(context.Background(), "run_1", "ws_1", "org_1", "user_1", "rejected", "no", "", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if !tx.committed {
		t.Fatal("rejection did not commit")
	}
	if len(handoff.calls) != 1 || handoff.calls[0] != "ws_1/run_1" {
		t.Errorf("hand-off = %v, want one release of ws_1/run_1", handoff.calls)
	}
}

func TestApprovalCreate_ApprovalDoesNotReleaseTheSlotItJustClaimed(t *testing.T) {
	// The mirror image, and the one that would actually hurt: releasing here
	// would hand the workspace to another run while this one's apply is queued
	// against it.
	tx := &fakeApprovalTx{run: plannedRun()}
	s, handoff := approvalWithFaults(tx, nil)

	if _, err := approve(s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(handoff.calls) != 0 {
		t.Errorf("an approved run released its workspace slot: %v", handoff.calls)
	}
}
