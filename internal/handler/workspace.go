package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
	"github.com/nanohype/portal/internal/storage"
)

type WorkspaceHandler struct {
	svc      *service.WorkspaceService
	auditSvc *service.AuditService
	storage  *storage.S3Storage
	queries  *repository.Queries
}

func NewWorkspaceHandler(svc *service.WorkspaceService, auditSvc *service.AuditService, store *storage.S3Storage, queries *repository.Queries) *WorkspaceHandler {
	return &WorkspaceHandler{svc: svc, auditSvc: auditSvc, storage: store, queries: queries}
}

type CreateWorkspaceRequest struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	Source            string `json:"source"`
	RepoURL           string `json:"repo_url"`
	RepoBranch        string `json:"repo_branch"`
	WorkingDir        string `json:"working_dir"`
	TofuVersion       string `json:"tofu_version"`
	Environment       string `json:"environment"`
	AutoApply         bool   `json:"auto_apply"`
	RequiresApproval  bool   `json:"requires_approval"`
	VcsTriggerEnabled bool   `json:"vcs_trigger_enabled"`
}

type CloneWorkspaceRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Environment string `json:"environment"`
}

type UpdateWorkspaceRequest struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	RepoURL           string `json:"repo_url"`
	RepoBranch        string `json:"repo_branch"`
	WorkingDir        string `json:"working_dir"`
	TofuVersion       string `json:"tofu_version"`
	Environment       string `json:"environment"`
	AutoApply         *bool  `json:"auto_apply"`
	RequiresApproval  *bool  `json:"requires_approval"`
	VcsTriggerEnabled *bool  `json:"vcs_trigger_enabled"`
}

func (h *WorkspaceHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	search := r.URL.Query().Get("search")
	environment := r.URL.Query().Get("environment")

	workspaces, total, err := h.svc.List(r.Context(), userCtx.OrgID, page, perPage, search, environment)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list workspaces")
		return
	}

	respond.JSON(w, http.StatusOK, respond.ListResponse[any]{
		Data:    workspaces,
		Total:   total,
		Page:    page,
		PerPage: perPage,
	})
}

func (h *WorkspaceHandler) Get(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	workspace, err := h.svc.Get(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	respond.JSON(w, http.StatusOK, workspace)
}

func (h *WorkspaceHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	var req CreateWorkspaceRequest
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
	if len(req.Description) > 4096 {
		respond.Error(w, http.StatusBadRequest, "description must be at most 4096 characters")
		return
	}

	// Validate source
	source := req.Source
	if source == "" {
		source = "vcs"
	}
	if source != "vcs" && source != "upload" {
		respond.Error(w, http.StatusBadRequest, "source must be 'vcs' or 'upload'")
		return
	}

	// VCS workspaces require repo_url
	if source == "vcs" && req.RepoURL == "" {
		respond.Error(w, http.StatusBadRequest, "repo_url is required for VCS workspaces")
		return
	}
	if len(req.RepoURL) > 2048 {
		respond.Error(w, http.StatusBadRequest, "repo_url must be at most 2048 characters")
		return
	}

	// Upload workspaces cannot have VCS trigger
	if source == "upload" && req.VcsTriggerEnabled {
		respond.Error(w, http.StatusBadRequest, "vcs_trigger_enabled is not supported for upload workspaces")
		return
	}

	if req.Environment != "" && req.Environment != "development" && req.Environment != "staging" && req.Environment != "production" {
		respond.Error(w, http.StatusBadRequest, "environment must be development, staging, or production")
		return
	}

	workspace, err := h.svc.Create(r.Context(), service.CreateWorkspaceParams{
		OrgID:             userCtx.OrgID,
		Name:              req.Name,
		Description:       req.Description,
		Source:            source,
		RepoURL:           req.RepoURL,
		RepoBranch:        req.RepoBranch,
		WorkingDir:        req.WorkingDir,
		TofuVersion:       req.TofuVersion,
		Environment:       req.Environment,
		AutoApply:         req.AutoApply,
		RequiresApproval:  req.RequiresApproval,
		VcsTriggerEnabled: req.VcsTriggerEnabled,
		CreatedBy:         userCtx.UserID,
	})
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to create workspace")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "workspace.create", EntityType: "workspace", EntityID: workspace.ID,
		After: workspace, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, workspace)
}

