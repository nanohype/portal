package auth

import (
	"fmt"
	"net/http"

	"github.com/nanohype/portal/internal/handler/respond"
)

// Action represents a permission-checked action.
type Action string

const (
	ActionViewWorkspace   Action = "view_workspace"
	ActionCreateRun       Action = "create_run"
	ActionApplyRun        Action = "apply_run"
	ActionApplyProd       Action = "apply_prod"
	ActionDestroyRun      Action = "destroy_run"
	ActionManageState     Action = "manage_state"
	ActionManageVars      Action = "manage_vars"
	ActionManageTeams     Action = "manage_teams"
	ActionManageOrg       Action = "manage_org"
	ActionDeleteWorkspace Action = "delete_workspace"
)

// roleLevel returns the numeric level for a role (higher = more permissions).
func roleLevel(role string) int {
	switch role {
	case "owner":
		return 4
	case "admin":
		return 3
	case "operator":
		return 2
	case "viewer":
		return 1
	default:
		return 0
	}
}

// minRoleForAction returns the minimum role required for an action.
func minRoleForAction(action Action) string {
	switch action {
	case ActionViewWorkspace:
		return "viewer"
	case ActionCreateRun:
		return "operator"
	case ActionApplyRun:
		return "operator"
	case ActionApplyProd:
		return "admin"
	case ActionDestroyRun, ActionManageState, ActionDeleteWorkspace:
		return "admin"
	case ActionManageVars:
		return "admin"
	case ActionManageTeams:
		return "admin"
	case ActionManageOrg:
		return "owner"
	default:
		return "owner"
	}
}

// CanPerform checks if a role can perform an action.
func CanPerform(role string, action Action) bool {
	return roleLevel(role) >= roleLevel(minRoleForAction(action))
}

// ActionForOperation maps a run operation to the action it requires. apply and
// destroy are the destructive operations and carry their own (higher) min-role;
// plan/test/import are gated at the create_run baseline.
func ActionForOperation(operation string) Action {
	switch operation {
	case "apply":
		return ActionApplyRun
	case "destroy":
		return ActionDestroyRun
	default: // plan, test, import
		return ActionCreateRun
	}
}

// RequireRole returns middleware that enforces a minimum role.
func RequireRole(minRole string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := GetUser(r.Context())
			if user == nil {
				respond.Error(w, http.StatusUnauthorized, "not authenticated")
				return
			}
			if roleLevel(user.Role) < roleLevel(minRole) {
				respond.Error(w, http.StatusForbidden, fmt.Sprintf("requires %s role or higher", minRole))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAction returns middleware that checks if the user can perform a specific action.
func RequireAction(action Action) func(http.Handler) http.Handler {
	return RequireRole(minRoleForAction(action))
}
