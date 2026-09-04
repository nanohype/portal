package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker/executor"
)

// A pipeline stage advances when the run under it reaches a terminal state.
// Every terminal state has to reach the advancer, not just the successful ones:
// a stage left "running" under a run that will never move again sits beneath a
// pipeline row that reads active for ever, and
// GetActivePipelineRunForPipeline then refuses every later run of that
// pipeline.
//
// These drive the terminal paths that settle a run from inside the worker's own
// finish handling — failRun and settleFinishedRun — rather than the advancer's
// arms, so one of those that never calls the advancer fails here. They need no
// database: every store those paths touch is behind an interface, which is what
// makes the property assertable rather than skippable.
//
// They enter below Work's dispatch, so the terminal paths above it are outside
// what they reach and are held elsewhere: Work's mid-flight cancel branch by
// TestRunJobAdvancesThePipelineWhenTheRunIsCancelledMidExecution, and a run
// settled through the API by
// TestRunServiceCancelSettlesTheStageAndPipelineAboveTheRun in internal/service.
// Both drive the real path against a database and read the stage and pipeline
// rows back, because neither is reachable from a store interface.

// recordingStreamer answers whether a publish would have reached anyone. A
// Publish after Close is dropped by the memory streamer and lands on a cancelled
// subscription in the Redis one, so ordering is the property, not the call.
type recordingStreamer struct {
	published    []string
	closed       bool
	afterClose   []string
	closedBefore int
}

func (s *recordingStreamer) Publish(_ string, data []byte) {
	if s.closed {
		s.afterClose = append(s.afterClose, string(data))
		return
	}
	s.published = append(s.published, string(data))
}
func (s *recordingStreamer) Subscribe(string) <-chan []byte    { return nil }
func (s *recordingStreamer) Unsubscribe(string, <-chan []byte) {}
func (s *recordingStreamer) Close(string) {
	s.closed = true
	s.closedBefore = len(s.published)
}

type stubSlots struct {
	released int
	fail     error
}

func (s *stubSlots) ReleaseWorkspaceRun(context.Context, string, string, string) error {
	s.released++
	return s.fail
}

// terminalWorker is a worker whose every terminal-path store is a stub, so the
// paths run with no database.
func terminalWorker(pipelines *stubPipelines, priorStatus string) (*RunJobWorker, *stubPipelines, *recordingStreamer) {
	stream := &recordingStreamer{}
	w := &RunJobWorker{
		pipelines: pipelines,
		runs:      &stubRuns{run: repository.Run{Status: priorStatus}},
		finishes:  &stubFinishes{},
		slots:     &stubSlots{},
		streamer:  stream,
	}
	return w, pipelines, stream
}

func failedRunArgs() RunJobArgs {
	return RunJobArgs{RunID: "run_1", WorkspaceID: "ws_1", OrgID: "org_1", Operation: "apply"}
}

// ── a run that fails ───────────────────────────────────────────────────────

// The path every executor error and every pre-execution refusal ends on. Before
// this, it was the only terminal path that never advanced, so the advancer's
// errored arm was unreachable from production entirely.
func TestFailRun_AdvancesItsPipelineAsErrored(t *testing.T) {
	st := onePipeline("stop", 1)
	w, _, _ := terminalWorker(st, "applying")

	var buf strings.Builder
	if err := w.failRun(context.Background(), failedRunArgs(), capture(&buf),
		errors.New("Error: creating EC2 instance"), ""); err != nil {
		t.Fatalf("failRun: %v", err)
	}

	if len(st.finishedStage) == 0 {
		t.Fatal("a failed run left its pipeline stage untouched; the stage reads 'running' under a run that will never move and nothing sweeps it")
	}
	if st.finishedStage[0] != "errored" {
		t.Errorf("the stage was finished %q, want errored", st.finishedStage[0])
	}
	if len(st.finishedRun) == 0 || st.finishedRun[0] != "errored" {
		t.Errorf("the pipeline run was finished %v, want errored; while it reads running every later run of this pipeline is refused", st.finishedRun)
	}
}

