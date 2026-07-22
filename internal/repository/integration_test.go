// Integration tests for the hand-written repository layer, run against a real
// Postgres. They cover the things unit tests can't: that org_id scoping actually
// isolates tenants (a dropped filter is a cross-tenant leak), and that the
// concurrency-sensitive queries (the workspace run-claim, the batched tenant
// upsert) behave as designed.
//
// They need a database. Set TEST_DATABASE_URL to a server the test can create a
// scratch database on (it connects to it, CREATE DATABASE portal_repo_test,
// migrates, and drops it afterward). Without it the package falls back to the
// dev default and skips when nothing is listening, so `go test ./...` stays
// green on a machine with no Postgres. Setting it and having it not answer is a
// hard failure, not a skip — a tier that silently didn't run must not report
// green.
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
	// An explicitly set TEST_DATABASE_URL is a caller saying "run the DB tier
	// against this" — CI does exactly that. Falling back to skips when that
	// server turns out to be unreachable would report a green run for tests that
	// never executed, so an unusable explicit URL is fatal below. The dev default
	// is a guess, and a guess that misses is allowed to skip.
	base, pinned := os.LookupEnv("TEST_DATABASE_URL")
	if base == "" {
		base = "postgres://portal:portal@localhost:5432/postgres?sslmode=disable"
		pinned = false
	}
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		if pinned {
			panic("TEST_DATABASE_URL is set but unreachable, refusing to skip the DB tier: " + err.Error())
		}
		os.Exit(m.Run()) // no server and none asked for — DB tests skip via requireDB
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
	// Name off the full ULID: workspaces are unique on (org_id, name), and a
	// ULID prefix is the shared timestamp, so two workspaces seeded in the same
	// org in the same millisecond would collide.
	exec(t, ctx, `INSERT INTO workspaces (id,org_id,name,created_by) VALUES ($1,$2,$3,$4)`, wsID, orgID, "ws-"+wsID, userID)
	return wsID
}

func seedRun(t *testing.T, ctx context.Context, wsID, orgID, userID string) string {
	t.Helper()
	runID := id()
	exec(t, ctx, `INSERT INTO runs (id,workspace_id,org_id,operation,status,created_by) VALUES ($1,$2,$3,'plan','pending',$4)`, runID, wsID, orgID, userID)
	return runID
}

// currentRunID reads the workspace's run slot, "" when it is free.
func currentRunID(t *testing.T, ctx context.Context, wsID string) string {
	t.Helper()
	var current *string
	if err := testPool.QueryRow(ctx, `SELECT current_run_id FROM workspaces WHERE id = $1`, wsID).Scan(&current); err != nil {
		t.Fatalf("read workspace slot: %v", err)
	}
	if current == nil {
		return ""
	}
	return *current
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

// TestWorkspaceRunClaimRefusesItsOwnHolder pins the exclusion the enqueue paths
// are built on: the claim succeeds for at most one caller per held slot, and
// that includes a second caller naming the run that already holds it.
//
// The hand-off (ClaimAndEnqueueNextRun) reads the oldest pending run without a
// row lock, so two callers routinely reach the claim with the same run id. Only
// the predicate separates them — Postgres re-checks it against the committed row
// when the second caller gets the workspace lock, and "the slot is free" is the
// only condition that fails there. Accept the caller's own run id and both
// enqueue it: two River jobs, no dedupe, two tofu processes on one state file.
func TestWorkspaceRunClaimRefusesItsOwnHolder(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "claim-self")
	wsID := seedWorkspace(t, ctx, orgID, userID)
	r1 := seedRun(t, ctx, wsID, orgID, userID)

	if _, err := testQueries.ClaimWorkspaceForRun(ctx, wsID, orgID, r1); err != nil {
		t.Fatalf("first claim should win, got: %v", err)
	}
	if _, err := testQueries.ClaimWorkspaceForRun(ctx, wsID, orgID, r1); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("re-claiming a held slot for its own holder must return no rows, got: %v", err)
	}
}

