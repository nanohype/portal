package server

import (
	"context"
	"errors"
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

// stubAuthz is the authorization source the router reads under test: a user's
// org role, and the grants their teams hold per workspace.
//
// It is a real resolver, not a hole. The previous harness pointed the router at
// an unreachable database, which made every grant lookup error and every gate
// fall back to the org role alone — so nothing in the suite could see a grant,
// and a grant that leaked across workspaces would have gone unnoticed. Here the
// grants answer, which is what makes the cross-workspace tests below mean
// something.
type stubAuthz struct {
	// orgRole is keyed by user id. The route table uses the role name as the
	// user id, so "operator" is a user whose org role is operator.
	orgRole map[string]string
	// grants is keyed by "userID/workspaceID" — a team grant that has already
	// been capped by the member's role within that team.
	grants map[string]string
}

func (s stubAuthz) UserRole(_ context.Context, userID, _ string) (string, error) {
	if role, ok := s.orgRole[userID]; ok {
		return role, nil
	}
	// Unknown users in the route table are the role name itself, including
	// nonsense roles like "intern" that must clear no gate.
	return userID, nil
}

func (s stubAuthz) WorkspaceTeamRole(_ context.Context, workspaceID, userID, _ string) (string, error) {
	return s.grants[userID+"/"+workspaceID], nil
}

// newAuthzTestServer builds the real router with a resolver the test controls,
// in front of a pool that can never connect. Authorization runs entirely in
// middleware, so every gate behaves exactly as it does in production; a request
// that gets past its gate reaches a handler that cannot read the database and
// fails with a non-authorization status. That difference is what these tests
// assert on, which means an "allowed" case cannot pass by accidentally being
// denied.
func newAuthzTestServer(t *testing.T, authz AuthzResolver) *Server {
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

	return New(cfg, pool, slog.New(slog.NewTextHandler(io.Discard, nil)), WithAuthzResolver(authz))
}

// call issues one request as the named user. Each request carries its own client
// IP so the per-IP rate limiter never colours a result.
func call(t *testing.T, s *Server, method, path, userID string, seq int) int {
	t.Helper()

	jwt := auth.NewJWTAuth(testJWTSecret, time.Hour)
	token, err := jwt.GenerateToken(userID, "org-1", "user@example.com")
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
	// Discover reads the workspace's config and writes nothing, so it sits on
	// the read bar with the rest of the reads.
	{http.MethodPost, "/api/v1/workspaces/ws-1/variables/discover", []string{"viewer", "operator", "admin"}, []string{"intern"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/variables/import-outputs", []string{"admin"}, []string{"operator"}},
	{http.MethodPost, "/api/v1/workspaces/ws-1/variables/copy", []string{"admin"}, []string{"operator"}},
	{http.MethodPut, "/api/v1/workspaces/ws-1/variables/var-1", []string{"admin"}, []string{"operator"}},
	{http.MethodDelete, "/api/v1/workspaces/ws-1/variables/var-1", []string{"admin"}, []string{"operator"}},
	// Handing back a decrypted secret sits at the same bar as editing it.
	{http.MethodGet, "/api/v1/workspaces/ws-1/variables/var-1/value", []string{"admin", "owner"}, []string{"operator", "viewer"}},

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
	{http.MethodGet, "/api/v1/variables/var-1/value", []string{"admin", "owner"}, []string{"operator", "viewer"}},

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
	// Pipeline variables reach the worker's process environment the same way
	// org and workspace variables do, so they carry the same bar.
	{http.MethodPost, "/api/v1/pipelines/pipe-1/variables", []string{"admin"}, []string{"operator"}},
	{http.MethodPut, "/api/v1/pipelines/pipe-1/variables/var-1", []string{"admin"}, []string{"operator"}},
	{http.MethodDelete, "/api/v1/pipelines/pipe-1/variables/var-1", []string{"admin"}, []string{"operator"}},
	{http.MethodGet, "/api/v1/pipelines/pipe-1/variables/var-1/value", []string{"admin"}, []string{"operator", "viewer"}},
}

func TestRouteAuthorization(t *testing.T) {
	s := newAuthzTestServer(t, stubAuthz{})
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
	s := newAuthzTestServer(t, stubAuthz{})

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

// ── workspace team grants ───────────────────────────────────────────────────
//
// A grant is meant to raise what a team may do on ONE workspace. These pin both
// halves of that sentence: the grant has to actually work on the workspace it
// names, and it has to be worth nothing anywhere else.

// grantedAuthz is a viewer whose team holds admin on ws-1 and nothing on ws-2.
func grantedAuthz() stubAuthz {
	return stubAuthz{
		orgRole: map[string]string{"granted-viewer": "viewer"},
		grants:  map[string]string{"granted-viewer/ws-1": "admin"},
	}
}

// The grant elevates on the workspace it names. Without this passing, the
// cross-workspace denials below would be satisfied by a grant that simply never
// resolved.
func TestWorkspaceGrantElevatesOnItsOwnWorkspace(t *testing.T) {
	s := newAuthzTestServer(t, grantedAuthz())

	elevated := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/workspaces/ws-1/variables/var-1/value"},
		{http.MethodPut, "/api/v1/workspaces/ws-1/variables/var-1"},
		{http.MethodDelete, "/api/v1/workspaces/ws-1/variables/var-1"},
		{http.MethodGet, "/api/v1/workspaces/ws-1/state/st-1/download"},
		{http.MethodPost, "/api/v1/workspaces/ws-1/runs/run-1/cancel"},
		{http.MethodPost, "/api/v1/workspaces/ws-1/runs"},
	}

	for i, route := range elevated {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			code := call(t, s, route.method, route.path, "granted-viewer", 20000+i)
			if code == http.StatusUnauthorized || code == http.StatusForbidden {
				t.Fatalf("status = %d, want the grant to let this reach its handler", code)
			}
		})
	}
}

// The same grant is worth nothing on any other workspace. This is the defect
// the grant feature shipped with: the gate resolved the role from the URL's
// workspace, so a grant on one workspace was a role bump everywhere the caller
// could name a child object.
func TestWorkspaceGrantDoesNotReachAnotherWorkspace(t *testing.T) {
	s := newAuthzTestServer(t, grantedAuthz())

	denied := []struct {
		method string
		path   string
	}{
		// Reveal another workspace's decrypted variable.
		{http.MethodGet, "/api/v1/workspaces/ws-2/variables/var-1/value"},
		// Edit or drop another workspace's variable (env-category variables
		// become the worker's process environment).
		{http.MethodPut, "/api/v1/workspaces/ws-2/variables/var-1"},
		{http.MethodDelete, "/api/v1/workspaces/ws-2/variables/var-1"},
		// Download another workspace's raw tfstate.
		{http.MethodGet, "/api/v1/workspaces/ws-2/state/st-1/download"},
		{http.MethodDelete, "/api/v1/workspaces/ws-2/state/serial/3"},
		// Cancel or start another workspace's runs.
		{http.MethodPost, "/api/v1/workspaces/ws-2/runs/run-1/cancel"},
		{http.MethodPost, "/api/v1/workspaces/ws-2/runs"},
		// Bulk-move another workspace's variables.
		{http.MethodPost, "/api/v1/workspaces/ws-2/variables/copy"},
		{http.MethodPost, "/api/v1/workspaces/ws-2/variables/import-outputs"},
		// Change another workspace's settings, or delete it.
		{http.MethodPut, "/api/v1/workspaces/ws-2"},
		{http.MethodDelete, "/api/v1/workspaces/ws-2"},
	}

	for i, route := range denied {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			code := call(t, s, route.method, route.path, "granted-viewer", 21000+i)
			if code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 — a grant on ws-1 must be worth nothing on ws-2", code)
			}
		})
	}
}

