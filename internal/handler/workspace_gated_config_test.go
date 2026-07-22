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
//
// This fixture fixes the leaf at "envs/prod", so it can only state what folding
// has to preserve — one identity. What folding must not *introduce* is asked
// over the wider fixture below, which is free to spell leaves this one cannot.
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

// workingDirAlphabet is the four characters that decide a working directory's
// shape: an ordinary path character, the "." that path.Clean folds away, the
// "/" that separates segments and roots a path, and the "-" that turns a leaf
// into a git option. Every string the validator can be asked about is one of
// these four repeated, plus name characters that behave exactly like "a".
var workingDirAlphabet = []string{"a", ".", "/", "-"}

// workingDirSpellingsOverTheAlphabet enumerates every string of length 1..7
// over workingDirAlphabet — 21844 of them, generated in a few milliseconds.
//
// A fixture built by respelling "envs/prod" cannot express the violation the
// assertions below are about: no respelling of a leaf whose first character is
// a letter can ever clean into one that starts with a dash, so a test that
// asserts "the canonical form is admissible too" over that fixture asserts it
// against inputs that cannot break it. This alphabet reaches every shape the
// validator rejects — a leading "-", a leading "/", a "..", and a path that
// cleans away to nothing — so a spelling whose canonical form breaks a rule has
// somewhere to show up.
func workingDirSpellingsOverTheAlphabet() []string {
	out := []string{}
	frontier := []string{""}
	for length := 1; length <= 7; length++ {
		var next []string
		for _, prefix := range frontier {
			for _, char := range workingDirAlphabet {
				next = append(next, prefix+char)
			}
		}
		out = append(out, next...)
		frontier = next
	}
	return out
}

// canonicalisingOptionSpellings are the concrete requests that reached the
// column holding a value the boundary itself refuses. Each is a leaf named
// "-rf" or "--upload-pack" wearing a "./" in front of it: admitted as typed
// because the dash is not first, and then path.Clean folds the "./" away and
// the dash is first after all.
var canonicalisingOptionSpellings = []string{
	"./-rf",
	"././-rf",
	".//-rf",
	"./--upload-pack",
	"./-rf/",
	".//./-rf",
	"./-",
}

// What the working_dir rules say about a request has to still be true of the
// value the request stores, or the rules are about a string nothing downstream
// ever reads.
//
// The column is written canonical, so the canonical form is the working_dir:
// it is what the executors cd into, what the gated-twin comparison reads, and
// what the settings form resubmits on the next save. A spelling admitted as
// typed whose canonical form the same validator refuses leaves a row that
// breaks the rule it was checked against, and a workspace whose settings can
// never be saved again — the resubmit is validated, and it is validated as the
// stored spelling, not the typed one.
func TestNoAdmittedWorkingDirIsStoredAsOneTheValidatorRefuses(t *testing.T) {
	spellings := append(append([]string{}, canonicalisingOptionSpellings...),
		workingDirSpellingsOverTheAlphabet()...)

	for _, dir := range spellings {
		if err := validateWorkingDir(dir); err != nil {
			// Refused at the boundary. Nothing is stored, nothing to check.
			continue
		}

		stored := service.CanonicalWorkingDir(dir)
		if err := validateWorkingDir(stored); err != nil {
			t.Errorf("working_dir %q is admitted but stored as %q, which the same validator refuses: %v",
				dir, stored, err)
			continue
		}
		// A request that named a directory must not store "no directory" —
		// the empty string means "keep what is stored" everywhere else.
		if dir != "" && stored == "" {
			t.Errorf("working_dir %q is admitted and stores the empty string", dir)
		}
		// And the stored value has to be what storing it again would produce,
		// or a row rewritten by an unrelated save drifts off the target the
		// gated-twin comparison matched it on.
		if again := service.CanonicalWorkingDir(stored); again != stored {
			t.Errorf("working_dir %q stores %q, which canonicalises further to %q", dir, stored, again)
		}
	}
}

