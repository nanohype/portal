package handler

import (
	"testing"

	"github.com/nanohype/portal/internal/repository"
)

func boolPtr(b bool) *bool { return &b }

// The settings form posts every field on every save, so "the request carries a
// value" is not the same as "the request takes the human out of an apply".
// Direction is what counts: adding a wait is an operator's to make, removing one
// is the act ActionApplyProd protects.
func TestOpensApprovalGate(t *testing.T) {
	gated := repository.Workspace{AutoApply: false, RequiresApproval: true}
	ungated := repository.Workspace{AutoApply: true, RequiresApproval: false}

	tests := []struct {
		name    string
		current repository.Workspace
		req     UpdateWorkspaceRequest
		want    bool
	}{
		{"no approval fields submitted", gated, UpdateWorkspaceRequest{Name: "renamed"}, false},
		{"resubmits the stored values", gated, UpdateWorkspaceRequest{
			AutoApply: boolPtr(false), RequiresApproval: boolPtr(true),
		}, false},
		{"turns auto_apply on", gated, UpdateWorkspaceRequest{AutoApply: boolPtr(true)}, true},
		{"turns requires_approval off", gated, UpdateWorkspaceRequest{RequiresApproval: boolPtr(false)}, true},
		{"flips both open", gated, UpdateWorkspaceRequest{
			AutoApply: boolPtr(true), RequiresApproval: boolPtr(false),
		}, true},
		{"changes other fields alongside stored approval values", gated, UpdateWorkspaceRequest{
			RepoURL: "https://example.test/repo.git", RepoBranch: "main",
			AutoApply: boolPtr(false), RequiresApproval: boolPtr(true),
		}, false},

		// The other direction only ever adds a wait. It is what the twin
		// check's 403 tells an operator to do, so it cannot itself be
		// admin-only.
		{"turns requires_approval on", ungated, UpdateWorkspaceRequest{RequiresApproval: boolPtr(true)}, false},
		{"turns auto_apply off", ungated, UpdateWorkspaceRequest{AutoApply: boolPtr(false)}, false},
		{"gates the workspace while repointing it", ungated, UpdateWorkspaceRequest{
			RepoURL: "https://example.test/infra.git", WorkingDir: "modules/vpc",
			RequiresApproval: boolPtr(true), AutoApply: boolPtr(false),
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := opensApprovalGate(tt.current, tt.req); got != tt.want {
				t.Errorf("opensApprovalGate = %v, want %v", got, tt.want)
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

// gatedTwinMessage tells an operator to "set requires_approval on this one too".
// Update runs the gate check before the twin check, so if adding a gate were an
// admin act the advice would be impossible to follow: leave the gate off and the
// twin check refuses, turn it on and the gate check refuses. This pins the way
// out the message documents, on the route that has to offer it — and pins that
// the door it is an alternative to stays shut.
func TestOperatorCanTakeTheGatedTwinEscapeHatch(t *testing.T) {
	const role = "operator"
	current := repository.Workspace{
		RepoURL:    "https://example.test/scratch.git",
		WorkingDir: ".",
	}
	onto := func(gate *bool) UpdateWorkspaceRequest {
		return UpdateWorkspaceRequest{
			RepoURL:          "https://example.test/infra.git",
			WorkingDir:       "modules/vpc",
			RequiresApproval: gate,
		}
	}

	// Repointing onto the gated config, carrying the gate.
	withGate := onto(boolPtr(true))
	if !approvalGateChangeAllowed(current, withGate, role) {
		t.Fatal("an operator must be allowed to add the gate the twin check asks for")
	}
	_, _, gated := effectiveConfigTarget(current, withGate)
	if !gatedTwinAllowed(true, gated, role) {
		t.Fatal("carrying the gate must satisfy the twin check — it is the way out the 403 names")
	}

	// The same move without the gate is the exploit, and stays refused.
	withoutGate := onto(nil)
	if _, _, stillUngated := effectiveConfigTarget(current, withoutGate); gatedTwinAllowed(true, stillUngated, role) {
		t.Fatal("an operator must not repoint an ungated workspace onto a gated config")
	}

	// And the gate they raised is still theirs to raise only — clearing it
	// afterwards is the act that removes the human, and stays at admin.
	gatedNow := repository.Workspace{RepoURL: "https://example.test/infra.git", WorkingDir: "modules/vpc", RequiresApproval: true}
	if approvalGateChangeAllowed(gatedNow, UpdateWorkspaceRequest{RequiresApproval: boolPtr(false)}, role) {
		t.Fatal("an operator must not clear the gate again")
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

// The twin check answers "may this caller put an ungated workspace onto a
// config something else gates" — a question about a move. The settings form
// resubmits every field on every save, so an edit that leaves the config target
// and the gate where they already are must not be treated as one, or renaming a
// workspace that already sits on such a config becomes an admin-only act.
func TestMovesConfigTarget(t *testing.T) {
	current := repository.Workspace{
		RepoURL:          "https://example.test/infra.git",
		WorkingDir:       "envs/prod",
		RequiresApproval: false,
	}

	tests := []struct {
		name             string
		repoURL          string
		workingDir       string
		requiresApproval bool
		want             bool
	}{
		// effectiveConfigTarget resolves an omitted field to the stored value,
		// so "unchanged" is what a rename or a description edit looks like here.
		{"rename only", current.RepoURL, current.WorkingDir, current.RequiresApproval, false},
		{"resubmits the stored config", current.RepoURL, current.WorkingDir, current.RequiresApproval, false},

		{"repoints at another repo", "https://example.test/other.git", current.WorkingDir, current.RequiresApproval, true},
		{"repoints at another working dir", current.RepoURL, "envs/staging", current.RequiresApproval, true},
		{"turns the gate on", current.RepoURL, current.WorkingDir, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := movesConfigTarget(current, tt.repoURL, tt.workingDir, tt.requiresApproval)
			if got != tt.want {
				t.Errorf("movesConfigTarget(%q, %q, gated=%v) = %v, want %v",
					tt.repoURL, tt.workingDir, tt.requiresApproval, got, tt.want)
			}
		})
	}
}

// Clearing requires_approval on a workspace that has a gated twin is still a
// move, so the twin check keeps running on the one update that could turn the
// last gate on a config off.
func TestMovesConfigTargetCatchesGateRemoval(t *testing.T) {
	gated := repository.Workspace{
		RepoURL:          "https://example.test/infra.git",
		WorkingDir:       "envs/prod",
		RequiresApproval: true,
	}
	if !movesConfigTarget(gated, gated.RepoURL, gated.WorkingDir, false) {
		t.Error("turning requires_approval off must count as a move")
	}
	if movesConfigTarget(gated, gated.RepoURL, gated.WorkingDir, true) {
		t.Error("keeping requires_approval on is not a move")
	}
}
