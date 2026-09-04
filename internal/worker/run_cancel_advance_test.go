package worker

import (
	"context"
	"testing"

	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker/executor"
)

// A run cancelled while the executor is still working reaches its terminal state
// through a branch of its own: Work sees the row already at "cancelled", skips
// the finish write, and returns. The stage above it is still "running", and a
// stage left non-terminal under a terminal run sits beneath a pipeline row that
// reads active for ever — GetActivePipelineRunForPipeline then refuses every
// later run of that pipeline.
//
// The stub tests in terminal_paths_test.go enter below Work's dispatch, so they
// cannot reach this branch. This drives Work itself against a real database with
// an executor that cancels the run mid-execution, and reads all three rows back.

// cancellingExecutor sets the run row to "cancelled" while it is executing,
// which is what a cancel issued through the API does to a run already claimed.
type cancellingExecutor struct {
	ctx    context.Context
	runID  string
	orgID  string
	cancel func(ctx context.Context, runID, orgID string)
}

func (e *cancellingExecutor) Execute(ctx context.Context, params executor.ExecuteParams) (*executor.ExecuteResult, error) {
	e.cancel(ctx, e.runID, e.orgID)
	params.LogCallback([]byte("executing\n"))
	return &executor.ExecuteResult{Output: "executing"}, nil
}

func TestRunJobAdvancesThePipelineWhenTheRunIsCancelledMidExecution(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, prID, stageID := seedTwoStagePipeline(t, ctx, "cancelmid", "stop")

	var wsID string
	if err := testPool.QueryRow(ctx,
		`SELECT workspace_id FROM pipeline_run_stages WHERE id = $1`, stageID).Scan(&wsID); err != nil {
		t.Fatalf("read stage workspace: %v", err)
	}
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

	w := newTestRunWorker(&cancellingExecutor{
		runID: run.ID, orgID: orgID,
		cancel: func(ctx context.Context, runID, orgID string) {
			exec(t, ctx, `UPDATE runs SET status = 'cancelled' WHERE id = $1 AND org_id = $2`, runID, orgID)
		},
	})
	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "plan"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if finished.Status != "cancelled" {
		t.Fatalf("run status = %q, want cancelled; the fixture did not reach the branch under test", finished.Status)
	}

	var stageStatus string
	if err := testPool.QueryRow(ctx,
		`SELECT status FROM pipeline_run_stages WHERE id = $1`, stageID).Scan(&stageStatus); err != nil {
		t.Fatalf("read stage status: %v", err)
	}
	if stageStatus != "cancelled" {
		t.Errorf("the stage reads %q under a run recorded cancelled, want cancelled; \"running\" leaves the pipeline holding a stage nothing will finish, and any completed state records a cancelled run as work that was done", stageStatus)
	}

	var pipelineStatus string
	if err := testPool.QueryRow(ctx,
		`SELECT status FROM pipeline_runs WHERE id = $1`, prID).Scan(&pipelineStatus); err != nil {
		t.Fatalf("read pipeline status: %v", err)
	}
	if pipelineStatus != "cancelled" {
		t.Errorf("the pipeline run reads %q, want cancelled; \"running\" has GetActivePipelineRunForPipeline refuse every later run of this pipeline, and any other terminal state reports a cancelled run as a pipeline that finished its work", pipelineStatus)
	}
}
