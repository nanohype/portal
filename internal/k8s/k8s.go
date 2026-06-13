// Package k8s builds Kubernetes API clients from the slim per-cluster
// credentials portal stores: API endpoint, CA bundle, and a service-account
// bearer token. This is the shape portal talks to ARBITRARY EKS clusters with
// — distinct from the worker's in-cluster executor, which uses
// rest.InClusterConfig() against the single cluster the worker runs in.
package k8s

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// BuildDynamicClient returns a dynamic client for CRD reads — used to walk
// eks-agent-platform custom resources (Tenants, Platforms, etc.) without
// vendoring their Go types.
func BuildDynamicClient(c SlimConfig) (dynamic.Interface, error) {
	cfg, err := BuildRestConfig(c)
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(cfg)
}

// TenantGVR is the GroupVersionResource for the eks-agent-platform Tenant CRD. Centralized
// so the watcher and any future tenant-touching code stay in sync if the
// CRD's apiVersion ever bumps.
var TenantGVR = schema.GroupVersionResource{
	Group:    "platform.nanohype.dev",
	Version:  "v1alpha1",
	Resource: "tenants",
}

// ClusterGVR is the GroupVersionResource for the eks-fleet Cluster XR. The
// provision watch-back reads these on the hub to learn when a vended cluster's
// EKS endpoint + CA are up, so it can auto-register the new cluster.
var ClusterGVR = schema.GroupVersionResource{
	Group:    "fleet.nanohype.dev",
	Version:  "v1alpha1",
	Resource: "clusters",
}

// WorkspaceGVR is the GroupVersionResource for the provider-opentofu Workspaces
// an eks-fleet Cluster XR composes (<name>-stack + <name>-bootstrap). The
// provision watch-back reads their .status.conditions on the hub to project the
// tofu build phase (and any tofu error) onto the vend timeline.
var WorkspaceGVR = schema.GroupVersionResource{
	Group:    "opentofu.m.upbound.io",
	Version:  "v1beta1",
	Resource: "workspaces",
}

// InClusterAPIEndpoint is the API server address a pod uses to reach its own
// cluster. An ArgoCD "in-cluster" registry entry carries this as its server and
// has no bearer token — the portal watches it with its own mounted
// ServiceAccount rather than stored credentials.
const InClusterAPIEndpoint = "https://kubernetes.default.svc"

// SlimConfig is the minimum set of fields needed to talk to a Kubernetes API
// server. Equivalent to a kubeconfig that has exactly one cluster + one user
// with a bearer token — no contexts, no exec plugins, no auth providers. When
// InCluster is set (or APIEndpoint is the in-cluster address), the pod's own
// mounted ServiceAccount is used and CABundle/BearerToken are ignored.
type SlimConfig struct {
	APIEndpoint string // e.g. https://A1B2C3.gr7.us-west-2.eks.amazonaws.com
	CABundle    []byte // PEM-encoded CA cert (decrypted)
	BearerToken string // service-account token (decrypted) — static-token mode
	InCluster   bool   // use the pod's mounted ServiceAccount (no stored creds)
	// TokenSource, when set, supplies a fresh bearer token per request — EKS IAM
	// auth mode. Used instead of the static BearerToken; it caches + refreshes
	// internally (aws.Provider.EKSTokenSource), so a cached client keeps working
	// as the short-lived token rotates and portal stores no long-lived credential.
	TokenSource func(ctx context.Context) (string, error)
}

// BuildRestConfig assembles a *rest.Config from the slim creds. Returned
// config has reasonable timeouts so a hung API server doesn't wedge a worker
// indefinitely.
func BuildRestConfig(c SlimConfig) (*rest.Config, error) {
	if c.APIEndpoint == "" {
		return nil, fmt.Errorf("api endpoint is required")
	}
	// In-cluster: authenticate with the pod's own mounted ServiceAccount. Used
	// for the local cluster the portal runs in (e.g. an ArgoCD in-cluster
	// registry entry, which has no bearer token). Errors if not actually
	// running in a pod.
	if c.InCluster || c.APIEndpoint == InClusterAPIEndpoint {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
		cfg.Timeout = 30 * time.Second
		return cfg, nil
	}
	if len(c.CABundle) == 0 {
		return nil, fmt.Errorf("ca bundle is required")
	}
	cfg := &rest.Config{
		Host:            c.APIEndpoint,
		TLSClientConfig: rest.TLSClientConfig{CAData: c.CABundle},
		Timeout:         30 * time.Second,
	}
	// EKS IAM mode: a transport wrapper injects a freshly-minted token per
	// request, so the built client can be cached while the token underneath
	// rotates. No static credential is stored.
	if c.TokenSource != nil {
		ts := c.TokenSource
		cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
			return &bearerInjector{rt: rt, token: ts}
		}
		return cfg, nil
	}
	if c.BearerToken == "" {
		return nil, fmt.Errorf("bearer token or token source is required")
	}
	cfg.BearerToken = c.BearerToken
	return cfg, nil
}

// bearerInjector sets a freshly-minted bearer token on each request — the
// transport behind EKS IAM auth, where tokens are short-lived.
type bearerInjector struct {
	rt    http.RoundTripper
	token func(ctx context.Context) (string, error)
}

func (b *bearerInjector) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := b.token(req.Context())
	if err != nil {
		return nil, fmt.Errorf("mint bearer token: %w", err)
	}
	// Clone so the caller's request isn't mutated (RoundTripper contract).
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+tok)
	return b.rt.RoundTrip(r)
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

// Probe verifies reachability + that the stored credentials authenticate, and
// captures the summary. Used by the async connection-test job. Failures are
// surfaced as-is so the UI can show a useful error message.
func Probe(ctx context.Context, client kubernetes.Interface) (ClusterSummary, error) {
	ver, err := client.Discovery().ServerVersion()
	if err != nil {
		return ClusterSummary{}, fmt.Errorf("server version: %w", err)
	}

	// /version above proves reachability but not authentication — clusters
	// often serve discovery to anonymous callers. A SelfSubjectReview echoes
	// back the authenticated identity, needs no RBAC grant (so it works with a
	// least-privilege token like `view`), and fails closed if the token is
	// invalid. This is the load-bearing credential check.
	review, err := client.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return ClusterSummary{}, fmt.Errorf("verify credentials: %w", err)
	}
	if u := review.Status.UserInfo.Username; u == "" || u == "system:anonymous" {
		return ClusterSummary{}, fmt.Errorf("credentials did not authenticate (resolved to %q)", u)
	}

	// Node count is best-effort: a least-privilege token may not list nodes,
	// and that must not fail an otherwise-healthy, authenticated connection.
	ready := 0
	if nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
		for _, n := range nodes.Items {
			for _, c := range n.Status.Conditions {
				if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
					ready++
					break
				}
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
