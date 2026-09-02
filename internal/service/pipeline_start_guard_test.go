package service_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

// A pipeline runs one at a time. Its stages hand outputs to each other through
// the target workspace's variables, so two runs of the same pipeline write the
// same keys and each plans against whichever landed last.
//
// The guard asks the database whether one is already running. A read that fails
// is not the answer "none": starting on an unanswered question is the one
// outcome the guard exists to prevent.

// emptySchemaPool opens a pool on a database with no schema, so the guard's read
// fails rather than answering. PipelineService holds a concrete
// *repository.Queries, and the branch under test is what it does when that read
// cannot answer.
func emptySchemaPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("no test database (set TEST_DATABASE_URL)")
	}

	name := "portal_nopipeline_" + strings.ToLower(id())
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to create an empty database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close(ctx)
		t.Fatalf("create empty database: %v", err)
	}
	admin.Close(ctx)

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if a, err := pgx.Connect(context.Background(), base); err == nil {
			_, _ = a.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name)
			a.Close(context.Background())
		}
	})
	return pool
}

func TestPipelineStartRun_RefusesWhenItCannotTellWhetherOneIsRunning(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	pool := emptySchemaPool(t, ctx)

	svc := service.NewPipelineService(repository.New(pool), pool, nil)
	_, err := svc.StartRun(ctx, "pl_1", "org_1", "user_1")
	if err == nil {
		t.Fatal("a second run was started although whether one is already running could not be determined; two runs of a pipeline write the same variables and each plans against whichever landed last")
	}
	if !strings.Contains(err.Error(), "already has an active run") && !strings.Contains(err.Error(), "check whether") {
		t.Errorf("the guard fell through and the run failed later on something else, so nothing refused a start on an unanswered question: %v", err)
	}
}

// An active run is refused as a conflict, and a pipeline with none proceeds past
// the guard — the refusal above means nothing if either changes.
func TestPipelineStartRun_RefusesASecondRunAndAdmitsAFirst(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "plstart")

	wsID := seedWorkspace(t, ctx, orgID, userID)
	plID := id()
	exec(t, ctx, `INSERT INTO pipelines (id,org_id,name,created_by) VALUES ($1,$2,$3,$4)`, plID, orgID, "pl-"+plID, userID)
	exec(t, ctx, `INSERT INTO pipeline_stages (id,pipeline_id,workspace_id,stage_order) VALUES ($1,$2,$3,0)`, id(), plID, wsID)

	svc := service.NewPipelineService(testQueries, testPool, nil)

	// No active run: the guard lets it past, and it fails later for want of a
	// river client rather than at the guard.
	if _, err := svc.StartRun(ctx, plID, orgID, userID); err != nil &&
		strings.Contains(err.Error(), "already has an active run") {
		t.Fatalf("a pipeline with no active run was refused as having one: %v", err)
	}

	// An active run: refused.
	exec(t, ctx,
		`INSERT INTO pipeline_runs (id,pipeline_id,org_id,status,total_stages,created_by)
		 VALUES ($1,$2,$3,'running',1,$4)`,
		id(), plID, orgID, userID)

	_, err := svc.StartRun(ctx, plID, orgID, userID)
	if err == nil || !strings.Contains(err.Error(), "already has an active run") {
		t.Fatalf("a second run was started while one was active: %v", err)
	}
}
