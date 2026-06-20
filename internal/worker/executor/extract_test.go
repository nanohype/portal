package executor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// tarGz builds a gzipped tar from name->content entries (in order).
func tarGz(t *testing.T, entries [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		name, content := e[0], e[1]
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("write header %q: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write body %q: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func TestExtractArchive_Valid(t *testing.T) {
	dir := t.TempDir()
	data := tarGz(t, [][2]string{{"main.tf", "x"}, {"modules/vpc/main.tf", "y"}})
	if err := extractArchive(data, dir); err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "modules/vpc/main.tf")); err != nil || string(b) != "y" {
		t.Errorf("nested file not extracted correctly: %q, %v", b, err)
	}
}

// TestExtractArchive_ZipSlip proves a traversal entry can't escape destDir.
func TestExtractArchive_ZipSlip(t *testing.T) {
	for _, name := range []string{"../escape.tf", "foo/../../escape.tf", "a/b/../../../escape.tf"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			err := extractArchive(tarGz(t, [][2]string{{name, "pwned"}}), dir)
			if err == nil {
				t.Fatalf("extractArchive accepted traversal entry %q", name)
			}
			// Nothing should have been written outside dir.
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escape.tf")); statErr == nil {
				t.Errorf("traversal entry %q escaped destDir", name)
			}
		})
	}
}
