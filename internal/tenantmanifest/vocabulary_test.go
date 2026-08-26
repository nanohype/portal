package tenantmanifest

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"gopkg.in/yaml.v3"
)

// enumAt walks a parsed CRD document to a dotted path and returns the enum
// declared there. The path is walked rather than the file grepped: an enum is a
// list under a specific property, and a grep for the values would match them
// anywhere in the document — including inside a description that mentions them.
func enumAt(t *testing.T, doc any, path ...string) []string {
	t.Helper()
	node := doc
	for _, key := range path {
		if idx, err := strconv.Atoi(key); err == nil {
			list, ok := node.([]any)
			if !ok {
				t.Fatalf("path %v: %q indexes something that is not a list", path, key)
			}
			if idx >= len(list) {
				t.Fatalf("path %v: index %d is past the end (%d entries)", path, idx, len(list))
			}
			node = list[idx]
			continue
		}
		m, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %q is not under a mapping", path, key)
		}
		var ok2 bool
		node, ok2 = m[key]
		if !ok2 {
			t.Fatalf("path %v: no %q — the schema moved, so this check is looking at nothing", path, key)
		}
	}
	m, ok := node.(map[string]any)
	if !ok {
		t.Fatalf("path %v does not end at a mapping", path)
	}
	raw, ok := m["enum"].([]any)
	if !ok {
		t.Fatalf("path %v declares no enum — the constraint this mirrors is gone", path)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("path %v has a non-string enum entry %#v", path, v)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		t.Fatalf("path %v declares an empty enum, so this check would pass vacuously", path)
	}
	return out
}

func platformCRD(t *testing.T) any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("schemas", "platform.nanohype.dev_platforms.yaml"))
	if err != nil {
		t.Fatalf("read vendored Platform CRD: %v", err)
	}
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse vendored Platform CRD: %v", err)
	}
	return doc
}

// TestVocabularyModelFamiliesMatchTheCRD is what makes the copy in
// vocabulary.go safe to keep. Order is compared too: the list is what a form
// renders, and a form whose options reorder under the reader for no reason is a
// worse form.
func TestVocabularyModelFamiliesMatchTheCRD(t *testing.T) {
	want := enumAt(t, platformCRD(t),
		"spec", "versions", "0", "schema", "openAPIV3Schema",
		"properties", "spec", "properties", "identity",
		"properties", "allowedModelFamilies", "items")

	if len(ModelFamilies) != len(want) {
		t.Fatalf("ModelFamilies has %d entries, the CRD declares %d\n  ours: %v\n  CRD:  %v",
			len(ModelFamilies), len(want), ModelFamilies, want)
	}
	for i := range want {
		if ModelFamilies[i] != want[i] {
			t.Fatalf("ModelFamilies[%d] = %q, CRD has %q\n  ours: %v\n  CRD:  %v",
				i, ModelFamilies[i], want[i], ModelFamilies, want)
		}
	}
}

// TestVocabularyModelFamiliesMatchTheAPIContract closes the third side. The CRD
// is the source, vocabulary.go mirrors it for the server, and api/openapi.yaml
// mirrors it for the browser — types.ts is generated from the contract and CI
// fails on drift between those two, so the contract is the last copy nothing was
// checking.
//
// Without this, correcting vocabulary.go against the CRD would leave the form
// offering the old list and every gate still green.
func TestVocabularyModelFamiliesMatchTheAPIContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse api/openapi.yaml: %v", err)
	}

	want := enumAt(t, doc, "components", "schemas", "ModelFamily")

	if len(ModelFamilies) != len(want) {
		t.Fatalf("ModelFamilies has %d entries, the contract declares %d\n  ours:     %v\n  contract: %v",
			len(ModelFamilies), len(want), ModelFamilies, want)
	}
	for i := range want {
		if ModelFamilies[i] != want[i] {
			t.Fatalf("ModelFamilies[%d] = %q, the contract has %q\n  ours:     %v\n  contract: %v",
				i, ModelFamilies[i], want[i], ModelFamilies, want)
		}
	}
}
