package worker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/nanohype/portal/internal/repository"
)

// TestClaimAndEnqueueNextRunEnqueuesTheSameRunOnce drives the hand-off from two
// callers at once and asserts the run reaches the queue exactly once.
//
// The hand-off runs from several places on an ordinary workspace — a worker
// finishing a run (enqueueNextPendingRun), an operator cancelling one
// (RunService.Cancel), the reaper freeing a wedged slot. Nothing serializes
// them, and nothing downstream of the enqueue does either: RunJobArgs declares
// no UniqueOpts, so River inserts whatever it's given; RunJobWorker.Work takes
// no lock; UpdateRunStarted has no status guard. Two jobs for one run id means
// two tofu processes on one state file.
//
// The single thing standing between those callers and that outcome is the
// conditional claim on the workspace's run slot, so this test forces the exact
// interleaving that tests it. A third transaction holds the workspace row, both
// callers read the same pending run and then pile up on the claim, and the
// holder lets go. Postgres hands the row to one caller at a time and
// re-evaluates the claim's WHERE against the committed row, so the second
// caller sees a slot already pointing at the run it is trying to claim — the
// one case a predicate that also accepts its own run id would wave through.
func TestClaimAndEnqueueNextRunEnqueuesTheSameRunOnce(t *testing.T) {
	requireDB(t)
	ctx := context.Background()

	pool, queries := claimTestPool(t, ctx)
	riverClient := insertOnlyRiverClient(t, pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	orgID, userID := seedOrg(t, ctx, "claim-once")
	wsID := seedRunWorkspace(t, ctx, orgID, userID)
	runID := seedPendingRun(t, ctx, wsID, orgID, userID)

	// Hold the workspace row so both callers get past their read of the pending
	// run before either can claim. This is the window the callers race in; the
	// lock only makes it wide enough to land on every time.
	gate, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin gate tx: %v", err)
	}
	var held string
	if err := gate.QueryRow(ctx, `SELECT id FROM workspaces WHERE id = $1 FOR UPDATE`, wsID).Scan(&held); err != nil {
		_ = gate.Rollback(ctx)
		t.Fatalf("lock workspace row: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			ClaimAndEnqueueNextRun(ctx, queries, pool, riverClient, wsID, logger)
		}()
	}

	// Both callers are now blocked on the workspace row, which means both have
	// already read the same pending run. Releasing the gate replays exactly the
	// sequence the two production paths produce.
	waitForBlockedOnLock(t, ctx, pool, 2)
	if err := gate.Rollback(ctx); err != nil {
		t.Fatalf("release gate tx: %v", err)
	}
	wg.Wait()

	if got := countRunJobs(t, ctx, pool, runID); got != 1 {
		t.Fatalf("run %s enqueued %d times, want exactly 1 — two jobs for one run id is two tofu processes on one state file", runID, got)
	}
	if got := currentRunID(t, ctx, pool, wsID); got != runID {
		t.Fatalf("workspace slot = %q, want the enqueued run (%q)", got, runID)
	}
}

