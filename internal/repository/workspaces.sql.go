// Hand-written pgx queries (sqlc-style); not generated, edit directly.

package repository

import (
	"context"
	"fmt"
)

const workspaceColumns = `id, org_id, name, description, repo_url, repo_branch, working_dir, tofu_version, environment, auto_apply, requires_approval, vcs_trigger_enabled, locked, locked_by, current_run_id, created_by, source, current_config_version_id, created_at, updated_at`

// workspaceColumnsQualified uses the "w." table alias, needed for joins that introduce ambiguous column names.
const workspaceColumnsQualified = `w.id, w.org_id, w.name, w.description, w.repo_url, w.repo_branch, w.working_dir, w.tofu_version, w.environment, w.auto_apply, w.requires_approval, w.vcs_trigger_enabled, w.locked, w.locked_by, w.current_run_id, w.created_by, w.source, w.current_config_version_id, w.created_at, w.updated_at`

func scanWorkspace(row interface{ Scan(...interface{}) error }) (Workspace, error) {
	var w Workspace
	err := row.Scan(&w.ID, &w.OrgID, &w.Name, &w.Description, &w.RepoURL, &w.RepoBranch, &w.WorkingDir, &w.TofuVersion, &w.Environment, &w.AutoApply, &w.RequiresApproval, &w.VcsTriggerEnabled, &w.Locked, &w.LockedBy, &w.CurrentRunID, &w.CreatedBy, &w.Source, &w.CurrentConfigVersionID, &w.CreatedAt, &w.UpdatedAt)
	return w, err
}

type GetWorkspaceParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) GetWorkspace(ctx context.Context, arg GetWorkspaceParams) (Workspace, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return scanWorkspace(row)
}

// WorkspaceGateRow names a workspace and says whether it gates its applies. It
// is what an authorization check needs about a workspace it is not otherwise
// reading — the id to match on, the name to put in an error message, and the
// gate to decide the bar.
type WorkspaceGateRow struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	RequiresApproval bool   `json:"requires_approval"`
}

// ListWorkspaceGates returns the gate rows for the named workspaces in one org.
// Ids that name nothing in this org are simply absent from the result, so a
// caller can tell "not mine" from "mine and ungated" and refuse the first.
func (q *Queries) ListWorkspaceGates(ctx context.Context, orgID string, ids []string) ([]WorkspaceGateRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.db.Query(ctx,
		`SELECT id, name, requires_approval FROM workspaces WHERE org_id = $1 AND id = ANY($2)`,
		orgID, ids,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var gates []WorkspaceGateRow
	for rows.Next() {
		var g WorkspaceGateRow
		if err := rows.Scan(&g.ID, &g.Name, &g.RequiresApproval); err != nil {
			return nil, err
		}
		gates = append(gates, g)
	}
	return gates, rows.Err()
}

type ListWorkspacesParams struct {
	OrgID  string `json:"org_id"`
	Limit  int32  `json:"limit"`
	Offset int32  `json:"offset"`
}

func (q *Queries) ListWorkspaces(ctx context.Context, arg ListWorkspacesParams) ([]Workspace, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces WHERE org_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		arg.OrgID, arg.Limit, arg.Offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workspaces []Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		workspaces = append(workspaces, w)
	}
	if workspaces == nil {
		workspaces = []Workspace{}
	}
	return workspaces, rows.Err()
}

func (q *Queries) CountWorkspaces(ctx context.Context, orgID string) (int64, error) {
	row := q.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM workspaces WHERE org_id = $1`, orgID,
	)
	var count int64
	err := row.Scan(&count)
	return count, err
}

type CreateWorkspaceParams struct {
	ID                string `json:"id"`
	OrgID             string `json:"org_id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	RepoURL           string `json:"repo_url"`
	RepoBranch        string `json:"repo_branch"`
	WorkingDir        string `json:"working_dir"`
	TofuVersion       string `json:"tofu_version"`
	Environment       string `json:"environment"`
	AutoApply         bool   `json:"auto_apply"`
	RequiresApproval  bool   `json:"requires_approval"`
	VcsTriggerEnabled bool   `json:"vcs_trigger_enabled"`
	CreatedBy         string `json:"created_by"`
	Source            string `json:"source"`
}