// A run cancelled through the API while the worker was executing it is settled
// by the same path, and it is as terminal as a failure.
func TestFailRun_AdvancesItsPipelineAsCancelledWhenTheRunWasCancelled(t *testing.T) {
	st := onePipeline("stop", 1)
	w, _, _ := terminalWorker(st, "cancelled")

	var buf strings.Builder
	if err := w.failRun(context.Background(), failedRunArgs(), capture(&buf),
		errors.New("Error: creating EC2 instance"), ""); err != nil {
		t.Fatalf("failRun: %v", err)
	}

	if len(st.finishedStage) == 0 || st.finishedStage[0] != "cancelled" {
		t.Errorf("the stage was finished %v, want cancelled; a cancelled run is terminal and its stage cannot stay running", st.finishedStage)
	}
	if len(st.finishedRun) == 0 || st.finishedRun[0] != "cancelled" {
		t.Errorf("the pipeline run was finished %v, want cancelled", st.finishedRun)
	}
}

// A run that belongs to no pipeline must still settle, and quietly.
func TestFailRun_SettlesARunInNoPipeline(t *testing.T) {
	st := onePipeline("stop", 1)
	st.failLookup = pgx.ErrNoRows
	w, _, stream := terminalWorker(st, "applying")

	var buf strings.Builder
	if err := w.failRun(context.Background(), failedRunArgs(), capture(&buf),
		errors.New("boom"), ""); err != nil {
		t.Fatalf("failRun: %v", err)
	}
	if len(st.finishedStage) != 0 || len(st.finishedRun) != 0 {
		t.Errorf("a run in no pipeline advanced one: stages=%v runs=%v", st.finishedStage, st.finishedRun)
	}
	if !stream.closed {
		t.Error("the run's log stream was left open")
	}
}

// ── the advancer's own arms ────────────────────────────────────────────────

// The errored arm's stage write. Its two other writes are pinned; this one was
// not, so the arm could drop the write that stops the stage reading "running" —
// the exact state the arm exists to leave behind.
func TestAdvance_ErroredMarksItsOwnStageErrored(t *testing.T) {
	st := onePipeline("stop", 1)
	advance(t, st, "errored")

	if len(st.finishedStage) == 0 {
		t.Fatal("an errored run did not mark its own stage; the stage reads 'running' under an errored pipeline")
	}
	if st.finishedStage[0] != "errored" {
		t.Errorf("the stage was finished %q, want errored", st.finishedStage[0])
	}
}

func TestAdvance_CancelledSettlesTheStageAndTheRun(t *testing.T) {
	st := onePipeline("stop", 2)
	advance(t, st, "cancelled")

	if len(st.finishedStage) == 0 || st.finishedStage[0] != "cancelled" {
		t.Errorf("the stage was finished %v, want cancelled", st.finishedStage)
	}
	if st.cancelled == 0 {
		t.Error("the pipeline's pending stages were left pending under a cancelled run")
	}
	if len(st.finishedRun) == 0 || st.finishedRun[0] != "cancelled" {
		t.Errorf("the pipeline run was finished %v, want cancelled", st.finishedRun)
	}
}

// ── the withdrawal notice reaches a watcher ────────────────────────────────

// This covers the helper: given an open stream, the notice is published to it.
//
// It does NOT cover the ordering that made the notice undeliverable in
// production — whether Work closes the stream before reaching the withdrawal.
// That is a property of Work, which cannot run without a database;
// TestRunJobPublishesTheWithdrawalToAnOpenStream asserts it and is skipped when
// no database is present. Nothing here stands in for it.
func TestWithdrawAutoApplyPromise_PublishesToAnOpenStream(t *testing.T) {
	stream := &recordingStreamer{}
	w := &RunJobWorker{finishes: &stubFinishes{}, streamer: stream}

	var buf strings.Builder
	added, changed, deleted := int32(3), int32(1), int32(0)
	w.withdrawAutoApplyPromise(context.Background(), failedRunArgs(),
		&executor.ExecuteResult{Output: "Plan: 3 to add", ResourcesAdded: added, ResourcesChanged: changed, ResourcesDeleted: deleted},
		errors.New("connection reset by peer"), nil, capture(&buf))

	if len(stream.published) == 0 {
		t.Fatal("the notice was not published at all")
	}
	if !strings.Contains(strings.Join(stream.published, "\n"), "not enqueued") {
		t.Errorf("the published lines do not carry the notice: %q", stream.published)
	}
}

// ── the successful run's tail ──────────────────────────────────────────────

