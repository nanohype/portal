package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/config"
)

const testJWTSecret = "test-secret-32-bytes-long-value!"

// newAuthzTestServer builds the real router in front of a pool that can never
// connect. Authorization runs entirely in middleware, so every gate behaves
// exactly as it does in production; a request that gets past its gate reaches a
// handler that cannot read the database and fails with a non-authorization
// status. That difference is what these tests assert on, which means an
// "allowed" case cannot pass by accidentally being denied.
func newAuthzTestServer(t *testing.T) *Server {
	t.Helper()

	pool, err := pgxpool.New(context.Background(), "postgres://portal:portal@127.0.0.1:1/portal?connect_timeout=1")
	if err != nil {
		t.Fatalf("build unreachable pool: %v", err)
	}
	t.Cleanup(pool.Close)

	cfg := &config.Config{
		ServerAddr:    ":0",
		WebURL:        "http://localhost:5173",
		Environment:   "test",
		JWTSecret:     testJWTSecret,
		JWTExpiration: time.Hour,
	}

	return New(cfg, pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// call issues one request as a user holding the given org role. Each request
// carries its own client IP so the per-IP rate limiter never colours a result.
func call(t *testing.T, s *Server, method, path, role string, seq int) int {
	t.Helper()

	jwt := auth.NewJWTAuth(testJWTSecret, time.Hour)
	token, err := jwt.GenerateToken("user-1", "org-1", "user@example.com", role)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	var body io.Reader
	if method == http.MethodPost || method == http.MethodPut {
		body = strings.NewReader("{}")
	}

	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", fmt.Sprintf("10.0.%d.%d", seq/250, seq%250))

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	return rec.Code
}

// routeGate is one route plus the roles that must and must not get through.
// Both directions are mandatory: a deny-only expectation would also be met by
// a route that refuses everybody.
type routeGate struct {
	method  string
	path    string
	allowed []string
	denied  []string
}

// gatedRoutes pins the authorization bar of every route that carries one.
// "denied" means the gate answered 401/403; "allowed" means the request reached
// its handler.
var gatedRoutes = []routeGate{
	// ── workspaces ──────────────────────────────────────────────────────
	{http.MethodGet, "/api/v1/workspaces", []string{"viewer", "owner"}, []string{"intern"}},
	{http.MethodPost, "/api/v1/workspaces", []string{"operator", "admin"}, []string{"viewer"}},
	{http.MethodGet, "/api/v1/workspaces/ws-1", []string{"viewer"}, []string{"intern"}},
	{http.MethodPut, "/api/v1/workspaces/ws-1", []string{"operator"}, []string{"viewer"}},
	{http.MethodDelete, "/api/v1/workspaces/ws-1", []string{"admin", "owner"}, []string{"operator"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/upload", []string{"operator"}, []string{"viewer"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/lock", []string{"operator"}, []string{"viewer"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/unlock", []string{"operator"}, []string{"viewer"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/clone", []string{"operator"}, []string{"viewer"}},

	// ── workspace variables ─────────────────────────────────────────────
	{http.MethodGet, "/api/v1/workspaces/ws-1/variables", []string{"viewer"}, []string{"intern"}},
	{http.MethodGet, "/api/v1/workspaces/ws-1/variables/effective", []string{"viewer"}, []string{"intern"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/variables", []string{"admin"}, []string{"operator", "viewer"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/variables/bulk", []string{"admin"}, []string{"operator"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/variables/discover", []string{"admin"}, []string{"operator"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/variables/import-outputs", []string{"admin"}, []string{"operator"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/variables/copy", []string{"admin"}, []string{"operator"}},
	{http.MethodPut, "/api/v1/workspaces/ws-1/variables/var-1", []string{"admin"}, []string{"operator"}},
	{http.MethodDelete, "/api/v1/workspaces/ws-1/variables/var-1", []string{"admin"}, []string{"operator"}},
	{http.MethodGet, "/api/v1/workspaces/ws-1/variables/var-1/value", []string{"operator"}, []string{"viewer"}},

	// ── workspace state ─────────────────────────────────────────────────
	{http.MethodGet, "/api/v1/workspaces/ws-1/state", []string{"viewer"}, []string{"intern"}},
	{http.MethodGet, "/api/v1/workspaces/ws-1/state/current/outputs", []string{"viewer"}, []string{"intern"}},
	{http.MethodGet, "/api/v1/workspaces/ws-1/state/current/resources", []string{"viewer"}, []string{"intern"}},
	{http.MethodGet, "/api/v1/workspaces/ws-1/state/st-1/download", []string{"admin"}, []string{"operator", "viewer"}},
	{http.MethodDelete, "/api/v1/workspaces/ws-1/state/serial/3", []string{"admin"}, []string{"operator"}},

	// ── workspace team grants ───────────────────────────────────────────
	{http.MethodGet, "/api/v1/workspaces/ws-1/access", []string{"viewer"}, []string{"intern"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/access", []string{"admin"}, []string{"operator"}},
	{http.MethodDelete, "/api/v1/workspaces/ws-1/access/team-1", []string{"admin"}, []string{"operator"}},

	// ── runs ────────────────────────────────────────────────────────────
	{http.MethodGet, "/api/v1/workspaces/ws-1/runs", []string{"viewer"}, []string{"intern"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/runs", []string{"operator"}, []string{"viewer"}},
	{http.MethodGet, "/api/v1/workspaces/ws-1/runs/run-1", []string{"viewer"}, []string{"intern"}},
	{http.MethodGet, "/api/v1/workspaces/ws-1/runs/run-1/plan-json", []string{"viewer"}, []string{"intern"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/runs/run-1/cancel", []string{"operator"}, []string{"viewer"}},
	{http.MethodGet, "/api/v1/workspaces/ws-1/runs/run-1/approvals", []string{"viewer"}, []string{"intern"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/runs/run-1/approvals", []string{"admin"}, []string{"operator", "viewer"}},

	// ── org variables ───────────────────────────────────────────────────
	{http.MethodGet, "/api/v1/variables", []string{"viewer"}, []string{"intern"}},
	{http.MethodPost, "/api/v1/variables", []string{"admin"}, []string{"operator"}},
	{http.MethodPut, "/api/v1/variables/var-1", []string{"admin"}, []string{"operator"}},
	{http.MethodDelete, "/api/v1/variables/var-1", []string{"admin"}, []string{"operator"}},
	{http.MethodGet, "/api/v1/variables/var-1/value", []string{"operator"}, []string{"viewer"}},

	// ── teams ───────────────────────────────────────────────────────────
	{http.MethodGet, "/api/v1/teams", []string{"viewer"}, []string{"intern"}},
	{http.MethodPost, "/api/v1/teams", []string{"admin"}, []string{"operator"}},
	{http.MethodGet, "/api/v1/teams/team-1/members", []string{"viewer"}, []string{"intern"}},
	{http.MethodPost, "/api/v1/teams/team-1/members", []string{"admin"}, []string{"operator"}},
	{http.MethodDelete, "/api/v1/teams/team-1", []string{"admin"}, []string{"operator"}},

	// ── fleet reads ─────────────────────────────────────────────────────
	{http.MethodGet, "/api/v1/accounts", []string{"admin"}, []string{"operator", "viewer"}},
	{http.MethodGet, "/api/v1/accounts/acct-1", []string{"admin"}, []string{"operator"}},
	{http.MethodGet, "/api/v1/cluster-orders", []string{"admin"}, []string{"operator"}},
	{http.MethodGet, "/api/v1/cluster-orders/development/c-1/operations", []string{"admin"}, []string{"operator"}},

	// ── org administration ──────────────────────────────────────────────
	{http.MethodGet, "/api/v1/users", []string{"admin"}, []string{"operator"}},
	{http.MethodPut, "/api/v1/users/user-2/role", []string{"owner"}, []string{"admin"}},
	{http.MethodGet, "/api/v1/audit-logs", []string{"admin"}, []string{"operator"}},

	// ── pipelines ───────────────────────────────────────────────────────
	{http.MethodPost, "/api/v1/pipelines/pipe-1/runs", []string{"operator"}, []string{"viewer"}},
	{http.MethodDelete, "/api/v1/pipelines/pipe-1", []string{"admin"}, []string{"operator"}},
}

func TestRouteAuthorization(t *testing.T) {
	s := newAuthzTestServer(t)
	seq := 0

	for _, route := range gatedRoutes {
		for _, role := range route.denied {
			seq++
			name := fmt.Sprintf("%s %s denies %s", route.method, route.path, role)
			t.Run(name, func(t *testing.T) {
				code := call(t, s, route.method, route.path, role, seq)
				if code != http.StatusUnauthorized && code != http.StatusForbidden {
					t.Fatalf("status = %d, want 401 or 403", code)
				}
			})
		}

		for _, role := range route.allowed {
			seq++
			name := fmt.Sprintf("%s %s permits %s", route.method, route.path, role)
			t.Run(name, func(t *testing.T) {
				code := call(t, s, route.method, route.path, role, seq)
				if code == http.StatusUnauthorized || code == http.StatusForbidden {
					t.Fatalf("status = %d, want the request to reach its handler", code)
				}
			})
		}
	}
}

// Authentication still comes before authorization on every gated route.
func TestGatedRoutesRejectAnonymous(t *testing.T) {
	s := newAuthzTestServer(t)

	for i, route := range gatedRoutes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			var body io.Reader
			if route.method == http.MethodPost || route.method == http.MethodPut {
				body = strings.NewReader("{}")
			}
			req := httptest.NewRequest(route.method, route.path, body)
			req.Header.Set("X-Real-IP", fmt.Sprintf("10.9.%d.%d", i/250, i%250))
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}
