package service

import (
	"strings"
	"testing"
)

// TestApplyToValuesBudgetCap_StringForm is the shape the CRD calls canonical.
// governance.nanohype.dev_budgetpolicies declares spec.monthlyUsd as
// `type: string` — "the soft threshold expressed as a decimal-string USD amount
// (e.g. \"2500\", \"1500.50\")" — so a caller sending the value in the form the
// schema documents is the expected case, not an exotic one.
func TestApplyToValuesBudgetCap_StringForm(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{"budget.monthlyUsd"},
		5000, nil, nil,
		map[string]interface{}{"budget": map[string]interface{}{"monthlyUsd": "2500"}},
	)

	_, err := svc.ApplyToValues(template, map[string]interface{}{
		"budget": map[string]interface{}{"monthlyUsd": "99999999"},
	})
	if err == nil {
		t.Fatal("a decimal-string budget of 99999999 passed a template cap of 5000")
	}
	if !strings.Contains(err.Error(), "exceeds template cap") {
		t.Errorf("expected cap message; got %v", err)
	}
}

// TestApplyToValuesBudgetCap_StringUnderCapIsAccepted keeps the cap a cap. A
// change that rejected every string would satisfy the test above.
func TestApplyToValuesBudgetCap_StringUnderCapIsAccepted(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{"budget.monthlyUsd"},
		5000, nil, nil,
		map[string]interface{}{"budget": map[string]interface{}{"monthlyUsd": "2500"}},
	)

	got, err := svc.ApplyToValues(template, map[string]interface{}{
		"budget": map[string]interface{}{"monthlyUsd": "1500.50"},
	})
	if err != nil {
		t.Fatalf("a decimal-string budget under the cap was rejected: %v", err)
	}
	if v := getPath(got, "budget.monthlyUsd"); v != "1500.50" {
		t.Errorf("budget.monthlyUsd = %v, want the string form preserved for the CRD", v)
	}
}

// TestApplyToValuesBudgetCap_UnreadableValueIsRejected closes the other half.
// The cap sat behind `if got, ok := asFloat(...); ok`, so any value asFloat
// declined skipped the check entirely rather than failing it. A budget nothing
// can read is not a budget under the cap.
func TestApplyToValuesBudgetCap_UnreadableValueIsRejected(t *testing.T) {
	for _, bad := range []interface{}{
		"not-a-number",
		true,
		map[string]interface{}{"amount": 5},
		[]interface{}{1, 2},
	} {
		svc := &TemplateService{}
		template := tpl(t,
			[]string{"budget.monthlyUsd"},
			5000, nil, nil,
			map[string]interface{}{"budget": map[string]interface{}{"monthlyUsd": "2500"}},
		)
		_, err := svc.ApplyToValues(template, map[string]interface{}{
			"budget": map[string]interface{}{"monthlyUsd": bad},
		})
		if err == nil {
			t.Errorf("budget.monthlyUsd = %#v bypassed the cap instead of being rejected", bad)
		}
	}
}

// TestApplyToValuesBudgetCap_AbsentBudgetStillApplies pins the case that must
// keep working: no override for the path at all, so the template's own default
// stands and the cap is measured against that.
func TestApplyToValuesBudgetCap_AbsentBudgetStillApplies(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{"platform.persona"},
		5000, nil, nil,
		map[string]interface{}{"budget": map[string]interface{}{"monthlyUsd": "2500"}},
	)

	if _, err := svc.ApplyToValues(template, map[string]interface{}{
		"platform": map[string]interface{}{"persona": "eng"},
	}); err != nil {
		t.Fatalf("a create that does not touch the budget was rejected: %v", err)
	}
}
