// Integration tests for the hand-written repository layer, run against a real
// Postgres. They cover the things unit tests can't: that org_id scoping actually
// isolates tenants (a dropped filter is a cross-tenant leak), and that the
// concurrency-sensitive queries (the workspace run-claim, the batched tenant
// upsert) behave as designed.
//
// They need a database. Set TEST_DATABASE_URL to a server the test can create a
// scratch database on (it connects to it, CREATE DATABASE portal_repo_test,
// migrates, and drops it afterward). With no reachable server the whole package
// skips, so `go test ./...` stays green without one.
package repository_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/repository"
)

var (
	testPool    *pgxpool.Pool
	testQueries *repository.Queries
)

func TestMain(m *testing.M) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		base = "postgres://portal:portal@localhost:5432/postgres?sslmode=disable"
	}
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		// No reachable server — leave testPool nil; requireDB() skips each test.
		os.Exit(m.Run())
	}
	const dbName = "portal_repo_test"
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		admin.Close(ctx)
		panic("create test database: " + err.Error())
	}
	admin.Close(ctx)

	dbURL, err := withDatabase(base, dbName)
	if err != nil {
		panic(err)
	}

	// Migrate via the same golang-migrate source the binary uses; path resolved
	// off this file so it works regardless of the test's working directory.
	_, thisFile, _, _ := runtime.Caller(0)
	sourceURL := "file://" + filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
	mig, err := migrate.New(sourceURL, dbURL)
	if err != nil {
		panic("migrate init: " + err.Error())
	}
	if err := mig.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		panic("migrate up: " + err.Error())
	}
	mig.Close()

	testPool, err = pgxpool.New(ctx, dbURL)
	if err != nil {
		panic("connect test pool: " + err.Error())
	}
	testQueries = repository.New(testPool)

	code := m.Run()

	testPool.Close()
	if admin2, err := pgx.Connect(ctx, base); err == nil {
		_, _ = admin2.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName)
		admin2.Close(ctx)
	}
	os.Exit(code)
}

func requireDB(t *testing.T) {
	t.Helper()
	if testPool == nil {
		t.Skip("no test database (set TEST_DATABASE_URL)")
	}
}

func withDatabase(base, dbName string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

func id() string { return ulid.Make().String() }

// seedOrg creates an organization + an owner user and returns their ids.
func seedOrg(t *testing.T, ctx context.Context, slug string) (orgID, userID string) {
	t.Helper()
	orgID, userID = id(), id()
	// orgID/userID are full ULIDs (globally unique); use them for the unique
	// slug/email rather than a ULID prefix, which is the shared timestamp.
	exec(t, ctx, `INSERT INTO organizations (id,name,slug) VALUES ($1,$2,$3)`, orgID, slug, slug+"-"+orgID)
	exec(t, ctx, `INSERT INTO users (id,org_id,email,name,role) VALUES ($1,$2,$3,'U','owner')`, userID, orgID, userID+"@t.local")
	return orgID, userID
}

func seedWorkspace(t *testing.T, ctx context.Context, orgID, userID string) string {
	t.Helper()
	wsID := id()
	exec(t, ctx, `INSERT INTO workspaces (id,org_id,name,created_by) VALUES ($1,$2,$3,$4)`, wsID, orgID, "ws-"+id()[:8], userID)
	return wsID
}

func seedRun(t *testing.T, ctx context.Context, wsID, orgID, userID string) string {
	t.Helper()
	runID := id()
	exec(t, ctx, `INSERT INTO runs (id,workspace_id,org_id,operation,status,created_by) VALUES ($1,$2,$3,'plan','pending',$4)`, runID, wsID, orgID, userID)
	return runID
}

func exec(t *testing.T, ctx context.Context, sql string, args ...any) {
	t.Helper()
	if _, err := testPool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("setup exec failed: %v\nsql: %s", err, sql)
	}
}

