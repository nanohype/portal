package handler

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The golden is a real OpenTofu plan carrying a rotated secret, shared with the
// package that projects it. See internal/tfstate/testdata/regenerate.sh.
const (
	rotatedSecret  = "sentinel-rotated-FFFF"
	variableSecret = "sentinel-plain-BBBB"
	noOpSecret     = "sentinel-no-op-EEEE"
)

func storedPlan(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "tfstate", "testdata", "plan.json"))
	if err != nil {
		t.Fatalf("reading the stored-plan golden: %v", err)
	}
	for _, sentinel := range []string{rotatedSecret, variableSecret, noOpSecret} {
		if !strings.Contains(string(data), sentinel) {
			t.Fatalf("the golden no longer holds %s, so serving it proves nothing", sentinel)
		}
	}
	return data
}

func servePlanAs(t *testing.T, effectiveRole string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := workspaceRequestAs("/api/v1/workspaces/ws-1/runs/run-1/plan-json", effectiveRole)
	rec := httptest.NewRecorder()
	servePlanJSON(rec, req, "run-1", storedPlan(t))
	return rec, rec.Body.String()
}

// The endpoint that fed the plan diff wrote the stored `tofu show -json` bytes
// straight to the response, at the workspace READ bar. Those bytes are not
// redacted: the rendered plan prints "(sensitive value)" where this holds the
// cleartext, and the document also carries the root variable values and a whole
// prior-state representation that the rendered plan has no equivalent for.
//
// THE EXPLOIT: anyone who can see a workspace reading every secret the plan
// touches — including the sensitive variables portal encrypted at rest and
// decrypted to run the plan — from the run's own Changes tab.
func TestAViewerIsServedNoValueThePlanMarksSensitive(t *testing.T) {
	for _, role := range []string{"viewer", "operator", "intern", ""} {
		t.Run("as "+role, func(t *testing.T) {
			rec, body := servePlanAs(t, role)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if strings.Contains(body, rotatedSecret) {
				t.Errorf("the value the plan marks sensitive reached a %q", role)
			}
			if strings.Contains(body, variableSecret) {
				t.Errorf("a root variable value reached a %q", role)
			}
			if !strings.Contains(body, "sensitive_values_redacted") {
				t.Error("the response does not say values were withheld")
			}
		})
	}
}

// Values follow the state download, which sits at ActionManageState — admin.
// Without this the redaction could be unconditional and no bar would serve the
// plan the product exists to show.
func TestAStateManagerIsServedTheValues(t *testing.T) {
	for _, role := range []string{"admin", "owner"} {
		t.Run("as "+role, func(t *testing.T) {
			rec, body := servePlanAs(t, role)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if !strings.Contains(body, rotatedSecret) {
				t.Errorf("a %q was not served the value; the bar is wrong or nothing serves it", role)
			}
			if strings.Contains(body, "sensitive_values_redacted") {
				t.Errorf("a %q was told values were withheld", role)
			}
		})
	}
}

// The sections outside resource_changes are dropped at every bar, because that
// is what this endpoint returns rather than a judgement about the caller. The
// state download and the variables endpoint own those, at their own bars.
func TestTheSectionsOutsideResourceChangesAreDroppedForEveryone(t *testing.T) {
	for _, role := range []string{"viewer", "admin", "owner"} {
		_, body := servePlanAs(t, role)
		for _, gone := range []string{variableSecret, noOpSecret} {
			if strings.Contains(body, gone) {
				t.Errorf("as %q: %s survived", role, gone)
			}
		}
	}
}

// A stored plan that will not parse is an error, and the error does not carry
// the bytes back out. Falling back to the raw artifact here would reopen
// everything above on exactly the input nobody understands.
func TestAnUnreadableStoredPlanIsNotEchoedBack(t *testing.T) {
	req := workspaceRequestAs("/api/v1/workspaces/ws-1/runs/run-1/plan-json", "viewer")
	rec := httptest.NewRecorder()

	servePlanJSON(rec, req, "run-1", []byte(`{"variables": {"t": {"value": "`+rotatedSecret+`"}} NOT JSON`))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), rotatedSecret) {
		t.Error("the failure echoed the stored bytes")
	}
}

