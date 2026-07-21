// Hand-written pgx queries (sqlc-style); not generated, edit directly.

package repository

import "context"

const stateVersionColumns = `id, workspace_id, org_id, run_id, serial, state_url, resource_count, resource_summary, created_at`

func scanStateVersion(row interface{ Scan(...interface{}) error }) (StateVersion, error) {
	var sv StateVersion
	err := row.Scan(&sv.ID, &sv.WorkspaceID, &sv.OrgID, &sv.RunID, &sv.Serial, &sv.StateURL, &sv.ResourceCount, &sv.ResourceSummary, &sv.CreatedAt)
	return sv, err
}

type CreateStateVersionParams struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	OrgID           string `json:"org_id"`
	RunID           string `json:"run_id"`
	Serial          int32  `json:"serial"`
	StateURL        string `json:"state_url"`
	ResourceCount   int32  `json:"resource_count"`
	ResourceSummary string `json:"resource_summary"`
}

func (q *Queries) CreateStateVersion(ctx context.Context, arg CreateStateVersionParams) (StateVersion, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO state_versions (id, workspace_id, org_id, run_id, serial, state_url, resource_count, resource_summary)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+stateVersionColumns,
		arg.ID, arg.WorkspaceID, arg.OrgID, arg.RunID, arg.Serial, arg.StateURL, arg.ResourceCount, arg.ResourceSummary,
	)
	return scanStateVersion(row)
}

type GetStateVersionParams struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	OrgID       string `json:"org_id"`
}

// GetStateVersion returns one state version of one workspace. The workspace is
// part of the key, not just the org: the route that reaches this is authorized
// against the workspace in its path, and a tfstate blob carries every provider
// credential the run used, so a state-version id belonging to a different
// workspace must miss rather than resolve.
func (q *Queries) GetStateVersion(ctx context.Context, arg GetStateVersionParams) (StateVersion, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+stateVersionColumns+` FROM state_versions WHERE id = $1 AND workspace_id = $2 AND org_id = $3`,
		arg.ID, arg.WorkspaceID, arg.OrgID,
	)
	return scanStateVersion(row)
}

type ListStateVersionsParams struct {
	WorkspaceID string `json:"workspace_id"`
	OrgID       string `json:"org_id"`
	Limit       int32  `json:"limit"`
	Offset      int32  `json:"offset"`
}

func (q *Queries) ListStateVersionsByWorkspace(ctx context.Context, arg ListStateVersionsParams) ([]StateVersion, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+stateVersionColumns+` FROM state_versions WHERE workspace_id = $1 AND org_id = $2 ORDER BY serial DESC LIMIT $3 OFFSET $4`,
		arg.WorkspaceID, arg.OrgID, arg.Limit, arg.Offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []StateVersion
	for rows.Next() {
		sv, err := scanStateVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, sv)
	}
	if versions == nil {
		versions = []StateVersion{}
	}
	return versions, rows.Err()
}

type GetStateVersionBySerialParams struct {
	WorkspaceID string `json:"workspace_id"`
	OrgID       string `json:"org_id"`
	Serial      int32  `json:"serial"`
}

func (q *Queries) GetStateVersionBySerial(ctx context.Context, arg GetStateVersionBySerialParams) (StateVersion, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+stateVersionColumns+` FROM state_versions WHERE workspace_id = $1 AND org_id = $2 AND serial = $3`,
		arg.WorkspaceID, arg.OrgID, arg.Serial,
	)
	return scanStateVersion(row)
}

type GetLatestStateVersionParams struct {
	WorkspaceID string `json:"workspace_id"`
	OrgID       string `json:"org_id"`
}

func (q *Queries) GetLatestStateVersion(ctx context.Context, arg GetLatestStateVersionParams) (StateVersion, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+stateVersionColumns+` FROM state_versions WHERE workspace_id = $1 AND org_id = $2 ORDER BY serial DESC LIMIT 1`,
		arg.WorkspaceID, arg.OrgID,
	)
	return scanStateVersion(row)
}

// DeleteStateVersion removes a single state-version row, scoped by
// workspace and org. Returns pgx.ErrNoRows when there's no matching row
// so the handler can surface a clean 404. The caller is responsible for
// removing the corresponding S3 objects (see storage.DeleteStateObjects).
func (q *Queries) DeleteStateVersion(ctx context.Context, arg GetStateVersionBySerialParams) (StateVersion, error) {
	row := q.db.QueryRow(ctx,
		`DELETE FROM state_versions WHERE workspace_id = $1 AND org_id = $2 AND serial = $3 RETURNING `+stateVersionColumns,
		arg.WorkspaceID, arg.OrgID, arg.Serial,
	)
	return scanStateVersion(row)
}
