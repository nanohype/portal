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

// The clone route prints gatedTwinMessage too — "set requires_approval on this
// one too, or hold admin role or higher" — and a clone that could not carry a
// gate made the first half of that unreachable: the request had no field for it
// and the clone copied the source's flag. The manual way round (create the
// workspace by hand, then copy the variables) runs through a route that is
// itself admin-only, so an operator was told to do something they could not do.
//
// So the clone request may raise the gate, and only raise it.
func TestCloneApprovalGate(t *testing.T) {
	tests := []struct {
		name        string
		sourceGated bool
		requested   *bool
		role        string
		wantGate    bool
		wantAllowed bool
	}{
		// Omitting the field inherits, which is what a clone always meant.
		{"inherits an ungated source", false, nil, "operator", false, true},
		{"inherits a gated source", true, nil, "operator", true, true},

		// The escape hatch the 403 names: clone the twin gated.
		{"an operator may raise the gate", false, boolPtr(true), "operator", true, true},
		{"a viewer may raise the gate", false, boolPtr(true), "viewer", true, true},
		{"asking for the gate a gated source already has", true, boolPtr(true), "operator", true, true},

		// The other direction is the act that removes the human from an apply.
		{"an operator may not clone away the gate", true, boolPtr(false), "operator", true, false},
		{"a viewer may not clone away the gate", true, boolPtr(false), "viewer", true, false},
		{"an unknown role may not clone away the gate", true, boolPtr(false), "intern", true, false},
		{"an admin may clone away the gate", true, boolPtr(false), "admin", false, true},
		{"an owner may clone away the gate", true, boolPtr(false), "owner", false, true},

		// Nothing to clear on an ungated source.
		{"an operator may clone an ungated source ungated", false, boolPtr(false), "operator", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate, allowed := cloneApprovalGate(tt.sourceGated, tt.requested, tt.role)
			if allowed != tt.wantAllowed {
				t.Errorf("allowed = %v, want %v", allowed, tt.wantAllowed)
			}
			if gate != tt.wantGate {
				t.Errorf("gate = %v, want %v", gate, tt.wantGate)
			}
		})
	}
}

// And the hatch actually clears the twin refusal: an operator cloning onto a
// config something else gates gets through by carrying the gate, and no other
// way.
func TestCloneCanTakeTheGatedTwinEscapeHatch(t *testing.T) {
	const role = "operator"

	gate, allowed := cloneApprovalGate(false, boolPtr(true), role)
	if !allowed {
		t.Fatal("an operator must be allowed to raise the gate on a clone")
	}
	if !gatedTwinAllowed(true, gate, role) {
		t.Fatal("carrying the gate must satisfy the twin check on the clone route")
	}

	inherited, allowed := cloneApprovalGate(false, nil, role)
	if !allowed {
		t.Fatal("inheriting an ungated source is not itself a refusal")
	}
	if gatedTwinAllowed(true, inherited, role) {
		t.Fatal("an operator must not clone an ungated twin onto a gated config")
	}
}

// A gated workspace does not only gate itself: while it sits on a repo +
// working_dir, gatedTwinAllowed refuses an ungated second workspace there at
// the operator bar. Moving it away takes that refusal with it, which is the
// same act as clearing its gate — and used to cost nothing, because both twin
// checks in Update only ever asked about the destination.
func TestVacatesGatedConfig(t *testing.T) {
	gated := repository.Workspace{
		RepoURL:          "https://example.test/infra.git",
		WorkingDir:       "envs/prod",
		RequiresApproval: true,
	}
	ungated := repository.Workspace{
		RepoURL:          "https://example.test/infra.git",
		WorkingDir:       "envs/prod",
		RequiresApproval: false,
	}
	upload := repository.Workspace{
		RepoURL:          "",
		WorkingDir:       "envs/prod",
		RequiresApproval: true,
	}

	tests := []struct {
		name        string
		current     repository.Workspace
		targetGated bool
		sameTarget  bool
		want        bool
	}{
		// THE EXPLOIT: walk the gated workspace off the config it guards, then
		// create an ungated workspace on the vacated config and apply.
		{"gated workspace repointed elsewhere", gated, true, false, true},

		// Clearing the gate in place empties the config just as well.
		// approvalGateChangeAllowed already holds that at admin; this is the
		// second reading of the same act, and it must agree.
		{"gate cleared in place", gated, false, true, true},
		{"repointed and ungated at once", gated, false, false, true},

		// Staying put is not a move. The settings form resubmits every field on
		// every save, and sameTarget is the identity comparison, so a save that
		// respells its own repo URL is not charged for a move it did not make.
		{"gated workspace saved in place", gated, true, true, false},

		// A workspace with no gate is holding nothing, so it takes nothing with
		// it. Repointing at the operator bar is the documented workflow.
		{"ungated workspace repointed", ungated, false, false, false},
		{"ungated workspace picking up a gate as it moves", ungated, true, false, false},

		// An upload workspace has no repo URL, so it has no config identity to
		// guard — the same reading gatedTwinAllowed and the query both take.
		{"gated upload workspace repointed", upload, true, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vacatesGatedConfig(tt.current, tt.targetGated, tt.sameTarget)
			if got != tt.want {
				t.Errorf("vacatesGatedConfig(gated=%v, repo=%q, targetGated=%v, sameTarget=%v) = %v, want %v",
					tt.current.RequiresApproval, tt.current.RepoURL, tt.targetGated, tt.sameTarget, got, tt.want)
			}
		})
	}
}

