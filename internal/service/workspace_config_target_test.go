package service_test

import (
	"context"
	"testing"

	"github.com/nanohype/portal/internal/service"
)

// A working directory is a leaf in a checkout, not a string. Both executors
// resolve it the same way — the local one joins it with filepath.Join, which
// cleans, and the Kubernetes one runs `cd "/work/$PORTAL_WORKING_DIR"` in
// /bin/sh — so every spelling below lands in the same directory and drives the
// same infrastructure.
func TestCanonicalWorkingDir(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"envs/prod", "envs/prod"},
		{"./envs/prod", "envs/prod"},
		{"envs/prod/", "envs/prod"},
		{"/envs/prod", "envs/prod"},
		// The spellings the gated-twin check used to read as different targets.
		{"envs//prod", "envs/prod"},
		{"envs/./prod", "envs/prod"},
		{"envs/prod/.", "envs/prod"},
		{"envs/././prod//", "envs/prod"},
		{".//envs///./prod/.", "envs/prod"},

		// Every spelling of the repo root is the repo root.
		{".", "."},
		{"./", "."},
		{"/", "."},
		{"/./", "."},

		// Rooting before cleaning means a traversal cannot climb out of the
		// checkout even if it reaches here unvalidated.
		{"../../etc", "etc"},
		{"envs/../../../etc/passwd", "etc/passwd"},

		// Empty stays empty: an omitted field is "keep what is stored", and
		// turning it into "." would silently repoint a workspace at the root.
		{"", ""},

		// Directories that really are different stay different.
		{"envs/staging", "envs/staging"},
		{"envs/prod.old", "envs/prod.old"},
		{"envs/.hidden", "envs/.hidden"},
	}

	for _, tt := range tests {
		if got := service.CanonicalWorkingDir(tt.in); got != tt.want {
			t.Errorf("CanonicalWorkingDir(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The exploit this closes: an org operator reads the workspace list (org-wide at
// the viewer bar), finds a workspace whose applies wait for an admin, and
// creates their own on the same repo with the working directory typed a little
// differently. Both clone the same tree and cd into the same leaf, so the second
// one applies the first one's infrastructure with no approval row anywhere —
// unless the two spellings resolve to one target before anything compares them.
func TestCreateStoresACanonicalConfigTarget(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "canon-create")
	svc := service.NewWorkspaceService(testQueries, testPool, nil)

	const repo = "https://github.com/acme/infra.git"
	gated, err := svc.Create(ctx, service.CreateWorkspaceParams{
		OrgID: orgID, Name: "prod", CreatedBy: userID, Source: "vcs",
		RepoURL: repo, RepoBranch: "main", WorkingDir: "envs/prod", RequiresApproval: true,
	})
	if err != nil {
		t.Fatalf("create gated workspace: %v", err)
	}

	// The legitimate cases first, while the org holds exactly one gated
	// workspace: a genuinely different leaf in the same repo is not a twin, and
	// a workspace never matches itself however its own path is typed.
	has, err := svc.HasGatedTwin(ctx, orgID, repo, "envs/staging", "")
	if err != nil {
		t.Fatalf("HasGatedTwin: %v", err)
	}
	if has {
		t.Error("HasGatedTwin(dir=envs/staging) = true — a different directory is a different target")
	}
	has, err = svc.HasGatedTwin(ctx, orgID, repo, "envs//prod", gated.ID)
	if err != nil {
		t.Fatalf("HasGatedTwin: %v", err)
	}
	if has {
		t.Error("a workspace matched itself through a respelled working directory")
	}

	for _, spelling := range []string{"envs//prod", "envs/./prod", "envs/prod/.", "./envs//prod/"} {
		t.Run(spelling, func(t *testing.T) {
			has, err := svc.HasGatedTwin(ctx, orgID, repo, spelling, "")
			if err != nil {
				t.Fatalf("HasGatedTwin: %v", err)
			}
			if !has {
				t.Fatalf("HasGatedTwin(dir=%q) = false — %q is the same leaf as the gated workspace's envs/prod",
					spelling, spelling)
			}

			// And if one is created anyway (an admin may), the row itself holds
			// the canonical leaf, so it is not a target the next check misses.
			ws, err := svc.Create(ctx, service.CreateWorkspaceParams{
				OrgID: orgID, Name: "twin-" + spelling, CreatedBy: userID, Source: "vcs",
				RepoURL: repo, RepoBranch: "main", WorkingDir: spelling, RequiresApproval: true,
			})
			if err != nil {
				t.Fatalf("create twin: %v", err)
			}
			if ws.WorkingDir != "envs/prod" {
				t.Errorf("stored working_dir = %q, want the canonical envs/prod", ws.WorkingDir)
			}
		})
	}
}

// Update takes the same route into the same column: repointing an existing
// workspace at a gated config's leaf must land on the canonical spelling, and an
// omitted working_dir must still mean "keep what is stored".
func TestUpdateStoresACanonicalWorkingDir(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "canon-update")
	svc := service.NewWorkspaceService(testQueries, testPool, nil)

	ws, err := svc.Create(ctx, service.CreateWorkspaceParams{
		OrgID: orgID, Name: "scratch", CreatedBy: userID, Source: "vcs",
		RepoURL: "https://github.com/acme/infra.git", RepoBranch: "main", WorkingDir: "envs/dev",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	moved, err := svc.Update(ctx, service.UpdateWorkspaceParams{
		ID: ws.ID, OrgID: orgID, WorkingDir: "envs/./prod//",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if moved.WorkingDir != "envs/prod" {
		t.Fatalf("stored working_dir = %q, want the canonical envs/prod", moved.WorkingDir)
	}

	// A save that carries no working_dir keeps the stored one rather than
	// resetting the workspace to the repo root.
	kept, err := svc.Update(ctx, service.UpdateWorkspaceParams{
		ID: ws.ID, OrgID: orgID, Name: "renamed",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if kept.WorkingDir != "envs/prod" {
		t.Errorf("working_dir after a rename = %q, want it untouched at envs/prod", kept.WorkingDir)
	}
	if kept.Name != "renamed" {
		t.Errorf("name = %q, want renamed", kept.Name)
	}
}

// Upload workspaces have no repo URL to compare, so they are outside the check
// entirely — the query must not treat "no repo" as "matches every other upload".
func TestHasGatedTwinIgnoresUploadWorkspaces(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "canon-upload")
	svc := service.NewWorkspaceService(testQueries, testPool, nil)

	if _, err := svc.Create(ctx, service.CreateWorkspaceParams{
		OrgID: orgID, Name: "uploaded", CreatedBy: userID, Source: "upload",
		WorkingDir: "envs/prod", RequiresApproval: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	has, err := svc.HasGatedTwin(ctx, orgID, "", "envs/prod", "")
	if err != nil {
		t.Fatalf("HasGatedTwin: %v", err)
	}
	if has {
		t.Error("an upload workspace matched another upload workspace on working_dir alone")
	}
}
