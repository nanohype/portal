// Integration tests for the service layer, run against a real Postgres. They
// cover the transactional write paths whose correctness can't be shown by a unit
// test — the approval status-guard + run transition + slot hand-off, and the
// transactional workspace-variable copy.
//
// Like the repository integration tests, they create a scratch database
// (portal_svc_test) off TEST_DATABASE_URL, migrate it, and drop it afterward.
// With no reachable server the DB-requiring tests skip, so `go test ./...` stays
// green without one.
package service_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

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
		os.Exit(m.Run()) // no server — DB tests skip via requireDB
	}
	const dbName = "portal_svc_test"
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

	// River's own tables, migrated the way the migrate binary does it, so the
	// tests that hand the service a real River client can count what it enqueued.
	riverMigrator, err := rivermigrate.New[pgx.Tx](riverpgxv5.New(testPool), nil)
	if err != nil {
		panic("river migrator: " + err.Error())
	}
	if _, err := riverMigrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		panic("river migrate up: " + err.Error())
	}

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

func exec(t *testing.T, ctx context.Context, sql string, args ...any) {
	t.Helper()
	if _, err := testPool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("setup exec failed: %v\nsql: %s", err, sql)
	}
}

func seedOrg(t *testing.T, ctx context.Context, slug string) (orgID, userID string) {
	t.Helper()
	orgID, userID = id(), id()
	exec(t, ctx, `INSERT INTO organizations (id,name,slug) VALUES ($1,$2,$3)`, orgID, slug, slug+"-"+orgID)
	exec(t, ctx, `INSERT INTO users (id,org_id,email,name,role) VALUES ($1,$2,$3,'U','owner')`, userID, orgID, userID+"@t.local")
	return orgID, userID
}

func seedWorkspace(t *testing.T, ctx context.Context, orgID, userID string) string {
	t.Helper()
	wsID := id()
	// Name off the full ULID (not a prefix): the prefix is the shared ms timestamp,
	// so several workspaces seeded in one test would collide on (org_id, name).
	exec(t, ctx, `INSERT INTO workspaces (id,org_id,name,created_by) VALUES ($1,$2,$3,$4)`, wsID, orgID, "ws-"+wsID, userID)
	return wsID
}

// seedPlannedRun creates a run in the "planned" status — the state a run is in
// when it's awaiting an apply approval.
func seedPlannedRun(t *testing.T, ctx context.Context, wsID, orgID, userID string) string {
	t.Helper()
	runID := id()
	exec(t, ctx, `INSERT INTO runs (id,workspace_id,org_id,operation,status,created_by) VALUES ($1,$2,$3,'apply','planned',$4)`, runID, wsID, orgID, userID)
	return runID
}

func mustClaim(t *testing.T, ctx context.Context, wsID, orgID, runID string) {
	t.Helper()
	if _, err := testQueries.ClaimWorkspaceForRun(ctx, wsID, orgID, runID); err != nil {
		t.Fatalf("claim slot for %s: %v", runID, err)
	}
}

func runStatus(t *testing.T, ctx context.Context, runID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM runs WHERE id=$1`, runID).Scan(&status); err != nil {
		t.Fatalf("read run status: %v", err)
	}
	return status
}

func seedVar(t *testing.T, ctx context.Context, wsID, orgID, key, value, category string) {
	t.Helper()
	if _, err := testQueries.CreateWorkspaceVariable(ctx, repository.CreateWorkspaceVariableParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Key: key, Value: value, Sensitive: false, Category: category, Description: "",
	}); err != nil {
		t.Fatalf("seed var %q: %v", key, err)
	}
}

// listVarMap returns a workspace's variables as key→value for easy assertions.
func listVarMap(t *testing.T, ctx context.Context, wsID, orgID string) map[string]string {
	t.Helper()
	vars, err := testQueries.ListWorkspaceVariables(ctx, repository.ListWorkspaceVariablesParams{WorkspaceID: wsID, OrgID: orgID})
	if err != nil {
		t.Fatalf("list vars: %v", err)
	}
	out := make(map[string]string, len(vars))
	for _, v := range vars {
		out[v.Key] = v.Value
	}
	return out
}
