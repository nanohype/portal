package repository

import (
	"context"
	"time"
)

type Team struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TeamMember struct {
	ID            string    `json:"id"`
	TeamID        string    `json:"team_id"`
	UserID        string    `json:"user_id"`
	Role          string    `json:"role"`
	CloudIdentity string    `json:"cloud_identity"`
	CreatedAt     time.Time `json:"created_at"`
	Email         string    `json:"email"`
	UserName      string    `json:"user_name"`
	AvatarURL     string    `json:"avatar_url"`
}

type WorkspaceTeamAccess struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	TeamID      string    `json:"team_id"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	TeamName    string    `json:"team_name"`
	TeamSlug    string    `json:"team_slug"`
}

type Approval struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	OrgID     string    `json:"org_id"`
	UserID    string    `json:"user_id"`
	Status    string    `json:"status"`
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
	UserName  string    `json:"user_name,omitempty"`
	AvatarURL string    `json:"avatar_url,omitempty"`
}

type CreateTeamParams struct {
	ID    string
	OrgID string
	Name  string
	Slug  string
}

func (q *Queries) CreateTeam(ctx context.Context, arg CreateTeamParams) (Team, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO teams (id, org_id, name, slug) VALUES ($1, $2, $3, $4)
		RETURNING id, org_id, name, slug, created_at, updated_at`,
		arg.ID, arg.OrgID, arg.Name, arg.Slug,
	)
	var t Team
	err := row.Scan(&t.ID, &t.OrgID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (q *Queries) GetTeam(ctx context.Context, id, orgID string) (Team, error) {
	row := q.db.QueryRow(ctx,
		`SELECT id, org_id, name, slug, created_at, updated_at FROM teams WHERE id = $1 AND org_id = $2`,
		id, orgID,
	)
	var t Team
	err := row.Scan(&t.ID, &t.OrgID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (q *Queries) ListTeams(ctx context.Context, orgID string) ([]Team, error) {
	rows, err := q.db.Query(ctx,
		`SELECT id, org_id, name, slug, created_at, updated_at FROM teams WHERE org_id = $1 ORDER BY name`, orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var teams []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.OrgID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		teams = append(teams, t)
	}
	if teams == nil {
		teams = []Team{}
	}
	return teams, rows.Err()
}

func (q *Queries) DeleteTeam(ctx context.Context, id, orgID string) error {
	_, err := q.db.Exec(ctx, `DELETE FROM teams WHERE id = $1 AND org_id = $2`, id, orgID)
	return err
}

type AddTeamMemberParams struct {
	ID            string
	TeamID        string
	UserID        string
	Role          string
	CloudIdentity string
}

func (q *Queries) AddTeamMember(ctx context.Context, arg AddTeamMemberParams) (TeamMember, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO team_members (id, team_id, user_id, role, cloud_identity) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (team_id, user_id) DO UPDATE SET role = EXCLUDED.role, cloud_identity = EXCLUDED.cloud_identity
		RETURNING id, team_id, user_id, role, cloud_identity, created_at`,
		arg.ID, arg.TeamID, arg.UserID, arg.Role, arg.CloudIdentity,
	)
	var m TeamMember
	err := row.Scan(&m.ID, &m.TeamID, &m.UserID, &m.Role, &m.CloudIdentity, &m.CreatedAt)
	return m, err
}

type UpdateTeamMemberParams struct {
	TeamID        string
	UserID        string
	Role          string
	CloudIdentity string
}

func (q *Queries) UpdateTeamMember(ctx context.Context, arg UpdateTeamMemberParams) (TeamMember, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE team_members SET role = $3, cloud_identity = $4
		WHERE team_id = $1 AND user_id = $2
		RETURNING id, team_id, user_id, role, cloud_identity, created_at`,
		arg.TeamID, arg.UserID, arg.Role, arg.CloudIdentity,
	)
	var m TeamMember
	err := row.Scan(&m.ID, &m.TeamID, &m.UserID, &m.Role, &m.CloudIdentity, &m.CreatedAt)
	return m, err
}

func (q *Queries) ListTeamMembers(ctx context.Context, teamID string) ([]TeamMember, error) {
	rows, err := q.db.Query(ctx,
		`SELECT tm.id, tm.team_id, tm.user_id, tm.role, tm.cloud_identity, tm.created_at, u.email, u.name, u.avatar_url
		FROM team_members tm JOIN users u ON u.id = tm.user_id
		WHERE tm.team_id = $1 ORDER BY u.name`, teamID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []TeamMember
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.ID, &m.TeamID, &m.UserID, &m.Role, &m.CloudIdentity, &m.CreatedAt, &m.Email, &m.UserName, &m.AvatarURL); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	if members == nil {
		members = []TeamMember{}
	}
	return members, rows.Err()
}

func (q *Queries) RemoveTeamMember(ctx context.Context, teamID, userID string) error {
	_, err := q.db.Exec(ctx, `DELETE FROM team_members WHERE team_id = $1 AND user_id = $2`, teamID, userID)
	return err
}

func (q *Queries) RemoveWorkspaceTeamAccess(ctx context.Context, workspaceID, teamID string) error {
	_, err := q.db.Exec(ctx, `DELETE FROM workspace_team_access WHERE workspace_id = $1 AND team_id = $2`, workspaceID, teamID)
	return err
}

type SetWorkspaceTeamAccessParams struct {
	ID          string
	WorkspaceID string
	TeamID      string
	Role        string
}

func (q *Queries) SetWorkspaceTeamAccess(ctx context.Context, arg SetWorkspaceTeamAccessParams) (WorkspaceTeamAccess, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO workspace_team_access (id, workspace_id, team_id, role) VALUES ($1, $2, $3, $4)
		ON CONFLICT (workspace_id, team_id) DO UPDATE SET role = EXCLUDED.role
		RETURNING id, workspace_id, team_id, role, created_at`,
		arg.ID, arg.WorkspaceID, arg.TeamID, arg.Role,
	)
	var a WorkspaceTeamAccess
	err := row.Scan(&a.ID, &a.WorkspaceID, &a.TeamID, &a.Role, &a.CreatedAt)
	return a, err
}

func (q *Queries) ListWorkspaceTeamAccess(ctx context.Context, workspaceID string) ([]WorkspaceTeamAccess, error) {
	rows, err := q.db.Query(ctx,
		`SELECT wta.id, wta.workspace_id, wta.team_id, wta.role, wta.created_at, t.name, t.slug
		FROM workspace_team_access wta JOIN teams t ON t.id = wta.team_id
		WHERE wta.workspace_id = $1 ORDER BY t.name`, workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var access []WorkspaceTeamAccess
	for rows.Next() {
		var a WorkspaceTeamAccess
		if err := rows.Scan(&a.ID, &a.WorkspaceID, &a.TeamID, &a.Role, &a.CreatedAt, &a.TeamName, &a.TeamSlug); err != nil {
			return nil, err
		}
		access = append(access, a)
	}
	if access == nil {
		access = []WorkspaceTeamAccess{}
	}
	return access, rows.Err()
}

type CreateApprovalParams struct {
	ID      string
	RunID   string
	OrgID   string
	UserID  string
	Status  string
	Comment string
}

func (q *Queries) CreateApproval(ctx context.Context, arg CreateApprovalParams) (Approval, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO approvals (id, run_id, org_id, user_id, status, comment)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, run_id, org_id, user_id, status, comment, created_at`,
		arg.ID, arg.RunID, arg.OrgID, arg.UserID, arg.Status, arg.Comment,
	)
	var a Approval
	err := row.Scan(&a.ID, &a.RunID, &a.OrgID, &a.UserID, &a.Status, &a.Comment, &a.CreatedAt)
	return a, err
}

func (q *Queries) ListApprovalsByRun(ctx context.Context, runID string) ([]Approval, error) {
	rows, err := q.db.Query(ctx,
		`SELECT a.id, a.run_id, a.org_id, a.user_id, a.status, a.comment, a.created_at, u.name, u.avatar_url
		FROM approvals a JOIN users u ON u.id = a.user_id
		WHERE a.run_id = $1 ORDER BY a.created_at DESC`, runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var approvals []Approval
	for rows.Next() {
		var a Approval
		if err := rows.Scan(&a.ID, &a.RunID, &a.OrgID, &a.UserID, &a.Status, &a.Comment, &a.CreatedAt, &a.UserName, &a.AvatarURL); err != nil {
			return nil, err
		}
		approvals = append(approvals, a)
	}
	if approvals == nil {
		approvals = []Approval{}
	}
	return approvals, rows.Err()
}

func (q *Queries) GetUserEffectiveRole(ctx context.Context, workspaceID, userID string) (string, error) {
	row := q.db.QueryRow(ctx,
		`SELECT COALESCE(
			(SELECT wta.role FROM workspace_team_access wta
			 JOIN team_members tm ON tm.team_id = wta.team_id
			 WHERE wta.workspace_id = $1 AND tm.user_id = $2
			 ORDER BY CASE wta.role
				 WHEN 'owner' THEN 4 WHEN 'admin' THEN 3 WHEN 'operator' THEN 2 WHEN 'viewer' THEN 1
			 END DESC LIMIT 1),
			(SELECT u.role FROM users u WHERE u.id = $2)
		) as effective_role`,
		workspaceID, userID,
	)
	var role string
	err := row.Scan(&role)
	return role, err
}
