package worker

import "testing"

// TestClusterManifestPath covers the path the cluster-apply worker writes to in
// the clusters repo. Like the tenant path it relies on sanitizePathSegment (also
// tested in tenant_apply_test.go) as the first line of defense against traversal;
// git.Repo's safeAbs is the second.
func TestClusterManifestPath(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		clusterName string
		want        string
	}{
		{"happy path", "dev", "dev-eks", "clusters/dev/dev-eks.yaml"},
		{"production", "production", "prod-eks", "clusters/production/prod-eks.yaml"},
		{"uppercase normalized", "Dev", "Prod-EKS", "clusters/dev/prod-eks.yaml"},
		{"slash sanitized", "a/b", "evil/x", "clusters/a-b/evil-x.yaml"},
		{"empty env", "", "cluster", "clusters/unknown/cluster.yaml"},
		{"empty name", "dev", "", "clusters/dev/unknown.yaml"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clusterManifestPath(tc.environment, tc.clusterName); got != tc.want {
				t.Errorf("clusterManifestPath(%q, %q) = %q, want %q", tc.environment, tc.clusterName, got, tc.want)
			}
		})
	}
}