// The same claim from the other side, on the concrete spellings, so the fix is
// pinned to a behaviour and not only to a property: a path that means "-rf"
// once it is cleaned is refused where it is typed. The caller gets the refusal
// the leaf earns rather than a workspace they cannot save.
func TestWorkingDirSpellingsThatCleanIntoAnOptionAreRefused(t *testing.T) {
	for _, dir := range canonicalisingOptionSpellings {
		if err := validateWorkingDir(dir); err == nil {
			t.Errorf("validateWorkingDir(%q) = nil — it names the leaf %q, which working_dir may not start with",
				dir, service.CanonicalWorkingDir(dir))
		}
	}

	// The refusal is about the leaf, not about the "./": the same paths with
	// an ordinary leaf stay admitted, and a dash anywhere but the front of a
	// segment was never the problem.
	for _, dir := range []string{"./envs", "././envs", ".//envs", "./envs-dr/", "envs/-rf", "a-b/c-d"} {
		if err := validateWorkingDir(dir); err != nil {
			t.Errorf("validateWorkingDir(%q) = %v — this names an ordinary directory", dir, err)
		}
	}
}

// workspaceWriteRequest builds an authenticated create/update request. The org
// role is admin so the approval-gate checks stay out of the way — what is under
// test here is the working directory, not who may set a gate.
func workspaceWriteRequest(method, target, orgID, userID, workspaceID, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	if workspaceID != "" {
		rctx.URLParams.Add("workspaceID", workspaceID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, auth.UserContextKey, &auth.UserContext{
		UserID: userID, OrgID: orgID, Role: "admin",
	})
	ctx = auth.ContextWithWorkspaceRole(ctx, "admin")
	return req.WithContext(ctx)
}

// The route end of the same claim, against a real database: what the boundary
// refuses never reaches the column, and what it admits reaches the column in
// the spelling it was admitted as.
//
// The seam test above cannot say this on its own. A route that asked the
// admission helper and then wrote the string the request arrived with would
// pass every assertion in this file that stops at the helper, and the row would
// hold a working directory nothing judged — which is the shape of the defect
// this test exists for.
func TestCreateAndUpdateStoreTheWorkingDirTheyAdmitted(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "ws-workdir-admit")

	svc := service.NewWorkspaceService(testQueries, testPool, nil)
	h := NewWorkspaceHandler(svc, service.NewAuditService(testQueries), nil, testQueries)

	create := func(t *testing.T, name, workingDir string) *httptest.ResponseRecorder {
		t.Helper()
		body := `{"name":"` + name + `","source":"vcs","repo_url":"https://github.com/acme/infra.git",` +
			`"repo_branch":"main","working_dir":"` + workingDir + `"}`
		rr := httptest.NewRecorder()
		h.Create(rr, workspaceWriteRequest(http.MethodPost, "/api/v1/workspaces", orgID, userID, "", body))
		return rr
	}
	update := func(t *testing.T, workspaceID, workingDir string) *httptest.ResponseRecorder {
		t.Helper()
		body := `{"working_dir":"` + workingDir + `"}`
		rr := httptest.NewRecorder()
		h.Update(rr, workspaceWriteRequest(http.MethodPatch,
			"/api/v1/workspaces/"+workspaceID, orgID, userID, workspaceID, body))
		return rr
	}

	// A respelled ordinary directory goes in and lands on its leaf.
	if code := create(t, "prod", ".//envs/./prod/").Code; code != http.StatusCreated {
		t.Fatalf("create with a respelled working_dir = %d, want 201", code)
	}
	stored, _, err := svc.List(ctx, orgID, 1, 50, "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(stored) != 1 || stored[0].WorkingDir != "envs/prod" {
		t.Fatalf("stored working_dir = %+v, want a single row at envs/prod", stored)
	}
	workspaceID := stored[0].ID

	// And a directory that only becomes a git option once it is cleaned is
	// refused on both routes, with nothing written and nothing moved.
	for _, dir := range canonicalisingOptionSpellings {
		t.Run(dir, func(t *testing.T) {
			if code := create(t, "sneaky", dir).Code; code != http.StatusBadRequest {
				t.Errorf("create with working_dir %q = %d, want 400", dir, code)
			}
			if code := update(t, workspaceID, dir).Code; code != http.StatusBadRequest {
				t.Errorf("update with working_dir %q = %d, want 400", dir, code)
			}
		})
	}

	after, err := svc.Get(ctx, workspaceID, orgID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.WorkingDir != "envs/prod" {
		t.Errorf("working_dir after the refused updates = %q, want it untouched at envs/prod", after.WorkingDir)
	}
	all, _, err := svc.List(ctx, orgID, 1, 50, "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("org holds %d workspaces, want only the one the boundary admitted", len(all))
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
