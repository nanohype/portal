package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"

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

// NewWorkspaceService builds the workspace domain. The store is what output
// import reads state through; without one the import paths return
// ErrStorageNotConfigured. No encryptor: variable ciphertext is copied between
// workspaces as-is, and state outputs the source marked sensitive arrive
// already redacted, so nothing here ever holds a plaintext secret.
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

// CanonicalWorkingDir reduces a working directory to the one spelling that
// names that leaf.
//
// "envs/prod", "./envs/prod", "envs//prod", "envs/./prod", "envs/prod/" and
// "envs/prod/." are one directory to both executors — the local one joins the
// path with filepath.Join, which cleans it, and the Kubernetes one emits
// `cd "/work/$PORTAL_WORKING_DIR"`, where `cd envs//prod` and `cd envs/./prod`
// are the same cd in /bin/sh. So they have to be one string to portal too:
// the gated-twin check compares stored working_dir values, and a comparison
// that reads those as different targets is a second door onto gated
// infrastructure that anyone who may create a workspace can open by respelling
// the path. Canonicalising on the way in means the column only ever holds one
// spelling of a leaf, so the comparison sees the target and not the typing.
//
// Rooting the path before cleaning also neutralises "..": path.Clean resolves
// it against "/" and can never climb above it, so a caller who reaches this
// without the handler's validation still cannot name anything outside the
// checkout.
//
// Empty in, empty out. An omitted field is a request to keep the stored value,
// not a request to point at the repo root; Create fills the "." default itself.
func CanonicalWorkingDir(dir string) string {
	if dir == "" {
		return ""
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+dir), "/")
	if cleaned == "" {
		return "."
	}
	return cleaned
}