// TestClaimAndEnqueueNextRunIsExclusiveAcrossPendingRuns is the same race with
// two runs queued behind the slot: the hand-off must start the oldest one and
// leave the other pending, never both.
func TestClaimAndEnqueueNextRunIsExclusiveAcrossPendingRuns(t *testing.T) {
	requireDB(t)
	ctx := context.Background()

	pool, queries := claimTestPool(t, ctx)
	riverClient := insertOnlyRiverClient(t, pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	orgID, userID := seedOrg(t, ctx, "claim-excl")
	wsID := seedRunWorkspace(t, ctx, orgID, userID)
	first := seedPendingRun(t, ctx, wsID, orgID, userID)
	exec(t, ctx, `UPDATE runs SET created_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, first)
	second := seedPendingRun(t, ctx, wsID, orgID, userID)

	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			ClaimAndEnqueueNextRun(ctx, queries, pool, riverClient, wsID, logger)
		}()
	}
	wg.Wait()

	if got := countRunJobs(t, ctx, pool, first); got != 1 {
		t.Errorf("oldest pending run enqueued %d times, want 1", got)
	}
	if got := countRunJobs(t, ctx, pool, second); got != 0 {
		t.Errorf("second pending run enqueued %d times, want 0 — it waits for the slot", got)
	}
}

// TestClaimAndEnqueueNextRunSkipsARunThatAlreadyHoldsTheSlot pins the other
// half of the invariant: the hand-off never re-claims. A run holding the slot is
// a run that is already on its way, so a caller that finds it as the oldest
// pending row must leave it alone rather than enqueue a second job for it.
func TestClaimAndEnqueueNextRunSkipsARunThatAlreadyHoldsTheSlot(t *testing.T) {
	requireDB(t)
	ctx := context.Background()

	pool, queries := claimTestPool(t, ctx)
	riverClient := insertOnlyRiverClient(t, pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	orgID, userID := seedOrg(t, ctx, "claim-self")
	wsID := seedRunWorkspace(t, ctx, orgID, userID)
	runID := seedPendingRun(t, ctx, wsID, orgID, userID)

	if _, err := queries.ClaimWorkspaceForRun(ctx, wsID, orgID, runID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	ClaimAndEnqueueNextRun(ctx, queries, pool, riverClient, wsID, logger)

	if got := countRunJobs(t, ctx, pool, runID); got != 0 {
		t.Fatalf("run holding the slot was enqueued %d times, want 0", got)
	}
}

// claimTestPool gives each test its own pool. The interleaving these tests force
// parks several connections at once (the gate, both callers, the poller), which
// is more than the shared pool's default budget on a small CI runner.
func claimTestPool(t *testing.T, ctx context.Context) (*pgxpool.Pool, *repository.Queries) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(testDBURL)
	if err != nil {
		t.Fatalf("parse test db url: %v", err)
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, repository.New(pool)
}

// insertOnlyRiverClient builds the same insert-only client the API server runs
// with: no queues and no workers, so nothing picks the inserted jobs up and the
// river_job rows stay where the test can count them.
func insertOnlyRiverClient(t *testing.T, pool *pgxpool.Pool) *river.Client[pgx.Tx] {
	t.Helper()
	client, err := river.NewClient[pgx.Tx](riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	return client
}

func seedRunWorkspace(t *testing.T, ctx context.Context, orgID, userID string) string {
	t.Helper()
	wsID := id()
	exec(t, ctx, `INSERT INTO workspaces (id,org_id,name,created_by) VALUES ($1,$2,$3,$4)`, wsID, orgID, "ws-"+wsID, userID)
	return wsID
}

func seedPendingRun(t *testing.T, ctx context.Context, wsID, orgID, userID string) string {
	t.Helper()
	runID := id()
	exec(t, ctx, `INSERT INTO runs (id,workspace_id,org_id,operation,status,created_by,config_source)
		VALUES ($1,$2,$3,'plan','pending',$4,'upload')`, runID, wsID, orgID, userID)
	return runID
}

// waitForBlockedOnLock blocks until want backends in this database are waiting
// on a lock, so the test releases the gate only once both callers have piled up
// behind it.
func waitForBlockedOnLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var blocked int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity
			 WHERE datname = current_database() AND wait_event_type = 'Lock' AND state = 'active'`,
		).Scan(&blocked); err != nil {
			t.Fatalf("poll pg_stat_activity: %v", err)
		}
		if blocked >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d callers blocked on the workspace row after 30s", blocked, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func countRunJobs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM river_job WHERE kind = 'run' AND args->>'run_id' = $1`, runID,
	).Scan(&n); err != nil {
		t.Fatalf("count river jobs: %v", err)
	}
	return n
}

func currentRunID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wsID string) string {
	t.Helper()
	var current *string
	if err := pool.QueryRow(ctx, `SELECT current_run_id FROM workspaces WHERE id = $1`, wsID).Scan(&current); err != nil {
		t.Fatalf("read workspace slot: %v", err)
	}
	if current == nil {
		return ""
	}
	return *current
}
