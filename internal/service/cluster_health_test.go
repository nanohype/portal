package service

import "testing"

// applicationHealth must defensively pull sync.status + health.status from an
// ArgoCD Application's unstructured .status — the status block is absent early in
// a reconcile, and sub-objects can be missing or oddly shaped mid-flight.
func TestApplicationHealth(t *testing.T) {
	tests := []struct {
		name       string
		obj        map[string]interface{}
		wantSync   string
		wantHealth string
	}{
		{"no status", map[string]interface{}{"spec": map[string]interface{}{}}, "", ""},
		{"status but no sync/health", map[string]interface{}{"status": map[string]interface{}{"x": 1}}, "", ""},
		{
			name: "fully populated",
			obj: map[string]interface{}{"status": map[string]interface{}{
				"sync":   map[string]interface{}{"status": "Synced"},
				"health": map[string]interface{}{"status": "Healthy"},
			}},
			wantSync:   "Synced",
			wantHealth: "Healthy",
		},
		{
			name: "out of sync + degraded",
			obj: map[string]interface{}{"status": map[string]interface{}{
				"sync":   map[string]interface{}{"status": "OutOfSync"},
				"health": map[string]interface{}{"status": "Degraded"},
			}},
			wantSync:   "OutOfSync",
			wantHealth: "Degraded",
		},
		{
			name: "sync present, health sub-object missing",
			obj: map[string]interface{}{"status": map[string]interface{}{
				"sync": map[string]interface{}{"status": "Synced"},
			}},
			wantSync:   "Synced",
			wantHealth: "",
		},
		{
			name: "sync not a map is ignored, not panicked on",
			obj: map[string]interface{}{"status": map[string]interface{}{
				"sync":   "oops",
				"health": map[string]interface{}{"status": "Progressing"},
			}},
			wantSync:   "",
			wantHealth: "Progressing",
		},
		{
			name: "status field not a string",
			obj: map[string]interface{}{"status": map[string]interface{}{
				"sync": map[string]interface{}{"status": 42},
			}},
			wantSync:   "",
			wantHealth: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sync, health := applicationHealth(tc.obj)
			if sync != tc.wantSync || health != tc.wantHealth {
				t.Errorf("applicationHealth() = (%q, %q), want (%q, %q)", sync, health, tc.wantSync, tc.wantHealth)
			}
		})
	}
}

func TestNestedString(t *testing.T) {
	m := map[string]interface{}{
		"a": map[string]interface{}{"b": map[string]interface{}{"c": "deep"}},
	}
	if got := nestedString(m, "a", "b", "c"); got != "deep" {
		t.Errorf("deep walk = %q, want deep", got)
	}
	if got := nestedString(m, "a", "x", "c"); got != "" {
		t.Errorf("missing mid-key = %q, want ''", got)
	}
	if got := nestedString(m, "a", "b"); got != "" {
		t.Errorf("terminal is a map, not a string = %q, want ''", got)
	}
}
