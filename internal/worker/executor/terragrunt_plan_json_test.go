package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Terragrunt does not run tofu where portal invoked it. It renders the module
// into .terragrunt-cache and runs tofu there, so a relative `-out=planfile`
// writes into the cache directory while the `show` that follows — invoked at the
// leaf — finds nothing. The JSON plan is silently absent for every terragrunt
// workspace, and it is the diff an approval is granted against.
//
// These tests stand up a leaf with a terragrunt.hcl and a fake `terragrunt` on
// PATH that reproduces exactly that: it chdirs into a cache directory before
// honouring the arguments it was given. A relative -out therefore lands
// somewhere `show` cannot reach, and an absolute one does not.

// fakeTerragrunt writes an executable named `terragrunt` into dir. It behaves
// the way the real one does for the two things this test turns on: it runs from
// a rendered cache directory, and it honours -out and `show` paths from there.
func fakeTerragrunt(t *testing.T, dir string) {
	t.Helper()
	script := `#!/bin/sh
# Reproduce terragrunt's rendered-cache behaviour: everything runs from a
# directory that is not the leaf portal invoked us in.
CACHE="$PWD/.terragrunt-cache/rendered"
mkdir -p "$CACHE"
cd "$CACHE" || exit 1

CMD="$1"
shift

case "$CMD" in
  init|validate)
    echo "terragrunt $CMD ok"
    ;;
  plan)
    OUT=""
    for arg in "$@"; do
      case "$arg" in
        -out=*) OUT="${arg#-out=}" ;;
      esac
    done
    echo "terragrunt plan ok"
    if [ -n "$OUT" ]; then
      # Resolved from the cache directory, exactly as tofu would.
      printf 'binary plan' > "$OUT"
    fi
    ;;
  show)
    FILE=""
    for arg in "$@"; do
      case "$arg" in
        -json) ;;
        -*) ;;
        *) FILE="$arg" ;;
      esac
    done
    if [ ! -f "$FILE" ]; then
      echo "terragrunt show: no such plan file: $FILE" >&2
      exit 1
    fi
    printf '{"format_version":"1.2","resource_changes":[]}'
    ;;
  *)
    echo "terragrunt: unhandled $CMD" >&2
    exit 1
    ;;
esac
`
	path := filepath.Join(dir, "terragrunt")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake terragrunt: %v", err)
	}
}

// terragruntArchive is an uploaded workspace whose working directory carries a
// terragrunt.hcl, which is what DetectBinary keys on, plus the parent tree a
// real terragrunt upload has to contain.
func terragruntArchive(t *testing.T) []byte {
	t.Helper()
	return tarGz(t, [][2]string{
		{"root.hcl", "remote_state {}\n"},
		{"envs/production/terragrunt.hcl", "include \"root\" {\n  path = find_in_parent_folders()\n}\ninputs = {}\n"},
	})
}

func TestPlanFilePathIsAbsolute(t *testing.T) {
	got := planFilePath("/work/envs/production")
	if !filepath.IsAbs(got) {
		t.Fatalf("planFilePath = %q, which terragrunt resolves against its rendered cache directory rather than the leaf", got)
	}
	if got != "/work/envs/production/planfile" {
		t.Errorf("planFilePath = %q", got)
	}
}

// The end-to-end shape: a terragrunt workspace must come back with a JSON plan.
func TestLocalExecutor_ProducesAJSONPlanForATerragruntWorkspace(t *testing.T) {
	bin := t.TempDir()
	fakeTerragrunt(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The wrapper detection this turns on: terragrunt.hcl at the leaf.
	probe := t.TempDir()
	if err := extractArchive(terragruntArchive(t), probe); err != nil {
		t.Fatalf("extract probe: %v", err)
	}
	if got := DetectBinary(filepath.Join(probe, "envs", "production")); got != "terragrunt" {
		t.Fatalf("DetectBinary = %q, want terragrunt; the rest of this test is not exercising the wrapper", got)
	}

	e := &LocalExecutor{}
	result, err := e.Execute(context.Background(), ExecuteParams{
		RunID:       "run_terragrunt",
		Operation:   "plan",
		Source:      "upload",
		WorkingDir:  "envs/production",
		ArchiveData: terragruntArchive(t),
		LogCallback: func([]byte) {},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.PlanJSON) == 0 {
		t.Fatal("a terragrunt plan produced no JSON plan; with the approval gate in place every approval-gated terragrunt workspace would be refused")
	}
	if !strings.Contains(string(result.PlanJSON), "format_version") {
		t.Errorf("PlanJSON is not a plan document: %q", result.PlanJSON)
	}
}

// The control: the same leaf without terragrunt.hcl runs under tofu, whose
// working directory is the leaf, and must still produce a JSON plan. An
// absolute path has to work for both wrappers, not trade one for the other.
func TestLocalExecutor_ProducesAJSONPlanForATofuWorkspace(t *testing.T) {
	bin := t.TempDir()
	fakeTofu(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	e := &LocalExecutor{}
	result, err := e.Execute(context.Background(), ExecuteParams{
		RunID:       "run_tofu",
		Operation:   "plan",
		Source:      "upload",
		WorkingDir:  "envs/production",
		ArchiveData: tarGz(t, [][2]string{{"envs/production/main.tf", "# empty\n"}}),
		LogCallback: func([]byte) {},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.PlanJSON) == 0 {
		t.Fatal("a tofu plan produced no JSON plan")
	}
}

// fakeTofu stays in the directory it was invoked in, as tofu does.
func fakeTofu(t *testing.T, dir string) {
	t.Helper()
	script := `#!/bin/sh
CMD="$1"
shift
case "$CMD" in
  init|validate)
    echo "tofu $CMD ok"
    ;;
  plan)
    OUT=""
    for arg in "$@"; do
      case "$arg" in
        -out=*) OUT="${arg#-out=}" ;;
      esac
    done
    echo "tofu plan ok"
    if [ -n "$OUT" ]; then printf 'binary plan' > "$OUT"; fi
    ;;
  show)
    FILE=""
    for arg in "$@"; do
      case "$arg" in
        -json) ;;
        -*) ;;
        *) FILE="$arg" ;;
      esac
    done
    if [ ! -f "$FILE" ]; then
      echo "tofu show: no such plan file: $FILE" >&2
      exit 1
    fi
    printf '{"format_version":"1.2","resource_changes":[]}'
    ;;
  *)
    echo "tofu: unhandled $CMD" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "tofu"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tofu: %v", err)
	}
}

// The pod script carries the same rule, and it is the executor production
// permits. A relative -out there fails the same way and cannot be caught by
// running anything, so the script text is the assertion.
func TestPodScript_NamesThePlanFileAbsolutely(t *testing.T) {
	e := &KubernetesExecutor{}
	script := e.buildScript(ExecuteParams{Operation: "plan", Source: "upload", WorkingDir: "envs/production"})

	if !strings.Contains(script, `PLANFILE="$PWD/planfile"`) {
		t.Fatalf("the script does not name the plan file absolutely:\n%s", script)
	}
	if strings.Contains(script, "-out=planfile") {
		t.Error("the script still passes a relative -out; terragrunt resolves it inside its rendered cache directory")
	}
	if !strings.Contains(script, `-out="$PLANFILE"`) {
		t.Error("the plan does not write to the absolute path")
	}
	if !strings.Contains(script, `$BIN show -json "$PLANFILE"`) {
		t.Error("the show does not read the absolute path, so it looks somewhere the plan did not write")
	}
	if strings.Contains(script, "if [ -f planfile ]") {
		t.Error("the existence test still looks at the leaf rather than at the file the plan wrote")
	}
}
