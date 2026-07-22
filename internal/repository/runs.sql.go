// Hand-written pgx queries (sqlc-style); not generated, edit directly.

package repository

import "context"

const runColumns = `id, workspace_id, org_id, operation, status, plan_output, plan_log_url, apply_log_url, resources_added, resources_changed, resources_deleted, error_message, commit_sha, config_source, config_repo_url, config_repo_branch, config_working_dir, config_version_id, config_tofu_version, plan_json_url, created_by, started_at, finished_at, created_at, updated_at`

func scanRun(row interface{ Scan(...interface{}) error }) (Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.OrgID, &r.Operation, &r.Status, &r.PlanOutput, &r.PlanLogURL, &r.ApplyLogURL, &r.ResourcesAdded, &r.ResourcesChanged, &r.ResourcesDeleted, &r.ErrorMessage, &r.CommitSHA, &r.ConfigSource, &r.ConfigRepoURL, &r.ConfigRepoBranch, &r.ConfigWorkingDir, &r.ConfigVersionID, &r.ConfigTofuVersion, &r.PlanJSONURL, &r.CreatedBy, &r.StartedAt, &r.FinishedAt, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

type GetRunParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) GetRun(ctx context.Context, arg GetRunParams) (Run, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+runColumns+` FROM runs WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return scanRun(row)
}

// GetRunInWorkspaceParams keys a run by the workspace it belongs to as well as
// its org.
type GetRunInWorkspaceParams struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	OrgID       string `json:"org_id"`
}

// GetRunInWorkspace is the lookup for HTTP routes under
// /workspaces/{workspaceID}/runs/{runID}: those are authorized against the
// workspace in the path, so the run has to belong to it. GetRun (org-scoped)
// stays for the worker and the pipeline advance path, which hold a run id
// straight off the job args and have no workspace in hand to check against.
func (q *Queries) GetRunInWorkspace(ctx context.Context, arg GetRunInWorkspaceParams) (Run, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+runColumns+` FROM runs WHERE id = $1 AND workspace_id = $2 AND org_id = $3`,
		arg.ID, arg.WorkspaceID, arg.OrgID,
	)
	return scanRun(row)
}

// GetRunInWorkspaceForUpdate is GetRunInWorkspace with the row lock the
// approval transaction takes.
func (q *Queries) GetRunInWorkspaceForUpdate(ctx context.Context, arg GetRunInWorkspaceParams) (Run, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+runColumns+` FROM runs WHERE id = $1 AND workspace_id = $2 AND org_id = $3 FOR UPDATE`,
		arg.ID, arg.WorkspaceID, arg.OrgID,
	)
	return scanRun(row)
}

type ListRunsByWorkspaceParams struct {
	WorkspaceID string `json:"workspace_id"`
	OrgID       string `json:"org_id"`
	Limit       int32  `json:"limit"`
	Offset      int32  `json:"offset"`
}

func (q *Queries) ListRunsByWorkspace(ctx context.Context, arg ListRunsByWorkspaceParams) ([]Run, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+runColumns+` FROM runs WHERE workspace_id = $1 AND org_id = $2 ORDER BY created_at DESC LIMIT $3 OFFSET $4`,
		arg.WorkspaceID, arg.OrgID, arg.Limit, arg.Offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	if runs == nil {
		runs = []Run{}
	}
	return runs, rows.Err()
}

type CountRunsByWorkspaceParams struct {
	WorkspaceID string `json:"workspace_id"`
	OrgID       string `json:"org_id"`
}

func (q *Queries) CountRunsByWorkspace(ctx context.Context, arg CountRunsByWorkspaceParams) (int64, error) {
	row := q.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM runs WHERE workspace_id = $1 AND org_id = $2`,
		arg.WorkspaceID, arg.OrgID,
	)
	var count int64
	err := row.Scan(&count)
	return count, err
}

