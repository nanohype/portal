package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"strings"
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

// An operation the executors cannot render is refused before anything runs, and
// that refusal arrives at failRun like any other pre-execution failure. failRun
// is the terminal path for all of them, so the stage above a refused run has to
// settle rather than sit "running" under a run that will never move.
//
// The refusal is the real one: a real LocalExecutor is driven through Work
// against a real archive, and the dispatch refuses the operation itself. Which
// operations the dispatch admits is held in internal/worker/executor; what this
// holds is that its refusal reaches the advancer.
//
// runs.operation is a NOT NULL run_operation column, so the row carries a name
// the enum admits while the job carries the one the dispatch does not — which is
// the shape a job outliving a migration takes.
func TestRunJobSettlesTheStageWhenTheExecutorRefusesTheOperation(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, prID, stageID := seedTwoStagePipeline(t, ctx, "refusedop", "stop")

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
		ConfigSource: "upload", ConfigVersionID: "cfg-1", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	exec(t, ctx, `UPDATE pipeline_run_stages SET run_id = $1, status = 'running' WHERE id = $2`, run.ID, stageID)

	w := newTestRunWorker(&executor.LocalExecutor{})
	w.storage = &stubBlobs{configArchive: minimalConfigArchive(t)}

	// The job names an operation the dispatch has no arm for. The executor
	// refuses it, and Work routes that refusal to failRun.
	err = w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "refresh"},
	})
	if err != nil {
		t.Fatalf("Work returned an error rather than settling the run: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if finished.Status != "errored" {
		t.Fatalf("run status = %q, want errored; the fixture did not reach the refusal", finished.Status)
	}
	if !strings.Contains(finished.ErrorMessage, "unknown operation") {
		t.Errorf("the run does not say the operation was refused: %q", finished.ErrorMessage)
	}

	var stageStatus string
	if err := testPool.QueryRow(ctx,
		`SELECT status FROM pipeline_run_stages WHERE id = $1`, stageID).Scan(&stageStatus); err != nil {
		t.Fatalf("read stage status: %v", err)
	}
	if stageStatus != "errored" {
		t.Errorf("the stage reads %q under a run the executor refused, want errored; \"running\" leaves the pipeline holding a stage nothing will finish", stageStatus)
	}

	var pipelineStatus string
	if err := testPool.QueryRow(ctx,
		`SELECT status FROM pipeline_runs WHERE id = $1`, prID).Scan(&pipelineStatus); err != nil {
		t.Fatalf("read pipeline status: %v", err)
	}
	if pipelineStatus != "errored" {
		t.Errorf("the pipeline run reads %q, want errored; \"running\" has GetActivePipelineRunForPipeline refuse every later run of this pipeline", pipelineStatus)
	}
}

// minimalConfigArchive is the smallest tree a run can stage: one empty module.
func minimalConfigArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("# empty\n")
	if err := tw.WriteHeader(&tar.Header{
		Name: "envs/production/main.tf", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}
