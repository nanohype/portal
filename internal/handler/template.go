package handler

import (
	"encoding/json"
	stderrs "errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

// TemplateHandler exposes CRUD for the admin-curated tenant templates that
// drive the tenant-create form. Read is open to any authenticated user
// (operators need to see what's available); write is admin-only.
type TemplateHandler struct {
	svc       *service.TemplateService
	accessSvc *service.TeamAccessService
	auditSvc  *service.AuditService
}

func NewTemplateHandler(svc *service.TemplateService, accessSvc *service.TeamAccessService, auditSvc *service.AuditService) *TemplateHandler {
	return &TemplateHandler{svc: svc, accessSvc: accessSvc, auditSvc: auditSvc}
}

// templateNameRe mirrors the k8s name regex used elsewhere — templates
// don't strictly need to be RFC-1123 names but the convention keeps the
// surface predictable across entities.
var templateNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// TemplateResponse projects repository.Template for API + audit consumption.
// The JSON-column fields pass through as stored.
type TemplateResponse struct {
	ID                   string          `json:"id"`
	OrgID                string          `json:"org_id"`
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Persona              string          `json:"persona"`
	DefaultValues        json.RawMessage `json:"default_values"`
	AllowedOverrides     json.RawMessage `json:"allowed_overrides"`
	MaxBudgetUSD         int32           `json:"max_budget_usd"`
	AllowedModelFamilies json.RawMessage `json:"allowed_model_families"`
	RequiredCompliance   json.RawMessage `json:"required_compliance"`
	CreatedBy            string          `json:"created_by"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

func templateResponse(t repository.Template) TemplateResponse {
	return TemplateResponse{
		ID:                   t.ID,
		OrgID:                t.OrgID,
		Name:                 t.Name,
		Description:          t.Description,
		Persona:              t.Persona,
		DefaultValues:        t.DefaultValues,
		AllowedOverrides:     t.AllowedOverrides,
		MaxBudgetUSD:         t.MaxBudgetUSD,
		AllowedModelFamilies: t.AllowedModelFamilies,
		RequiredCompliance:   t.RequiredCompliance,
		CreatedBy:            t.CreatedBy,
		CreatedAt:            t.CreatedAt,
		UpdatedAt:            t.UpdatedAt,
	}
}

// TemplateTeamAccessResponse projects repository.TemplateTeamAccess for API +
// audit consumption.
type TemplateTeamAccessResponse struct {
	ID         string    `json:"id"`
	OrgID      string    `json:"org_id"`
	TemplateID string    `json:"template_id"`
	TeamID     string    `json:"team_id"`
	GrantedBy  string    `json:"granted_by"`
	GrantedAt  time.Time `json:"granted_at"`
}

func templateTeamAccessResponse(a repository.TemplateTeamAccess) TemplateTeamAccessResponse {
	return TemplateTeamAccessResponse{
		ID:         a.ID,
		OrgID:      a.OrgID,
		TemplateID: a.TemplateID,
		TeamID:     a.TeamID,
		GrantedBy:  a.GrantedBy,
		GrantedAt:  a.GrantedAt,
	}
}

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

	// Admins see all templates; non-admins see only templates their teams
	// have been granted access to. Same nil-vs-empty semantics as TenantHandler.
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

	templates, err := h.svc.List(r.Context(), userCtx.OrgID, teamIDs)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list templates")
		return
	}
	data := make([]TemplateResponse, len(templates))
	for i, t := range templates {
		data[i] = templateResponse(t)
	}
	respond.List(w, data)
}

func (h *TemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "templateID")
	t, err := h.fetchTemplateForCaller(r, id)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "template not found")
		return
	}
	respond.JSON(w, http.StatusOK, templateResponse(t))
}

// fetchTemplateForCaller mirrors TenantHandler.fetchTenantForCaller:
// non-admins must have at least one team granted access to the template,
// admins always pass. "Not found" is returned in both the missing and
// not-visible cases to avoid existence probing.
func (h *TemplateHandler) fetchTemplateForCaller(r *http.Request, id string) (repository.Template, error) {
	userCtx := auth.GetUser(r.Context())
	t, err := h.svc.Get(r.Context(), id, userCtx.OrgID)
	if err != nil {
		return repository.Template{}, err
	}
	if isAdmin(userCtx.Role) {
		return t, nil
	}
	ok, err := h.accessSvc.UserHasTemplateAccess(r.Context(), userCtx.UserID, userCtx.OrgID, id)
	if err != nil {
		return repository.Template{}, err
	}
	if !ok {
		return repository.Template{}, errTemplateNotVisible
	}
	return t, nil
}

var errTemplateNotVisible = stderrs.New("template not visible to caller")

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
		After: templateResponse(t), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, templateResponse(t))
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
		Before: templateResponse(existing), After: templateResponse(updated), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, templateResponse(updated))
}

// ListAccess returns the team-access grants on a template.
func (h *TemplateHandler) ListAccess(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	id := chi.URLParam(r, "templateID")

	if _, err := h.svc.Get(r.Context(), id, userCtx.OrgID); err != nil {
		respond.Error(w, http.StatusNotFound, "template not found")
		return
	}
	access, err := h.accessSvc.ListTemplate(r.Context(), userCtx.OrgID, id)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list access")
		return
	}
	data := make([]TemplateTeamAccessResponse, len(access))
	for i, a := range access {
		data[i] = templateTeamAccessResponse(a)
	}
	respond.List(w, data)
}

type GrantTemplateAccessRequest struct {
	TeamID string `json:"team_id"`
}

func (h *TemplateHandler) GrantAccess(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	id := chi.URLParam(r, "templateID")

	if _, err := h.svc.Get(r.Context(), id, userCtx.OrgID); err != nil {
		respond.Error(w, http.StatusNotFound, "template not found")
		return
	}

	var req GrantTemplateAccessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.TeamID) == "" {
		respond.Error(w, http.StatusBadRequest, "team_id is required")
		return
	}

	grant, err := h.accessSvc.GrantTemplate(r.Context(), userCtx.OrgID, id, req.TeamID, userCtx.UserID)
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
		Action: "template.access_granted", EntityType: "template", EntityID: id,
		After: templateTeamAccessResponse(grant), IPAddress: ip, UserAgent: ua,
	})
	respond.JSON(w, http.StatusCreated, templateTeamAccessResponse(grant))
}

func (h *TemplateHandler) RevokeAccess(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	id := chi.URLParam(r, "templateID")
	teamID := chi.URLParam(r, "teamID")

	if _, err := h.svc.Get(r.Context(), id, userCtx.OrgID); err != nil {
		respond.Error(w, http.StatusNotFound, "template not found")
		return
	}

	if err := h.accessSvc.RevokeTemplate(r.Context(), userCtx.OrgID, id, teamID); err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to revoke access")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "template.access_revoked", EntityType: "template", EntityID: id,
		Before: map[string]string{"team_id": teamID}, IPAddress: ip, UserAgent: ua,
	})
	respond.NoContent(w)
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
		Before: templateResponse(existing), IPAddress: ip, UserAgent: ua,
	})

	respond.NoContent(w)
}