// A grant never carries the org-scoped powers: it cannot widen itself, hand
// itself to another team, or sign off a gated production apply.
func TestWorkspaceGrantCarriesNoOrgAuthority(t *testing.T) {
	s := newAuthzTestServer(t, grantedAuthz())

	denied := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/workspaces/ws-1/access"},
		{http.MethodDelete, "/api/v1/workspaces/ws-1/access/team-1"},
		{http.MethodPost, "/api/v1/workspaces/ws-1/runs/run-1/approvals"},
		{http.MethodPost, "/api/v1/teams"},
		{http.MethodGet, "/api/v1/audit-logs"},
		{http.MethodPut, "/api/v1/users/user-2/role"},
	}

	for i, route := range denied {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			code := call(t, s, route.method, route.path, "granted-viewer", 22000+i)
			if code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 — a workspace grant carries no org authority", code)
			}
		})
	}
}

// A grant only ever raises. Granting a team viewer on a workspace must not take
// anything away from an admin who happens to be on that team, or adding a
// viewer grant would become a way to demote people.
func TestWorkspaceGrantNeverDemotes(t *testing.T) {
	s := newAuthzTestServer(t, stubAuthz{
		orgRole: map[string]string{"admin-user": "admin"},
		grants:  map[string]string{"admin-user/ws-1": "viewer"},
	})

	code := call(t, s, http.MethodGet, "/api/v1/workspaces/ws-1/variables/var-1/value", "admin-user", 23000)
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		t.Fatalf("status = %d, want an admin to keep their authority under a viewer grant", code)
	}
}

// An unreadable grant denies rather than elevating.
func TestWorkspaceGrantFailsClosed(t *testing.T) {
	s := newAuthzTestServer(t, failingAuthz{orgRole: "viewer"})

	code := call(t, s, http.MethodGet, "/api/v1/workspaces/ws-1/variables/var-1/value", "viewer", 24000)
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 when the grant lookup fails", code)
	}
}

// failingAuthz resolves an org role but cannot read grants.
type failingAuthz struct{ orgRole string }

func (f failingAuthz) UserRole(_ context.Context, _, _ string) (string, error) {
	return f.orgRole, nil
}

func (f failingAuthz) WorkspaceTeamRole(_ context.Context, _, _, _ string) (string, error) {
	return "", errors.New("grant lookup unavailable")
}
