// Hand-written pgx queries (sqlc-style); not generated, edit directly.
//
// A variable is addressed by (id, workspace_id, org_id), never by (id, org_id).
// The route that reaches these carries the workspace in its path and is
// authorized against that workspace, so the workspace has to be part of the
// lookup too — otherwise a caller authorized on one workspace could name any
// variable id in the org and the query would happily return it.

package repository

import "context"

const varColumns = `id, workspace_id, org_id, key, value, sensitive, category, description, created_at, updated_at`

func scanVariable(row interface{ Scan(...interface{}) error }) (WorkspaceVariable, error) {
	var v WorkspaceVariable
	err := row.Scan(&v.ID, &v.WorkspaceID, &v.OrgID, &v.Key, &v.Value, &v.Sensitive, &v.Category, &v.Description, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

type GetWorkspaceVariableParams struct {
	ID          string
	WorkspaceID string
	OrgID       string
}

// GetWorkspaceVariable returns one variable of one workspace. A variable that
// exists in the org but on a different workspace is pgx.ErrNoRows, exactly like
// one that does not exist — the caller reports both as 404, so the response
// never tells an attacker which of the two it was.
func (q *Queries) GetWorkspaceVariable(ctx context.Context, arg GetWorkspaceVariableParams) (WorkspaceVariable, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+varColumns+` FROM workspace_variables WHERE id = $1 AND workspace_id = $2 AND org_id = $3`,
		arg.ID, arg.WorkspaceID, arg.OrgID,
	)
	return scanVariable(row)
}

type ListWorkspaceVariablesParams struct {
	WorkspaceID string
	OrgID       string
}

func (q *Queries) ListWorkspaceVariables(ctx context.Context, arg ListWorkspaceVariablesParams) ([]WorkspaceVariable, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+varColumns+` FROM workspace_variables WHERE workspace_id = $1 AND org_id = $2 ORDER BY key`,
		arg.WorkspaceID, arg.OrgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vars []WorkspaceVariable
	for rows.Next() {
		v, err := scanVariable(rows)
		if err != nil {
			return nil, err
		}
		vars = append(vars, v)
	}
	if vars == nil {
		vars = []WorkspaceVariable{}
	}
	return vars, rows.Err()
}

type CreateWorkspaceVariableParams struct {
	ID          string
	WorkspaceID string
	OrgID       string
	Key         string
	Value       string
	Sensitive   bool
	Category    string
	Description string
}

func (q *Queries) CreateWorkspaceVariable(ctx context.Context, arg CreateWorkspaceVariableParams) (WorkspaceVariable, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO workspace_variables (id, workspace_id, org_id, key, value, sensitive, category, description)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+varColumns,
		arg.ID, arg.WorkspaceID, arg.OrgID, arg.Key, arg.Value, arg.Sensitive, arg.Category, arg.Description,
	)
	return scanVariable(row)
}

type UpdateWorkspaceVariableParams struct {
	ID          string
	WorkspaceID string
	OrgID       string
	Value       string
	Sensitive   bool
	Description string
	Category    string
}

func (q *Queries) UpdateWorkspaceVariable(ctx context.Context, arg UpdateWorkspaceVariableParams) (WorkspaceVariable, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE workspace_variables SET value = $4, sensitive = $5, description = $6, category = COALESCE(NULLIF($7, ''), category), updated_at = NOW()
		WHERE id = $1 AND workspace_id = $2 AND org_id = $3
		RETURNING `+varColumns,
		arg.ID, arg.WorkspaceID, arg.OrgID, arg.Value, arg.Sensitive, arg.Description, arg.Category,
	)
	return scanVariable(row)
}

type DeleteWorkspaceVariableParams struct {
	ID          string
	WorkspaceID string
	OrgID       string
}

// DeleteWorkspaceVariable removes one variable of one workspace and returns the
// row it deleted, so a delete that matched nothing is pgx.ErrNoRows rather than
// a silent success. Without that, deleting another workspace's variable id and
// deleting nothing at all would look identical to the caller.
func (q *Queries) DeleteWorkspaceVariable(ctx context.Context, arg DeleteWorkspaceVariableParams) (WorkspaceVariable, error) {
	row := q.db.QueryRow(ctx,
		`DELETE FROM workspace_variables WHERE id = $1 AND workspace_id = $2 AND org_id = $3 RETURNING `+varColumns,
		arg.ID, arg.WorkspaceID, arg.OrgID,
	)
	return scanVariable(row)
}
