package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nanohype/portal/internal/auth"
)

// stubWorkspaceGrants answers the grant lookup from a map keyed by
// "userID/workspaceID".
type stubWorkspaceGrants struct {
	grants map[string]string
	err    error
}

func (s stubWorkspaceGrants) WorkspaceTeamRole(_ context.Context, workspaceID, userID, _ string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.grants[userID+"/"+workspaceID], nil
}

func requestAs(role string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/dest/variables/copy", nil)
	ctx := context.WithValue(req.Context(), auth.UserContextKey, &auth.UserContext{
		UserID: "user-1", OrgID: "org-1", Role: role,
	})
	return req.WithContext(ctx)
}

// /copy and /import-outputs name their SOURCE workspace in the request body, so
// the route's gate — which only ever covers the workspace in the path — cannot
// authorize it. These pin that the handler does.
func TestAuthorizeSourceWorkspace(t *testing.T) {
	tests := []struct {
		name    string
		orgRole string
		grants  map[string]string
		source  string
		want    bool
	}{
		{
			name:    "org admin reaches any workspace in the org",
			orgRole: "admin",
			source:  "source-ws",
			want:    true,
		},
		{
			name:    "org owner reaches any workspace in the org",
			orgRole: "owner",
			source:  "source-ws",
			want:    true,
		},
		{
			// The exploit: elevated to admin on the destination by a team grant,
			// then naming any other workspace as the source to bulk-copy its
			// variables — sensitive ciphertext included — somewhere readable.
			name:    "a grant on the destination does not reach the source",
			orgRole: "viewer",
			grants:  map[string]string{"user-1/dest": "admin"},
			source:  "source-ws",
			want:    false,
		},
		{
			name:    "a grant on the source itself does reach it",
			orgRole: "viewer",
			grants:  map[string]string{"user-1/dest": "admin", "user-1/source-ws": "admin"},
			source:  "source-ws",
			want:    true,
		},
		{
			// The grant has to clear the same bar the destination did; an
			// operator grant on the source is not enough to move its variables.
			name:    "an operator grant on the source is below the bar",
			orgRole: "viewer",
			grants:  map[string]string{"user-1/source-ws": "operator"},
			source:  "source-ws",
			want:    false,
		},
		{
			name:    "an org operator cannot reach a source they hold nothing on",
			orgRole: "operator",
			source:  "source-ws",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &VariableHandler{authz: stubWorkspaceGrants{grants: tt.grants}}
			if got := h.authorizeSourceWorkspace(requestAs(tt.orgRole), tt.source); got != tt.want {
				t.Errorf("authorizeSourceWorkspace = %v, want %v", got, tt.want)
			}
		})
	}
}

// An unreadable grant denies. Without this the failure mode of a database blip
// would be to fall through to whatever the org role alone allowed.
func TestAuthorizeSourceWorkspaceFailsClosed(t *testing.T) {
	h := &VariableHandler{authz: stubWorkspaceGrants{err: errors.New("lookup unavailable")}}
	if h.authorizeSourceWorkspace(requestAs("viewer"), "source-ws") {
		t.Error("authorizeSourceWorkspace allowed the source despite an unreadable grant")
	}

	// An org admin still clears it — the error only costs the elevation, and
	// admin never needed one.
	if !h.authorizeSourceWorkspace(requestAs("admin"), "source-ws") {
		t.Error("an org admin should still reach the source when the grant lookup fails")
	}
}

// No resolver wired at all must not read as "everyone is allowed".
func TestAuthorizeSourceWorkspaceWithoutResolver(t *testing.T) {
	h := &VariableHandler{}
	if h.authorizeSourceWorkspace(requestAs("viewer"), "source-ws") {
		t.Error("a viewer reached the source with no resolver wired")
	}
	if h.authorizeSourceWorkspace(requestAs("operator"), "source-ws") {
		t.Error("an operator reached the source with no resolver wired")
	}
}
