package executor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// An executor is a strategy, and a strategy that silently handles a subset of
// its inputs is worse than one that handles none: the operation it skipped is
// reported as having run. The Kubernetes executor rendered a script with no
// operation in it for an `import`, the pod exited 0, and the worker wrote the
// run back as applied.
//
// A label naming a command is not the command. So these run the operation and
// look at what it invoked: a rendered arm is executed under a shell against a
// recording `tofu`, and the local executor is driven end to end against the
// same. An arm that echoes its own name, or carries a label over an empty body,
// invokes nothing and fails here.
//
// The other direction — that no arm exists for an operation the vocabulary does
// not admit — is a property of the source, so it is read from the source: the
// switch statement is parsed out of the file by AST and its case labels
// compared. It is scoped to one function's one switch, not to a token anywhere
// in a file.

// runOperationEnum reads the vocabulary from the migration that defines it.
// CLAUDE.md makes that enum the one source of truth, so reading it here is what
// stops this becoming a second copy.
func runOperationEnum(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile("../../../migrations/000001_initial_schema.up.sql")
	if err != nil {
		t.Fatalf("read the schema that defines run_operation: %v", err)
	}
	block := regexp.MustCompile(`(?s)CREATE TYPE run_operation AS ENUM \((.*?)\);`).FindStringSubmatch(string(body))
	if block == nil {
		t.Fatal("no run_operation enum in the initial schema; the vocabulary moved and this test reads the wrong file")
	}
	var ops []string
	for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(block[1], -1) {
		ops = append(ops, m[1])
	}
	if len(ops) == 0 {
		t.Fatal("run_operation enum parsed as empty")
	}
	return ops
}

// ── the recording binary ───────────────────────────────────────────────────

