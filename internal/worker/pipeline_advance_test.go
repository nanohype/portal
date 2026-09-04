package worker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/nanohype/portal/internal/repository"
)

// A pipeline advances by writing state transitions. None of them can be retried:
// the run row is already final and the job is not re-run, so a write that fails
// leaves a row nothing sweeps. These tests make each write fail and require the
// advancement to say so — the log line is the only thing standing between a
// wedged pipeline and an operator who thinks it is still working.

type stubPipelines struct {
	stage      repository.PipelineRunStage
	run        repository.PipelineRun
	failLookup error
	failStage  error
	failRun    error
	failCancel error
	failPause  error

	finishedStage, finishedRun []string
	pausedStage                []string
	cancelled                  int
}

func (s *stubPipelines) GetPipelineRunStageByRunID(context.Context, string) (repository.PipelineRunStage, error) {
	return s.stage, s.failLookup
}
func (s *stubPipelines) GetPipelineRun(context.Context, repository.GetPipelineRunParams) (repository.PipelineRun, error) {
	return s.run, nil
}
func (s *stubPipelines) FinishPipelineRunStage(_ context.Context, _, status string) (repository.PipelineRunStage, error) {
	s.finishedStage = append(s.finishedStage, status)
	return repository.PipelineRunStage{}, s.failStage
}
func (s *stubPipelines) FinishPipelineRun(_ context.Context, _, status string) (repository.PipelineRun, error) {
	s.finishedRun = append(s.finishedRun, status)
	return repository.PipelineRun{}, s.failRun
}
func (s *stubPipelines) CancelPendingPipelineRunStages(context.Context, string) error {
	s.cancelled++
	return s.failCancel
}
func (s *stubPipelines) UpdatePipelineRunStageStatus(_ context.Context, arg repository.UpdatePipelineRunStageStatusParams) (repository.PipelineRunStage, error) {
	s.pausedStage = append(s.pausedStage, arg.Status)
	return repository.PipelineRunStage{}, s.failPause
}

// capture returns a logger writing into buf, so a test can require that a
// failure was reported rather than merely that nothing panicked.
func capture(buf *strings.Builder) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func advance(t *testing.T, st *stubPipelines, finalStatus string) string {
	t.Helper()
	var buf strings.Builder
	w := &RunJobWorker{pipelines: st}
	w.advancePipelineIfNeeded(context.Background(), "run_1", "org_1", finalStatus, capture(&buf))
	return buf.String()
}

func onePipeline(onFailure string, total int32) *stubPipelines {
	return &stubPipelines{
		stage: repository.PipelineRunStage{ID: "stage_1", PipelineRunID: "pr_1", StageOrder: 0, OnFailure: onFailure},
		run:   repository.PipelineRun{ID: "pr_1", Status: "running", TotalStages: total},
	}
}

func TestAdvance_ReportsAFailedStageTransition(t *testing.T) {
	st := onePipeline("stop", 1)
	st.failStage = errors.New("deadlock detected")

	out := advance(t, st, "applied")

	if !strings.Contains(out, "failed to finish pipeline stage") {
		t.Fatalf("a stage transition failed and nothing said so.\n%s", out)
	}
	if !strings.Contains(out, "deadlock detected") {
		t.Errorf("the cause is not in the log, so an operator cannot tell why:\n%s", out)
	}
}

// The run row staying 'running' blocks every later run of the pipeline, and
// nothing sweeps it. Announcing completion while that is true is worse than
// silence: it puts the log and the database in disagreement.
func TestAdvance_DoesNotAnnounceACompletionThatDidNotLand(t *testing.T) {
	st := onePipeline("stop", 1)
	st.failRun = errors.New("connection reset")

	out := advance(t, st, "applied")

	if !strings.Contains(out, "failed to finish pipeline run") {
		t.Fatalf("the terminal write failed and nothing said so.\n%s", out)
	}
	if strings.Contains(out, "pipeline completed") {
		t.Errorf("the log claims the pipeline completed while the row still reads 'running':\n%s", out)
	}
}

func TestAdvance_ReportsAFailedCancelOfPendingStages(t *testing.T) {
	st := onePipeline("stop", 3) // stop => cancel pending stages
	st.failCancel = errors.New("statement timeout")

	out := advance(t, st, "errored")

	if st.cancelled != 1 {
		t.Fatalf("pending stages were never cancelled (calls=%d)", st.cancelled)
	}
	if !strings.Contains(out, "failed to cancel pending pipeline stages") {
		t.Fatalf("the cancel failed and nothing said so; the UI keeps showing work nothing will pick up.\n%s", out)
	}
}

