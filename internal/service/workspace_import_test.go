package service

import (
	"testing"

	"github.com/nanohype/portal/internal/tfstate"
)

// tfstate.ParseOutputs is the only way outputs reach ImportOutputs, and it
// blanks the value of everything the state marks sensitive. This pins that,
// because it is the reason a sensitive output cannot be imported at all: there
// is nothing left to import by the time the service sees it.
func TestParseOutputsRedactsSensitiveValues(t *testing.T) {
	state := []byte(`{
	  "version": 4,
	  "outputs": {
	    "vpc_id":      {"value": "vpc-123", "type": "string"},
	    "db_password": {"value": "hunter2", "type": "string", "sensitive": true}
	  }
	}`)

	outputs, err := tfstate.ParseOutputs(state)
	if err != nil {
		t.Fatalf("ParseOutputs: %v", err)
	}
	for _, out := range outputs {
		if out.Sensitive && out.Value != nil {
			t.Fatalf("%s: sensitive output kept its value %v", out.Name, out.Value)
		}
		if out.Name == "vpc_id" && out.Value != "vpc-123" {
			t.Fatalf("vpc_id: value = %v, want it kept", out.Value)
		}
	}
}

// Importing an already-redacted output would store the JSON encoding of nothing
// — the string "null" — under the source's key, and the worker would hand that
// to the next run as TF_VAR_<key>=null. Marking that row sensitive on top of it
// hides the damage behind ***, so the operator sees a variable that looks
// imported and is garbage. Skip them, count them, and let the caller say so.
func TestImportableOutputsDropsSensitive(t *testing.T) {
	outputs := []tfstate.Output{
		{Name: "vpc_id", Value: "vpc-123", Type: "string"},
		{Name: "db_password", Value: nil, Type: "string", Sensitive: true},
		{Name: "subnet_ids", Value: []any{"a", "b"}, Type: "list(string)"},
		{Name: "api_token", Value: nil, Type: "string", Sensitive: true},
	}

	importable, skipped := importableOutputs(outputs)

	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if len(importable) != 2 {
		t.Fatalf("importable = %d, want 2", len(importable))
	}
	for _, out := range importable {
		if out.Sensitive {
			t.Errorf("%s: a sensitive output reached the write path", out.Name)
		}
		if out.Value == nil {
			t.Errorf("%s: a valueless output reached the write path", out.Name)
		}
	}
	if importable[0].Name != "vpc_id" || importable[1].Name != "subnet_ids" {
		t.Errorf("the ordinary outputs must still come through: %+v", importable)
	}
}

// A state whose every output is sensitive imports nothing, and the count is
// what lets the endpoint answer "all N were sensitive" instead of the false
// "source workspace has no outputs".
func TestImportableOutputsAllSensitive(t *testing.T) {
	importable, skipped := importableOutputs([]tfstate.Output{
		{Name: "db_password", Sensitive: true},
		{Name: "api_token", Sensitive: true},
	})
	if len(importable) != 0 {
		t.Errorf("importable = %d, want 0", len(importable))
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
}

// Nothing sensitive in the state means nothing is dropped — the ordinary import
// still carries every output across.
func TestImportableOutputsKeepsEverythingOrdinary(t *testing.T) {
	importable, skipped := importableOutputs([]tfstate.Output{
		{Name: "vpc_id", Value: "vpc-123"},
		{Name: "region", Value: "us-west-2"},
	})
	if len(importable) != 2 {
		t.Errorf("importable = %d, want 2", len(importable))
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
}
