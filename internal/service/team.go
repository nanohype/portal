package service

import (
	"context"
	"regexp"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/repository"
)

var teamSlugRegex = regexp.MustCompile("[^a-z0-9-]")

// TeamService owns team lifecycle, membership, and the workspace team-access
// grants. Tenant/template access grants live on TeamAccessService.
type TeamService struct {
	queries *repository.Queries
}

func NewTeamService(queries *repository.Queries) *TeamService {
	return &TeamService{queries: queries}
}

// teamSlug derives a URL-safe slug from a team name: lowercase, alphanumerics
// and hyphens only, capped at 64 characters. Empty means the name had no
// usable characters.
func teamSlug(name string) string {
	slug := teamSlugRegex.ReplaceAllString(strings.ToLower(name), "")
	slug = strings.Trim(slug, "-")
	if len(slug) > 64 {
		slug = slug[:64]
	}
	return slug
}

func (s *TeamService) List(ctx context.Context, orgID string) ([]repository.Team, error) {
	return s.queries.ListTeams(ctx, orgID)
}

// ListForUser scopes the list to teams the user belongs to — the tenant create
// form uses it so operators see only teams they can own a tenant under.
func (s *TeamService) ListForUser(ctx context.Context, userID, orgID string) ([]repository.Team, error) {
	return s.queries.ListTeamsForUser(ctx, userID, orgID)
}

func (s *TeamService) Create(ctx context.Context, orgID, name string) (repository.Team, error) {
	slug := teamSlug(name)
	if slug == "" {
		return repository.Team{}, apperr.Validation("name must contain at least one alphanumeric character")
	}

	return s.queries.CreateTeam(ctx, repository.CreateTeamParams{
		ID:    ulid.Make().String(),
		OrgID: orgID,
		Name:  name,
		Slug:  slug,
	})
}

func (s *TeamService) Get(ctx context.Context, id, orgID string) (repository.Team, error) {
	team, err := s.queries.GetTeam(ctx, id, orgID)
	if err != nil {
		return repository.Team{}, apperr.Wrap(apperr.KindNotFound, "team not found", err)
	}
	return team, nil
}

func (s *TeamService) Delete(ctx context.Context, id, orgID string) error {
	return s.queries.DeleteTeam(ctx, id, orgID)
}

// ListMembers returns a team's members. The org-scoped existence check runs
// first so a cross-org team ID is not-found rather than an empty list.
func (s *TeamService) ListMembers(ctx context.Context, teamID, orgID string) ([]repository.TeamMember, error) {
	if _, err := s.Get(ctx, teamID, orgID); err != nil {
		return nil, err
	}
	return s.queries.ListTeamMembers(ctx, teamID)
}

type AddTeamMemberParams struct {
	TeamID        string
	OrgID         string
	UserID        string
	Role          string
	CloudIdentity string
}

// userInOrg keeps membership inside the org. team_members has no org_id and its
// foreign key only requires the user to exist, so the check has to be explicit.
func (s *TeamService) userInOrg(ctx context.Context, userID, orgID string) error {
	if _, err := s.queries.GetUserRole(ctx, userID, orgID); err != nil {
		return apperr.Wrap(apperr.KindNotFound, "user not found", err)
	}
	return nil
}

func (s *TeamService) AddMember(ctx context.Context, params AddTeamMemberParams) (repository.TeamMember, error) {
	if _, err := s.Get(ctx, params.TeamID, params.OrgID); err != nil {
		return repository.TeamMember{}, err
	}
	if err := s.userInOrg(ctx, params.UserID, params.OrgID); err != nil {
		return repository.TeamMember{}, err
	}

	return s.queries.AddTeamMember(ctx, repository.AddTeamMemberParams{
		ID:            ulid.Make().String(),
		TeamID:        params.TeamID,
		UserID:        params.UserID,
		Role:          params.Role,
		CloudIdentity: params.CloudIdentity,
	})
}

