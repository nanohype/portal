package service_test

import (
	"context"
	"testing"

	"github.com/nanohype/portal/internal/service"
)

// auditRows returns the audit rows written for an entity, newest first.
func auditRows(t *testing.T, ctx context.Context, entityID string) []struct {
	Action     string
	BeforeData string
	AfterData  string
} {
	t.Helper()
	rows, err := testPool.Query(ctx,
		`SELECT action, COALESCE(before_data::text, ''), COALESCE(after_data::text, '')
		   FROM audit_logs WHERE entity_id = $1 ORDER BY created_at DESC`, entityID)
	if err != nil {
		t.Fatalf("query audit_logs: %v", err)
	}
	defer rows.Close()

	var out []struct {
		Action     string
		BeforeData string
		AfterData  string
	}
	for rows.Next() {
		var r struct {
			Action     string
			BeforeData string
			AfterData  string
		}
		if err := rows.Scan(&r.Action, &r.BeforeData, &r.AfterData); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func TestAuditServiceLog(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "audit")
	svc := service.NewAuditService(testQueries)

	t.Run("writes the entry with both payloads", func(t *testing.T) {
		entity := id()
		svc.Log(ctx, service.AuditEntry{
			OrgID: orgID, UserID: userID,
			Action: "workspace.update", EntityType: "workspace", EntityID: entity,
			Before: map[string]string{"name": "old"},
			After:  map[string]string{"name": "new"},
		})

		rows := auditRows(t, ctx, entity)
		if len(rows) != 1 {
			t.Fatalf("expected one audit row, got %d", len(rows))
		}
		if rows[0].Action != "workspace.update" {
			t.Errorf("action: got %q", rows[0].Action)
		}
		if rows[0].BeforeData == "" || rows[0].AfterData == "" {
			t.Errorf("payloads not stored: before=%q after=%q", rows[0].BeforeData, rows[0].AfterData)
		}
	})

	t.Run("nil payloads store as JSON null", func(t *testing.T) {
		entity := id()
		svc.Log(ctx, service.AuditEntry{
			OrgID: orgID, UserID: userID,
			Action: "workspace.create", EntityType: "workspace", EntityID: entity,
		})

		rows := auditRows(t, ctx, entity)
		if len(rows) != 1 {
			t.Fatalf("expected one audit row, got %d", len(rows))
		}
		if rows[0].BeforeData != "null" || rows[0].AfterData != "null" {
			t.Errorf("nil payload should store as null: before=%q after=%q", rows[0].BeforeData, rows[0].AfterData)
		}
	})

	// An unserialisable payload must not cost the audit row. The entry still
	// records who did what to which entity; only the diff degrades to null. The
	// opposite behaviour — dropping the row because a payload would not marshal —
	// would let a caller erase its own audit trail by passing an odd value.
	t.Run("an unmarshalable payload degrades to null and still writes", func(t *testing.T) {
		entity := id()
		svc.Log(ctx, service.AuditEntry{
			OrgID: orgID, UserID: userID,
			Action: "workspace.update", EntityType: "workspace", EntityID: entity,
			Before: make(chan int), // channels have no JSON representation
			After:  func() {},      // nor do funcs
		})

		rows := auditRows(t, ctx, entity)
		if len(rows) != 1 {
			t.Fatalf("an unmarshalable payload dropped the audit row entirely; got %d rows", len(rows))
		}
		if rows[0].BeforeData != "null" || rows[0].AfterData != "null" {
			t.Errorf("unmarshalable payloads should degrade to null: before=%q after=%q", rows[0].BeforeData, rows[0].AfterData)
		}
	})

	// Log is documented as best-effort: it is used after a mutation has already
	// committed, so a failed audit write is logged and swallowed rather than
	// panicking into a caller that has nothing left to roll back. (Decisions that
	// must not stand without their audit row use LogTx instead, inside the
	// mutation's own transaction — see TestAuditServiceLogTx.)
	t.Run("a rejected insert does not panic the caller", func(t *testing.T) {
		svc.Log(ctx, service.AuditEntry{
			OrgID: "org-that-does-not-exist", UserID: userID,
			Action: "workspace.update", EntityType: "workspace", EntityID: id(),
		})
	})
}

// TestAuditServiceLogTx pins the difference that matters between the two entry
// points: LogTx returns the error so the surrounding transaction can abort, and
// its row is rolled back with that transaction rather than surviving it.
func TestAuditServiceLogTx(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "audit-tx")
	svc := service.NewAuditService(testQueries)

	t.Run("returns the error instead of swallowing it", func(t *testing.T) {
		tx, err := testPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		err = svc.LogTx(ctx, testQueries.WithTx(tx), service.AuditEntry{
			OrgID: "org-that-does-not-exist", UserID: userID,
			Action: "run.approve", EntityType: "run", EntityID: id(),
		})
		if err == nil {
			t.Fatal("LogTx swallowed a failed insert; the caller cannot abort on it")
		}
	})

	t.Run("rolls back with its transaction", func(t *testing.T) {
		entity := id()
		tx, err := testPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := svc.LogTx(ctx, testQueries.WithTx(tx), service.AuditEntry{
			OrgID: orgID, UserID: userID,
			Action: "run.approve", EntityType: "run", EntityID: entity,
		}); err != nil {
			t.Fatalf("LogTx: %v", err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatalf("rollback: %v", err)
		}

		if rows := auditRows(t, ctx, entity); len(rows) != 0 {
			t.Errorf("audit row survived the rollback of the transaction it recorded: %+v", rows)
		}
	})
}