// TestWorkspaceGetIsOrgScoped is the security-critical assertion: a workspace is
// invisible to another org even by exact id.
func TestWorkspaceGetIsOrgScoped(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgA, userA := seedOrg(t, ctx, "a")
	orgB, _ := seedOrg(t, ctx, "b")
	wsID := seedWorkspace(t, ctx, orgA, userA)

	if _, err := testQueries.GetWorkspace(ctx, repository.GetWorkspaceParams{ID: wsID, OrgID: orgA}); err != nil {
		t.Fatalf("owner org should see its workspace, got: %v", err)
	}
	if _, err := testQueries.GetWorkspace(ctx, repository.GetWorkspaceParams{ID: wsID, OrgID: orgB}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-org GetWorkspace must return no rows, got: %v", err)
	}
}

// TestUpdateUserRoleIsOrgScoped guards the authz fix: an owner in one org can't
// re-role a user in another by id.
func TestUpdateUserRoleIsOrgScoped(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgA, _ := seedOrg(t, ctx, "a")
	orgB, userB := seedOrg(t, ctx, "b")

	// Caller in org A targets userB (org B) — must affect no row.
	if _, err := testQueries.UpdateUserRole(ctx, repository.UpdateUserRoleParams{ID: userB, Role: "admin", OrgID: orgA}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-org UpdateUserRole must affect no row, got: %v", err)
	}
	// Correct org succeeds.
	if _, err := testQueries.UpdateUserRole(ctx, repository.UpdateUserRoleParams{ID: userB, Role: "admin", OrgID: orgB}); err != nil {
		t.Fatalf("same-org UpdateUserRole should succeed, got: %v", err)
	}
}

// TestWorkspaceRunClaim covers the run-serialization slot: only one run can hold
// it, and only the holder can release it.
func TestWorkspaceRunClaim(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "claim")
	wsID := seedWorkspace(t, ctx, orgID, userID)
	r1 := seedRun(t, ctx, wsID, orgID, userID)
	r2 := seedRun(t, ctx, wsID, orgID, userID)

	if _, err := testQueries.ClaimWorkspaceForRun(ctx, wsID, orgID, r1); err != nil {
		t.Fatalf("first claim should win, got: %v", err)
	}
	if _, err := testQueries.ClaimWorkspaceForRun(ctx, wsID, orgID, r2); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second claim while held must return no rows, got: %v", err)
	}
	// A non-holder release must not free the slot.
	if err := testQueries.ReleaseWorkspaceRun(ctx, wsID, orgID, r2); err != nil {
		t.Fatalf("release (no-op) errored: %v", err)
	}
	if _, err := testQueries.ClaimWorkspaceForRun(ctx, wsID, orgID, r2); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("slot should still be held by r1 after a non-holder release, got: %v", err)
	}
	// The holder releases; now r2 can claim.
	if err := testQueries.ReleaseWorkspaceRun(ctx, wsID, orgID, r1); err != nil {
		t.Fatalf("holder release errored: %v", err)
	}
	if _, err := testQueries.ClaimWorkspaceForRun(ctx, wsID, orgID, r2); err != nil {
		t.Fatalf("r2 should claim the freed slot, got: %v", err)
	}
}

// TestReapStaleRunSlots covers the self-heal that frees a run slot wedged by a
// discarded/dead job: a fresh active run is left alone, a terminal or long-stale
// run's slot is freed.
func TestReapStaleRunSlots(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "reap")
	ws := seedWorkspace(t, ctx, orgID, userID)
	r1 := seedRun(t, ctx, ws, orgID, userID)

	contains := func(ids []string, want string) bool {
		for _, id := range ids {
			if id == want {
				return true
			}
		}
		return false
	}

	// A fresh, active (pending) run holds the slot — must NOT be reaped.
	if _, err := testQueries.ClaimWorkspaceForRun(ctx, ws, orgID, r1); err != nil {
		t.Fatalf("claim: %v", err)
	}
	freed, err := testQueries.ReapStaleRunSlots(ctx)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if contains(freed, ws) {
		t.Fatalf("a fresh active run's slot must not be reaped")
	}

	// The held run reaches a terminal status → its slot is reapable now.
	exec(t, ctx, `UPDATE runs SET status='errored' WHERE id=$1`, r1)
	freed, err = testQueries.ReapStaleRunSlots(ctx)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if !contains(freed, ws) {
		t.Fatalf("a terminal run's slot must be reaped; freed=%v", freed)
	}
	if _, err := testQueries.ClaimWorkspaceForRun(ctx, ws, orgID, r1); err != nil {
		t.Fatalf("re-claim after reap should succeed: %v", err)
	}

	// A still-"active" run that hasn't been touched in hours (its job died) is
	// also reaped.
	exec(t, ctx, `UPDATE runs SET status='planning', updated_at = NOW() - INTERVAL '4 hours' WHERE id=$1`, r1)
	freed, err = testQueries.ReapStaleRunSlots(ctx)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if !contains(freed, ws) {
		t.Fatalf("a long-stale active run's slot must be reaped; freed=%v", freed)
	}
}

