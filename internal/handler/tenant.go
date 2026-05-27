package handler

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/stxkxs/tofui/internal/auth"
	"github.com/stxkxs/tofui/internal/handler/respond"
	"github.com/stxkxs/tofui/internal/service"
)

// TenantHandler exposes read-only endpoints for tenants discovered by the
// cluster watcher. There is no Create / Update / Delete here in this slice —
// tenants are surface-of-truth-elsewhere data. Writes come in phase 2c via
// git commits to the tenants repo.
type TenantHandler struct {
	svc *service.TenantService
}

func NewTenantHandler(svc *service.TenantService) *TenantHandler {
	return &TenantHandler{svc: svc}
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

func toAny[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}
