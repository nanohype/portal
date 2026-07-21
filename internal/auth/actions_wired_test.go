package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is where this package sits relative to the module root.
const repoRoot = "../.."

// rbacFile declares the action vocabulary. References from inside it are
// declarations and mappings, not enforcement, so the scan skips it.
const rbacFile = "internal/auth/rbac.go"

// runOperations is the set of run operations the API accepts. ActionForOperation
// turns each of them into the action the run handler enforces, so anything it
// can return counts as wired the moment the handler calls it.
var runOperations = []string{"plan", "apply", "destroy", "import", "test"}

// TestEveryActionIsEnforced fails when an Action constant exists but nothing in
// production code enforces it.
//
// An action counts as enforced when non-test code outside internal/auth names
// it — a route gate in the server, or a check inside a handler — or when
// ActionForOperation can return it and some non-test code outside internal/auth
// calls ActionForOperation. Anything else is a name that reads like policy and
// enforces nothing, which is what let workspace variables and state downloads
// sit open behind actions that claimed to cover them.
func TestEveryActionIsEnforced(t *testing.T) {
	declared := declaredActions(t)
	if len(declared) == 0 {
		t.Fatal("no Action constants found — the scanner is broken, not the code")
	}

	referenced, callsActionForOperation := scanProductionReferences(t)

	if callsActionForOperation {
		for _, op := range runOperations {
			referenced[string(ActionForOperation(op))] = struct{}{}
		}
	}

	for name, value := range declared {
		if _, ok := referenced[name]; ok {
			continue
		}
		if _, ok := referenced[value]; ok {
			continue
		}
		t.Errorf("auth.%s (%q) is declared but no non-test code outside internal/auth enforces it — "+
			"gate a route with it, check it in a handler, or delete the constant", name, value)
	}
}

// declaredActions parses rbac.go and returns every `Name Action = "value"`
// constant, so a constant added later joins the check automatically.
func declaredActions(t *testing.T) map[string]string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(repoRoot, rbacFile), nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", rbacFile, err)
	}

	actions := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			ident, ok := value.Type.(*ast.Ident)
			if !ok || ident.Name != "Action" {
				continue
			}
			for i, name := range value.Names {
				literal, ok := value.Values[i].(*ast.BasicLit)
				if !ok {
					continue
				}
				actions[name.Name] = strings.Trim(literal.Value, `"`)
			}
		}
	}
	return actions
}

// scanProductionReferences walks every non-test Go file outside internal/auth
// and collects the `auth.ActionXxx` selectors it names, plus whether any of
// them calls auth.ActionForOperation.
func scanProductionReferences(t *testing.T) (map[string]struct{}, bool) {
	t.Helper()

	referenced := map[string]struct{}{}
	callsActionForOperation := false
	fset := token.NewFileSet()

	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			switch rel {
			case "internal/auth", "web", "node_modules", ".git":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "auth" {
				return true
			}
			switch {
			case sel.Sel.Name == "ActionForOperation":
				callsActionForOperation = true
			case strings.HasPrefix(sel.Sel.Name, "Action"):
				referenced[sel.Sel.Name] = struct{}{}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}

	return referenced, callsActionForOperation
}
