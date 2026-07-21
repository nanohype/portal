package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
	"github.com/nanohype/portal/internal/storage"
)

// gitRefPattern allows the characters in a normal git branch/tag name and a
// relative working-directory path: letters, digits, and . _ / - . Everything
// else (whitespace, shell metacharacters, $() ; | & quotes) is rejected. The
// repo_branch and working_dir reach the executors' clone/cd commands, so this
// boundary check is the first line of defense against command/option injection
// (the executors add quoting + `--` as the second).
var gitRefPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// validateRepoBranch rejects branch names that could be read as shell or
// git-option payloads. Empty is allowed — the service fills the default "main".
func validateRepoBranch(branch string) error {
	if branch == "" {
		return nil
	}
	if len(branch) > 255 {
		return fmt.Errorf("repo_branch must be at most 255 characters")
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("repo_branch must not start with '-'")
	}
	if strings.Contains(branch, "..") {
		return fmt.Errorf("repo_branch must not contain '..'")
	}
	if !gitRefPattern.MatchString(branch) {
		return fmt.Errorf("repo_branch may only contain letters, digits, and . _ / -")
	}
	return nil
}

// validateWorkingDir rejects working dirs that escape the checkout or carry
// shell/option payloads. Empty is allowed — the service fills ".".
func validateWorkingDir(dir string) error {
	if dir == "" {
		return nil
	}
	if len(dir) > 1024 {
		return fmt.Errorf("working_dir must be at most 1024 characters")
	}
	if strings.HasPrefix(dir, "-") {
		return fmt.Errorf("working_dir must not start with '-'")
	}
	if strings.HasPrefix(dir, "/") {
		return fmt.Errorf("working_dir must be a relative path")
	}
	if strings.Contains(dir, "..") {
		return fmt.Errorf("working_dir must not contain '..'")
	}
	if !gitRefPattern.MatchString(dir) {
		return fmt.Errorf("working_dir may only contain letters, digits, and . _ / -")
	}
	return nil
}

// validateRepoURL keeps the clone target from being read as a git option and
// caps its length. The executors also pass it after a `--` separator; this is
// defense in depth and a clearer error at the boundary. Empty is allowed for
// upload workspaces.
func validateRepoURL(url string) error {
	if url == "" {
		return nil
	}
	if len(url) > 2048 {
		return fmt.Errorf("repo_url must be at most 2048 characters")
	}
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("repo_url must not start with '-'")
	}
	return nil
}

// validateRepoFields runs the three repo-field validators and returns the first
// error, so Create and Update share one call site.
func validateRepoFields(repoURL, branch, workingDir string) error {
	if err := validateRepoURL(repoURL); err != nil {
		return err
	}
	if err := validateRepoBranch(branch); err != nil {
		return err
	}
	return validateWorkingDir(workingDir)
}

type WorkspaceHandler struct {
	svc      *service.WorkspaceService
	auditSvc *service.AuditService
	storage  *storage.S3Storage
	queries  *repository.Queries
}

func NewWorkspaceHandler(svc *service.WorkspaceService, auditSvc *service.AuditService, store *storage.S3Storage, queries *repository.Queries) *WorkspaceHandler {
	return &WorkspaceHandler{svc: svc, auditSvc: auditSvc, storage: store, queries: queries}
}

// WorkspaceResponse projects repository.Workspace for API + audit consumption.
type WorkspaceResponse struct {
	ID                     string    `json:"id"`
	OrgID                  string    `json:"org_id"`
	Name                   string    `json:"name"`
	Description            string    `json:"description"`
	RepoURL                string    `json:"repo_url"`
	RepoBranch             string    `json:"repo_branch"`
	WorkingDir             string    `json:"working_dir"`
	TofuVersion            string    `json:"tofu_version"`
	Environment            string    `json:"environment"`
	AutoApply              bool      `json:"auto_apply"`
	RequiresApproval       bool      `json:"requires_approval"`
	VcsTriggerEnabled      bool      `json:"vcs_trigger_enabled"`
	Locked                 bool      `json:"locked"`
	LockedBy               *string   `json:"locked_by"`
	CurrentRunID           *string   `json:"current_run_id"`
	CreatedBy              string    `json:"created_by"`
	Source                 string    `json:"source"`
	CurrentConfigVersionID string    `json:"current_config_version_id,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`

	// EffectiveRole is the caller's role on this one workspace: their org role,
	// or a higher role one of their teams was granted through
	// workspace_team_access. Only the single-workspace read fills it in — it is
	// what the UI uses to decide which controls to offer, so it must describe
	// the workspace on screen.
	EffectiveRole string `json:"effective_role,omitempty"`
}

// WorkspaceSummaryResponse is the list-view projection: the workspace plus its
// last-run rollup.
type WorkspaceSummaryResponse struct {
	WorkspaceResponse
	LastRunStatus *string    `json:"last_run_status"`
	LastRunAt     *time.Time `json:"last_run_at"`
	ResourceCount int32      `json:"resource_count"`
}

