package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/conv"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/storage"
	"github.com/nanohype/portal/internal/tfstate"
)

// ErrWorkspaceHasRuns is returned when a workspace cannot be deleted because it has runs.
var ErrWorkspaceHasRuns = fmt.Errorf("workspace has existing runs")

// ErrStorageNotConfigured is returned by state-backed operations (output
// import) when the service was built without an object store.
var ErrStorageNotConfigured = fmt.Errorf("storage not configured")

type WorkspaceService struct {
	queries *repository.Queries
	db      *pgxpool.Pool
	storage *storage.S3Storage
}

func NewWorkspaceService(queries *repository.Queries, db *pgxpool.Pool, store *storage.S3Storage) *WorkspaceService {
	return &WorkspaceService{queries: queries, db: db, storage: store}
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

func (s *WorkspaceService) List(ctx context.Context, orgID string, page, perPage int, search, environment string) ([]repository.WorkspaceSummary, int64, error) {
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

	return workspaces, count, nil
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

// ImportOutputsParams selects a source workspace whose latest state outputs are
// imported as terraform variables on a target workspace.
type ImportOutputsParams struct {
	SourceWorkspaceID string
	TargetWorkspaceID string
	OrgID             string
	// SkipSensitive drops outputs marked sensitive in state instead of storing
	// their values as plaintext variables. Pipeline stage imports skip them; the
	// explicit import endpoint brings everything the operator asked for across.
	SkipSensitive bool
	// DescriptionSource names where each imported value came from in the
	// variable's description, e.g. "workspace" or "pipeline stage".
	DescriptionSource string
}

// ImportOutputs reads the source workspace's latest state, parses its outputs,
// and upserts each one as a terraform-category variable on the target (update
// by key when it exists, create otherwise). Both the import-outputs endpoint
// and pipeline stage advancement run through here. A failed upsert is logged
// and skipped so one bad output doesn't abort the rest; the returned slice
// holds the variables actually written. A source with no outputs returns an
// empty result — callers decide whether that is an error.
func (s *WorkspaceService) ImportOutputs(ctx context.Context, params ImportOutputsParams) ([]repository.WorkspaceVariable, error) {
	if s.storage == nil {
		return nil, ErrStorageNotConfigured
	}

	sv, err := s.queries.GetLatestStateVersion(ctx, repository.GetLatestStateVersionParams{
		WorkspaceID: params.SourceWorkspaceID, OrgID: params.OrgID,
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.KindNotFound, "source workspace has no state", err)
	}

	data, err := s.storage.GetState(ctx, sv.StateURL)
	if err != nil {
		return nil, fmt.Errorf("fetch source state: %w", err)
	}

	outputs, err := tfstate.ParseOutputs(data)
	if err != nil {
		return nil, fmt.Errorf("parse source outputs: %w", err)
	}
	if len(outputs) == 0 {
		slog.Info("no outputs to import", "source_workspace", params.SourceWorkspaceID, "target_workspace", params.TargetWorkspaceID)
		return nil, nil
	}

	existing, err := s.queries.ListWorkspaceVariables(ctx, repository.ListWorkspaceVariablesParams{
		WorkspaceID: params.TargetWorkspaceID, OrgID: params.OrgID,
	})
	if err != nil {
		return nil, fmt.Errorf("list target variables: %w", err)
	}
	existingByKey := make(map[string]repository.WorkspaceVariable, len(existing))
	for _, v := range existing {
		if v.Category == "terraform" {
			existingByKey[v.Key] = v
		}
	}

	affected := make([]repository.WorkspaceVariable, 0, len(outputs))
	for _, out := range outputs {
		if params.SkipSensitive && out.Sensitive {
			continue
		}

		// Non-string outputs (lists, maps, numbers) are stored as their JSON
		// encoding, which is also how tofu expects complex variable values.
		var valueStr string
		switch v := out.Value.(type) {
		case string:
			valueStr = v
		default:
			b, _ := json.Marshal(v)
			valueStr = string(b)
		}

		desc := fmt.Sprintf("Imported from %s output (%s)", params.DescriptionSource, out.Type)

		var v repository.WorkspaceVariable
		if ev, exists := existingByKey[out.Name]; exists {
			v, err = s.queries.UpdateWorkspaceVariable(ctx, repository.UpdateWorkspaceVariableParams{
				ID: ev.ID, OrgID: params.OrgID, Value: valueStr, Sensitive: false, Description: desc,
			})
		} else {
			v, err = s.queries.CreateWorkspaceVariable(ctx, repository.CreateWorkspaceVariableParams{
				ID:          ulid.Make().String(),
				WorkspaceID: params.TargetWorkspaceID,
				OrgID:       params.OrgID,
				Key:         out.Name,
				Value:       valueStr,
				Sensitive:   false,
				Category:    "terraform",
				Description: desc,
			})
		}
		if err != nil {
			slog.Warn("failed to import output as variable", "key", out.Name, "error", err)
			continue
		}
		affected = append(affected, v)
	}

	slog.Info("imported outputs between workspaces",
		"source", params.SourceWorkspaceID, "target", params.TargetWorkspaceID, "imported", len(affected))
	return affected, nil
}