type UpdateTeamMemberParams struct {
	TeamID        string
	OrgID         string
	UserID        string
	Role          string
	CloudIdentity string
}

func (s *TeamService) UpdateMember(ctx context.Context, params UpdateTeamMemberParams) (repository.TeamMember, error) {
	if _, err := s.Get(ctx, params.TeamID, params.OrgID); err != nil {
		return repository.TeamMember{}, err
	}
	if err := s.userInOrg(ctx, params.UserID, params.OrgID); err != nil {
		return repository.TeamMember{}, err
	}

	return s.queries.UpdateTeamMember(ctx, repository.UpdateTeamMemberParams{
		TeamID:        params.TeamID,
		UserID:        params.UserID,
		Role:          params.Role,
		CloudIdentity: params.CloudIdentity,
	})
}

func (s *TeamService) RemoveMember(ctx context.Context, teamID, orgID, userID string) error {
	if _, err := s.Get(ctx, teamID, orgID); err != nil {
		return err
	}
	return s.queries.RemoveTeamMember(ctx, teamID, userID)
}

// ─── workspace access ─────────────────────────────────────────────────

// workspaceExists org-scopes the workspace before any access read/write so a
// cross-org workspace ID is not-found.
func (s *TeamService) workspaceExists(ctx context.Context, workspaceID, orgID string) error {
	if _, err := s.queries.GetWorkspace(ctx, repository.GetWorkspaceParams{
		ID: workspaceID, OrgID: orgID,
	}); err != nil {
		return apperr.Wrap(apperr.KindNotFound, "workspace not found", err)
	}
	return nil
}

func (s *TeamService) ListWorkspaceAccess(ctx context.Context, workspaceID, orgID string) ([]repository.WorkspaceTeamAccess, error) {
	if err := s.workspaceExists(ctx, workspaceID, orgID); err != nil {
		return nil, err
	}
	return s.queries.ListWorkspaceTeamAccess(ctx, workspaceID, orgID)
}

type SetWorkspaceAccessParams struct {
	WorkspaceID string
	OrgID       string
	TeamID      string
	Role        string
	// GranterRole is the org role of the caller writing the grant. A grant is
	// authority handed to other people, so it is capped at what the granter
	// holds themselves.
	GranterRole string
}

// SetWorkspaceAccess writes a team's grant on a workspace.
//
// Both sides are org-scoped: the workspace, and the team the grant names.
// workspace_team_access has no org_id of its own and its foreign key only
// requires the team to exist somewhere, so without the team check an admin
// could plant a row naming another org's team.
//
// The granted role may not exceed the granter's own. Nothing today reads an
// owner-level workspace grant, but an admin minting one would be handing out
// authority above their own the moment a route required it.
func (s *TeamService) SetWorkspaceAccess(ctx context.Context, params SetWorkspaceAccessParams) (repository.WorkspaceTeamAccess, error) {
	if err := s.workspaceExists(ctx, params.WorkspaceID, params.OrgID); err != nil {
		return repository.WorkspaceTeamAccess{}, err
	}
	if _, err := s.Get(ctx, params.TeamID, params.OrgID); err != nil {
		return repository.WorkspaceTeamAccess{}, err
	}
	if auth.MaxRole(params.GranterRole, params.Role) != params.GranterRole {
		return repository.WorkspaceTeamAccess{}, apperr.Forbidden("cannot grant a role above your own")
	}

	return s.queries.SetWorkspaceTeamAccess(ctx, repository.SetWorkspaceTeamAccessParams{
		ID:          ulid.Make().String(),
		WorkspaceID: params.WorkspaceID,
		TeamID:      params.TeamID,
		Role:        params.Role,
	})
}

func (s *TeamService) RemoveWorkspaceAccess(ctx context.Context, workspaceID, orgID, teamID string) error {
	if err := s.workspaceExists(ctx, workspaceID, orgID); err != nil {
		return err
	}
	return s.queries.RemoveWorkspaceTeamAccess(ctx, workspaceID, teamID)
}
