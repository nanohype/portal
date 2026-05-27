package handler

import (
	"testing"

	"github.com/nanohype/portal/internal/tfparse"
)

func strPtr(s string) *string { return &s }

func findByName(entries []DiscoverVariableResponse, name string) (DiscoverVariableResponse, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return DiscoverVariableResponse{}, false
}

// TestMergeDiscovered exercises every provenance combination of the
// module-schema × terragrunt-resolved × portal-configured cube. Each module
// variable should be returned with its source recorded in ConfiguredBy;
// resolved-input keys with no matching module variable are appended.
func TestMergeDiscovered(t *testing.T) {
	moduleVars := []tfparse.DiscoveredVariable{
		{Name: "environment", Type: "string", Required: true},
		{Name: "region", Type: "string", Description: "AWS region", Required: true},
		{Name: "vpc_cidr", Type: "string", Default: strPtr(`"10.0.0.0/16"`), Required: false},
		{Name: "max_azs", Type: "number", Default: strPtr("3"), Required: false},
		{Name: "nat_gateways", Type: "number", Default: strPtr("1"), Required: false},
		{Name: "tags", Type: "map(string)", Default: strPtr("{}"), Required: false},
		{Name: "cluster_name", Type: "string", Default: strPtr(`"eks"`), Required: false},
	}
	resolved := map[string]any{
		"environment":  "production",
		"region":       "us-west-2",
		"nat_gateways": float64(3),
		"cluster_name": "eks",
		// Key terragrunt sets that has no matching module variable —
		// expected to appear as an "extra" entry.
		"unknown_extra": "from-terragrunt",
	}
	portalConfigured := map[string]bool{
		"vpc_cidr": true, // user added this via the UI
	}

	got := mergeDiscovered(moduleVars, resolved, portalConfigured)

	cases := []struct {
		name             string
		wantConfigured   bool
		wantConfiguredBy string
		wantDefault      *string // nil = don't check
	}{
		// terragrunt-resolved: ConfiguredBy=terragrunt; default replaced
		// with the resolved value's HCL representation.
		{"environment", true, "terragrunt", strPtr(`"production"`)},
		{"region", true, "terragrunt", strPtr(`"us-west-2"`)},
		{"nat_gateways", true, "terragrunt", strPtr("3")},
		{"cluster_name", true, "terragrunt", strPtr(`"eks"`)},
		// portal-configured: ConfiguredBy=portal; default stays as module default.
		{"vpc_cidr", true, "portal", strPtr(`"10.0.0.0/16"`)},
		// Unconfigured: no badge; default stays as module default.
		{"max_azs", false, "", strPtr("3")},
		{"tags", false, "", strPtr("{}")},
		// Extra terragrunt input with no matching module variable.
		{"unknown_extra", true, "terragrunt", strPtr(`"from-terragrunt"`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, ok := findByName(got, tc.name)
			if !ok {
				t.Fatalf("expected entry %q not found", tc.name)
			}
			if e.Configured != tc.wantConfigured {
				t.Errorf("Configured: got %v, want %v", e.Configured, tc.wantConfigured)
			}
			if e.ConfiguredBy != tc.wantConfiguredBy {
				t.Errorf("ConfiguredBy: got %q, want %q", e.ConfiguredBy, tc.wantConfiguredBy)
			}
			if tc.wantDefault != nil {
				if e.Default == nil {
					t.Errorf("Default: got nil, want %q", *tc.wantDefault)
				} else if *e.Default != *tc.wantDefault {
					t.Errorf("Default: got %q, want %q", *e.Default, *tc.wantDefault)
				}
			}
		})
	}
}

// TestFormatHCL covers the JSON-to-HCL-literal translation used when a
// terragrunt-resolved value replaces a module's default in the response.
func TestFormatHCL(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "us-west-2", `"us-west-2"`},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"integer (as float64 from JSON)", float64(3), "3"},
		{"fractional float", 1.5, "1.5"},
		{"nil", nil, ""},
		{"map", map[string]any{"Env": "prod"}, `{"Env":"prod"}`},
		{"list", []any{"a", "b"}, `["a","b"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatHCL(tc.in)
			if got != tc.want {
				t.Errorf("formatHCL(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsLocalModuleSource covers the local-vs-remote discrimination used
// to decide whether tfparse.ParseDirectory can read the module's
// variables.tf, or whether to fall back to inputs-only.
func TestIsLocalModuleSource(t *testing.T) {
	local := []string{
		"/home/dev/repo/components/aws/network",
		"/abs/path",
		"./relative",
		"../sibling",
	}
	remote := []string{
		"",
		"git::https://github.com/foo/bar.git",
		"github.com/foo/bar",
		"https://example.com/module.zip",
		"tfr:registry.example.com/foo/bar",
		"s3::https://s3.amazonaws.com/bucket/key",
	}
	for _, p := range local {
		if !isLocalModuleSource(p) {
			t.Errorf("expected local: %q", p)
		}
	}
	for _, p := range remote {
		if isLocalModuleSource(p) {
			t.Errorf("expected remote: %q", p)
		}
	}
}
