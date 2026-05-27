package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
	"github.com/nanohype/portal/internal/service"
)

type OrgVariableHandler struct {
	queries   *repository.Queries
	encryptor *secrets.Encryptor
	auditSvc  *service.AuditService
}

func NewOrgVariableHandler(queries *repository.Queries, encryptor *secrets.Encryptor, auditSvc *service.AuditService) *OrgVariableHandler {
	return &OrgVariableHandler{queries: queries, encryptor: encryptor, auditSvc: auditSvc}
}

func (h *OrgVariableHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	vars, err := h.queries.ListOrgVariables(r.Context(), userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list org variables")
		return
	}

	for i := range vars {
		if vars[i].Sensitive {
			vars[i].Value = "***"
		}
	}

	respond.JSON(w, http.StatusOK, vars)
}

func (h *OrgVariableHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	var req CreateVariableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Key == "" {
		respond.Error(w, http.StatusBadRequest, "key is required")
		return
	}
	if len(req.Key) > 256 {
		respond.Error(w, http.StatusBadRequest, "key must be at most 256 characters")
		return
	}
	if len(req.Value) > 65536 {
		respond.Error(w, http.StatusBadRequest, "value must be at most 64KB")
		return
	}
	if req.Category == "" {
		req.Category = "terraform"
	}
	if req.Category != "terraform" && req.Category != "env" {
		respond.Error(w, http.StatusBadRequest, "category must be 'terraform' or 'env'")
		return
	}

	value := req.Value
	if req.Sensitive && h.encryptor != nil {
		encrypted, err := h.encryptor.Encrypt(req.Value)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "failed to encrypt value")
			return
		}
		value = encrypted
	}

	v, err := h.queries.CreateOrgVariable(r.Context(), repository.CreateOrgVariableParams{
		ID:          ulid.Make().String(),
		OrgID:       userCtx.OrgID,
		Key:         req.Key,
		Value:       value,
		Sensitive:   req.Sensitive,
		Category:    req.Category,
		Description: req.Description,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to create org variable")
		return
	}

	ip, ua := auditContext(r)
	auditVar := v
	auditVar.Value = "***"
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "org_variable.create", EntityType: "org_variable", EntityID: v.ID,
		After: auditVar, IPAddress: ip, UserAgent: ua,
	})

	if v.Sensitive {
		v.Value = "***"
	}

	respond.JSON(w, http.StatusCreated, v)
}

func (h *OrgVariableHandler) Update(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	varID := chi.URLParam(r, "variableID")

	before, err := h.queries.GetOrgVariable(r.Context(), repository.GetOrgVariableParams{
		ID: varID, OrgID: userCtx.OrgID,
	})
	if err != nil {
		respond.Error(w, http.StatusNotFound, "variable not found")
		return
	}

	var req CreateVariableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.Key) > 256 {
		respond.Error(w, http.StatusBadRequest, "key must be at most 256 characters")
		return
	}
	if len(req.Value) > 65536 {
		respond.Error(w, http.StatusBadRequest, "value must be at most 64KB")
		return
	}
	if req.Category != "" && req.Category != "terraform" && req.Category != "env" {
		respond.Error(w, http.StatusBadRequest, "category must be 'terraform' or 'env'")
		return
	}

	value := req.Value
	if req.Sensitive && h.encryptor != nil {
		encrypted, err := h.encryptor.Encrypt(req.Value)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "failed to encrypt value")
			return
		}
		value = encrypted
	}

	v, err := h.queries.UpdateOrgVariable(r.Context(), repository.UpdateOrgVariableParams{
		ID: varID, OrgID: userCtx.OrgID, Value: value, Sensitive: req.Sensitive, Description: req.Description, Category: req.Category,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update org variable")
		return
	}

	ip, ua := auditContext(r)
	auditBefore := before
	auditBefore.Value = "***"
	auditAfter := v
	auditAfter.Value = "***"
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "org_variable.update", EntityType: "org_variable", EntityID: varID,
		Before: auditBefore, After: auditAfter, IPAddress: ip, UserAgent: ua,
	})

	if v.Sensitive {
		v.Value = "***"
	}

	respond.JSON(w, http.StatusOK, v)
}

func (h *OrgVariableHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	varID := chi.URLParam(r, "variableID")

	if err := h.queries.DeleteOrgVariable(r.Context(), repository.DeleteOrgVariableParams{
		ID: varID, OrgID: userCtx.OrgID,
	}); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to delete org variable")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "org_variable.delete", EntityType: "org_variable", EntityID: varID,
		IPAddress: ip, UserAgent: ua,
	})

	respond.NoContent(w)
}

func (h *OrgVariableHandler) RevealValue(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	varID := chi.URLParam(r, "variableID")

	v, err := h.queries.GetOrgVariable(r.Context(), repository.GetOrgVariableParams{
		ID: varID, OrgID: userCtx.OrgID,
	})
	if err != nil {
		respond.Error(w, http.StatusNotFound, "variable not found")
		return
	}

	value := v.Value
	if v.Sensitive && h.encryptor != nil {
		decrypted, err := h.encryptor.Decrypt(v.Value)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "failed to decrypt variable")
			return
		}
		value = decrypted
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "org_variable.reveal", EntityType: "org_variable", EntityID: varID,
		IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, map[string]string{"value": value})
}
