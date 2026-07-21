package handler

import (
	"testing"

	"github.com/nanohype/portal/internal/repository"
)

func boolPtr(b bool) *bool { return &b }

// The settings form posts every field on every save, so "the request carries a
// value" is not the same as "the request changes the approval gate".
func TestChangesApprovalGate(t *testing.T) {
	current := repository.Workspace{AutoApply: false, RequiresApproval: true}

	tests := []struct {
		name string
		req  UpdateWorkspaceRequest
		want bool
	}{
		{"no approval fields submitted", UpdateWorkspaceRequest{Name: "renamed"}, false},
		{"resubmits the stored values", UpdateWorkspaceRequest{
			AutoApply: boolPtr(false), RequiresApproval: boolPtr(true),
		}, false},
		{"turns auto_apply on", UpdateWorkspaceRequest{AutoApply: boolPtr(true)}, true},
		{"turns requires_approval off", UpdateWorkspaceRequest{RequiresApproval: boolPtr(false)}, true},
		{"flips both", UpdateWorkspaceRequest{
			AutoApply: boolPtr(true), RequiresApproval: boolPtr(false),
		}, true},
		{"changes other fields alongside stored approval values", UpdateWorkspaceRequest{
			RepoURL: "https://example.test/repo.git", RepoBranch: "main",
			AutoApply: boolPtr(false), RequiresApproval: boolPtr(true),
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := changesApprovalGate(current, tt.req); got != tt.want {
				t.Errorf("changesApprovalGate = %v, want %v", got, tt.want)
			}
		})
	}
}

