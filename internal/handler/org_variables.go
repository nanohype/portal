package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

type OrgVariableHandler struct {
	variableSvc *service.VariableService
}

func NewOrgVariableHandler(variableSvc *service.VariableService) *OrgVariableHandler {
	return &OrgVariableHandler{variableSvc: variableSvc}
}

// OrgVariableResponse projects repository.OrgVariable for the wire. Redaction is
// not done here — the service returns rows already redacted, so a response type
// cannot leak a value the service decided to withhold.
type OrgVariableResponse struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Sensitive   bool      `json:"sensitive"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func orgVariableResponse(v repository.OrgVariable) OrgVariableResponse {
	return OrgVariableResponse{
		ID:          v.ID,
		OrgID:       v.OrgID,
		Key:         v.Key,
		Value:       v.Value,
		Sensitive:   v.Sensitive,
		Category:    v.Category,
		Description: v.Description,
		CreatedAt:   v.CreatedAt,
		UpdatedAt:   v.UpdatedAt,
	}
}

// variableInputFrom maps the wire request onto the service's input. Validation
// lives on the input, not here.
func variableInputFrom(req CreateVariableRequest) service.VariableInput {
	return service.VariableInput{
		Key:         req.Key,
		Value:       req.Value,
		Sensitive:   req.Sensitive,
		Category:    req.Category,
		Description: req.Description,
	}
}

func actorFrom(r *http.Request) service.ActorMeta {
	userCtx := auth.GetUser(r.Context())
	ip, ua := auditContext(r)
	return service.ActorMeta{UserID: userCtx.UserID, IPAddress: ip, UserAgent: ua}
}

func (h *OrgVariableHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	vars, err := h.variableSvc.ListOrgVariables(r.Context(), userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	data := make([]OrgVariableResponse, len(vars))
	for i, v := range vars {
		data[i] = orgVariableResponse(v)
	}
	respond.List(w, data)
}

func (h *OrgVariableHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	var req CreateVariableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	v, err := h.variableSvc.CreateOrgVariable(r.Context(), userCtx.OrgID, variableInputFrom(req), actorFrom(r))
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusCreated, orgVariableResponse(v))
}

func (h *OrgVariableHandler) Update(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	varID := chi.URLParam(r, "variableID")

	var req CreateVariableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	v, err := h.variableSvc.UpdateOrgVariable(r.Context(), userCtx.OrgID, varID, variableInputFrom(req), actorFrom(r))
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusOK, orgVariableResponse(v))
}

func (h *OrgVariableHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	varID := chi.URLParam(r, "variableID")

	if err := h.variableSvc.DeleteOrgVariable(r.Context(), userCtx.OrgID, varID, actorFrom(r)); err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.NoContent(w)
}

func (h *OrgVariableHandler) RevealValue(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	varID := chi.URLParam(r, "variableID")

	value, err := h.variableSvc.RevealOrgVariable(r.Context(), userCtx.OrgID, varID, actorFrom(r))
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]string{"value": value})
}
