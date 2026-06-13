package service

import "testing"

// clusterStatus must extract the watch-back's fields from an eks-fleet Cluster
// XR .status defensively, and ready() must gate registration on all three being
// present — the XR exists (ArgoCD applied it) well before Crossplane fills in
// the endpoint/CA, so a half-populated status must read as not-ready.
func TestClusterStatusAndReady(t *testing.T) {
	tests := []struct {
		name        string
		obj         map[string]interface{}
		wantReady   bool
		wantEnpoint string
		wantName    string
	}{
		{
			name: "fully populated + XR Ready is ready",
			obj: map[string]interface{}{"status": map[string]interface{}{
				"clusterEndpoint":          "https://abc.gr7.us-west-2.eks.amazonaws.com",
				"certificateAuthorityData": "TFMwdExTMA==",
				"clusterName":              "eks-dev",
				"conditions": []interface{}{
					map[string]interface{}{"type": "Ready", "status": "True", "reason": "Available"},
				},
			}},
			wantReady:   true,
			wantEnpoint: "https://abc.gr7.us-west-2.eks.amazonaws.com",
			wantName:    "eks-dev",
		},
		{
			name: "endpoint/CA/name present but XR not Ready (bootstrap still building) is not ready",
			obj: map[string]interface{}{"status": map[string]interface{}{
				"clusterEndpoint":          "https://abc.eks.amazonaws.com",
				"certificateAuthorityData": "TFMwdExTMA==",
				"clusterName":              "eks-dev",
				"conditions": []interface{}{
					map[string]interface{}{"type": "Ready", "status": "False", "reason": "Creating"},
				},
			}},
			wantReady:   false,
			wantEnpoint: "https://abc.eks.amazonaws.com",
			wantName:    "eks-dev",
		},
		{
			name: "endpoint present but CA missing is not ready",
			obj: map[string]interface{}{"status": map[string]interface{}{
				"clusterEndpoint": "https://abc.eks.amazonaws.com",
				"clusterName":     "eks-dev",
			}},
			wantReady:   false,
			wantEnpoint: "https://abc.eks.amazonaws.com",
			wantName:    "eks-dev",
		},
		{
			name: "endpoint + CA but no cluster name is not ready",
			obj: map[string]interface{}{"status": map[string]interface{}{
				"clusterEndpoint":          "https://abc.eks.amazonaws.com",
				"certificateAuthorityData": "TFMwdExTMA==",
			}},
			wantReady: false,
		},
		{
			name:      "no status block is not ready",
			obj:       map[string]interface{}{"spec": map[string]interface{}{"account": "123456789012"}},
			wantReady: false,
		},
		{
			name: "non-string status fields are ignored, not panicked on",
			obj: map[string]interface{}{"status": map[string]interface{}{
				"clusterEndpoint": 42,
				"clusterName":     nil,
			}},
			wantReady: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := clusterStatus(tc.obj)
			if got := st.ready(); got != tc.wantReady {
				t.Errorf("ready() = %v, want %v", got, tc.wantReady)
			}
			if tc.wantEnpoint != "" && st.endpoint != tc.wantEnpoint {
				t.Errorf("endpoint = %q, want %q", st.endpoint, tc.wantEnpoint)
			}
			if tc.wantName != "" && st.clusterName != tc.wantName {
				t.Errorf("clusterName = %q, want %q", st.clusterName, tc.wantName)
			}
		})
	}
}

// conditions must defensively extract .status.conditions[] — Workspaces have no
// status early, conditions can be missing or oddly shaped mid-reconcile, and a
// non-map item must be skipped rather than panicked on.
func TestConditions(t *testing.T) {
	tests := []struct {
		name string
		obj  map[string]interface{}
		want int
	}{
		{"no status block", map[string]interface{}{"spec": map[string]interface{}{}}, 0},
		{"status but no conditions", map[string]interface{}{"status": map[string]interface{}{"x": 1}}, 0},
		{"conditions not a slice", map[string]interface{}{"status": map[string]interface{}{"conditions": "oops"}}, 0},
		{"odd items skipped", map[string]interface{}{"status": map[string]interface{}{"conditions": []interface{}{
			"not-a-map",
			map[string]interface{}{"type": "Ready", "status": "False", "reason": "Creating", "message": "creating"},
		}}}, 1},
		{"two valid", map[string]interface{}{"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Synced", "status": "True", "reason": "ReconcileSuccess"},
			map[string]interface{}{"type": "Ready", "status": "False", "reason": "Creating"},
		}}}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(conditions(tc.obj)); got != tc.want {
				t.Errorf("conditions() len = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReconcileError(t *testing.T) {
	cases := []struct {
		name  string
		conds []condition
		want  string
	}{
		{"synced ok", []condition{{condType: "Synced", status: "True", reason: "ReconcileSuccess"}}, ""},
		{"building not failed", []condition{{condType: "Ready", status: "False", reason: "Creating"}}, ""},
		{"reconcile error with message", []condition{{condType: "Synced", status: "False", reason: "ReconcileError", message: "tofu: insufficient capacity"}}, "tofu: insufficient capacity"},
		{"reconcile error no message falls back to reason", []condition{{condType: "Synced", status: "False", reason: "ReconcileError"}}, "ReconcileError"},
		{"async apply error on LastAsyncOperation", []condition{{condType: "LastAsyncOperation", status: "False", reason: "ApplyError", message: "tofu apply failed"}}, "tofu apply failed"},
		{"error surfaced on Ready", []condition{{condType: "Ready", status: "False", reason: "ReconcileError", message: "boom"}}, "boom"},
		{"Ready=False/Creating is normal building, not an error", []condition{{condType: "Ready", status: "False", reason: "Creating"}}, ""},
		{"error-ish reason on an unwatched condition type is ignored", []condition{{condType: "Healthy", status: "False", reason: "SomeError", message: "x"}}, ""},
		{"synced false but not an error reason", []condition{{condType: "Synced", status: "False", reason: "Deleting"}}, ""},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconcileError(tc.conds); got != tc.want {
				t.Errorf("reconcileError() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTofuState(t *testing.T) {
	building := []condition{{condType: "Ready", status: "False", reason: "Creating"}}
	erroring := []condition{{condType: "Synced", status: "False", reason: "ReconcileError", message: "boom"}}

	if b, _ := tofuState(nil); b {
		t.Error("no present workspaces: building = true, want false (timeline stays at committed)")
	}
	if b, msg := tofuState([][]condition{building}); !b || msg != "" {
		t.Errorf("building: got (%v, %q), want (true, '')", b, msg)
	}
	// A current error on EITHER workspace surfaces as the detail (not terminal).
	if b, msg := tofuState([][]condition{building, erroring}); !b || msg != "boom" {
		t.Errorf("erroring: got (%v, %q), want (true, boom)", b, msg)
	}
	// Present but no conditions yet still counts as building.
	if b, msg := tofuState([][]condition{nil}); !b || msg != "" {
		t.Errorf("present-no-conditions: got (%v, %q), want (true, '')", b, msg)
	}
}
