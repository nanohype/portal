package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSafeAbsRejectsTraversal is the load-bearing security test for the
// write path. Tenant names eventually become path segments — a malicious
// or fat-fingered name like `../../../etc/passwd` must never produce a
// write outside the workdir.
func TestSafeAbsRejectsTraversal(t *testing.T) {
	workdir := t.TempDir()
	r := &Repo{workdir: workdir}

	tests := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{"normal path", "tenants/prod/x.yaml", false},
		{"nested path", "a/b/c/d.yaml", false},
		{"empty", "", true},
		{"absolute", "/etc/passwd", true},
		{"parent traversal", "../escape.yaml", true},
		{"deep parent traversal", "../../../../../../tmp/x", true},
		{"dot-prefixed but inside", "./tenants/x.yaml", false},
		{"trailing dotdot", "tenants/..", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			abs, err := r.safeAbs(tc.rel)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got abs=%q", tc.rel, abs)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.rel, err)
				return
			}
			if !strings.HasPrefix(abs, workdir+string(filepath.Separator)) && abs != workdir {
				t.Errorf("path %q resolved outside workdir: %q", tc.rel, abs)
			}
		})
	}
}

// TestWriteFileCreatesParents — WriteFile should make nested directories so
// callers don't have to think about it. Worker callers build paths like
// `tenants/<cluster>/<name>.yaml` and expect them to land regardless of
// whether the cluster directory existed before.
func TestWriteFileCreatesParents(t *testing.T) {
	workdir := t.TempDir()
	r := &Repo{workdir: workdir}
	if err := r.WriteFile("tenants/clusterA/foo.yaml", []byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(workdir, "tenants/clusterA/foo.yaml"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", string(got), "hello")
	}
}

// TestRemoveFile — removing a non-existent file is not an error, so the delete
// worker can be invoked safely even if a prior create never completed. But the
// two outcomes have to be distinguishable: a caller that committed afterward
// would see the same clean tree either way, and report a teardown it never did.
func TestRemoveFile(t *testing.T) {
	workdir := t.TempDir()
	r := &Repo{workdir: workdir}

	removed, err := r.RemoveFile("tenants/nope/missing.yaml")
	if err != nil {
		t.Errorf("expected nil for missing file, got %v", err)
	}
	if removed {
		t.Error("removed = true for a file that never existed")
	}

	if err := r.WriteFile("tenants/acme/app.yaml", []byte("kind: Platform\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	removed, err = r.RemoveFile("tenants/acme/app.yaml")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed {
		t.Error("removed = false after removing a file that existed")
	}
}
