package worker

import "testing"

// TestTenantManifestPath / TestSanitizePathSegment together cover the path
// the worker writes to in the tenants repo. Adversarial inputs must produce
// safe paths — git.Repo's safeAbs is the second line of defense, this is
// the first.
func TestTenantManifestPath(t *testing.T) {
	tests := []struct {
		name        string
		clusterName string
		tenantName  string
		want        string
	}{
		{"happy path", "production-eks", "marketing-team", "tenants/production-eks/marketing-team.yaml"},
		{"uppercase normalized", "Prod-EKS", "Marketing", "tenants/prod-eks/marketing.yaml"},
		{"slash sanitized", "evil/cluster", "tenant", "tenants/evil-cluster/tenant.yaml"},
		{"dot-dot sanitized", "..", "tenant", "tenants/unknown/tenant.yaml"},
		{"empty cluster", "", "tenant", "tenants/unknown/tenant.yaml"},
		{"empty tenant", "cluster", "", "tenants/cluster/unknown.yaml"},
		{"unicode stripped", "クラスター", "テナント", "tenants/-----/----.yaml"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tenantManifestPath(tc.clusterName, tc.tenantName); got != tc.want {
				t.Errorf("tenantManifestPath(%q, %q) = %q, want %q", tc.clusterName, tc.tenantName, got, tc.want)
			}
		})
	}
}

func TestSanitizePathSegment(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"normal", "normal"},
		{"With-Hyphens-123", "with-hyphens-123"},
		{"under_score.dot", "under_score.dot"},
		{"slash/in/path", "slash-in-path"},
		{"", "unknown"},
		{".", "unknown"},
		{"..", "unknown"},
		{"   ", "---"},
		{"emoji-🚀-test", "emoji---test"}, // one rune for the emoji → one hyphen
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := sanitizePathSegment(tc.in); got != tc.want {
				t.Errorf("sanitizePathSegment(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
