// Package k8s builds Kubernetes API clients from the slim per-cluster
// credentials tofui stores: API endpoint, CA bundle, and a service-account
// bearer token. This is the shape tofui talks to ARBITRARY EKS clusters with
// — distinct from the worker's in-cluster executor, which uses
// rest.InClusterConfig() against the single cluster the worker runs in.
package k8s

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// BuildDynamicClient returns a dynamic client for CRD reads — used to walk
// EAP custom resources (Tenants, Platforms, etc.) without vendoring their
// Go types.
func BuildDynamicClient(c SlimConfig) (dynamic.Interface, error) {
	cfg, err := BuildRestConfig(c)
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(cfg)
}

// TenantGVR is the GroupVersionResource for the EAP Tenant CRD. Centralized
// so the watcher and any future tenant-touching code stay in sync if the
// CRD's apiVersion ever bumps.
var TenantGVR = schema.GroupVersionResource{
	Group:    "agents.stxkxs.io",
	Version:  "v1alpha1",
	Resource: "tenants",
}

// SlimConfig is the minimum set of fields needed to talk to a Kubernetes API
// server. Equivalent to a kubeconfig that has exactly one cluster + one user
// with a bearer token — no contexts, no exec plugins, no auth providers.
type SlimConfig struct {
	APIEndpoint string // e.g. https://A1B2C3.gr7.us-west-2.eks.amazonaws.com
	CABundle    []byte // PEM-encoded CA cert (decrypted)
	BearerToken string // service-account token (decrypted)
}

// BuildRestConfig assembles a *rest.Config from the slim creds. Returned
// config has reasonable timeouts so a hung API server doesn't wedge a worker
// indefinitely.
func BuildRestConfig(c SlimConfig) (*rest.Config, error) {
	if c.APIEndpoint == "" {
		return nil, fmt.Errorf("api endpoint is required")
	}
	if c.BearerToken == "" {
		return nil, fmt.Errorf("bearer token is required")
	}
	if len(c.CABundle) == 0 {
		return nil, fmt.Errorf("ca bundle is required")
	}
	return &rest.Config{
		Host:        c.APIEndpoint,
		BearerToken: c.BearerToken,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: c.CABundle,
		},
		Timeout: 30 * time.Second,
	}, nil
}

// BuildClient returns a *kubernetes.Clientset from slim creds. Convenience
// over BuildRestConfig + kubernetes.NewForConfig for callers that don't need
// the rest.Config separately.
func BuildClient(c SlimConfig) (*kubernetes.Clientset, error) {
	cfg, err := BuildRestConfig(c)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// ClusterSummary is what the connection-test job records: a small set of
// observed facts about a reachable cluster, used both as proof-of-life and as
// useful display metadata in the UI.
type ClusterSummary struct {
	ServerVersion string // e.g. v1.33.3-eks-...
	NodeCount     int    // best-effort count of Ready nodes
}

// Probe runs a minimal pair of API calls to verify reachability + capture the
// summary. Used by the async connection-test job. Failures are surfaced as-is
// so the UI can show a useful error message.
func Probe(ctx context.Context, client *kubernetes.Clientset) (ClusterSummary, error) {
	ver, err := client.Discovery().ServerVersion()
	if err != nil {
		return ClusterSummary{}, fmt.Errorf("server version: %w", err)
	}

	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return ClusterSummary{}, fmt.Errorf("list nodes: %w", err)
	}

	ready := 0
	for _, n := range nodes.Items {
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				ready++
				break
			}
		}
	}
	return ClusterSummary{ServerVersion: ver.GitVersion, NodeCount: ready}, nil
}

// ClientCache memoizes built clientsets per cluster ID so handlers and
// workers don't pay the TLS handshake cost on every request. Tokens and CAs
// can rotate; Invalidate drops a cached entry so the next access rebuilds.
type ClientCache struct {
	mu      sync.RWMutex
	clients map[string]*kubernetes.Clientset
}

func NewClientCache() *ClientCache {
	return &ClientCache{clients: map[string]*kubernetes.Clientset{}}
}

// Get returns a cached client or builds one with the supplied creds. The
// caller is responsible for passing the current creds; the cache does not
// detect rotation on its own.
func (c *ClientCache) Get(clusterID string, creds SlimConfig) (*kubernetes.Clientset, error) {
	c.mu.RLock()
	if client, ok := c.clients[clusterID]; ok {
		c.mu.RUnlock()
		return client, nil
	}
	c.mu.RUnlock()

	client, err := BuildClient(creds)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.clients[clusterID] = client
	c.mu.Unlock()
	return client, nil
}

// Invalidate drops the cached client for a cluster. Call after an Update that
// changes credentials so the next Get rebuilds with the fresh values.
func (c *ClientCache) Invalidate(clusterID string) {
	c.mu.Lock()
	delete(c.clients, clusterID)
	c.mu.Unlock()
}
