// Hand-written pgx queries (sqlc-style); not generated, edit directly.

package repository

import "context"

// --- Pipelines ---

const pipelineColumns = `id, org_id, name, description, created_by, created_at, updated_at`

func scanPipeline(row interface{ Scan(...interface{}) error }) (Pipeline, error) {
	var p Pipeline
	err := row.Scan(&p.ID, &p.OrgID, &p.Name, &p.Description, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

type CreatePipelineParams struct {
	ID          string `json:"id"`
	OrgID       string `json:"org_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedBy   string `json:"created_by"`
}

func (q *Queries) CreatePipeline(ctx context.Context, arg CreatePipelineParams) (Pipeline, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO pipelines (id, org_id, name, description, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+pipelineColumns,
		arg.ID, arg.OrgID, arg.Name, arg.Description, arg.CreatedBy,
	)
	return scanPipeline(row)
}

type GetPipelineParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) GetPipeline(ctx context.Context, arg GetPipelineParams) (Pipeline, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+pipelineColumns+` FROM pipelines WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return scanPipeline(row)
}

func (q *Queries) ListPipelines(ctx context.Context, orgID string) ([]Pipeline, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+pipelineColumns+` FROM pipelines WHERE org_id = $1 ORDER BY created_at DESC`,
		orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pipelines []Pipeline
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return nil, err
		}
		pipelines = append(pipelines, p)
	}
	if pipelines == nil {
		pipelines = []Pipeline{}
	}
	return pipelines, rows.Err()
}

type UpdatePipelineParams struct {
	ID          string `json:"id"`
	OrgID       string `json:"org_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (q *Queries) UpdatePipeline(ctx context.Context, arg UpdatePipelineParams) (Pipeline, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE pipelines SET
			name = CASE WHEN $3 = '' THEN name ELSE $3 END,
			description = $4,
			updated_at = NOW()
		WHERE id = $1 AND org_id = $2
		RETURNING `+pipelineColumns,
		arg.ID, arg.OrgID, arg.Name, arg.Description,
	)
	return scanPipeline(row)
}

type DeletePipelineParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) DeletePipeline(ctx context.Context, arg DeletePipelineParams) error {
	_, err := q.db.Exec(ctx,
		`DELETE FROM pipelines WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return err
}

// --- Pipeline Stages ---

const pipelineStageColumns = `id, pipeline_id, workspace_id, stage_order, auto_apply, on_failure, created_at`

func scanPipelineStage(row interface{ Scan(...interface{}) error }) (PipelineStage, error) {
	var s PipelineStage
	err := row.Scan(&s.ID, &s.PipelineID, &s.WorkspaceID, &s.StageOrder, &s.AutoApply, &s.OnFailure, &s.CreatedAt)
	return s, err
}

type CreatePipelineStageParams struct {
	ID          string `json:"id"`
	PipelineID  string `json:"pipeline_id"`
	WorkspaceID string `json:"workspace_id"`
	StageOrder  int32  `json:"stage_order"`
	AutoApply   bool   `json:"auto_apply"`
	OnFailure   string `json:"on_failure"`
}

func (q *Queries) CreatePipelineStage(ctx context.Context, arg CreatePipelineStageParams) (PipelineStage, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO pipeline_stages (id, pipeline_id, workspace_id, stage_order, auto_apply, on_failure)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+pipelineStageColumns,
		arg.ID, arg.PipelineID, arg.WorkspaceID, arg.StageOrder, arg.AutoApply, arg.OnFailure,
	)
	return scanPipelineStage(row)
}

func (q *Queries) ListPipelineStages(ctx context.Context, pipelineID string) ([]PipelineStageWithWorkspace, error) {
	rows, err := q.db.Query(ctx,
		`SELECT ps.id, ps.pipeline_id, ps.workspace_id, ps.stage_order, ps.auto_apply, ps.on_failure, ps.created_at, w.name
		FROM pipeline_stages ps
		JOIN workspaces w ON w.id = ps.workspace_id
		WHERE ps.pipeline_id = $1
		ORDER BY ps.stage_order ASC`,
		pipelineID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stages []PipelineStageWithWorkspace
	for rows.Next() {
		var s PipelineStageWithWorkspace
		err := rows.Scan(&s.ID, &s.PipelineID, &s.WorkspaceID, &s.StageOrder, &s.AutoApply, &s.OnFailure, &s.CreatedAt, &s.WorkspaceName)
		if err != nil {
			return nil, err
		}
		stages = append(stages, s)
	}
	if stages == nil {
		stages = []PipelineStageWithWorkspace{}
	}
	return stages, rows.Err()
}

func (q *Queries) DeletePipelineStages(ctx context.Context, pipelineID string) error {
	_, err := q.db.Exec(ctx,
		`DELETE FROM pipeline_stages WHERE pipeline_id = $1`,
		pipelineID,
	)
	return err
}

// --- Pipeline Runs ---

const pipelineRunColumns = `id, pipeline_id, org_id, status, current_stage, total_stages, created_by, started_at, finished_at, created_at, updated_at`

func scanPipelineRun(row interface{ Scan(...interface{}) error }) (PipelineRun, error) {
	var r PipelineRun
	err := row.Scan(&r.ID, &r.PipelineID, &r.OrgID, &r.Status, &r.CurrentStage, &r.TotalStages, &r.CreatedBy, &r.StartedAt, &r.FinishedAt, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

type CreatePipelineRunParams struct {
	ID          string `json:"id"`
	PipelineID  string `json:"pipeline_id"`
	OrgID       string `json:"org_id"`
	TotalStages int32  `json:"total_stages"`
	CreatedBy   string `json:"created_by"`
}

func (q *Queries) CreatePipelineRun(ctx context.Context, arg CreatePipelineRunParams) (PipelineRun, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO pipeline_runs (id, pipeline_id, org_id, status, current_stage, total_stages, created_by)
		VALUES ($1, $2, $3, 'running', 0, $4, $5)
		RETURNING `+pipelineRunColumns,
		arg.ID, arg.PipelineID, arg.OrgID, arg.TotalStages, arg.CreatedBy,
	)
	return scanPipelineRun(row)
}

type GetPipelineRunParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) GetPipelineRun(ctx context.Context, arg GetPipelineRunParams) (PipelineRun, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+pipelineRunColumns+` FROM pipeline_runs WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return scanPipelineRun(row)
}

type ListPipelineRunsParams struct {
	PipelineID string `json:"pipeline_id"`
	OrgID      string `json:"org_id"`
	Limit      int32  `json:"limit"`
	Offset     int32  `json:"offset"`
}

func (q *Queries) ListPipelineRuns(ctx context.Context, arg ListPipelineRunsParams) ([]PipelineRun, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+pipelineRunColumns+` FROM pipeline_runs WHERE pipeline_id = $1 AND org_id = $2 ORDER BY created_at DESC LIMIT $3 OFFSET $4`,
		arg.PipelineID, arg.OrgID, arg.Limit, arg.Offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []PipelineRun
	for rows.Next() {
		r, err := scanPipelineRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	if runs == nil {
		runs = []PipelineRun{}
	}
	return runs, rows.Err()
}

func (q *Queries) CountPipelineRuns(ctx context.Context, pipelineID, orgID string) (int64, error) {
	row := q.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM pipeline_runs WHERE pipeline_id = $1 AND org_id = $2`,
		pipelineID, orgID,
	)
	var count int64
	err := row.Scan(&count)
	return count, err
}

type UpdatePipelineRunStatusParams struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	CurrentStage int32  `json:"current_stage"`
}

func (q *Queries) UpdatePipelineRunStatus(ctx context.Context, arg UpdatePipelineRunStatusParams) (PipelineRun, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE pipeline_runs SET status = $2::pipeline_status, current_stage = $3, updated_at = NOW() WHERE id = $1 RETURNING `+pipelineRunColumns,
		arg.ID, arg.Status, arg.CurrentStage,
	)
	return scanPipelineRun(row)
}

func (q *Queries) FinishPipelineRun(ctx context.Context, id, status string) (PipelineRun, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE pipeline_runs SET status = $2::pipeline_status, finished_at = NOW(), updated_at = NOW() WHERE id = $1 RETURNING `+pipelineRunColumns,
		id, status,
	)
	return scanPipelineRun(row)
}

func (q *Queries) GetActivePipelineRunForPipeline(ctx context.Context, pipelineID, orgID string) (PipelineRun, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+pipelineRunColumns+` FROM pipeline_runs WHERE pipeline_id = $1 AND org_id = $2 AND status = 'running' LIMIT 1`,
		pipelineID, orgID,
	)
	return scanPipelineRun(row)
}

// --- Pipeline Run Stages ---

const pipelineRunStageColumns = `id, pipeline_run_id, stage_id, workspace_id, run_id, stage_order, status, auto_apply, on_failure, started_at, finished_at, created_at, updated_at`

