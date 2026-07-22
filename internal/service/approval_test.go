package service_test

import (
	"context"
	"testing"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/service"
)

// TestApprovalServiceCreate covers the approval write path against a real DB:
// the status guard, the queued/discarded transitions, the slot release on
// rejection, and org scoping. The river client is nil, so the apply-enqueue is
// skipped — the transaction + slot handling (the part that needs a DB) still runs.
func TestApprovalServiceCreate(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	svc := service.NewApprovalService(testQueries, testPool, service.NewAuditService(testQueries))
	orgID, userID := seedOrg(t, ctx, "appr")
	wsID := seedWorkspace(t, ctx, orgID, userID)

	t.Run("approve transitions the run to queued", func(t *testing.T) {
		runID := seedPlannedRun(t, ctx, wsID, orgID, userID)
		mustClaim(t, ctx, wsID, orgID, runID)

		ap, err := svc.Create(ctx, runID, wsID, orgID, userID, "approved", "lgtm", "1.2.3.4", "test-agent")
		if err != nil {
			t.Fatalf("approve: %v", err)
		}
		if ap.Status != "approved" {
			t.Errorf("approval.Status = %q, want approved", ap.Status)
		}
		if got := runStatus(t, ctx, runID); got != "queued" {
			t.Errorf("run status = %q, want queued", got)
		}
		// The signature releases an apply, so the run becomes one. Every path
		// that enqueues reads the operation off the row.
		if got := runOperation(t, ctx, runID); got != "apply" {
			t.Errorf("run operation = %q, want apply", got)
		}
		// The decision's audit row is written on the same transaction, so it's
		// present and durable the moment Create returns (SEC: no fire-and-forget).
		if got := auditCount(t, ctx, orgID, "approval", ap.ID); got != 1 {
			t.Errorf("approval audit rows = %d, want 1 (audit must commit with the decision)", got)
		}
		// The approved run proceeds, so it keeps the slot: a different run can't claim.
		other := seedPlannedRun(t, ctx, wsID, orgID, userID)
		if _, err := testQueries.ClaimWorkspaceForRun(ctx, wsID, orgID, other); err == nil {
			t.Errorf("slot should still be held by the approved run")
		}
		// Free it for the next subtest.
		_ = testQueries.ReleaseWorkspaceRun(ctx, wsID, orgID, runID)
	})

	t.Run("reject discards the run and frees the slot", func(t *testing.T) {
		runID := seedPlannedRun(t, ctx, wsID, orgID, userID)
		next := seedPlannedRun(t, ctx, wsID, orgID, userID)
		mustClaim(t, ctx, wsID, orgID, runID)

		if _, err := svc.Create(ctx, runID, wsID, orgID, userID, "rejected", "no", "1.2.3.4", "test-agent"); err != nil {
			t.Fatalf("reject: %v", err)
		}
		if got := runStatus(t, ctx, runID); got != "discarded" {
			t.Errorf("run status = %q, want discarded", got)
		}
		// The slot the rejected run held is released (river nil, so the auto-claim
		// is skipped) — the next pending run can take it.
		if _, err := testQueries.ClaimWorkspaceForRun(ctx, wsID, orgID, next); err != nil {
			t.Fatalf("slot should be free after rejection, got: %v", err)
		}
		_ = testQueries.ReleaseWorkspaceRun(ctx, wsID, orgID, next)
	})

	t.Run("a run not awaiting approval is a conflict", func(t *testing.T) {
		runID := seedPlannedRun(t, ctx, wsID, orgID, userID)
		exec(t, ctx, `UPDATE runs SET status='applied' WHERE id=$1`, runID)

		_, err := svc.Create(ctx, runID, wsID, orgID, userID, "approved", "", "", "")
		if apperr.KindOf(err) != apperr.KindConflict {
			t.Fatalf("want KindConflict, got %v (kind %v)", err, apperr.KindOf(err))
		}
	})

	t.Run("a run in another org is not found", func(t *testing.T) {
		runID := seedPlannedRun(t, ctx, wsID, orgID, userID)
		otherOrg, otherUser := seedOrg(t, ctx, "appr-other")

		_, err := svc.Create(ctx, runID, wsID, otherOrg, otherUser, "approved", "", "", "")
		if apperr.KindOf(err) != apperr.KindNotFound {
			t.Fatalf("want KindNotFound for cross-org, got %v (kind %v)", err, apperr.KindOf(err))
		}
	})

	// The approval route lives under /workspaces/{workspaceID}/runs/{runID}, so
	// the run has to belong to the workspace the caller was authorized on.
	// Approving releases a gated apply; naming another workspace's run must not
	// reach it.
	t.Run("a run on another workspace is not found", func(t *testing.T) {
		runID := seedPlannedRun(t, ctx, wsID, orgID, userID)
		otherWS := seedWorkspace(t, ctx, orgID, userID)

		_, err := svc.Create(ctx, runID, otherWS, orgID, userID, "approved", "", "", "")
		if apperr.KindOf(err) != apperr.KindNotFound {
			t.Fatalf("want KindNotFound for cross-workspace, got %v (kind %v)", err, apperr.KindOf(err))
		}
		if got := runStatus(t, ctx, runID); got != "planned" {
			t.Errorf("run status = %q, want planned (the run must not have been released)", got)
		}
	})

	t.Run("listing approvals of another workspace's run is not found", func(t *testing.T) {
		runID := seedPlannedRun(t, ctx, wsID, orgID, userID)
		otherWS := seedWorkspace(t, ctx, orgID, userID)

		if _, err := svc.List(ctx, runID, otherWS, orgID); apperr.KindOf(err) != apperr.KindNotFound {
			t.Fatalf("want KindNotFound for cross-workspace list, got %v (kind %v)", err, apperr.KindOf(err))
		}
	})
}

