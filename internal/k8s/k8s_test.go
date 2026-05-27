package k8s

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"sync"
	"testing"
	"time"
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
		Subject:               pkix.Name{CommonName: "tofui-test-ca"},
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
