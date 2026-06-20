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

		ap, err := svc.Create(ctx, runID, orgID, userID, "approved", "lgtm", "1.2.3.4", "test-agent")
		if err != nil {
			t.Fatalf("approve: %v", err)
		}
		if ap.Status != "approved" {
			t.Errorf("approval.Status = %q, want approved", ap.Status)
		}
		if got := runStatus(t, ctx, runID); got != "queued" {
			t.Errorf("run status = %q, want queued", got)
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

		if _, err := svc.Create(ctx, runID, orgID, userID, "rejected", "no", "1.2.3.4", "test-agent"); err != nil {
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

		_, err := svc.Create(ctx, runID, orgID, userID, "approved", "", "", "")
		if apperr.KindOf(err) != apperr.KindConflict {
			t.Fatalf("want KindConflict, got %v (kind %v)", err, apperr.KindOf(err))
		}
	})

	t.Run("a run in another org is not found", func(t *testing.T) {
		runID := seedPlannedRun(t, ctx, wsID, orgID, userID)
		otherOrg, otherUser := seedOrg(t, ctx, "appr-other")

		_, err := svc.Create(ctx, runID, otherOrg, otherUser, "approved", "", "", "")
		if apperr.KindOf(err) != apperr.KindNotFound {
			t.Fatalf("want KindNotFound for cross-org, got %v (kind %v)", err, apperr.KindOf(err))
		}
	})
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