// CreateRunParams carries the run's identity and the configuration it will
// execute. The config fields are resolved from the workspace by the service
// layer and frozen on the row — see RunService.Create.
type CreateRunParams struct {
	ID                string `json:"id"`
	WorkspaceID       string `json:"workspace_id"`
	OrgID             string `json:"org_id"`
	Operation         string `json:"operation"`
	Status            string `json:"status"`
	CreatedBy         string `json:"created_by"`
	CommitSHA         string `json:"commit_sha"`
	ConfigSource      string `json:"config_source"`
	ConfigRepoURL     string `json:"config_repo_url"`
	ConfigRepoBranch  string `json:"config_repo_branch"`
	ConfigWorkingDir  string `json:"config_working_dir"`
	ConfigVersionID   string `json:"config_version_id"`
	ConfigTofuVersion string `json:"config_tofu_version"`
}

func (q *Queries) CreateRun(ctx context.Context, arg CreateRunParams) (Run, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO runs (id, workspace_id, org_id, operation, status, created_by, commit_sha,
			config_source, config_repo_url, config_repo_branch, config_working_dir, config_version_id, config_tofu_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING `+runColumns,
		arg.ID, arg.WorkspaceID, arg.OrgID, arg.Operation, arg.Status, arg.CreatedBy, arg.CommitSHA,
		arg.ConfigSource, arg.ConfigRepoURL, arg.ConfigRepoBranch, arg.ConfigWorkingDir, arg.ConfigVersionID, arg.ConfigTofuVersion,
	)
	return scanRun(row)
}

type HasRecentRunForCommitParams struct {
	WorkspaceID string `json:"workspace_id"`
	CommitSHA   string `json:"commit_sha"`
}

func (q *Queries) HasRecentRunForCommit(ctx context.Context, arg HasRecentRunForCommitParams) (bool, error) {
	row := q.db.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM runs
			WHERE workspace_id = $1 AND commit_sha = $2 AND commit_sha != ''
			AND status NOT IN ('errored', 'cancelled')
		)`,
		arg.WorkspaceID, arg.CommitSHA,
	)
	var exists bool
	err := row.Scan(&exists)
	return exists, err
}

type UpdateRunStatusParams struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (q *Queries) UpdateRunStatus(ctx context.Context, arg UpdateRunStatusParams) (Run, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE runs SET status = $2, updated_at = NOW() WHERE id = $1 RETURNING `+runColumns,
		arg.ID, arg.Status,
	)
	return scanRun(row)
}

// PinRunCommitSHA records the commit a run executed, once. The guard on the
// column is the whole contract: a run's commit is set either by the VCS trigger
// that created it or by the first execution that resolved a checkout, and after
// that it is the tree that run means. A second execution of the same row — the
// apply that follows an approval, or an auto-apply — reads it rather than
// rewriting it, which is what stops the apply from landing on a branch that
// moved while the plan sat waiting for a signature.
func (q *Queries) PinRunCommitSHA(ctx context.Context, id, commitSHA string) error {
	_, err := q.db.Exec(ctx,
		`UPDATE runs SET commit_sha = $2, updated_at = NOW() WHERE id = $1 AND commit_sha = ''`,
		id, commitSHA,
	)
	return err
}

// MarkRunApproved transitions a signed-off run to the apply it was approved
// for: the operation becomes 'apply' and the status becomes whatever the
// approval transaction could take — 'queued' when it also took the workspace's
// run slot, 'pending' when another run holds it.
//
// The operation has to move with the status. Every path that enqueues a run
// reads the operation off the row (ClaimAndEnqueueNextRun), so a run left as
// 'plan' would be re-planned instead of applied when the slot frees up, and the
// approval would silently do nothing.
func (q *Queries) MarkRunApproved(ctx context.Context, id, status string) (Run, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE runs SET operation = 'apply', status = $2, updated_at = NOW() WHERE id = $1 RETURNING `+runColumns,
		id, status,
	)
	return scanRun(row)
}

