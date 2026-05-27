package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/stxkxs/tofui/internal/repository"
)

// TeamAccessService is the write + read API for tenant_team_access and
// template_team_access. Read-side filtering (list-by-teams) lives on the
// existing TenantService / TemplateService where the data already lives;
// this service handles the grant/revoke + UserTeamIDs lookup that other
// handlers need.
type TeamAccessService struct {
	queries *repository.Queries
	db      *pgxpool.Pool
}

func NewTeamAccessService(queries *repository.Queries, db *pgxpool.Pool) *TeamAccessService {
	return &TeamAccessService{queries: queries, db: db}
}

// UserTeamIDs returns the team IDs a user belongs to in an org. Used by the
// list paths in TenantHandler / TemplateHandler to decide whether the
// caller's read should be scoped.
func (s *TeamAccessService) UserTeamIDs(ctx context.Context, userID, orgID string) ([]string, error) {
	return s.queries.ListUserTeamIDs(ctx, userID, orgID)
}

// ─── tenants ──────────────────────────────────────────────────────────

func (s *TeamAccessService) GrantTenant(ctx context.Context, orgID, clusterID, tenantName, teamID, grantedBy string) (repository.TenantTeamAccess, error) {
	return s.queries.GrantTenantTeamAccess(ctx, repository.GrantTenantTeamAccessParams{
		ID:         ulid.Make().String(),
		OrgID:      orgID,
		ClusterID:  clusterID,
		TenantName: tenantName,
		TeamID:     teamID,
		GrantedBy:  grantedBy,
	})
}

func (s *TeamAccessService) RevokeTenant(ctx context.Context, orgID, clusterID, tenantName, teamID string) error {
	return s.queries.RevokeTenantTeamAccess(ctx, repository.RevokeTenantTeamAccessParams{
		OrgID:      orgID,
		ClusterID:  clusterID,
		TenantName: tenantName,
		TeamID:     teamID,
	})
}

// RevokeAllForTenant clears all access rows for a tenant. Called when the
// tenant is deleted via tofui so stale access rows don't accumulate.
func (s *TeamAccessService) RevokeAllForTenant(ctx context.Context, orgID, clusterID, tenantName string) error {
	return s.queries.RevokeAllTenantTeamAccess(ctx, repository.RevokeAllTenantTeamAccessParams{
		OrgID:      orgID,
		ClusterID:  clusterID,
		TenantName: tenantName,
	})
}

func (s *TeamAccessService) ListTenant(ctx context.Context, orgID, clusterID, tenantName string) ([]repository.TenantTeamAccess, error) {
	return s.queries.ListTenantTeamAccess(ctx, repository.ListTenantTeamAccessParams{
		OrgID:      orgID,
		ClusterID:  clusterID,
		TenantName: tenantName,
	})
}

// ─── templates ────────────────────────────────────────────────────────

func (s *TeamAccessService) GrantTemplate(ctx context.Context, orgID, templateID, teamID, grantedBy string) (repository.TemplateTeamAccess, error) {
	return s.queries.GrantTemplateTeamAccess(ctx, repository.GrantTemplateTeamAccessParams{
		ID:         ulid.Make().String(),
		OrgID:      orgID,
		TemplateID: templateID,
		TeamID:     teamID,
		GrantedBy:  grantedBy,
	})
}

func (s *TeamAccessService) RevokeTemplate(ctx context.Context, orgID, templateID, teamID string) error {
	return s.queries.RevokeTemplateTeamAccess(ctx, repository.RevokeTemplateTeamAccessParams{
		OrgID:      orgID,
		TemplateID: templateID,
		TeamID:     teamID,
	})
}

func (s *TeamAccessService) ListTemplate(ctx context.Context, orgID, templateID string) ([]repository.TemplateTeamAccess, error) {
	return s.queries.ListTemplateTeamAccess(ctx, repository.ListTemplateTeamAccessParams{
		OrgID:      orgID,
		TemplateID: templateID,
	})
}