func (q *Queries) CreateWorkspace(ctx context.Context, arg CreateWorkspaceParams) (Workspace, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO workspaces (id, org_id, name, description, repo_url, repo_branch, working_dir, tofu_version, environment, auto_apply, requires_approval, vcs_trigger_enabled, created_by, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING `+workspaceColumns,
		arg.ID, arg.OrgID, arg.Name, arg.Description, arg.RepoURL, arg.RepoBranch, arg.WorkingDir, arg.TofuVersion, arg.Environment, arg.AutoApply, arg.RequiresApproval, arg.VcsTriggerEnabled, arg.CreatedBy, arg.Source,
	)
	return scanWorkspace(row)
}

type SetWorkspaceConfigVersionParams struct {
	ID                     string `json:"id"`
	OrgID                  string `json:"org_id"`
	CurrentConfigVersionID string `json:"current_config_version_id"`
}

func (q *Queries) SetWorkspaceConfigVersion(ctx context.Context, arg SetWorkspaceConfigVersionParams) (Workspace, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE workspaces SET current_config_version_id = $3, updated_at = NOW()
		WHERE id = $1 AND org_id = $2
		RETURNING `+workspaceColumns,
		arg.ID, arg.OrgID, arg.CurrentConfigVersionID,
	)
	return scanWorkspace(row)
}

type UpdateWorkspaceParams struct {
	ID                string `json:"id"`
	OrgID             string `json:"org_id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	RepoURL           string `json:"repo_url"`
	RepoBranch        string `json:"repo_branch"`
	WorkingDir        string `json:"working_dir"`
	TofuVersion       string `json:"tofu_version"`
	Environment       string `json:"environment"`
	AutoApply         *bool  `json:"auto_apply"`
	RequiresApproval  *bool  `json:"requires_approval"`
	VcsTriggerEnabled *bool  `json:"vcs_trigger_enabled"`
}

func (q *Queries) UpdateWorkspace(ctx context.Context, arg UpdateWorkspaceParams) (Workspace, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE workspaces
		SET name = COALESCE(NULLIF($3, ''), name),
			description = COALESCE(NULLIF($4, ''), description),
			repo_url = COALESCE(NULLIF($5, ''), repo_url),
			repo_branch = COALESCE(NULLIF($6, ''), repo_branch),
			working_dir = COALESCE(NULLIF($7, ''), working_dir),
			tofu_version = COALESCE(NULLIF($8, ''), tofu_version),
			environment = COALESCE(NULLIF($9, ''), environment),
			auto_apply = COALESCE($10, auto_apply),
			requires_approval = COALESCE($11, requires_approval),
			vcs_trigger_enabled = COALESCE($12, vcs_trigger_enabled),
			updated_at = NOW()
		WHERE id = $1 AND org_id = $2
		RETURNING `+workspaceColumns,
		arg.ID, arg.OrgID, arg.Name, arg.Description, arg.RepoURL, arg.RepoBranch, arg.WorkingDir, arg.TofuVersion, arg.Environment, arg.AutoApply, arg.RequiresApproval, arg.VcsTriggerEnabled,
	)
	return scanWorkspace(row)
}

type DeleteWorkspaceParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) DeleteWorkspace(ctx context.Context, arg DeleteWorkspaceParams) error {
	_, err := q.db.Exec(ctx,
		`DELETE FROM workspaces WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return err
}

func (q *Queries) LockWorkspace(ctx context.Context, id, orgID, lockedBy string) (Workspace, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE workspaces SET locked = TRUE, locked_by = $3, updated_at = NOW()
		WHERE id = $1 AND org_id = $2 AND locked = FALSE
		RETURNING `+workspaceColumns,
		id, orgID, lockedBy,
	)
	return scanWorkspace(row)
}

func (q *Queries) UnlockWorkspace(ctx context.Context, id, orgID string) (Workspace, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE workspaces SET locked = FALSE, locked_by = NULL, updated_at = NOW()
		WHERE id = $1 AND org_id = $2
		RETURNING `+workspaceColumns,
		id, orgID,
	)
	return scanWorkspace(row)
}

type SetWorkspaceCurrentRunParams struct {
	ID           string  `json:"id"`
	OrgID        string  `json:"org_id"`
	CurrentRunID *string `json:"current_run_id"`
}

func (q *Queries) SetWorkspaceCurrentRun(ctx context.Context, arg SetWorkspaceCurrentRunParams) error {
	_, err := q.db.Exec(ctx,
		`UPDATE workspaces SET current_run_id = $3, updated_at = NOW() WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID, arg.CurrentRunID,
	)
	return err
}

