package handler

import (
	"net/http"
	"testing"

	"github.com/nanohype/portal/internal/domain"
)

func TestOAuthStateVerification(t *testing.T) {
	h := &AuthHandler{cfg: &domain.Config{JWTSecret: "test-secret-32-bytes-long-value!"}}

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

func TestAssignRole(t *testing.T) {
	tests := []struct {
		name      string
		userCount int64
		want      string
	}{
		{"first user gets owner", 0, "owner"},
		{"second user gets viewer", 1, "viewer"},
		{"many users get viewer", 100, "viewer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := assignRole(tt.userCount)
			if got != tt.want {
				t.Errorf("assignRole(%d) = %q, want %q", tt.userCount, got, tt.want)
			}
		})
	}
}
