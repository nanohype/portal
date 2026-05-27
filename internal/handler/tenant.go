package handler

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/stxkxs/tofui/internal/auth"
	"github.com/stxkxs/tofui/internal/handler/respond"
	"github.com/stxkxs/tofui/internal/service"
)

// TenantHandler exposes read endpoints + the write-via-git endpoints. The
// read side surfaces what the watcher has observed; the write side enqueues
// jobs that render the chart, commit to the tenants repo, and let ArgoCD
// reconcile. Each write creates a `tenant_operations` row the UI can show.
type TenantHandler struct {
	svc      *service.TenantService
	auditSvc *service.AuditService
}

func NewTenantHandler(svc *service.TenantService, auditSvc *service.AuditService) *TenantHandler {
	return &TenantHandler{svc: svc, auditSvc: auditSvc}
}

// k8sNameRe is the RFC 1123 label rule: lowercase alphanumeric + hyphen,
// must start + end alphanumeric, ≤ 63 chars. Tenant names land as resource
// names in the cluster and as filenames in the tenants repo, so the rule
// applies in both contexts.
var k8sNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type CreateTenantRequest struct {
	ClusterID string                 `json:"cluster_id"`
	Name      string                 `json:"name"`
	Values    map[string]interface{} `json:"values"`
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

	tenants, total, err := h.svc.List(r.Context(), userCtx.OrgID, clusterID, page, perPage)
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
	userCtx := auth.GetUser(r.Context())
	tenantID := chi.URLParam(r, "tenantID")

	tenant, err := h.svc.Get(r.Context(), tenantID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	respond.JSON(w, http.StatusOK, tenant)
}

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
	req.ClusterID = strings.TrimSpace(req.ClusterID)
	req.Name = strings.TrimSpace(req.Name)

	if req.ClusterID == "" {
		respond.Error(w, http.StatusBadRequest, "cluster_id is required")
		return
	}
	if !k8sNameRe.MatchString(req.Name) {
		respond.Error(w, http.StatusBadRequest, "name must be a valid Kubernetes name (lowercase alphanumeric + hyphen, 1-63 chars)")
		return
	}
	if req.Values == nil {
		respond.Error(w, http.StatusBadRequest, "values is required")
		return
	}

	op, err := h.svc.EnqueueCreate(r.Context(), userCtx.OrgID, req.ClusterID, req.Name, userCtx.UserID, req.Values)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to enqueue tenant create")
		return
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

	tenant, err := h.svc.Get(r.Context(), tenantID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	op, err := h.svc.EnqueueDelete(r.Context(), userCtx.OrgID, tenant.ClusterID, tenant.Name, userCtx.UserID)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to enqueue tenant delete")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "tenant.delete_requested", EntityType: "tenant_operation", EntityID: op.ID,
		Before: tenant, After: op, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusAccepted, op)
}

// Operations returns the per-tenant operation log: every create/delete tofui
// has attempted against this tenant, with status + commit SHA + error.
func (h *TenantHandler) Operations(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	tenantID := chi.URLParam(r, "tenantID")

	tenant, err := h.svc.Get(r.Context(), tenantID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}
	ops, err := h.svc.ListOperations(r.Context(), userCtx.OrgID, tenant.ClusterID, tenant.Name)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list tenant operations")
		return
	}
	respond.JSON(w, http.StatusOK, ops)
}

func toAny[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}
