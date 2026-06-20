package service

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/conv"
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
	offset := conv.Int32((page - 1) * perPage)

	workspaces, err := s.queries.ListWorkspacesWithSummary(ctx, repository.ListWorkspacesWithSummaryParams{
		OrgID:       orgID,
		Limit:       conv.Int32(perPage),
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

// CopyAll copies every variable from source into target in one transaction. It
// backs workspace clone, whose target is a freshly created, empty workspace.
// Values are copied as stored (ciphertext; the encryption key is org-wide, so a
// value is portable between workspaces) — the previous inline loop copied them
// one row at a time, non-transactionally, so a mid-copy failure left a workspace
// with half its variables. One transaction makes the copy all-or-nothing.
// Returns the number copied.
func (s *WorkspaceService) CopyAll(ctx context.Context, sourceID, targetID, orgID string) (int, error) {
	vars, err := s.queries.ListWorkspaceVariables(ctx, repository.ListWorkspaceVariablesParams{
		WorkspaceID: sourceID, OrgID: orgID,
	})
	if err != nil {
		return 0, err
	}
	if len(vars) == 0 {
		return 0, nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin variable-copy tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	for _, v := range vars {
		if _, err := qtx.CreateWorkspaceVariable(ctx, repository.CreateWorkspaceVariableParams{
			ID:          ulid.Make().String(),
			WorkspaceID: targetID,
			OrgID:       orgID,
			Key:         v.Key,
			Value:       v.Value,
			Sensitive:   v.Sensitive,
			Category:    v.Category,
			Description: v.Description,
		}); err != nil {
			return 0, fmt.Errorf("copy variable %q: %w", v.Key, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit variable-copy tx: %w", err)
	}
	return len(vars), nil
}

// CopyInto copies source's variables into target, upserting by key+category, in
// one transaction (the explicit copy-from-another-workspace action). Values are
// copied as stored. Returns the affected variables so the caller can audit each;
// a source with no variables is apperr.Validation (the caller maps it to 400).
func (s *WorkspaceService) CopyInto(ctx context.Context, sourceID, targetID, orgID string) ([]repository.WorkspaceVariable, error) {
	sourceVars, err := s.queries.ListWorkspaceVariables(ctx, repository.ListWorkspaceVariablesParams{
		WorkspaceID: sourceID, OrgID: orgID,
	})
	if err != nil {
		return nil, err
	}
	if len(sourceVars) == 0 {
		return nil, apperr.Validation("source workspace has no variables")
	}

	targetVars, err := s.queries.ListWorkspaceVariables(ctx, repository.ListWorkspaceVariablesParams{
		WorkspaceID: targetID, OrgID: orgID,
	})
	if err != nil {
		return nil, err
	}
	existingByKey := make(map[string]repository.WorkspaceVariable, len(targetVars))
	for _, v := range targetVars {
		existingByKey[v.Key+"|"+v.Category] = v
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin variable-copy tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	affected := make([]repository.WorkspaceVariable, 0, len(sourceVars))
	for _, sv := range sourceVars {
		var v repository.WorkspaceVariable
		if existing, ok := existingByKey[sv.Key+"|"+sv.Category]; ok {
			v, err = qtx.UpdateWorkspaceVariable(ctx, repository.UpdateWorkspaceVariableParams{
				ID: existing.ID, OrgID: orgID,
				Value: sv.Value, Sensitive: sv.Sensitive, Description: sv.Description,
			})
		} else {
			v, err = qtx.CreateWorkspaceVariable(ctx, repository.CreateWorkspaceVariableParams{
				ID:          ulid.Make().String(),
				WorkspaceID: targetID,
				OrgID:       orgID,
				Key:         sv.Key,
				Value:       sv.Value,
				Sensitive:   sv.Sensitive,
				Category:    sv.Category,
				Description: sv.Description,
			})
		}
		if err != nil {
			return nil, fmt.Errorf("copy variable %q: %w", sv.Key, err)
		}
		affected = append(affected, v)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit variable-copy tx: %w", err)
	}
	return affected, nil
}
