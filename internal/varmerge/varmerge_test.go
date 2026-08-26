package varmerge

import (
	"encoding/json"
	"testing"
)

func TestIsTagsKey(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"tags", true},
		{"default_tags", true},
		{"extra_tags", true},
		// The suffix rule is what makes the provider-specific names work.
		{"eks_tags", true},
		{"common_tags", true},
		{"a_tags", true},
		// Near misses. A key that merely contains "tags" replaces like anything
		// else — merging `tags_enabled` as a JSON map would be nonsense.
		{"tags_enabled", false},
		{"tagsuffix", false},
		{"instance_type", false},
		{"", false},
	} {
		if got := IsTagsKey(tc.key); got != tc.want {
			t.Errorf("IsTagsKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestDeepMergeJSON_HigherPrecedenceWins(t *testing.T) {
	got := DeepMergeJSON(`{"env":"dev","owner":"platform"}`, `{"env":"prod"}`)

	var m map[string]string
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("result is not JSON: %q", got)
	}
	// b overrides a on collision, and a's other keys survive — that is the
	// whole reason tags merge instead of replacing.
	if m["env"] != "prod" {
		t.Errorf("env = %q, want the higher-precedence value", m["env"])
	}
	if m["owner"] != "platform" {
		t.Errorf("owner = %q, want the org tag preserved", m["owner"])
	}
}

func TestDeepMergeJSON_EmptySides(t *testing.T) {
	if got := DeepMergeJSON(`{}`, `{"a":"1"}`); got != `{"a":"1"}` {
		t.Errorf("merging into an empty map = %q", got)
	}
	if got := DeepMergeJSON(`{"a":"1"}`, `{}`); got != `{"a":"1"}` {
		t.Errorf("merging an empty map = %q", got)
	}
}

func TestDeepMergeJSON_RefusesNonObjects(t *testing.T) {
	// Returning "" means "no merge", and the caller keeps the higher-precedence
	// value. Inventing a merged value from a malformed one would put a tag map
	// on real infrastructure that nobody wrote.
	for _, tc := range []struct{ name, a, b string }{
		{"left not JSON", `not json`, `{"a":"1"}`},
		{"right not JSON", `{"a":"1"}`, `not json`},
		{"left is an array", `["a"]`, `{"a":"1"}`},
		{"right is a string", `{"a":"1"}`, `"scalar"`},
		{"both empty", ``, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeepMergeJSON(tc.a, tc.b); got != "" {
				t.Errorf("DeepMergeJSON(%q, %q) = %q, want \"\"", tc.a, tc.b, got)
			}
		})
	}
}

func TestDeepMergeJSON_NestedValuesReplaceWholesale(t *testing.T) {
	// One level deep, deliberately: b's value for a key replaces a's entirely
	// rather than recursing. Both callers document tag maps as flat, and a
	// recursive merge would make the effective value depend on a nesting rule
	// nothing else in the product has.
	got := DeepMergeJSON(`{"nested":{"keep":"1","drop":"2"}}`, `{"nested":{"keep":"3"}}`)

	var m map[string]map[string]string
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("result is not JSON: %q", got)
	}
	if len(m["nested"]) != 1 || m["nested"]["keep"] != "3" {
		t.Errorf("nested = %v, want b's object to replace a's", m["nested"])
	}
}

func TestLayer(t *testing.T) {
	cases := []struct {
		name               string
		key, category      string
		existing, incoming string
		exists             bool
		want               string
	}{
		{"first layer wins outright", "tags", "terraform", "", `{"a":"1"}`, false, `{"a":"1"}`},
		{"tag maps deep-merge", "tags", "terraform", `{"a":"1"}`, `{"b":"2"}`, true, `{"a":"1","b":"2"}`},
		{"higher scope wins a shared key", "tags", "terraform", `{"a":"org"}`, `{"a":"ws"}`, true, `{"a":"ws"}`},
		{"suffixed tag keys merge", "eks_tags", "terraform", `{"a":"1"}`, `{"b":"2"}`, true, `{"a":"1","b":"2"}`},
		{"non-tag keys replace", "region", "terraform", "us-east-1", "us-west-2", true, "us-west-2"},
		{"env category never merges", "tags", "env", `{"a":"1"}`, `{"b":"2"}`, true, `{"b":"2"}`},
		{"a redacted value is not an object, so it replaces", "tags", "terraform", "***", "***", true, "***"},
		{"a non-object incoming value replaces", "tags", "terraform", `{"a":"1"}`, "not-json", true, "not-json"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Layer(c.key, c.category, c.existing, c.incoming, c.exists)
			if !sameObject(got, c.want) {
				t.Fatalf("Layer(%q,%q,%q,%q,%v) = %q, want %q",
					c.key, c.category, c.existing, c.incoming, c.exists, got, c.want)
			}
		})
	}
}

// sameObject compares JSON objects by value and everything else by string, so
// the assertion does not depend on Go's map iteration order.
func sameObject(got, want string) bool {
	var g, w map[string]any
	if json.Unmarshal([]byte(got), &g) != nil || json.Unmarshal([]byte(want), &w) != nil {
		return got == want
	}
	if len(g) != len(w) {
		return false
	}
	for k, v := range w {
		if g[k] != v {
			return false
		}
	}
	return true
}