// Taking the last gate off a config is the act ActionApplyProd protects,
// whichever way it is spelled — and leaving another gate behind is the way an
// operator does it without one.
func TestGatedOriginAllowed(t *testing.T) {
	tests := []struct {
		name             string
		vacates          bool
		originStillGated bool
		role             string
		want             bool
	}{
		// Not a move off a gated config: nothing to decide, every role through.
		{"operator saving a workspace in place", false, false, "operator", true},
		{"viewer request untouched by this check", false, false, "viewer", true},

		// The exploit: the only gate on a config walks away at the operator bar.
		{"operator cannot vacate the last gate", true, false, "operator", false},
		{"viewer cannot vacate the last gate", true, false, "viewer", false},
		{"unknown role cannot vacate the last gate", true, false, "intern", false},

		// The way out: another gated workspace stays on that config, so the
		// config is still guarded and the move opens nothing. Creating that
		// workspace is itself an operator act — requires_approval only ever
		// adds a wait.
		{"operator may move when another gate stays behind", true, true, "operator", true},
		{"viewer request with another gate behind", true, true, "viewer", true},

		// Whoever may release a gated apply may also retire the gate.
		{"admin may vacate the last gate", true, false, "admin", true},
		{"owner may vacate the last gate", true, false, "owner", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gatedOriginAllowed(tt.vacates, tt.originStillGated, tt.role)
			if got != tt.want {
				t.Errorf("gatedOriginAllowed(vacates=%v, stillGated=%v, role=%q) = %v, want %v",
					tt.vacates, tt.originStillGated, tt.role, got, tt.want)
			}
		})
	}
}

// The full sequence the two destination-only checks let through, walked step by
// step at the operator bar: move the gate off production, then twin the vacated
// config ungated, then apply without a signature. Each step is what Update and
// Create actually evaluate, in the order they evaluate it.
func TestOperatorCannotWalkAGateOffItsConfig(t *testing.T) {
	const role = "operator"
	const prodRepo = "https://example.test/infra.git"
	const prodDir = "envs/prod"

	gate := repository.Workspace{RepoURL: prodRepo, WorkingDir: prodDir, RequiresApproval: true}
	moveAway := UpdateWorkspaceRequest{WorkingDir: "envs/prod-old"}

	// Step 1 as it used to pass: no approval field is submitted, so the gate
	// check has nothing to say, and the destination is empty so the twin check
	// waves it through.
	if !approvalGateChangeAllowed(gate, moveAway, role) {
		t.Fatal("moving a workspace submits no approval field; the gate-direction check cannot be what stops this")
	}
	targetRepo, targetDir, targetGated := effectiveConfigTarget(gate, moveAway)
	if !movesConfigTarget(gate, targetRepo, targetDir, targetGated) {
		t.Fatal("repointing the working directory must read as a move")
	}
	if !gatedTwinAllowed(false, targetGated, role) {
		t.Fatal("nothing gates the destination; the destination check cannot be what stops this either")
	}

	// Step 1 as it is now: the workspace is the only gate on envs/prod, and
	// leaving is refused.
	vacates := vacatesGatedConfig(gate, targetGated, false /* sameTarget */)
	if !vacates {
		t.Fatal("moving the only gated workspace off a config must read as vacating it")
	}
	if gatedOriginAllowed(vacates, false /* originStillGated */, role) {
		t.Fatal("an operator must not walk the last approval gate off a configuration")
	}

	// Step 2 is what the refusal is protecting, and it stays refused only for
	// as long as step 1 does: with the gate still on envs/prod, an ungated twin
	// there is admin-only.
	if gatedTwinAllowed(true /* hasGatedTwin */, false /* requiresApproval */, role) {
		t.Fatal("with the gate still in place, an ungated twin on that config must be refused")
	}

	// The escape hatch, exercised: leave another gated workspace on envs/prod
	// and the move is an operator's to make. Creating that workspace is itself
	// allowed at the operator bar, so the advice is followable.
	if !gatedTwinAllowed(true, true /* requiresApproval */, role) {
		t.Fatal("an operator must be able to stand up the replacement gate the 403 asks for")
	}
	if !approvalGateAtCreateAllowed(false /* autoApply */, role) {
		t.Fatal("the replacement gate is an ordinary workspace; creating it must not need admin")
	}
	if !gatedOriginAllowed(vacates, true /* originStillGated */, role) {
		t.Fatal("with a replacement gate on the config, the move opens nothing and must be allowed")
	}

	// And an admin — who may clear the gate outright — may also move it.
	if !gatedOriginAllowed(vacates, false, "admin") {
		t.Fatal("admin holds ActionApplyProd and must not be locked out of moving a gated workspace")
	}
}