// recordingBinary writes an executable named `tofu` into dir that appends its
// own argv to a log and answers the few subcommands a rendered script depends
// on. What the script actually invoked is then a fact rather than a reading of
// its text.
func recordingBinary(t *testing.T, dir, logPath string) {
	t.Helper()
	script := `#!/bin/sh
echo "$@" >> "` + logPath + `"
case "$1" in
  show)   printf '{"format_version":"1.2","resource_changes":[]}' ;;
  state)  printf '{"version":4}' ;;
  output) printf '{}' ;;
  plan)
    OUT=""
    for a in "$@"; do case "$a" in -out=*) OUT="${a#-out=}" ;; esac; done
    [ -n "$OUT" ] && printf 'binary plan' > "$OUT"
    ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "tofu"), []byte(script), 0o755); err != nil {
		t.Fatalf("write recording tofu: %v", err)
	}
	// terragrunt is the other wrapper the same scripts drive.
	if err := os.WriteFile(filepath.Join(dir, "terragrunt"), []byte(script), 0o755); err != nil {
		t.Fatalf("write recording terragrunt: %v", err)
	}
}

// invocations runs a rendered operation section under /bin/sh and returns what
// the binary was called with.
func invocations(t *testing.T, section string) []string {
	t.Helper()
	work := t.TempDir()
	bin := t.TempDir()
	logPath := filepath.Join(work, "invocations.log")
	recordingBinary(t, bin, logPath)

	// smoke-test.sh is the `test` arm's own script, and it records that it ran —
	// the arm's whole job is to execute the workspace's script, so nothing else
	// can stand in for having done so.
	smoke := "#!/bin/sh\necho ./smoke-test.sh >> \"" + logPath + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(work, "smoke-test.sh"), []byte(smoke), 0o755); err != nil {
		t.Fatalf("write smoke-test.sh: %v", err)
	}

	prelude := "#!/bin/sh\nset -e\nBIN=tofu\nVAR_FILE=''\n"
	scriptPath := filepath.Join(work, "operation.sh")
	if err := os.WriteFile(scriptPath, []byte(prelude+section), 0o755); err != nil {
		t.Fatalf("write operation script: %v", err)
	}

	cmd := exec.Command("/bin/sh", scriptPath)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PORTAL_IMPORT_ADDR_0=aws_s3_bucket.logs",
		"PORTAL_IMPORT_ID_0=portal-logs",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the rendered operation did not run: %v\n%s\n--- script ---\n%s", err, out, section)
	}

	body, err := os.ReadFile(logPath)
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// ── every operation the vocabulary admits runs ─────────────────────────────

// The Kubernetes executor is the only one production permits.
func TestPodScript_RunsEveryOperationTheEnumAdmits(t *testing.T) {
	for _, op := range runOperationEnum(t) {
		t.Run(op, func(t *testing.T) {
			params := ExecuteParams{Operation: op, Source: "upload", WorkingDir: "envs/production"}
			if op == "import" {
				params.ImportResources = []ImportResource{{Address: "aws_s3_bucket.logs", ID: "portal-logs"}}
			}

			section, err := operationScript(params)
			if err != nil {
				t.Fatalf("the production executor renders nothing for %q: %v", op, err)
			}

			ran := invocations(t, section)
			if !invokedOperation(op, ran) {
				t.Errorf("the %q arm invoked %v; none of it is the %s the operation is named for, so a pod running this exits 0 having done nothing",
					op, ran, expectedInvocation(op))
			}
		})
	}
}

// expectedInvocation names what an operation must actually call. `test` runs the
// workspace's own smoke script rather than a tofu subcommand.
func expectedInvocation(op string) string {
	if op == "test" {
		return "./smoke-test.sh"
	}
	return "tofu " + op
}

func invokedOperation(op string, ran []string) bool {
	if op == "test" {
		// The smoke script records its own execution, so an arm that skips
		// straight past it records nothing.
		for _, line := range ran {
			if strings.Contains(line, "smoke-test.sh") {
				return true
			}
		}
		return false
	}
	for _, line := range ran {
		if strings.HasPrefix(line, op+" ") || line == op {
			return true
		}
	}
	return false
}

// An import with resources must invoke one import per resource, and the values
// must arrive as the addresses rather than as empty strings.
func TestPodScript_InvokesOneImportPerResource(t *testing.T) {
	section, err := operationScript(ExecuteParams{
		Operation: "import", Source: "upload",
		ImportResources: []ImportResource{{Address: "aws_s3_bucket.logs", ID: "portal-logs"}},
	})
	if err != nil {
		t.Fatalf("operationScript: %v", err)
	}

	ran := invocations(t, section)
	var imports int
	for _, line := range ran {
		if strings.HasPrefix(line, "import ") {
			imports++
			if !strings.Contains(line, "aws_s3_bucket.logs") || !strings.Contains(line, "portal-logs") {
				t.Errorf("the import ran without its address and id: %q", line)
			}
		}
	}
	if imports != 1 {
		t.Errorf("%d imports ran, want 1; the arm's echo is not the import", imports)
	}
}

// An import with no resources adopts nothing, and a run that adopted nothing
// must not be recorded as having done so. Imports live only in the River job
// args, and the re-enqueue path carries the operation alone.
func TestPodScript_RefusesAnImportWithNoResources(t *testing.T) {
	_, err := operationScript(ExecuteParams{Operation: "import", Source: "upload"})
	if err == nil {
		t.Fatal("an import naming no resources rendered a script that adopts nothing and exits 0; the run is then recorded applied with nothing imported")
	}
	if !strings.Contains(err.Error(), "no resources") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

// The local executor runs the same vocabulary. Driving it end to end is what
// catches a case label kept over an emptied body.
func TestLocalExecutor_RunsEveryOperationTheEnumAdmits(t *testing.T) {
	for _, op := range runOperationEnum(t) {
		t.Run(op, func(t *testing.T) {
			work := t.TempDir()
			bin := t.TempDir()
			logPath := filepath.Join(work, "invocations.log")
			recordingBinary(t, bin, logPath)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

			entries := [][2]string{{"envs/production/main.tf", "# empty\n"}}
			if op == "test" {
				entries = append(entries, [2]string{"envs/production/smoke-test.sh",
					"#!/bin/sh\necho ./smoke-test.sh >> \"" + logPath + "\"\nexit 0\n"})
			}

			params := ExecuteParams{
				RunID: "run_" + op, Operation: op, Source: "upload",
				WorkingDir: "envs/production", ArchiveData: tarGz(t, entries),
				LogCallback: func([]byte) {},
			}
			if op == "import" {
				params.ImportResources = []ImportResource{{Address: "aws_s3_bucket.logs", ID: "portal-logs"}}
			}

			e := &LocalExecutor{}
			if _, err := e.Execute(t.Context(), params); err != nil && op != "test" {
				t.Fatalf("the local executor failed %q: %v", op, err)
			}

			body, _ := os.ReadFile(logPath)
			ran := strings.Split(strings.TrimSpace(string(body)), "\n")
			if !invokedOperation(op, ran) {
				t.Errorf("the local executor's %q arm invoked %v; none of it is %s", op, ran, expectedInvocation(op))
			}
		})
	}
}

// ── no arm outside the vocabulary ──────────────────────────────────────────

// caseLabelsOf parses one function's switch on the named selector and returns
// its case labels, plus whether it has a default.
//
// This is a property of the source — which labels the dispatch declares — so it
// is read from the source. It is scoped to one function's one switch: a
// `default:` belonging to another switch in the same file, or a case label in
// another function, cannot satisfy it.
func caseLabelsOf(t *testing.T, file, fn, selector string) (labels []string, hasDefault bool) {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	var target *ast.FuncDecl
	for _, d := range parsed.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == fn {
			target = fd
			break
		}
	}
	if target == nil {
		t.Fatalf("%s declares no function %s; the dispatch moved and this test reads the wrong place", file, fn)
	}

	found := false
	ast.Inspect(target, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			return true
		}
		sel, ok := sw.Tag.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != selector {
			return true
		}
		found = true
		for _, stmt := range sw.Body.List {
			cc := stmt.(*ast.CaseClause)
			if cc.List == nil {
				hasDefault = true
				continue
			}
			for _, expr := range cc.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Errorf("%s: a case label that is not a string literal cannot be compared to the vocabulary: %v", fn, expr)
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s: %v", lit.Value, err)
				}
				labels = append(labels, v)
			}
		}
		return false
	})
	if !found {
		t.Fatalf("%s has no switch on .%s; the dispatch moved and this test reads the wrong place", fn, selector)
	}
	sort.Strings(labels)
	return labels, hasDefault
}

func TestDispatchDeclaresExactlyTheVocabulary(t *testing.T) {
	enum := runOperationEnum(t)
	sort.Strings(enum)

	for _, d := range []struct{ file, fn string }{
		{"kubernetes.go", "operationScript"},
		{"local.go", "Execute"},
	} {
		t.Run(d.fn, func(t *testing.T) {
			labels, hasDefault := caseLabelsOf(t, d.file, d.fn, "Operation")

			if !hasDefault {
				t.Error("the operation switch has no default, so an operation it does not know falls through it and the run is recorded as having succeeded")
			}

			inEnum := map[string]bool{}
			for _, op := range enum {
				inEnum[op] = true
			}
			seen := map[string]bool{}
			for _, l := range labels {
				seen[l] = true
				if !inEnum[l] {
					t.Errorf("the dispatch declares an arm for %q, which the database does not admit; nothing holds that arm to running anything", l)
				}
			}
			for _, op := range enum {
				if !seen[op] {
					t.Errorf("the database admits %q and the dispatch declares no arm for it", op)
				}
			}
		})
	}
}
