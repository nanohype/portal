package service

import (
	"context"
	"fmt"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/repository"
)

func redactWorkspace(v repository.WorkspaceVariable) repository.WorkspaceVariable {
	if v.Sensitive {
		v.Value = redactedValue
	}
	return v
}

func (s *VariableService) ListWorkspaceVariables(ctx context.Context, orgID, workspaceID string) ([]repository.WorkspaceVariable, error) {
	vars, err := s.queries.ListWorkspaceVariables(ctx, repository.ListWorkspaceVariablesParams{WorkspaceID: workspaceID, OrgID: orgID})
	if err != nil {
		return nil, fmt.Errorf("list workspace variables: %w", err)
	}
	for i := range vars {
		vars[i] = redactWorkspace(vars[i])
	}
	return vars, nil
}

func (s *VariableService) CreateWorkspaceVariable(ctx context.Context, orgID, workspaceID string, in VariableInput, actor ActorMeta) (repository.WorkspaceVariable, error) {
	in, err := in.Normalize()
	if err != nil {
		return repository.WorkspaceVariable{}, err
	}
	v, err := s.createWorkspaceVariable(ctx, orgID, workspaceID, in)
	if err != nil {
		return repository.WorkspaceVariable{}, err
	}
	s.logChange(ctx, orgID, "variable.create", "variable", v.ID, nil, redactWorkspace(v), actor)
	return redactWorkspace(v), nil
}

// createWorkspaceVariable is the write without the audit entry, so a bulk caller
// can log once for the batch instead of once per row while still going through
// the same normalization and sealing.
func (s *VariableService) createWorkspaceVariable(ctx context.Context, orgID, workspaceID string, in VariableInput) (repository.WorkspaceVariable, error) {
	stored, err := s.seal(in)
	if err != nil {
		return repository.WorkspaceVariable{}, err
	}
	v, err := s.queries.CreateWorkspaceVariable(ctx, repository.CreateWorkspaceVariableParams{
		ID: newVariableID(), WorkspaceID: workspaceID, OrgID: orgID,
		Key: in.Key, Value: stored, Sensitive: in.Sensitive,
		Category: in.Category, Description: in.Description,
	})
	if err != nil {
		return repository.WorkspaceVariable{}, fmt.Errorf("create workspace variable: %w", err)
	}
	return v, nil
}

// BulkCreateWorkspaceVariables applies the batch rules, then writes every row.
//
// Audit granularity is deliberately per variable rather than per batch: the
// trail records "variable.create" once per key, the same entries a caller would
// get creating them one at a time. A single batch entry would be tidier and
// would lose the ability to answer "when did this key appear".
const maxBulkVariables = 50

func (s *VariableService) BulkCreateWorkspaceVariables(ctx context.Context, orgID, workspaceID string, ins []VariableInput, actor ActorMeta) ([]repository.WorkspaceVariable, error) {
	if len(ins) == 0 {
		return nil, apperr.Validation("variables array is required")
	}
	if len(ins) > maxBulkVariables {
		return nil, apperr.Validation(fmt.Sprintf("maximum %d variables per batch", maxBulkVariables))
	}

	// Normalize and check the whole batch before writing any of it, so a batch
	// with one bad category is rejected whole rather than applied halfway.
	seen := make(map[string]bool, len(ins))
	normalized := make([]VariableInput, len(ins))
	for i, in := range ins {
		n, err := in.Normalize()
		if err != nil {
			return nil, err
		}
		if seen[n.Key] {
			return nil, apperr.Validation("duplicate key: " + n.Key)
		}
		seen[n.Key] = true
		normalized[i] = n
	}

	created := make([]repository.WorkspaceVariable, 0, len(normalized))
	for _, in := range normalized {
		v, err := s.createWorkspaceVariable(ctx, orgID, workspaceID, in)
		if err != nil {
			return nil, err
		}
		s.logChange(ctx, orgID, "variable.create", "variable", v.ID, nil, redactWorkspace(v), actor)
		created = append(created, redactWorkspace(v))
	}
	return created, nil
}

