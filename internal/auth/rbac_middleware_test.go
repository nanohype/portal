package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// serveWith runs mw around a handler that records whether it was reached, with
// user (nil for anonymous) in the request context.
func serveWith(t *testing.T, mw func(http.Handler) http.Handler, user *UserContext) (*httptest.ResponseRecorder, bool) {
	t.Helper()

	var reached bool
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if user != nil {
		req = req.WithContext(context.WithValue(req.Context(), UserContextKey, user))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, reached
}

func TestRequireRole(t *testing.T) {
	tests := []struct {
		name     string
		minRole  string
		user     *UserContext
		wantCode int
		wantNext bool
	}{
		{
			// No identity in context at all — the middleware must not fall through
			// to a role comparison against the zero value, which roleLevel would
			// score as the lowest role rather than as "unauthenticated".
			name: "anonymous is 401, not 403", minRole: "viewer", user: nil,
			wantCode: http.StatusUnauthorized, wantNext: false,
		},
		{
			name: "below the bar is 403", minRole: "admin", user: &UserContext{Role: "operator"},
			wantCode: http.StatusForbidden, wantNext: false,
		},
		{
			name: "exactly at the bar passes", minRole: "admin", user: &UserContext{Role: "admin"},
			wantCode: http.StatusOK, wantNext: true,
		},
		{
			name: "above the bar passes", minRole: "operator", user: &UserContext{Role: "owner"},
			wantCode: http.StatusOK, wantNext: true,
		},
		{
			// An unrecognised role must not out-rank a known one.
			name: "unknown role is below every bar", minRole: "viewer", user: &UserContext{Role: "nonsense"},
			wantCode: http.StatusForbidden, wantNext: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, reached := serveWith(t, RequireRole(tt.minRole), tt.user)
			if rec.Code != tt.wantCode {
				t.Errorf("status: got %d want %d", rec.Code, tt.wantCode)
			}
			if reached != tt.wantNext {
				t.Errorf("next handler reached = %v, want %v", reached, tt.wantNext)
			}
		})
	}
}

// TestRequireAction pins that the action middleware resolves through the same
// role table CanPerform uses. A drift between them would mean an endpoint gated
// by RequireAction admits a role that CanPerform rejects for the same action.
func TestRequireAction(t *testing.T) {
	allActions := []Action{
		ActionViewWorkspace, ActionCreateRun, ActionApplyRun, ActionApplyProd,
		ActionDestroyRun, ActionManageState, ActionManageVars, ActionRevealSecret,
		ActionManageTeams, ActionManageOrg, ActionDeleteWorkspace,
	}
	for _, action := range allActions {
		for _, role := range []string{"viewer", "operator", "admin", "owner"} {
			_, reached := serveWith(t, RequireAction(action), &UserContext{Role: role})
			if want := CanPerform(role, action); reached != want {
				t.Errorf("RequireAction(%s) admitted %s = %v, but CanPerform says %v",
					action, role, reached, want)
			}
		}
	}
}

func TestRequireAction_Anonymous(t *testing.T) {
	rec, reached := serveWith(t, RequireAction(ActionViewWorkspace), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", rec.Code)
	}
	if reached {
		t.Error("anonymous request reached the handler")
	}
}

// TestMinRoleForAction_RevealSecretMatchesWrites is the one entry in the table
// with a written rationale: handing back a stored secret's plaintext discloses
// at least as much as editing it, so it sits at the write tier and not below.
func TestMinRoleForAction_RevealSecretMatchesWrites(t *testing.T) {
	if got, want := minRoleForAction(ActionRevealSecret), minRoleForAction(ActionManageVars); got != want {
		t.Errorf("reveal-secret is %q but managing the same variables is %q; reading a secret must not be the cheaper action", got, want)
	}
	if CanPerform("operator", ActionRevealSecret) {
		t.Error("an operator can reveal a stored secret")
	}
}

// TestMinRoleForAction_UnknownActionIsOwner: the fallback has to be the most
// restrictive role. An action added to the enum but forgotten here must fail
// closed rather than default to something an operator can do.
func TestMinRoleForAction_UnknownActionIsOwner(t *testing.T) {
	if got := minRoleForAction(Action("action_that_does_not_exist")); got != "owner" {
		t.Errorf("unknown action resolves to %q; it must fail closed to owner", got)
	}
}
