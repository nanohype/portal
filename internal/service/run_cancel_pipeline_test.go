package service_test

import (
	"context"
	"testing"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

// A run belonging to a pipeline stage can be cancelled through the run API
// rather than through the pipeline. The run row goes terminal either way, and a
// stage left 'running' under a terminal run sits beneath a pipeline row that
// reads active for ever — GetActivePipelineRunForPipeline then refuses every
// later run of that pipeline.
//
// PipelineService.CancelRun is the other direction and is covered by stubs in
// pipeline_cancel_test.go. This is the direction that starts at the run, so it
// drives RunService.Cancel and reads all three rows back.
func TestRunServiceCancelSettlesTheStageAndPipelineAboveTheRun(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "runcancelpipe")
	wsA, wsB := seedWorkspace(t, ctx, orgID, userID), seedWorkspace(t, ctx, orgID, userID)

	plID := id()
	exec(t, ctx, `INSERT INTO pipelines (id,org_id,name,created_by) VALUES ($1,$2,$3,$4)`,
		plID, orgID, "pl-"+plID, userID)
	for i, ws := range []string{wsA, wsB} {
		exec(t, ctx, `INSERT INTO pipeline_stages (id,pipeline_id,workspace_id,stage_order,on_failure)
		              VALUES ($1,$2,$3,$4,'stop')`, id(), plID, ws, i)
	}

	prID := id()
	exec(t, ctx, `INSERT INTO pipeline_runs (id,pipeline_id,org_id,status,current_stage,total_stages,created_by)
	              VALUES ($1,$2,$3,'running',1,2,$4)`, prID, plID, orgID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsB, OrgID: orgID, Operation: "plan", Status: "planning", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	stageID := id()
	exec(t, ctx, `INSERT INTO pipeline_run_stages (id,pipeline_run_id,stage_id,workspace_id,run_id,stage_order,status,on_failure)
	              VALUES ($1,$2,(SELECT id FROM pipeline_stages WHERE pipeline_id=$3 AND stage_order=1),$4,$5,1,'running','stop')`,
		stageID, prID, plID, wsB, run.ID)

	svc := service.NewRunService(testQueries, testPool, nil)
	cancelled, err := svc.Cancel(ctx, run.ID, wsB, orgID)
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("run status = %q, want cancelled; the fixture did not reach the path under test", cancelled.Status)
	}

	var stageStatus string
	if err := testPool.QueryRow(ctx,
		`SELECT status FROM pipeline_run_stages WHERE id = $1`, stageID).Scan(&stageStatus); err != nil {
		t.Fatalf("read stage status: %v", err)
	}
	if stageStatus != "cancelled" {
		t.Errorf("the stage reads %q under a run cancelled through the run API, want cancelled; \"running\" leaves the pipeline holding a stage nothing will finish, and any completed state records a cancelled run as work that was done", stageStatus)
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
