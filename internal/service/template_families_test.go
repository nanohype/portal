package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/tenantmanifest"
)

// A template's allowed_model_families is written into the rendered Platform CR
// verbatim when an operator overrides none of it, so a family the CRD's enum
// does not admit becomes a manifest the apiserver refuses. Catching it at the
// template form makes that a 400 naming the field, rather than a tenant create
// that fails later against a schema whoever filled in the form never saw.
func TestValidateModelFamilies_RejectsWhatTheCRDDoesNot(t *testing.T) {
	for _, bad := range []string{"openai", "google", "Anthropic", "anthropic ", ""} {
		err := ValidateModelFamilies([]string{bad})
		if err == nil {
			t.Errorf("model family %q was accepted; the CRD's enum does not admit it", bad)
			continue
		}
		if !strings.Contains(err.Error(), "allowed_model_families") {
			t.Errorf("error for %q does not name the field: %v", bad, err)
		}
	}
}

// Every family the CRD admits has to be accepted, and the list is read from the
// vocabulary rather than retyped — a test that repeated the seven names would
// pass while the product offered four of them, which is the defect this closes.
func TestValidateModelFamilies_AcceptsEveryFamilyTheCRDAdmits(t *testing.T) {
	if len(tenantmanifest.ModelFamilies) == 0 {
		t.Fatal("the vocabulary is empty, so this check would pass vacuously")
	}
	for _, good := range tenantmanifest.ModelFamilies {
		if err := ValidateModelFamilies([]string{good}); err != nil {
			t.Errorf("model family %q is in the CRD's enum but was rejected: %v", good, err)
		}
	}
	if err := ValidateModelFamilies(tenantmanifest.ModelFamilies); err != nil {
		t.Errorf("the full admitted set was rejected: %v", err)
	}
}

// An empty list is "no restriction", which is what ApplyToValues already means
// by one. It must not become a validation error.
func TestValidateModelFamilies_EmptyIsNoRestriction(t *testing.T) {
	if err := ValidateModelFamilies(nil); err != nil {
		t.Errorf("nil rejected: %v", err)
	}
	if err := ValidateModelFamilies([]string{}); err != nil {
		t.Errorf("empty slice rejected: %v", err)
	}
}

// The consequence the validator exists to prevent, pinned end to end: an
// invented family reached identity.allowedModelFamilies in the rendered values.
func TestApplyToValues_CannotRenderAFamilyTheCRDRejects(t *testing.T) {
	svc := &TemplateService{}
	fam, _ := json.Marshal([]string{"openai"})
	d, _ := json.Marshal(map[string]interface{}{})
	o, _ := json.Marshal([]string{})
	tmpl := repository.Template{
		Name: "t", Persona: "eng",
		DefaultValues: d, AllowedOverrides: o, AllowedModelFamilies: fam,
	}
	// A row already holding an invented family — written before the validator
	// existed — still renders it. The validator stops one being stored; this
	// records that it does not retroactively clean a row that has one.
	out, err := svc.ApplyToValues(tmpl, map[string]interface{}{})
	if err != nil {
		t.Fatalf("ApplyToValues: %v", err)
	}
	if got := getPath(out, "identity.allowedModelFamilies"); got == nil {
		t.Fatal("expected the stored list to render")
	}
	// What the write path now refuses:
	if err := ValidateModelFamilies([]string{"openai"}); err == nil {
		t.Fatal("the write path would still store openai")
	}
}
