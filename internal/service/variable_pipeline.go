package service

import (
	"context"
	"fmt"

	"github.com/nanohype/portal/internal/repository"
)

func redactPipeline(v repository.PipelineVariable) repository.PipelineVariable {
	if v.Sensitive {
		v.Value = redactedValue
	}
	return v
}

func (s *VariableService) ListPipelineVariables(ctx context.Context, orgID, pipelineID string) ([]repository.PipelineVariable, error) {
	vars, err := s.queries.ListPipelineVariables(ctx, repository.ListPipelineVariablesParams{PipelineID: pipelineID, OrgID: orgID})
	if err != nil {
		return nil, fmt.Errorf("list pipeline variables: %w", err)
	}
	for i := range vars {
		vars[i] = redactPipeline(vars[i])
	}
	return vars, nil
}

func (s *VariableService) CreatePipelineVariable(ctx context.Context, orgID, pipelineID string, in VariableInput, actor ActorMeta) (repository.PipelineVariable, error) {
	in, err := in.Normalize()
	if err != nil {
		return repository.PipelineVariable{}, err
	}
	stored, err := s.seal(in)
	if err != nil {
		return repository.PipelineVariable{}, err
	}

	v, err := s.queries.CreatePipelineVariable(ctx, repository.CreatePipelineVariableParams{
		ID: newVariableID(), PipelineID: pipelineID, OrgID: orgID,
		Key: in.Key, Value: stored, Sensitive: in.Sensitive,
		Category: in.Category, Description: in.Description,
	})
	if err != nil {
		return repository.PipelineVariable{}, fmt.Errorf("create pipeline variable: %w", err)
	}

	s.logChange(ctx, orgID, "pipeline_variable.create", "pipeline_variable", v.ID, nil, redactPipeline(v), actor)
	return redactPipeline(v), nil
}

func (s *VariableService) UpdatePipelineVariable(ctx context.Context, orgID, pipelineID, varID string, in VariableInput, actor ActorMeta) (repository.PipelineVariable, error) {
	before, err := s.queries.GetPipelineVariable(ctx, repository.GetPipelineVariableParams{ID: varID, PipelineID: pipelineID, OrgID: orgID})
	if err != nil {
		return repository.PipelineVariable{}, fmt.Errorf("get pipeline variable: %w", err)
	}
	in, err = in.Normalize()
	if err != nil {
		return repository.PipelineVariable{}, err
	}
	stored, err := s.seal(in)
	if err != nil {
		return repository.PipelineVariable{}, err
	}

	v, err := s.queries.UpdatePipelineVariable(ctx, repository.UpdatePipelineVariableParams{
		ID: varID, PipelineID: pipelineID, OrgID: orgID, Value: stored, Sensitive: in.Sensitive,
		Description: in.Description, Category: in.Category,
	})
	if err != nil {
		return repository.PipelineVariable{}, fmt.Errorf("update pipeline variable: %w", err)
	}

	s.logChange(ctx, orgID, "pipeline_variable.update", "pipeline_variable", varID, redactPipeline(before), redactPipeline(v), actor)
	return redactPipeline(v), nil
}

func (s *VariableService) DeletePipelineVariable(ctx context.Context, orgID, pipelineID, varID string, actor ActorMeta) error {
	if _, err := s.queries.DeletePipelineVariable(ctx, repository.DeletePipelineVariableParams{ID: varID, PipelineID: pipelineID, OrgID: orgID}); err != nil {
		return fmt.Errorf("delete pipeline variable: %w", err)
	}
	s.logChange(ctx, orgID, "pipeline_variable.delete", "pipeline_variable", varID, nil, nil, actor)
	return nil
}

func (s *VariableService) RevealPipelineVariable(ctx context.Context, orgID, pipelineID, varID string, actor ActorMeta) (string, error) {
	v, err := s.queries.GetPipelineVariable(ctx, repository.GetPipelineVariableParams{ID: varID, PipelineID: pipelineID, OrgID: orgID})
	if err != nil {
		return "", fmt.Errorf("get pipeline variable: %w", err)
	}
	plain, err := s.open(v.Value, v.Sensitive)
	if err != nil {
		return "", err
	}
	if err := s.recordDisclosure(ctx, orgID, "pipeline_variable.reveal", "pipeline_variable", varID, actor); err != nil {
		return "", fmt.Errorf("record disclosure: %w", err)
	}
	return plain, nil
}
