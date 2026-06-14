// Hand-written pgx queries (sqlc-style); not generated, edit directly.

package repository

import "context"

type CreateOrganizationParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (q *Queries) CreateOrganization(ctx context.Context, arg CreateOrganizationParams) (Organization, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3) RETURNING id, name, slug, created_at, updated_at`,
		arg.ID, arg.Name, arg.Slug,
	)
	var o Organization
	err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

func (q *Queries) GetOrganization(ctx context.Context, id string) (Organization, error) {
	row := q.db.QueryRow(ctx,
		`SELECT id, name, slug, created_at, updated_at FROM organizations WHERE id = $1`,
		id,
	)
	var o Organization
	err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

func (q *Queries) GetOrganizationBySlug(ctx context.Context, slug string) (Organization, error) {
	row := q.db.QueryRow(ctx,
		`SELECT id, name, slug, created_at, updated_at FROM organizations WHERE slug = $1`,
		slug,
	)
	var o Organization
	err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

func (q *Queries) GetDefaultOrganization(ctx context.Context) (Organization, error) {
	row := q.db.QueryRow(ctx,
		`SELECT id, name, slug, created_at, updated_at FROM organizations ORDER BY created_at ASC LIMIT 1`,
	)
	var o Organization
	err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}
