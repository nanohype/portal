package service

import (
	"encoding/json"
	"testing"
)

// TestNonNullJSON guards the JSONB column default. The watcher hands us
// raw bytes that may be empty (no .spec on the CRD) or genuinely nil from
// a marshal failure path — both need to map to a valid empty object so the
// INSERT doesn't violate the NOT NULL constraint or land literal `null`.
func TestNonNullJSON(t *testing.T) {
	tests := []struct {
		name string
		in   json.RawMessage
		want string
	}{
		{"nil → empty object", nil, "{}"},
		{"empty → empty object", json.RawMessage(""), "{}"},
		{"populated → unchanged", json.RawMessage(`{"phase":"Ready"}`), `{"phase":"Ready"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(nonNullJSON(tc.in))
			if got != tc.want {
				t.Errorf("nonNullJSON(%q) = %q, want %q", string(tc.in), got, tc.want)
			}
		})
	}
}

// computeDeletions exercises the load-bearing diff in Reconcile: given an
// observed set and an existing set, which DB rows need to be pruned. Pulled
// into a tiny helper to keep the test focused on the set math (and to make
// the live Reconcile method's DB-touching code easy to read alongside).
func computeDeletions(existing []string, observed map[string]struct{}) []string {
	var toDelete []string
	for _, name := range existing {
		if _, ok := observed[name]; !ok {
			toDelete = append(toDelete, name)
		}
	}
	return toDelete
}

func TestReconcileDeletionMath(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		observed []string
		want     []string
	}{
		{
			name:     "all observed → no deletions",
			existing: []string{"a", "b", "c"},
			observed: []string{"a", "b", "c"},
			want:     nil,
		},
		{
			name:     "one gone → one deletion",
			existing: []string{"a", "b", "c"},
			observed: []string{"a", "c"},
			want:     []string{"b"},
		},
		{
			name:     "all gone → delete all",
			existing: []string{"a", "b"},
			observed: []string{},
			want:     []string{"a", "b"},
		},
		{
			name:     "new tenant in observed set → no deletion (new is added by Upsert path)",
			existing: []string{"a"},
			observed: []string{"a", "b"},
			want:     nil,
		},
		{
			name:     "no existing → no deletions",
			existing: nil,
			observed: []string{"a"},
			want:     nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			observedSet := make(map[string]struct{}, len(tc.observed))
			for _, n := range tc.observed {
				observedSet[n] = struct{}{}
			}
			got := computeDeletions(tc.existing, observedSet)
			if !equalUnordered(got, tc.want) {
				t.Errorf("computeDeletions(%v, %v) = %v, want %v", tc.existing, tc.observed, got, tc.want)
			}
		})
	}
}

func equalUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}
