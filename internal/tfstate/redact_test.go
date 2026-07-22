package tfstate

import (
	"encoding/json"
	"strings"
	"testing"
)

// secretState is a state file shaped like the ones this actually protects:
// resources whose provider writes a secret into the attributes map in
// cleartext, next to an ordinary resource whose attributes are not secret at
// all. Terraform records no schema sensitivity here — "sensitive_attributes"
// is empty, exactly as tofu writes it — which is why nothing in the file marks
// the two apart and why the values go by the caller's bar instead.
const secretState = `{
	"version": 4,
	"terraform_version": "1.11.0",
	"serial": 7,
	"lineage": "abc123",
	"resources": [
		{
			"mode": "managed",
			"type": "random_password",
			"name": "db",
			"provider": "provider[\"registry.terraform.io/hashicorp/random\"]",
			"instances": [
				{
					"schema_version": 3,
					"attributes": {
						"length": 32,
						"result": "PLAINTEXT-DB-PASSWORD",
						"bcrypt_hash": "$2a$10$PLAINTEXT-HASH"
					},
					"sensitive_attributes": []
				}
			]
		},
		{
			"mode": "managed",
			"type": "tls_private_key",
			"name": "signing",
			"provider": "provider[\"registry.terraform.io/hashicorp/tls\"]",
			"instances": [
				{
					"schema_version": 0,
					"attributes": {
						"algorithm": "RSA",
						"private_key_pem": "-----BEGIN PRIVATE KEY-----PLAINTEXT-SIGNING-KEY",
						"nested": {"secret_string": "PLAINTEXT-NESTED-SECRET"}
					},
					"sensitive_attributes": []
				}
			]
		},
		{
			"mode": "managed",
			"type": "aws_instance",
			"name": "web",
			"provider": "provider[\"registry.terraform.io/hashicorp/aws\"]",
			"instances": [
				{
					"schema_version": 1,
					"attributes": {"id": "i-abcdef", "instance_type": "t3.micro"}
				}
			]
		}
	]
}`

// Every literal a caller below the state bar must never see, including the one
// buried inside a nested attribute object.
var stateSecrets = []string{
	"PLAINTEXT-DB-PASSWORD",
	"$2a$10$PLAINTEXT-HASH",
	"PLAINTEXT-SIGNING-KEY",
	"PLAINTEXT-NESTED-SECRET",
}

func assertNoSecrets(t *testing.T, what string, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", what, err)
	}
	for _, secret := range stateSecrets {
		if strings.Contains(string(body), secret) {
			t.Errorf("%s leaked %q in the serialized response: %s", what, secret, body)
		}
	}
}

// THE EXPLOIT: anyone holding the workspace read bar — an org viewer, or a
// viewer grant on this one workspace — asking for the resource browser and
// getting every generated password and private key in the state back, without
// ever clearing the bar the raw tfstate download sits at.
func TestParseResourcesRedactsAttributeValues(t *testing.T) {
	resources, err := ParseResources([]byte(secretState), AttributesRedacted)
	if err != nil {
		t.Fatalf("ParseResources() error = %v", err)
	}
	if len(resources) != 3 {
		t.Fatalf("got %d resources, want 3", len(resources))
	}

	assertNoSecrets(t, "redacted resources", resources)

	for _, r := range resources {
		if !r.AttributesRedacted {
			t.Errorf("%s.%s: attributes_redacted = false, want true — the UI has no other way to say the values were withheld", r.Type, r.Name)
		}
		for k, v := range r.Attributes {
			if v != nil {
				t.Errorf("%s.%s: attribute %q = %v, want nil", r.Type, r.Name, k, v)
			}
		}
	}

	// The inventory the State tab is for survives: addresses, providers, and
	// the attribute NAMES, which are schema rather than data.
	byAddr := map[string]Resource{}
	for _, r := range resources {
		byAddr[r.Type+"."+r.Name] = r
	}
	pw, ok := byAddr["random_password.db"]
	if !ok {
		t.Fatal("random_password.db missing from the redacted inventory")
	}
	if pw.Provider != "random" {
		t.Errorf("provider = %q, want %q", pw.Provider, "random")
	}
	for _, want := range []string{"length", "result", "bcrypt_hash"} {
		if _, present := pw.Attributes[want]; !present {
			t.Errorf("attribute name %q dropped; the browser still lists which attributes exist", want)
		}
	}
}

