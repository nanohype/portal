package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/tfstate"
)

// stateRequestAs builds a request as the workspace gates leave it: the caller's
// org role in the user context, and the effective workspace role the gate
// resolved in the request context. An empty effective role is what a request
// that never passed a workspace gate looks like.
func stateRequestAs(effectiveRole string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/ws-1/state/current/resources", nil)
	ctx := context.WithValue(req.Context(), auth.UserContextKey, &auth.UserContext{
		UserID: "user-1", OrgID: "org-1", Role: effectiveRole,
	})
	if effectiveRole != "" {
		ctx = auth.ContextWithWorkspaceRole(ctx, effectiveRole)
	}
	return req.WithContext(ctx)
}

// The resource browser and the state diff sit at the workspace READ bar so
// everyone who can see a workspace can see what it manages. The attribute
// VALUES in those responses are the tfstate blob's own contents — tofu writes
// random_password.result and tls_private_key.private_key_pem into state in
// cleartext — and the raw download of those same bytes sits at
// ActionManageState. This pins that the values follow the download.
func TestAttributeViewFollowsTheStateBar(t *testing.T) {
	tests := []struct {
		name          string
		effectiveRole string
		want          tfstate.AttributeView
	}{
		// THE EXPLOIT: any grant or org role at or above viewer reading every
		// generated password and private key in a workspace's state without
		// ever clearing the admin bar the download sits at.
		{"viewer gets the inventory only", "viewer", tfstate.AttributesRedacted},
		{"operator gets the inventory only", "operator", tfstate.AttributesRedacted},
		{"unknown role gets the inventory only", "intern", tfstate.AttributesRedacted},

		// No gate ran, or the gate resolved nothing: no authority, no values.
		{"no effective role redacts", "", tfstate.AttributesRedacted},

		// The legitimate case: whoever may download the tfstate reads the same
		// bytes through the parsed view, including via a team grant that raised
		// them to admin on this one workspace.
		{"admin reads state whole", "admin", tfstate.AttributesFull},
		{"owner reads state whole", "owner", tfstate.AttributesFull},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := attributeView(stateRequestAs(tt.effectiveRole)); got != tt.want {
				t.Errorf("attributeView(effective=%q) = %v, want %v", tt.effectiveRole, got, tt.want)
			}
		})
	}
}

// The bar it reads is the workspace-effective role, not the org role: a team
// grant of admin on one workspace is how a non-admin legitimately manages that
// workspace's state, and it must buy the same view there — and nowhere else.
func TestAttributeViewReadsTheWorkspaceRoleNotTheOrgRole(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/ws-1/state/current/resources", nil)
	ctx := context.WithValue(req.Context(), auth.UserContextKey, &auth.UserContext{
		UserID: "user-1", OrgID: "org-1", Role: "viewer",
	})

	// Granted admin on this workspace: the values are theirs here.
	granted := auth.ContextWithWorkspaceRole(ctx, auth.MaxRole("viewer", "admin"))
	if got := attributeView(req.WithContext(granted)); got != tfstate.AttributesFull {
		t.Errorf("a workspace admin grant must read state whole, got %v", got)
	}

	// Same org viewer on a workspace they hold nothing on: the gate resolves
	// their org role, and the values stay withheld.
	ungranted := auth.ContextWithWorkspaceRole(ctx, auth.MaxRole("viewer", ""))
	if got := attributeView(req.WithContext(ungranted)); got != tfstate.AttributesRedacted {
		t.Errorf("an org viewer with no grant must not read attribute values, got %v", got)
	}
}