// The route lets operators edit workspace settings. Removing the approval an
// admin would otherwise have to sign is the one part of that form they may not
// touch — and everything else on it must still go through.
func TestApprovalGateChangeAllowed(t *testing.T) {
	current := repository.Workspace{AutoApply: false, RequiresApproval: true}
	disableGate := UpdateWorkspaceRequest{AutoApply: boolPtr(true), RequiresApproval: boolPtr(false)}
	renameOnly := UpdateWorkspaceRequest{Name: "renamed", AutoApply: boolPtr(false), RequiresApproval: boolPtr(true)}

	tests := []struct {
		name string
		req  UpdateWorkspaceRequest
		role string
		want bool
	}{
		{"operator cannot disable the approval gate", disableGate, "operator", false},
		{"viewer cannot disable the approval gate", disableGate, "viewer", false},
		{"unknown role cannot disable the approval gate", disableGate, "intern", false},
		{"admin can disable the approval gate", disableGate, "admin", true},
		{"owner can disable the approval gate", disableGate, "owner", true},
		{"operator can still edit everything else", renameOnly, "operator", true},
		{"viewer request untouched by this check", renameOnly, "viewer", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approvalGateChangeAllowed(current, tt.req, tt.role); got != tt.want {
				t.Errorf("approvalGateChangeAllowed(role=%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}

// The guard on Update is only worth something if creation carries it too.
// Otherwise the workaround is to skip the update: create the workspace with the
// gate already open.
func TestApprovalGateAtCreateAllowed(t *testing.T) {
	tests := []struct {
		name      string
		autoApply bool
		role      string
		want      bool
	}{
		{"operator creates a normal workspace", false, "operator", true},
		{"viewer creates a normal workspace", false, "viewer", true},
		{"unknown role creates a normal workspace", false, "intern", true},

		{"operator cannot create an auto-applying workspace", true, "operator", false},
		{"viewer cannot create an auto-applying workspace", true, "viewer", false},
		{"unknown role cannot create an auto-applying workspace", true, "intern", false},
		{"admin can create an auto-applying workspace", true, "admin", true},
		{"owner can create an auto-applying workspace", true, "owner", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approvalGateAtCreateAllowed(tt.autoApply, tt.role); got != tt.want {
				t.Errorf("approvalGateAtCreateAllowed(autoApply=%v, role=%q) = %v, want %v",
					tt.autoApply, tt.role, got, tt.want)
			}
		})
	}
}

// A workspace that requires approval must not be reached by asking for an
// operation that changes live infrastructure or state directly — those paths
// create no approval row at all.
func TestRequiresApprovalGate(t *testing.T) {
	gated := repository.Workspace{RequiresApproval: true}
	ungated := repository.Workspace{RequiresApproval: false}

	tests := []struct {
		name      string
		workspace repository.Workspace
		operation string
		want      bool
	}{
		{"apply on a gated workspace", gated, "apply", true},
		{"destroy on a gated workspace", gated, "destroy", true},

		// test is not a dry run: the executors chmod +x smoke-test.sh from the
		// checked-out repo and run it with the run's decrypted environment and
		// the executor's cloud identity, and the worker records the result as
		// "applied". Repointing a workspace at another repo sits at the
		// operator bar, so an ungated test is a way to run anything on a
		// workspace whose whole point is that nothing runs unsigned.
		{"test on a gated workspace", gated, "test", true},

		// import writes a new state version — it decides which real resources
		// this config claims.
		{"import on a gated workspace", gated, "import", true},

		// A plan changes nothing live, and it is the only way an operator
		// reaches the approval. Gating it would leave gated workspaces with no
		// operator workflow at all.
		{"plan on a gated workspace", gated, "plan", false},

		// A workspace with no gate keeps the operator workflow untouched.
		{"apply on an ungated workspace", ungated, "apply", false},
		{"destroy on an ungated workspace", ungated, "destroy", false},
		{"plan on an ungated workspace", ungated, "plan", false},
		{"test on an ungated workspace", ungated, "test", false},
		{"import on an ungated workspace", ungated, "import", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiresApprovalGate(tt.workspace, tt.operation); got != tt.want {
				t.Errorf("requiresApprovalGate(requires_approval=%v, %q) = %v, want %v",
					tt.workspace.RequiresApproval, tt.operation, got, tt.want)
			}
		})
	}
}

// requires_approval protects the infrastructure a config manages, not the row
// that names it. Two workspaces on the same repo + working_dir drive the same
// backend, so an ungated second one is a second door onto the gated
// workspace's own resources — opened at the operator bar the create, clone and
// update routes sit at.
func TestGatedTwinAllowed(t *testing.T) {
	tests := []struct {
		name             string
		hasGatedTwin     bool
		requiresApproval bool
		role             string
		want             bool
	}{
		// Nothing gates this config: every role proceeds untouched.
		{"operator, no gated twin, ungated", false, false, "operator", true},
		{"viewer, no gated twin, ungated", false, false, "viewer", true},
		{"operator, no gated twin, gated", false, true, "operator", true},

		// The exploit: an ungated twin of a gated config at the operator bar.
		{"operator cannot add an ungated twin", true, false, "operator", false},
		{"viewer cannot add an ungated twin", true, false, "viewer", false},
		{"unknown role cannot add an ungated twin", true, false, "intern", false},

		// Carrying the gate is always open to an operator — requires_approval
		// only ever adds a wait.
		{"operator may add a twin that is gated too", true, true, "operator", true},
		{"viewer may add a twin that is gated too", true, true, "viewer", true},

		// Whoever may release a gated apply may also stand up an ungated twin.
		{"admin may add an ungated twin", true, false, "admin", true},
		{"owner may add an ungated twin", true, false, "owner", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gatedTwinAllowed(tt.hasGatedTwin, tt.requiresApproval, tt.role)
			if got != tt.want {
				t.Errorf("gatedTwinAllowed(twin=%v, gated=%v, role=%q) = %v, want %v",
					tt.hasGatedTwin, tt.requiresApproval, tt.role, got, tt.want)
			}
		})
	}
}

// UpdateWorkspace COALESCEs empty strings and nil pointers to the stored row,
// so the twin check has to run against what the workspace will point at after
// the write, not against whatever the request happened to carry.
func TestEffectiveConfigTarget(t *testing.T) {
	current := repository.Workspace{
		RepoURL:          "https://example.test/prod.git",
		WorkingDir:       "envs/prod",
		RequiresApproval: true,
	}

	tests := []struct {
		name     string
		req      UpdateWorkspaceRequest
		wantRepo string
		wantDir  string
		wantGate bool
	}{
		{"rename only keeps the stored config", UpdateWorkspaceRequest{Name: "renamed"},
			"https://example.test/prod.git", "envs/prod", true},
		{"repoint at another repo", UpdateWorkspaceRequest{RepoURL: "https://example.test/evil"},
			"https://example.test/evil", "envs/prod", true},
		{"repoint at another directory", UpdateWorkspaceRequest{WorkingDir: "envs/dev"},
			"https://example.test/prod.git", "envs/dev", true},
		{"drop the gate", UpdateWorkspaceRequest{RequiresApproval: boolPtr(false)},
			"https://example.test/prod.git", "envs/prod", false},
		{"resubmitting the stored gate is not a change", UpdateWorkspaceRequest{RequiresApproval: boolPtr(true)},
			"https://example.test/prod.git", "envs/prod", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, dir, gate := effectiveConfigTarget(current, tt.req)
			if repo != tt.wantRepo || dir != tt.wantDir || gate != tt.wantGate {
				t.Errorf("effectiveConfigTarget = (%q, %q, %v), want (%q, %q, %v)",
					repo, dir, gate, tt.wantRepo, tt.wantDir, tt.wantGate)
			}
		})
	}
}
