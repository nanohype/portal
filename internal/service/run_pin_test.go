package service_test

import (
	"context"
	"testing"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

// A run is one execution of one tree, not a pointer at whatever the workspace
// says when the job starts. RunService.Create resolves the workspace's config
// once and freezes it on the row; the worker runs that. Without the freeze, an
// approval signs a plan diff and releases an apply of something else — both
// levers that would swap it (PUT /workspaces/{id}, POST /workspaces/{id}/upload)
// sit at the operator bar.
func TestRunServicePinsTheWorkspaceConfig(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	svc := service.NewRunService(testQueries, testPool, nil)
	orgID, userID := seedOrg(t, ctx, "runpin")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,repo_url,repo_branch,working_dir,tofu_version)
		 VALUES ($1,$2,$3,$4,'vcs','https://example.test/infra.git','main','envs/production','1.11.0')`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := svc.Create(ctx, service.CreateRunParams{
		WorkspaceID: wsID, OrgID: orgID, Operation: "plan", CreatedBy: userID,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	want := repository.Run{
		ConfigSource: "vcs", ConfigRepoURL: "https://example.test/infra.git",
		ConfigRepoBranch: "main", ConfigWorkingDir: "envs/production", ConfigTofuVersion: "1.11.0",
	}
	if run.ConfigSource != want.ConfigSource || run.ConfigRepoURL != want.ConfigRepoURL ||
		run.ConfigRepoBranch != want.ConfigRepoBranch || run.ConfigWorkingDir != want.ConfigWorkingDir ||
		run.ConfigTofuVersion != want.ConfigTofuVersion {
		t.Fatalf("run config = %+v, want the workspace's config %+v", run, want)
	}

	// Editing the workspace afterwards does not reach the run that already exists.
	exec(t, ctx, `UPDATE workspaces SET repo_branch='attacker', working_dir='.' WHERE id=$1`, wsID)
	stored, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if stored.ConfigRepoBranch != "main" || stored.ConfigWorkingDir != "envs/production" {
		t.Errorf("stored run config = (%q, %q), want (main, envs/production)", stored.ConfigRepoBranch, stored.ConfigWorkingDir)
	}

	// And the edit is not lost — it is what the NEXT run executes. Repointing a
	// workspace is ordinary operator work; the pin only decides which run it
	// takes effect on.
	next, err := svc.Create(ctx, service.CreateRunParams{
		WorkspaceID: wsID, OrgID: orgID, Operation: "plan", CreatedBy: userID,
	})
	if err != nil {
		t.Fatalf("create second run: %v", err)
	}
	if next.ConfigRepoBranch != "attacker" || next.ConfigWorkingDir != "." {
		t.Errorf("next run config = (%q, %q), want the edited workspace's (attacker, .)",
			next.ConfigRepoBranch, next.ConfigWorkingDir)
	}
}

// An upload workspace carries only its current config version, so the same rule
// applies to the archive: the run holds the version it was created against.
func TestRunServicePinsTheUploadedConfigVersion(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	svc := service.NewRunService(testQueries, testPool, nil)
	orgID, userID := seedOrg(t, ctx, "runpin-upload")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,current_config_version_id)
		 VALUES ($1,$2,$3,$4,'upload','cfg-signed')`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := svc.Create(ctx, service.CreateRunParams{
		WorkspaceID: wsID, OrgID: orgID, Operation: "plan", CreatedBy: userID,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.ConfigSource != "upload" || run.ConfigVersionID != "cfg-signed" {
		t.Fatalf("run config = (%q, %q), want (upload, cfg-signed)", run.ConfigSource, run.ConfigVersionID)
	}

	exec(t, ctx, `UPDATE workspaces SET current_config_version_id='cfg-attacker' WHERE id=$1`, wsID)
	stored, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if stored.ConfigVersionID != "cfg-signed" {
		t.Errorf("stored config version = %q, want cfg-signed", stored.ConfigVersionID)
	}
}

// A run against a workspace that is not this org's is not a run with an empty
// config — it is not a run at all.
func TestRunServiceRefusesAnUnknownWorkspace(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	svc := service.NewRunService(testQueries, testPool, nil)
	orgID, userID := seedOrg(t, ctx, "runpin-cross")
	otherOrg, otherUser := seedOrg(t, ctx, "runpin-cross-other")
	wsID := seedWorkspace(t, ctx, otherOrg, otherUser)

	if _, err := svc.Create(ctx, service.CreateRunParams{
		WorkspaceID: wsID, OrgID: orgID, Operation: "plan", CreatedBy: userID,
	}); err == nil {
		t.Fatal("creating a run on another org's workspace must fail")
	}
}
