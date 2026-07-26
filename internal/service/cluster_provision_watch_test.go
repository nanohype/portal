package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/nanohype/portal/internal/repository"
)

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

// TestResolveNameCollision pins what a register-time name collision is allowed
// to mean. Only one reading lets the op continue — its own earlier tick. Any
// other reading means a different cluster holds the name, and returning that
// cluster's id would mark the op active against an endpoint it never
// provisioned while the cluster it did provision runs unregistered and billing.
func TestResolveNameCollision(t *testing.T) {
	op := repository.ClusterOperation{
		ID: "op1", OrgID: "org-a", Name: "web", Environment: "production",
	}

	t.Run("own earlier tick resumes", func(t *testing.T) {
		existing := repository.Cluster{ID: "cl-1", OrgID: "org-a", Name: "web", Environment: "production"}
		id, err := resolveNameCollision(op, existing, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "cl-1" {
			t.Errorf("id = %q, want cl-1 — a crashed-then-retried tick must find its own row", id)
		}
	})

	t.Run("same name in another environment is another cluster", func(t *testing.T) {
		// The vend path writes clusters/<environment>/<name>.yaml, so this row
		// and this op are two separate EKS clusters that happen to share a name.
		existing := repository.Cluster{ID: "cl-dev", OrgID: "org-a", Name: "web", Environment: "development"}
		id, err := resolveNameCollision(op, existing, nil)
		if err == nil {
			t.Fatal("want an error; adopting the development row would alias the production vend onto it")
		}
		if id != "" {
			t.Errorf("id = %q, want empty — no id may be returned for a cluster this op did not vend", id)
		}
		if !strings.Contains(err.Error(), "development") || !strings.Contains(err.Error(), "production") {
			t.Errorf("error must name both environments so the collision is diagnosable, got: %v", err)
		}
	})

	t.Run("name held outside this org", func(t *testing.T) {
		// GetClusterByName is org-scoped, so a collision plus no row means the
		// name belongs to another org in the same deployment.
		id, err := resolveNameCollision(op, repository.Cluster{}, pgx.ErrNoRows)
		if err == nil {
			t.Fatal("want an error, not a silent success on an empty row")
		}
		if id != "" {
			t.Errorf("id = %q, want empty", id)
		}
		if !strings.Contains(err.Error(), "another org") {
			t.Errorf("error should say the name is held elsewhere, got: %v", err)
		}
	})

	t.Run("lookup failure is not a resume", func(t *testing.T) {
		boom := errors.New("connection reset")
		id, err := resolveNameCollision(op, repository.Cluster{}, boom)
		if !errors.Is(err, boom) {
			t.Errorf("error = %v, want it to wrap the lookup failure", err)
		}
		if id != "" {
			t.Errorf("id = %q, want empty", id)
		}
	})
}