// ClaimWorkspaceForRun atomically takes the workspace's single run slot for
// runID, but only if the slot is free or runID already holds it. Returns the
// workspace id when claimed and pgx.ErrNoRows when a DIFFERENT run holds it. The
// conditional UPDATE serializes concurrent claimers on the workspace row — the
// basis for run serialization, so two runs can never execute against the same
// tofu state.
//
// Re-claiming for the run that already holds the slot succeeds because it grants
// nothing: the caller is the holder, so no other run can be executing. That
// matters on the approval path, where a plan that finished but whose slot
// release failed would otherwise be parked forever behind its own claim.
func (q *Queries) ClaimWorkspaceForRun(ctx context.Context, id, orgID, runID string) (string, error) {
	var claimed string
	err := q.db.QueryRow(ctx,
		`UPDATE workspaces SET current_run_id = $3, updated_at = NOW()
		 WHERE id = $1 AND org_id = $2 AND (current_run_id IS NULL OR current_run_id = $3)
		 RETURNING id`,
		id, orgID, runID,
	).Scan(&claimed)
	return claimed, err
}

// ReleaseWorkspaceRun frees the slot only if runID still holds it, so releasing
// a run that isn't the active holder (e.g. cancelling a queued run) can't free
// the running run's slot out from under it.
func (q *Queries) ReleaseWorkspaceRun(ctx context.Context, id, orgID, runID string) error {
	_, err := q.db.Exec(ctx,
		`UPDATE workspaces SET current_run_id = NULL, updated_at = NOW()
		 WHERE id = $1 AND org_id = $2 AND current_run_id = $3`,
		id, orgID, runID,
	)
	return err
}

// ReapStaleRunSlots frees workspace run slots that are wedged: the held run is
// already terminal (a completion path that failed to release), or it's still
// "active" but hasn't been touched in hours, meaning its worker job died or was
// discarded after exhausting retries. Without this a discarded run job leaves
// current_run_id pinned to a dead run and no future run for that workspace can
// ever be claimed. Returns the freed workspace ids so the caller can hand off to
// their next pending run.
func (q *Queries) ReapStaleRunSlots(ctx context.Context) ([]string, error) {
	rows, err := q.db.Query(ctx,
		`UPDATE workspaces w SET current_run_id = NULL, updated_at = NOW()
		 FROM runs r
		 WHERE w.current_run_id = r.id
		   AND (
		     r.status NOT IN ('pending','queued','planning','planned','awaiting_approval','applying')
		     OR r.updated_at < NOW() - INTERVAL '3 hours'
		   )
		 RETURNING w.id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var freed []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		freed = append(freed, id)
	}
	return freed, rows.Err()
}

func scanWorkspaceSummary(row interface{ Scan(...interface{}) error }) (WorkspaceSummary, error) {
	var ws WorkspaceSummary
	err := row.Scan(
		&ws.ID, &ws.OrgID, &ws.Name, &ws.Description, &ws.RepoURL, &ws.RepoBranch,
		&ws.WorkingDir, &ws.TofuVersion, &ws.Environment, &ws.AutoApply, &ws.RequiresApproval,
		&ws.VcsTriggerEnabled, &ws.Locked, &ws.LockedBy, &ws.CurrentRunID, &ws.CreatedBy,
		&ws.Source, &ws.CurrentConfigVersionID, &ws.CreatedAt, &ws.UpdatedAt,
		&ws.LastRunStatus, &ws.LastRunAt, &ws.ResourceCount,
	)
	return ws, err
}

type ListWorkspacesWithSummaryParams struct {
	OrgID       string `json:"org_id"`
	Limit       int32  `json:"limit"`
	Offset      int32  `json:"offset"`
	Search      string `json:"search"`
	Environment string `json:"environment"`
}

func (q *Queries) ListWorkspacesWithSummary(ctx context.Context, arg ListWorkspacesWithSummaryParams) ([]WorkspaceSummary, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+workspaceColumnsQualified+`,
		       lr.status AS last_run_status,
		       lr.created_at AS last_run_at,
		       COALESCE(sv.resource_count, 0) AS resource_count
		FROM workspaces w
		LEFT JOIN LATERAL (
		    SELECT status, created_at FROM runs WHERE workspace_id = w.id ORDER BY created_at DESC LIMIT 1
		) lr ON true
		LEFT JOIN LATERAL (
		    SELECT resource_count FROM state_versions WHERE workspace_id = w.id ORDER BY serial DESC LIMIT 1
		) sv ON true
		WHERE w.org_id = $1
		  AND ($4::TEXT = '' OR w.name ILIKE '%' || $4 || '%')
		  AND ($5::TEXT = '' OR w.environment = $5)
		ORDER BY w.created_at DESC LIMIT $2 OFFSET $3`,
		arg.OrgID, arg.Limit, arg.Offset, arg.Search, arg.Environment,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workspaces []WorkspaceSummary
	for rows.Next() {
		ws, err := scanWorkspaceSummary(rows)
		if err != nil {
			return nil, err
		}
		workspaces = append(workspaces, ws)
	}
	if workspaces == nil {
		workspaces = []WorkspaceSummary{}
	}
	return workspaces, rows.Err()
}

type CountWorkspacesFilteredParams struct {
	OrgID       string `json:"org_id"`
	Search      string `json:"search"`
	Environment string `json:"environment"`
}

func (q *Queries) CountWorkspacesFiltered(ctx context.Context, arg CountWorkspacesFilteredParams) (int64, error) {
	row := q.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM workspaces
		WHERE org_id = $1
		  AND ($2::TEXT = '' OR name ILIKE '%' || $2 || '%')
		  AND ($3::TEXT = '' OR environment = $3)`,
		arg.OrgID, arg.Search, arg.Environment,
	)
	var count int64
	err := row.Scan(&count)
	return count, err
}

