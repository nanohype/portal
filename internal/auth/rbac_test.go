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