func (h *WorkspaceHandler) Update(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	var req UpdateWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.Name) > 128 {
		respond.Error(w, http.StatusBadRequest, "name must be at most 128 characters")
		return
	}
	if len(req.RepoURL) > 2048 {
		respond.Error(w, http.StatusBadRequest, "repo_url must be at most 2048 characters")
		return
	}
	if len(req.Description) > 4096 {
		respond.Error(w, http.StatusBadRequest, "description must be at most 4096 characters")
		return
	}
	if req.Environment != "" && req.Environment != "development" && req.Environment != "staging" && req.Environment != "production" {
		respond.Error(w, http.StatusBadRequest, "environment must be development, staging, or production")
		return
	}

	workspace, err := h.svc.Update(r.Context(), service.UpdateWorkspaceParams{
		ID:                workspaceID,
		OrgID:             userCtx.OrgID,
		Name:              req.Name,
		Description:       req.Description,
		RepoURL:           req.RepoURL,
		RepoBranch:        req.RepoBranch,
		WorkingDir:        req.WorkingDir,
		TofuVersion:       req.TofuVersion,
		Environment:       req.Environment,
		AutoApply:         req.AutoApply,
		RequiresApproval:  req.RequiresApproval,
		VcsTriggerEnabled: req.VcsTriggerEnabled,
	})

	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to update workspace")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "workspace.update", EntityType: "workspace", EntityID: workspaceID,
		After: workspace, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, workspace)
}

func (h *WorkspaceHandler) Lock(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	workspace, err := h.svc.Lock(r.Context(), workspaceID, userCtx.OrgID, userCtx.UserID)
	if err != nil {
		respond.Error(w, http.StatusConflict, "workspace is already locked or not found")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "workspace.lock", EntityType: "workspace", EntityID: workspaceID,
		After: workspace, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, workspace)
}

func (h *WorkspaceHandler) Unlock(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	workspace, err := h.svc.Unlock(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "workspace.unlock", EntityType: "workspace", EntityID: workspaceID,
		After: workspace, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, workspace)
}

func (h *WorkspaceHandler) Upload(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	ws, err := h.svc.Get(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	if ws.Source != "upload" {
		respond.Error(w, http.StatusBadRequest, "workspace is not an upload workspace")
		return
	}

	if h.storage == nil {
		respond.Error(w, http.StatusServiceUnavailable, "storage not configured")
		return
	}

	// Parse multipart form (50 MB max)
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		respond.Error(w, http.StatusBadRequest, "failed to parse upload: file may exceed size limit")
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to read uploaded file")
		return
	}

	if len(data) == 0 {
		respond.Error(w, http.StatusBadRequest, "uploaded file is empty")
		return
	}

	configVersionID := ulid.Make().String()
	if _, err := h.storage.PutConfigArchive(r.Context(), workspaceID, configVersionID, data); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to store configuration")
		return
	}

	updated, err := h.queries.SetWorkspaceConfigVersion(r.Context(), repository.SetWorkspaceConfigVersionParams{
		ID:                     workspaceID,
		OrgID:                  userCtx.OrgID,
		CurrentConfigVersionID: configVersionID,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update workspace")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "workspace.upload", EntityType: "workspace", EntityID: workspaceID,
		After: map[string]string{
			"config_version_id": configVersionID,
			"size":              fmt.Sprintf("%d", len(data)),
		},
		IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, updated)
}

func (h *WorkspaceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	if err := h.svc.Delete(r.Context(), workspaceID, userCtx.OrgID); err != nil {
		if errors.Is(err, service.ErrWorkspaceHasRuns) {
			respond.Error(w, http.StatusConflict, "cannot delete workspace with existing runs")
			return
		}
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to delete workspace")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "workspace.delete", EntityType: "workspace", EntityID: workspaceID,
		IPAddress: ip, UserAgent: ua,
	})

	respond.NoContent(w)
}

func (h *WorkspaceHandler) Clone(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	sourceID := chi.URLParam(r, "workspaceID")

	source, err := h.svc.Get(r.Context(), sourceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	var req CloneWorkspaceRequest
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
	if len(req.Description) > 4096 {
		respond.Error(w, http.StatusBadRequest, "description must be at most 4096 characters")
		return
	}
	if req.Environment != "" && req.Environment != "development" && req.Environment != "staging" && req.Environment != "production" {
		respond.Error(w, http.StatusBadRequest, "environment must be development, staging, or production")
		return
	}

	// Fall back to source values for optional fields
	description := req.Description
	if description == "" {
		description = source.Description
	}
	environment := req.Environment
	if environment == "" {
		environment = source.Environment
	}

	workspace, err := h.svc.Create(r.Context(), service.CreateWorkspaceParams{
		OrgID:             userCtx.OrgID,
		Name:              req.Name,
		Description:       description,
		Source:            source.Source,
		RepoURL:           source.RepoURL,
		RepoBranch:        source.RepoBranch,
		WorkingDir:        source.WorkingDir,
		TofuVersion:       source.TofuVersion,
		Environment:       environment,
		AutoApply:         source.AutoApply,
		RequiresApproval:  source.RequiresApproval,
		VcsTriggerEnabled: source.VcsTriggerEnabled,
		CreatedBy:         userCtx.UserID,
	})
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to create cloned workspace")
		return
	}

	// Copy the source workspace's variables into the new one, transactionally —
	// a mid-copy failure must not leave the clone with only some of them.
	if _, err := h.svc.CopyAll(r.Context(), sourceID, workspace.ID, userCtx.OrgID); err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to copy variables")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "workspace.clone", EntityType: "workspace", EntityID: workspace.ID,
		Before: map[string]string{"source_workspace_id": sourceID},
		After:  workspace, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, workspace)
}
