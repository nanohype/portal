package tfparse

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseVariables_RequiredVar(t *testing.T) {
	content := `
variable "region" {
  type        = string
  description = "AWS region"
}
`
	vars := ParseVariables(content)
	if len(vars) != 1 {
		t.Fatalf("expected 1 variable, got %d", len(vars))
	}
	v := vars[0]
	if v.Name != "region" {
		t.Errorf("expected name 'region', got %q", v.Name)
	}
	if v.Type != "string" {
		t.Errorf("expected type 'string', got %q", v.Type)
	}
	if v.Description != "AWS region" {
		t.Errorf("expected description 'AWS region', got %q", v.Description)
	}
	if !v.Required {
		t.Error("expected Required=true")
	}
	if v.Default != nil {
		t.Errorf("expected Default=nil, got %q", *v.Default)
	}
}

func TestParseVariables_AllFields(t *testing.T) {
	content := `
variable "instance_type" {
  type        = string
  description = "EC2 instance type"
  default     = "t3.micro"
}
`
	vars := ParseVariables(content)
	if len(vars) != 1 {
		t.Fatalf("expected 1 variable, got %d", len(vars))
	}
	v := vars[0]
	if v.Name != "instance_type" {
		t.Errorf("expected name 'instance_type', got %q", v.Name)
	}
	if v.Required {
		t.Error("expected Required=false")
	}
	if v.Default == nil || *v.Default != "t3.micro" {
		t.Errorf("expected Default='t3.micro', got %v", v.Default)
	}
}

func TestParseVariables_NestedBraces(t *testing.T) {
	content := `
variable "tags" {
  type = map(object({
    name  = string
    value = string
  }))
  description = "Resource tags"
  default     = {}
}
`
	vars := ParseVariables(content)
	if len(vars) != 1 {
		t.Fatalf("expected 1 variable, got %d", len(vars))
	}
	v := vars[0]
	if v.Name != "tags" {
		t.Errorf("expected name 'tags', got %q", v.Name)
	}
	if v.Required {
		t.Error("expected Required=false for var with default")
	}
}

func TestParseVariables_Multiple(t *testing.T) {
	content := `
variable "name" {
  type = string
}

variable "count" {
  type    = number
  default = 1
}

variable "enabled" {
  type    = bool
  default = true
}
`
	vars := ParseVariables(content)
	if len(vars) != 3 {
		t.Fatalf("expected 3 variables, got %d", len(vars))
	}
	if vars[0].Name != "name" || !vars[0].Required {
		t.Errorf("var[0]: expected name='name', required=true; got name=%q, required=%v", vars[0].Name, vars[0].Required)
	}
	if vars[1].Name != "count" || vars[1].Required {
		t.Errorf("var[1]: expected name='count', required=false; got name=%q, required=%v", vars[1].Name, vars[1].Required)
	}
	if vars[2].Name != "enabled" || vars[2].Required {
		t.Errorf("var[2]: expected name='enabled', required=false; got name=%q, required=%v", vars[2].Name, vars[2].Required)
	}
}

func TestParseVariables_NoVars(t *testing.T) {
	content := `
resource "aws_instance" "example" {
  ami           = "ami-12345"
  instance_type = "t3.micro"
}
`
	vars := ParseVariables(content)
	if len(vars) != 0 {
		t.Fatalf("expected 0 variables, got %d", len(vars))
	}
}

func TestParseVariables_EmptyFile(t *testing.T) {
	vars := ParseVariables("")
	if len(vars) != 0 {
		t.Fatalf("expected 0 variables, got %d", len(vars))
	}
}

func TestParseVariables_ListDefault(t *testing.T) {
	content := `
variable "subnets" {
  type    = list(string)
  default = ["a", "b"]
}
`
	vars := ParseVariables(content)
	if len(vars) != 1 {
		t.Fatalf("expected 1 variable, got %d", len(vars))
	}
	if vars[0].Required {
		t.Error("expected Required=false for var with list default")
	}
}

