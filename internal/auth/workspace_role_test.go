package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// stubResolver stands in for the workspace_team_access read.
type stubResolver struct {
	role string
	err  error

	gotWorkspaceID string
	gotUserID      string
	gotOrgID       string
	calls          int
}

func (s *stubResolver) WorkspaceTeamRole(_ context.Context, workspaceID, userID, orgID string) (string, error) {
	s.calls++
	s.gotWorkspaceID, s.gotUserID, s.gotOrgID = workspaceID, userID, orgID
	return s.role, s.err
}

// serveWorkspaceRoute runs one request through a chi router that carries the
// gate, so {workspaceID} resolves the way it does in the real router.
func serveWorkspaceRoute(t *testing.T, gate func(http.Handler) http.Handler, user *UserContext) (*httptest.ResponseRecorder, string) {
	t.Helper()

	var seenRole string
	r := chi.NewRouter()
	r.With(gate).Get("/workspaces/{workspaceID}", func(w http.ResponseWriter, r *http.Request) {
		seenRole = WorkspaceRole(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/workspaces/ws-1", nil)
	if user != nil {
		req = req.WithContext(context.WithValue(req.Context(), UserContextKey, user))
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec, seenRole
}

func TestRequireWorkspaceRole_OrgRoleDecides(t *testing.T) {
	tests := []struct {
		name       string
		orgRole    string
		minRole    string
		wantStatus int
	}{
		{"viewer denied operator route", "viewer", "operator", http.StatusForbidden},
		{"viewer denied admin route", "viewer", "admin", http.StatusForbidden},
		{"operator denied admin route", "operator", "admin", http.StatusForbidden},
		{"unknown role denied viewer route", "intern", "viewer", http.StatusForbidden},
		{"empty role denied viewer route", "", "viewer", http.StatusForbidden},
		{"viewer allowed viewer route", "viewer", "viewer", http.StatusOK},
		{"operator allowed operator route", "operator", "operator", http.StatusOK},
		{"admin allowed admin route", "admin", "admin", http.StatusOK},
		{"owner allowed admin route", "owner", "admin", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &stubResolver{}
			gate := RequireWorkspaceRole(resolver, tt.minRole)
			rec, seenRole := serveWorkspaceRoute(t, gate, &UserContext{
				UserID: "user-1", OrgID: "org-1", Role: tt.orgRole,
			})

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK && seenRole != tt.orgRole {
				t.Errorf("WorkspaceRole = %q, want %q", seenRole, tt.orgRole)
			}
			if resolver.gotWorkspaceID != "ws-1" || resolver.gotUserID != "user-1" || resolver.gotOrgID != "org-1" {
				t.Errorf("resolver called with %q/%q/%q, want ws-1/user-1/org-1",
					resolver.gotWorkspaceID, resolver.gotUserID, resolver.gotOrgID)
			}
		})
	}
}

func TestRequireWorkspaceRole_GrantElevates(t *testing.T) {
	tests := []struct {
		name       string
		orgRole    string
		grant      string
		minRole    string
		wantStatus int
		wantRole   string
	}{
		{"grant lifts viewer to operator", "viewer", "operator", "operator", http.StatusOK, "operator"},
		{"grant lifts viewer to admin", "viewer", "admin", "admin", http.StatusOK, "admin"},
		{"grant lifts operator to admin", "operator", "admin", "admin", http.StatusOK, "admin"},
		{"grant below the bar still denies", "viewer", "operator", "admin", http.StatusForbidden, ""},
		{"unrecognised grant role cannot elevate", "viewer", "superuser", "operator", http.StatusForbidden, ""},
		{"no grant leaves org role alone", "viewer", "", "operator", http.StatusForbidden, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := RequireWorkspaceRole(&stubResolver{role: tt.grant}, tt.minRole)
			rec, seenRole := serveWorkspaceRoute(t, gate, &UserContext{
				UserID: "user-1", OrgID: "org-1", Role: tt.orgRole,
			})

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if seenRole != tt.wantRole {
				t.Errorf("WorkspaceRole = %q, want %q", seenRole, tt.wantRole)
			}
		})
	}
}

// A grant is never allowed to take authority away: an admin keeps admin on a
// workspace whose only grant names a lower role.
func TestRequireWorkspaceRole_GrantNeverRestricts(t *testing.T) {
	gate := RequireWorkspaceRole(&stubResolver{role: "viewer"}, "admin")
	rec, seenRole := serveWorkspaceRoute(t, gate, &UserContext{
		UserID: "user-1", OrgID: "org-1", Role: "admin",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d — a lower grant must not demote an org admin", rec.Code, http.StatusOK)
	}
	if seenRole != "admin" {
		t.Errorf("WorkspaceRole = %q, want admin", seenRole)
	}
}

func TestRequireWorkspaceRole_FailsClosed(t *testing.T) {
	t.Run("unreadable grant denies when the org role falls short", func(t *testing.T) {
		gate := RequireWorkspaceRole(&stubResolver{err: errors.New("database unreachable")}, "admin")
		rec, _ := serveWorkspaceRoute(t, gate, &UserContext{UserID: "user-1", OrgID: "org-1", Role: "viewer"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
	})

	t.Run("unreadable grant still allows what the org role already allowed", func(t *testing.T) {
		gate := RequireWorkspaceRole(&stubResolver{err: errors.New("database unreachable")}, "viewer")
		rec, seenRole := serveWorkspaceRoute(t, gate, &UserContext{UserID: "user-1", OrgID: "org-1", Role: "viewer"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if seenRole != "viewer" {
			t.Errorf("WorkspaceRole = %q, want viewer", seenRole)
		}
	})

	t.Run("no authenticated user is unauthorized", func(t *testing.T) {
		gate := RequireWorkspaceRole(&stubResolver{role: "owner"}, "viewer")
		rec, _ := serveWorkspaceRoute(t, gate, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("nil resolver denies rather than skipping the grant", func(t *testing.T) {
		gate := RequireWorkspaceRole(nil, "admin")
		rec, _ := serveWorkspaceRoute(t, gate, &UserContext{UserID: "user-1", OrgID: "org-1", Role: "operator"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
	})

	t.Run("route without a workspaceID cannot be elevated", func(t *testing.T) {
		resolver := &stubResolver{role: "owner"}
		r := chi.NewRouter()
		r.With(RequireWorkspaceRole(resolver, "admin")).Get("/workspaces", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
		req = req.WithContext(context.WithValue(req.Context(), UserContextKey,
			&UserContext{UserID: "user-1", OrgID: "org-1", Role: "viewer"}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
		if resolver.calls != 0 {
			t.Errorf("resolver called %d times for a route with no workspace", resolver.calls)
		}
	})
}

// RequireWorkspaceAction must resolve the same bar as the action's min role.
func TestRequireWorkspaceAction_UsesActionMinRole(t *testing.T) {
	tests := []struct {
		action     Action
		role       string
		wantStatus int
	}{
		{ActionViewWorkspace, "viewer", http.StatusOK},
		{ActionViewWorkspace, "bogus", http.StatusForbidden},
		{ActionCreateRun, "viewer", http.StatusForbidden},
		{ActionCreateRun, "operator", http.StatusOK},
		{ActionManageVars, "operator", http.StatusForbidden},
		{ActionManageVars, "admin", http.StatusOK},
		{ActionManageState, "operator", http.StatusForbidden},
		{ActionManageState, "admin", http.StatusOK},
		{ActionManageTeams, "operator", http.StatusForbidden},
		{ActionManageTeams, "admin", http.StatusOK},
		{ActionDeleteWorkspace, "operator", http.StatusForbidden},
		{ActionDeleteWorkspace, "admin", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(string(tt.action)+"/"+tt.role, func(t *testing.T) {
			gate := RequireWorkspaceAction(&stubResolver{}, tt.action)
			rec, _ := serveWorkspaceRoute(t, gate, &UserContext{UserID: "u", OrgID: "o", Role: tt.role})
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestMaxRole(t *testing.T) {
	tests := []struct {
		a, b, want string
	}{
		{"viewer", "admin", "admin"},
		{"admin", "viewer", "admin"},
		{"owner", "admin", "owner"},
		{"operator", "operator", "operator"},
		{"viewer", "", "viewer"},
		{"", "viewer", "viewer"},
		{"viewer", "root", "viewer"},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := MaxRole(tt.a, tt.b); got != tt.want {
			t.Errorf("MaxRole(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestWorkspaceRole_UnsetContext(t *testing.T) {
	if got := WorkspaceRole(context.Background()); got != "" {
		t.Errorf("WorkspaceRole on a bare context = %q, want empty", got)
	}
}