// The continue-on-failure path used to be a second copy of the success path's
// three calls with none of its error checks. Both now go through one helper, so
// this asserts the branch that was silent.
func TestAdvance_ContinueOnFailureReportsItsTerminalWrite(t *testing.T) {
	st := onePipeline("continue", 1) // last stage => finish the run, do not enqueue
	st.failRun = errors.New("connection reset")

	out := advance(t, st, "errored")

	if !strings.Contains(out, "failed to finish pipeline run") {
		t.Fatalf("the continue-on-failure terminal write failed silently.\n%s", out)
	}
	if st.cancelled != 0 {
		t.Errorf("continue-on-failure must not cancel later stages; cancelled=%d", st.cancelled)
	}
}

func TestAdvance_ReportsAFailedPause(t *testing.T) {
	st := onePipeline("stop", 3)
	st.failPause = errors.New("connection reset")

	out := advance(t, st, "awaiting_approval")

	if !strings.Contains(out, "failed to mark pipeline stage awaiting approval") {
		t.Fatalf("the pause write failed and nothing said so.\n%s", out)
	}
	if strings.Contains(out, "pipeline paused at stage") {
		t.Errorf("the log says the pipeline paused while the stage still reads 'running':\n%s", out)
	}
}

// Guard in the other direction: the happy path must still announce, or the
// tests above would be satisfied by an advancement that never logs anything.
func TestAdvance_StillAnnouncesASuccessfulCompletion(t *testing.T) {
	out := advance(t, onePipeline("stop", 1), "applied")

	if !strings.Contains(out, "pipeline completed") {
		t.Fatalf("a clean completion was not announced:\n%s", out)
	}
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("a clean completion logged an error:\n%s", out)
	}
}

// The lookup decides whether a run belongs to a pipeline at all, and only
// pgx.ErrNoRows answers that question. Any other error read as "no pipeline"
// leaves the pipeline waiting on the run at "running", which blocks every later
// run of that pipeline and which nothing sweeps.
//
// There is nowhere to fail to: the run is already terminal and this job is not
// retried. The pipeline cannot even be named, because the lookup that would name
// it is the one that failed. So the log is the surface, and it carries the
// consequence rather than the error alone.
func TestAdvance_ReportsAFailedStageLookup(t *testing.T) {
	st := onePipeline("stop", 1)
	st.failLookup = errors.New("connection reset by peer")

	out := advance(t, st, "applied")

	if !strings.Contains(out, "connection reset by peer") {
		t.Errorf("a failed stage lookup was read as 'no pipeline' and nothing said so:\n%s", out)
	}
	if !strings.Contains(out, "blocks later runs") {
		t.Errorf("the log does not carry the consequence, so an operator reading it does not know a pipeline is wedged:\n%s", out)
	}
	// Nothing may be advanced on a lookup that did not answer.
	if len(st.finishedStage) != 0 || len(st.finishedRun) != 0 {
		t.Errorf("the advancement acted on a stage it could not read: stages=%v runs=%v", st.finishedStage, st.finishedRun)
	}
}

// A run started from a workspace belongs to no pipeline, which is the ordinary
// case and must stay silent — otherwise every such run logs an error.
func TestAdvance_IsSilentForARunInNoPipeline(t *testing.T) {
	st := onePipeline("stop", 1)
	st.failLookup = pgx.ErrNoRows

	out := advance(t, st, "applied")

	if strings.Contains(out, "ERROR") || strings.Contains(out, "level=ERROR") {
		t.Errorf("a run in no pipeline logged an error:\n%s", out)
	}
	if len(st.finishedStage) != 0 {
		t.Errorf("a run in no pipeline advanced a stage: %v", st.finishedStage)
	}
}

// AdvancePipelineForTerminalRun is exported and builds a worker with no queue
// client, so a caller can finish one stage and have no way to start the next.
// Every other return on this path carries its consequence, and this one wedges a
// pipeline: without a line here the stage is finished, the next stays 'pending',
// and nothing records that anything was skipped.
func TestAdvance_ReportsThatItCannotStartTheNextStage(t *testing.T) {
	st := onePipeline("stop", 2)

	out := advance(t, st, "applied")

	if !strings.Contains(out, "no job queue client") {
		t.Fatalf("the next stage was not enqueued and nothing said so:\n%s", out)
	}
	if !strings.Contains(out, "stays 'pending'") {
		t.Errorf("the log does not carry the consequence, so an operator reading it does not know a pipeline is wedged:\n%s", out)
	}
}