// CancelRun atomically sets status to 'cancelled' only if the run is in a
// cancellable state and belongs to the named workspace. Returns pgx.ErrNoRows
// if the run was already in a terminal status, does not exist, or lives on a
// different workspace — the caller cannot tell those apart, which is the point:
// cancelling is reachable through the workspace that was authorized, and no
// other.
func (q *Queries) CancelRun(ctx context.Context, id, workspaceID, orgID string) (Run, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE runs SET status = 'cancelled', updated_at = NOW()
		WHERE id = $1 AND workspace_id = $2 AND org_id = $3
		AND status IN ('pending', 'queued', 'planning', 'planned', 'applying', 'awaiting_approval')
		RETURNING `+runColumns,
		id, workspaceID, orgID,
	)
	return scanRun(row)
}

type UpdateRunStartedParams struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (q *Queries) UpdateRunStarted(ctx context.Context, arg UpdateRunStartedParams) (Run, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE runs SET status = $2, started_at = NOW(), updated_at = NOW() WHERE id = $1 RETURNING `+runColumns,
		arg.ID, arg.Status,
	)
	return scanRun(row)
}

type UpdateRunFinishedParams struct {
	ID               string  `json:"id"`
	Status           string  `json:"status"`
	PlanOutput       *string `json:"plan_output"`
	ResourcesAdded   *int32  `json:"resources_added"`
	ResourcesChanged *int32  `json:"resources_changed"`
	ResourcesDeleted *int32  `json:"resources_deleted"`
	ErrorMessage     *string `json:"error_message"`
}

func (q *Queries) UpdateRunFinished(ctx context.Context, arg UpdateRunFinishedParams) (Run, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE runs
		SET status = $2,
			plan_output = COALESCE($3, plan_output),
			resources_added = COALESCE($4, resources_added),
			resources_changed = COALESCE($5, resources_changed),
			resources_deleted = COALESCE($6, resources_deleted),
			error_message = COALESCE($7, error_message),
			finished_at = NOW(),
			updated_at = NOW()
		WHERE id = $1
		RETURNING `+runColumns,
		arg.ID, arg.Status, arg.PlanOutput, arg.ResourcesAdded, arg.ResourcesChanged, arg.ResourcesDeleted, arg.ErrorMessage,
	)
	return scanRun(row)
}

func (q *Queries) GetNextPendingRun(ctx context.Context, workspaceID string) (Run, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+runColumns+` FROM runs WHERE workspace_id = $1 AND status = 'pending' ORDER BY created_at ASC LIMIT 1`,
		workspaceID,
	)
	return scanRun(row)
}

// HasRunsForWorkspace returns true if any runs exist for the workspace (blocks deletion).
func (q *Queries) HasRunsForWorkspace(ctx context.Context, workspaceID, orgID string) (bool, error) {
	row := q.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM runs WHERE workspace_id = $1 AND org_id = $2)`,
		workspaceID, orgID,
	)
	var exists bool
	err := row.Scan(&exists)
	return exists, err
}

func (q *Queries) GetActiveRunForWorkspace(ctx context.Context, workspaceID string) (Run, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+runColumns+` FROM runs WHERE workspace_id = $1 AND status IN ('pending', 'queued', 'planning', 'planned', 'awaiting_approval', 'applying') LIMIT 1`,
		workspaceID,
	)
	return scanRun(row)
}

type UpdateRunLogURLsParams struct {
	ID          string  `json:"id"`
	PlanLogURL  *string `json:"plan_log_url"`
	ApplyLogURL *string `json:"apply_log_url"`
}

type UpdateRunPlanJSONURLParams struct {
	ID          string `json:"id"`
	PlanJSONURL string `json:"plan_json_url"`
}

func (q *Queries) UpdateRunPlanJSONURL(ctx context.Context, arg UpdateRunPlanJSONURLParams) error {
	_, err := q.db.Exec(ctx,
		`UPDATE runs SET plan_json_url = $2, updated_at = NOW() WHERE id = $1`,
		arg.ID, arg.PlanJSONURL,
	)
	return err
}

func (q *Queries) UpdateRunLogURLs(ctx context.Context, arg UpdateRunLogURLsParams) (Run, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE runs
		SET plan_log_url = COALESCE($2, plan_log_url),
			apply_log_url = COALESCE($3, apply_log_url),
			updated_at = NOW()
		WHERE id = $1
		RETURNING `+runColumns,
		arg.ID, arg.PlanLogURL, arg.ApplyLogURL,
	)
	return scanRun(row)
}
