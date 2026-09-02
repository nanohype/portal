package worker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
	"github.com/nanohype/portal/internal/worker/executor"
)

// A run executes against the variable set these reads assemble. A read that
// fails and is treated as an empty layer produces an apply against a smaller set
// than the operator approved, with no run-log line, no failed run and no plan
// entry naming the substitution — the failure is invisible in every surface that
// exists to catch it.
//
// Each test below plants one such failure and requires the load to refuse. The
// layers are enumerated from the reads runVariableStore declares rather than from
// whatever these tests happen to catch, so a read added to that interface without
// a refusal alongside it fails here.

type stubVariables struct {
	orgVars []repository.OrgVariable
	failOrg error

	stage     repository.PipelineRunStage
	failStage error

	pipelineRun  repository.PipelineRun
	failPipeline error

	pipelineVars     []repository.PipelineVariable
	failPipelineVars error

	workspaceVars []repository.WorkspaceVariable
	failWorkspace error
}

func (s *stubVariables) ListOrgVariables(context.Context, string) ([]repository.OrgVariable, error) {
	return s.orgVars, s.failOrg
}
func (s *stubVariables) GetPipelineRunStageByRunID(context.Context, string) (repository.PipelineRunStage, error) {
	return s.stage, s.failStage
}
func (s *stubVariables) GetPipelineRun(context.Context, repository.GetPipelineRunParams) (repository.PipelineRun, error) {
	return s.pipelineRun, s.failPipeline
}
func (s *stubVariables) ListPipelineVariables(context.Context, repository.ListPipelineVariablesParams) ([]repository.PipelineVariable, error) {
	return s.pipelineVars, s.failPipelineVars
}
func (s *stubVariables) ListWorkspaceVariables(context.Context, repository.ListWorkspaceVariablesParams) ([]repository.WorkspaceVariable, error) {
	return s.workspaceVars, s.failWorkspace
}

// notInAPipeline is the ordinary case: a run started from a workspace, which no
// pipeline stage claims. Every test starts here and plants one failure on top,
// so a refusal can only come from the thing that test planted.
func notInAPipeline() *stubVariables {
	return &stubVariables{failStage: pgx.ErrNoRows}
}

const testEncryptionKey = "0123456789abcdef0123456789abcdef" // 32 bytes, AES-256

func testEncryptor(t *testing.T) *secrets.Encryptor {
	t.Helper()
	enc, err := secrets.NewEncryptor(testEncryptionKey)
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	return enc
}

func loadVars(t *testing.T, st *stubVariables, enc *secrets.Encryptor) ([]executor.Variable, error) {
	t.Helper()
	w := &RunJobWorker{variables: st, encryptor: enc}
	return w.loadRunVariables(context.Background(), RunJobArgs{
		RunID: "run_1", WorkspaceID: "ws_1", OrgID: "org_1", Operation: "apply",
	})
}

// refuses requires that the load failed and that the cause names the planted
// error. A refusal that loses the cause leaves an operator with a failed run and
// no way to tell which layer went missing.
func refuses(t *testing.T, st *stubVariables, enc *secrets.Encryptor, wantIn string) {
	t.Helper()
	vars, err := loadVars(t, st, enc)
	if err == nil {
		t.Fatalf("the load returned %d variables and no error; the run would have executed against a set it could not fully read", len(vars))
	}
	if !strings.Contains(err.Error(), wantIn) {
		t.Errorf("error does not name the cause %q, so the layer that went missing is not identifiable: %v", wantIn, err)
	}
}

// ── the five reads ─────────────────────────────────────────────────────────

// planted returns a store in which exactly one read fails, named by the
// runVariableStore method that fails. The stage lookup answers pgx.ErrNoRows
// everywhere else, so a refusal can only come from the planted failure.
func planted(method string, boom error) *stubVariables {
	inPipeline := func(s *stubVariables) *stubVariables {
		s.failStage = nil
		s.stage = repository.PipelineRunStage{ID: "stage_1", PipelineRunID: "pr_1"}
		s.pipelineRun = repository.PipelineRun{ID: "pr_1", PipelineID: "pl_1"}
		return s
	}
	st := notInAPipeline()
	switch method {
	case "ListOrgVariables":
		st.failOrg = boom
	case "GetPipelineRunStageByRunID":
		st.failStage = boom
	case "GetPipelineRun":
		st = inPipeline(st)
		st.failPipeline = boom
	case "ListPipelineVariables":
		st = inPipeline(st)
		st.failPipelineVars = boom
	case "ListWorkspaceVariables":
		st.failWorkspace = boom
	default:
		return nil
	}
	return st
}

// Every read runVariableStore declares is a layer of the set an apply executes
// against, so every one of them has to stop the run when it fails. The table is
// checked against the interface by reflection: a read added without a row here
// fails this test rather than waiting for a reviewer to notice.
func TestLoadRunVariables_RefusesWhenAnyLayerCannotBeRead(t *testing.T) {
	iface := reflect.TypeOf((*runVariableStore)(nil)).Elem()

	for i := range iface.NumMethod() {
		method := iface.Method(i).Name
		t.Run(method, func(t *testing.T) {
			st := planted(method, errors.New("connection reset by peer"))
			if st == nil {
				t.Fatalf("runVariableStore declares %s and no case plants a failure for it, so nothing shows the run refuses when that layer cannot be read", method)
			}
			refuses(t, st, nil, "connection reset by peer")
		})
	}
}

