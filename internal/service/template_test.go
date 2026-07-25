package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nanohype/portal/internal/repository"
)

// TestAsFloat guards the budget-cap numeric coercion. `asFloat` must accept
// every plausible numeric runtime type — a values map built in Go with `int`
// literals has to trip the cap just like a JSON-derived float64 does — so no
// future "tightening" may drop a case.
func TestAsFloat(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want float64
		ok   bool
	}{
		{"float64", 1234.5, 1234.5, true},
		{"float32", float32(1234.5), 1234.5, true},
		{"int", int(1234), 1234, true},
		{"int32", int32(1234), 1234, true},
		{"int64", int64(1234), 1234, true},
		{"json.Number numeric", json.Number("1234"), 1234, true},
		{"json.Number decimal", json.Number("1234.5"), 1234.5, true},
		{"string", "1234", 0, false},
		{"nil", nil, 0, false},
		{"bool", true, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := asFloat(tc.in)
			if ok != tc.ok {
				t.Errorf("asFloat(%v): ok=%v, want %v", tc.in, ok, tc.ok)
				return
			}
			if ok && got != tc.want {
				t.Errorf("asFloat(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestApplyToValuesBudgetCapWithIntOverride ensures the cap is enforced
// when an in-process caller hands us an int (not a JSON-derived float64).
func TestApplyToValuesBudgetCapWithIntOverride(t *testing.T) {
	svc := &TemplateService{}
	template := tpl(t,
		[]string{"budget.monthlyUsd"},
		5000, nil, nil,
		map[string]interface{}{"budget": map[string]interface{}{"monthlyUsd": 2500.0}},
	)
	// Hand in an integer override: it must still trip the cap. Matching only
	// float64 would silently admit it.
	_, err := svc.ApplyToValues(template, map[string]interface{}{
		"budget": map[string]interface{}{"monthlyUsd": int(9999)},
	})
	if err == nil {
		t.Fatal("expected budget cap violation for integer override")
	}
	if !strings.Contains(err.Error(), "exceeds template cap") {
		t.Errorf("expected cap message; got %v", err)
	}
}

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

// tplWithDatastoreKinds is tpl plus the datastore-kind cap. Kept separate so the
// existing cases stay untouched — every one of them exercises the empty-cap
// default, which has to keep meaning "no restriction".
func tplWithDatastoreKinds(t *testing.T, overrides []string, kinds []string, defaults map[string]interface{}) repository.Template {
	t.Helper()
	template := tpl(t, overrides, 0, nil, nil, defaults)
	k, _ := json.Marshal(kinds)
	template.AllowedDatastoreKinds = k
	return template
}

func datastoreOverride(entries ...map[string]interface{}) map[string]interface{} {
	list := make([]interface{}, len(entries))
	for i, e := range entries {
		list[i] = e
	}
	return map[string]interface{}{"datastores": list}
}

// The override allowlist alone cannot cap datastores: `flatten` treats a list as
// a leaf, so one allowed `datastores` path admits the whole list — including a
// relational store, which is an Aurora cluster. That is what the kind cap is for.
func TestApplyToValuesDatastoreKindOutsideCapRejected(t *testing.T) {
	svc := &TemplateService{}
	template := tplWithDatastoreKinds(t,
		[]string{"datastores"},
		[]string{"objectStore", "queue"},
		map[string]interface{}{},
	)
	_, err := svc.ApplyToValues(template, datastoreOverride(
		map[string]interface{}{"name": "docs", "kind": "objectStore"},
		map[string]interface{}{"name": "ledger", "kind": "relational"},
	))
	if err == nil {
		t.Fatal("expected a relational datastore to be rejected by the kind cap")
	}
	if !strings.Contains(err.Error(), "relational") {
		t.Errorf("expected the rejected kind in the error; got %v", err)
	}
}

func TestApplyToValuesDatastoreKindInsideCapAllowed(t *testing.T) {
	svc := &TemplateService{}
	template := tplWithDatastoreKinds(t,
		[]string{"datastores"},
		[]string{"objectStore", "queue"},
		map[string]interface{}{},
	)
	merged, err := svc.ApplyToValues(template, datastoreOverride(
		map[string]interface{}{"name": "docs", "kind": "objectStore"},
		map[string]interface{}{"name": "work", "kind": "queue"},
	))
	if err != nil {
		t.Fatalf("kinds inside the cap should be allowed; got %v", err)
	}
	list, ok := merged["datastores"].([]interface{})
	if !ok || len(list) != 2 {
		t.Errorf("the declaration should survive into the merged values; got %#v", merged["datastores"])
	}
}

// An empty cap is every existing row, so it has to keep meaning "no restriction".
func TestApplyToValuesDatastoreEmptyCapMeansUnrestricted(t *testing.T) {
	svc := &TemplateService{}
	template := tplWithDatastoreKinds(t,
		[]string{"datastores"},
		nil,
		map[string]interface{}{},
	)
	if _, err := svc.ApplyToValues(template, datastoreOverride(
		map[string]interface{}{"name": "ledger", "kind": "relational"},
	)); err != nil {
		t.Errorf("an empty cap should place no restriction; got %v", err)
	}
}

// The cap applies to what the template itself declares, not only to overrides —
// otherwise an admin's own default_values could carry a kind the cap forbids and
// the template would contradict itself.
func TestApplyToValuesDatastoreCapAppliesToTemplateDefaults(t *testing.T) {
	svc := &TemplateService{}
	template := tplWithDatastoreKinds(t,
		nil,
		[]string{"objectStore"},
		map[string]interface{}{
			"datastores": []interface{}{
				map[string]interface{}{"name": "hot", "kind": "cache"},
			},
		},
	)
	_, err := svc.ApplyToValues(template, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected the cap to reject a kind the template's own defaults declare")
	}
	if !strings.Contains(err.Error(), "cache") {
		t.Errorf("expected the rejected kind in the error; got %v", err)
	}
}

// A declaration that is not a list of objects contributes no kinds — shape is
// CreateTenantInput.Validate's job, and it runs on every create path including
// the ones that carry no template at all.
func TestApplyToValuesDatastoreCapIgnoresMalformedEntries(t *testing.T) {
	svc := &TemplateService{}
	template := tplWithDatastoreKinds(t,
		[]string{"datastores"},
		[]string{"objectStore"},
		map[string]interface{}{},
	)
	if _, err := svc.ApplyToValues(template, map[string]interface{}{
		"datastores": []interface{}{"not-an-object"},
	}); err != nil {
		t.Errorf("the kind cap should ignore a malformed entry, leaving shape to Validate; got %v", err)
	}
}
