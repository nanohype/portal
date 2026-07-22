package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

// workingDirSpellings enumerates the respellings of one leaf that
// validateWorkingDir admits.
//
// The validator's alphabet is [A-Za-z0-9._/-] with no leading "-", no leading
// "/" and no "..", which leaves exactly three ways to write the same directory
// differently: a "." segment, a repeated separator, and a trailing separator.
// Every combination of them is generated here rather than listed, because the
// gated-twin check is only as good as the set of spellings it folds — a
// hand-written list is a list of the ones somebody thought of, and the one
// nobody thought of is the second door.
func workingDirSpellings() []string {
	prefixes := []string{"", "./", "././", ".//", ".//./"}
	separators := []string{"/", "//", "/./", "//./", "/././"}
	suffixes := []string{"", "/", "//", "/.", "/./", "/.//"}

	var out []string
	for _, prefix := range prefixes {
		for _, sep := range separators {
			for _, suffix := range suffixes {
				out = append(out, prefix+"envs"+sep+"prod"+suffix)
			}
		}
	}
	return out
}

// Every spelling the validator lets through has to resolve to one identity
// before anything compares configs, or an ungated twin of gated infrastructure
// is one keystroke away: both executors resolve these to the same directory —
// the local one joins with filepath.Join, which cleans, and the Kubernetes one
// runs `cd "/work/$PORTAL_WORKING_DIR"` in /bin/sh — so portal has to as well.
//
// This is the coupling the two functions have to keep: whatever
// validateWorkingDir admits, CanonicalWorkingDir folds. Loosening the validator
// without teaching the canonical form the new spelling fails here.
func TestEveryWorkingDirSpellingTheValidatorAdmitsIsOneTarget(t *testing.T) {
	const want = "envs/prod"

	spellings := workingDirSpellings()
	if len(spellings) < 100 {
		t.Fatalf("generated %d spellings, expected the full cross product", len(spellings))
	}

	for _, dir := range spellings {
		t.Run(dir, func(t *testing.T) {
			if err := validateWorkingDir(dir); err != nil {
				t.Fatalf("validateWorkingDir(%q) = %v — this spelling is outside the set under test", dir, err)
			}
			got := service.CanonicalWorkingDir(dir)
			if got != want {
				t.Fatalf("CanonicalWorkingDir(%q) = %q, want %q — a second door onto %q",
					dir, got, want, want)
			}

			// The canonical form has to be a working_dir the validator would
			// itself accept, or canonicalising on write turns a legal path into
			// one the next save is refused for.
			if err := validateWorkingDir(got); err != nil {
				t.Fatalf("validateWorkingDir(canonical %q) = %v", got, err)
			}
			// And it has to be a fixed point, so a row written twice does not
			// drift away from what the comparison expects.
			if again := service.CanonicalWorkingDir(got); again != got {
				t.Fatalf("CanonicalWorkingDir(%q) = %q — the canonical form must be stable", got, again)
			}
		})
	}
}

// The other half of the same claim: folding spellings must not fold
// directories. A canonicaliser that answered "envs/prod" for everything would
// pass the test above and refuse every legitimate second workspace in the org.
func TestDifferentWorkingDirsStayDifferent(t *testing.T) {
	distinct := []string{
		"envs/prod",
		"envs/prod2",
		"envs/prod.old",
		"envs/prod-dr",
		"envs/staging",
		"envs/prod/us-west-2",
		"prod",
		"Envs/Prod", // a checkout on a case-sensitive filesystem is a different leaf
		".",
	}

	seen := map[string]string{}
	for _, dir := range distinct {
		if err := validateWorkingDir(dir); err != nil {
			t.Fatalf("validateWorkingDir(%q) = %v", dir, err)
		}
		canonical := service.CanonicalWorkingDir(dir)
		if first, collided := seen[canonical]; collided {
			t.Errorf("CanonicalWorkingDir(%q) and CanonicalWorkingDir(%q) both = %q — distinct directories collapsed",
				first, dir, canonical)
		}
		seen[canonical] = dir
	}
}