// TestBatchUpsertTenants covers the reconcile batch: last_observed_at advances
// every tick, but updated_at only moves on a real content change.
func TestBatchUpsertTenants(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "tnt")
	acctID := id()
	exec(t, ctx, `INSERT INTO accounts (id,org_id,name,aws_account_id,assume_role_arn,default_region,created_by) VALUES ($1,$2,'acc','111111111111','arn:aws:iam::111111111111:role/x','us-west-2',$3)`, acctID, orgID, userID)
	clID := id()
	exec(t, ctx, `INSERT INTO clusters (id,org_id,account_id,name,api_endpoint,ca_bundle_encrypted,sa_token_encrypted,region,created_by) VALUES ($1,$2,$3,'cl','https://x','x','x','us-west-2',$4)`, clID, orgID, acctID, userID)

	batch := func(t0 time.Time, phaseA string) {
		err := testQueries.BatchUpsertTenants(ctx, repository.BatchUpsertTenantsParams{
			OrgID: orgID, ClusterID: clID, LastObservedAt: t0,
			IDs:      []string{id(), id()},
			Names:    []string{"t-a", "t-b"},
			Phases:   []string{phaseA, "Pending"},
			Specs:    []string{`{"k":1}`, `{}`},
			Statuses: []string{`{}`, `{}`},
		})
		if err != nil {
			t.Fatalf("batch upsert: %v", err)
		}
	}

	now := time.Now()
	batch(now, "Ready")

	var updatedAfterFirst, observedAfterFirst time.Time
	if err := testPool.QueryRow(ctx, `SELECT updated_at, last_observed_at FROM tenants WHERE cluster_id=$1 AND name='t-a'`, clID).Scan(&updatedAfterFirst, &observedAfterFirst); err != nil {
		t.Fatalf("read t-a: %v", err)
	}

	// Re-upsert identical content, later observe time: last_observed_at moves,
	// updated_at must not.
	batch(now.Add(time.Minute), "Ready")
	var updated2, observed2 time.Time
	testPool.QueryRow(ctx, `SELECT updated_at, last_observed_at FROM tenants WHERE cluster_id=$1 AND name='t-a'`, clID).Scan(&updated2, &observed2)
	if !updated2.Equal(updatedAfterFirst) {
		t.Errorf("updated_at moved on an unchanged tenant: %v -> %v", updatedAfterFirst, updated2)
	}
	if !observed2.After(observedAfterFirst) {
		t.Errorf("last_observed_at should advance every tick: %v -> %v", observedAfterFirst, observed2)
	}

	// A real phase change must move updated_at.
	batch(now.Add(2*time.Minute), "Degraded")
	var updated3 time.Time
	testPool.QueryRow(ctx, `SELECT updated_at FROM tenants WHERE cluster_id=$1 AND name='t-a'`, clID).Scan(&updated3)
	if !updated3.After(updated2) {
		t.Errorf("updated_at should move on a phase change: %v -> %v", updated2, updated3)
	}
}