func (s *VariableService) UpdateWorkspaceVariable(ctx context.Context, orgID, workspaceID, varID string, in VariableInput, actor ActorMeta) (repository.WorkspaceVariable, error) {
	before, err := s.queries.GetWorkspaceVariable(ctx, repository.GetWorkspaceVariableParams{ID: varID, WorkspaceID: workspaceID, OrgID: orgID})
	if err != nil {
		return repository.WorkspaceVariable{}, fmt.Errorf("get workspace variable: %w", err)
	}
	in, err = in.Normalize()
	if err != nil {
		return repository.WorkspaceVariable{}, err
	}
	stored, err := s.seal(in)
	if err != nil {
		return repository.WorkspaceVariable{}, err
	}

	v, err := s.queries.UpdateWorkspaceVariable(ctx, repository.UpdateWorkspaceVariableParams{
		ID: varID, WorkspaceID: workspaceID, OrgID: orgID, Value: stored, Sensitive: in.Sensitive,
		Description: in.Description, Category: in.Category,
	})
	if err != nil {
		return repository.WorkspaceVariable{}, fmt.Errorf("update workspace variable: %w", err)
	}

	s.logChange(ctx, orgID, "variable.update", "variable", varID, redactWorkspace(before), redactWorkspace(v), actor)
	return redactWorkspace(v), nil
}

func (s *VariableService) DeleteWorkspaceVariable(ctx context.Context, orgID, workspaceID, varID string, actor ActorMeta) error {
	if _, err := s.queries.DeleteWorkspaceVariable(ctx, repository.DeleteWorkspaceVariableParams{ID: varID, WorkspaceID: workspaceID, OrgID: orgID}); err != nil {
		return fmt.Errorf("delete workspace variable: %w", err)
	}
	s.logChange(ctx, orgID, "variable.delete", "variable", varID, nil, nil, actor)
	return nil
}

func (s *VariableService) RevealWorkspaceVariable(ctx context.Context, orgID, workspaceID, varID string, actor ActorMeta) (string, error) {
	v, err := s.queries.GetWorkspaceVariable(ctx, repository.GetWorkspaceVariableParams{ID: varID, WorkspaceID: workspaceID, OrgID: orgID})
	if err != nil {
		return "", fmt.Errorf("get workspace variable: %w", err)
	}
	plain, err := s.open(v.Value, v.Sensitive)
	if err != nil {
		return "", err
	}
	if err := s.recordDisclosure(ctx, orgID, "variable.reveal", "variable", varID, actor); err != nil {
		return "", fmt.Errorf("record disclosure: %w", err)
	}
	return plain, nil
}

// EffectiveVariable is one variable as a run would see it, after the three
// scopes have been layered.
type EffectiveVariable struct {
	Key         string
	Value       string
	Sensitive   bool
	Category    string
	Description string
	Source      string // "org", "pipeline", or "workspace"
	SourceID    string
}

// EffectiveVariables layers org < pipeline < workspace, which is the precedence
// the worker applies at run time. Values arrive already redacted, because this
// is a view an operator reads before approving an apply, not a way to read
// secrets.
//
// Every layer is required. A read that fails is not an empty layer: dropping it
// would return a smaller effective set than the run will actually use, with a
// 200 and nothing to say a layer is missing.
func (s *VariableService) EffectiveVariables(ctx context.Context, orgID, workspaceID, pipelineID string) ([]EffectiveVariable, error) {
	merged := make(map[string]EffectiveVariable)
	put := func(key, category string, v EffectiveVariable) { merged[key+"|"+category] = v }

	orgVars, err := s.ListOrgVariables(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, v := range orgVars {
		put(v.Key, v.Category, EffectiveVariable{
			Key: v.Key, Value: v.Value, Sensitive: v.Sensitive,
			Category: v.Category, Description: v.Description,
			Source: "org", SourceID: v.ID,
		})
	}

	if pipelineID != "" {
		pipelineVars, err := s.ListPipelineVariables(ctx, orgID, pipelineID)
		if err != nil {
			return nil, err
		}
		for _, v := range pipelineVars {
			put(v.Key, v.Category, EffectiveVariable{
				Key: v.Key, Value: v.Value, Sensitive: v.Sensitive,
				Category: v.Category, Description: v.Description,
				Source: "pipeline", SourceID: v.ID,
			})
		}
	}

	wsVars, err := s.ListWorkspaceVariables(ctx, orgID, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, v := range wsVars {
		put(v.Key, v.Category, EffectiveVariable{
			Key: v.Key, Value: v.Value, Sensitive: v.Sensitive,
			Category: v.Category, Description: v.Description,
			Source: "workspace", SourceID: v.ID,
		})
	}

	result := make([]EffectiveVariable, 0, len(merged))
	for _, v := range merged {
		result = append(result, v)
	}
	return result, nil
}