// A leading "/" is refused at the boundary rather than folded, and the fold is
// still there underneath: a caller who reaches the service without the
// handler's validation cannot name a different target by rooting the path.
func TestRootedWorkingDirIsRefusedAndStillFolds(t *testing.T) {
	for _, dir := range []string{"/envs/prod", "/envs//prod/.", "//envs/prod"} {
		if err := validateWorkingDir(dir); err == nil {
			t.Errorf("validateWorkingDir(%q) = nil, want a refusal — working_dir must be relative", dir)
		}
		if got := service.CanonicalWorkingDir(dir); got != "envs/prod" {
			t.Errorf("CanonicalWorkingDir(%q) = %q, want envs/prod", dir, got)
		}
	}
}

// A delete is a move to nowhere, and the update predicate already says what a
// move to nowhere costs: the workspace leaves the config, carries no gate to
// the destination, and cannot be standing still.
func TestDeleteReadsAsVacatingTheConfig(t *testing.T) {
	tests := []struct {
		name    string
		current repository.Workspace
		want    bool
	}{
		{"the last gate on a config", repository.Workspace{
			RepoURL: "https://example.test/infra.git", WorkingDir: "envs/prod", RequiresApproval: true,
		}, true},

		// An ungated workspace is holding nothing, so removing it retires no
		// refusal — deleting a scratch workspace stays an ordinary act.
		{"an ungated workspace", repository.Workspace{
			RepoURL: "https://example.test/infra.git", WorkingDir: "envs/prod", RequiresApproval: false,
		}, false},

		// An upload workspace has no repo URL, so it has no config identity to
		// guard — the same reading gatedTwinAllowed and the query both take.
		{"a gated upload workspace", repository.Workspace{
			RepoURL: "", WorkingDir: "envs/prod", RequiresApproval: true,
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// targetGated=false, sameTarget=false is the delete case stated in
			// the update predicate's terms: no destination to be the same as,
			// and none to carry the gate.
			if got := vacatesGatedConfig(tt.current, false, false); got != tt.want {
				t.Errorf("vacatesGatedConfig(delete) = %v, want %v", got, tt.want)
			}
		})
	}
}

// The move check and the delete check have to agree, or the cheaper spelling
// wins: whatever Update refuses to let an operator walk away from, Delete has
// to refuse to let them remove.
func TestDeleteCostsWhatMovingCosts(t *testing.T) {
	gate := repository.Workspace{
		RepoURL: "https://example.test/infra.git", WorkingDir: "envs/prod", RequiresApproval: true,
	}

	moveAway := UpdateWorkspaceRequest{WorkingDir: "envs/prod-old"}
	_, _, targetGated := effectiveConfigTarget(gate, moveAway)
	movedOff := vacatesGatedConfig(gate, targetGated, false /* sameTarget */)
	deleted := vacatesGatedConfig(gate, false, false)
	if movedOff != deleted {
		t.Fatalf("moving reads as vacating = %v but deleting reads as %v — the two must be one act", movedOff, deleted)
	}

	for _, role := range []string{"operator", "viewer", "intern"} {
		if gatedOriginAllowed(deleted, false /* originStillGated */, role) {
			t.Errorf("%s must not delete the only workspace gating a configuration", role)
		}
	}
	// The way out an operator can actually take, and the authority that needs
	// no way out.
	if !gatedOriginAllowed(deleted, true /* originStillGated */, "operator") {
		t.Error("with another gate left on the config, deleting opens nothing and must be allowed")
	}
	if !gatedOriginAllowed(deleted, false, "admin") {
		t.Error("admin holds ActionApplyProd and must not be locked out of deleting a gated workspace")
	}
}

