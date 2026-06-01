package k8s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func clusterSecret(name string, labels map[string]string, data map[string]string) *corev1.Secret {
	d := map[string][]byte{}
	for k, v := range data {
		d[k] = []byte(v)
	}
	l := map[string]string{"argocd.argoproj.io/secret-type": "cluster"}
	for k, v := range labels {
		l[k] = v
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "argocd", Labels: l},
		Data:       d,
	}
}

func cfgJSON(t *testing.T, bearer, caPEM string) string {
	t.Helper()
	c := argocdClusterConfig{BearerToken: bearer}
	if caPEM != "" {
		c.TLSClientConfig.CAData = base64.StdEncoding.EncodeToString([]byte(caPEM))
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return string(b)
}

func TestListArgoCDClusters(t *testing.T) {
	ctx := context.Background()
	objs := []*corev1.Secret{
		clusterSecret("in-cluster",
			map[string]string{"environment": "dev", "region": "us-west-2", "cluster_name": "dev-eks"},
			map[string]string{"name": "in-cluster", "server": InClusterAPIEndpoint}),
		clusterSecret("remote-eks", nil, map[string]string{
			"name":   "remote-eks",
			"server": "https://remote.eks.amazonaws.com",
			"config": cfgJSON(t, "tok-123", "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"),
		}),
		clusterSecret("iam-eks", nil, map[string]string{
			"name":   "iam-eks",
			"server": "https://iam.eks.amazonaws.com",
			"config": `{"execProviderConfig":{"command":"aws"}}`,
		}),
		clusterSecret("notoken", nil, map[string]string{
			"name":   "notoken",
			"server": "https://x.example.com",
			"config": `{"tlsClientConfig":{}}`,
		}),
	}
	// A non-cluster secret in the same namespace must be ignored by the selector.
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "repo-cred", Namespace: "argocd",
		Labels: map[string]string{"argocd.argoproj.io/secret-type": "repository"}}}

	cs := fake.NewSimpleClientset(other)
	for _, s := range objs {
		if _, err := cs.CoreV1().Secrets("argocd").Create(ctx, s, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed secret %s: %v", s.Name, err)
		}
	}

	got, skipped, err := ListArgoCDClusters(ctx, cs, "argocd")
	if err != nil {
		t.Fatalf("ListArgoCDClusters: %v", err)
	}
	byName := map[string]DiscoveredCluster{}
	for _, c := range got {
		byName[c.Name] = c
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 usable clusters (in-cluster + remote-eks), got %d: %v", len(got), got)
	}
	ic, ok := byName["in-cluster"]
	if !ok || !ic.InCluster {
		t.Errorf("in-cluster entry should be present with InCluster=true, got %+v", ic)
	}
	if ic.Labels["cluster_name"] != "dev-eks" {
		t.Errorf("in-cluster labels not carried through: %v", ic.Labels)
	}
	r, ok := byName["remote-eks"]
	if !ok || r.InCluster {
		t.Errorf("remote-eks should be present, not in-cluster: %+v", r)
	}
	if r.BearerToken != "tok-123" {
		t.Errorf("remote-eks bearer token = %q, want tok-123", r.BearerToken)
	}
	if len(r.CABundle) == 0 || string(r.CABundle[:5]) != "-----" {
		t.Errorf("remote-eks CA not decoded to PEM: %q", string(r.CABundle))
	}
	// iam-eks (exec) and notoken must be skipped, not errors.
	if len(skipped) != 2 {
		t.Errorf("expected 2 skipped (iam-eks, notoken), got %v", skipped)
	}
}