func TestParseDirectory(t *testing.T) {
	dir := t.TempDir()

	// Write a .tf file
	tf1 := `
variable "region" {
  type = string
}
variable "env" {
  type    = string
  default = "dev"
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(tf1), 0644); err != nil {
		t.Fatal(err)
	}

	// Write a second .tf file with duplicate
	tf2 := `
variable "region" {
  type = string
}
variable "bucket" {
  type = string
}
`
	if err := os.WriteFile(filepath.Join(dir, "vars.tf"), []byte(tf2), 0644); err != nil {
		t.Fatal(err)
	}

	// Write a non-tf file (should be ignored)
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# readme"), 0644); err != nil {
		t.Fatal(err)
	}

	vars, err := ParseDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Should have 3 unique vars: region, env, bucket (region deduped)
	if len(vars) != 3 {
		t.Fatalf("expected 3 variables, got %d", len(vars))
	}

	names := map[string]bool{}
	for _, v := range vars {
		names[v.Name] = true
	}
	for _, expected := range []string{"region", "env", "bucket"} {
		if !names[expected] {
			t.Errorf("expected variable %q not found", expected)
		}
	}
}

func TestParseTerragruntInputs_Literal(t *testing.T) {
	content := `
include "root" {
  path = find_in_parent_folders("root.hcl")
}

inputs = {
  nat_gateways         = 3
  enable_flow_logs     = true
  enable_vpc_endpoints = true
}
`
	vars := parseTerragruntInputs(content)
	if len(vars) != 3 {
		t.Fatalf("expected 3 inputs, got %d: %+v", len(vars), vars)
	}
	want := map[string]string{
		"nat_gateways":         "3",
		"enable_flow_logs":     "true",
		"enable_vpc_endpoints": "true",
	}
	for _, v := range vars {
		w, ok := want[v.Name]
		if !ok {
			t.Errorf("unexpected variable %q", v.Name)
			continue
		}
		if v.Default == nil || *v.Default != w {
			got := "nil"
			if v.Default != nil {
				got = *v.Default
			}
			t.Errorf("var %q: want default %q, got %q", v.Name, w, got)
		}
		if v.Required {
			t.Errorf("var %q: expected Required=false", v.Name)
		}
	}
}

func TestParseTerragruntInputs_NestedAndStrings(t *testing.T) {
	content := `
inputs = {
  region = "us-west-2"
  tags = {
    Environment = "production"
    Team        = "platform"
  }
  subnets = ["a", "b", "c"]
}
`
	vars := parseTerragruntInputs(content)
	if len(vars) != 3 {
		t.Fatalf("expected 3 inputs, got %d: %+v", len(vars), vars)
	}
	byName := map[string]string{}
	for _, v := range vars {
		if v.Default != nil {
			byName[v.Name] = *v.Default
		}
	}
	if byName["region"] != `"us-west-2"` {
		t.Errorf("region: got %q", byName["region"])
	}
	if !contains(byName["tags"], `Environment = "production"`) {
		t.Errorf("tags: got %q", byName["tags"])
	}
	if byName["subnets"] != `["a", "b", "c"]` {
		t.Errorf("subnets: got %q", byName["subnets"])
	}
}

func TestParseTerragruntInputs_NoInputs(t *testing.T) {
	content := `include "root" { path = find_in_parent_folders("root.hcl") }`
	vars := parseTerragruntInputs(content)
	if len(vars) != 0 {
		t.Errorf("expected 0 inputs, got %d", len(vars))
	}
}

func TestParseTerragruntInputs_FromDisk(t *testing.T) {
	dir := t.TempDir()
	hcl := `
include "root" {
  path = find_in_parent_folders("root.hcl")
}

inputs = {
  cluster_name = "prod"
  node_count   = 6
}
`
	if err := os.WriteFile(filepath.Join(dir, "terragrunt.hcl"), []byte(hcl), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	vars, err := ParseTerragruntInputs(dir)
	if err != nil {
		t.Fatalf("ParseTerragruntInputs: %v", err)
	}
	if len(vars) != 2 {
		t.Fatalf("expected 2 inputs, got %d", len(vars))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > 0 && indexOf(s, substr) >= 0))
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
