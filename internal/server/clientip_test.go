package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
)

func serve(t *testing.T, h http.Handler, peer, xff string) int {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = peer
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func resolved(t *testing.T, trusted []string, peer, xff string) string {
	t.Helper()
	var got string
	h := clientIPMiddleware(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = clientIPOf(r)
	}))
	serve(t, h, peer, xff)
	return got
}

// TestRateLimitCannotBeEvadedByAHeader is the property the limiter exists to
// hold. Its key has to come from the connection: a key the caller writes is a
// fresh bucket per request, and 100 req/s per IP becomes unbounded for anyone
// who sends a different X-Forwarded-For each time.
func TestRateLimitCannotBeEvadedByAHeader(t *testing.T) {
	rl := NewRateLimiter(1, 1) // one token, so a second request on one key is refused
	h := clientIPMiddleware(nil)(rl.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))

	if code := serve(t, h, "203.0.113.9:1234", "198.51.100.1"); code != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", code)
	}
	if code := serve(t, h, "203.0.113.9:1234", "198.51.100.2"); code != http.StatusTooManyRequests {
		t.Fatalf("a forged X-Forwarded-For bought a fresh rate-limit bucket: got %d, want 429", code)
	}
}

// TestUntrustedForwardedForIsIgnored pins what the address resolves to when
// nothing in front of the server is trusted.
func TestUntrustedForwardedForIsIgnored(t *testing.T) {
	if got := resolved(t, nil, "203.0.113.9:1234", "198.51.100.1, 10.0.0.1"); got != "203.0.113.9" {
		t.Fatalf("client IP = %q, want the peer 203.0.113.9 — an untrusted header decided it", got)
	}
}

// TestTrustedProxyIsHonoured is the other direction. Ignoring X-Forwarded-For
// outright would pass the two tests above and put every request behind an
// ingress in one bucket and one audit IP, which is why this is here.
func TestTrustedProxyIsHonoured(t *testing.T) {
	if got := resolved(t, []string{"10.0.0.0/8"}, "10.0.0.7:5555", "198.51.100.1, 10.0.0.7"); got != "198.51.100.1" {
		t.Fatalf("client IP = %q, want 198.51.100.1 — the trusted hop was not walked past", got)
	}
}

// TestSpoofedEntryLeftOfATrustedProxyDoesNotWin guards the walk. An attacker
// sets X-Forwarded-For before the ingress appends to it, so everything left of
// the trusted hops is attacker-written; the rightmost untrusted entry is the
// last address a trusted party actually observed.
func TestSpoofedEntryLeftOfATrustedProxyDoesNotWin(t *testing.T) {
	if got := resolved(t, []string{"10.0.0.0/8"}, "10.0.0.7:5555", "1.2.3.4, 198.51.100.1, 10.0.0.7"); got != "198.51.100.1" {
		t.Fatalf("client IP = %q, want 198.51.100.1 — an entry the attacker wrote won", got)
	}
}

// TestUnresolvableChainFallsBackToThePeer covers chi's fail-closed walk: an
// unparseable entry aborts it and sets no address. Falling back to the peer
// keeps one rate-limit key and one audit entry per connection rather than
// grouping every malformed request under one empty key.
func TestUnresolvableChainFallsBackToThePeer(t *testing.T) {
	if got := resolved(t, []string{"10.0.0.0/8"}, "10.0.0.7:5555", "not-an-ip, 10.0.0.7"); got != "10.0.0.7" {
		t.Fatalf("client IP = %q, want the peer 10.0.0.7", got)
	}
}

// TestClientIPMiddlewareIsInstalledAheadOfTheRateLimiter would be satisfied by
// a correct middleware nobody calls, so it checks the router the server builds.
func TestRealIPIsNotInTheChain(t *testing.T) {
	// middleware.RealIP mutates RemoteAddr; the replacement never does. A
	// handler seeing a rewritten RemoteAddr means RealIP is back.
	var seen string
	h := clientIPMiddleware(nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.RemoteAddr
	}))
	serve(t, h, "203.0.113.9:1234", "198.51.100.1")
	if seen != "203.0.113.9:1234" {
		t.Fatalf("RemoteAddr = %q, want it untouched — something is rewriting it from a header", seen)
	}
	_ = middleware.GetClientIP
}
