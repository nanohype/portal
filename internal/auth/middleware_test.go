package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stubRoles is a UserRoleResolver backed by a map, so a test can move somebody's
// role without a database.
type stubRoles struct {
	roles map[string]string
	err   error
}

func (s stubRoles) UserRole(_ context.Context, userID, orgID string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.roles[userID+"/"+orgID], nil
}

func newTestMiddleware(t *testing.T) (*Middleware, string) {
	t.Helper()
	jwtAuth := NewJWTAuth("test-secret-32-bytes-long-value!", time.Hour)
	token, err := jwtAuth.GenerateToken("user-1", "org-1", "user@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return NewMiddleware(jwtAuth, stubRoles{roles: map[string]string{"user-1/org-1": "operator"}}), token
}

func TestAuthenticate(t *testing.T) {
	m, token := newTestMiddleware(t)

	tests := []struct {
		name       string
		header     string
		value      string
		target     string
		wantStatus int
	}{
		{
			name:       "authorization header",
			header:     "Authorization",
			value:      "Bearer " + token,
			target:     "/api/v1/runs",
			wantStatus: http.StatusOK,
		},
		{
			name:       "websocket subprotocol",
			header:     "Sec-WebSocket-Protocol",
			value:      "bearer, " + token,
			target:     "/api/v1/runs",
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing credentials",
			target:     "/api/v1/runs",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "token in query param is not accepted",
			target:     "/api/v1/runs?token=" + token,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid token in authorization header",
			header:     "Authorization",
			value:      "Bearer not-a-jwt",
			target:     "/api/v1/runs",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "authorization header without bearer scheme",
			header:     "Authorization",
			value:      "Basic dXNlcjpwYXNz",
			target:     "/api/v1/runs",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "subprotocol list without bearer prefix",
			header:     "Sec-WebSocket-Protocol",
			value:      token,
			target:     "/api/v1/runs",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid token in subprotocol list",
			header:     "Sec-WebSocket-Protocol",
			value:      "bearer, not-a-jwt",
			target:     "/api/v1/runs",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotUser *UserContext
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotUser = GetUser(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if tt.header != "" {
				req.Header.Set(tt.header, tt.value)
			}
			rec := httptest.NewRecorder()

			m.Authenticate(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK {
				if gotUser == nil {
					t.Fatal("expected user context to be set")
				}
				if gotUser.UserID != "user-1" || gotUser.OrgID != "org-1" || gotUser.Role != "operator" {
					t.Errorf("user context = %+v, want user-1/org-1/operator", gotUser)
				}
			}
		})
	}
}

func TestWebsocketBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"bearer plus token", "bearer, abc.def.ghi", "abc.def.ghi"},
		{"no space after comma", "bearer,abc.def.ghi", "abc.def.ghi"},
		{"empty header", "", ""},
		{"bearer alone", "bearer", ""},
		{"wrong first protocol", "graphql-ws, abc.def.ghi", ""},
		{"token without bearer prefix", "abc.def.ghi", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := websocketBearerToken(tt.header); got != tt.want {
				t.Errorf("websocketBearerToken(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

// The role a request is decided with comes from the users table, not the token.
// A token minted while someone was a viewer carries their new role the moment
// it changes — and loses the old one just as fast.
func TestAuthenticateResolvesRolePerRequest(t *testing.T) {
	jwtAuth := NewJWTAuth("test-secret-32-bytes-long-value!", time.Hour)
	token, err := jwtAuth.GenerateToken("user-1", "org-1", "user@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	roles := map[string]string{"user-1/org-1": "viewer"}
	m := NewMiddleware(jwtAuth, stubRoles{roles: roles})

	roleFor := func(t *testing.T) string {
		t.Helper()
		var got string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = GetUser(r.Context()).Role
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		m.Authenticate(next).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		return got
	}

	if got := roleFor(t); got != "viewer" {
		t.Fatalf("role = %q, want viewer", got)
	}

	// Promotion lands on the very next request — no re-login, no waiting out
	// the token.
	roles["user-1/org-1"] = "operator"
	if got := roleFor(t); got != "operator" {
		t.Fatalf("after promotion role = %q, want operator", got)
	}

	// Demotion is just as immediate: the same unexpired token now carries less.
	roles["user-1/org-1"] = "viewer"
	if got := roleFor(t); got != "viewer" {
		t.Fatalf("after demotion role = %q, want viewer", got)
	}
}

// An unreadable role denies. It answers 503 rather than 401 so a database blip
// cannot make every client throw its session away.
func TestAuthenticateFailsClosedOnResolverError(t *testing.T) {
	jwtAuth := NewJWTAuth("test-secret-32-bytes-long-value!", time.Hour)
	token, err := jwtAuth.GenerateToken("user-1", "org-1", "user@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	cases := []struct {
		name     string
		resolver UserRoleResolver
		want     int
	}{
		{"lookup error", stubRoles{err: errors.New("connection refused")}, http.StatusServiceUnavailable},
		{"no resolver wired", nil, http.StatusServiceUnavailable},
		{"user no longer exists", stubRoles{roles: map[string]string{}}, http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			NewMiddleware(jwtAuth, tc.resolver).Authenticate(next).ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if reached {
				t.Fatal("handler ran despite an unresolved role")
			}
		})
	}
}
