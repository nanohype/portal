package handler

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/stxkxs/tofui/internal/auth"
	"github.com/stxkxs/tofui/internal/handler/respond"
	"github.com/stxkxs/tofui/internal/service"
)

// TemplateHandler exposes CRUD for the admin-curated tenant templates that
// drive the tenant-create form. Read is open to any authenticated user
// (operators need to see what's available); write is admin-only.
type TemplateHandler struct {
	svc      *service.TemplateService
	auditSvc *service.AuditService
}

func NewTemplateHandler(svc *service.TemplateService, auditSvc *service.AuditService) *TemplateHandler {
	return &TemplateHandler{svc: svc, auditSvc: auditSvc}
}

// templateNameRe mirrors the k8s name regex used elsewhere — templates
// don't strictly need to be RFC-1123 names but the convention keeps the
// surface predictable across entities.
var templateNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type CreateTemplateRequest struct {
	Name                 string                 `json:"name"`
	Description          string                 `json:"description"`
	Persona              string                 `json:"persona"`
	DefaultValues        map[string]interface{} `json:"default_values"`
	AllowedOverrides     []string               `json:"allowed_overrides"`
	MaxBudgetUSD         int32                  `json:"max_budget_usd"`
	AllowedModelFamilies []string               `json:"allowed_model_families"`
	RequiredCompliance   []string               `json:"required_compliance"`
}

type UpdateTemplateRequest struct {
	Name                 string                 `json:"name"`
	Description          string                 `json:"description"`
	Persona              string                 `json:"persona"`
	DefaultValues        map[string]interface{} `json:"default_values"`
	AllowedOverrides     *[]string              `json:"allowed_overrides"`
	MaxBudgetUSD         *int32                 `json:"max_budget_usd"`
	AllowedModelFamilies *[]string              `json:"allowed_model_families"`
	RequiredCompliance   *[]string              `json:"required_compliance"`
}

func (h *TemplateHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	templates, err := h.svc.List(r.Context(), userCtx.OrgID)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list templates")
		return
	}
	respond.JSON(w, http.StatusOK, templates)
}

func (h *TemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	id := chi.URLParam(r, "templateID")
	t, err := h.svc.Get(r.Context(), id, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "template not found")
		return
	}
	respond.JSON(w, http.StatusOK, t)
}

func (h *TemplateHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	var req CreateTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Persona = strings.TrimSpace(req.Persona)

	if !templateNameRe.MatchString(req.Name) {
		respond.Error(w, http.StatusBadRequest, "name must be a valid RFC 1123 label (lowercase alphanumeric + hyphen, 1-63)")
		return
	}
	if len(req.Description) > 4096 {
		respond.Error(w, http.StatusBadRequest, "description must be at most 4096 characters")
		return
	}
	if req.Persona == "" {
		respond.Error(w, http.StatusBadRequest, "persona is required")
		return
	}
	if req.MaxBudgetUSD < 0 {
		respond.Error(w, http.StatusBadRequest, "max_budget_usd must be >= 0 (0 = no cap)")
		return
	}

	t, err := h.svc.Create(r.Context(), service.CreateTemplateParams{
		OrgID:                userCtx.OrgID,
		Name:                 req.Name,
		Description:          req.Description,
		Persona:              req.Persona,
		DefaultValues:        req.DefaultValues,
		AllowedOverrides:     req.AllowedOverrides,
		MaxBudgetUSD:         req.MaxBudgetUSD,
		AllowedModelFamilies: req.AllowedModelFamilies,
		RequiredCompliance:   req.RequiredCompliance,
		CreatedBy:            userCtx.UserID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			respond.Error(w, http.StatusConflict, "a template with this name already exists")
			return
		}
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to create template")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "template.create", EntityType: "template", EntityID: t.ID,
		After: t, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, t)
}

func (h *TemplateHandler) Update(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	id := chi.URLParam(r, "templateID")

	existing, err := h.svc.Get(r.Context(), id, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "template not found")
		return
	}

	var req UpdateTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name != "" && !templateNameRe.MatchString(req.Name) {
		respond.Error(w, http.StatusBadRequest, "name must be a valid RFC 1123 label")
		return
	}
	if len(req.Description) > 4096 {
		respond.Error(w, http.StatusBadRequest, "description must be at most 4096 characters")
		return
	}
	if req.MaxBudgetUSD != nil && *req.MaxBudgetUSD < 0 {
		respond.Error(w, http.StatusBadRequest, "max_budget_usd must be >= 0")
		return
	}

	updated, err := h.svc.Update(r.Context(), service.UpdateTemplateParams{
		ID:                   id,
		OrgID:                userCtx.OrgID,
		Name:                 req.Name,
		Description:          req.Description,
		Persona:              req.Persona,
		DefaultValues:        req.DefaultValues,
		AllowedOverrides:     req.AllowedOverrides,
		MaxBudgetUSD:         req.MaxBudgetUSD,
		AllowedModelFamilies: req.AllowedModelFamilies,
		RequiredCompliance:   req.RequiredCompliance,
	})
	if err != nil {
		if isUniqueViolation(err) {
			respond.Error(w, http.StatusConflict, "a template with this name already exists")
			return
		}
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to update template")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "template.update", EntityType: "template", EntityID: id,
		Before: existing, After: updated, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, updated)
}

func (h *TemplateHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	id := chi.URLParam(r, "templateID")

	existing, err := h.svc.Get(r.Context(), id, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "template not found")
		return
	}
	if err := h.svc.Delete(r.Context(), id, userCtx.OrgID); err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to delete template")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "template.delete", EntityType: "template", EntityID: id,
		Before: existing, IPAddress: ip, UserAgent: ua,
	})

	respond.NoContent(w)
}
