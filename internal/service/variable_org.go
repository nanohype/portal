package service

import (
	"context"
	"fmt"
)

import "github.com/nanohype/portal/internal/repository"

// redactOrg blanks a sensitive value. Every path out of this service — response
// and audit entry alike — goes through it, so a new caller cannot forget.
func redactOrg(v repository.OrgVariable) repository.OrgVariable {
	if v.Sensitive {
		v.Value = redactedValue
	}
	return v
}

func (s *VariableService) ListOrgVariables(ctx context.Context, orgID string) ([]repository.OrgVariable, error) {
	vars, err := s.queries.ListOrgVariables(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list org variables: %w", err)
	}
	for i := range vars {
		vars[i] = redactOrg(vars[i])
	}
	return vars, nil
}

func (s *VariableService) CreateOrgVariable(ctx context.Context, orgID string, in VariableInput, actor ActorMeta) (repository.OrgVariable, error) {
	in, err := in.Normalize()
	if err != nil {
		return repository.OrgVariable{}, err
	}
	stored, err := s.seal(in)
	if err != nil {
		return repository.OrgVariable{}, err
	}

	v, err := s.queries.CreateOrgVariable(ctx, repository.CreateOrgVariableParams{
		ID: newVariableID(), OrgID: orgID,
		Key: in.Key, Value: stored, Sensitive: in.Sensitive,
		Category: in.Category, Description: in.Description,
	})
	if err != nil {
		return repository.OrgVariable{}, fmt.Errorf("create org variable: %w", err)
	}

	s.logChange(ctx, orgID, "org_variable.create", "org_variable", v.ID, nil, redactOrg(v), actor)
	return redactOrg(v), nil
}

func (s *VariableService) UpdateOrgVariable(ctx context.Context, orgID, varID string, in VariableInput, actor ActorMeta) (repository.OrgVariable, error) {
	before, err := s.queries.GetOrgVariable(ctx, repository.GetOrgVariableParams{ID: varID, OrgID: orgID})
	if err != nil {
		return repository.OrgVariable{}, fmt.Errorf("get org variable: %w", err)
	}
	in, err = in.Normalize()
	if err != nil {
		return repository.OrgVariable{}, err
	}
	stored, err := s.seal(in)
	if err != nil {
		return repository.OrgVariable{}, err
	}

	v, err := s.queries.UpdateOrgVariable(ctx, repository.UpdateOrgVariableParams{
		ID: varID, OrgID: orgID, Value: stored, Sensitive: in.Sensitive,
		Description: in.Description, Category: in.Category,
	})
	if err != nil {
		return repository.OrgVariable{}, fmt.Errorf("update org variable: %w", err)
	}

	s.logChange(ctx, orgID, "org_variable.update", "org_variable", varID, redactOrg(before), redactOrg(v), actor)
	return redactOrg(v), nil
}

func (s *VariableService) DeleteOrgVariable(ctx context.Context, orgID, varID string, actor ActorMeta) error {
	if err := s.queries.DeleteOrgVariable(ctx, repository.DeleteOrgVariableParams{ID: varID, OrgID: orgID}); err != nil {
		return fmt.Errorf("delete org variable: %w", err)
	}
	s.logChange(ctx, orgID, "org_variable.delete", "org_variable", varID, nil, nil, actor)
	return nil
}

// RevealOrgVariable returns the decrypted value, and only once the disclosure is
// recorded. The ordering is the guarantee: the audit row commits before the
// plaintext is returned, so a released secret always has a record of its
// release.
func (s *VariableService) RevealOrgVariable(ctx context.Context, orgID, varID string, actor ActorMeta) (string, error) {
	v, err := s.queries.GetOrgVariable(ctx, repository.GetOrgVariableParams{ID: varID, OrgID: orgID})
	if err != nil {
		return "", fmt.Errorf("get org variable: %w", err)
	}
	plain, err := s.open(v.Value, v.Sensitive)
	if err != nil {
		return "", err
	}
	if err := s.recordDisclosure(ctx, orgID, "org_variable.reveal", "org_variable", varID, actor); err != nil {
		return "", fmt.Errorf("record disclosure: %w", err)
	}
	return plain, nil
}