type FindWorkspacesByRepoParams struct {
	RepoURL    string `json:"repo_url"`
	RepoBranch string `json:"repo_branch"`
}

func (q *Queries) FindWorkspacesByRepo(ctx context.Context, arg FindWorkspacesByRepoParams) ([]Workspace, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces
		WHERE LOWER(REGEXP_REPLACE(repo_url, '\.git$', '')) = $1
		AND repo_branch = $2 AND vcs_trigger_enabled = TRUE AND locked = FALSE
		ORDER BY created_at`,
		arg.RepoURL, arg.RepoBranch,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workspaces []Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		workspaces = append(workspaces, w)
	}
	if workspaces == nil {
		workspaces = []Workspace{}
	}
	return workspaces, rows.Err()
}

// A workspace is only a name for "this config, at this path". Two workspaces
// pointing at the same repo and the same working_dir drive the same backend —
// terragrunt declares remote_state in the tree itself — so they are two doors
// onto one piece of infrastructure. These expressions reduce both halves of
// that identity to a canonical form, so the same target spelled differently
// still compares equal.
//
// repoURLIdentitySQL runs in two halves, inside-out.
//
// First the origin is reduced to "host/path": lowercase, drop a default-looking
// port from a URL that carries a scheme, drop the scheme, drop any userinfo (a
// URL carrying a token is the same repo), then turn scp-style "host:path" into
// "host/path". The port is stripped while the scheme is still attached, and
// only then, so the scp-style rule that follows cannot mistake a numeric path
// segment for one: "git@github.com:2600/repo" is a repo owned by "2600", not a
// port.
//
// Then the path is folded the way the remote folds it — the same three rewrites
// workingDirIdentitySQL applies, for the same reason. A git path is resolved as
// a path at the far end, so "github.com/acme//infra",
// "github.com/acme/./infra" and "github.com/acme/infra/." all serve the tree
// that "github.com/acme/infra" serves, and every one of them clones. Folding
// the path also has to happen before the ".git" suffix comes off, or
// ".../infra.git/." keeps its suffix and reads as another repo. The scheme's
// own "//" is gone by then, so collapsing repeated slashes cannot touch it.
//
// All of these land on "github.com/acme/infra":
//
//	https://github.com/acme/infra.git     ssh://git@github.com/acme/infra
//	https://TOKEN@github.com/acme/infra/   git@github.com:acme/infra.git
//	https://github.com:443/acme/infra.git  ssh://git@github.com:22/acme/infra
//	https://github.com/acme//infra         https://github.com/acme/./infra
//	https://github.com/acme/infra.git/.    https://github.com/acme/infra//
//
// It normalises spelling, not remote identity: a mirror under a different host
// or path is a different string and stays one. The check narrows the gap
// structurally, it does not close it by proof.
const (
	repoURLIdentitySQL = `RTRIM(REGEXP_REPLACE(
		RTRIM(
			REGEXP_REPLACE(
				REGEXP_REPLACE(
					REGEXP_REPLACE(
						REGEXP_REPLACE(
							REGEXP_REPLACE(
								REGEXP_REPLACE(
									REGEXP_REPLACE(LOWER(%s),
									'^([a-z][a-z0-9+.-]*://[^/]*):[0-9]+(/|$)', '\1\2'),
								'^[a-z][a-z0-9+.-]*://', ''),
							'^[^/@]*@', ''),
						'^([^/:]+):', '\1/'),
					'/+', '/', 'g'),
				'(^|/)(\./)+', '\1', 'g'),
			'(^|/)\.$', '\1'),
		'/'),
	'\.git$', ''), '/')`

	// A leaf is one directory under every spelling of it. Applied outside-in:
	// collapse repeated slashes, drop a leading slash, drop every "." segment,
	// drop a trailing "/.", then trim the trailing slash and read an empty
	// result as the repo root.
	//
	//	"", ".", "./", "/"                          -> "."
	//	"envs/prod", "./envs/prod", "/envs/prod"     -> "envs/prod"
	//	"envs//prod", "envs/./prod", "envs/prod/."   -> "envs/prod"
	//
	// Portal canonicalises the column on write (service.CanonicalWorkingDir), so
	// in practice both sides of the comparison already agree; this is what makes
	// that true for a row written before the column was canonical, and it is
	// what a hand-run query against the table gets as well.
	workingDirIdentitySQL = `COALESCE(NULLIF(BTRIM(
		REGEXP_REPLACE(
			REGEXP_REPLACE(
				REGEXP_REPLACE(
					REGEXP_REPLACE(%s, '/+', '/', 'g'),
				'^/', ''),
			'(^|/)(\./)+', '\1', 'g'),
		'(^|/)\.$', '\1'),
	'/'), ''), '.')`
)

type GatedTwinParams struct {
	OrgID      string `json:"org_id"`
	RepoURL    string `json:"repo_url"`
	WorkingDir string `json:"working_dir"`
	ExcludeID  string `json:"exclude_id"`
}

// HasGatedWorkspaceForConfig reports whether some OTHER workspace in this org
// already gates this exact repo + working_dir behind an approval.
//
// Both sides of each comparison run through the same normalising expression, so
// the check cannot be dodged by spelling the repo URL or the working directory
// differently — a respelled path resolves to the same `cd` in the executor, so
// it has to resolve to the same row here.
//
// An empty repo_url means an upload workspace, which has no comparable config
// identity; the query returns false rather than matching every other upload
// workspace in the org.
func (q *Queries) HasGatedWorkspaceForConfig(ctx context.Context, arg GatedTwinParams) (bool, error) {
	if arg.RepoURL == "" {
		return false, nil
	}
	repoCol := fmt.Sprintf(repoURLIdentitySQL, "repo_url")
	repoArg := fmt.Sprintf(repoURLIdentitySQL, "$2::TEXT")
	dirCol := fmt.Sprintf(workingDirIdentitySQL, "working_dir")
	dirArg := fmt.Sprintf(workingDirIdentitySQL, "$3::TEXT")

	row := q.db.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM workspaces
			WHERE org_id = $1
			  AND id <> $4
			  AND requires_approval = TRUE
			  AND repo_url <> ''
			  AND `+repoCol+` = `+repoArg+`
			  AND `+dirCol+` = `+dirArg+`
		)`,
		arg.OrgID, arg.RepoURL, arg.WorkingDir, arg.ExcludeID,
	)
	var exists bool
	err := row.Scan(&exists)
	return exists, err
}

