package worker

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nanohype/portal/internal/repository"
)

// emptyDatabasePool opens a pool on a database with no schema, so a query
// against it fails rather than answering. It is how a read failure is planted
// without a stub: ClaimAndEnqueueNextRun takes a concrete *repository.Queries,
// and the branch under test is what it does when that read cannot answer.
func emptyDatabasePool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("no test database (set TEST_DATABASE_URL)")
	}

	name := "portal_noschema_" + strings.ToLower(id())
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
		t.Fatalf("open pool on the empty database: %v", err)
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

// The hand-off is the only thing that starts a pending run, and it runs on a
// release that has already happened. Reading a failed query as "nothing is
// waiting" strands whatever was: the run sits at 'pending' behind a free slot
// and no later event comes back for it.
//
// pgx.ErrNoRows is the answer. Everything else is a failed question, and the log
// is the only surface there is — the caller is a void function on a path whose
// run is already terminal.
func TestClaimAndEnqueueNextRun_ReportsAReadItCouldNotAnswer(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	pool := emptyDatabasePool(t, ctx)

	var buf strings.Builder
	ClaimAndEnqueueNextRun(ctx, repository.New(pool), pool, insertOnlyRiverClient(t, pool), "ws_1", capture(&buf))

	out := buf.String()
	if out == "" {
		t.Fatal("the read failed and nothing said so; a pending run on this workspace is left behind a free slot with nothing coming back for it")
	}
	if !strings.Contains(out, "consequence") {
		t.Errorf("the log does not carry the consequence, so an operator reading it does not know a run is stranded:\n%s", out)
	}
	if !strings.Contains(out, "ws_1") {
		t.Errorf("the log does not name the workspace:\n%s", out)
	}
}

// A workspace with nothing pending is the ordinary case and must stay silent, or
// every release of every workspace logs an error.
func TestClaimAndEnqueueNextRun_IsSilentWhenNothingIsPending(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "handoffquiet")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version)
		 VALUES ($1,$2,$3,$4,'upload','1.11.0')`,
		wsID, orgID, "ws-"+wsID, userID)

	var buf strings.Builder
	ClaimAndEnqueueNextRun(ctx, testQueries, testPool, insertOnlyRiverClient(t, testPool), wsID, capture(&buf))

	if strings.Contains(buf.String(), "ERROR") {
		t.Errorf("a workspace with nothing pending logged an error:\n%s", buf.String())
	}
}

// isRunCancelled arms the guard that stops a worker overwriting a cancellation
// an operator issued and watched take effect. A read that fails is not evidence
// the run was not cancelled — the same reasoning retryRefusal applies to the
// status it reads.
func TestIsRunCancelled_ReportsAReadItCouldNotAnswer(t *testing.T) {
	requireDB(t)
	ctx := context.Background()

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	var buf strings.Builder
	w := &RunJobWorker{queries: testQueries}
	got := w.isRunCancelled(cancelled, "run_1", "org_1", capture(&buf))

	if got {
		t.Fatal("a failed read reported the run as cancelled")
	}
	out := buf.String()
	if out == "" {
		t.Fatal("the read that arms the cancel guard failed and nothing said so; a cancellation is overwritten in silence")
	}
	if !strings.Contains(out, "consequence") {
		t.Errorf("the log does not say what the silence costs:\n%s", out)
	}
}

// A run that is not cancelled is the ordinary case, and it must not log.
func TestIsRunCancelled_IsSilentForARunThatIsNotCancelled(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "cancelquiet")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version)
		 VALUES ($1,$2,$3,$4,'upload','1.11.0')`,
		wsID, orgID, "ws-"+wsID, userID)
	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "planning", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	var buf strings.Builder
	w := &RunJobWorker{queries: testQueries}
	if w.isRunCancelled(ctx, run.ID, orgID, capture(&buf)) {
		t.Error("a running run was reported cancelled")
	}
	if strings.Contains(buf.String(), "ERROR") {
		t.Errorf("an ordinary read logged an error:\n%s", buf.String())
	}
}
