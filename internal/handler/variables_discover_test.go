package handler

import (
	"context"
	"testing"

	"github.com/nanohype/portal/internal/auth"
)

// /discover sits on the read bar so anyone who can see a workspace can see what
// its config declares. The values are a separate question: they carry whatever
// terragrunt resolved — get_env() against the server's own environment, a
// dependency's output — so they answer only to the bar that writing variables
// answers to.
func TestDiscoverIncludesValues(t *testing.T) {
	tests := []struct {
		name string
		role string
		want bool
	}{
		// The exploit: an org viewer with no grant reads the workspace, and
		// before the value bar existed the response carried every resolved
		// input and module default in the config.
		{"no gate resolved a role", "", false},
		{"viewer", "viewer", false},
		{"operator", "operator", false},
		{"unrecognised role", "intern", false},
		// The legitimate case: the people who fill these values in are the ones
		// who get to see them, including a viewer holding an admin team grant
		// on this one workspace (the gate resolves that to "admin").
		{"admin", "admin", true},
		{"owner", "owner", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.role != "" {
				ctx = auth.ContextWithWorkspaceRole(ctx, tt.role)
			}
			if got := discoverIncludesValues(ctx); got != tt.want {
				t.Errorf("discoverIncludesValues(role=%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}
