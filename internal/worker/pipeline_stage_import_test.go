package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/repository"
)

// A stage exists to run against what the stage before it produced, so a failed
// import fails the stage. A stage that creates its run regardless plans against
// whatever the target workspace happens to hold — the previous pipeline run's
// values, or none — and no line in the plan names the substitution.
//
// The refusal routes through the pipeline's own on_failure setting, which is
// where a pipeline says whether it wants to carry on past a broken stage.

// seedTwoStagePipeline builds a pipeline run whose second stage is pending, so
// the import path is the one under test.
func seedTwoStagePipeline(t *testing.T, ctx context.Context, tag, onFailure string) (orgID, pipelineRunID, stageRunStageID string) {
	t.Helper()
	orgID, userID := seedOrg(t, ctx, tag)

	wsA, wsB := id(), id()
	for _, ws := range []string{wsA, wsB} {
		exec(t, ctx,
			`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version)
			 VALUES ($1,$2,$3,$4,'upload','1.11.0')`,
			ws, orgID, "ws-"+ws, userID)
	}

	plID := id()
	exec(t, ctx, `INSERT INTO pipelines (id,org_id,name,created_by) VALUES ($1,$2,$3,$4)`,
		plID, orgID, "pl-"+plID, userID)
	for i, ws := range []string{wsA, wsB} {
		exec(t, ctx,
			`INSERT INTO pipeline_stages (id,pipeline_id,workspace_id,stage_order,on_failure)
			 VALUES ($1,$2,$3,$4,$5)`,
			id(), plID, ws, i, onFailure)
	}

	prID := id()
	exec(t, ctx,
		`INSERT INTO pipeline_runs (id,pipeline_id,org_id,status,total_stages,created_by)
		 VALUES ($1,$2,$3,'running',2,$4)`,
		prID, plID, orgID, userID)

	firstStage, secondStage := id(), id()
	stageIDs := map[int]string{0: firstStage, 1: secondStage}
	for i, ws := range []string{wsA, wsB} {
		status := "pending"
		if i == 0 {
			status = "completed"
		}
		exec(t, ctx,
			`INSERT INTO pipeline_run_stages (id,pipeline_run_id,stage_id,workspace_id,stage_order,status,on_failure)
			 VALUES ($1,$2,(SELECT id FROM pipeline_stages WHERE pipeline_id=$3 AND stage_order=$4),$5,$4,$6,$7)`,
			stageIDs[i], prID, plID, i, ws, status, onFailure)
	}
	return orgID, prID, secondStage
}

func runSecondStage(t *testing.T, ctx context.Context, orgID, prID string, importErr error) {
	t.Helper()
	importer := func(context.Context, string, string, string) error { return importErr }
	createRun := func(_ context.Context, workspaceID, orgID, operation, createdBy string, _ *bool) (repository.Run, error) {
		return testQueries.CreateRun(ctx, repository.CreateRunParams{
			ID: id(), WorkspaceID: workspaceID, OrgID: orgID, Operation: operation,
			Status: "pending", CreatedBy: createdBy,
			ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
		})
	}
	w := NewPipelineStageJobWorker(testQueries, createRun, importer)
	if err := w.Work(ctx, &river.Job[PipelineStageJobArgs]{
		Args: PipelineStageJobArgs{PipelineRunID: prID, StageOrder: 1, OrgID: orgID, CreatedBy: seedUserFor(t, ctx, orgID)},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}
}

func seedUserFor(t *testing.T, ctx context.Context, orgID string) string {
	t.Helper()
	var userID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM users WHERE org_id = $1 LIMIT 1`, orgID).Scan(&userID); err != nil {
		t.Fatalf("find seeded user: %v", err)
	}
	return userID
}

func stageStatus(t *testing.T, ctx context.Context, stageID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM pipeline_run_stages WHERE id = $1`, stageID).Scan(&status); err != nil {
		t.Fatalf("read stage status: %v", err)
	}
	return status
}

func pipelineRunStatus(t *testing.T, ctx context.Context, prID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM pipeline_runs WHERE id = $1`, prID).Scan(&status); err != nil {
		t.Fatalf("read pipeline run status: %v", err)
	}
	return status
}

func TestPipelineStage_FailsWhenTheUpstreamOutputsCannotBeImported(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, prID, stageID := seedTwoStagePipeline(t, ctx, "impfail", "stop")

	runSecondStage(t, ctx, orgID, prID, errors.New("fetch source state: 503 SlowDown"))

	if got := stageStatus(t, ctx, stageID); got != "errored" {
		t.Errorf("stage status = %q, want errored; it would otherwise plan against values the previous stage did not produce", got)
	}
	if got := pipelineRunStatus(t, ctx, prID); got != "errored" {
		t.Errorf("pipeline run status = %q, want errored", got)
	}

	var runs int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM runs r JOIN pipeline_run_stages s ON s.run_id = r.id WHERE s.pipeline_run_id = $1`, prID).Scan(&runs); err != nil {
		t.Fatalf("count stage runs: %v", err)
	}
	if runs != 0 {
		t.Errorf("%d run(s) were created for a stage whose inputs never arrived", runs)
	}
}

// on_failure "continue" is the pipeline saying it wants to proceed past a broken
// stage. Failing the import must route through that setting rather than around
// it, or the fix replaces one unasked-for behaviour with another.
func TestPipelineStage_HonoursOnFailureContinueForAFailedImport(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, prID, stageID := seedTwoStagePipeline(t, ctx, "impcont", "continue")

	runSecondStage(t, ctx, orgID, prID, errors.New("fetch source state: 503 SlowDown"))

	if got := stageStatus(t, ctx, stageID); got != "errored" {
		t.Errorf("stage status = %q, want errored", got)
	}
	// The second stage is the last, so continuing finishes the run rather than
	// enqueueing another stage.
	if got := pipelineRunStatus(t, ctx, prID); got == "running" {
		t.Errorf("pipeline run is still %q; on_failure=continue must settle it", got)
	}
}

// A stage whose import succeeds still creates its run. The refusal above means
// nothing if this does not hold.
func TestPipelineStage_CreatesItsRunWhenTheImportSucceeds(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, prID, stageID := seedTwoStagePipeline(t, ctx, "impok", "stop")

	runSecondStage(t, ctx, orgID, prID, nil)

	if got := stageStatus(t, ctx, stageID); got == "errored" {
		t.Fatalf("stage status = %q for a clean import", got)
	}
	var runID *string
	if err := testPool.QueryRow(ctx, `SELECT run_id FROM pipeline_run_stages WHERE id = $1`, stageID).Scan(&runID); err != nil {
		t.Fatalf("read stage run_id: %v", err)
	}
	if runID == nil || *runID == "" {
		t.Error("the stage created no run although its inputs arrived")
	}
}
