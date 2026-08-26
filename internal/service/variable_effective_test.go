package service

import (
	"encoding/json"
	"testing"
)

// find returns the merged variable for key|category.
func find(t *testing.T, got []EffectiveVariable, key, category string) EffectiveVariable {
	t.Helper()
	for _, v := range got {
		if v.Key == key && v.Category == category {
			return v
		}
	}
	t.Fatalf("no effective variable for %s|%s", key, category)
	return EffectiveVariable{}
}

// sameJSON compares two JSON objects by value, so the assertion does not depend
// on Go's map iteration order leaking into the marshalled string.
func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var g, w map[string]any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %s", want)
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

// TestLayerEffective_TagsDeepMergeLikeTheRunDoes is the agreement the varmerge
// package exists to hold: the effective view an operator approves from and the
// set the run executes with resolve tag variables the same way. The worker
// deep-merges them (internal/worker/jobs.go mergeVar); a view that replaces
// instead shows a smaller tag set than the run will apply, and the operator
// approves a production apply against numbers that are not the ones used.
func TestLayerEffective_TagsDeepMergeLikeTheRunDoes(t *testing.T) {
	org := []EffectiveVariable{{
		Key: "tags", Value: `{"org":"nanohype","cost-center":"platform"}`,
		Category: "terraform", Source: "org", SourceID: "o1",
	}}
	ws := []EffectiveVariable{{
		Key: "tags", Value: `{"workspace":"prod-vpc"}`,
		Category: "terraform", Source: "workspace", SourceID: "w1",
	}}

	got := find(t, layerEffective(org, nil, ws), "tags", "terraform")

	want := `{"org":"nanohype","cost-center":"platform","workspace":"prod-vpc"}`
	if !sameJSON(t, got.Value, want) {
		t.Fatalf("tags resolved to %s, want %s — the org layer was dropped, so this view disagrees with the run", got.Value, want)
	}
}

// TestLayerEffective_SuffixedTagKeysMergeToo pins the suffix rule, which is what
// makes the agreement hold for the names infrastructure code actually uses.
func TestLayerEffective_SuffixedTagKeysMergeToo(t *testing.T) {
	for _, key := range []string{"tags", "default_tags", "extra_tags", "eks_tags"} {
		t.Run(key, func(t *testing.T) {
			org := []EffectiveVariable{{Key: key, Value: `{"a":"1"}`, Category: "terraform", Source: "org"}}
			ws := []EffectiveVariable{{Key: key, Value: `{"b":"2"}`, Category: "terraform", Source: "workspace"}}
			got := find(t, layerEffective(org, nil, ws), key, "terraform")
			if !sameJSON(t, got.Value, `{"a":"1","b":"2"}`) {
				t.Fatalf("%s resolved to %s, want both keys", key, got.Value)
			}
		})
	}
}

// TestLayerEffective_NonTagsReplace guards the other direction. Everything that
// is not a tag map must still be replaced outright by the higher scope, and a
// merge rule that swallowed ordinary variables would pass the tests above.
func TestLayerEffective_NonTagsReplace(t *testing.T) {
	org := []EffectiveVariable{{Key: "region", Value: "us-east-1", Category: "terraform", Source: "org"}}
	ws := []EffectiveVariable{{Key: "region", Value: "us-west-2", Category: "terraform", Source: "workspace"}}

	got := find(t, layerEffective(org, nil, ws), "region", "terraform")
	if got.Value != "us-west-2" {
		t.Fatalf("region = %q, want %q — a non-tag variable must be replaced, not merged", got.Value, "us-west-2")
	}
	if got.Source != "workspace" {
		t.Fatalf("source = %q, want workspace", got.Source)
	}
}

// TestLayerEffective_PipelineSitsBetween pins the middle scope, so the merge
// rule cannot be satisfied by an implementation that only knows two layers.
func TestLayerEffective_PipelineSitsBetween(t *testing.T) {
	org := []EffectiveVariable{{Key: "tags", Value: `{"a":"org"}`, Category: "terraform", Source: "org"}}
	pipe := []EffectiveVariable{{Key: "tags", Value: `{"b":"pipeline"}`, Category: "terraform", Source: "pipeline"}}
	ws := []EffectiveVariable{{Key: "tags", Value: `{"a":"workspace"}`, Category: "terraform", Source: "workspace"}}

	got := find(t, layerEffective(org, pipe, ws), "tags", "terraform")
	if !sameJSON(t, got.Value, `{"a":"workspace","b":"pipeline"}`) {
		t.Fatalf("tags = %s, want the workspace value to win the shared key and the pipeline key to survive", got.Value)
	}
}

// TestLayerEffective_RedactedTagsFallBackToReplacement records why the merge
// needs no sensitivity check. A sensitive value reaches this view already
// redacted, so it is not a JSON object, DeepMergeJSON declines, and the higher
// scope's value stands. Inventing a merged value out of two redactions would be
// worse than showing the one that wins.
func TestLayerEffective_RedactedTagsFallBackToReplacement(t *testing.T) {
	org := []EffectiveVariable{{Key: "tags", Value: "***", Category: "terraform", Sensitive: true, Source: "org"}}
	ws := []EffectiveVariable{{Key: "tags", Value: "***", Category: "terraform", Sensitive: true, Source: "workspace"}}

	got := find(t, layerEffective(org, nil, ws), "tags", "terraform")
	if got.Value != "***" {
		t.Fatalf("redacted tags resolved to %q, want %q", got.Value, "***")
	}
}
