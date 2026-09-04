package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

// A stage or an operator importing outputs gets back the list of what landed.
// That list alone cannot separate a partial import from a source that published
// fewer outputs, so an upsert failure has to come back as one: a target variable
// set smaller than the source's is what a pipeline stage then plans against.
//
// One unwritable output must not cost the others, so the loop finishes and
// reports what it could not write.

// stateAt returns a state document with the given outputs, which is what the
// object store hands ImportOutputs.
type fakeState struct{ body []byte }

func (f fakeState) GetState(context.Context, string) ([]byte, error) { return f.body, nil }

func seedStateVersion(t *testing.T, ctx context.Context, wsID, orgID, runID string) {
	t.Helper()
	if _, err := testQueries.CreateStateVersion(ctx, repository.CreateStateVersionParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, RunID: runID, Serial: 1,
		StateURL: "state/" + wsID + "/1.tfstate", ResourceCount: 0, ResourceSummary: "+0 ~0 -0",
	}); err != nil {
		t.Fatalf("seed state version: %v", err)
	}
}

// A NUL byte is the reachable per-output write failure: Postgres text cannot
// hold one, and a state output is free to carry one. It fails the write for that
// output and nothing else, which is exactly the shape the loop has to handle.
const stateWithOneUnwritableOutput = `{
  "version": 4,
  "outputs": {
    "vpc_id":   {"value": "vpc-0abc", "type": "string"},
    "bad_name": {"value": "carries a \u0000 byte", "type": "string"},
    "region":   {"value": "us-west-2", "type": "string"}
  }
}`

func TestImportOutputs_ReportsOutputsItCouldNotWrite(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "impout")
	src := seedWorkspace(t, ctx, orgID, userID)
	dst := seedWorkspace(t, ctx, orgID, userID)
	runID := seedPlannedRun(t, ctx, src, orgID, userID)
	seedStateVersion(t, ctx, src, orgID, runID)

	svc := service.NewWorkspaceServiceWithState(testQueries, testPool, fakeState{body: []byte(stateWithOneUnwritableOutput)})

	imported, _, err := svc.ImportOutputs(ctx, service.ImportOutputsParams{
		SourceWorkspaceID: src, TargetWorkspaceID: dst, OrgID: orgID, DescriptionSource: "pipeline stage",
	})
	if err == nil {
		t.Fatal("an output that could not be written was skipped in silence; a stage would plan against a variable set smaller than the source published")
	}
	for _, want := range []string{"2 of 3", "bad_name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say what was lost — missing %q: %v", want, err)
		}
	}

	// One bad output must not cost the others.
	if len(imported) != 2 {
		t.Errorf("imported %d outputs, want the 2 that were writable", len(imported))
	}
	got := listVarMap(t, ctx, dst, orgID)
	if got["vpc_id"] != "vpc-0abc" || got["region"] != "us-west-2" {
		t.Errorf("the writable outputs did not land: %v", got)
	}
}

// A clean import returns no error, or the report above means nothing.
func TestImportOutputs_ReportsNothingWhenEveryOutputLands(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "impoutok")
	src := seedWorkspace(t, ctx, orgID, userID)
	dst := seedWorkspace(t, ctx, orgID, userID)
	runID := seedPlannedRun(t, ctx, src, orgID, userID)
	seedStateVersion(t, ctx, src, orgID, runID)

	clean := `{"version":4,"outputs":{"vpc_id":{"value":"vpc-0abc","type":"string"},"region":{"value":"us-west-2","type":"string"}}}`
	svc := service.NewWorkspaceServiceWithState(testQueries, testPool, fakeState{body: []byte(clean)})

	imported, _, err := svc.ImportOutputs(ctx, service.ImportOutputsParams{
		SourceWorkspaceID: src, TargetWorkspaceID: dst, OrgID: orgID, DescriptionSource: "pipeline stage",
	})
	if err != nil {
		t.Fatalf("a clean import reported a failure: %v", err)
	}
	if len(imported) != 2 {
		t.Errorf("imported %d outputs, want 2", len(imported))
	}
}
