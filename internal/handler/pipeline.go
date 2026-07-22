package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

type PipelineHandler struct {
	pipelineSvc *service.PipelineService
	auditSvc    *service.AuditService
}

func NewPipelineHandler(pipelineSvc *service.PipelineService, auditSvc *service.AuditService) *PipelineHandler {
	return &PipelineHandler{pipelineSvc: pipelineSvc, auditSvc: auditSvc}
}

// PipelineResponse projects repository.Pipeline for API + audit consumption.
type PipelineResponse struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func pipelineResponse(p repository.Pipeline) PipelineResponse {
	return PipelineResponse{
		ID:          p.ID,
		OrgID:       p.OrgID,
		Name:        p.Name,
		Description: p.Description,
		CreatedBy:   p.CreatedBy,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
}

// PipelineStageResponse projects repository.PipelineStageWithWorkspace.
type PipelineStageResponse struct {
	ID            string    `json:"id"`
	PipelineID    string    `json:"pipeline_id"`
	WorkspaceID   string    `json:"workspace_id"`
	StageOrder    int32     `json:"stage_order"`
	AutoApply     bool      `json:"auto_apply"`
	OnFailure     string    `json:"on_failure"`
	CreatedAt     time.Time `json:"created_at"`
	WorkspaceName string    `json:"workspace_name"`
}

func pipelineStageResponse(st repository.PipelineStageWithWorkspace) PipelineStageResponse {
	return PipelineStageResponse{
		ID:            st.ID,
		PipelineID:    st.PipelineID,
		WorkspaceID:   st.WorkspaceID,
		StageOrder:    st.StageOrder,
		AutoApply:     st.AutoApply,
		OnFailure:     st.OnFailure,
		CreatedAt:     st.CreatedAt,
		WorkspaceName: st.WorkspaceName,
	}
}

// PipelineRunResponse projects repository.PipelineRun for API + audit
// consumption.
type PipelineRunResponse struct {
	ID           string     `json:"id"`
	PipelineID   string     `json:"pipeline_id"`
	OrgID        string     `json:"org_id"`
	Status       string     `json:"status"`
	CurrentStage int32      `json:"current_stage"`
	TotalStages  int32      `json:"total_stages"`
	CreatedBy    string     `json:"created_by"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func pipelineRunResponse(pr repository.PipelineRun) PipelineRunResponse {
	return PipelineRunResponse{
		ID:           pr.ID,
		PipelineID:   pr.PipelineID,
		OrgID:        pr.OrgID,
		Status:       pr.Status,
		CurrentStage: pr.CurrentStage,
		TotalStages:  pr.TotalStages,
		CreatedBy:    pr.CreatedBy,
		StartedAt:    pr.StartedAt,
		FinishedAt:   pr.FinishedAt,
		CreatedAt:    pr.CreatedAt,
		UpdatedAt:    pr.UpdatedAt,
	}
}

// PipelineRunStageResponse projects repository.PipelineRunStageWithWorkspace.
type PipelineRunStageResponse struct {
	ID            string     `json:"id"`
	PipelineRunID string     `json:"pipeline_run_id"`
	StageID       string     `json:"stage_id"`
	WorkspaceID   string     `json:"workspace_id"`
	RunID         *string    `json:"run_id,omitempty"`
	StageOrder    int32      `json:"stage_order"`
	Status        string     `json:"status"`
	AutoApply     bool       `json:"auto_apply"`
	OnFailure     string     `json:"on_failure"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	WorkspaceName string     `json:"workspace_name"`
}

func pipelineRunStageResponse(st repository.PipelineRunStageWithWorkspace) PipelineRunStageResponse {
	return PipelineRunStageResponse{
		ID:            st.ID,
		PipelineRunID: st.PipelineRunID,
		StageID:       st.StageID,
		WorkspaceID:   st.WorkspaceID,
		RunID:         st.RunID,
		StageOrder:    st.StageOrder,
		Status:        st.Status,
		AutoApply:     st.AutoApply,
		OnFailure:     st.OnFailure,
		StartedAt:     st.StartedAt,
		FinishedAt:    st.FinishedAt,
		CreatedAt:     st.CreatedAt,
		UpdatedAt:     st.UpdatedAt,
		WorkspaceName: st.WorkspaceName,
	}
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

	data := make([]PipelineResponse, len(pipelines))
	for i, p := range pipelines {
		data[i] = pipelineResponse(p)
	}
	respond.List(w, data)
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

	// A new pipeline has no stored stages, so every auto-apply stage on it is
	// new authority.
	if !h.authorizeStages(w, r, userCtx.OrgID, userCtx.Role, nil, req.Stages) {
		return
	}

	pipeline, err := h.pipelineSvc.Create(r.Context(), userCtx.OrgID, req.Name, req.Description, userCtx.UserID, req.Stages)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "pipeline.create", EntityType: "pipeline", EntityID: pipeline.ID,
		After: pipelineResponse(pipeline), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, pipelineResponse(pipeline))
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

	stageData := make([]PipelineStageResponse, len(stages))
	for i, st := range stages {
		stageData[i] = pipelineStageResponse(st)
	}
	respond.JSON(w, http.StatusOK, PipelineDetailResponse{
		Pipeline: pipelineResponse(pipeline),
		Stages:   stageData,
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

	// Update replaces the whole stage list, so an edit that only renames the
	// pipeline or reorders it still resubmits every stage's auto_apply. Only a
	// stage that gains auto-apply is an authorization event — the same rule
	// changesApprovalGate follows on the workspace form. Without the
	// comparison, one admin-set auto-apply stage would freeze the pipeline
	// against every later operator edit.
	if req.Stages != nil {
		if _, err := h.pipelineSvc.Get(r.Context(), pipelineID, userCtx.OrgID); err != nil {
			respond.FromError(w, r, err)
			return
		}
		currentStages, err := h.pipelineSvc.ListStages(r.Context(), pipelineID)
		if err != nil {
			respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list stages")
			return
		}
		if !h.authorizeStages(w, r, userCtx.OrgID, userCtx.Role, currentStages, req.Stages) {
			return
		}
	}

	pipeline, err := h.pipelineSvc.Update(r.Context(), pipelineID, userCtx.OrgID, req.Name, req.Description, req.Stages)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "pipeline.update", EntityType: "pipeline", EntityID: pipelineID,
		After: pipelineResponse(pipeline), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, pipelineResponse(pipeline))
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
		After: pipelineRunResponse(pipelineRun), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, pipelineRunResponse(pipelineRun))
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

	data := make([]PipelineRunResponse, len(runs))
	for i, pr := range runs {
		data[i] = pipelineRunResponse(pr)
	}

	respond.JSON(w, http.StatusOK, respond.ListResponse[PipelineRunResponse]{
		Data:    data,
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

	stageData := make([]PipelineRunStageResponse, len(stages))
	for i, st := range stages {
		stageData[i] = pipelineRunStageResponse(st)
	}
	respond.JSON(w, http.StatusOK, PipelineRunDetailResponse{
		PipelineRun: pipelineRunResponse(pipelineRun),
		Stages:      stageData,
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
		After: pipelineRunResponse(pipelineRun), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, pipelineRunResponse(pipelineRun))
}

// PipelineDetailResponse pairs a pipeline with its ordered stages.
type PipelineDetailResponse struct {
	Pipeline PipelineResponse        `json:"pipeline"`
	Stages   []PipelineStageResponse `json:"stages"`
}

// PipelineRunDetailResponse pairs a pipeline run with its per-stage progress.
type PipelineRunDetailResponse struct {
	PipelineRun PipelineRunResponse        `json:"pipeline_run"`
	Stages      []PipelineRunStageResponse `json:"stages"`
}

// authorizeStages resolves every submitted stage's workspace inside the caller's
// org and holds the stages that gain auto-apply to the bar those workspaces
// carry. It writes the response and returns false when the write may not
// proceed.
func (h *PipelineHandler) authorizeStages(
	w http.ResponseWriter, r *http.Request,
	orgID, role string,
	current []repository.PipelineStageWithWorkspace,
	submitted []service.CreatePipelineStageInput,
) bool {
	seen := make(map[string]bool, len(submitted))
	ids := make([]string, 0, len(submitted))
	for _, s := range submitted {
		if !seen[s.WorkspaceID] {
			seen[s.WorkspaceID] = true
			ids = append(ids, s.WorkspaceID)
		}
	}

	rows, err := h.pipelineSvc.StageWorkspaceGates(r.Context(), orgID, ids)
	if err != nil {
		slog.Error("failed to resolve stage workspaces", "error", err)
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to resolve stage workspaces")
		return false
	}
	gates := make(map[string]repository.WorkspaceGateRow, len(rows))
	for _, row := range rows {
		gates[row.ID] = row
	}

	// A stage naming a workspace this org does not have is refused rather than
	// stored: the auto-apply bar below is decided by what the target workspace
	// is, so an unresolvable target must not fall through it.
	for _, id := range ids {
		if _, ok := gates[id]; !ok {
			respond.Error(w, http.StatusBadRequest, "stage workspace not found: "+id)
			return false
		}
	}

	gaining := workspacesGainingAutoApply(current, submitted)
	if len(gaining) == 0 {
		return true
	}
	action, gatedNames := autoApplyStageBar(gates, gaining)
	if auth.CanPerform(role, action) {
		return true
	}
	if len(gatedNames) > 0 {
		respond.Error(w, http.StatusForbidden,
			"auto_apply on a stage targeting a workspace that requires approval ("+
				strings.Join(gatedNames, ", ")+") requires admin role or higher")
		return false
	}
	respond.Error(w, http.StatusForbidden, "setting auto_apply on a stage requires operator role or higher")
	return false
}

// workspacesGainingAutoApply returns the workspaces a submitted stage list hands
// auto-apply that they do not already hold on this pipeline, in submission
// order.
//
// Stages carry no stable id across an update — the write deletes and recreates
// the whole list — so identity is the workspace a stage targets, counted rather
// than set-tested because a pipeline may run the same workspace twice. Any
// workspace that would end up with more auto-applying stages than it has today
// is new authority; reordering, renaming, changing on_failure, dropping a stage
// or adding a non-auto one is not.
func workspacesGainingAutoApply(current []repository.PipelineStageWithWorkspace, submitted []service.CreatePipelineStageInput) []string {
	have := make(map[string]int, len(current))
	for _, s := range current {
		if s.AutoApply {
			have[s.WorkspaceID]++
		}
	}
	want := make(map[string]int, len(submitted))
	var gaining []string
	for _, s := range submitted {
		if !s.AutoApply {
			continue
		}
		want[s.WorkspaceID]++
		if want[s.WorkspaceID] == have[s.WorkspaceID]+1 {
			gaining = append(gaining, s.WorkspaceID)
		}
	}
	return gaining
}

// autoApplyStageBar returns the action a caller must clear to hand these
// workspaces auto-apply on a pipeline stage, plus the gated ones by name.
//
// A stage's auto_apply overrides the workspace's own setting for that run, so
// writing it is the same act as flipping auto_apply on the workspace — and worth
// exactly what that workspace's applies are worth.
//
// On an UNGATED workspace that is ActionApplyRun: the same caller may POST
// {"operation":"apply"} on that workspace right now (handler/run.go, wsRun), so
// a stage that applies it automatically hands out nothing they do not already
// hold. Pinning it at admin would have made pipelines — the whole point of
// which is running several applies in order — an admin-only feature for the
// operators who build them, while leaving the manual equivalent open.
//
// On a GATED workspace it is ActionApplyProd. The override cannot actually open
// that gate (postPlanAction checks requires_approval first, and the stage parks
// at awaiting_approval regardless), so this bar guards a flag that is inert
// today — deliberately, because it is the flag that would matter if that
// ordering ever moved, and nobody with an operator's job needs to set it.
// A workspace the caller's org does not have is refused before this runs; if one
// reaches here anyway it counts as gated, so an unresolvable target can never be
// the reason a stage got the lower bar.
func autoApplyStageBar(gates map[string]repository.WorkspaceGateRow, gaining []string) (auth.Action, []string) {
	var gatedNames []string
	for _, id := range gaining {
		g, ok := gates[id]
		if !ok {
			gatedNames = append(gatedNames, id)
			continue
		}
		if g.RequiresApproval {
			gatedNames = append(gatedNames, g.Name)
		}
	}
	if len(gatedNames) > 0 {
		return auth.ActionApplyProd, gatedNames
	}
	return auth.ActionApplyRun, nil
}
