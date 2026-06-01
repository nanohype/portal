package k8s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DiscoveredCluster is one cluster read from ArgoCD's cluster registry — the
// Secrets in the argocd namespace labeled
// argocd.argoproj.io/secret-type=cluster. It maps directly onto a SlimConfig.
type DiscoveredCluster struct {
	Name        string            // ArgoCD cluster name (data.name)
	Server      string            // API endpoint (data.server)
	BearerToken string            // config.bearerToken (empty for the in-cluster entry)
	CABundle    []byte            // PEM, decoded from config.tlsClientConfig.caData
	InCluster   bool              // server is the in-cluster address -> watch via the pod's SA
	Labels      map[string]string // the Secret's labels (cluster-bootstrap sets account_id/region/environment/cluster_name)
}

// argocdClusterConfig is the shape of an ArgoCD cluster Secret's `config` value.
type argocdClusterConfig struct {
	BearerToken     string `json:"bearerToken"`
	TLSClientConfig struct {
		CAData string `json:"caData"`
	} `json:"tlsClientConfig"`
	ExecProviderConfig *json.RawMessage `json:"execProviderConfig"`
}

// ListArgoCDClusters reads ArgoCD's cluster registry from the given namespace
// and returns the clusters the portal can watch: the in-cluster entry (watched
// via the pod's own ServiceAccount) plus any remote cluster carrying a static
// bearer token. Entries authenticating via an exec provider (e.g. AWS IAM) are
// skipped — the portal can only reuse static bearer-token credentials. The
// second return is the list of skipped clusters (name + reason) for logging.
func ListArgoCDClusters(ctx context.Context, client kubernetes.Interface, namespace string) ([]DiscoveredCluster, []string, error) {
	secrets, err := client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "argocd.argoproj.io/secret-type=cluster",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list argocd cluster secrets: %w", err)
	}
	var out []DiscoveredCluster
	var skipped []string
	for i := range secrets.Items {
		dc, reason := parseArgoCDClusterSecret(&secrets.Items[i])
		if reason != "" {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", secrets.Items[i].Name, reason))
			continue
		}
		out = append(out, dc)
	}
	return out, skipped, nil
}

func parseArgoCDClusterSecret(s *corev1.Secret) (DiscoveredCluster, string) {
	name := string(s.Data["name"])
	server := string(s.Data["server"])
	if name == "" || server == "" {
		return DiscoveredCluster{}, "missing name or server"
	}
	dc := DiscoveredCluster{Name: name, Server: server, Labels: s.Labels}

	// The in-cluster entry has no usable token — it's the cluster ArgoCD (and
	// the portal) run in, watched via the pod's own ServiceAccount.
	if server == InClusterAPIEndpoint {
		dc.InCluster = true
		return dc, ""
	}

	var cfg argocdClusterConfig
	if raw := s.Data["config"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return DiscoveredCluster{}, "unparseable config json"
		}
	}
	if cfg.ExecProviderConfig != nil && cfg.BearerToken == "" {
		return DiscoveredCluster{}, "exec-provider auth (portal needs a static token)"
	}
	if cfg.BearerToken == "" {
		return DiscoveredCluster{}, "no bearer token"
	}
	dc.BearerToken = cfg.BearerToken
	if cfg.TLSClientConfig.CAData != "" {
		ca, err := base64.StdEncoding.DecodeString(cfg.TLSClientConfig.CAData)
		if err != nil {
			return DiscoveredCluster{}, "unparseable caData"
		}
		dc.CABundle = ca
	}
	return dc, ""
}
