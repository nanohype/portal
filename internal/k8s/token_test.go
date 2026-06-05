package k8s

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

type fakeRT struct{ gotAuth string }

func (f *fakeRT) RoundTrip(r *http.Request) (*http.Response, error) {
	f.gotAuth = r.Header.Get("Authorization")
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func TestBearerInjector_MintsPerRequestAndDoesNotMutateCaller(t *testing.T) {
	rt := &fakeRT{}
	n := 0
	bi := &bearerInjector{rt: rt, token: func(context.Context) (string, error) {
		n++
		return fmt.Sprintf("tok-%d", n), nil
	}}

	req, _ := http.NewRequest(http.MethodGet, "https://x/", nil)
	if _, err := bi.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if rt.gotAuth != "Bearer tok-1" {
		t.Errorf("injected auth = %q, want Bearer tok-1", rt.gotAuth)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("caller's request was mutated (RoundTripper must clone)")
	}
	// Each request re-asks the source (the source itself caches/refreshes).
	if _, err := bi.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip 2: %v", err)
	}
	if rt.gotAuth != "Bearer tok-2" {
		t.Errorf("second auth = %q, want Bearer tok-2", rt.gotAuth)
	}
}

func TestBearerInjector_MintErrorSurfaced(t *testing.T) {
	bi := &bearerInjector{rt: &fakeRT{}, token: func(context.Context) (string, error) {
		return "", fmt.Errorf("assume-role denied")
	}}
	req, _ := http.NewRequest(http.MethodGet, "https://x/", nil)
	if _, err := bi.RoundTrip(req); err == nil {
		t.Error("expected the mint error to surface as a transport error")
	}
}

func TestBuildRestConfig_TokenSourceMode(t *testing.T) {
	cfg, err := BuildRestConfig(SlimConfig{
		APIEndpoint: "https://eks.example",
		CABundle:    []byte("ca-pem-bytes"),
		TokenSource: func(context.Context) (string, error) { return "t", nil },
	})
	if err != nil {
		t.Fatalf("BuildRestConfig: %v", err)
	}
	if cfg.BearerToken != "" {
		t.Error("static BearerToken must be empty in token-source mode")
	}
	if cfg.WrapTransport == nil {
		t.Error("WrapTransport must be set in token-source mode")
	}
}

func TestBuildRestConfig_TokenSourceStillRequiresCA(t *testing.T) {
	_, err := BuildRestConfig(SlimConfig{
		APIEndpoint: "https://eks.example",
		TokenSource: func(context.Context) (string, error) { return "t", nil },
	})
	if err == nil {
		t.Error("token-source mode still needs a CA bundle to verify the API server")
	}
}
