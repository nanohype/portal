package tfstate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TFState represents the top-level structure of an OpenTofu/Terraform state file.
type TFState struct {
	Version          int                      `json:"version"`
	TerraformVersion string                   `json:"terraform_version"`
	Serial           int                      `json:"serial"`
	Lineage          string                   `json:"lineage"`
	Outputs          map[string]TFStateOutput `json:"outputs"`
	Resources        []TFStateResource        `json:"resources"`
}

// TFStateOutput represents an output value in the state file.
type TFStateOutput struct {
	Value     interface{} `json:"value"`
	Type      interface{} `json:"type"`
	Sensitive bool        `json:"sensitive"`
}

// Output is a simplified output representation returned by the API.
type Output struct {
	Name      string      `json:"name"`
	Value     interface{} `json:"value"`
	Type      string      `json:"type"`
	Sensitive bool        `json:"sensitive"`
}

// TFStateResource represents a resource block in the state file.
type TFStateResource struct {
	Module    string            `json:"module"`
	Mode      string            `json:"mode"`
	Type      string            `json:"type"`
	Name      string            `json:"name"`
	Provider  string            `json:"provider"`
	Instances []TFStateInstance `json:"instances"`
}

// TFStateInstance represents a single instance of a resource.
type TFStateInstance struct {
	SchemaVersion  int                    `json:"schema_version"`
	Attributes     map[string]interface{} `json:"attributes"`
	AttributesFlat map[string]string      `json:"attributes_flat"`
}

// Resource is a simplified representation returned by the API.
type Resource struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Module   string `json:"module"`
	Provider string `json:"provider"`
	Mode     string `json:"mode"`
	// Attributes carries every attribute name the instance has. Under
	// AttributesRedacted each value is null and AttributesRedacted is true; the
	// names themselves are schema, not data, so they stay.
	Attributes         map[string]interface{} `json:"attributes"`
	AttributesRedacted bool                   `json:"attributes_redacted,omitempty"`
}

// AttributeView decides whether parsed state carries the VALUES of resource
// attributes or only their names.
//
// Terraform and OpenTofu write provider-sensitive attributes into state in
// cleartext — random_password.result, tls_private_key.private_key_pem,
// aws_db_instance.password, aws_iam_access_key.secret,
// aws_secretsmanager_secret_version.secret_string. State carries no reliable
// machine-readable marking for them: the instance-level "sensitive_attributes"
// list records marks that reached a value through configuration, not the
// sensitivity a provider schema declares, so there is no subset of attributes
// that can be handed out safely by name. A denylist would be a guess, and the
// next provider attribute nobody thought of is the leak.
//
// So attribute values are the tfstate blob under another name, and they sit at
// the bar the raw download sits at: ActionManageState. Everything else the
// resource browser shows — addresses, providers, which attribute names exist,
// which of them changed between two serials — is inventory, and stays at the
// workspace read bar.
//
// The zero value redacts, so a caller that never decides discloses nothing.
type AttributeView int

const (
	// AttributesRedacted keeps attribute names and drops every value.
	AttributesRedacted AttributeView = iota
	// AttributesFull returns state attributes verbatim, secrets included.
	AttributesFull
)

// project returns the attribute map to serialize under this view, and whether
// it was redacted. Redaction replaces each top-level value with nil, which
// takes every nested map, list and string underneath it with them.
func (v AttributeView) project(attrs map[string]interface{}) (map[string]interface{}, bool) {
	if attrs == nil {
		attrs = map[string]interface{}{}
	}
	if v == AttributesFull {
		return attrs, false
	}
	redacted := make(map[string]interface{}, len(attrs))
	for k := range attrs {
		redacted[k] = nil
	}
	return redacted, true
}

// ParseResources extracts a flat list of resources from raw state JSON. The
// view decides whether attribute values come with it — see AttributeView.
func ParseResources(data []byte, view AttributeView) ([]Resource, error) {
	var state TFState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state JSON: %w", err)
	}

	var resources []Resource
	for _, r := range state.Resources {
		provider := cleanProviderName(r.Provider)

		for _, inst := range r.Instances {
			attrs, redacted := view.project(inst.Attributes)
			resources = append(resources, Resource{
				Type:               r.Type,
				Name:               r.Name,
				Module:             r.Module,
				Provider:           provider,
				Mode:               r.Mode,
				Attributes:         attrs,
				AttributesRedacted: redacted,
			})
		}

		// If no instances, still include the resource shell
		if len(r.Instances) == 0 {
			attrs, redacted := view.project(nil)
			resources = append(resources, Resource{
				Type:               r.Type,
				Name:               r.Name,
				Module:             r.Module,
				Provider:           provider,
				Mode:               r.Mode,
				Attributes:         attrs,
				AttributesRedacted: redacted,
			})
		}
	}

	if resources == nil {
		resources = []Resource{}
	}
	return resources, nil
}

// ParseOutputs extracts output values from raw state JSON.
//
// Outputs take no AttributeView, because unlike resource attributes they carry
// their own reliable marking: a root output records the config's `sensitive`
// declaration, and tofu refuses to plan a config that surfaces a
// provider-sensitive value through an unmarked output. So the state names the
// secrets here and this blanks exactly those, at every bar.
func ParseOutputs(data []byte) ([]Output, error) {
	var state TFState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state JSON: %w", err)
	}

	var outputs []Output
	for name, out := range state.Outputs {
		typeName := formatOutputType(out.Type)
		value := out.Value
		if out.Sensitive {
			value = nil
		}
		outputs = append(outputs, Output{
			Name:      name,
			Value:     value,
			Type:      typeName,
			Sensitive: out.Sensitive,
		})
	}

	if outputs == nil {
		outputs = []Output{}
	}
	return outputs, nil
}

// formatOutputType converts the type field from a state file into a human-readable string.
func formatOutputType(t interface{}) string {
	switch v := t.(type) {
	case string:
		return v
	case []interface{}:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
		b, _ := json.Marshal(v)
		return string(b)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// cleanProviderName strips the registry prefix from a provider string.
// e.g. "registry.terraform.io/hashicorp/aws" → "aws"
func cleanProviderName(provider string) string {
	// Format: registry.terraform.io/hashicorp/aws or provider["registry.terraform.io/hashicorp/aws"]
	p := strings.TrimPrefix(provider, "provider[\"")
	p = strings.TrimSuffix(p, "\"]")
	parts := strings.Split(p, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return provider
}
