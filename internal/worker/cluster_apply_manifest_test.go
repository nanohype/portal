package worker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nanohype/portal/internal/clusterspec"
)

// What the apply worker adds to an order.
//
// Two fields on every vended Cluster describe portal to the cluster rather than
// describing the cluster to portal, so neither is on the order form and neither
// has an operator who would notice it missing. Both fail the same quiet way: the
// cluster comes up, reports healthy, and is missing a capability nobody asked
// for explicitly — ArgoCD cannot pull the tenant manifests portal writes, or
// portal cannot authenticate to the kube API of a cluster it vended itself.
//
// A test of the happy path would not catch either. These assert the stamps are
// in the rendered manifest.

func orderJSON(t *testing.T, in clusterspec.Input) []byte {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal order: %v", err)
	}
	return raw
}

func sameAccountOrder() clusterspec.Input {
	return clusterspec.Input{
		Name: "analytics", Account: "222222222222", Region: "us-east-1", Team: "platform",
	}
}

func TestProvisionManifest_StampsTheTenantsRepo(t *testing.T) {
	w := &ClusterApplyJobWorker{tenantsRepoURL: "git@github.com:nanohype/tenants.git"}

	manifest, err := w.provisionManifest(orderJSON(t, sameAccountOrder()))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(manifest, "tenantsRepoUrl: git@github.com:nanohype/tenants.git") {
		t.Errorf("no tenantsRepoUrl in the rendered CR — the vended cluster registers no deploy key and its ArgoCD cannot pull portal's tenant manifests:\n%s", manifest)
	}
}

func TestProvisionManifest_LeavesTheTenantsRepoOutWhenPortalHasNone(t *testing.T) {
	// A portal deployment with no tenants repo configured stamps nothing rather
	// than an empty string, which the XRD would take as "registered no key" all
	// the same but which reads as a value someone set.
	w := &ClusterApplyJobWorker{}

	manifest, err := w.provisionManifest(orderJSON(t, sameAccountOrder()))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(manifest, "tenantsRepoUrl") {
		t.Errorf("rendered a tenantsRepoUrl with no repo configured:\n%s", manifest)
	}
}

func TestProvisionManifest_KeepsAnExplicitTenantsRepo(t *testing.T) {
	// The stamp fills a gap; it does not overrule an order that named one.
	w := &ClusterApplyJobWorker{tenantsRepoURL: "git@github.com:nanohype/tenants.git"}
	in := sameAccountOrder()
	in.TenantsRepoURL = "git@github.com:acme/their-tenants.git"

	manifest, err := w.provisionManifest(orderJSON(t, in))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(manifest, "acme/their-tenants.git") {
		t.Errorf("the stamp overwrote an explicitly ordered tenants repo:\n%s", manifest)
	}
}

func TestProvisionManifest_StampsTheHubRoleOnACrossAccountVend(t *testing.T) {
	w := &ClusterApplyJobWorker{hubRoleArn: "arn:aws:iam::111111111111:role/eks-fleet-crossplane"}
	in := sameAccountOrder()
	in.VendRoleArn = "arn:aws:iam::222222222222:role/production-eks-fleet-vend"
	in.ClusterPermissionsBoundaryArn = "arn:aws:iam::222222222222:policy/vend-boundary"
	in.OperatorPermissionsBoundaryArn = "arn:aws:iam::222222222222:policy/vend-boundary"

	manifest, err := w.provisionManifest(orderJSON(t, in))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(manifest, "bootstrapAccessRoleArn: arn:aws:iam::111111111111:role/eks-fleet-crossplane") {
		t.Errorf("no bootstrapAccessRoleArn on a cross-account vend:\n%s", manifest)
	}
}

func TestProvisionManifest_RefusesASpecTheXRDWouldReject(t *testing.T) {
	// Render validates, so a bad order fails here — before the manifest is
	// committed and the operation reported committed — rather than at admission
	// where only ArgoCD sees it.
	in := sameAccountOrder()
	in.VendRoleArn = "arn:aws:iam::222222222222:role/production-eks-fleet-vend"

	if _, err := (&ClusterApplyJobWorker{}).provisionManifest(orderJSON(t, in)); err == nil {
		t.Fatal("a cross-account vend with no permissions boundary rendered a manifest")
	}
}

func TestProvisionManifest_RejectsUnreadableSpecJSON(t *testing.T) {
	if _, err := (&ClusterApplyJobWorker{}).provisionManifest([]byte("{not json")); err == nil {
		t.Fatal("unreadable spec_json produced a manifest")
	}
}
