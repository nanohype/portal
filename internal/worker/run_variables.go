package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
	"github.com/nanohype/portal/internal/worker/executor"
)

// runVariableStore is the slice of the query layer that assembling a run's
// variable set touches. It exists as an interface for the same reason
// pipelineAdvanceStore does: every read below contributes a layer of the input
// an apply executes against, and a test has to be able to fail each one
// individually to show the load refuses rather than proceeds with what it got.
// A concrete *repository.Queries puts those branches out of reach.
type runVariableStore interface {
	ListOrgVariables(ctx context.Context, orgID string) ([]repository.OrgVariable, error)
	GetPipelineRunStageByRunID(ctx context.Context, runID string) (repository.PipelineRunStage, error)
	GetPipelineRun(ctx context.Context, arg repository.GetPipelineRunParams) (repository.PipelineRun, error)
	ListPipelineVariables(ctx context.Context, arg repository.ListPipelineVariablesParams) ([]repository.PipelineVariable, error)
	ListWorkspaceVariables(ctx context.Context, arg repository.ListWorkspaceVariablesParams) ([]repository.WorkspaceVariable, error)
}

// loadRunVariables assembles the variable set a run executes against, layering
// org < pipeline < workspace.
//
// Every layer is required, and a read or a decrypt that fails is fatal to the
// run. A dropped layer is not an empty layer. It produces an apply against a
// different variable set than the one the operator approved, and nothing names
// the substitution: the run log carries no line for it, the run row records
// success, and the plan diff shows the substituted value as though it had always
// been that. What the missing variable was pinning — a region, an account id, an
// instance count — is what moves.
//
// The failed read is the only signal that exists, so it is the one that has to
// stop the run. That is the direction the workspace layer already took, and the
// direction VariableService.EffectiveVariables takes on all three layers for the
// preview an operator approves against. An executor looser than the preview that
// authorised it is the disagreement this removes.
func (w *RunJobWorker) loadRunVariables(ctx context.Context, args RunJobArgs) ([]executor.Variable, error) {
	orgVars, err := w.variables.ListOrgVariables(ctx, args.OrgID)
	if err != nil {
		return nil, fmt.Errorf("load org variables: %w", err)
	}
	orgExecVars, err := openLayer(w.encryptor, "org", orgVars, func(v repository.OrgVariable) variableRow {
		return variableRow{Key: v.Key, Value: v.Value, Sensitive: v.Sensitive, Category: v.Category}
	})
	if err != nil {
		return nil, err
	}

	pipelineExecVars, err := w.loadPipelineVariables(ctx, args)
	if err != nil {
		return nil, err
	}

	wsVars, err := w.variables.ListWorkspaceVariables(ctx, repository.ListWorkspaceVariablesParams{
		WorkspaceID: args.WorkspaceID, OrgID: args.OrgID,
	})
	if err != nil {
		return nil, fmt.Errorf("load workspace variables: %w", err)
	}
	wsExecVars, err := openLayer(w.encryptor, "workspace", wsVars, func(v repository.WorkspaceVariable) variableRow {
		return variableRow{Key: v.Key, Value: v.Value, Sensitive: v.Sensitive, Category: v.Category}
	})
	if err != nil {
		return nil, err
	}

	return mergeVariables(orgExecVars, pipelineExecVars, wsExecVars), nil
}

// loadPipelineVariables returns the pipeline layer, or nothing when the run
// belongs to no pipeline.
//
// Those two outcomes have to stay distinguishable. pgx.ErrNoRows from the stage
// lookup is the answer "no pipeline claims this run", which is the ordinary case
// for a run started from a workspace. Any other error is a failed question, and
// answering a failed question with "no pipeline" is what lets a transient
// database error drop a whole layer.
//
// The pipeline run behind a stage is a foreign key with ON DELETE CASCADE
// (migrations/000001_initial_schema.up.sql, pipeline_run_stages.pipeline_run_id),
// so a stage row cannot outlive it. pgx.ErrNoRows there means the org scope on
// the lookup did not match, which is a tenancy anomaly and not an empty layer
// either.
func (w *RunJobWorker) loadPipelineVariables(ctx context.Context, args RunJobArgs) ([]executor.Variable, error) {
	prs, err := w.variables.GetPipelineRunStageByRunID(ctx, args.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up the pipeline stage for this run: %w", err)
	}

	pr, err := w.variables.GetPipelineRun(ctx, repository.GetPipelineRunParams{ID: prs.PipelineRunID, OrgID: args.OrgID})
	if err != nil {
		return nil, fmt.Errorf("load the pipeline run for stage %s: %w", prs.ID, err)
	}

	pVars, err := w.variables.ListPipelineVariables(ctx, repository.ListPipelineVariablesParams{
		PipelineID: pr.PipelineID, OrgID: args.OrgID,
	})
	if err != nil {
		return nil, fmt.Errorf("load pipeline variables: %w", err)
	}

	return openLayer(w.encryptor, "pipeline", pVars, func(v repository.PipelineVariable) variableRow {
		return variableRow{Key: v.Key, Value: v.Value, Sensitive: v.Sensitive, Category: v.Category}
	})
}

// variableRow is the shape the org, pipeline and workspace variable tables
// share. The three row types are distinct structs with distinct columns; this is
// the intersection the executor consumes.
type variableRow struct {
	Key       string
	Value     string
	Sensitive bool
	Category  string
}

// openLayer decrypts one layer's sensitive values and converts it for the
// executor. A value that will not decrypt fails the layer, naming the key and
// the scope.
//
// The rule lives here, once, so the three layers cannot drift apart on it: an
// undecryptable variable is a variable whose value is unknown, and substituting
// its ciphertext or omitting it are both ways of running against something other
// than what was approved.
//
// A nil encryptor is the unencrypted configuration, where sensitive values were
// stored as written (VariableService.seal makes the same check on the way in).
// Non-development environments cannot reach it: config.Validate refuses to start
// without ENCRYPTION_KEY set to a non-default value.
func openLayer[T any](enc *secrets.Encryptor, scope string, rows []T, as func(T) variableRow) ([]executor.Variable, error) {
	out := make([]executor.Variable, 0, len(rows))
	for _, row := range rows {
		v := as(row)
		if v.Sensitive && enc != nil {
			decrypted, err := enc.Decrypt(v.Value)
			if err != nil {
				return nil, fmt.Errorf("decrypt %s variable %q: %w", scope, v.Key, err)
			}
			v.Value = decrypted
		}
		out = append(out, executor.Variable{Key: v.Key, Value: v.Value, Category: v.Category})
	}
	return out, nil
}
