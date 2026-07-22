package worker

import (
	"context"
	"testing"

	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/logstream"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker/executor"
)

// recordingExecutor captures the parameters the worker builds and reports a
// clean result, so a test can assert on what would have been checked out and run
// without running anything.
type recordingExecutor struct {
	params executor.ExecuteParams
	called bool
}

func (e *recordingExecutor) Execute(_ context.Context, params executor.ExecuteParams) (*executor.ExecuteResult, error) {
	e.params = params
	e.called = true
	params.LogCallback([]byte("recorded\n"))
	return &executor.ExecuteResult{Output: "recorded"}, nil
}

func newTestRunWorker(exec executor.Executor) *RunJobWorker {
	return NewRunJobWorker(testQueries, exec, logstream.NewMemoryStreamer(), nil, nil)
}

// The apply that follows an approval is a fresh execution — a new checkout, a
// new process, no saved plan file replayed. What it executes therefore has to be
// decided when the run is created, not when the job starts, or the signature
// covers a plan the worker never runs.
//
// Both levers that would move it sit at the operator bar the workspace routes
// use: PUT /workspaces/{id} (repo, branch, working dir) and
// POST /workspaces/{id}/upload (a new config archive). The exploit is: park a
// plan on a gated workspace, repoint the workspace, let an admin approve the
// diff they were shown, and the apply runs the attacker's tree.
func TestRunJobExecutesThePinnedConfigNotTheLiveWorkspace(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "pin")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,repo_url,repo_branch,working_dir,tofu_version,requires_approval)
		 VALUES ($1,$2,$3,$4,'vcs','https://example.test/infra.git','main','envs/prod','1.11.0',TRUE)`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "pending", CreatedBy: userID,
		ConfigSource:      "vcs",
		ConfigRepoURL:     "https://example.test/infra.git",
		ConfigRepoBranch:  "main",
		ConfigWorkingDir:  "envs/prod",
		ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// The window: an operator repoints the workspace while the plan waits for a
	// signature. Every field the executor would clone from moves.
	exec(t, ctx,
		`UPDATE workspaces SET repo_url='https://example.test/attacker.git', repo_branch='attacker',
		 working_dir='.', tofu_version='1.6.0' WHERE id=$1`, wsID)

	rec := &recordingExecutor{}
	w := newTestRunWorker(rec)
	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "apply"},
	}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if !rec.called {
		t.Fatal("executor was never invoked")
	}

	if rec.params.RepoURL != "https://example.test/infra.git" {
		t.Errorf("RepoURL = %q, want the repo the run was created against", rec.params.RepoURL)
	}
	if rec.params.RepoBranch != "main" {
		t.Errorf("RepoBranch = %q, want main — the branch the plan was produced from", rec.params.RepoBranch)
	}
	if rec.params.WorkingDir != "envs/prod" {
		t.Errorf("WorkingDir = %q, want envs/prod", rec.params.WorkingDir)
	}
	if rec.params.TofuVersion != "1.11.0" {
		t.Errorf("TofuVersion = %q, want 1.11.0", rec.params.TofuVersion)
	}
	if rec.params.Source != "vcs" {
		t.Errorf("Source = %q, want vcs", rec.params.Source)
	}
}

// The upload variant of the same exploit: swap the archive instead of the branch.
// An upload workspace stores only its current config version, so an apply that
// read the workspace would run whatever was uploaded last — including something
// uploaded after the plan an admin signed.
func TestRunJobExecutesThePinnedConfigVersionForUploads(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "pin-upload")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,current_config_version_id,requires_approval)
		 VALUES ($1,$2,$3,$4,'upload','cfg-signed',TRUE)`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "pending", CreatedBy: userID,
		ConfigSource:    "upload",
		ConfigVersionID: "cfg-signed",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	exec(t, ctx, `UPDATE workspaces SET current_config_version_id='cfg-attacker' WHERE id=$1`, wsID)

	rec := &recordingExecutor{}
	w := newTestRunWorker(rec)
	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "apply"},
	}); err != nil {
		t.Fatalf("work: %v", err)
	}

	if rec.params.Source != "upload" {
		t.Errorf("Source = %q, want upload", rec.params.Source)
	}
	// Storage is nil here, so no archive is fetched; the run row is what decides
	// which one would have been. Assert on the row the worker read.
	after, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if after.ConfigVersionID != "cfg-signed" {
		t.Errorf("run config version = %q, want cfg-signed", after.ConfigVersionID)
	}
}

// The gate decision after a plan is a question about the workspace as it stands
// now — is this workspace one where applies wait for a human — so that one still
// reads the live row. Only the tree to execute is frozen.
func TestRunJobReadsTheGateFromTheLiveWorkspace(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "pin-gate")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,repo_url,repo_branch,working_dir,requires_approval)
		 VALUES ($1,$2,$3,$4,'vcs','https://example.test/infra.git','main','.',FALSE)`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "pending", CreatedBy: userID,
		ConfigSource: "vcs", ConfigRepoURL: "https://example.test/infra.git",
		ConfigRepoBranch: "main", ConfigWorkingDir: ".",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	// An admin gates the workspace after the plan was started.
	exec(t, ctx, `UPDATE workspaces SET requires_approval=TRUE WHERE id=$1`, wsID)

	w := newTestRunWorker(&recordingExecutor{})
	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "plan"},
	}); err != nil {
		t.Fatalf("work: %v", err)
	}

	after, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if after.Status != "awaiting_approval" {
		t.Errorf("run status = %q, want awaiting_approval — the gate is read live", after.Status)
	}
}

// A run row with no pinned configuration cannot be executed: the only fallback
// available is the live workspace, which is what the pin exists to stop the
// worker reading. It fails the run instead of guessing.
func TestRunJobRefusesAnUnpinnedRun(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "pin-missing")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,repo_url,repo_branch)
		 VALUES ($1,$2,$3,$4,'vcs','https://example.test/infra.git','main')`,
		wsID, orgID, "ws-"+wsID, userID)

	runID := id()
	exec(t, ctx,
		`INSERT INTO runs (id,workspace_id,org_id,operation,status,created_by) VALUES ($1,$2,$3,'apply','pending',$4)`,
		runID, wsID, orgID, userID)

	rec := &recordingExecutor{}
	w := newTestRunWorker(rec)
	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: runID, WorkspaceID: wsID, OrgID: orgID, Operation: "apply"},
	}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if rec.called {
		t.Fatal("an unpinned run must not execute")
	}
	var status, message string
	if err := testPool.QueryRow(ctx, `SELECT status, error_message FROM runs WHERE id=$1`, runID).Scan(&status, &message); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status != "errored" {
		t.Errorf("run status = %q, want errored", status)
	}
	if message == "" {
		t.Error("an unpinned run must say why it failed")
	}
}