// pipeline_run_stages.pipeline_run_id is NOT NULL REFERENCES pipeline_runs(id)
// ON DELETE CASCADE, so a stage cannot outlive its run. pgx.ErrNoRows here means
// the org scope on the lookup did not match, which is a tenancy anomaly rather
// than an empty layer.
func TestLoadRunVariables_RefusesWhenThePipelineRunIsMissing(t *testing.T) {
	st := planted("GetPipelineRun", pgx.ErrNoRows)
	refuses(t, st, nil, "pipeline run")
}

// ── the three decrypt paths ────────────────────────────────────────────────

// An undecryptable value is a value whose contents are unknown. Skipping it runs
// the apply without that variable; passing the ciphertext through runs it with a
// wrong one. Both are the substitution this refuses.

func TestLoadRunVariables_RefusesAnUndecryptableOrgVariable(t *testing.T) {
	st := notInAPipeline()
	st.orgVars = []repository.OrgVariable{
		{Key: "aws_account_id", Value: "not valid base64!!!", Sensitive: true, Category: "terraform"},
	}
	refuses(t, st, testEncryptor(t), `decrypt org variable "aws_account_id"`)
}

func TestLoadRunVariables_RefusesAnUndecryptablePipelineVariable(t *testing.T) {
	st := notInAPipeline()
	st.failStage = nil
	st.stage = repository.PipelineRunStage{ID: "stage_1", PipelineRunID: "pr_1"}
	st.pipelineRun = repository.PipelineRun{ID: "pr_1", PipelineID: "pl_1"}
	st.pipelineVars = []repository.PipelineVariable{
		{Key: "aws_account_id", Value: "not valid base64!!!", Sensitive: true, Category: "terraform"},
	}
	refuses(t, st, testEncryptor(t), `decrypt pipeline variable "aws_account_id"`)
}

func TestLoadRunVariables_RefusesAnUndecryptableWorkspaceVariable(t *testing.T) {
	st := notInAPipeline()
	st.workspaceVars = []repository.WorkspaceVariable{
		{Key: "aws_account_id", Value: "not valid base64!!!", Sensitive: true, Category: "terraform"},
	}
	refuses(t, st, testEncryptor(t), `decrypt workspace variable "aws_account_id"`)
}

// A key rotated out from under one layer must not be answered by another layer's
// value. Without the refusal the workspace value survives and the run proceeds
// with the org default silently absent.
func TestLoadRunVariables_RefusesEvenWhenAnotherLayerCoversTheKey(t *testing.T) {
	enc := testEncryptor(t)
	sealed, err := enc.Encrypt("us-west-2")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	st := notInAPipeline()
	st.orgVars = []repository.OrgVariable{
		{Key: "region", Value: "not valid base64!!!", Sensitive: true, Category: "terraform"},
	}
	st.workspaceVars = []repository.WorkspaceVariable{
		{Key: "region", Value: sealed, Sensitive: true, Category: "terraform"},
	}
	refuses(t, st, enc, `decrypt org variable "region"`)
}

// ── the layering the refusals must not have broken ─────────────────────────

func TestLoadRunVariables_LayersOrgUnderPipelineUnderWorkspace(t *testing.T) {
	st := &stubVariables{
		stage:       repository.PipelineRunStage{ID: "stage_1", PipelineRunID: "pr_1"},
		pipelineRun: repository.PipelineRun{ID: "pr_1", PipelineID: "pl_1"},
		orgVars: []repository.OrgVariable{
			{Key: "region", Value: "us-east-1", Category: "terraform"},
			{Key: "org_only", Value: "kept", Category: "terraform"},
		},
		pipelineVars: []repository.PipelineVariable{
			{Key: "region", Value: "us-east-2", Category: "terraform"},
			{Key: "pipeline_only", Value: "kept", Category: "terraform"},
		},
		workspaceVars: []repository.WorkspaceVariable{
			{Key: "region", Value: "us-west-2", Category: "terraform"},
		},
	}

	vars, err := loadVars(t, st, nil)
	if err != nil {
		t.Fatalf("loadRunVariables: %v", err)
	}

	got := map[string]string{}
	for _, v := range vars {
		got[v.Key] = v.Value
	}
	want := map[string]string{"region": "us-west-2", "org_only": "kept", "pipeline_only": "kept"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("merged %d keys, want %d: %v", len(got), len(want), got)
	}
}

// A run outside a pipeline is the ordinary case and must not be refused: the
// stage lookup answering pgx.ErrNoRows is an answer, not a failure.
func TestLoadRunVariables_AcceptsARunThatBelongsToNoPipeline(t *testing.T) {
	st := notInAPipeline()
	st.orgVars = []repository.OrgVariable{{Key: "region", Value: "us-east-1", Category: "terraform"}}

	vars, err := loadVars(t, st, nil)
	if err != nil {
		t.Fatalf("a run started from a workspace was refused: %v", err)
	}
	if len(vars) != 1 || vars[0].Key != "region" {
		t.Errorf("got %v, want the single org variable", vars)
	}
}

// The unencrypted configuration stores sensitive values as written. It is
// reachable only in development — config.Validate refuses to start elsewhere
// without ENCRYPTION_KEY — and there the value must pass through untouched
// rather than be dropped as undecryptable.
func TestLoadRunVariables_PassesSensitiveValuesThroughWithoutAnEncryptor(t *testing.T) {
	st := notInAPipeline()
	st.workspaceVars = []repository.WorkspaceVariable{
		{Key: "token", Value: "plaintext-in-dev", Sensitive: true, Category: "env"},
	}

	vars, err := loadVars(t, st, nil)
	if err != nil {
		t.Fatalf("loadRunVariables: %v", err)
	}
	if len(vars) != 1 || vars[0].Value != "plaintext-in-dev" {
		t.Errorf("got %v, want the stored value unchanged", vars)
	}
}
