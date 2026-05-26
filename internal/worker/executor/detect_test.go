package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectBinary(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "terragrunt.hcl present → terragrunt",
			files: map[string]string{"terragrunt.hcl": "include \"root\" { path = find_in_parent_folders(\"root.hcl\") }"},
			want:  "terragrunt",
		},
		{
			name:  "only .tf files → tofu",
			files: map[string]string{"main.tf": "resource \"null_resource\" \"x\" {}"},
			want:  "tofu",
		},
		{
			name:  "empty directory → tofu",
			files: nil,
			want:  "tofu",
		},
		{
			// Terragrunt is the higher-level wrapper; if both are present, it owns the run.
			name: "both terragrunt.hcl and .tf present → terragrunt",
			files: map[string]string{
				"terragrunt.hcl": "include \"root\" { path = find_in_parent_folders(\"root.hcl\") }",
				"main.tf":        "resource \"null_resource\" \"x\" {}",
			},
			want: "terragrunt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}
			got := DetectBinary(dir)
			if got != tc.want {
				t.Errorf("DetectBinary(%q) = %q, want %q", dir, got, tc.want)
			}
		})
	}
}