// Parking at awaiting_approval releases the workspace's run slot and hands it to
// the next pending run, so by the time an admin signs, something else may be
// running against this workspace's state. The approval has to take the slot like
// every other enqueue path, or the approved apply starts alongside whatever holds
// it — two tofu processes on one workspace, and for a plain-tofu workspace (state
// restored per run from object storage, no backend lock) the later state version
// simply wins.
func TestApprovalWaitsForTheWorkspaceRunSlot(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	svc := service.NewApprovalService(testQueries, testPool, service.NewAuditService(testQueries))
	orgID, userID := seedOrg(t, ctx, "appr-slot")
	wsID := seedWorkspace(t, ctx, orgID, userID)

	parked := seedPlannedRun(t, ctx, wsID, orgID, userID)
	// The slot moved on while the plan waited: another run holds it now.
	other := seedPlannedRun(t, ctx, wsID, orgID, userID)
	mustClaim(t, ctx, wsID, orgID, other)

	if _, err := svc.Create(ctx, parked, wsID, orgID, userID, "approved", "lgtm", "", ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	if got := runStatus(t, ctx, parked); got != "pending" {
		t.Errorf("approved run status = %q, want pending (it waits for the slot)", got)
	}
	// The approval is not lost: the run is an apply now, so the hand-off that
	// runs on the next release enqueues it as one.
	if got := runOperation(t, ctx, parked); got != "apply" {
		t.Errorf("approved run operation = %q, want apply", got)
	}
	if got := currentRunID(t, ctx, wsID); got != other {
		t.Errorf("workspace slot = %q, want the run that already held it (%q)", got, other)
	}

	// Once the holder is done, the approved apply is the oldest pending run and
	// takes the slot — the same claim ClaimAndEnqueueNextRun makes.
	if err := testQueries.ReleaseWorkspaceRun(ctx, wsID, orgID, other); err != nil {
		t.Fatalf("release: %v", err)
	}
	next, err := testQueries.GetNextPendingRun(ctx, wsID)
	if err != nil {
		t.Fatalf("next pending run: %v", err)
	}
	if next.ID != parked {
		t.Fatalf("next pending run = %q, want the approved one (%q)", next.ID, parked)
	}
	if next.Operation != "apply" {
		t.Errorf("next pending run operation = %q, want apply", next.Operation)
	}
	if _, err := testQueries.ClaimWorkspaceForRun(ctx, wsID, orgID, parked); err != nil {
		t.Fatalf("the approved run must be able to claim the freed slot: %v", err)
	}
	_ = testQueries.ReleaseWorkspaceRun(ctx, wsID, orgID, parked)
}

// A plan whose slot release failed still holds its own slot when the admin
// signs. Re-claiming for the run that already holds it grants nothing — nothing
// else can be running — so the approval proceeds instead of parking the run
// behind itself forever.
func TestApprovalReclaimsTheSlotItAlreadyHolds(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	svc := service.NewApprovalService(testQueries, testPool, service.NewAuditService(testQueries))
	orgID, userID := seedOrg(t, ctx, "appr-selfslot")
	wsID := seedWorkspace(t, ctx, orgID, userID)

	runID := seedPlannedRun(t, ctx, wsID, orgID, userID)
	mustClaim(t, ctx, wsID, orgID, runID)

	if _, err := svc.Create(ctx, runID, wsID, orgID, userID, "approved", "", "", ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got := runStatus(t, ctx, runID); got != "queued" {
		t.Errorf("run status = %q, want queued", got)
	}
	if got := currentRunID(t, ctx, wsID); got != runID {
		t.Errorf("workspace slot = %q, want the approved run (%q)", got, runID)
	}
	_ = testQueries.ReleaseWorkspaceRun(ctx, wsID, orgID, runID)
}

// runOperation reads a run's stored operation — what every enqueue path runs.
func runOperation(t *testing.T, ctx context.Context, runID string) string {
	t.Helper()
	var operation string
	if err := testPool.QueryRow(ctx, `SELECT operation FROM runs WHERE id=$1`, runID).Scan(&operation); err != nil {
		t.Fatalf("read run operation: %v", err)
	}
	return operation
}

// currentRunID reads which run holds a workspace's single run slot.
func currentRunID(t *testing.T, ctx context.Context, wsID string) string {
	t.Helper()
	var held *string
	if err := testPool.QueryRow(ctx, `SELECT current_run_id FROM workspaces WHERE id=$1`, wsID).Scan(&held); err != nil {
		t.Fatalf("read workspace slot: %v", err)
	}
	if held == nil {
		return ""
	}
	return *held
}

// auditCount returns how many audit_logs rows exist for a given entity — used to
// prove the approval decision's audit row is written transactionally, not
// fire-and-forget.
func auditCount(t *testing.T, ctx context.Context, orgID, entityType, entityID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs WHERE org_id=$1 AND entity_type=$2 AND entity_id=$3`,
		orgID, entityType, entityID).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}
