package service

import (
	"errors"
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/nanohype/portal/internal/apperr"
)

func TestValidateIdentityBounds(t *testing.T) {
	many := func(n int, v string) []interface{} {
		out := make([]interface{}, n)
		for i := range out {
			out[i] = v
		}
		return out
	}
	ops := func(v ...interface{}) map[string]interface{} {
		return map[string]interface{}{"attribution": map[string]interface{}{"operators": v}}
	}
	models := func(v ...interface{}) map[string]interface{} {
		return map[string]interface{}{"identity": map[string]interface{}{"allowedModels": v}}
	}

	tests := []struct {
		name    string
		values  map[string]interface{}
		wantErr string
	}{
		{"absent", map[string]interface{}{}, ""},
		{"operator email", ops("alice@example.com"), ""},
		{"operator plus-tag", ops("alice+ops@corp.example.io"), ""},
		{"operator uppercase", ops("Alice@example.com"), "lowercase email"},
		{"operator system principal", ops("system:admin"), "lowercase email"},
		{"operator serviceaccount", ops("system:serviceaccount:kube-system:default"), "lowercase email"},
		{"operator not an email", ops("alice"), "lowercase email"},
		{"operator too long", ops(strings.Repeat("a", 250) + "@x.io"), "at most 254"},
		{"operators at cap", ops(many(16, "a@b.io")...), ""},
		{"operators over cap", ops(many(17, "a@b.io")...), "at most 16"},
		{"model full id", models("anthropic.claude-sonnet-4-20250514-v1:0"), ""},
		{"model geo prefixed", models("us.anthropic.claude-3-5-haiku-20241022-v1:0"), ""},
		{"model star", models("*"), "whole Bedrock model id"},
		{"model family prefix", models("anthropic.claude"), "whole Bedrock model id"},
		{"model geo letter", models("us.a"), "whole Bedrock model id"},
		{"model too long", models("anthropic." + strings.Repeat("a", 120) + "-v1"), "at most 128"},
		{"models over cap", models(many(33, "amazon.titan-text-v1")...), "at most 32"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateIdentityBounds(tt.values)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// Validate is the gate the handler calls, so the bound has to surface there as
// a validation error, not only from the helper.
func TestCreateTenantInputValidateRejectsUnboundedOperator(t *testing.T) {
	in := CreateTenantInput{
		ClusterID: "cluster-1",
		Name:      "acme",
		Values: map[string]interface{}{
			"attribution": map[string]interface{}{"operators": []interface{}{"system:admin"}},
		},
	}
	err := in.Validate()
	var ae *apperr.Error
	if err == nil || !errors.As(err, &ae) || ae.Kind != apperr.KindValidation {
		t.Fatalf("want a validation error, got %v", err)
	}
}

// The mirrored bounds are copies of the vendored Platform CRD's. Reading them
// back out of the schema here means a re-vendor that moves one fails this test
// rather than leaving portal enforcing last quarter's rule.
func TestIdentityBoundsMatchTheVendoredCRD(t *testing.T) {
	raw, err := os.ReadFile("../tenantmanifest/schemas/platform.nanohype.dev_platforms.yaml")
	if err != nil {
		t.Fatalf("read vendored Platform CRD: %v", err)
	}
	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema schemaNode `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("parse vendored Platform CRD: %v", err)
	}
	if len(crd.Spec.Versions) == 0 {
		t.Fatal("vendored Platform CRD has no versions")
	}
	spec := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]

	tests := []struct {
		path               []string
		pattern            string
		maxLength, maxItem int
	}{
		{[]string{"attribution", "operators"}, attributionOperatorPattern, attributionOperatorMaxLength, attributionOperatorsMaxItems},
		{[]string{"identity", "allowedModels"}, allowedModelPattern, allowedModelMaxLength, allowedModelsMaxItems},
	}
	for _, tt := range tests {
		name := strings.Join(tt.path, ".")
		node := spec
		for _, p := range tt.path {
			next, ok := node.Properties[p]
			if !ok {
				t.Fatalf("spec.%s is not in the vendored Platform CRD", name)
			}
			node = next
		}
		if node.Items == nil {
			t.Fatalf("spec.%s has no items schema", name)
		}
		if node.Items.Pattern != tt.pattern {
			t.Errorf("spec.%s pattern: CRD %q, portal %q", name, node.Items.Pattern, tt.pattern)
		}
		if node.Items.MaxLength == nil || *node.Items.MaxLength != tt.maxLength {
			t.Errorf("spec.%s maxLength: CRD %v, portal %d", name, node.Items.MaxLength, tt.maxLength)
		}
		if node.MaxItems == nil || *node.MaxItems != tt.maxItem {
			t.Errorf("spec.%s maxItems: CRD %v, portal %d", name, node.MaxItems, tt.maxItem)
		}
	}
}

type schemaNode struct {
	Properties map[string]schemaNode `json:"properties"`
	Items      *schemaNode           `json:"items"`
	Pattern    string                `json:"pattern"`
	MaxLength  *int                  `json:"maxLength"`
	MaxItems   *int                  `json:"maxItems"`
}
