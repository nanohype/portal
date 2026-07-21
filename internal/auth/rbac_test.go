package auth

import "testing"

func TestCanPerform(t *testing.T) {
	actions := []Action{
		ActionViewWorkspace,
		ActionCreateRun,
		ActionApplyRun,
		ActionApplyProd,
		ActionDestroyRun,
		ActionManageState,
		ActionManageVars,
		ActionManageTeams,
		ActionManageOrg,
		ActionDeleteWorkspace,
	}

	roles := []string{"viewer", "operator", "admin", "owner"}

	// Expected minimum role per action (index into roles slice)
	// viewer=0, operator=1, admin=2, owner=3
	minRole := map[Action]int{
		ActionViewWorkspace:   0, // viewer
		ActionCreateRun:       1, // operator
		ActionApplyRun:        1, // operator
		ActionApplyProd:       2, // admin
		ActionDestroyRun:      2, // admin
		ActionManageState:     2, // admin
		ActionManageVars:      2, // admin
		ActionManageTeams:     2, // admin
		ActionManageOrg:       3, // owner
		ActionDeleteWorkspace: 2, // admin
	}

	for _, action := range actions {
		for roleIdx, role := range roles {
			t.Run(role+"/"+string(action), func(t *testing.T) {
				got := CanPerform(role, action)
				want := roleIdx >= minRole[action]
				if got != want {
					t.Errorf("CanPerform(%q, %q) = %v, want %v", role, action, got, want)
				}
			})
		}
	}
}

func TestCanPerform_UnknownRole(t *testing.T) {
	if CanPerform("unknown", ActionViewWorkspace) {
		t.Error("unknown role should not be able to perform any action")
	}
}

func TestActionForOperation(t *testing.T) {
	cases := map[string]Action{
		"plan": ActionCreateRun,
		"test": ActionCreateRun,
		// import rewrites which real resources a config claims. Every other way
		// to move state already sits at ActionManageState, so this does too.
		"import":  ActionManageState,
		"apply":   ActionApplyRun,
		"destroy": ActionDestroyRun,
	}
	for op, want := range cases {
		if got := ActionForOperation(op); got != want {
			t.Errorf("ActionForOperation(%q) = %q, want %q", op, got, want)
		}
	}
}

// TestRunOperationAuthorization pins the gate the run handler enforces: a viewer
// can run nothing, an operator can plan/apply but not destroy, admin+ can destroy.
// This is the regression guard for the broken-access-control fix (a viewer must
// never be able to POST {operation: "destroy"}).
func TestRunOperationAuthorization(t *testing.T) {
	cases := []struct {
		role, operation string
		allowed         bool
	}{
		{"viewer", "plan", false},
		{"viewer", "apply", false},
		{"viewer", "destroy", false},
		{"operator", "plan", true},
		{"operator", "apply", true},
		{"operator", "destroy", false},
		{"operator", "test", true},
		{"operator", "import", false},
		{"admin", "apply", true},
		{"admin", "destroy", true},
		{"admin", "import", true},
		{"owner", "destroy", true},
		{"owner", "import", true},
	}
	for _, c := range cases {
		if got := CanPerform(c.role, ActionForOperation(c.operation)); got != c.allowed {
			t.Errorf("%s performing %q: got %v, want %v", c.role, c.operation, got, c.allowed)
		}
	}
}