// TestReclaimWorkspaceForRun covers the approval path's widened claim: it takes
// a free slot, takes back one the same run already holds — the case a plan whose
// release failed would otherwise be parked behind forever — and still refuses a
// slot held by anything else.
func TestReclaimWorkspaceForRun(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "reclaim")
	wsID := seedWorkspace(t, ctx, orgID, userID)
	r1 := seedRun(t, ctx, wsID, orgID, userID)
	r2 := seedRun(t, ctx, wsID, orgID, userID)

	if _, err := testQueries.ReclaimWorkspaceForRun(ctx, wsID, orgID, r1); err != nil {
		t.Fatalf("reclaim of a free slot should win, got: %v", err)
	}
	if _, err := testQueries.ReclaimWorkspaceForRun(ctx, wsID, orgID, r1); err != nil {
		t.Fatalf("the holder must be able to re-take its own slot, got: %v", err)
	}
	if _, err := testQueries.ReclaimWorkspaceForRun(ctx, wsID, orgID, r2); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("reclaim by a different run must return no rows, got: %v", err)
	}
	if got := currentRunID(t, ctx, wsID); got != r1 {
		t.Fatalf("workspace slot = %q, want the original holder (%q)", got, r1)
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
// seedTeamMember creates a team and puts the user in it with the given role
// within the team. That in-team role caps any grant the team holds, so it is a
// parameter rather than a fixed value.
func seedTeamMember(t *testing.T, ctx context.Context, orgID, userID, memberRole string) string {
	t.Helper()
	teamID := id()
	exec(t, ctx, `INSERT INTO teams (id,org_id,name,slug) VALUES ($1,$2,$3,$4)`, teamID, orgID, "team-"+teamID, "team-"+teamID)
	exec(t, ctx, `INSERT INTO team_members (id,team_id,user_id,role) VALUES ($1,$2,$3,$4)`, id(), teamID, userID, memberRole)
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

	teamID := seedTeamMember(t, ctx, orgA, userA, "operator")
	exec(t, ctx, `INSERT INTO workspace_team_access (id,workspace_id,team_id,role) VALUES ($1,$2,$3,'operator')`, id(), wsID, teamID)

	if role, err = testQueries.GetWorkspaceTeamRole(ctx, wsID, userA, orgA); err != nil || role != "operator" {
		t.Fatalf("granted workspace returned (%q, %v), want operator", role, err)
	}

	// The in-team role caps the grant. A team granted operator hands operator to
	// its operators and viewer to its viewers — the members panel and the
	// authority a member actually holds say the same thing.
	cappedUser := id()
	exec(t, ctx, `INSERT INTO users (id,org_id,email,name,role) VALUES ($1,$2,$3,'U','viewer')`, cappedUser, orgA, cappedUser+"@t.local")
	exec(t, ctx, `INSERT INTO team_members (id,team_id,user_id,role) VALUES ($1,$2,$3,'viewer')`, id(), teamID, cappedUser)
	if role, err = testQueries.GetWorkspaceTeamRole(ctx, wsID, cappedUser, orgA); err != nil || role != "viewer" {
		t.Fatalf("viewer in an operator-granted team returned (%q, %v), want viewer", role, err)
	}

	// The cap never raises: an admin within a team granted only viewer still
	// picks up viewer from the grant.
	lowGrantTeam := seedTeamMember(t, ctx, orgA, cappedUser, "admin")
	exec(t, ctx, `INSERT INTO workspace_team_access (id,workspace_id,team_id,role) VALUES ($1,$2,$3,'viewer')`, id(), wsID, lowGrantTeam)
	if role, err = testQueries.GetWorkspaceTeamRole(ctx, wsID, cappedUser, orgA); err != nil || role != "viewer" {
		t.Fatalf("admin in a viewer-granted team returned (%q, %v), want viewer", role, err)
	}

	// A second team grants more: the highest capped result wins, not the newest.
	higherTeam := seedTeamMember(t, ctx, orgA, userA, "admin")
	exec(t, ctx, `INSERT INTO workspace_team_access (id,workspace_id,team_id,role) VALUES ($1,$2,$3,'admin')`, id(), wsID, higherTeam)
	if role, err = testQueries.GetWorkspaceTeamRole(ctx, wsID, userA, orgA); err != nil || role != "admin" {
		t.Fatalf("two grants returned (%q, %v), want the higher one (admin)", role, err)
	}

	// A grant on one workspace is read only for that workspace.
	otherWS := seedWorkspace(t, ctx, orgA, userA)
	if role, err = testQueries.GetWorkspaceTeamRole(ctx, otherWS, userA, orgA); err != nil || role != "" {
		t.Fatalf("grant leaked to another workspace: (%q, %v), want empty", role, err)
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

// ── workspace-scoped child lookups ──────────────────────────────────────────
//
// Every route under /workspaces/{workspaceID}/… is authorized against the
// workspace in its path. These assert the queries behind those routes agree:
// a child object addressed through the wrong workspace has to miss, so being
// authorized on one workspace cannot be spent on another's variables, state, or
// runs. Org scoping alone does not do it — both workspaces below are in the
// same org, which is exactly the case the gate cannot see.

func seedVariable(t *testing.T, ctx context.Context, wsID, orgID, key string) string {
	t.Helper()
	varID := id()
	exec(t, ctx, `INSERT INTO workspace_variables (id,workspace_id,org_id,key,value,sensitive,category)
		VALUES ($1,$2,$3,$4,'secret-value',true,'env')`, varID, wsID, orgID, key)
	return varID
}

func seedStateVersion(t *testing.T, ctx context.Context, wsID, orgID, runID string, serial int) string {
	t.Helper()
	svID := id()
	exec(t, ctx, `INSERT INTO state_versions (id,workspace_id,org_id,run_id,serial,state_url)
		VALUES ($1,$2,$3,$4,$5,'s3://state/x.json')`, svID, wsID, orgID, runID, serial)
	return svID
}

func TestWorkspaceVariableIsWorkspaceScoped(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "varscope")
	victim := seedWorkspace(t, ctx, orgID, userID)
	attacker := seedWorkspace(t, ctx, orgID, userID)
	varID := seedVariable(t, ctx, victim, orgID, "aws_secret_access_key")

	// Reading through the owning workspace works.
	if _, err := testQueries.GetWorkspaceVariable(ctx, repository.GetWorkspaceVariableParams{
		ID: varID, WorkspaceID: victim, OrgID: orgID,
	}); err != nil {
		t.Fatalf("owning workspace should read its own variable, got: %v", err)
	}

	// Reading the same variable id through another workspace in the same org
	// must miss — this is the read that returned a decrypted secret.
	if _, err := testQueries.GetWorkspaceVariable(ctx, repository.GetWorkspaceVariableParams{
		ID: varID, WorkspaceID: attacker, OrgID: orgID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-workspace variable read must return no rows, got: %v", err)
	}

	// Writing through another workspace must change nothing.
	if _, err := testQueries.UpdateWorkspaceVariable(ctx, repository.UpdateWorkspaceVariableParams{
		ID: varID, WorkspaceID: attacker, OrgID: orgID, Value: "poisoned", Category: "env",
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-workspace variable update must affect no row, got: %v", err)
	}
	after, err := testQueries.GetWorkspaceVariable(ctx, repository.GetWorkspaceVariableParams{
		ID: varID, WorkspaceID: victim, OrgID: orgID,
	})
	if err != nil {
		t.Fatalf("re-read after blocked update: %v", err)
	}
	if after.Value != "secret-value" {
		t.Fatalf("variable value = %q, want it untouched by the cross-workspace write", after.Value)
	}

	// Deleting through another workspace must miss and leave the row in place.
	if _, err := testQueries.DeleteWorkspaceVariable(ctx, repository.DeleteWorkspaceVariableParams{
		ID: varID, WorkspaceID: attacker, OrgID: orgID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-workspace variable delete must affect no row, got: %v", err)
	}
	if _, err := testQueries.GetWorkspaceVariable(ctx, repository.GetWorkspaceVariableParams{
		ID: varID, WorkspaceID: victim, OrgID: orgID,
	}); err != nil {
		t.Fatalf("variable should still exist after a blocked delete, got: %v", err)
	}

	// The legitimate delete still works.
	if _, err := testQueries.DeleteWorkspaceVariable(ctx, repository.DeleteWorkspaceVariableParams{
		ID: varID, WorkspaceID: victim, OrgID: orgID,
	}); err != nil {
		t.Fatalf("owning workspace should delete its own variable, got: %v", err)
	}
}

func TestStateVersionIsWorkspaceScoped(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "statescope")
	victim := seedWorkspace(t, ctx, orgID, userID)
	attacker := seedWorkspace(t, ctx, orgID, userID)
	runID := seedRun(t, ctx, victim, orgID, userID)
	svID := seedStateVersion(t, ctx, victim, orgID, runID, 1)

	if _, err := testQueries.GetStateVersion(ctx, repository.GetStateVersionParams{
		ID: svID, WorkspaceID: victim, OrgID: orgID,
	}); err != nil {
		t.Fatalf("owning workspace should read its own state version, got: %v", err)
	}

	// The download route resolves the blob's location from this row, so a hit
	// here is a full tfstate — every provider credential in it — handed to a
	// caller authorized on a different workspace.
	if _, err := testQueries.GetStateVersion(ctx, repository.GetStateVersionParams{
		ID: svID, WorkspaceID: attacker, OrgID: orgID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-workspace state version read must return no rows, got: %v", err)
	}
}

func TestRunLookupIsWorkspaceScoped(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "runscope")
	victim := seedWorkspace(t, ctx, orgID, userID)
	attacker := seedWorkspace(t, ctx, orgID, userID)
	runID := seedRun(t, ctx, victim, orgID, userID)

	if _, err := testQueries.GetRunInWorkspace(ctx, repository.GetRunInWorkspaceParams{
		ID: runID, WorkspaceID: victim, OrgID: orgID,
	}); err != nil {
		t.Fatalf("owning workspace should read its own run, got: %v", err)
	}
	if _, err := testQueries.GetRunInWorkspace(ctx, repository.GetRunInWorkspaceParams{
		ID: runID, WorkspaceID: attacker, OrgID: orgID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-workspace run read must return no rows, got: %v", err)
	}

	// Cancelling another workspace's in-flight run is a denial of service
	// against whoever owns it.
	if _, err := testQueries.CancelRun(ctx, runID, attacker, orgID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-workspace cancel must affect no row, got: %v", err)
	}
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, runID).Scan(&status); err != nil {
		t.Fatalf("read run status: %v", err)
	}
	if status != "pending" {
		t.Fatalf("run status = %q, want it untouched by the cross-workspace cancel", status)
	}

	// The legitimate cancel still works.
	if _, err := testQueries.CancelRun(ctx, runID, victim, orgID); err != nil {
		t.Fatalf("owning workspace should cancel its own run, got: %v", err)
	}
}

// The grants panel names teams, so its join is org-scoped like the grant read
// itself — a row planted against another org's team discloses nothing.
func TestListWorkspaceTeamAccessIsOrgScoped(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgA, userA := seedOrg(t, ctx, "wtaa")
	orgB, userB := seedOrg(t, ctx, "wtab")
	wsID := seedWorkspace(t, ctx, orgA, userA)

	ownTeam := seedTeamMember(t, ctx, orgA, userA, "admin")
	foreignTeam := seedTeamMember(t, ctx, orgB, userB, "admin")
	exec(t, ctx, `INSERT INTO workspace_team_access (id,workspace_id,team_id,role) VALUES ($1,$2,$3,'operator')`, id(), wsID, ownTeam)
	exec(t, ctx, `INSERT INTO workspace_team_access (id,workspace_id,team_id,role) VALUES ($1,$2,$3,'admin')`, id(), wsID, foreignTeam)

	access, err := testQueries.ListWorkspaceTeamAccess(ctx, wsID, orgA)
	if err != nil {
		t.Fatalf("list workspace access: %v", err)
	}
	if len(access) != 1 {
		t.Fatalf("got %d grants, want only the caller's own org's team", len(access))
	}
	if access[0].TeamID != ownTeam {
		t.Fatalf("grant team = %q, want %q", access[0].TeamID, ownTeam)
	}
}

// seedConfigWorkspace creates a workspace pinned to a specific repo URL,
// working directory and approval gate — the three fields that decide whether
// two workspaces are two doors onto the same infrastructure.
func seedConfigWorkspace(t *testing.T, ctx context.Context, orgID, userID, repoURL, workingDir string, requiresApproval bool) string {
	t.Helper()
	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,repo_url,working_dir,requires_approval)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		wsID, orgID, "ws-"+wsID, userID, repoURL, workingDir, requiresApproval)
	return wsID
}

// requires_approval protects the infrastructure a config manages, not the row
// that names it: with terragrunt the backend is declared in the repo, so a
// second workspace on the same repo + working_dir drives the same remote state.
// HasGatedWorkspaceForConfig is what lets the handler refuse an ungated twin,
// so it has to see through the ways the same target can be spelled.
func TestHasGatedWorkspaceForConfig(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgA, userA := seedOrg(t, ctx, "twin-a")
	orgB, userB := seedOrg(t, ctx, "twin-b")

	const repo = "https://github.com/acme/infra.git"
	gated := seedConfigWorkspace(t, ctx, orgA, userA, repo, "envs/prod", true)
	seedConfigWorkspace(t, ctx, orgA, userA, repo, "envs/dev", false)
	// Same config, gated, but in another org — must never be visible here.
	seedConfigWorkspace(t, ctx, orgB, userB, "https://github.com/acme/other", "envs/prod", true)

	tests := []struct {
		name       string
		orgID      string
		repoURL    string
		workingDir string
		excludeID  string
		want       bool
	}{
		{"exact match on a gated config", orgA, repo, "envs/prod", "", true},
		{"the same repo without the .git suffix", orgA, "https://github.com/acme/infra", "envs/prod", "", true},
		{"a trailing slash on the repo", orgA, "https://github.com/acme/infra.git/", "envs/prod", "", true},
		{"a different case on the host and path", orgA, "HTTPS://GitHub.com/Acme/Infra.GIT", "envs/prod", "", true},
		{"the same repo over ssh", orgA, "git@github.com:acme/infra.git", "envs/prod", "", true},
		{"the same repo with an embedded token", orgA, "https://ghp_token@github.com/acme/infra", "envs/prod", "", true},
		{"the same repo over the ssh:// scheme", orgA, "ssh://git@github.com/acme/infra", "envs/prod", "", true},
		{"the same repo on its scheme's default port", orgA, "https://github.com:443/acme/infra.git", "envs/prod", "", true},
		{"the same repo over ssh:// with an explicit port", orgA, "ssh://git@github.com:22/acme/infra", "envs/prod", "", true},
		// A git path is resolved as a path at the far end: every one of these
		// clones the same tree — GitHub serves "acme//infra" and "acme/./infra"
		// as "acme/infra" — so every one of them has to be the same row here.
		{"a doubled slash inside the repo path", orgA, "https://github.com/acme//infra", "envs/prod", "", true},
		{"a . segment inside the repo path", orgA, "https://github.com/acme/./infra", "envs/prod", "", true},
		{"a trailing /. on the repo path", orgA, "https://github.com/acme/infra/.", "envs/prod", "", true},
		{"a trailing /. after the .git suffix", orgA, "https://github.com/acme/infra.git/.", "envs/prod", "", true},
		{"every repo respelling at once", orgA, "ssh://TOKEN@GitHub.com:22/acme//./infra.GIT//", "envs/prod", "", true},
		{"a ./ prefix on the working directory", orgA, repo, "./envs/prod", "", true},
		{"a trailing slash on the working directory", orgA, repo, "envs/prod/", "", true},
		// Every one of these is the same `cd` in the executor, so every one of
		// them has to be the same row here — otherwise an ungated twin on gated
		// infrastructure is one respelled path away.
		{"a doubled slash inside the working directory", orgA, repo, "envs//prod", "", true},
		{"a . segment inside the working directory", orgA, repo, "envs/./prod", "", true},
		{"a trailing /. on the working directory", orgA, repo, "envs/prod/.", "", true},
		{"several of them at once", orgA, repo, "./envs//./prod/.", "", true},

		{"a different directory in the same repo", orgA, repo, "envs/staging", "", false},
		{"the ungated sibling's own directory", orgA, repo, "envs/dev", "", false},
		{"a different repo entirely", orgA, "https://github.com/acme/apps", "envs/prod", "", false},
		// Folding spellings must not fold repos: a neighbouring name, a deeper
		// path and another host all stay distinct, or the check refuses
		// legitimate workspaces it has no business refusing.
		{"a repo whose name starts the same", orgA, "https://github.com/acme/infra2", "envs/prod", "", false},
		{"a repo one level deeper", orgA, "https://github.com/acme/infra/sub", "envs/prod", "", false},
		{"the same path on another host", orgA, "https://gitlab.com/acme/infra", "envs/prod", "", false},
		{"an upload workspace has no repo to compare", orgA, "", "envs/prod", "", false},

		{"the gated workspace does not match itself", orgA, repo, "envs/prod", gated, false},
		{"another org's gated workspace is invisible", orgB, repo, "envs/prod", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := testQueries.HasGatedWorkspaceForConfig(ctx, repository.GatedTwinParams{
				OrgID:      tt.orgID,
				RepoURL:    tt.repoURL,
				WorkingDir: tt.workingDir,
				ExcludeID:  tt.excludeID,
			})
			if err != nil {
				t.Fatalf("HasGatedWorkspaceForConfig: %v", err)
			}
			if got != tt.want {
				t.Errorf("HasGatedWorkspaceForConfig(repo=%q, dir=%q) = %v, want %v",
					tt.repoURL, tt.workingDir, got, tt.want)
			}
		})
	}

	// The root of a repo has several spellings and they must all agree, so a
	// gated workspace at "." cannot be twinned by one at "" or "./".
	rootRepo := "https://github.com/acme/root-config"
	seedConfigWorkspace(t, ctx, orgA, userA, rootRepo, ".", true)
	for _, dir := range []string{".", "", "./", "/", "//", "/./", "./."} {
		got, err := testQueries.HasGatedWorkspaceForConfig(ctx, repository.GatedTwinParams{
			OrgID: orgA, RepoURL: rootRepo, WorkingDir: dir,
		})
		if err != nil {
			t.Fatalf("HasGatedWorkspaceForConfig(root, %q): %v", dir, err)
		}
		if !got {
			t.Errorf("working_dir %q did not match a gated workspace at the repo root", dir)
		}
	}

	// The column is normalised too, not just the argument: a row written before
	// the write boundary canonicalised working_dir still has to be found. Same
	// for a stored repo URL that carries a port.
	legacyRepo := "https://github.com:8443/acme/legacy.git"
	seedConfigWorkspace(t, ctx, orgA, userA, legacyRepo, "envs//./prod/", true)
	got, err := testQueries.HasGatedWorkspaceForConfig(ctx, repository.GatedTwinParams{
		OrgID: orgA, RepoURL: "https://github.com:8443/acme/legacy", WorkingDir: "envs/prod",
	})
	if err != nil {
		t.Fatalf("HasGatedWorkspaceForConfig(legacy): %v", err)
	}
	if !got {
		t.Error("a canonically-spelled query missed a gated row stored with a respelled working_dir")
	}

	// Normalising the port must not collapse an scp-style remote whose owner is
	// numeric — "git@github.com:2600/infra" is a repo, not a port.
	seedConfigWorkspace(t, ctx, orgA, userA, "git@github.com:2600/infra.git", "envs/prod", true)
	got, err = testQueries.HasGatedWorkspaceForConfig(ctx, repository.GatedTwinParams{
		OrgID: orgA, RepoURL: "https://github.com/infra", WorkingDir: "envs/prod",
	})
	if err != nil {
		t.Fatalf("HasGatedWorkspaceForConfig(scp numeric owner): %v", err)
	}
	if got {
		t.Error("github.com/infra matched git@github.com:2600/infra — a numeric path segment was read as a port")
	}
}

// A gated workspace guards the config it sits on, so the handler has to know
// whether an update MOVES it — and "same config" has to mean here exactly what
// it means to HasGatedWorkspaceForConfig. If a respelled save read as a move,
// an operator would be refused an edit that opens nothing; if a real move read
// as a stay, the last gate could walk off a configuration for free.
func TestConfigTargetsMatch(t *testing.T) {
	requireDB(t)
	ctx := context.Background()

	const repo = "https://github.com/acme/infra.git"
	tests := []struct {
		name                     string
		repoA, dirA, repoB, dirB string
		want                     bool
	}{
		{"identical spelling", repo, "envs/prod", repo, "envs/prod", true},

		// Every spelling HasGatedWorkspaceForConfig collapses has to collapse
		// here too, or the two checks disagree about the same pair of rows.
		{"without the .git suffix", repo, "envs/prod", "https://github.com/acme/infra", "envs/prod", true},
		{"trailing slash on the repo", repo, "envs/prod", "https://github.com/acme/infra.git/", "envs/prod", true},
		{"different case", repo, "envs/prod", "HTTPS://GitHub.com/Acme/Infra.GIT", "envs/prod", true},
		{"over ssh", repo, "envs/prod", "git@github.com:acme/infra.git", "envs/prod", true},
		{"with an embedded token", repo, "envs/prod", "https://ghp_token@github.com/acme/infra", "envs/prod", true},
		{"on the scheme's default port", repo, "envs/prod", "https://github.com:443/acme/infra", "envs/prod", true},
		{"doubled slash in the repo path", repo, "envs/prod", "https://github.com/acme//infra", "envs/prod", true},
		{"a . segment in the repo path", repo, "envs/prod", "https://github.com/acme/./infra", "envs/prod", true},
		{"trailing /. after the .git suffix", repo, "envs/prod", "https://github.com/acme/infra.git/.", "envs/prod", true},
		{"./ prefix on the directory", repo, "envs/prod", repo, "./envs/prod", true},
		{"doubled slash in the directory", repo, "envs/prod", repo, "envs//prod", true},
		{"every spelling of the repo root", repo, ".", repo, "", true},
		{"another spelling of the repo root", repo, "/", repo, "./.", true},

		// Real moves.
		{"a different directory", repo, "envs/prod", repo, "envs/prod-old", false},
		{"a different repo", repo, "envs/prod", "https://github.com/acme/apps", "envs/prod", false},
		{"a numeric scp owner is not a port", "git@github.com:2600/infra.git", ".", "https://github.com/infra", ".", false},

		// An upload workspace has no config identity, so it matches nothing —
		// not even another upload. Same reading the twin check takes.
		{"upload on the left", "", "envs/prod", repo, "envs/prod", false},
		{"upload on the right", repo, "envs/prod", "", "envs/prod", false},
		{"upload on both sides", "", "envs/prod", "", "envs/prod", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := testQueries.ConfigTargetsMatch(ctx, repository.ConfigTargetsMatchParams{
				RepoURLA: tt.repoA, WorkingDirA: tt.dirA,
				RepoURLB: tt.repoB, WorkingDirB: tt.dirB,
			})
			if err != nil {
				t.Fatalf("ConfigTargetsMatch: %v", err)
			}
			if got != tt.want {
				t.Errorf("ConfigTargetsMatch(%q %q, %q %q) = %v, want %v",
					tt.repoA, tt.dirA, tt.repoB, tt.dirB, got, tt.want)
			}
		})
	}

	// The two checks have to agree row for row: whatever ConfigTargetsMatch
	// calls the same config, HasGatedWorkspaceForConfig has to find, and
	// whatever it calls a move must leave the origin visible as gated when the
	// mover is excluded.
	orgA, userA := seedOrg(t, ctx, "origin-a")
	gated := seedConfigWorkspace(t, ctx, orgA, userA, repo, "envs/prod", true)

	same, err := testQueries.ConfigTargetsMatch(ctx, repository.ConfigTargetsMatchParams{
		RepoURLA: repo, WorkingDirA: "envs/prod",
		RepoURLB: "https://github.com/acme/infra/", WorkingDirB: "./envs/prod",
	})
	if err != nil {
		t.Fatalf("ConfigTargetsMatch: %v", err)
	}
	if !same {
		t.Fatal("a respelled resubmit of the same target must not read as a move")
	}

	// Excluding the only gate leaves the config unguarded — the state the
	// handler refuses to create at the operator bar.
	stillGated, err := testQueries.HasGatedWorkspaceForConfig(ctx, repository.GatedTwinParams{
		OrgID: orgA, RepoURL: repo, WorkingDir: "envs/prod", ExcludeID: gated,
	})
	if err != nil {
		t.Fatalf("HasGatedWorkspaceForConfig: %v", err)
	}
	if stillGated {
		t.Fatal("nothing else gates envs/prod; excluding the mover must report it unguarded")
	}

	// Stand a second gate on the same config and the move is free again, which
	// is the escape hatch the 403 names.
	seedConfigWorkspace(t, ctx, orgA, userA, "https://github.com/acme/infra", "./envs/prod", true)
	stillGated, err = testQueries.HasGatedWorkspaceForConfig(ctx, repository.GatedTwinParams{
		OrgID: orgA, RepoURL: repo, WorkingDir: "envs/prod", ExcludeID: gated,
	})
	if err != nil {
		t.Fatalf("HasGatedWorkspaceForConfig: %v", err)
	}
	if !stillGated {
		t.Fatal("a replacement gate spelled differently must still count as guarding the config")
	}
}
