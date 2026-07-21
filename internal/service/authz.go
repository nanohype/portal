package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nanohype/portal/internal/repository"
)

// AuthzService answers the two questions every gate asks, and nothing else:
// what org role does this caller hold right now, and what has any of their
// teams been granted on this one workspace.
//
// It exists as its own service because both answers are read on every request
// and neither belongs to a domain — pinning them to UserService or TeamService
// would make the authorization path depend on whichever domain happened to own
// the table. The router hands one instance to the authentication middleware and
// to every workspace gate, so a request cannot be decided from two different
// pictures of who the caller is.
type AuthzService struct {
	queries *repository.Queries
}

func NewAuthzService(queries *repository.Queries) *AuthzService {
	return &AuthzService{queries: queries}
}

// UserRole reports the caller's current org role. A user with no row in that
// org is "" — an authenticated token for an account that no longer exists —
// which every gate treats as no authority. A real query failure comes back as
// an error so the middleware can deny rather than guess.
func (s *AuthzService) UserRole(ctx context.Context, userID, orgID string) (string, error) {
	role, err := s.queries.GetUserRole(ctx, userID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return role, nil
}

// WorkspaceTeamRole reports the role the caller picks up on one workspace
// through workspace_team_access, capped by their role within the granted team,
// or "" when no grant applies. It is the read side of the grant:
// auth.RequireWorkspaceRole combines the result with the caller's org role to
// decide a workspace-scoped request.
func (s *AuthzService) WorkspaceTeamRole(ctx context.Context, workspaceID, userID, orgID string) (string, error) {
	return s.queries.GetWorkspaceTeamRole(ctx, workspaceID, userID, orgID)
}
