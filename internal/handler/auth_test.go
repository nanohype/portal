package handler

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nanohype/portal/internal/auth"
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

// TestGitHubSignInRequiresAllowedOrg pins the fail-closed behavior: GitHub
// OAuth authenticates every GitHub account, so with no ALLOWED_GITHUB_ORG to
// check membership against there is nothing restricting who may sign in. Both
// entry points refuse rather than fall through to admitting everyone. An
// explicit development environment is the only exception — dev login already
// bypasses OAuth there.
func TestGitHubSignInRequiresAllowedOrg(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		allowedOrg  string
		wantRefused bool
	}{
		{"production without an org is refused", "production", "", true},
		{"staging without an org is refused", "staging", "", true},
		{"unset environment without an org is refused", "", "", true},
		{"production with an org proceeds", "production", "nanohype", false},
		{"development without an org proceeds", "development", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Environment:      tt.environment,
				AllowedGitHubOrg: tt.allowedOrg,
				JWTSecret:        "test-secret-32-bytes-long-value!",
				GitHubClientID:   "client-id",
				WebURL:           "http://localhost:5173",
			}
			h := NewAuthHandler(cfg, nil, nil)

			loginRec := httptest.NewRecorder()
			h.GitHubLogin(loginRec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/github", nil))

			// The callback is a separate entry point, so it carries its own guard.
			// It is driven with no code parameter: a refusal is 503, and anything
			// else means the guard let the request through to the OAuth exchange.
			cbRec := httptest.NewRecorder()
			h.GitHubCallback(cbRec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback", nil))

			if tt.wantRefused {
				if loginRec.Code != http.StatusServiceUnavailable {
					t.Errorf("GitHubLogin status = %d, want %d", loginRec.Code, http.StatusServiceUnavailable)
				}
				// A refused login must not start the OAuth dance at all.
				if loc := loginRec.Header().Get("Location"); loc != "" {
					t.Errorf("refused login redirected to %q, want no redirect", loc)
				}
				for _, c := range loginRec.Result().Cookies() {
					if c.Name == "oauth_state" && c.Value != "" {
						t.Error("refused login set an oauth_state cookie")
					}
				}
				if cbRec.Code != http.StatusServiceUnavailable {
					t.Errorf("GitHubCallback status = %d, want %d", cbRec.Code, http.StatusServiceUnavailable)
				}
				return
			}

			if loginRec.Code != http.StatusTemporaryRedirect {
				t.Errorf("GitHubLogin status = %d, want %d", loginRec.Code, http.StatusTemporaryRedirect)
			}
			if !strings.Contains(loginRec.Header().Get("Location"), "github.com/login/oauth/authorize") {
				t.Errorf("GitHubLogin Location = %q, want the GitHub authorize URL", loginRec.Header().Get("Location"))
			}
			if cbRec.Code != http.StatusBadRequest {
				t.Errorf("GitHubCallback status = %d, want %d (missing code, i.e. past the guard)", cbRec.Code, http.StatusBadRequest)
			}
		})
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

			// The token rides in a short-lived HttpOnly cookie scoped to the
			// handoff endpoint.
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
			if cookie.Path != "/api/v1/auth/handoff" {
				t.Errorf("cookie path = %q, want /api/v1/auth/handoff", cookie.Path)
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
			if !cookie.HttpOnly {
				t.Error("cookie must be HttpOnly — the SPA never reads it; it exchanges it via POST /auth/handoff")
			}
		})
	}
}

func TestHandoff(t *testing.T) {
	jwtAuth := auth.NewJWTAuth("test-secret-32-bytes-long-value!", time.Hour)
	h := &AuthHandler{
		cfg: &config.Config{Environment: "development", WebURL: "http://localhost:5173"},
		jwt: jwtAuth,
	}
	token, err := jwtAuth.GenerateToken("usr_1", "org_1", "dev@portal.local")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	t.Run("valid cookie returns the token once and expires the cookie", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/handoff", nil)
		req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
		h.Handoff(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Token != token {
			t.Errorf("token = %q, want the JWT from the cookie", body.Token)
		}

		// Delete-on-read: the same response must expire the cookie.
		var cookie *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == "auth_token" {
				cookie = c
			}
		}
		if cookie == nil {
			t.Fatal("expected an expiring auth_token cookie in the response")
		}
		if cookie.MaxAge >= 0 {
			t.Errorf("cookie max-age = %d, want < 0 (serialized as Max-Age=0)", cookie.MaxAge)
		}
		if cookie.Value != "" {
			t.Errorf("cookie value = %q, want empty", cookie.Value)
		}
		if cookie.Path != "/api/v1/auth/handoff" {
			t.Errorf("cookie path = %q, want /api/v1/auth/handoff", cookie.Path)
		}
		if !cookie.HttpOnly {
			t.Error("expiring cookie must mirror HttpOnly")
		}
	})

	t.Run("missing cookie is unauthorized", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/handoff", nil)
		h.Handoff(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("malformed token is unauthorized", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/handoff", nil)
		req.AddCookie(&http.Cookie{Name: "auth_token", Value: "not.a.jwt"})
		h.Handoff(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("second call after expiry is unauthorized", func(t *testing.T) {
		// Drive the exchange through a real server + cookie jar so the
		// browser-side delete-on-read semantics are what's under test: the jar
		// stores the handoff cookie, the first POST consumes it (the expiring
		// Set-Cookie evicts it from the jar), the second POST arrives bare and
		// gets 401.
		mux := http.NewServeMux()
		mux.HandleFunc("POST /api/v1/auth/handoff", h.Handoff)
		srv := httptest.NewServer(mux)
		defer srv.Close()

		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatalf("cookiejar: %v", err)
		}
		endpoint, err := url.Parse(srv.URL + "/api/v1/auth/handoff")
		if err != nil {
			t.Fatalf("parse url: %v", err)
		}
		jar.SetCookies(endpoint, []*http.Cookie{{
			Name:  "auth_token",
			Value: token,
			Path:  "/api/v1/auth/handoff",
		}})
		client := &http.Client{Jar: jar}

		first, err := client.Post(endpoint.String(), "", nil)
		if err != nil {
			t.Fatalf("first POST: %v", err)
		}
		first.Body.Close()
		if first.StatusCode != http.StatusOK {
			t.Fatalf("first POST status = %d, want %d", first.StatusCode, http.StatusOK)
		}
		if remaining := jar.Cookies(endpoint); len(remaining) != 0 {
			t.Errorf("jar still holds %d cookie(s) after the exchange, want 0", len(remaining))
		}

		second, err := client.Post(endpoint.String(), "", nil)
		if err != nil {
			t.Fatalf("second POST: %v", err)
		}
		second.Body.Close()
		if second.StatusCode != http.StatusUnauthorized {
			t.Errorf("second POST status = %d, want %d", second.StatusCode, http.StatusUnauthorized)
		}
	})
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
