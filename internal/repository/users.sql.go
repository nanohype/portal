// Hand-written pgx queries (sqlc-style); not generated, edit directly.

package repository

import "context"

const userColumns = `id, org_id, email, name, avatar_url, github_id, role, last_login_at, created_at, updated_at`

func scanUser(row interface{ Scan(...interface{}) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.OrgID, &u.Email, &u.Name, &u.AvatarURL, &u.GithubID, &u.Role, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

func (q *Queries) GetUser(ctx context.Context, id string) (User, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = $1`, id,
	)
	return scanUser(row)
}

// GetUserRole returns just the role column for one user in one org. Every
// authenticated request runs it, so it stays a single-column read on the
// primary key rather than loading the whole row. A user that does not exist in
// that org returns pgx.ErrNoRows.
func (q *Queries) GetUserRole(ctx context.Context, id, orgID string) (string, error) {
	var role string
	err := q.db.QueryRow(ctx,
		`SELECT role::text FROM users WHERE id = $1 AND org_id = $2`, id, orgID,
	).Scan(&role)
	return role, err
}

func (q *Queries) GetUserByEmail(ctx context.Context, email string, orgID string) (User, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = $1 AND org_id = $2`, email, orgID,
	)
	return scanUser(row)
}

func (q *Queries) GetUserByGitHubID(ctx context.Context, githubID int64) (User, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE github_id = $1`, githubID,
	)
	return scanUser(row)
}

type UpsertUserByGitHubIDParams struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	GithubID  *int64 `json:"github_id"`
	Role      string `json:"role"`
}

func (q *Queries) UpsertUserByGitHubID(ctx context.Context, arg UpsertUserByGitHubIDParams) (User, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO users (id, org_id, email, name, avatar_url, github_id, role)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (github_id) DO UPDATE
		SET name = EXCLUDED.name, avatar_url = EXCLUDED.avatar_url, last_login_at = NOW(), updated_at = NOW()
		RETURNING `+userColumns,
		arg.ID, arg.OrgID, arg.Email, arg.Name, arg.AvatarURL, arg.GithubID, arg.Role,
	)
	return scanUser(row)
}

type UpsertUserByEmailParams struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Role      string `json:"role"`
}

func (q *Queries) UpsertUserByEmail(ctx context.Context, arg UpsertUserByEmailParams) (User, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO users (id, org_id, email, name, avatar_url, role)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (org_id, email) DO UPDATE
		SET name = EXCLUDED.name, last_login_at = NOW(), updated_at = NOW()
		RETURNING `+userColumns,
		arg.ID, arg.OrgID, arg.Email, arg.Name, arg.AvatarURL, arg.Role,
	)
	return scanUser(row)
}

type CreateUserParams struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	GithubID  *int64 `json:"github_id"`
	Role      string `json:"role"`
}

func (q *Queries) CreateUser(ctx context.Context, arg CreateUserParams) (User, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO users (id, org_id, email, name, avatar_url, github_id, role)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+userColumns,
		arg.ID, arg.OrgID, arg.Email, arg.Name, arg.AvatarURL, arg.GithubID, arg.Role,
	)
	return scanUser(row)
}

func (q *Queries) CountUsersByOrg(ctx context.Context, orgID string) (int64, error) {
	row := q.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM users WHERE org_id = $1`, orgID,
	)
	var count int64
	err := row.Scan(&count)
	return count, err
}

type UpdateUserRoleParams struct {
	ID    string `json:"id"`
	Role  string `json:"role"`
	OrgID string `json:"org_id"`
}

func (q *Queries) UpdateUserRole(ctx context.Context, arg UpdateUserRoleParams) (User, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE users SET role = $2, updated_at = NOW() WHERE id = $1 AND org_id = $3 RETURNING `+userColumns,
		arg.ID, arg.Role, arg.OrgID,
	)
	return scanUser(row)
}

func (q *Queries) CountOwnersByOrg(ctx context.Context, orgID string) (int64, error) {
	row := q.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM users WHERE org_id = $1 AND role = 'owner'`, orgID,
	)
	var count int64
	err := row.Scan(&count)
	return count, err
}

func (q *Queries) ListUsersByOrg(ctx context.Context, orgID string) ([]User, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+userColumns+` FROM users WHERE org_id = $1 ORDER BY created_at DESC`, orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