// Whoever fetches a stored plan writes it through servePlanJSON, or does not
// write it.
//
// This is the gate that outlives the fix. Every test above exercises
// servePlanJSON, which is the redaction — none of them can see the caller
// deciding not to use it, and the defect being fixed here was exactly that
// shape: a handler that reached past its own response type and wrote the bytes
// it had just fetched. A test that could not catch the code being put back is
// not the check for this.
//
// So: a function that reads the stored plan must hand it to servePlanJSON, and
// must not touch the ResponseWriter itself. respond.Error is fine — it takes the
// writer as an argument. w.Write and w.Header() are the passthrough.
func TestNothingWritesAStoredPlanExceptThroughServePlanJSON(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing the handler package: %v", err)
	}

	var fetchers int
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				if !callsMethod(fn.Body, "GetPlanJSON") {
					continue
				}
				fetchers++

				where := fset.Position(fn.Pos())
				if !callsFunction(fn.Body, "servePlanJSON") {
					t.Errorf("%s: %s reads the stored plan and does not serve it through servePlanJSON — the stored artifact is unredacted `tofu show -json` (%s:%d)",
						filepath.Base(path), fn.Name.Name, where.Filename, where.Line)
				}
				if name := responseWriterParam(fn); name != "" {
					if bad := methodsCalledOn(fn.Body, name); len(bad) > 0 {
						t.Errorf("%s: %s writes to the response itself (%s) — the plan reaches the caller through servePlanJSON or not at all (%s:%d)",
							filepath.Base(path), fn.Name.Name, strings.Join(bad, ", "), where.Filename, where.Line)
					}
				}
			}
		}
	}

	if fetchers == 0 {
		t.Fatal("no handler reads the stored plan; the walk is not seeing the package")
	}
}

// callsMethod reports whether the body calls x.name(...) on anything.
func callsMethod(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			found = true
		}
		return true
	})
	return found
}

// callsFunction reports whether the body calls the bare function name(...).
func callsFunction(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return true
	})
	return found
}

// responseWriterParam returns the name the function gave its http.ResponseWriter.
func responseWriterParam(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil {
		return ""
	}
	for _, field := range fn.Type.Params.List {
		sel, ok := field.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "ResponseWriter" {
			continue
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "http" {
			continue
		}
		if len(field.Names) > 0 {
			return field.Names[0].Name
		}
	}
	return ""
}

// methodsCalledOn returns every method invoked on the named identifier.
func methodsCalledOn(body *ast.BlockStmt, name string) []string {
	var called []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == name {
			called = append(called, name+"."+sel.Sel.Name)
		}
		return true
	})
	return called
}

// The response is the declared shape, not a passthrough.
func TestTheResponseIsTheDeclaredShape(t *testing.T) {
	rec, body := servePlanAs(t, "viewer")

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}

	var got struct {
		FormatVersion   string `json:"format_version"`
		ValuesRedacted  bool   `json:"sensitive_values_redacted"`
		ResourceChanges []struct {
			Address string `json:"address"`
			Change  struct {
				Actions []string       `json:"actions"`
				Before  map[string]any `json:"before"`
				After   map[string]any `json:"after"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("response did not parse: %v", err)
	}

	if got.FormatVersion == "" {
		t.Error("format_version is empty")
	}
	if !got.ValuesRedacted {
		t.Error("sensitive_values_redacted is false for a viewer")
	}
	if len(got.ResourceChanges) == 0 {
		t.Fatal("no resource changes were served")
	}
	for _, rc := range got.ResourceChanges {
		if rc.Address == "" || len(rc.Change.Actions) == 0 {
			t.Errorf("a change came through without an address or an action: %+v", rc)
		}
	}
}
