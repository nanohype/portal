package service_test

import (
	"context"
	"testing"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/service"
)

// TestWorkspaceVariableCopy covers the two transactional copy paths against a
// real DB: CopyAll (clone — create every source var into a fresh target) and
// CopyInto (the explicit copy action — upsert by key+category).
func TestWorkspaceVariableCopy(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	svc := service.NewWorkspaceService(testQueries, testPool, nil, nil)
	orgID, userID := seedOrg(t, ctx, "wvcopy")

	t.Run("CopyAll clones every variable", func(t *testing.T) {
		src := seedWorkspace(t, ctx, orgID, userID)
		dst := seedWorkspace(t, ctx, orgID, userID)
		seedVar(t, ctx, src, orgID, "region", `"us-west-2"`, "terraform")
		seedVar(t, ctx, src, orgID, "TOKEN", "ciphertext", "env")

		n, err := svc.CopyAll(ctx, src, dst, orgID)
		if err != nil {
			t.Fatalf("CopyAll: %v", err)
		}
		if n != 2 {
			t.Errorf("copied %d, want 2", n)
		}
		got := listVarMap(t, ctx, dst, orgID)
		if got["region"] != `"us-west-2"` || got["TOKEN"] != "ciphertext" {
			t.Errorf("target vars = %v", got)
		}
	})

	t.Run("CopyAll from an empty source copies nothing", func(t *testing.T) {
		src := seedWorkspace(t, ctx, orgID, userID)
		dst := seedWorkspace(t, ctx, orgID, userID)
		n, err := svc.CopyAll(ctx, src, dst, orgID)
		if err != nil {
			t.Fatalf("CopyAll: %v", err)
		}
		if n != 0 {
			t.Errorf("copied %d, want 0", n)
		}
	})

	t.Run("CopyInto upserts by key+category", func(t *testing.T) {
		src := seedWorkspace(t, ctx, orgID, userID)
		dst := seedWorkspace(t, ctx, orgID, userID)
		seedVar(t, ctx, dst, orgID, "shared", "old", "terraform") // already in target → updated
		seedVar(t, ctx, src, orgID, "shared", "new", "terraform")
		seedVar(t, ctx, src, orgID, "fresh", "created", "terraform") // not in target → created

		affected, err := svc.CopyInto(ctx, src, dst, orgID)
		if err != nil {
			t.Fatalf("CopyInto: %v", err)
		}
		if len(affected) != 2 {
			t.Errorf("affected %d, want 2", len(affected))
		}
		got := listVarMap(t, ctx, dst, orgID)
		if got["shared"] != "new" {
			t.Errorf("shared = %q, want %q (updated)", got["shared"], "new")
		}
		if got["fresh"] != "created" {
			t.Errorf("fresh = %q, want %q (created)", got["fresh"], "created")
		}
		if len(got) != 2 {
			t.Errorf("target should have exactly 2 vars, got %d: %v", len(got), got)
		}
	})

	t.Run("CopyInto from an empty source is a validation error", func(t *testing.T) {
		src := seedWorkspace(t, ctx, orgID, userID)
		dst := seedWorkspace(t, ctx, orgID, userID)
		if _, err := svc.CopyInto(ctx, src, dst, orgID); apperr.KindOf(err) != apperr.KindValidation {
			t.Fatalf("want KindValidation, got %v (kind %v)", err, apperr.KindOf(err))
		}
	})
}
