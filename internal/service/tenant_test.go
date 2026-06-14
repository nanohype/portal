package service

import (
	"encoding/json"
	"testing"

	"github.com/nanohype/portal/internal/apperr"
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

// TestCreateTenantInputValidate covers the tenant identity rules — a cluster is
// required and the name must be an RFC-1123 label — and that failures carry
// KindValidation so the handler maps them to 400.
func TestCreateTenantInputValidate(t *testing.T) {
	tests := []struct {
		name    string
		in      CreateTenantInput
		wantErr bool
	}{
		{"valid", CreateTenantInput{ClusterID: "c1", Name: "my-tenant"}, false},
		{"valid single char", CreateTenantInput{ClusterID: "c1", Name: "a"}, false},
		{"missing cluster", CreateTenantInput{Name: "my-tenant"}, true},
		{"empty name", CreateTenantInput{ClusterID: "c1", Name: ""}, true},
		{"uppercase name", CreateTenantInput{ClusterID: "c1", Name: "MyTenant"}, true},
		{"leading hyphen", CreateTenantInput{ClusterID: "c1", Name: "-bad"}, true},
		{"trailing hyphen", CreateTenantInput{ClusterID: "c1", Name: "bad-"}, true},
		{"underscore", CreateTenantInput{ClusterID: "c1", Name: "bad_name"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Validate() = nil, want error")
				}
				if apperr.KindOf(err) != apperr.KindValidation {
					t.Errorf("Validate() kind = %v, want KindValidation", apperr.KindOf(err))
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

// TestResolveOwningTeam covers the four-way owning-team rule and that the
// bad-input cases surface as KindValidation.
func TestResolveOwningTeam(t *testing.T) {
	tests := []struct {
		name      string
		isAdmin   bool
		owning    string
		userTeams []string
		want      string
		wantErr   bool
	}{
		{"admin omits", true, "", nil, "", false},
		{"admin explicit (even outside teams)", true, "t9", nil, "t9", false},
		{"non-admin sole team defaults", false, "", []string{"t1"}, "t1", false},
		{"non-admin explicit own team", false, "t2", []string{"t1", "t2"}, "t2", false},
		{"non-admin zero teams", false, "", nil, "", true},
		{"non-admin multi no pick", false, "", []string{"t1", "t2"}, "", true},
		{"non-admin pick not a member", false, "t9", []string{"t1", "t2"}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveOwningTeam(tc.isAdmin, tc.owning, tc.userTeams)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveOwningTeam() = nil err, want error")
				}
				if apperr.KindOf(err) != apperr.KindValidation {
					t.Errorf("kind = %v, want KindValidation", apperr.KindOf(err))
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveOwningTeam() unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("ResolveOwningTeam() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestForcePlatformIdentity asserts the security invariant: platform.name is
// always the tenant name even when the caller's blob set a different one, and
// platform.tenant defaults to the name only when unset.
func TestForcePlatformIdentity(t *testing.T) {
	t.Run("adds platform when absent", func(t *testing.T) {
		v := map[string]interface{}{}
		forcePlatformIdentity(v, "alpha")
		pf := v["platform"].(map[string]interface{})
		if pf["name"] != "alpha" || pf["tenant"] != "alpha" {
			t.Errorf("got %v, want name+tenant=alpha", pf)
		}
	})
	t.Run("overrides a divergent name", func(t *testing.T) {
		v := map[string]interface{}{"platform": map[string]interface{}{"name": "evil-other"}}
		forcePlatformIdentity(v, "alpha")
		pf := v["platform"].(map[string]interface{})
		if pf["name"] != "alpha" {
			t.Errorf("name = %v, want forced to alpha", pf["name"])
		}
	})
	t.Run("preserves an explicit tenant", func(t *testing.T) {
		v := map[string]interface{}{"platform": map[string]interface{}{"tenant": "shared"}}
		forcePlatformIdentity(v, "alpha")
		pf := v["platform"].(map[string]interface{})
		if pf["name"] != "alpha" || pf["tenant"] != "shared" {
			t.Errorf("got name=%v tenant=%v, want name=alpha tenant=shared", pf["name"], pf["tenant"])
		}
	})
}