// approvalGateChangeAllowed decides whether a caller may make this particular
// workspace update.
//
// auto_apply and requires_approval together decide whether an apply waits for a
// human. Turning the wait off removes exactly the approval that ActionApplyProd
// protects, so moving either one asks for the same org-level authority as
// signing an approval — a per-workspace team grant does not carry it. Every
// other field on the settings form stays at the route's operator bar.
func approvalGateChangeAllowed(current repository.Workspace, req UpdateWorkspaceRequest, orgRole string) bool {
	if !changesApprovalGate(current, req) {
		return true
	}
	return auth.CanPerform(orgRole, auth.ActionApplyProd)
}

// approvalGateAtCreateAllowed decides whether a caller may stand up a workspace
// with these approval-gate settings.
//
// A new workspace defaults to auto_apply off, so it starts behind the same
// human-in-the-loop Update protects. Asking for auto_apply at creation asks for
// a workspace that applies to live cloud state with nobody signing off — the
// authority Update holds at ActionApplyProd. Without this, the guard on Update
// is trivially sidestepped: create the workspace with the gate already open
// instead of opening it afterwards.
//
// requires_approval only ever adds a wait, so turning it on needs no extra bar.
func approvalGateAtCreateAllowed(autoApply bool, orgRole string) bool {
	if !autoApply {
		return true
	}
	return auth.CanPerform(orgRole, auth.ActionApplyProd)
}

// changesApprovalGate reports whether an update actually moves auto_apply or
// requires_approval away from what the workspace stores today. The settings
// form submits every field on every save, so a nil-pointer check would flag
// each save; only a real change is an authorization event.
func changesApprovalGate(current repository.Workspace, req UpdateWorkspaceRequest) bool {
	if req.AutoApply != nil && *req.AutoApply != current.AutoApply {
		return true
	}
	if req.RequiresApproval != nil && *req.RequiresApproval != current.RequiresApproval {
		return true
	}
	return false
}

func workspaceResponse(ws repository.Workspace) WorkspaceResponse {
	return WorkspaceResponse{
		ID:                     ws.ID,
		OrgID:                  ws.OrgID,
		Name:                   ws.Name,
		Description:            ws.Description,
		RepoURL:                ws.RepoURL,
		RepoBranch:             ws.RepoBranch,
		WorkingDir:             ws.WorkingDir,
		TofuVersion:            ws.TofuVersion,
		Environment:            ws.Environment,
		AutoApply:              ws.AutoApply,
		RequiresApproval:       ws.RequiresApproval,
		VcsTriggerEnabled:      ws.VcsTriggerEnabled,
		Locked:                 ws.Locked,
		LockedBy:               ws.LockedBy,
		CurrentRunID:           ws.CurrentRunID,
		CreatedBy:              ws.CreatedBy,
		Source:                 ws.Source,
		CurrentConfigVersionID: ws.CurrentConfigVersionID,
		CreatedAt:              ws.CreatedAt,
		UpdatedAt:              ws.UpdatedAt,
	}
}

func workspaceSummaryResponse(ws repository.WorkspaceSummary) WorkspaceSummaryResponse {
	return WorkspaceSummaryResponse{
		WorkspaceResponse: workspaceResponse(ws.Workspace),
		LastRunStatus:     ws.LastRunStatus,
		LastRunAt:         ws.LastRunAt,
		ResourceCount:     ws.ResourceCount,
	}
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

	data := make([]WorkspaceSummaryResponse, len(workspaces))
	for i, ws := range workspaces {
		data[i] = workspaceSummaryResponse(ws)
	}

	respond.JSON(w, http.StatusOK, respond.ListResponse[WorkspaceSummaryResponse]{
		Data:    data,
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

	resp := workspaceResponse(workspace)
	resp.EffectiveRole = auth.WorkspaceRole(r.Context())
	respond.JSON(w, http.StatusOK, resp)
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
	// repo_url / repo_branch / working_dir flow into the executor's git clone
	// and cd — validate them against a safe charset to block command/option
	// injection at the boundary (any role can create a workspace).
	if err := validateRepoFields(req.RepoURL, req.RepoBranch, req.WorkingDir); err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
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

	if !approvalGateAtCreateAllowed(req.AutoApply, userCtx.Role) {
		respond.Error(w, http.StatusForbidden, "creating a workspace with auto_apply requires admin role or higher")
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
		After: workspaceResponse(workspace), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, workspaceResponse(workspace))
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
	if err := validateRepoFields(req.RepoURL, req.RepoBranch, req.WorkingDir); err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
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

	current, err := h.svc.Get(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	if !approvalGateChangeAllowed(current, req, userCtx.Role) {
		respond.Error(w, http.StatusForbidden, "changing auto_apply or requires_approval requires admin role or higher")
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
		After: workspaceResponse(workspace), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, workspaceResponse(workspace))
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
		After: workspaceResponse(workspace), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, workspaceResponse(workspace))
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
		After: workspaceResponse(workspace), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, workspaceResponse(workspace))
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

	respond.JSON(w, http.StatusOK, workspaceResponse(updated))
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

	// A clone carries the source's approval-gate settings. Cloning an
	// auto-applying workspace therefore hands the caller a second auto-applying
	// workspace — one they can then repoint at any repo through Update, which
	// sits at the operator bar. That is the create case, so it takes the create
	// bar.
	if !approvalGateAtCreateAllowed(source.AutoApply, userCtx.Role) {
		respond.Error(w, http.StatusForbidden, "cloning a workspace with auto_apply requires admin role or higher")
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
		After:  workspaceResponse(workspace), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, workspaceResponse(workspace))
}
