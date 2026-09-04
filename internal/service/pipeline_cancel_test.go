package service

import (
	"context"
	"errors"
	"testing"

	"github.com/nanohype/portal/internal/repository"
)

// Cancelling a pipeline run settles every row that would otherwise stay
// non-terminal under it. The live stage is the one the pending-stage sweep does
// not reach — CancelPendingPipelineRunStages matches 'pending' and
// 'importing_outputs' — so without its own write it reads 'running' under a
// cancelled pipeline, and the detail view returns that column verbatim.
//
// These need no database, because a gate that skips when one is absent reports
// green for a rule it did not check.

type fakeCancelStore struct {
	run    repository.PipelineRun
	stages []repository.PipelineRunStageWithWorkspace

	finishedStages  map[string]string
	cancelledPend   int
	finishedRun     string
	failFinishStage error
}

func (f *fakeCancelStore) GetPipelineRun(context.Context, repository.GetPipelineRunParams) (repository.PipelineRun, error) {
	return f.run, nil
}
func (f *fakeCancelStore) ListPipelineRunStages(context.Context, string) ([]repository.PipelineRunStageWithWorkspace, error) {
	return f.stages, nil
}
func (f *fakeCancelStore) FinishPipelineRunStage(_ context.Context, id, status string) (repository.PipelineRunStage, error) {
	if f.finishedStages == nil {
		f.finishedStages = map[string]string{}
	}
	f.finishedStages[id] = status
	return repository.PipelineRunStage{ID: id, Status: status}, f.failFinishStage
}
func (f *fakeCancelStore) CancelPendingPipelineRunStages(context.Context, string) error {
	f.cancelledPend++
	return nil
}
func (f *fakeCancelStore) FinishPipelineRun(_ context.Context, _, status string) (repository.PipelineRun, error) {
	f.finishedRun = status
	return repository.PipelineRun{Status: status}, nil
}

type fakeRunCanceller struct{ cancelled []string }

func (f *fakeRunCanceller) Cancel(_ context.Context, runID, _, _ string) (repository.Run, error) {
	f.cancelled = append(f.cancelled, runID)
	return repository.Run{ID: runID, Status: "cancelled"}, nil
}

func liveStagePipeline() (*fakeCancelStore, *fakeRunCanceller, *PipelineService) {
	runID := "run_live"
	store := &fakeCancelStore{
		run: repository.PipelineRun{ID: "pr_1", Status: "running", TotalStages: 3},
		stages: []repository.PipelineRunStageWithWorkspace{
			{PipelineRunStage: repository.PipelineRunStage{ID: "stage_0", Status: "completed", StageOrder: 0}},
			{PipelineRunStage: repository.PipelineRunStage{ID: "stage_1", Status: "running", StageOrder: 1, RunID: &runID, WorkspaceID: "ws_1"}},
			{PipelineRunStage: repository.PipelineRunStage{ID: "stage_2", Status: "pending", StageOrder: 2}},
		},
	}
	canceller := &fakeRunCanceller{}
	return store, canceller, &PipelineService{cancels: store, cancelRun: canceller}
}

func TestCancelRun_SettlesTheLiveStageTheSweepDoesNotReach(t *testing.T) {
	store, canceller, svc := liveStagePipeline()

	if _, err := svc.CancelRun(context.Background(), "pr_1", "org_1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	if len(canceller.cancelled) != 1 || canceller.cancelled[0] != "run_live" {
		t.Errorf("the live stage's run was not cancelled: %v", canceller.cancelled)
	}
	got := store.finishedStages["stage_1"]
	if got == "" {
		t.Fatal("the running stage was left untouched; CancelPendingPipelineRunStages matches only 'pending' and 'importing_outputs', so it reads 'running' under a cancelled pipeline for ever")
	}
	if got != "cancelled" {
		t.Errorf("the running stage was finished %q, want cancelled", got)
	}
	if store.cancelledPend == 0 {
		t.Error("the pending stages were left pending")
	}
	if store.finishedRun != "cancelled" {
		t.Errorf("the pipeline run was finished %q, want cancelled", store.finishedRun)
	}
}

// A stage that is not running is not this write's business — settling it would
// overwrite a completed stage's own outcome.
func TestCancelRun_LeavesStagesThatAreNotRunningAlone(t *testing.T) {
	store, _, svc := liveStagePipeline()

	if _, err := svc.CancelRun(context.Background(), "pr_1", "org_1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	for _, id := range []string{"stage_0", "stage_2"} {
		if status, ok := store.finishedStages[id]; ok {
			t.Errorf("%s was finished %q; only the running stage is settled here", id, status)
		}
	}
}

// The write is the only thing standing between the stage and a status nothing
// moves, so its failure has to say what is left behind.
func TestCancelRun_ReportsAFailureToSettleTheLiveStage(t *testing.T) {
	store, _, svc := liveStagePipeline()
	store.failFinishStage = errors.New("deadlock detected")

	if _, err := svc.CancelRun(context.Background(), "pr_1", "org_1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if store.finishedRun != "cancelled" {
		t.Errorf("the pipeline run was not settled after a stage write failed: %q", store.finishedRun)
	}
}
