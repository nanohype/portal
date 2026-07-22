package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
// human. Removing that wait removes exactly the approval that ActionApplyProd
// protects, so it asks for the same org-level authority as signing one — a
// per-workspace team grant does not carry it. Every other field on the settings
// form stays at the route's operator bar.
func approvalGateChangeAllowed(current repository.Workspace, req UpdateWorkspaceRequest, orgRole string) bool {
	if !opensApprovalGate(current, req) {
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

// gatedTwinAllowed decides whether a caller may stand up — or repoint — an
// ungated workspace onto a config some other workspace already gates.
//
// requires_approval lives on the workspace row, but what it protects is the
// infrastructure the config manages, and a workspace row is not a boundary
// around that. Two workspaces on the same repo + working_dir run the same
// resource addresses against the same cloud accounts, so an ungated twin is a
// second door onto the gated workspace's own resources, opened at the operator
// bar the create and update routes sit at.
//
// Whether they also share state varies — a terragrunt tree declares remote_state
// itself, and so does any .tf with a backend block, while portal keeps state per
// workspace for a plain-tofu leaf. It does not change the answer: plenty of
// creates are overwrites at the provider API (a bucket policy, an inline role
// policy, a DNS record), so an apply from the twin lands on the gated
// workspace's infrastructure either way, and org-scoped variables are inherited
// by any new workspace, so the twin does not need variables of its own to
// resolve to the same target.
//
// Two ways through: carry the gate too — an operator may always create the twin
// gated, since requires_approval only ever adds a wait — or hold the authority
// to release a gated apply in the first place, ActionApplyProd, the same bar
// turning the gate off already takes.
//
// This keys on the config identity portal can see. Upload workspaces have no
// repo URL to compare, so two uploads of the same tree stay indistinguishable
// here; the check does not claim to cover them.
func gatedTwinAllowed(hasGatedTwin, requiresApproval bool, orgRole string) bool {
	if !hasGatedTwin || requiresApproval {
		return true
	}
	return auth.CanPerform(orgRole, auth.ActionApplyProd)
}

const gatedTwinMessage = "another workspace already requires approval for this repository and working directory: " +
	"set requires_approval on this one too, or hold admin role or higher"

// cloneApprovalGate decides what gate a clone carries, and whether the caller
// may ask for it. It returns the gate to write and whether the request is
// allowed.
//
// A clone lands on the source's repo and working directory, so it is a twin by
// construction, and gatedTwinMessage tells the caller how to clear the refusal:
// carry the gate too. That instruction has to be followable on the route that
// prints it, so the clone request may raise the gate — the same direction
// opensApprovalGate treats as free, for the same reason, adding a wait hands out
// no authority.
//
// The other direction is the act that takes the human out of an apply. Clearing
// a gate the source holds sits at ActionApplyProd here, exactly where the update
// route holds it, so cloning is not a cheaper spelling of an ungating the update
// route would refuse. Omitting the field inherits, which is what a clone meant
// before the field existed.
func cloneApprovalGate(sourceGated bool, requested *bool, orgRole string) (gate bool, allowed bool) {
	gate = sourceGated
	if requested != nil {
		gate = *requested
	}
	if sourceGated && !gate && !auth.CanPerform(orgRole, auth.ActionApplyProd) {
		return sourceGated, false
	}
	return gate, true
}

// effectiveConfigTarget resolves what a workspace will point at, and whether it
// will be gated, once an update lands. UpdateWorkspace COALESCEs empty strings
// and nil pointers to the stored row, so a request that omits a field is a
// request to keep it.
func effectiveConfigTarget(current repository.Workspace, req UpdateWorkspaceRequest) (repoURL, workingDir string, requiresApproval bool) {
	repoURL = current.RepoURL
	if req.RepoURL != "" {
		repoURL = req.RepoURL
	}
	workingDir = current.WorkingDir
	if req.WorkingDir != "" {
		workingDir = req.WorkingDir
	}
	requiresApproval = current.RequiresApproval
	if req.RequiresApproval != nil {
		requiresApproval = *req.RequiresApproval
	}
	return repoURL, workingDir, requiresApproval
}

// movesConfigTarget reports whether an update actually repoints a workspace, or
// changes whether it is gated.
//
// The twin check answers "may this caller put an ungated workspace on a config
// something else gates". That is a question about a MOVE. The settings form
// resubmits every field on every save, so without this an operator renaming a
// workspace that already sits on such a config — a pair that predates the
// check, or one an admin set up deliberately — would be refused an edit that
// opens no door, and the only way out would be an admin. Same rule
// changesApprovalGate follows for the gate itself: only a real change is an
// authorization event.
//
// Turning the workspace's own gate off counts as a move, so an update that
// clears requires_approval is still checked (it also passes through
// approvalGateChangeAllowed, which holds that at admin on its own).
func movesConfigTarget(current repository.Workspace, repoURL, workingDir string, requiresApproval bool) bool {
	return repoURL != current.RepoURL ||
		workingDir != current.WorkingDir ||
		requiresApproval != current.RequiresApproval
}

// opensApprovalGate reports whether an update takes the human out of an apply on
// this workspace: turning auto_apply on, or turning requires_approval off.
//
// The direction is the whole point, and it is the same direction
// approvalGateAtCreateAllowed reads at creation. Adding a wait — gating a
// workspace that was not gated, or dropping auto-apply — hands out no authority
// the caller did not already have; it only costs them the ability to apply
// unattended, on a workspace the operator route already lets them repoint. So it
// is theirs to do.
//
// Holding both directions at admin looked symmetric but made the twin check's
// documented way out unreachable: gatedTwinMessage tells an operator to set
// requires_approval on their workspace too, and the update route would then
// refuse the very field it asked for. It also meant an operator could never
// raise protection on a workspace they own.
//
// The settings form submits every field on every save, so a nil-pointer check
// would flag each save; only a real change in that direction is an
// authorization event.
func opensApprovalGate(current repository.Workspace, req UpdateWorkspaceRequest) bool {
	if req.AutoApply != nil && *req.AutoApply && !current.AutoApply {
		return true
	}
	if req.RequiresApproval != nil && !*req.RequiresApproval && current.RequiresApproval {
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

	// RequiresApproval overrides the gate the clone would otherwise inherit
	// from its source. It exists because the twin refusal names it: cloning an
	// ungated workspace that sits on a config something else gates is refused
	// with "set requires_approval on this one too", and without this field
	// there is no way to do that on the clone route — the caller would have to
	// create the workspace by hand and copy the variables across, which is
	// itself an admin-only call. Nil means inherit.
	RequiresApproval *bool `json:"requires_approval"`
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
	// One leaf, one spelling. The twin check below and the row it writes both
	// have to be about the directory, not about how the request typed it.
	req.WorkingDir = service.CanonicalWorkingDir(req.WorkingDir)

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

	hasGatedTwin, err := h.svc.HasGatedTwin(r.Context(), userCtx.OrgID, req.RepoURL, req.WorkingDir, "")
	if err != nil {
		slog.Error("failed to check for a gated workspace on this config", "error", err)
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to create workspace")
		return
	}
	if !gatedTwinAllowed(hasGatedTwin, req.RequiresApproval, userCtx.Role) {
		respond.Error(w, http.StatusForbidden, gatedTwinMessage)
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
	// Canonicalise before anything reads the field, so movesConfigTarget
	// compares the stored leaf against the requested leaf and not one spelling
	// against another — otherwise a resubmit of the same directory typed
	// differently reads as a move, and a real move to the same directory typed
	// differently reads as a new target.
	req.WorkingDir = service.CanonicalWorkingDir(req.WorkingDir)
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

	// Update COALESCEs empty strings to the stored value, so the twin check has
	// to run against what the workspace will point at after the write, not
	// against what the request happened to carry. An update that leaves the
	// config target and the gate exactly where they already are is not a move,
	// and the twin check has nothing to decide about it.
	targetRepoURL, targetWorkingDir, targetGated := effectiveConfigTarget(current, req)
	if movesConfigTarget(current, targetRepoURL, targetWorkingDir, targetGated) {
		hasGatedTwin, err := h.svc.HasGatedTwin(r.Context(), userCtx.OrgID, targetRepoURL, targetWorkingDir, workspaceID)
		if err != nil {
			slog.Error("failed to check for a gated workspace on this config", "error", err)
			respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to update workspace")
			return
		}
		if !gatedTwinAllowed(hasGatedTwin, targetGated, userCtx.Role) {
			respond.Error(w, http.StatusForbidden, gatedTwinMessage)
			return
		}
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

	requiresApproval, ok := cloneApprovalGate(source.RequiresApproval, req.RequiresApproval, userCtx.Role)
	if !ok {
		respond.Error(w, http.StatusForbidden, "cloning a workspace without its requires_approval gate requires admin role or higher")
		return
	}

	// A clone copies the source's repo and working_dir, so cloning a gated
	// workspace produces a gated one unless the request said otherwise. Cloning
	// an ungated workspace that happens to sit on a config some OTHER workspace
	// gates is the twin case, and takes the twin bar.
	hasGatedTwin, err := h.svc.HasGatedTwin(r.Context(), userCtx.OrgID, source.RepoURL, source.WorkingDir, sourceID)
	if err != nil {
		slog.Error("failed to check for a gated workspace on this config", "error", err)
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to clone workspace")
		return
	}
	if !gatedTwinAllowed(hasGatedTwin, requiresApproval, userCtx.Role) {
		respond.Error(w, http.StatusForbidden, gatedTwinMessage)
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
		RequiresApproval:  requiresApproval,
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
