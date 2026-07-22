// Integration tests for the run worker, run against a real Postgres. They cover
// what the worker actually hands the executor — the question a unit test on a
// projection cannot answer, because the exploit they guard against is the worker
// reading the wrong row.
//
// Like the repository and service integration tests, they create a scratch
// database (portal_worker_test) off TEST_DATABASE_URL, migrate it, and drop it
// afterward. With no reachable server the DB-requiring tests skip, so
// `go test ./...` stays green without one.
package worker

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
	testDBURL   string
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
	const dbName = "portal_worker_test"
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
	testDBURL = dbURL

	// River's own tables, migrated the same way the migrate binary does it. The
	// enqueue paths under test insert through a real River client, so the proof
	// that a run was enqueued once is a row count in river_job, not a stub.
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
