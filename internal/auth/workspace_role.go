package auth

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/handler/respond"
)

// WorkspaceRoleResolver reports the highest role any of the caller's teams
// holds on one workspace through workspace_team_access. It returns an empty
// role when no grant applies — including when the workspace or the grant's
// team belongs to a different org.
type WorkspaceRoleResolver interface {
	WorkspaceTeamRole(ctx context.Context, workspaceID, userID, orgID string) (string, error)
}

const workspaceRoleContextKey contextKey = "workspace_role"

// WorkspaceRole returns the effective role a workspace gate computed for this
// request: the caller's org role, raised to a workspace_team_access grant when
// one of their teams holds a higher role on this workspace. It is empty when
// the request did not pass a workspace gate, so callers must treat "" as
// "no authority".
func WorkspaceRole(ctx context.Context) string {
	role, _ := ctx.Value(workspaceRoleContextKey).(string)
	return role
}

// RequireWorkspaceAction gates a workspace-scoped route on an action, using the
// caller's effective role on the workspace named by the {workspaceID} URL
// parameter.
func RequireWorkspaceAction(resolver WorkspaceRoleResolver, action Action) func(http.Handler) http.Handler {
	return RequireWorkspaceRole(resolver, minRoleForAction(action))
}

// RequireWorkspaceRole gates a workspace-scoped route on a minimum effective
// role.
//
// Team grants ELEVATE ONLY. The effective role is the higher of the caller's
// org role and the best grant their teams hold on this workspace; a grant can
// raise what someone may do on one workspace but can never take away what
// their org role already allows. The alternative — letting a grant restrict —
// is unsafe here because grants are sparse: almost every workspace has no row
// at all, so "no row" would have to mean either "nobody has access" (which
// locks owners out of every ungranted workspace) or "org role applies" (which
// turns adding a viewer grant into a way to demote an admin on that
// workspace). Elevate-only keeps one meaning for a missing row and makes every
// existing row additive.
//
// The gate fails closed. No user in context is a 401. A missing {workspaceID},
// an unreadable grant, or a grant naming a role this server does not recognise
// all resolve to no elevation, which denies whenever the org role alone did not
// already clear the bar.
func RequireWorkspaceRole(resolver WorkspaceRoleResolver, minRole string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := GetUser(r.Context())
			if user == nil {
				respond.Error(w, http.StatusUnauthorized, "not authenticated")
				return
			}

			effective := EffectiveWorkspaceRole(r.Context(), resolver, user, chi.URLParam(r, "workspaceID"))
			if roleLevel(effective) < roleLevel(minRole) {
				respond.Error(w, http.StatusForbidden, "requires "+minRole+" role or higher on this workspace")
				return
			}

			ctx := context.WithValue(r.Context(), workspaceRoleContextKey, effective)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// EffectiveWorkspaceRole returns the caller's role on ONE named workspace: the
// higher of their org role and the best grant their teams hold there.
//
// The middleware covers the workspace in the URL. This is for the handful of
// endpoints that also address a SECOND workspace named in the request body —
// copying variables out of one workspace, importing another's outputs. Those
// have to be authorized against the workspace they read, not only the one they
// write, or the body becomes a way to reach past the gate.
//
// Every failure path yields no elevation, so an unreadable grant can only deny.
func EffectiveWorkspaceRole(ctx context.Context, resolver WorkspaceRoleResolver, user *UserContext, workspaceID string) string {
	if user == nil {
		return ""
	}
	if workspaceID == "" || resolver == nil {
		return user.Role
	}

	granted, err := resolver.WorkspaceTeamRole(ctx, workspaceID, user.UserID, user.OrgID)
	if err != nil {
		slog.ErrorContext(ctx, "workspace team grant lookup failed, denying elevation",
			"workspace_id", workspaceID, "user_id", user.UserID, "error", err)
		return user.Role
	}
	return MaxRole(user.Role, granted)
}
