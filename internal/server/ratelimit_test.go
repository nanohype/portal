package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRateLimiter(t *testing.T) {
	// Create a limiter that allows 2 req/s with burst of 3
	rl := NewRateLimiter(2, 3)

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First 3 requests should succeed (burst)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("request %d: got status %d, want %d", i, rr.Code, http.StatusOK)
		}
	}

	// Next request should be rate limited (burst exhausted)
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("request after burst: got status %d, want %d", rr.Code, http.StatusTooManyRequests)
	}

	// Different IP should still work
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "5.6.7.8:1234"
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Errorf("different IP: got status %d, want %d", rr2.Code, http.StatusOK)
	}

	// Same IP on different port should share the same bucket
	req3 := httptest.NewRequest("GET", "/", nil)
	req3.RemoteAddr = "1.2.3.4:9999" // different port, same IP
	rr3 := httptest.NewRecorder()
	handler.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusTooManyRequests {
		t.Errorf("same IP different port: got status %d, want %d (should share bucket)", rr3.Code, http.StatusTooManyRequests)
	}
}
