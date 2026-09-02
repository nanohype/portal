package executor

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// An executor is a strategy, and a strategy that silently handles a subset of
// its inputs is worse than one that handles none: the operation it skipped is
// reported as having run. The Kubernetes executor rendered a script with no
// operation in it for an `import`, the pod exited 0, and the worker wrote the
// run back as applied — the resource an operator asked portal to adopt was never
// adopted, and nothing in the run said so.
//
// These tests hold the dispatch closed from both ends: every operation the
// database admits is rendered, and anything else is refused.

// runOperationEnum reads the vocabulary from the migration that defines it.
// CLAUDE.md makes that enum the one source of truth, so reading it here is what
// stops this list becoming a second one that can drift.
func runOperationEnum(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile("../../../migrations/000001_initial_schema.up.sql")
	if err != nil {
		t.Fatalf("read the schema that defines run_operation: %v", err)
	}
	block := regexp.MustCompile(`(?s)CREATE TYPE run_operation AS ENUM \((.*?)\);`).FindStringSubmatch(string(body))
	if block == nil {
		t.Fatal("no run_operation enum in the initial schema; the vocabulary moved and this test is reading the wrong file")
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

func TestOperationsMatchTheRunOperationEnum(t *testing.T) {
	enum := runOperationEnum(t)

	inEnum := map[string]bool{}
	for _, op := range enum {
		inEnum[op] = true
	}
	inList := map[string]bool{}
	for _, op := range Operations {
		inList[op] = true
	}

	for _, op := range enum {
		if !inList[op] {
			t.Errorf("the database admits operation %q and executor.Operations does not list it, so no executor is held to rendering it", op)
		}
	}
	for _, op := range Operations {
		if !inEnum[op] {
			t.Errorf("executor.Operations lists %q and the database does not admit it; the list has drifted from the vocabulary", op)
		}
	}
}

// The Kubernetes executor is the only one production permits, so every
// operation the database admits has to reach commands in its script.
func TestPodScript_RendersCommandsForEveryOperation(t *testing.T) {
	e := &KubernetesExecutor{}

	for _, op := range runOperationEnum(t) {
		t.Run(op, func(t *testing.T) {
			params := ExecuteParams{Operation: op, Source: "upload", WorkingDir: "envs/production"}
			if op == "import" {
				params.ImportResources = []ImportResource{{Address: "aws_s3_bucket.logs", ID: "portal-logs"}}
			}

			script, err := e.buildScript(params)
			if err != nil {
				t.Fatalf("the production executor renders nothing for %q: %v", op, err)
			}

			// The tail every script shares stops here; what has to differ is the
			// operation itself. An operation that renders no command for itself
			// is the defect: the pod exits 0 having done nothing.
			body := script[strings.Index(script, "if [ -f portal.auto.tfvars ]"):]
			if !strings.Contains(body, "$BIN "+op) && !strings.Contains(body, "smoke-test.sh") {
				t.Errorf("the script for %q runs no %s command:\n%s", op, op, body)
			}
		})
	}
}

// Anything the dispatch does not know is refused, so a value added to the
// database enum and to Operations cannot reach production as a pod that runs
// nothing while the two tests above are being written.
func TestPodScript_RefusesAnOperationItCannotRender(t *testing.T) {
	e := &KubernetesExecutor{}
	for _, op := range []string{"", "refresh", "taint", "apply-all", "IMPORT"} {
		script, err := e.buildScript(ExecuteParams{Operation: op, Source: "upload"})
		if err == nil {
			t.Errorf("buildScript(%q) rendered a script with no operation in it; the pod would exit 0 and the run be recorded as succeeded:\n%s", op, script)
			continue
		}
		if !strings.Contains(err.Error(), op) && op != "" {
			t.Errorf("the refusal for %q does not name it: %v", op, err)
		}
	}
}

// The local executor dispatches over the same set. The two executors handling
// different subsets is how a workspace's behaviour came to depend on which one
// ran it.
func TestLocalExecutor_HandlesEveryOperationTheEnumAdmits(t *testing.T) {
	body, err := os.ReadFile("local.go")
	if err != nil {
		t.Fatalf("read local.go: %v", err)
	}
	src := string(body)
	for _, op := range runOperationEnum(t) {
		if !strings.Contains(src, `case "`+op+`"`) {
			t.Errorf("the local executor has no case for %q, so it renders nothing and returns a clean result", op)
		}
	}
	if !strings.Contains(src, "default:") {
		t.Error("the local executor's operation switch has no default, so an unknown operation falls through it")
	}
}
