package repository

import "context"

const pipelineVarColumns = `id, pipeline_id, org_id, key, value, sensitive, category, description, created_at, updated_at`

func scanPipelineVariable(row interface{ Scan(...interface{}) error }) (PipelineVariable, error) {
	var v PipelineVariable
	err := row.Scan(&v.ID, &v.PipelineID, &v.OrgID, &v.Key, &v.Value, &v.Sensitive, &v.Category, &v.Description, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

type ListPipelineVariablesParams struct {
	PipelineID string
	OrgID      string
}

func (q *Queries) ListPipelineVariables(ctx context.Context, arg ListPipelineVariablesParams) ([]PipelineVariable, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+pipelineVarColumns+` FROM pipeline_variables WHERE pipeline_id = $1 AND org_id = $2 ORDER BY key`,
		arg.PipelineID, arg.OrgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vars []PipelineVariable
	for rows.Next() {
		v, err := scanPipelineVariable(rows)
		if err != nil {
			return nil, err
		}
		vars = append(vars, v)
	}
	if vars == nil {
		vars = []PipelineVariable{}
	}
	return vars, rows.Err()
}

type GetPipelineVariableParams struct {
	ID         string
	PipelineID string
	OrgID      string
}

func (q *Queries) GetPipelineVariable(ctx context.Context, arg GetPipelineVariableParams) (PipelineVariable, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+pipelineVarColumns+` FROM pipeline_variables WHERE id = $1 AND pipeline_id = $2 AND org_id = $3`,
		arg.ID, arg.PipelineID, arg.OrgID,
	)
	return scanPipelineVariable(row)
}

type CreatePipelineVariableParams struct {
	ID          string
	PipelineID  string
	OrgID       string
	Key         string
	Value       string
	Sensitive   bool
	Category    string
	Description string
}

func (q *Queries) CreatePipelineVariable(ctx context.Context, arg CreatePipelineVariableParams) (PipelineVariable, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO pipeline_variables (id, pipeline_id, org_id, key, value, sensitive, category, description)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+pipelineVarColumns,
		arg.ID, arg.PipelineID, arg.OrgID, arg.Key, arg.Value, arg.Sensitive, arg.Category, arg.Description,
	)
	return scanPipelineVariable(row)
}

type UpdatePipelineVariableParams struct {
	ID          string
	PipelineID  string
	OrgID       string
	Value       string
	Sensitive   bool
	Description string
	Category    string
}

func (q *Queries) UpdatePipelineVariable(ctx context.Context, arg UpdatePipelineVariableParams) (PipelineVariable, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE pipeline_variables SET value = $4, sensitive = $5, description = $6, category = COALESCE(NULLIF($7, ''), category), updated_at = NOW()
		WHERE id = $1 AND pipeline_id = $2 AND org_id = $3
		RETURNING `+pipelineVarColumns,
		arg.ID, arg.PipelineID, arg.OrgID, arg.Value, arg.Sensitive, arg.Description, arg.Category,
	)
	return scanPipelineVariable(row)
}

type DeletePipelineVariableParams struct {
	ID         string
	PipelineID string
	OrgID      string
}

// DeletePipelineVariable removes one variable of one pipeline and returns the row
// it deleted, so a delete that matched nothing is pgx.ErrNoRows rather than a
// silent success. Without that, deleting another pipeline's variable id and
// deleting nothing at all would look identical to the caller.
func (q *Queries) DeletePipelineVariable(ctx context.Context, arg DeletePipelineVariableParams) (PipelineVariable, error) {
	row := q.db.QueryRow(ctx,
		`DELETE FROM pipeline_variables WHERE id = $1 AND pipeline_id = $2 AND org_id = $3 RETURNING `+pipelineVarColumns,
		arg.ID, arg.PipelineID, arg.OrgID,
	)
	return scanPipelineVariable(row)
}
