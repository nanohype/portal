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
			name: "fully populated is ready",
			obj: map[string]interface{}{"status": map[string]interface{}{
				"clusterEndpoint":          "https://abc.gr7.us-west-2.eks.amazonaws.com",
				"certificateAuthorityData": "TFMwdExTMA==",
				"clusterName":              "eks-dev",
			}},
			wantReady:   true,
			wantEnpoint: "https://abc.gr7.us-west-2.eks.amazonaws.com",
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