// HasGatedTwin reports whether another workspace in this org already gates the
// same repo + working_dir behind an approval.
//
// The workspace row is a label on a config, not a boundary around it: with
// terragrunt the backend is declared in the repo, so two workspaces on the same
// path share one remote state and one set of real resources. Without this,
// requires_approval is a property of a row anyone may stand up a second copy
// of.
//
// The working directory is canonicalised here as well as on write, so a query
// spelled "envs//prod" is asked about the same leaf the column stores as
// "envs/prod". The repo URL is not: it is a clone target, and rewriting it
// would break a URL that carries its own credentials or an scp-style remote,
// so its spellings are collapsed inside the query instead.
//
// excludeID keeps a workspace from matching itself on update.
func (s *WorkspaceService) HasGatedTwin(ctx context.Context, orgID, repoURL, workingDir, excludeID string) (bool, error) {
	return s.queries.HasGatedWorkspaceForConfig(ctx, repository.GatedTwinParams{
		OrgID:      orgID,
		RepoURL:    repoURL,
		WorkingDir: CanonicalWorkingDir(workingDir),
		ExcludeID:  excludeID,
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
	workDir := CanonicalWorkingDir(params.WorkingDir)
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

// Update writes the fields a request carried. Empty strings mean "keep what is
// stored" (the query COALESCEs them), so the working directory is canonicalised
// only when one was actually supplied — CanonicalWorkingDir leaves empty empty
// so an omitted field stays omitted rather than becoming a move to the repo
// root.
func (s *WorkspaceService) Update(ctx context.Context, params UpdateWorkspaceParams) (repository.Workspace, error) {
	return s.queries.UpdateWorkspace(ctx, repository.UpdateWorkspaceParams{
		ID:                params.ID,
		OrgID:             params.OrgID,
		Name:              params.Name,
		Description:       params.Description,
		RepoURL:           params.RepoURL,
		RepoBranch:        params.RepoBranch,
		WorkingDir:        CanonicalWorkingDir(params.WorkingDir),
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
				ID: existing.ID, WorkspaceID: targetID, OrgID: orgID,
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
	// DescriptionSource names where each imported value came from in the
	// variable's description, e.g. "workspace" or "pipeline stage".
	DescriptionSource string
}

// importableOutputs splits parsed state outputs into the ones that can become
// variables and a count of the ones that cannot.
//
// tfstate.ParseOutputs blanks the value of every output the state marks
// sensitive, so by the time an output reaches here a sensitive one carries no
// value at all. Importing it would store the JSON encoding of nothing — the
// four characters "null" — under the source's key, and the worker would hand
// that to the next run as TF_VAR_<key>=null. There is nothing to import once
// the parser has redacted it, so sensitive outputs are dropped here and
// counted, and the caller says so rather than writing a plausible-looking
// variable whose content is garbage.
func importableOutputs(outputs []tfstate.Output) ([]tfstate.Output, int) {
	importable := make([]tfstate.Output, 0, len(outputs))
	skipped := 0
	for _, out := range outputs {
		if out.Sensitive {
			skipped++
			continue
		}
		importable = append(importable, out)
	}
	return importable, skipped
}

// ImportOutputs reads the source workspace's latest state, parses its outputs,
// and upserts each importable one as a terraform-category variable on the
// target (update by key when it exists, create otherwise). Both the
// import-outputs endpoint and pipeline stage advancement run through here. A
// failed upsert is logged and skipped so one bad output doesn't abort the rest.
//
// It returns the variables actually written and how many outputs were dropped
// for being sensitive — state redacts those, so there is no value to carry
// across. A source with no outputs returns an empty result; callers decide
// whether that is an error.
func (s *WorkspaceService) ImportOutputs(ctx context.Context, params ImportOutputsParams) ([]repository.WorkspaceVariable, int, error) {
	if s.storage == nil {
		return nil, 0, ErrStorageNotConfigured
	}

	sv, err := s.queries.GetLatestStateVersion(ctx, repository.GetLatestStateVersionParams{
		WorkspaceID: params.SourceWorkspaceID, OrgID: params.OrgID,
	})
	if err != nil {
		return nil, 0, apperr.Wrap(apperr.KindNotFound, "source workspace has no state", err)
	}

	data, err := s.storage.GetState(ctx, sv.StateURL)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch source state: %w", err)
	}

	outputs, err := tfstate.ParseOutputs(data)
	if err != nil {
		return nil, 0, fmt.Errorf("parse source outputs: %w", err)
	}
	if len(outputs) == 0 {
		slog.Info("no outputs to import", "source_workspace", params.SourceWorkspaceID, "target_workspace", params.TargetWorkspaceID)
		return nil, 0, nil
	}

	outputs, skippedSensitive := importableOutputs(outputs)
	if skippedSensitive > 0 {
		slog.Info("skipped sensitive outputs on import: state redacts their values",
			"source_workspace", params.SourceWorkspaceID, "target_workspace", params.TargetWorkspaceID,
			"skipped", skippedSensitive)
	}
	if len(outputs) == 0 {
		return nil, skippedSensitive, nil
	}

	existing, err := s.queries.ListWorkspaceVariables(ctx, repository.ListWorkspaceVariablesParams{
		WorkspaceID: params.TargetWorkspaceID, OrgID: params.OrgID,
	})
	if err != nil {
		return nil, skippedSensitive, fmt.Errorf("list target variables: %w", err)
	}
	existingByKey := make(map[string]repository.WorkspaceVariable, len(existing))
	for _, v := range existing {
		if v.Category == "terraform" {
			existingByKey[v.Key] = v
		}
	}

	affected := make([]repository.WorkspaceVariable, 0, len(outputs))
	for _, out := range outputs {
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

		// Everything that gets here is a value the source state published in
		// the clear, so it is stored in the clear: marking it sensitive would
		// hide a public value behind the reveal endpoint without protecting
		// anything.
		var v repository.WorkspaceVariable
		if ev, exists := existingByKey[out.Name]; exists {
			v, err = s.queries.UpdateWorkspaceVariable(ctx, repository.UpdateWorkspaceVariableParams{
				ID: ev.ID, WorkspaceID: params.TargetWorkspaceID, OrgID: params.OrgID,
				Value: valueStr, Sensitive: false, Description: desc,
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
		"source", params.SourceWorkspaceID, "target", params.TargetWorkspaceID,
		"imported", len(affected), "skipped_sensitive", skippedSensitive)
	return affected, skippedSensitive, nil
}
