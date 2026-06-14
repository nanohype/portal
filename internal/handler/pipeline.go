package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/service"
)

type PipelineHandler struct {
	pipelineSvc *service.PipelineService
	auditSvc    *service.AuditService
}

func NewPipelineHandler(pipelineSvc *service.PipelineService, auditSvc *service.AuditService) *PipelineHandler {
	return &PipelineHandler{pipelineSvc: pipelineSvc, auditSvc: auditSvc}
}

type CreatePipelineRequest struct {
	Name        string                             `json:"name"`
	Description string                             `json:"description"`
	Stages      []service.CreatePipelineStageInput `json:"stages"`
}

type UpdatePipelineRequest struct {
	Name        string                             `json:"name"`
	Description string                             `json:"description"`
	Stages      []service.CreatePipelineStageInput `json:"stages"`
}

func (h *PipelineHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	pipelines, err := h.pipelineSvc.List(r.Context(), userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list pipelines")
		return
	}

	respond.List(w, pipelines)
}

func (h *PipelineHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	var req CreatePipelineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		respond.Error(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Name) > 128 {
		respond.Error(w, http.StatusBadRequest, "name must be at most 128 characters")
		return
	}
	if len(req.Stages) == 0 {
		respond.Error(w, http.StatusBadRequest, "at least one stage is required")
		return
	}
	if len(req.Stages) > 20 {
		respond.Error(w, http.StatusBadRequest, "maximum 20 stages per pipeline")
		return
	}

	for _, s := range req.Stages {
		if s.WorkspaceID == "" {
			respond.Error(w, http.StatusBadRequest, "each stage must have a workspace_id")
			return
		}
		if s.OnFailure != "" && s.OnFailure != "stop" && s.OnFailure != "continue" {
			respond.Error(w, http.StatusBadRequest, "on_failure must be 'stop' or 'continue'")
			return
		}
	}

	pipeline, err := h.pipelineSvc.Create(r.Context(), userCtx.OrgID, req.Name, req.Description, userCtx.UserID, req.Stages)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to create pipeline")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "pipeline.create", EntityType: "pipeline", EntityID: pipeline.ID,
		After: pipeline, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, pipeline)
}

func (h *PipelineHandler) Get(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	pipelineID := chi.URLParam(r, "pipelineID")

	pipeline, err := h.pipelineSvc.Get(r.Context(), pipelineID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	stages, err := h.pipelineSvc.ListStages(r.Context(), pipelineID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list stages")
		return
	}

	respond.JSON(w, http.StatusOK, map[string]any{
		"pipeline": pipeline,
		"stages":   stages,
	})
}

func (h *PipelineHandler) Update(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	pipelineID := chi.URLParam(r, "pipelineID")

	var req UpdatePipelineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.Name) > 128 {
		respond.Error(w, http.StatusBadRequest, "name must be at most 128 characters")
		return
	}
	if req.Stages != nil && len(req.Stages) > 20 {
		respond.Error(w, http.StatusBadRequest, "maximum 20 stages per pipeline")
		return
	}

	for _, s := range req.Stages {
		if s.WorkspaceID == "" {
			respond.Error(w, http.StatusBadRequest, "each stage must have a workspace_id")
			return
		}
		if s.OnFailure != "" && s.OnFailure != "stop" && s.OnFailure != "continue" {
			respond.Error(w, http.StatusBadRequest, "on_failure must be 'stop' or 'continue'")
			return
		}
	}

	pipeline, err := h.pipelineSvc.Update(r.Context(), pipelineID, userCtx.OrgID, req.Name, req.Description, req.Stages)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update pipeline")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "pipeline.update", EntityType: "pipeline", EntityID: pipelineID,
		After: pipeline, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, pipeline)
}

func (h *PipelineHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	pipelineID := chi.URLParam(r, "pipelineID")

	if err := h.pipelineSvc.Delete(r.Context(), pipelineID, userCtx.OrgID); err != nil {
		if err.Error() == "pipeline has active runs" {
			respond.Error(w, http.StatusConflict, err.Error())
			return
		}
		respond.Error(w, http.StatusInternalServerError, "failed to delete pipeline")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "pipeline.delete", EntityType: "pipeline", EntityID: pipelineID,
		IPAddress: ip, UserAgent: ua,
	})

	respond.NoContent(w)
}

func (h *PipelineHandler) StartRun(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	pipelineID := chi.URLParam(r, "pipelineID")

	// Verify pipeline exists and belongs to org
	if _, err := h.pipelineSvc.Get(r.Context(), pipelineID, userCtx.OrgID); err != nil {
		respond.FromError(w, r, err)
		return
	}

	pipelineRun, err := h.pipelineSvc.StartRun(r.Context(), pipelineID, userCtx.OrgID, userCtx.UserID)
	if err != nil {
		if err.Error() == "pipeline already has an active run" {
			respond.Error(w, http.StatusConflict, err.Error())
			return
		}
		respond.Error(w, http.StatusInternalServerError, "failed to start pipeline run")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "pipeline_run.create", EntityType: "pipeline_run", EntityID: pipelineRun.ID,
		After: pipelineRun, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, pipelineRun)
}

func (h *PipelineHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	pipelineID := chi.URLParam(r, "pipelineID")

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	runs, total, err := h.pipelineSvc.ListRuns(r.Context(), pipelineID, userCtx.OrgID, page, perPage)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list pipeline runs")
		return
	}

	respond.JSON(w, http.StatusOK, respond.ListResponse[any]{
		Data:    toAnySlice(runs),
		Total:   total,
		Page:    page,
		PerPage: perPage,
	})
}

func (h *PipelineHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	pipelineRunID := chi.URLParam(r, "runId")

	pipelineRun, err := h.pipelineSvc.GetRun(r.Context(), pipelineRunID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "pipeline run not found")
		return
	}

	stages, err := h.pipelineSvc.ListRunStages(r.Context(), pipelineRunID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list run stages")
		return
	}

	respond.JSON(w, http.StatusOK, map[string]any{
		"pipeline_run": pipelineRun,
		"stages":       stages,
	})
}

func (h *PipelineHandler) CancelRun(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	pipelineRunID := chi.URLParam(r, "runId")

	pipelineRun, err := h.pipelineSvc.CancelRun(r.Context(), pipelineRunID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusConflict, err.Error())
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "pipeline_run.cancel", EntityType: "pipeline_run", EntityID: pipelineRunID,
		After: pipelineRun, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, pipelineRun)
}

func toAnySlice[T any](items []T) []any {
	result := make([]any, len(items))
	for i, item := range items {
		result[i] = item
	}
	return result
}
