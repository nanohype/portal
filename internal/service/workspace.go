package service

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/repository"
)

// ErrWorkspaceHasRuns is returned when a workspace cannot be deleted because it has runs.
var ErrWorkspaceHasRuns = fmt.Errorf("workspace has existing runs")

type WorkspaceService struct {
	queries *repository.Queries
	db      *pgxpool.Pool
}

func NewWorkspaceService(queries *repository.Queries, db *pgxpool.Pool) *WorkspaceService {
	return &WorkspaceService{queries: queries, db: db}
}

type CreateWorkspaceParams struct {
	OrgID             string
	Name              string
	Description       string
	RepoURL           string
	RepoBranch        string
	WorkingDir        string
	TofuVersion       string
	Environment       string
	AutoApply         bool
	RequiresApproval  bool
	VcsTriggerEnabled bool
	CreatedBy         string
	Source            string
}

type UpdateWorkspaceParams struct {
	ID                string
	OrgID             string
	Name              string
	Description       string
	RepoURL           string
	RepoBranch        string
	WorkingDir        string
	TofuVersion       string
	Environment       string
	AutoApply         *bool
	RequiresApproval  *bool
	VcsTriggerEnabled *bool
}

func (s *WorkspaceService) List(ctx context.Context, orgID string, page, perPage int, search, environment string) ([]any, int64, error) {
	offset := int32((page - 1) * perPage)

	workspaces, err := s.queries.ListWorkspacesWithSummary(ctx, repository.ListWorkspacesWithSummaryParams{
		OrgID:       orgID,
		Limit:       int32(perPage),
		Offset:      offset,
		Search:      search,
		Environment: environment,
	})
	if err != nil {
		return nil, 0, err
	}

	count, err := s.queries.CountWorkspacesFiltered(ctx, repository.CountWorkspacesFilteredParams{
		OrgID:       orgID,
		Search:      search,
		Environment: environment,
	})
	if err != nil {
		return nil, 0, err
	}

	result := make([]any, len(workspaces))
	for i, w := range workspaces {
		result[i] = w
	}

	return result, count, nil
}

func (s *WorkspaceService) Get(ctx context.Context, id, orgID string) (repository.Workspace, error) {
	return s.queries.GetWorkspace(ctx, repository.GetWorkspaceParams{
		ID:    id,
		OrgID: orgID,
	})
}

func (s *WorkspaceService) Create(ctx context.Context, params CreateWorkspaceParams) (repository.Workspace, error) {
	source := params.Source
	if source == "" {
		source = "vcs"
	}
	branch := params.RepoBranch
	if branch == "" && source == "vcs" {
		branch = "main"
	}
	workDir := params.WorkingDir
	if workDir == "" {
		workDir = "."
	}
	tofuVersion := params.TofuVersion
	if tofuVersion == "" {
		tofuVersion = "1.11.0"
	}
	env := params.Environment
	if env == "" {
		env = "development"
	}

	return s.queries.CreateWorkspace(ctx, repository.CreateWorkspaceParams{
		ID:                ulid.Make().String(),
		OrgID:             params.OrgID,
		Name:              params.Name,
		Description:       params.Description,
		RepoURL:           params.RepoURL,
		RepoBranch:        branch,
		WorkingDir:        workDir,
		TofuVersion:       tofuVersion,
		Environment:       env,
		AutoApply:         params.AutoApply,
		RequiresApproval:  params.RequiresApproval,
		VcsTriggerEnabled: params.VcsTriggerEnabled,
		CreatedBy:         params.CreatedBy,
		Source:            source,
	})
}

func (s *WorkspaceService) Update(ctx context.Context, params UpdateWorkspaceParams) (repository.Workspace, error) {
	return s.queries.UpdateWorkspace(ctx, repository.UpdateWorkspaceParams{
		ID:                params.ID,
		OrgID:             params.OrgID,
		Name:              params.Name,
		Description:       params.Description,
		RepoURL:           params.RepoURL,
		RepoBranch:        params.RepoBranch,
		WorkingDir:        params.WorkingDir,
		TofuVersion:       params.TofuVersion,
		Environment:       params.Environment,
		AutoApply:         params.AutoApply,
		RequiresApproval:  params.RequiresApproval,
		VcsTriggerEnabled: params.VcsTriggerEnabled,
	})
}

func (s *WorkspaceService) Delete(ctx context.Context, id, orgID string) error {
	hasRuns, err := s.queries.HasRunsForWorkspace(ctx, id, orgID)
	if err != nil {
		return fmt.Errorf("check workspace runs: %w", err)
	}
	if hasRuns {
		return ErrWorkspaceHasRuns
	}

	return s.queries.DeleteWorkspace(ctx, repository.DeleteWorkspaceParams{
		ID:    id,
		OrgID: orgID,
	})
}

func (s *WorkspaceService) Lock(ctx context.Context, id, orgID, lockedBy string) (repository.Workspace, error) {
	return s.queries.LockWorkspace(ctx, id, orgID, lockedBy)
}

func (s *WorkspaceService) Unlock(ctx context.Context, id, orgID string) (repository.Workspace, error) {
	return s.queries.UnlockWorkspace(ctx, id, orgID)
}