// A workspace that auto-applies and a worker that cannot enqueue: the promise is
// withdrawn, and everything downstream has to read the status the row now
// carries rather than the one the caller arrived with.
func settleAutoApply(t *testing.T, st *stubPipelines) (*stubPipelines, *stubFinishes, *recordingStreamer) {
	t.Helper()
	stream := &recordingStreamer{}
	fin := &stubFinishes{}
	w := &RunJobWorker{
		pipelines: st,
		finishes:  fin,
		slots:     &stubSlots{},
		streamer:  stream,
	}
	added, changed, deleted := int32(3), int32(1), int32(0)
	var buf strings.Builder
	if err := w.settleFinishedRun(context.Background(), failedRunArgs(),
		&executor.ExecuteResult{Output: "Plan: 3 to add", ResourcesAdded: added, ResourcesChanged: changed, ResourcesDeleted: deleted},
		"queued", nil, capture(&buf)); err != nil {
		t.Fatalf("settleFinishedRun: %v", err)
	}
	return st, fin, stream
}

func TestSettleFinishedRun_AdvancesAtTheWithdrawnStatus(t *testing.T) {
	st, fin, _ := settleAutoApply(t, onePipeline("stop", 2))

	if len(fin.finished) == 0 || fin.finished[0].Status != "planned" {
		t.Fatalf("the run row was settled %v, want planned", fin.finished)
	}
	// "queued" is the advancer's no-op arm. Reaching it here leaves the stage
	// running behind a run that will never move.
	if len(st.pausedStage) == 0 {
		t.Fatal("the stage was left running behind a withdrawn auto-apply; the advancer was handed the status the row stopped holding")
	}
	if st.pausedStage[0] != "awaiting_approval" {
		t.Errorf("the stage was moved to %q, want awaiting_approval — the arm a planned run takes", st.pausedStage[0])
	}
}

// The notice has to reach a watcher, so it is published while the stream is
// open. This is the ordering inside the settle tail, which the helper's own test
// cannot see.
func TestSettleFinishedRun_PublishesTheWithdrawalBeforeClosing(t *testing.T) {
	_, _, stream := settleAutoApply(t, onePipeline("stop", 2))

	for _, line := range stream.afterClose {
		if strings.Contains(line, "not enqueued") {
			t.Fatalf("the withdrawal notice was published after the stream closed, so no watcher receives it:\n%s", line)
		}
	}
	if !strings.Contains(strings.Join(stream.published, "\n"), "not enqueued") {
		t.Errorf("no watcher was told the apply is not coming; published lines were:\n%s", strings.Join(stream.published, "\n"))
	}
}

// A run that is not queued for an auto-apply settles at the status it arrived
// with, or the withdrawal above would be indistinguishable from the ordinary
// path.
func TestSettleFinishedRun_AdvancesAtTheStatusItArrivedWith(t *testing.T) {
	st := onePipeline("stop", 2)
	stream := &recordingStreamer{}
	w := &RunJobWorker{pipelines: st, finishes: &stubFinishes{}, slots: &stubSlots{}, streamer: stream}

	var buf strings.Builder
	if err := w.settleFinishedRun(context.Background(), failedRunArgs(),
		&executor.ExecuteResult{Output: "applied"}, "applied", nil, capture(&buf)); err != nil {
		t.Fatalf("settleFinishedRun: %v", err)
	}
	if len(st.finishedStage) == 0 || st.finishedStage[0] != "completed" {
		t.Errorf("the stage was finished %v, want completed", st.finishedStage)
	}
}

// ── a stage whose inputs never arrived ─────────────────────────────────────

// A stage exists to run against what the stage before it produced. Creating the
// run anyway plans against whatever the target workspace happens to hold, with
// no line in the plan naming the substitution, and the stage reports success.
//
// This drives the job with no database, so the rule is checked wherever the
// suite runs rather than only where a Postgres happens to be listening.

type stubStages struct {
	run   repository.PipelineRun
	stage repository.PipelineRunStage
	prev  repository.PipelineRunStage

	finishedStage []string
	finishedRun   []string
	cancelled     int
	linkedRun     int
}

