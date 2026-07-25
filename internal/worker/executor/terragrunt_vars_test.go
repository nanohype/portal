package executor

import (
	"strings"
	"testing"
)

// The override this notice describes is invisible everywhere else. An explicitly
// set TF_VAR_ outranks the leaf's `inputs = {}` (terragrunt passes `inputs`
// through the environment and will not overwrite a variable the parent process
// already set), so a portal variable replaces what the committed leaf pinned —
// and `terragrunt plan` reports the resulting value, never its origin. The run
// log is the only place an operator can see it happened, so the line has to name
// the keys and has to appear.
func TestTerragruntVarOverrideNotice(t *testing.T) {
	t.Run("names the keys on a terragrunt run", func(t *testing.T) {
		got := terragruntVarOverrideNotice("terragrunt", []string{"cluster_name", "vpc_cidr"})
		if got == "" {
			t.Fatal("a terragrunt run with terraform-category variables must produce a notice")
		}
		for _, want := range []string{"cluster_name", "vpc_cidr"} {
			if !strings.Contains(got, want) {
				t.Errorf("notice does not name %q: %s", want, got)
			}
		}
		// The point of the line is that the override is silent elsewhere, so it
		// has to say what precedence actually does.
		if !strings.Contains(got, "outrank") || !strings.Contains(got, "overridden") {
			t.Errorf("notice should state that these keys outrank the leaf and override it: %s", got)
		}
	})

	// Sorted, so an unchanged variable set produces an unchanged log line and a
	// diff between two runs means something really changed.
	t.Run("is stable regardless of variable order", func(t *testing.T) {
		a := terragruntVarOverrideNotice("terragrunt", []string{"zebra", "alpha", "middle"})
		b := terragruntVarOverrideNotice("terragrunt", []string{"middle", "zebra", "alpha"})
		if a != b {
			t.Errorf("notice is order-dependent:\n  %s  %s", a, b)
		}
		if !strings.Contains(a, "alpha, middle, zebra") {
			t.Errorf("keys should be sorted: %s", a)
		}
	})

	// The caller passes the slice it is still using to build the environment.
	t.Run("does not reorder the caller's slice", func(t *testing.T) {
		keys := []string{"zebra", "alpha"}
		terragruntVarOverrideNotice("terragrunt", keys)
		if keys[0] != "zebra" {
			t.Errorf("the caller's slice was sorted in place: %v", keys)
		}
	})

	// In tofu mode portal.auto.tfvars is the source and the file wins, so there
	// is no override to report.
	t.Run("is silent in tofu mode", func(t *testing.T) {
		if got := terragruntVarOverrideNotice("tofu", []string{"cluster_name"}); got != "" {
			t.Errorf("tofu runs should produce no notice, got: %s", got)
		}
	})

	t.Run("is silent with no terraform-category variables", func(t *testing.T) {
		if got := terragruntVarOverrideNotice("terragrunt", nil); got != "" {
			t.Errorf("no variables should produce no notice, got: %s", got)
		}
	})

	// Values may be sensitive; only names belong in a run log.
	t.Run("carries no values", func(t *testing.T) {
		got := terragruntVarOverrideNotice("terragrunt", []string{"db_password"})
		if strings.Contains(got, "=") {
			t.Errorf("notice should name keys only, never emit a value: %s", got)
		}
	})
}
