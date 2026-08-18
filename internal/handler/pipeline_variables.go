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

type PipelineVariableHandler struct {
	variableSvc *service.VariableService
}

func NewPipelineVariableHandler(variableSvc *service.VariableService) *PipelineVariableHandler {
	return &PipelineVariableHandler{variableSvc: variableSvc}
}

// PipelineVariableResponse projects repository.PipelineVariable for the wire.
// Redaction happens in the service, so this cannot leak a withheld value.
type PipelineVariableResponse struct {
	ID          string    `json:"id"`
	PipelineID  string    `json:"pipeline_id"`
	OrgID       string    `json:"org_id"`
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Sensitive   bool      `json:"sensitive"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func pipelineVariableResponse(v repository.PipelineVariable) PipelineVariableResponse {
	return PipelineVariableResponse{
		ID:          v.ID,
		PipelineID:  v.PipelineID,
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

func (h *PipelineVariableHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	pipelineID := chi.URLParam(r, "pipelineID")

	vars, err := h.variableSvc.ListPipelineVariables(r.Context(), userCtx.OrgID, pipelineID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	data := make([]PipelineVariableResponse, len(vars))
	for i, v := range vars {
		data[i] = pipelineVariableResponse(v)
	}
	respond.List(w, data)
}

func (h *PipelineVariableHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	pipelineID := chi.URLParam(r, "pipelineID")

	var req CreateVariableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	v, err := h.variableSvc.CreatePipelineVariable(r.Context(), userCtx.OrgID, pipelineID, variableInputFrom(req), actorFrom(r))
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusCreated, pipelineVariableResponse(v))
}

func (h *PipelineVariableHandler) Update(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	pipelineID := chi.URLParam(r, "pipelineID")
	varID := chi.URLParam(r, "variableID")

	var req CreateVariableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	v, err := h.variableSvc.UpdatePipelineVariable(r.Context(), userCtx.OrgID, pipelineID, varID, variableInputFrom(req), actorFrom(r))
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusOK, pipelineVariableResponse(v))
}

func (h *PipelineVariableHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	pipelineID := chi.URLParam(r, "pipelineID")
	varID := chi.URLParam(r, "variableID")

	if err := h.variableSvc.DeletePipelineVariable(r.Context(), userCtx.OrgID, pipelineID, varID, actorFrom(r)); err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.NoContent(w)
}

func (h *PipelineVariableHandler) RevealValue(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	pipelineID := chi.URLParam(r, "pipelineID")
	varID := chi.URLParam(r, "variableID")

	value, err := h.variableSvc.RevealPipelineVariable(r.Context(), userCtx.OrgID, pipelineID, varID, actorFrom(r))
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]string{"value": value})
}
