package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/logstream"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker/executor"
)

// "queued" on a run row is a promise that an apply is coming. Nothing keeps it
// if the enqueue fails: GetNextPendingRun claims status 'pending' only, so a run
// left at 'queued' with no job behind it is picked up by nothing, shows an apply
// pending forever, and is not displaced by any later run of the workspace.
//
// The promise has to be withdrawn where it was made — on the run row.

type stubFinishes struct {
	finished []repository.UpdateRunFinishedParams
	fail     error
}

func (s *stubFinishes) UpdateRunFinished(_ context.Context, arg repository.UpdateRunFinishedParams) (repository.Run, error) {
	s.finished = append(s.finished, arg)
	if s.fail != nil {
		return repository.Run{}, s.fail
	}
	return repository.Run{ID: arg.ID, Status: arg.Status}, nil
}

func withdraw(t *testing.T, finishes *stubFinishes) (*stubFinishes, string, string) {
	t.Helper()
	var buf strings.Builder
	w := &RunJobWorker{finishes: finishes, streamer: logstream.NewMemoryStreamer()}
	added, changed, deleted := int32(3), int32(1), int32(0)
	settled := w.withdrawAutoApplyPromise(context.Background(),
		RunJobArgs{RunID: "run_1", WorkspaceID: "ws_1", OrgID: "org_1", Operation: "plan"},
		&executor.ExecuteResult{
			Output:         "Plan: 3 to add, 1 to change, 0 to destroy.",
			ResourcesAdded: added, ResourcesChanged: changed, ResourcesDeleted: deleted,
		},
		errors.New("connection reset by peer"), capture(&buf))
	return finishes, settled, buf.String()
}

func TestWithdrawAutoApplyPromise_LeavesTheRunPlannedRatherThanQueued(t *testing.T) {
	finishes, _, _ := withdraw(t, &stubFinishes{})

	if len(finishes.finished) != 1 {
		t.Fatalf("the run was settled %d times, want once", len(finishes.finished))
	}
	got := finishes.finished[0]
	if got.Status != "planned" {
		t.Errorf("status = %q, want planned; a run left at queued with no job behind it is picked up by nothing", got.Status)
	}
	if got.ErrorMessage == nil {
		t.Fatal("the run says nothing about the apply that is not coming")
	}
}

// Both halves: the plan succeeded, and the apply it promised was not enqueued.
// Recording only the status hides the second; recording only the failure denies
// that the plan is usable.
func TestAutoApplyNotice_NamesBothHalves(t *testing.T) {
	msg := autoApplyNotice(errors.New("connection reset by peer"))

	succeeded := []string{"plan succeeded", "applies automatically"}
	lost := []string{"not enqueued", "connection reset by peer", "planned rather than queued", "by hand"}

	for _, want := range succeeded {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not say the plan is usable — missing %q:\n%s", want, msg)
		}
	}
	for _, want := range lost {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not say the apply is not coming — missing %q:\n%s", want, msg)
		}
	}
}

// The corrective write is the only thing standing between the run and a status
// nothing will ever move. If it fails too, the log has to say what is left
// behind rather than only that a write failed.
func TestWithdrawAutoApplyPromise_ReportsWhatIsLeftWhenTheCorrectionFails(t *testing.T) {
	_, _, logged := withdraw(t, &stubFinishes{fail: errors.New("deadlock detected")})

	if !strings.Contains(logged, "deadlock detected") {
		t.Errorf("the cause is not in the log:\n%s", logged)
	}
	for _, want := range []string{"queued", "picks it up"} {
		if !strings.Contains(logged, want) {
			t.Errorf("the log does not say what the run is left claiming — missing %q:\n%s", want, logged)
		}
	}
}

// The plan's own output and counts must survive the correction, or withdrawing
// the promise costs the operator the plan they were meant to read.
func TestWithdrawAutoApplyPromise_KeepsThePlanItIsCorrecting(t *testing.T) {
	finishes, _, _ := withdraw(t, &stubFinishes{})
	got := finishes.finished[0]

	if got.PlanOutput == nil || !strings.Contains(*got.PlanOutput, "3 to add") {
		t.Errorf("the plan output was dropped: %v", got.PlanOutput)
	}
	if got.ResourcesAdded == nil || *got.ResourcesAdded != 3 {
		t.Errorf("resources added = %v, want 3", got.ResourcesAdded)
	}
	if got.ResourcesChanged == nil || *got.ResourcesChanged != 1 {
		t.Errorf("resources changed = %v, want 1", got.ResourcesChanged)
	}
}

