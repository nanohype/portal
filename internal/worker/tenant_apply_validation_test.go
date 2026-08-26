package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/git"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/tenantmanifest"
)

// seedRemote builds a bare repo with one commit on `main` and returns a file://
// URL for it. The tenant write path clones a real remote, so asserting what it
// commits means giving it one rather than a stub: a stub would prove the test's
// own model of the path, not the path.
func seedRemote(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "tenants.git")
	if _, err := gogit.PlainInit(bare, true); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	repo, err := gogit.PlainInit(seed, false)
	if err != nil {
		t.Fatalf("init seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("tenants\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("seed", &gogit.CommitOptions{
		Author: &object.Signature{Name: "seed", Email: "seed@example.invalid"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := repo.CreateRemote(&gogitconfig.RemoteConfig{Name: "origin", URLs: []string{bare}}); err != nil {
		t.Fatalf("remote: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	// Push whatever PlainInit named the branch onto refs/heads/main, so the
	// seed does not depend on go-git's default branch name.
	if err := repo.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gogitconfig.RefSpec{gogitconfig.RefSpec(head.Name().String() + ":refs/heads/main")},
	}); err != nil && err != gogit.NoErrAlreadyUpToDate {
		t.Fatalf("push seed: %v", err)
	}
	bareRepo, err := gogit.PlainOpen(bare)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	if _, err := bareRepo.Reference(plumbing.NewBranchReferenceName("main"), true); err != nil {
		t.Fatalf("seed did not land on refs/heads/main: %v", err)
	}
	return "file://" + bare
}

// remoteHasFile reports whether `main` in the bare remote carries relPath.
func remoteHasFile(t *testing.T, url, relPath string) bool {
	t.Helper()
	dir := t.TempDir()
	if _, err := gogit.PlainClone(dir, false, &gogit.CloneOptions{
		URL:           strings.TrimPrefix(url, "file://"),
		ReferenceName: plumbing.NewBranchReferenceName("main"),
		SingleBranch:  true,
	}); err != nil {
		t.Fatalf("clone remote for inspection: %v", err)
	}
	_, err := os.Stat(filepath.Join(dir, relPath))
	return err == nil
}

func newValidatorForTest(t *testing.T) *tenantmanifest.Validator {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// tenantmanifest.New() resolves its vendored schemas relative to the
	// package, so run it from there.
	if err := os.Chdir(filepath.Join("..", "tenantmanifest")); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	v, err := tenantmanifest.New()
	if err != nil {
		t.Fatalf("build validator: %v", err)
	}
	return v
}

type recordedCompletion struct {
	status string
	errMsg string
}

// buildTenantApplyWorker stands up the real write path against a real remote,
// with only the database and the chart render replaced.
func buildTenantApplyWorker(t *testing.T, url, manifest string, v *tenantmanifest.Validator, got *recordedCompletion) *TenantApplyJobWorker {
	t.Helper()
	repo, err := git.NewRepo(filepath.Join(t.TempDir(), "work"), url, "", "")
	if err != nil {
		t.Fatalf("new repo: %v", err)
	}
	return NewTenantApplyJobWorker(TenantApplyDeps{
		LoadCluster: func(context.Context, string, string) (repository.Cluster, error) {
			return repository.Cluster{ID: "c1", OrgID: "o1", Name: "hub"}, nil
		},
		LoadOp: func(context.Context, string, string) (repository.TenantOperation, error) {
			return repository.TenantOperation{
				ID: "op1", OrgID: "o1", ClusterID: "c1",
				TenantName: "competitive-intelligence", Operation: "create",
				Status: "pending", ValuesJSON: json.RawMessage(`{}`), CreatedBy: "u1",
			}, nil
		},
		CompleteOp: func(_ context.Context, _, _, status, _, errMsg string) error {
			got.status, got.errMsg = status, errMsg
			return nil
		},
		Render: func(context.Context, string, string, string, map[string]interface{}) (string, error) {
			return manifest, nil
		},
		Manifests:   v,
		TenantsRepo: repo,
		RepoMu:      &sync.Mutex{},
		TenantsRef:  "main",
		Author:      git.Author{Name: "portal", Email: "portal@example.invalid"},
	})
}

const manifestPath = "tenants/hub/competitive-intelligence.yaml"

// A manifest the vendored CRD schemas reject. The kind is not one portal
// renders, so no vendored schema covers it — the validator's own "portal
// validates only the tenant CRs it renders" branch.
const rejectedManifest = `apiVersion: platform.nanohype.dev/v1alpha1
kind: NotAThingTheOperatorKnows
metadata:
  name: competitive-intelligence
spec:
  tenant: strategy
`

// TestTenantApply_RejectedManifestIsNeverCommitted is the assertion the write
// path exists to hold: what reaches the tenants repo has been checked against
// the operator's CRD schemas. Without the check the manifest lands, ArgoCD
// picks it up, and the apiserver's rejection surfaces to whoever is watching
// ArgoCD rather than to the person who filled in the form.
func TestTenantApply_RejectedManifestIsNeverCommitted(t *testing.T) {
	url := seedRemote(t)
	var got recordedCompletion
	w := buildTenantApplyWorker(t, url, rejectedManifest, newValidatorForTest(t), &got)

	err := w.Work(context.Background(), tenantApplyJob())

	if remoteHasFile(t, url, manifestPath) {
		t.Fatalf("a manifest the CRD schemas reject was committed to the tenants repo at %s", manifestPath)
	}
	if got.status != "failed" {
		t.Fatalf("operation status = %q, want %q (err=%v)", got.status, "failed", err)
	}
	if !strings.Contains(got.errMsg, "NotAThingTheOperatorKnows") {
		t.Errorf("error message does not name the offending kind, so an operator cannot act on it: %q", got.errMsg)
	}
}

// TestTenantApply_ValidManifestIsCommitted guards the other direction: a check
// that blocks everything is not a check. Without this, deleting the render call
// entirely would pass the test above.
func TestTenantApply_ValidManifestIsCommitted(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "tenantmanifest", "testdata", "valid.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	url := seedRemote(t)
	var got recordedCompletion
	w := buildTenantApplyWorker(t, url, string(fixture), newValidatorForTest(t), &got)

	if err := w.Work(context.Background(), tenantApplyJob()); err != nil {
		t.Fatalf("Work returned %v, want nil", err)
	}
	if !remoteHasFile(t, url, manifestPath) {
		t.Fatalf("a manifest the CRD schemas accept was not committed to %s", manifestPath)
	}
	if got.status != "committed" {
		t.Fatalf("operation status = %q, want %q", got.status, "committed")
	}
}

// TestTenantApply_NilValidatorFailsClosed pins the direction of the failure. A
// worker built without a validator must refuse the write, not perform it
// unchecked — the deps comment calls the validator required, and a requirement
// nothing enforces is a comment.
func TestTenantApply_NilValidatorFailsClosed(t *testing.T) {
	url := seedRemote(t)
	var got recordedCompletion
	w := buildTenantApplyWorker(t, url, rejectedManifest, nil, &got)

	_ = w.Work(context.Background(), tenantApplyJob())

	if remoteHasFile(t, url, manifestPath) {
		t.Fatal("a worker with no validator committed a manifest; a missing validator must fail the operation, not skip the check")
	}
	if got.status != "failed" {
		t.Fatalf("operation status = %q, want %q", got.status, "failed")
	}
}

func tenantApplyJob() *river.Job[TenantApplyJobArgs] {
	return &river.Job[TenantApplyJobArgs]{
		Args: TenantApplyJobArgs{OperationID: "op1", OrgID: "o1"},
	}
}
