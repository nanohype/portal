package tfstate

import (
	"testing"
)

func TestParseResources(t *testing.T) {
	stateJSON := `{
		"version": 4,
		"terraform_version": "1.6.0",
		"serial": 3,
		"lineage": "abc123",
		"resources": [
			{
				"mode": "managed",
				"type": "aws_instance",
				"name": "web",
				"provider": "provider[\"registry.terraform.io/hashicorp/aws\"]",
				"instances": [
					{
						"schema_version": 1,
						"attributes": {
							"ami": "ami-12345678",
							"instance_type": "t3.micro",
							"id": "i-abcdef"
						}
					}
				]
			},
			{
				"module": "module.vpc",
				"mode": "managed",
				"type": "aws_vpc",
				"name": "main",
				"provider": "provider[\"registry.terraform.io/hashicorp/aws\"]",
				"instances": [
					{
						"schema_version": 1,
						"attributes": {
							"cidr_block": "10.0.0.0/16",
							"id": "vpc-123"
						}
					}
				]
			},
			{
				"mode": "data",
				"type": "aws_ami",
				"name": "ubuntu",
				"provider": "provider[\"registry.terraform.io/hashicorp/aws\"]",
				"instances": []
			}
		]
	}`

	resources, err := ParseResources([]byte(stateJSON), AttributesFull)
	if err != nil {
		t.Fatalf("ParseResources() error = %v", err)
	}

	if len(resources) != 3 {
		t.Fatalf("expected 3 resources, got %d", len(resources))
	}

	// Check first resource
	r0 := resources[0]
	if r0.Type != "aws_instance" {
		t.Errorf("r0.Type = %q, want %q", r0.Type, "aws_instance")
	}
	if r0.Name != "web" {
		t.Errorf("r0.Name = %q, want %q", r0.Name, "web")
	}
	if r0.Provider != "aws" {
		t.Errorf("r0.Provider = %q, want %q", r0.Provider, "aws")
	}
	if r0.Mode != "managed" {
		t.Errorf("r0.Mode = %q, want %q", r0.Mode, "managed")
	}
	if r0.Module != "" {
		t.Errorf("r0.Module = %q, want empty", r0.Module)
	}
	if r0.Attributes["ami"] != "ami-12345678" {
		t.Errorf("r0.Attributes[ami] = %v, want %q", r0.Attributes["ami"], "ami-12345678")
	}

	// Check module resource
	r1 := resources[1]
	if r1.Module != "module.vpc" {
		t.Errorf("r1.Module = %q, want %q", r1.Module, "module.vpc")
	}
	if r1.Type != "aws_vpc" {
		t.Errorf("r1.Type = %q, want %q", r1.Type, "aws_vpc")
	}

	// Check data source with no instances (still included as shell)
	r2 := resources[2]
	if r2.Mode != "data" {
		t.Errorf("r2.Mode = %q, want %q", r2.Mode, "data")
	}
	if r2.Type != "aws_ami" {
		t.Errorf("r2.Type = %q, want %q", r2.Type, "aws_ami")
	}
}

func TestParseResources_Empty(t *testing.T) {
	stateJSON := `{"version": 4, "resources": []}`
	resources, err := ParseResources([]byte(stateJSON), AttributesFull)
	if err != nil {
		t.Fatalf("ParseResources() error = %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("expected 0 resources, got %d", len(resources))
	}
}

func TestParseResources_InvalidJSON(t *testing.T) {
	_, err := ParseResources([]byte("not json"), AttributesFull)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestCleanProviderName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`provider["registry.terraform.io/hashicorp/aws"]`, "aws"},
		{`provider["registry.terraform.io/hashicorp/google"]`, "google"},
		{"registry.terraform.io/hashicorp/azurerm", "azurerm"},
		{"aws", "aws"},
	}

	for _, tt := range tests {
		got := cleanProviderName(tt.input)
		if got != tt.want {
			t.Errorf("cleanProviderName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
