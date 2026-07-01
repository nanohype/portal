package service

import (
	"context"
	"fmt"

	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/repository"
)

// UserService owns identity provisioning (login upserts, org bootstrap, role
// assignment) and user administration. Handlers stay transport-only: the OAuth
// dance lives in the auth handler, the resulting identity lands here.
type UserService struct {
	queries *repository.Queries
}

func NewUserService(queries *repository.Queries) *UserService {
	return &UserService{queries: queries}
}

// EnsureDefaultOrg returns the instance's default organization, creating it on
// first login (single-org mode).
func (s *UserService) EnsureDefaultOrg(ctx context.Context) (repository.Organization, error) {
	org, err := s.queries.GetDefaultOrganization(ctx)
	if err == nil {
		return org, nil
	}

	return s.queries.CreateOrganization(ctx, repository.CreateOrganizationParams{
		ID:   ulid.Make().String(),
		Name: "Default Organization",
		Slug: "default",
	})
}

// ProvisionUserParams carries the identity a login flow verified. GitHubID set
// keys the upsert on the GitHub account; nil keys it on email (dev login).
type ProvisionUserParams struct {
	Email     string
	Name      string
	AvatarURL string
	GitHubID  *int64
}

// Provision upserts a verified identity into the default org and returns the
// user. The first user in the org becomes owner; everyone after starts as
// viewer (an owner promotes them).
func (s *UserService) Provision(ctx context.Context, params ProvisionUserParams) (repository.User, error) {
	org, err := s.EnsureDefaultOrg(ctx)
	if err != nil {
		return repository.User{}, fmt.Errorf("ensure default org: %w", err)
	}

	userCount, err := s.queries.CountUsersByOrg(ctx, org.ID)
	if err != nil {
		return repository.User{}, fmt.Errorf("count users: %w", err)
	}
	role := assignRole(userCount)

	if params.GitHubID != nil {
		return s.queries.UpsertUserByGitHubID(ctx, repository.UpsertUserByGitHubIDParams{
			ID:        ulid.Make().String(),
			OrgID:     org.ID,
			Email:     params.Email,
			Name:      params.Name,
			AvatarURL: params.AvatarURL,
			GithubID:  params.GitHubID,
			Role:      role,
		})
	}

	return s.queries.UpsertUserByEmail(ctx, repository.UpsertUserByEmailParams{
		ID:        ulid.Make().String(),
		OrgID:     org.ID,
		Email:     params.Email,
		Name:      params.Name,
		AvatarURL: params.AvatarURL,
		Role:      role,
	})
}

// Get returns a user by ID. Any lookup failure surfaces as not-found — the
// caller already holds an authenticated claim, so there is nothing more
// specific to reveal.
func (s *UserService) Get(ctx context.Context, id string) (repository.User, error) {
	user, err := s.queries.GetUser(ctx, id)
	if err != nil {
		return repository.User{}, apperr.Wrap(apperr.KindNotFound, "user not found", err)
	}
	return user, nil
}

func (s *UserService) List(ctx context.Context, orgID string) ([]repository.User, error) {
	return s.queries.ListUsersByOrg(ctx, orgID)
}

// UpdateRole changes a user's role, scoped to the caller's org. A cross-org
// target is not-found (without this, an owner in org A could re-role any user
// in org B by ID), and the last owner cannot be demoted — an org without an
// owner has nobody left who can manage roles.
func (s *UserService) UpdateRole(ctx context.Context, targetUserID, orgID, role string) (repository.User, error) {
	targetUser, err := s.queries.GetUser(ctx, targetUserID)
	if err != nil || targetUser.OrgID != orgID {
		return repository.User{}, apperr.NotFound("user not found")
	}

	if role != "owner" && targetUser.Role == "owner" {
		ownerCount, err := s.queries.CountOwnersByOrg(ctx, orgID)
		if err != nil {
			return repository.User{}, fmt.Errorf("count owners: %w", err)
		}
		if ownerCount <= 1 {
			return repository.User{}, apperr.Validation("cannot demote the last owner")
		}
	}

	return s.queries.UpdateUserRole(ctx, repository.UpdateUserRoleParams{
		ID:    targetUserID,
		Role:  role,
		OrgID: orgID,
	})
}

// assignRole returns "owner" for the first user in an org, "viewer" otherwise.
func assignRole(userCount int64) string {
	if userCount == 0 {
		return "owner"
	}
	return "viewer"
}
