// Hand-written pgx queries (sqlc-style); not generated, edit directly.

package repository

import (
	"context"
	"encoding/json"
)

const templateColumns = `id, org_id, name, description, persona, default_values, allowed_overrides, max_budget_usd, allowed_model_families, required_compliance, created_by, created_at, updated_at`

func scanTemplate(row interface{ Scan(...interface{}) error }) (Template, error) {
	var t Template
	err := row.Scan(&t.ID, &t.OrgID, &t.Name, &t.Description, &t.Persona, &t.DefaultValues, &t.AllowedOverrides, &t.MaxBudgetUSD, &t.AllowedModelFamilies, &t.RequiredCompliance, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

type GetTemplateParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) GetTemplate(ctx context.Context, arg GetTemplateParams) (Template, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+templateColumns+` FROM templates WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return scanTemplate(row)
}

type ListTemplatesParams struct {
	OrgID string `json:"org_id"`
	// TeamIDs scopes the result to templates granted to one of these teams.
	// Nil = no team filter (admin); empty non-nil = "user has no teams" → zero rows.
	TeamIDs []string `json:"team_ids"`
}

func (q *Queries) ListTemplates(ctx context.Context, arg ListTemplatesParams) ([]Template, error) {
	if arg.TeamIDs != nil {
		if len(arg.TeamIDs) == 0 {
			return []Template{}, nil
		}
		rows, err := q.db.Query(ctx,
			`SELECT DISTINCT `+templateColumnsPrefixed("t")+` FROM templates t
			JOIN template_team_access tta ON tta.template_id = t.id
			WHERE t.org_id = $1 AND tta.team_id = ANY($2::TEXT[])
			ORDER BY t.name`,
			arg.OrgID, arg.TeamIDs,
		)
		return scanTemplates(rows, err)
	}
	rows, err := q.db.Query(ctx,
		`SELECT `+templateColumns+` FROM templates WHERE org_id = $1 ORDER BY name`,
		arg.OrgID,
	)
	return scanTemplates(rows, err)
}

func templateColumnsPrefixed(alias string) string {
	return alias + ".id, " + alias + ".org_id, " + alias + ".name, " + alias + ".description, " + alias + ".persona, " + alias + ".default_values, " + alias + ".allowed_overrides, " + alias + ".max_budget_usd, " + alias + ".allowed_model_families, " + alias + ".required_compliance, " + alias + ".created_by, " + alias + ".created_at, " + alias + ".updated_at"
}

func scanTemplates(rows interface {
	Next() bool
	Scan(...interface{}) error
	Err() error
	Close()
}, err error) ([]Template, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var templates []Template
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	if templates == nil {
		templates = []Template{}
	}
	return templates, rows.Err()
}

type CreateTemplateParams struct {
	ID                   string          `json:"id"`
	OrgID                string          `json:"org_id"`
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Persona              string          `json:"persona"`
	DefaultValues        json.RawMessage `json:"default_values"`
	AllowedOverrides     json.RawMessage `json:"allowed_overrides"`
	MaxBudgetUSD         int32           `json:"max_budget_usd"`
	AllowedModelFamilies json.RawMessage `json:"allowed_model_families"`
	RequiredCompliance   json.RawMessage `json:"required_compliance"`
	CreatedBy            string          `json:"created_by"`
}

func (q *Queries) CreateTemplate(ctx context.Context, arg CreateTemplateParams) (Template, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO templates (id, org_id, name, description, persona, default_values, allowed_overrides, max_budget_usd, allowed_model_families, required_compliance, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+templateColumns,
		arg.ID, arg.OrgID, arg.Name, arg.Description, arg.Persona, arg.DefaultValues, arg.AllowedOverrides, arg.MaxBudgetUSD, arg.AllowedModelFamilies, arg.RequiredCompliance, arg.CreatedBy,
	)
	return scanTemplate(row)
}

type UpdateTemplateParams struct {
	ID                   string          `json:"id"`
	OrgID                string          `json:"org_id"`
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Persona              string          `json:"persona"`
	DefaultValues        json.RawMessage `json:"default_values"`
	AllowedOverrides     json.RawMessage `json:"allowed_overrides"`
	MaxBudgetUSD         *int32          `json:"max_budget_usd"`
	AllowedModelFamilies json.RawMessage `json:"allowed_model_families"`
	RequiredCompliance   json.RawMessage `json:"required_compliance"`
}

// UpdateTemplate uses the established partial-update pattern: empty strings
// leave fields untouched; explicit empty JSONB (`[]`/`{}`) replaces. Numeric
// fields are *int32 so caller can distinguish "leave alone" from "zero".
func (q *Queries) UpdateTemplate(ctx context.Context, arg UpdateTemplateParams) (Template, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE templates
		SET name = COALESCE(NULLIF($3, ''), name),
		    description = COALESCE(NULLIF($4, ''), description),
		    persona = COALESCE(NULLIF($5, ''), persona),
		    default_values = COALESCE($6, default_values),
		    allowed_overrides = COALESCE($7, allowed_overrides),
		    max_budget_usd = COALESCE($8, max_budget_usd),
		    allowed_model_families = COALESCE($9, allowed_model_families),
		    required_compliance = COALESCE($10, required_compliance),
		    updated_at = NOW()
		WHERE id = $1 AND org_id = $2
		RETURNING `+templateColumns,
		arg.ID, arg.OrgID, arg.Name, arg.Description, arg.Persona, arg.DefaultValues, arg.AllowedOverrides, arg.MaxBudgetUSD, arg.AllowedModelFamilies, arg.RequiredCompliance,
	)
	return scanTemplate(row)
}

type DeleteTemplateParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) DeleteTemplate(ctx context.Context, arg DeleteTemplateParams) error {
	_, err := q.db.Exec(ctx,
		`DELETE FROM templates WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return err
}
