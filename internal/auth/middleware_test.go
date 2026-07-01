package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestMiddleware(t *testing.T) (*Middleware, string) {
	t.Helper()
	jwtAuth := NewJWTAuth("test-secret-32-bytes-long-value!", time.Hour)
	token, err := jwtAuth.GenerateToken("user-1", "org-1", "user@example.com", "operator")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return NewMiddleware(jwtAuth), token
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
