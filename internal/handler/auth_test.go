package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nanohype/portal/internal/config"
)

func TestOAuthStateVerification(t *testing.T) {
	h := &AuthHandler{cfg: &config.Config{JWTSecret: "test-secret-32-bytes-long-value!"}}

	state := "01JTEST1234567890ABCDEF"
	sig := h.signState(state)
	cookieVal := state + "." + sig

	// Valid state should verify
	req, _ := http.NewRequest("GET", "/callback?state="+state, nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: cookieVal})
	if !h.verifyState(req, state) {
		t.Error("verifyState should return true for valid state")
	}

	// Wrong state parameter should fail
	req2, _ := http.NewRequest("GET", "/callback?state=wrong", nil)
	req2.AddCookie(&http.Cookie{Name: "oauth_state", Value: cookieVal})
	if h.verifyState(req2, "wrong") {
		t.Error("verifyState should return false for mismatched state")
	}

	// Tampered signature should fail
	req3, _ := http.NewRequest("GET", "/callback?state="+state, nil)
	req3.AddCookie(&http.Cookie{Name: "oauth_state", Value: state + ".tampered"})
	if h.verifyState(req3, state) {
		t.Error("verifyState should return false for tampered signature")
	}

	// Missing cookie should fail
	req4, _ := http.NewRequest("GET", "/callback?state="+state, nil)
	if h.verifyState(req4, state) {
		t.Error("verifyState should return false when cookie is missing")
	}
}

func TestRedirectWithToken(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		wantSecure  bool
	}{
		{"development is not secure", "development", false},
		{"production is secure", "production", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &AuthHandler{cfg: &config.Config{
				Environment: tt.environment,
				WebURL:      "http://localhost:5173",
			}}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback", nil)
			h.redirectWithToken(rec, req, "header.payload.signature")

			// The redirect must never carry the token in the URL.
			location := rec.Header().Get("Location")
			if location != "http://localhost:5173/auth/callback" {
				t.Errorf("Location = %q, want %q", location, "http://localhost:5173/auth/callback")
			}
			if strings.Contains(location, "token") {
				t.Errorf("Location %q must not carry a token", location)
			}
			if rec.Code != http.StatusTemporaryRedirect {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusTemporaryRedirect)
			}

			// The token rides in a short-lived cookie scoped to the callback route.
			var cookie *http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == "auth_token" {
					cookie = c
				}
			}
			if cookie == nil {
				t.Fatal("expected auth_token cookie to be set")
			}
			if cookie.Value != "header.payload.signature" {
				t.Errorf("cookie value = %q, want the token", cookie.Value)
			}
			if cookie.Path != "/auth/callback" {
				t.Errorf("cookie path = %q, want /auth/callback", cookie.Path)
			}
			if cookie.MaxAge != 60 {
				t.Errorf("cookie max-age = %d, want 60", cookie.MaxAge)
			}
			if cookie.SameSite != http.SameSiteLaxMode {
				t.Errorf("cookie samesite = %v, want Lax", cookie.SameSite)
			}
			if cookie.Secure != tt.wantSecure {
				t.Errorf("cookie secure = %v, want %v", cookie.Secure, tt.wantSecure)
			}
			if cookie.HttpOnly {
				t.Error("cookie must not be HttpOnly — the SPA reads it via document.cookie")
			}
		})
	}
}

func TestSplitStateCookie(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"state.sig", 2},
		{"no-dot", 0},
		{"state.with.dots.sig", 2}, // splits on last dot
	}
	for _, tt := range tests {
		got := splitStateCookie(tt.input)
		if len(got) != tt.want {
			t.Errorf("splitStateCookie(%q) = %d parts, want %d", tt.input, len(got), tt.want)
		}
	}
}
