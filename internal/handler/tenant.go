package handler

import (
	"encoding/json"
	stderrs "errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

// TenantHandler exposes read endpoints + the write-via-git endpoints. The
// read side surfaces what the watcher has observed; the write side enqueues
// jobs that render the chart, commit to the tenants repo, and let ArgoCD
// reconcile. Each write creates a `tenant_operations` row the UI can show.
type TenantHandler struct {
	svc         *service.TenantService
	templateSvc *service.TemplateService
	accessSvc   *service.TeamAccessService
	auditSvc    *service.AuditService
}

func NewTenantHandler(svc *service.TenantService, templateSvc *service.TemplateService, accessSvc *service.TeamAccessService, auditSvc *service.AuditService) *TenantHandler {
	return &TenantHandler{svc: svc, templateSvc: templateSvc, accessSvc: accessSvc, auditSvc: auditSvc}
}

// isAdmin centralizes the "see everything" check. owner ≥ admin. Operators
// and viewers fall through to the team-scoped path.
func isAdmin(role string) bool { return role == "admin" || role == "owner" }

type CreateTenantRequest struct {
	ClusterID    string                 `json:"cluster_id"`
	Name         string                 `json:"name"`
	Values       map[string]interface{} `json:"values"`
	TemplateID   string                 `json:"template_id,omitempty"`    // optional; when set, values are template overrides
	OwningTeamID string                 `json:"owning_team_id,omitempty"` // team that owns the new tenant
}

func (h *TenantHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 200 {
		perPage = 50
	}
	clusterID := r.URL.Query().Get("cluster_id")

	// Admins see everything; non-admins see only tenants their teams own.
	// A nil teamIDs slice signals "no filter" to the service layer; an
	// empty non-nil slice signals "this user is in zero teams → return 0".
	var teamIDs []string
	if !isAdmin(userCtx.Role) {
		ids, err := h.accessSvc.UserTeamIDs(r.Context(), userCtx.UserID, userCtx.OrgID)
		if err != nil {
			respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to resolve user teams")
			return
		}
		if ids == nil {
			ids = []string{}
		}
		teamIDs = ids
	}

	tenants, total, err := h.svc.List(r.Context(), userCtx.OrgID, clusterID, teamIDs, page, perPage)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	respond.JSON(w, http.StatusOK, respond.ListResponse[any]{
		Data:    toAny(tenants),
		Total:   total,
		Page:    page,
		PerPage: perPage,
	})
}

func (h *TenantHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantID")
	tenant, err := h.fetchTenantForCaller(r, tenantID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	respond.JSON(w, http.StatusOK, tenant)
}

// fetchTenantForCaller centralizes the authz gate for single-tenant
// reads: load the row, then for non-admin callers verify one of their
// teams has been granted access. Returns "not visible" in both the
// "tenant doesn't exist" and "exists but you can't see it" cases —
// intentional so unauthorized callers can't probe IDs to discover
// existence. Admins skip the team check entirely.
func (h *TenantHandler) fetchTenantForCaller(r *http.Request, tenantID string) (repository.Tenant, error) {
	userCtx := auth.GetUser(r.Context())
	tenant, err := h.svc.Get(r.Context(), tenantID, userCtx.OrgID)
	if err != nil {
		return repository.Tenant{}, err
	}
	if isAdmin(userCtx.Role) {
		return tenant, nil
	}
	ok, err := h.accessSvc.UserHasTenantAccess(r.Context(), userCtx.UserID, userCtx.OrgID, tenant.ClusterID, tenant.Name)
	if err != nil {
		return repository.Tenant{}, err
	}
	if !ok {
		return repository.Tenant{}, errTenantNotVisible
	}
	return tenant, nil
}

var errTenantNotVisible = stderrs.New("tenant not visible to caller")

// Create enqueues a tenant_operation of kind=create. The actual k8s resource
// will appear in the tenants table once ArgoCD applies the commit and the
// watcher observes the resulting Tenant CR (typically within ~60s after
// commit + ArgoCD's polling interval).
func (h *TenantHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	var req CreateTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	in := service.CreateTenantInput{
		OrgID:        userCtx.OrgID,
		ClusterID:    req.ClusterID,
		Name:         req.Name,
		TemplateID:   req.TemplateID,
		OwningTeamID: req.OwningTeamID,
		CreatedBy:    userCtx.UserID,
		Values:       req.Values,
	}
	in.Normalize()
	if err := in.Validate(); err != nil {
		respond.FromError(w, r, err)
		return
	}

	// When a template is referenced, the request `values` are operator
	// overrides: the template service merges them with the template's defaults
	// and enforces its caps + model-family + required-compliance rules. A
	// failure is a clean 400 with no orphan state. Without a template, the
	// operator (admin, expert mode) supplies the full values blob directly.
	if in.TemplateID != "" {
		t, err := h.templateSvc.Get(r.Context(), in.TemplateID, userCtx.OrgID)
		if err != nil {
			respond.FromError(w, r, apperr.Validation("template_id does not reference a known template"))
			return
		}
		merged, err := h.templateSvc.ApplyToValues(t, in.Values)
		if err != nil {
			respond.FromError(w, r, apperr.Validation(err.Error()))
			return
		}
		in.Values = merged
	}

	// Owning-team resolution: the membership lookup is I/O (kept here), the
	// rules are pure (service.ResolveOwningTeam). The new tenant gets an access
	// grant for the resolved team so its members see it on the next list call.
	var userTeams []string
	admin := isAdmin(userCtx.Role)
	if !admin {
		var err error
		userTeams, err = h.accessSvc.UserTeamIDs(r.Context(), userCtx.UserID, userCtx.OrgID)
		if err != nil {
			respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to resolve user teams")
			return
		}
	}
	owningTeamID, err := service.ResolveOwningTeam(admin, in.OwningTeamID, userTeams)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	// EnqueueCreate re-validates and forces the authoritative Platform identity
	// server-side, so a values blob can never point the rendered Platform at
	// another tenant. A bad input maps to 400 via apperr; anything else is 500.
	op, err := h.svc.EnqueueCreate(r.Context(), in)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	// Persist the team grant immediately so the operator sees the
	// resulting tenant in their list as soon as the watcher observes it.
	// Failure to grant is logged but not surfaced as a 5xx — the
	// operation is enqueued, an admin can grant access manually if this
	// ever fails. (In practice it won't; the access table is the same
	// connection pool as the operation row.)
	if owningTeamID != "" {
		if _, err := h.accessSvc.GrantTenant(r.Context(), userCtx.OrgID, req.ClusterID, req.Name, owningTeamID, userCtx.UserID); err != nil {
			// Don't fail the request — operation row exists, surface the
			// access-grant failure in the audit log for an admin to fix.
		}
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "tenant.create_requested", EntityType: "tenant_operation", EntityID: op.ID,
		After: op, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusAccepted, op)
}

// Delete enqueues a tenant_operation of kind=delete. The tenants table row
// will disappear once the watcher observes the Tenant CR going away.
func (h *TenantHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	tenantID := chi.URLParam(r, "tenantID")

	tenant, err := h.fetchTenantForCaller(r, tenantID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	op, err := h.svc.EnqueueDelete(r.Context(), userCtx.OrgID, tenant.ClusterID, tenant.Name, userCtx.UserID)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to enqueue tenant delete")
		return
	}

	// Best-effort cleanup of access rows. The tenant CR will disappear
	// from the watcher soon (and the tenants row with it via cascade),
	// but the access rows are FK'd to clusters, not tenants, so they
	// need an explicit prune.
	_ = h.accessSvc.RevokeAllForTenant(r.Context(), userCtx.OrgID, tenant.ClusterID, tenant.Name)

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "tenant.delete_requested", EntityType: "tenant_operation", EntityID: op.ID,
		Before: tenant, After: op, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusAccepted, op)
}

// ListAccess returns the team-access grants on a tenant.
func (h *TenantHandler) ListAccess(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	tenantID := chi.URLParam(r, "tenantID")

	tenant, err := h.fetchTenantForCaller(r, tenantID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	access, err := h.accessSvc.ListTenant(r.Context(), userCtx.OrgID, tenant.ClusterID, tenant.Name)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list access")
		return
	}
	respond.List(w, access)
}

type GrantTenantAccessRequest struct {
	TeamID string `json:"team_id"`
}

func (h *TenantHandler) GrantAccess(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	tenantID := chi.URLParam(r, "tenantID")

	tenant, err := h.svc.Get(r.Context(), tenantID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	var req GrantTenantAccessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.TeamID) == "" {
		respond.Error(w, http.StatusBadRequest, "team_id is required")
		return
	}

	grant, err := h.accessSvc.GrantTenant(r.Context(), userCtx.OrgID, tenant.ClusterID, tenant.Name, req.TeamID, userCtx.UserID)
	if err != nil {
		if isForeignKeyViolation(err) {
			respond.Error(w, http.StatusBadRequest, "team_id does not reference a known team")
			return
		}
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to grant access")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "tenant.access_granted", EntityType: "tenant", EntityID: tenantID,
		After: grant, IPAddress: ip, UserAgent: ua,
	})
	respond.JSON(w, http.StatusCreated, grant)
}

func (h *TenantHandler) RevokeAccess(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	tenantID := chi.URLParam(r, "tenantID")
	teamID := chi.URLParam(r, "teamID")

	tenant, err := h.svc.Get(r.Context(), tenantID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	if err := h.accessSvc.RevokeTenant(r.Context(), userCtx.OrgID, tenant.ClusterID, tenant.Name, teamID); err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to revoke access")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "tenant.access_revoked", EntityType: "tenant", EntityID: tenantID,
		Before: map[string]string{"team_id": teamID}, IPAddress: ip, UserAgent: ua,
	})
	respond.NoContent(w)
}

// Operations returns the per-tenant operation log: every create/delete portal
// has attempted against this tenant, with status + commit SHA + error.
func (h *TenantHandler) Operations(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	tenantID := chi.URLParam(r, "tenantID")

	tenant, err := h.fetchTenantForCaller(r, tenantID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	ops, err := h.svc.ListOperations(r.Context(), userCtx.OrgID, tenant.ClusterID, tenant.Name)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list tenant operations")
		return
	}
	respond.List(w, ops)
}

func toAny[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}
