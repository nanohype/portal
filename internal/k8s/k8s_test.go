package k8s

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// generateTestCA returns a self-signed PEM-encoded CA. Kubernetes
// NewForConfig actually parses the cert bytes during client construction, so
// these tests need a structurally-valid certificate, not a placeholder
// string. Generated fresh per test run to avoid checking a long-lived cert
// into the repo.
func generateTestCA(t *testing.T) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "portal-test-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestBuildRestConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SlimConfig
		wantErr bool
	}{
		{
			name: "valid",
			cfg: SlimConfig{
				APIEndpoint: "https://example.com",
				CABundle:    []byte("ca-bytes"),
				BearerToken: "tok",
			},
			wantErr: false,
		},
		{
			name:    "missing endpoint",
			cfg:     SlimConfig{CABundle: []byte("ca"), BearerToken: "tok"},
			wantErr: true,
		},
		{
			name:    "missing token",
			cfg:     SlimConfig{APIEndpoint: "https://x", CABundle: []byte("ca")},
			wantErr: true,
		},
		{
			name:    "missing ca",
			cfg:     SlimConfig{APIEndpoint: "https://x", BearerToken: "tok"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := BuildRestConfig(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if cfg.Host != tc.cfg.APIEndpoint {
				t.Errorf("Host = %q, want %q", cfg.Host, tc.cfg.APIEndpoint)
			}
			if cfg.BearerToken != tc.cfg.BearerToken {
				t.Errorf("BearerToken mismatch")
			}
		})
	}
}

// ClientCache concurrent get on the same ID should produce a stable cached
// client without racing — covers the sync.RWMutex path under contention.
func TestClientCacheConcurrentGet(t *testing.T) {
	cache := NewClientCache()
	creds := SlimConfig{
		APIEndpoint: "https://example.com",
		CABundle:    generateTestCA(t),
		BearerToken: "tok",
	}

	var wg sync.WaitGroup
	const goroutines = 16
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.Get("cluster-a", creds); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Get returned error: %v", err)
	}
}

func TestClientCacheInvalidate(t *testing.T) {
	cache := NewClientCache()
	creds := SlimConfig{
		APIEndpoint: "https://example.com",
		CABundle:    generateTestCA(t),
		BearerToken: "tok-1",
	}
	c1, err := cache.Get("cluster-a", creds)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	c2, err := cache.Get("cluster-a", creds)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if c1 != c2 {
		t.Errorf("cache miss on second Get — should be the same client instance")
	}
	cache.Invalidate("cluster-a")
	c3, err := cache.Get("cluster-a", creds)
	if err != nil {
		t.Fatalf("post-invalidate Get: %v", err)
	}
	if c3 == c1 {
		t.Errorf("expected new client after Invalidate, got cached")
	}
}

// probeClient builds a fake clientset wired so Probe's three checks can be
// driven independently: a server version, a SelfSubjectReview outcome (the
// load-bearing auth proof), and node listing. nodesForbidden simulates a
// least-privilege token (e.g. `view`) that may not list nodes.
func probeClient(username string, ssrErr error, nodesForbidden bool, nodes ...runtime.Object) *fake.Clientset {
	cs := fake.NewSimpleClientset(nodes...)
	cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.35.0"}
	cs.PrependReactor("create", "selfsubjectreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		if ssrErr != nil {
			return true, nil, ssrErr
		}
		return true, &authenticationv1.SelfSubjectReview{
			Status: authenticationv1.SelfSubjectReviewStatus{
				UserInfo: authenticationv1.UserInfo{Username: username},
			},
		}, nil
	})
	if nodesForbidden {
		cs.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", errors.New("forbidden"))
		})
	}
	return cs
}

func readyNode(name string, ready bool) *corev1.Node {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: st}}},
	}
}

func TestProbe(t *testing.T) {
	ctx := context.Background()

	t.Run("authenticated, nodes forbidden -> connected, best-effort node count", func(t *testing.T) {
		s, err := Probe(ctx, probeClient("system:serviceaccount:kube-system:portal", nil, true))
		if err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if s.ServerVersion != "v1.35.0" {
			t.Errorf("server version = %q, want v1.35.0", s.ServerVersion)
		}
		if s.NodeCount != 0 {
			t.Errorf("node count = %d, want 0 (list forbidden -> best effort)", s.NodeCount)
		}
	})

	t.Run("authenticated, nodes listed -> counts ready nodes", func(t *testing.T) {
		s, err := Probe(ctx, probeClient("u", nil, false, readyNode("a", true), readyNode("b", false), readyNode("c", true)))
		if err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if s.NodeCount != 2 {
			t.Errorf("node count = %d, want 2", s.NodeCount)
		}
	})

	t.Run("anonymous identity -> error (auth proof is load-bearing)", func(t *testing.T) {
		if _, err := Probe(ctx, probeClient("system:anonymous", nil, true)); err == nil {
			t.Fatal("expected error for anonymous identity, got nil")
		}
	})

	t.Run("credential check fails -> error", func(t *testing.T) {
		if _, err := Probe(ctx, probeClient("", errors.New("401 unauthorized"), true)); err == nil {
			t.Fatal("expected error when SelfSubjectReview fails, got nil")
		}
	})
}