func scanPipelineRunStage(row interface{ Scan(...interface{}) error }) (PipelineRunStage, error) {
	var s PipelineRunStage
	err := row.Scan(&s.ID, &s.PipelineRunID, &s.StageID, &s.WorkspaceID, &s.RunID, &s.StageOrder, &s.Status, &s.AutoApply, &s.OnFailure, &s.StartedAt, &s.FinishedAt, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

type CreatePipelineRunStageParams struct {
	ID            string `json:"id"`
	PipelineRunID string `json:"pipeline_run_id"`
	StageID       string `json:"stage_id"`
	WorkspaceID   string `json:"workspace_id"`
	StageOrder    int32  `json:"stage_order"`
	AutoApply     bool   `json:"auto_apply"`
	OnFailure     string `json:"on_failure"`
}

func (q *Queries) CreatePipelineRunStage(ctx context.Context, arg CreatePipelineRunStageParams) (PipelineRunStage, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO pipeline_run_stages (id, pipeline_run_id, stage_id, workspace_id, stage_order, auto_apply, on_failure)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+pipelineRunStageColumns,
		arg.ID, arg.PipelineRunID, arg.StageID, arg.WorkspaceID, arg.StageOrder, arg.AutoApply, arg.OnFailure,
	)
	return scanPipelineRunStage(row)
}

func (q *Queries) ListPipelineRunStages(ctx context.Context, pipelineRunID string) ([]PipelineRunStageWithWorkspace, error) {
	rows, err := q.db.Query(ctx,
		`SELECT prs.id, prs.pipeline_run_id, prs.stage_id, prs.workspace_id, prs.run_id, prs.stage_order, prs.status, prs.auto_apply, prs.on_failure, prs.started_at, prs.finished_at, prs.created_at, prs.updated_at, w.name
		FROM pipeline_run_stages prs
		JOIN workspaces w ON w.id = prs.workspace_id
		WHERE prs.pipeline_run_id = $1
		ORDER BY prs.stage_order ASC`,
		pipelineRunID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stages []PipelineRunStageWithWorkspace
	for rows.Next() {
		var s PipelineRunStageWithWorkspace
		err := rows.Scan(&s.ID, &s.PipelineRunID, &s.StageID, &s.WorkspaceID, &s.RunID, &s.StageOrder, &s.Status, &s.AutoApply, &s.OnFailure, &s.StartedAt, &s.FinishedAt, &s.CreatedAt, &s.UpdatedAt, &s.WorkspaceName)
		if err != nil {
			return nil, err
		}
		stages = append(stages, s)
	}
	if stages == nil {
		stages = []PipelineRunStageWithWorkspace{}
	}
	return stages, rows.Err()
}

// GetPipelineRunStageByRunID finds a pipeline run stage by its workspace run ID.
// Used by the worker callback to detect if a completed run belongs to a pipeline.
func (q *Queries) GetPipelineRunStageByRunID(ctx context.Context, runID string) (PipelineRunStage, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+pipelineRunStageColumns+` FROM pipeline_run_stages WHERE run_id = $1`,
		runID,
	)
	return scanPipelineRunStage(row)
}

func (q *Queries) GetPipelineRunStageByOrder(ctx context.Context, pipelineRunID string, stageOrder int32) (PipelineRunStage, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+pipelineRunStageColumns+` FROM pipeline_run_stages WHERE pipeline_run_id = $1 AND stage_order = $2`,
		pipelineRunID, stageOrder,
	)
	return scanPipelineRunStage(row)
}

type UpdatePipelineRunStageStatusParams struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (q *Queries) UpdatePipelineRunStageStatus(ctx context.Context, arg UpdatePipelineRunStageStatusParams) (PipelineRunStage, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE pipeline_run_stages SET status = $2::pipeline_stage_status, updated_at = NOW() WHERE id = $1 RETURNING `+pipelineRunStageColumns,
		arg.ID, arg.Status,
	)
	return scanPipelineRunStage(row)
}

func (q *Queries) StartPipelineRunStage(ctx context.Context, id, status string) (PipelineRunStage, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE pipeline_run_stages SET status = $2::pipeline_stage_status, started_at = NOW(), updated_at = NOW() WHERE id = $1 RETURNING `+pipelineRunStageColumns,
		id, status,
	)
	return scanPipelineRunStage(row)
}

func (q *Queries) FinishPipelineRunStage(ctx context.Context, id, status string) (PipelineRunStage, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE pipeline_run_stages SET status = $2::pipeline_stage_status, finished_at = NOW(), updated_at = NOW() WHERE id = $1 RETURNING `+pipelineRunStageColumns,
		id, status,
	)
	return scanPipelineRunStage(row)
}

func (q *Queries) SetPipelineRunStageRunID(ctx context.Context, id, runID string) error {
	_, err := q.db.Exec(ctx,
		`UPDATE pipeline_run_stages SET run_id = $2, status = 'running'::pipeline_stage_status, started_at = COALESCE(started_at, NOW()), updated_at = NOW() WHERE id = $1`,
		id, runID,
	)
	return err
}

// CancelPendingPipelineRunStages cancels all stages in a pipeline run that haven't started yet.
func (q *Queries) CancelPendingPipelineRunStages(ctx context.Context, pipelineRunID string) error {
	_, err := q.db.Exec(ctx,
		`UPDATE pipeline_run_stages SET status = 'cancelled'::pipeline_stage_status, finished_at = NOW(), updated_at = NOW()
		WHERE pipeline_run_id = $1 AND status IN ('pending', 'importing_outputs')`,
		pipelineRunID,
	)
	return err
}

// HasActivePipelineRuns checks if a pipeline has any running pipeline runs.
func (q *Queries) HasActivePipelineRuns(ctx context.Context, pipelineID string) (bool, error) {
	row := q.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pipeline_runs WHERE pipeline_id = $1 AND status = 'running')`,
		pipelineID,
	)
	var exists bool
	err := row.Scan(&exists)
	return exists, err
}