// deleteWorkspaceRequest builds the request the route hands the handler.
//
// The workspace-scoped role is admin, because that is how the exploit gets
// here: DELETE sits at ActionDeleteWorkspace, which a workspace_team_access
// grant of admin on this one workspace satisfies for someone whose ORG role is
// operator. The gate inside the handler reads the org role, so the grant that
// lets a team clean up its own workspaces is not also a way to retire a
// production approval.
func deleteWorkspaceRequest(orgID, userID, orgRole, workspaceID string) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces/"+workspaceID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspaceID", workspaceID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, auth.UserContextKey, &auth.UserContext{
		UserID: userID, OrgID: orgID, Role: orgRole,
	})
	ctx = auth.ContextWithWorkspaceRole(ctx, "admin")
	return req.WithContext(ctx)
}

// THE EXPLOIT, end to end against a real database: a plain org operator holding
// an admin grant on one workspace deletes the workspace that gates production,
// then stands an ungated workspace up on the config it vacated and applies with
// nobody signing. Every step here is the handler, the service and the SQL
// together, and the table is read afterwards — a 403 that still deleted the row
// would pass a status-only assertion.
func TestDeleteHoldsTheLastGateAtTheApprovalBar(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "ws-delete-gate")

	svc := service.NewWorkspaceService(testQueries, testPool, nil)
	h := NewWorkspaceHandler(svc, service.NewAuditService(testQueries), nil, testQueries)

	const repo = "https://github.com/acme/infra.git"
	create := func(t *testing.T, name, source, repoURL, dir string, gated bool) repository.Workspace {
		t.Helper()
		ws, err := svc.Create(ctx, service.CreateWorkspaceParams{
			OrgID: orgID, Name: name, CreatedBy: userID, Source: source,
			RepoURL: repoURL, RepoBranch: "main", WorkingDir: dir, RequiresApproval: gated,
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return ws
	}
	del := func(t *testing.T, orgRole, workspaceID string) int {
		t.Helper()
		rr := httptest.NewRecorder()
		h.Delete(rr, deleteWorkspaceRequest(orgID, userID, orgRole, workspaceID))
		return rr.Code
	}
	exists := func(t *testing.T, workspaceID string) bool {
		t.Helper()
		_, err := svc.Get(ctx, workspaceID, orgID)
		return err == nil
	}

	gate := create(t, "prod", "vcs", repo, "envs/prod", true)

	// 1. The exploit's first step, refused — and nothing removed.
	if code := del(t, "operator", gate.ID); code != http.StatusForbidden {
		t.Fatalf("delete of the last gate as operator = %d, want 403", code)
	}
	if !exists(t, gate.ID) {
		t.Fatal("the refused delete removed the workspace anyway")
	}

	// 2. The legitimate cases still go through at the operator bar, or the
	//    refusal above is worth nothing: an ungated workspace, and a gated
	//    upload workspace, which has no config identity to guard.
	scratch := create(t, "scratch", "vcs", repo, "envs/dev", false)
	if code := del(t, "operator", scratch.ID); code != http.StatusNoContent {
		t.Fatalf("delete of an ungated workspace as operator = %d, want 204", code)
	}
	if exists(t, scratch.ID) {
		t.Fatal("the allowed delete did not remove the workspace")
	}

	upload := create(t, "uploaded", "upload", "", "envs/prod", true)
	if code := del(t, "operator", upload.ID); code != http.StatusNoContent {
		t.Fatalf("delete of a gated upload workspace as operator = %d, want 204", code)
	}

	// 3. The way out the 403 names, taken across a respelling: another gated
	//    workspace on the same leaf, typed differently. It has to count, or the
	//    escape hatch is as dodgeable as the check it belongs to.
	sibling := create(t, "prod-dr", "vcs", repo+"/", "envs//prod/.", true)
	if sibling.WorkingDir != "envs/prod" {
		t.Fatalf("stored working_dir = %q, want the canonical envs/prod", sibling.WorkingDir)
	}
	if code := del(t, "operator", gate.ID); code != http.StatusNoContent {
		t.Fatalf("delete with another gate left on the config = %d, want 204", code)
	}
	if exists(t, gate.ID) {
		t.Fatal("the allowed delete did not remove the workspace")
	}

	// 4. The sibling is now the last gate, and whoever may release a gated
	//    apply may also retire the gate.
	if code := del(t, "operator", sibling.ID); code != http.StatusForbidden {
		t.Fatalf("delete of the new last gate as operator = %d, want 403", code)
	}
	if code := del(t, "admin", sibling.ID); code != http.StatusNoContent {
		t.Fatalf("delete of the last gate as admin = %d, want 204", code)
	}
	if exists(t, sibling.ID) {
		t.Fatal("the admin delete did not remove the workspace")
	}
}

// Deleting the gate and moving it away must cost the same at the same bar, over
// the same data — otherwise the invariant holds on one route and leaks on the
// other. This walks the fold too: the gate that stays behind is spelled
// differently from the one that leaves.
func TestDeleteAndMoveAgreeOnWhatVacatesAConfig(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "ws-delete-move")

	svc := service.NewWorkspaceService(testQueries, testPool, nil)
	const repo = "git@github.com:acme/infra.git"

	gate, err := svc.Create(ctx, service.CreateWorkspaceParams{
		OrgID: orgID, Name: "prod", CreatedBy: userID, Source: "vcs",
		RepoURL: repo, RepoBranch: "main", WorkingDir: "envs/prod", RequiresApproval: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Nothing else gates this config, so both routes see the same answer.
	stillGated, err := svc.HasGatedTwin(ctx, orgID, gate.RepoURL, gate.WorkingDir, gate.ID)
	if err != nil {
		t.Fatalf("HasGatedTwin: %v", err)
	}
	if stillGated {
		t.Fatal("no other workspace gates this config yet")
	}
	if gatedOriginAllowed(true, stillGated, "operator") {
		t.Fatal("an operator must not retire the only gate on a configuration, by move or by delete")
	}

	// A replacement gate on the same leaf under a different spelling of both
	// halves — https vs scp-style, and a "." segment in the path.
	if _, err := svc.Create(ctx, service.CreateWorkspaceParams{
		OrgID: orgID, Name: "prod-dr", CreatedBy: userID, Source: "vcs",
		RepoURL: "https://github.com/acme/infra", RepoBranch: "main",
		WorkingDir: "envs/./prod/", RequiresApproval: true,
	}); err != nil {
		t.Fatalf("create replacement gate: %v", err)
	}

	stillGated, err = svc.HasGatedTwin(ctx, orgID, gate.RepoURL, gate.WorkingDir, gate.ID)
	if err != nil {
		t.Fatalf("HasGatedTwin: %v", err)
	}
	if !stillGated {
		t.Fatal("a gated workspace on the same config, spelled differently, must count as the gate that stays behind")
	}
	if !gatedOriginAllowed(true, stillGated, "operator") {
		t.Fatal("with a replacement gate on the config, both routes must let an operator through")
	}
}

// A gated workspace on a config nobody else gates is what makes an ungated twin
// admin-only. Deleting it is therefore the same act as clearing its gate, and
// the messages have to say so on both routes — an operator who reads either one
// has to end up doing the same thing.
func TestGatedOriginMessagesNameTheSameWayOut(t *testing.T) {
	const wayOut = "leave another workspace requiring approval on it, or hold admin role or higher"
	for name, msg := range map[string]string{
		"move":   gatedOriginMessage,
		"delete": gatedOriginDeleteMessage,
	} {
		if !strings.Contains(msg, wayOut) {
			t.Errorf("%s refusal does not name the way out: %q", name, msg)
		}
	}
	if gatedOriginMessage == gatedOriginDeleteMessage {
		t.Error("the two refusals should describe the act they refused")
	}
}