// TestExpireClusterOperation covers the watch-back's stuck-provision reap: a
// committed op is marked failed with a reason, and the WHERE status='committed'
// guard leaves a non-committed (e.g. already active) op untouched so the reap
// can't race the active flip.
func TestExpireClusterOperation(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "expire")

	seedOp := func(status string) string {
		opID := id()
		exec(t, ctx, `INSERT INTO cluster_operations (id, org_id, name, environment, team, operation, status, spec_json, created_by)
			VALUES ($1,$2,$3,'development','platform','provision'::cluster_op_kind,$4::cluster_op_status,'{}'::jsonb,$5)`,
			opID, orgID, "cl-"+opID, status, userID)
		return opID
	}
	read := func(opID string) (status, errMsg string) {
		if err := testPool.QueryRow(ctx, `SELECT status::text, error FROM cluster_operations WHERE id=$1`, opID).Scan(&status, &errMsg); err != nil {
			t.Fatalf("read op: %v", err)
		}
		return
	}

	// A committed provision is reaped → failed + reason recorded.
	committed := seedOp("committed")
	if err := testQueries.ExpireClusterOperation(ctx, repository.ExpireClusterOperationParams{
		ID: committed, OrgID: orgID, Error: "expired: never applied", CompletedAt: time.Now(),
	}); err != nil {
		t.Fatalf("expire committed: %v", err)
	}
	if st, msg := read(committed); st != "failed" || msg != "expired: never applied" {
		t.Errorf("committed op = (%q, %q), want (failed, reason)", st, msg)
	}

	// Guard: an already-active op must not be reaped (status != committed).
	active := seedOp("active")
	if err := testQueries.ExpireClusterOperation(ctx, repository.ExpireClusterOperationParams{
		ID: active, OrgID: orgID, Error: "should-not-apply", CompletedAt: time.Now(),
	}); err != nil {
		t.Fatalf("expire active: %v", err)
	}
	if st, msg := read(active); st != "active" || msg != "" {
		t.Errorf("active op must be untouched, got (%q, %q)", st, msg)
	}
}

// seedTeamMember creates a team in an org and puts a user in it.
func seedTeamMember(t *testing.T, ctx context.Context, orgID, userID string) string {
	t.Helper()
	teamID := id()
	exec(t, ctx, `INSERT INTO teams (id,org_id,name,slug) VALUES ($1,$2,$3,$4)`, teamID, orgID, "team-"+teamID[:8], "team-"+teamID)
	exec(t, ctx, `INSERT INTO team_members (id,team_id,user_id,role) VALUES ($1,$2,$3,'viewer')`, id(), teamID, userID)
	return teamID
}

// TestGetWorkspaceTeamRole covers the read side of workspace_team_access, the
// query a workspace-scoped gate consults before deciding a request. The table
// carries no org_id of its own, so the org scoping has to come from the joins.
func TestGetWorkspaceTeamRole(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgA, userA := seedOrg(t, ctx, "a")
	wsID := seedWorkspace(t, ctx, orgA, userA)

	// No grant anywhere: an empty role, not an error, so the caller falls back
	// to the org role rather than treating this as a lookup failure.
	role, err := testQueries.GetWorkspaceTeamRole(ctx, wsID, userA, orgA)
	if err != nil {
		t.Fatalf("ungranted workspace should not error, got: %v", err)
	}
	if role != "" {
		t.Fatalf("ungranted workspace returned role %q, want empty", role)
	}

	teamID := seedTeamMember(t, ctx, orgA, userA)
	exec(t, ctx, `INSERT INTO workspace_team_access (id,workspace_id,team_id,role) VALUES ($1,$2,$3,'operator')`, id(), wsID, teamID)

	if role, err = testQueries.GetWorkspaceTeamRole(ctx, wsID, userA, orgA); err != nil || role != "operator" {
		t.Fatalf("granted workspace returned (%q, %v), want operator", role, err)
	}

	// A second team grants more: the highest grant wins, not the newest.
	higherTeam := seedTeamMember(t, ctx, orgA, userA)
	exec(t, ctx, `INSERT INTO workspace_team_access (id,workspace_id,team_id,role) VALUES ($1,$2,$3,'admin')`, id(), wsID, higherTeam)
	if role, err = testQueries.GetWorkspaceTeamRole(ctx, wsID, userA, orgA); err != nil || role != "admin" {
		t.Fatalf("two grants returned (%q, %v), want the higher one (admin)", role, err)
	}

	// A caller in another org reads no grant even with the exact workspace id.
	orgB, userB := seedOrg(t, ctx, "b")
	if role, err = testQueries.GetWorkspaceTeamRole(ctx, wsID, userA, orgB); err != nil || role != "" {
		t.Fatalf("cross-org read returned (%q, %v), want empty", role, err)
	}
	// And a user who is in no granted team reads nothing either.
	if role, err = testQueries.GetWorkspaceTeamRole(ctx, wsID, userB, orgA); err != nil || role != "" {
		t.Fatalf("non-member read returned (%q, %v), want empty", role, err)
	}
}