// The wiring: the withdrawal has to happen on the run the operator opens, not
// only in the function that composes the message. A worker with no job-queue
// client reaches the same branch a failed insert does — it cannot enqueue
// either, and the run row already says "queued".
func TestRunJobWithdrawsTheQueuedPromiseWhenNoApplyCanBeEnqueued(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "autoapply")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version,auto_apply)
		 VALUES ($1,$2,$3,$4,'upload','1.11.0',TRUE)`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "pending", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// newTestRunWorker wires no River client, which is what a worker that cannot
	// enqueue looks like.
	w := newTestRunWorker(&recordingExecutor{})

	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "plan"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if finished.Status == "queued" {
		t.Fatal("the run is left queued for an apply that was never enqueued; GetNextPendingRun claims 'pending' only, so nothing will ever pick it up")
	}
	if finished.Status != "planned" {
		t.Errorf("status = %q, want planned", finished.Status)
	}
	for _, want := range []string{"plan succeeded", "not enqueued", "by hand"} {
		if !strings.Contains(finished.ErrorMessage, want) {
			t.Errorf("the run's message is missing %q:\n%s", want, finished.ErrorMessage)
		}
	}
}

// The status a withdrawal settles is the status everything downstream reads. The
// pipeline advancer decides what to do with the stage from it, and its "queued"
// arm is a deliberate no-op resting on an apply being on its way — so a stale
// "queued" leaves the stage running behind a run that will never move again,
// which nothing sweeps.
func TestWithdrawAutoApplyPromise_ReturnsTheStatusTheRowHolds(t *testing.T) {
	_, settled, _ := withdraw(t, &stubFinishes{})
	if settled != "planned" {
		t.Errorf("returned %q, want planned; the run row was rewritten to planned and callers key on this value", settled)
	}

	// When the corrective write fails the row is still "queued", and the value
	// handed downstream has to be the row's — a "planned" here would have the
	// advancer act on a status the database does not hold.
	_, stuck, _ := withdraw(t, &stubFinishes{fail: errors.New("deadlock detected")})
	if stuck != "queued" {
		t.Errorf("returned %q after a failed correction, want queued; the row still reads queued", stuck)
	}
}

// A run in a pipeline is the case the standalone-workspace fixture could not
// reach, and it is where the stale status is load-bearing: the advancer's
// queued arm does nothing, so the stage is left non-terminal behind a terminal
// run.
func TestRunJobLeavesNoStageRunningWhenTheAutoApplyIsWithdrawn(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, prID, stageID := seedTwoStagePipeline(t, ctx, "withdrawpipe", "stop")

	// The pipeline's second stage, its run created and auto-applying.
	var wsID string
	if err := testPool.QueryRow(ctx,
		`SELECT workspace_id FROM pipeline_run_stages WHERE id = $1`, stageID).Scan(&wsID); err != nil {
		t.Fatalf("read stage workspace: %v", err)
	}
	exec(t, ctx, `UPDATE workspaces SET auto_apply = TRUE WHERE id = $1`, wsID)

	var userID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM users WHERE org_id = $1 LIMIT 1`, orgID).Scan(&userID); err != nil {
		t.Fatalf("find seeded user: %v", err)
	}
	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "pending", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	exec(t, ctx, `UPDATE pipeline_run_stages SET run_id = $1, status = 'running' WHERE id = $2`, run.ID, stageID)

	// newTestRunWorker wires no River client, so the apply cannot be enqueued.
	w := newTestRunWorker(&recordingExecutor{})
	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "plan"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if finished.Status != "planned" {
		t.Fatalf("run status = %q, want planned", finished.Status)
	}

	// The stage must not still read "running" behind a run that is terminal.
	got := stageStatus(t, ctx, stageID)
	if got == "running" {
		t.Errorf("the stage is still running behind a run recorded %q that will never move again; nothing sweeps it", finished.Status)
	}
	if got != "awaiting_approval" {
		t.Errorf("stage status = %q, want awaiting_approval — the arm the advancer takes for a planned run, which an operator can act on", got)
	}
	_ = prID
}

// A Publish after Close reaches nobody: the memory streamer drops it and the
// Redis one has already cancelled the subscription and closed every attached
// channel. The notice says the apply an operator is waiting for is not coming,
// so a watcher whose last line is the completion message is watching the wrong
// thing.
//
// The ordering is a property of Work, so this drives Work. It needs a database
// and is skipped without one.
func TestRunJobPublishesTheWithdrawalToAnOpenStream(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "withdrawstream")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version,auto_apply)
		 VALUES ($1,$2,$3,$4,'upload','1.11.0',TRUE)`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "pending", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	stream := &recordingStreamer{}
	w := newTestRunWorker(&recordingExecutor{})
	w.streamer = stream

	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "plan"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	for _, line := range stream.afterClose {
		if strings.Contains(line, "not enqueued") {
			t.Fatalf("the withdrawal notice was published after the stream closed, so no watcher receives it:\n%s", line)
		}
	}
	if !strings.Contains(strings.Join(stream.published, "\n"), "not enqueued") {
		t.Errorf("no watcher was told the apply is not coming; published lines were:\n%s", strings.Join(stream.published, "\n"))
	}
}