// The legitimate case: whoever may download the whole tfstate — ActionManageState
// — sees the same bytes through the parsed view. Redaction is a bar, not a
// feature that removes the data from the product.
func TestParseResourcesFullAttributesForStateManagers(t *testing.T) {
	resources, err := ParseResources([]byte(secretState), AttributesFull)
	if err != nil {
		t.Fatalf("ParseResources() error = %v", err)
	}
	body, err := json.Marshal(resources)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range stateSecrets {
		if !strings.Contains(string(body), secret) {
			t.Errorf("AttributesFull dropped %q — a state manager reads state whole", secret)
		}
	}
	for _, r := range resources {
		if r.AttributesRedacted {
			t.Errorf("%s.%s: attributes_redacted set under AttributesFull", r.Type, r.Name)
		}
	}
}

// The zero value of AttributeView redacts, so a call site that never decides —
// a new endpoint, a refactor that drops the argument through a wrapper —
// discloses nothing instead of everything.
func TestAttributeViewZeroValueRedacts(t *testing.T) {
	var unset AttributeView
	if unset != AttributesRedacted {
		t.Fatal("the zero AttributeView must be AttributesRedacted; fail-closed is the whole point of the ordering")
	}
	resources, err := ParseResources([]byte(secretState), unset)
	if err != nil {
		t.Fatalf("ParseResources() error = %v", err)
	}
	assertNoSecrets(t, "zero-value view", resources)
}

// The state diff runs through the same attribute maps, so it is the same
// disclosure by another route — and it was reachable at the same read bar.
func TestDiffStatesRedactsAttributeValues(t *testing.T) {
	before := `{"version":4,"resources":[
		{"mode":"managed","type":"aws_instance","name":"web",
		 "provider":"provider[\"registry.terraform.io/hashicorp/aws\"]",
		 "instances":[{"attributes":{"id":"i-1","instance_type":"t3.micro"}}]}
	]}`
	after := `{"version":4,"resources":[
		{"mode":"managed","type":"aws_instance","name":"web",
		 "provider":"provider[\"registry.terraform.io/hashicorp/aws\"]",
		 "instances":[{"attributes":{"id":"i-1","instance_type":"t3.large"}}]},
		{"mode":"managed","type":"random_password","name":"db",
		 "provider":"provider[\"registry.terraform.io/hashicorp/random\"]",
		 "instances":[{"attributes":{"length":32,"result":"PLAINTEXT-DB-PASSWORD"}}]}
	]}`

	redacted, err := DiffStates([]byte(before), []byte(after), AttributesRedacted)
	if err != nil {
		t.Fatalf("DiffStates() error = %v", err)
	}
	assertNoSecrets(t, "redacted diff", redacted)

	full, err := DiffStates([]byte(before), []byte(after), AttributesFull)
	if err != nil {
		t.Fatalf("DiffStates() error = %v", err)
	}

	// Redaction takes the values and nothing else: the same counts, the same
	// resources, the same changed attribute names.
	if redacted.Added != full.Added || redacted.Changed != full.Changed ||
		redacted.Removed != full.Removed || redacted.Unchanged != full.Unchanged {
		t.Errorf("redacted summary %+v differs from full %+v", redacted, full)
	}
	if len(redacted.Diffs) != len(full.Diffs) {
		t.Fatalf("redacted diff has %d entries, full has %d", len(redacted.Diffs), len(full.Diffs))
	}
	for i, d := range redacted.Diffs {
		if !d.AttributesRedacted {
			t.Errorf("%s.%s: attributes_redacted = false, want true", d.Type, d.Name)
		}
		if strings.Join(d.ChangedKeys, ",") != strings.Join(full.Diffs[i].ChangedKeys, ",") {
			t.Errorf("%s.%s: changed_keys = %v, want %v — which attribute changed is inventory",
				d.Type, d.Name, d.ChangedKeys, full.Diffs[i].ChangedKeys)
		}
		for k, v := range d.Before {
			if v != nil {
				t.Errorf("%s.%s: before[%q] = %v, want nil", d.Type, d.Name, k, v)
			}
		}
		for k, v := range d.After {
			if v != nil {
				t.Errorf("%s.%s: after[%q] = %v, want nil", d.Type, d.Name, k, v)
			}
		}
	}

	// And the full view is genuinely different, so the assertions above are
	// testing redaction and not an empty fixture.
	fullBody, _ := json.Marshal(full)
	if !strings.Contains(string(fullBody), "PLAINTEXT-DB-PASSWORD") {
		t.Fatal("the full diff should carry the value; the fixture is not exercising redaction")
	}
}