type ConfigTargetsMatchParams struct {
	RepoURLA    string `json:"repo_url_a"`
	WorkingDirA string `json:"working_dir_a"`
	RepoURLB    string `json:"repo_url_b"`
	WorkingDirB string `json:"working_dir_b"`
}

// ConfigTargetsMatch reports whether two (repo_url, working_dir) pairs name the
// same config, under exactly the identity rules HasGatedWorkspaceForConfig
// compares rows by.
//
// Callers that have to know whether an update MOVES a workspace need the same
// answer the gated-twin check would give, and there is one definition of that:
// this runs the same two expressions rather than restating them in Go, where
// the two spellings of "same config" could drift apart. A workspace resubmitted
// under an equivalent spelling of its own target has not moved, and must not be
// charged for a move.
//
// An empty repo_url on either side is an upload workspace, which has no
// comparable config identity — the same reading HasGatedWorkspaceForConfig
// takes — so it matches nothing, itself included.
func (q *Queries) ConfigTargetsMatch(ctx context.Context, arg ConfigTargetsMatchParams) (bool, error) {
	if arg.RepoURLA == "" || arg.RepoURLB == "" {
		return false, nil
	}
	repoA := fmt.Sprintf(repoURLIdentitySQL, "$1::TEXT")
	repoB := fmt.Sprintf(repoURLIdentitySQL, "$3::TEXT")
	dirA := fmt.Sprintf(workingDirIdentitySQL, "$2::TEXT")
	dirB := fmt.Sprintf(workingDirIdentitySQL, "$4::TEXT")

	row := q.db.QueryRow(ctx,
		`SELECT `+repoA+` = `+repoB+` AND `+dirA+` = `+dirB,
		arg.RepoURLA, arg.WorkingDirA, arg.RepoURLB, arg.WorkingDirB,
	)
	var same bool
	err := row.Scan(&same)
	return same, err
}
