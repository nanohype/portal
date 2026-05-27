package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stxkxs/tofui/internal/repository"
)

// helper: build a template with reasonable test defaults
func tpl(t *testing.T, overrides []string, maxBudget int32, families []string, compliance []string, defaults map[string]interface{}) repository.Template {
	t.Helper()
	d, _ := json.Marshal(defaults)
	o, _ := json.Marshal(overrides)
	f, _ := json.Marshal(families)
	c, _ := json.Marshal(compliance)
	return repository.Template{
		Name:                 "marketing-team",
		Persona:              "marketing",
		DefaultValues:        d,
		AllowedOverrides:     o,
		MaxBudgetUSD:         maxBudget,
		AllowedModelFamilies: f,
		RequiredCompliance:   c,
	}
}

// Happy path: an override on an allowed path produces the merged values.
func TestApplyToValuesAllowedOverride(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{"budget.monthlyUsd"},
		5000, nil, nil,
		map[string]interface{}{
			"platform": map[string]interface{}{"persona": "marketing"},
			"budget":   map[string]interface{}{"monthlyUsd": 2500.0},
		},
	)
	got, err := svc.ApplyToValues(template, map[string]interface{}{
		"budget": map[string]interface{}{"monthlyUsd": 3000.0},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if budget, _ := got["budget"].(map[string]interface{}); budget["monthlyUsd"] != 3000.0 {
		t.Errorf("expected monthlyUsd=3000, got %v", budget)
	}
}

// Disallowed override path is rejected with a message naming the path.
func TestApplyToValuesDisallowedOverride(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{"budget.monthlyUsd"}, // does NOT include persona
		0, nil, nil,
		map[string]interface{}{"platform": map[string]interface{}{"persona": "marketing"}},
	)
	_, err := svc.ApplyToValues(template, map[string]interface{}{
		"platform": map[string]interface{}{"persona": "finance"},
	})
	if err == nil {
		t.Fatal("expected error for disallowed override")
	}
	if !strings.Contains(err.Error(), "platform.persona") {
		t.Errorf("expected error to name the disallowed path; got %v", err)
	}
}

// Budget cap enforces an upper bound on the merged result.
func TestApplyToValuesBudgetCap(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{"budget.monthlyUsd"},
		5000, nil, nil,
		map[string]interface{}{"budget": map[string]interface{}{"monthlyUsd": 2500.0}},
	)
	_, err := svc.ApplyToValues(template, map[string]interface{}{
		"budget": map[string]interface{}{"monthlyUsd": 9999.0},
	})
	if err == nil {
		t.Fatal("expected budget cap violation")
	}
	if !strings.Contains(err.Error(), "exceeds template cap") {
		t.Errorf("expected cap message; got %v", err)
	}
}

// Cap of 0 means "no cap" — large budgets pass.
func TestApplyToValuesZeroCapMeansUnlimited(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{"budget.monthlyUsd"},
		0, nil, nil,
		map[string]interface{}{"budget": map[string]interface{}{"monthlyUsd": 100.0}},
	)
	_, err := svc.ApplyToValues(template, map[string]interface{}{
		"budget": map[string]interface{}{"monthlyUsd": 100000.0},
	})
	if err != nil {
		t.Errorf("unexpected error with 0 cap: %v", err)
	}
}

// Model family intersection: operator can narrow but not broaden.
func TestApplyToValuesModelFamilyNarrowsOK(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{"identity.allowedModelFamilies"},
		0, []string{"anthropic", "amazon-nova"}, nil,
		map[string]interface{}{},
	)
	_, err := svc.ApplyToValues(template, map[string]interface{}{
		"identity": map[string]interface{}{"allowedModelFamilies": []interface{}{"anthropic"}},
	})
	if err != nil {
		t.Errorf("narrowing should be allowed; got %v", err)
	}
}

func TestApplyToValuesModelFamilyBroadenRejected(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{"identity.allowedModelFamilies"},
		0, []string{"anthropic"}, nil,
		map[string]interface{}{},
	)
	_, err := svc.ApplyToValues(template, map[string]interface{}{
		"identity": map[string]interface{}{"allowedModelFamilies": []interface{}{"openai"}},
	})
	if err == nil {
		t.Fatal("expected error for model family outside allowlist")
	}
	if !strings.Contains(err.Error(), "openai") {
		t.Errorf("expected the rejected family in the error; got %v", err)
	}
}

// Operator omits familes → template's default set lands automatically.
func TestApplyToValuesModelFamilyDefaultsApply(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{},
		0, []string{"anthropic", "amazon-nova"}, nil,
		map[string]interface{}{},
	)
	got, err := svc.ApplyToValues(template, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	id, _ := got["identity"].(map[string]interface{})
	fams, _ := id["allowedModelFamilies"].([]interface{})
	if len(fams) != 2 {
		t.Errorf("expected template's 2 families to apply by default; got %v", fams)
	}
}

// Required compliance: missing flag → error names the flag.
func TestApplyToValuesRequiredComplianceMissing(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{},
		0, nil, []string{"soc2"},
		map[string]interface{}{
			"platform": map[string]interface{}{
				"compliance": map[string]interface{}{"soc2": false},
			},
		},
	)
	_, err := svc.ApplyToValues(template, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for unsatisfied compliance requirement")
	}
	if !strings.Contains(err.Error(), "soc2") {
		t.Errorf("expected soc2 in error; got %v", err)
	}
}

// Required compliance present in defaults → passes.
func TestApplyToValuesRequiredComplianceSatisfied(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{},
		0, nil, []string{"soc2"},
		map[string]interface{}{
			"platform": map[string]interface{}{
				"compliance": map[string]interface{}{"soc2": true},
			},
		},
	)
	_, err := svc.ApplyToValues(template, map[string]interface{}{})
	if err != nil {
		t.Errorf("compliance satisfied; got %v", err)
	}
}