func (s *stubStages) GetPipelineRun(context.Context, repository.GetPipelineRunParams) (repository.PipelineRun, error) {
	return s.run, nil
}
func (s *stubStages) GetPipelineRunStageByOrder(_ context.Context, _ string, order int32) (repository.PipelineRunStage, error) {
	if order == s.stage.StageOrder {
		return s.stage, nil
	}
	return s.prev, nil
}
func (s *stubStages) StartPipelineRunStage(_ context.Context, id, status string) (repository.PipelineRunStage, error) {
	return repository.PipelineRunStage{ID: id, Status: status}, nil
}
func (s *stubStages) UpdatePipelineRunStatus(context.Context, repository.UpdatePipelineRunStatusParams) (repository.PipelineRun, error) {
	return s.run, nil
}
func (s *stubStages) SetPipelineRunStageRunID(context.Context, string, string) error {
	s.linkedRun++
	return nil
}
func (s *stubStages) FinishPipelineRunStage(_ context.Context, id, status string) (repository.PipelineRunStage, error) {
	s.finishedStage = append(s.finishedStage, status)
	return repository.PipelineRunStage{ID: id, Status: status}, nil
}
func (s *stubStages) CancelPendingPipelineRunStages(context.Context, string) error {
	s.cancelled++
	return nil
}
func (s *stubStages) FinishPipelineRun(_ context.Context, _, status string) (repository.PipelineRun, error) {
	s.finishedRun = append(s.finishedRun, status)
	return repository.PipelineRun{Status: status}, nil
}

func secondStage(onFailure string) *stubStages {
	return &stubStages{
		run:   repository.PipelineRun{ID: "pr_1", Status: "running", TotalStages: 2},
		stage: repository.PipelineRunStage{ID: "stage_1", PipelineRunID: "pr_1", StageOrder: 1, Status: "pending", OnFailure: onFailure, WorkspaceID: "ws_b"},
		prev:  repository.PipelineRunStage{ID: "stage_0", PipelineRunID: "pr_1", StageOrder: 0, Status: "completed", WorkspaceID: "ws_a"},
	}
}

func runStage(t *testing.T, st *stubStages, importErr error) int {
	t.Helper()
	created := 0
	w := &PipelineStageJobWorker{
		stages:        st,
		importOutputs: func(context.Context, string, string, string) error { return importErr },
		createRun: func(context.Context, string, string, string, string, *bool) (repository.Run, error) {
			created++
			return repository.Run{ID: "run_new"}, nil
		},
	}
	if err := w.Work(context.Background(), &river.Job[PipelineStageJobArgs]{
		Args: PipelineStageJobArgs{PipelineRunID: "pr_1", StageOrder: 1, OrgID: "org_1", CreatedBy: "user_1"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}
	return created
}

func TestPipelineStage_CreatesNoRunWhenTheUpstreamOutputsDidNotArrive(t *testing.T) {
	st := secondStage("stop")
	created := runStage(t, st, errors.New("fetch source state: 503 SlowDown"))

	if created != 0 {
		t.Errorf("%d run(s) were created for a stage whose inputs never arrived; the plan would run against whatever the workspace held", created)
	}
	if len(st.finishedStage) == 0 || st.finishedStage[0] != "errored" {
		t.Errorf("the stage was finished %v, want errored", st.finishedStage)
	}
	if len(st.finishedRun) == 0 || st.finishedRun[0] != "errored" {
		t.Errorf("the pipeline run was finished %v, want errored", st.finishedRun)
	}
}

// on_failure "continue" is the pipeline saying it wants to proceed past a broken
// stage. The refusal routes through that setting rather than around it.
func TestPipelineStage_HonoursOnFailureContinueWithNoDatabase(t *testing.T) {
	st := secondStage("continue")
	runStage(t, st, errors.New("fetch source state: 503 SlowDown"))

	if len(st.finishedStage) == 0 || st.finishedStage[0] != "errored" {
		t.Errorf("the stage was finished %v, want errored", st.finishedStage)
	}
	if st.cancelled != 0 {
		t.Error("on_failure=continue cancelled the pipeline's pending stages")
	}
	if len(st.finishedRun) == 0 {
		t.Error("the last stage errored on continue and the pipeline run was left running")
	}
}

// A stage whose import succeeds creates its run. The refusal means nothing if
// this does not hold.
func TestPipelineStage_CreatesItsRunWhenTheImportSucceedsWithNoDatabase(t *testing.T) {
	st := secondStage("stop")
	created := runStage(t, st, nil)

	if created != 1 {
		t.Errorf("%d run(s) created, want 1", created)
	}
	if st.linkedRun != 1 {
		t.Errorf("the run was linked to the stage %d times, want 1", st.linkedRun)
	}
	if len(st.finishedStage) != 0 {
		t.Errorf("a clean stage was finished %v", st.finishedStage)
	}
}
