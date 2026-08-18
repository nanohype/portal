package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
	"github.com/nanohype/portal/internal/service"
	"github.com/nanohype/portal/internal/varmerge"
)

// auditLogger is the audit surface the variable handlers use. Narrowing to it
// keeps the constructors taking *service.AuditService while making the
// disclosure path's failure branch reachable from a test — the branch that
// decides whether a secret is released when its audit row did not land.
type auditLogger interface {
	Log(ctx context.Context, entry service.AuditEntry)
	LogDisclosure(ctx context.Context, entry service.AuditEntry) error
}

type VariableHandler struct {
	queries      *repository.Queries
	variableSvc  *service.VariableService
	encryptor    *secrets.Encryptor
	auditSvc     auditLogger
	workspaceSvc *service.WorkspaceService
	discoverySvc *service.DiscoveryService
	authz        auth.WorkspaceRoleResolver
}

// NewVariableHandler builds the workspace-variable handler. It takes the
// workspace-role resolver because two of its endpoints — copy and
// import-outputs — name a SECOND workspace in the request body. The route's
// gate only covers the workspace in the path, so those two have to authorize
// the body's workspace themselves.
func NewVariableHandler(queries *repository.Queries, encryptor *secrets.Encryptor, auditSvc auditLogger, variableSvc *service.VariableService, workspaceSvc *service.WorkspaceService, discoverySvc *service.DiscoveryService, authz auth.WorkspaceRoleResolver) *VariableHandler {
	return &VariableHandler{
		queries:      queries,
		encryptor:    encryptor,
		auditSvc:     auditSvc,
		workspaceSvc: workspaceSvc,
		discoverySvc: discoverySvc,
		authz:        authz,
		variableSvc:  variableSvc,
	}
}

// authorizeSourceWorkspace checks the caller against a workspace named in the
// request body, at the same bar the route already applied to the destination.
//
// Both endpoints that use it move variable material out of the source: copy
// takes every variable including sensitive ciphertext (the encryption key is
// org-wide, so ciphertext is portable), import-outputs takes the source's
// latest state outputs. Reading that out of a workspace is a
// manage-variables-grade act on THAT workspace, so it carries the same action
// there as on the destination.
//
// The answer is 404, not 403, so the response cannot be used to probe which
// workspace ids exist.
func (h *VariableHandler) authorizeSourceWorkspace(r *http.Request, sourceWorkspaceID string) bool {
	user := auth.GetUser(r.Context())
	role := auth.EffectiveWorkspaceRole(r.Context(), h.authz, user, sourceWorkspaceID)
	return role != "" && auth.CanPerform(role, auth.ActionManageVars)
}

type CreateVariableRequest struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Sensitive   bool   `json:"sensitive"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

// WorkspaceVariableResponse projects repository.WorkspaceVariable for API +
// audit consumption; sensitive values are redacted to *** before mapping.
type WorkspaceVariableResponse struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	OrgID       string    `json:"org_id"`
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Sensitive   bool      `json:"sensitive"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func workspaceVariableResponse(v repository.WorkspaceVariable) WorkspaceVariableResponse {
	return WorkspaceVariableResponse{
		ID:          v.ID,
		WorkspaceID: v.WorkspaceID,
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

func (h *VariableHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	vars, err := h.variableSvc.ListWorkspaceVariables(r.Context(), userCtx.OrgID, workspaceID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	data := make([]WorkspaceVariableResponse, len(vars))
	for i, v := range vars {
		data[i] = workspaceVariableResponse(v)
	}
	respond.List(w, data)
}

func (h *VariableHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	var req CreateVariableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	v, err := h.variableSvc.CreateWorkspaceVariable(r.Context(), userCtx.OrgID, workspaceID, variableInputFrom(req), actorFrom(r))
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusCreated, workspaceVariableResponse(v))
}

func (h *VariableHandler) Update(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")
	varID := chi.URLParam(r, "variableID")

	var req CreateVariableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	v, err := h.variableSvc.UpdateWorkspaceVariable(r.Context(), userCtx.OrgID, workspaceID, varID, variableInputFrom(req), actorFrom(r))
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusOK, workspaceVariableResponse(v))
}

func (h *VariableHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")
	varID := chi.URLParam(r, "variableID")

	if err := h.variableSvc.DeleteWorkspaceVariable(r.Context(), userCtx.OrgID, workspaceID, varID, actorFrom(r)); err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.NoContent(w)
}

// discoverIncludesValues reports whether /discover answers this request with
// the value column filled in.
//
// The route sits on the read bar: which variables a config declares is part of
// reading the workspace. What they are SET to is not — a resolved terragrunt
// input carries whatever get_env() or a dependency output produced, which is
// the same class of data as a stored variable's value. So the values ride the
// bar the variable writes ride, read off the effective role the workspace gate
// computed. An empty role means no gate ran on this request, and clears
// nothing.
func discoverIncludesValues(ctx context.Context) bool {
	return auth.CanPerform(auth.WorkspaceRole(ctx), auth.ActionManageVars)
}

func (h *VariableHandler) Discover(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	ws, err := h.workspaceSvc.Get(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	includeValues := discoverIncludesValues(r.Context())

	// Discover acquires the config + parses its variable surface in the service
	// layer. It is synchronous and request-scoped — the UI consumes the array
	// inline (no job). /discover is intentionally not list-enveloped.
	result, err := h.discoverySvc.DiscoverVariables(r.Context(), ws, userCtx.OrgID, includeValues)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusOK, result)
}

type BulkCreateVariablesRequest struct {
	Variables []CreateVariableRequest `json:"variables"`
}

func (h *VariableHandler) BulkCreate(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	var req BulkCreateVariablesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ins := make([]service.VariableInput, len(req.Variables))
	for i, rv := range req.Variables {
		ins[i] = variableInputFrom(rv)
	}

	created, err := h.variableSvc.BulkCreateWorkspaceVariables(r.Context(), userCtx.OrgID, workspaceID, ins, actorFrom(r))
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	data := make([]WorkspaceVariableResponse, len(created))
	for i, v := range created {
		data[i] = workspaceVariableResponse(v)
	}
	respond.JSON(w, http.StatusCreated, data)
}

type ImportOutputsRequest struct {
	SourceWorkspaceID string `json:"source_workspace_id"`
}

func (h *VariableHandler) ImportOutputs(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	var req ImportOutputsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SourceWorkspaceID == "" {
		respond.Error(w, http.StatusBadRequest, "source_workspace_id is required")
		return
	}

	// The route's gate covered the destination. The source arrives in the body,
	// so it is authorized here or not at all.
	if !h.authorizeSourceWorkspace(r, req.SourceWorkspaceID) {
		respond.Error(w, http.StatusNotFound, "source workspace not found")
		return
	}

	imported, skippedSensitive, err := h.workspaceSvc.ImportOutputs(r.Context(), service.ImportOutputsParams{
		SourceWorkspaceID: req.SourceWorkspaceID,
		TargetWorkspaceID: workspaceID,
		OrgID:             userCtx.OrgID,
		DescriptionSource: "workspace",
	})
	if err != nil {
		if errors.Is(err, service.ErrStorageNotConfigured) {
			respond.Error(w, http.StatusServiceUnavailable, "storage not configured")
			return
		}
		respond.FromError(w, r, err)
		return
	}
	if len(imported) == 0 {
		// Saying "no outputs" when the source has several would send the
		// operator looking for a bug in the source workspace. Sensitive outputs
		// are redacted in state, so there is no value to bring across — say
		// that instead.
		if skippedSensitive > 0 {
			respond.Error(w, http.StatusBadRequest, fmt.Sprintf(
				"every output on the source workspace is sensitive (%d skipped): "+
					"state redacts sensitive values, so there is nothing to import",
				skippedSensitive))
			return
		}
		respond.Error(w, http.StatusBadRequest, "source workspace has no outputs")
		return
	}

	ip, ua := auditContext(r)
	data := make([]WorkspaceVariableResponse, len(imported))
	for i, v := range imported {
		auditVar := v
		auditVar.Value = "***"
		h.auditSvc.Log(r.Context(), service.AuditEntry{
			OrgID: userCtx.OrgID, UserID: userCtx.UserID,
			Action: "variable.import", EntityType: "variable", EntityID: v.ID,
			After: workspaceVariableResponse(auditVar), IPAddress: ip, UserAgent: ua,
		})

		// Same redaction the list and copy responses apply: an imported output
		// marked sensitive stays behind the reveal endpoint.
		if v.Sensitive {
			v.Value = "***"
		}
		data[i] = workspaceVariableResponse(v)
	}

	respond.JSON(w, http.StatusCreated, data)
}

type CopyVariablesRequest struct {
	SourceWorkspaceID string `json:"source_workspace_id"`
}

func (h *VariableHandler) CopyVariables(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	var req CopyVariablesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.SourceWorkspaceID == "" {
		respond.Error(w, http.StatusBadRequest, "source_workspace_id is required")
		return
	}

	if req.SourceWorkspaceID == workspaceID {
		respond.Error(w, http.StatusBadRequest, "source and target workspace must be different")
		return
	}

	// The route's gate covered the destination. The source arrives in the body,
	// so it is authorized here — at the same bar, on that workspace — before
	// its variables are read. Org membership alone is not enough: a caller
	// elevated on the destination by a team grant holds nothing on the source.
	if !h.authorizeSourceWorkspace(r, req.SourceWorkspaceID) {
		respond.Error(w, http.StatusNotFound, "source workspace not found")
		return
	}

	if _, err := h.workspaceSvc.Get(r.Context(), req.SourceWorkspaceID, userCtx.OrgID); err != nil {
		respond.Error(w, http.StatusNotFound, "source workspace not found")
		return
	}

	// The copy is one transaction (create-or-update by key+category) in the
	// service, so a mid-copy failure leaves the target unchanged. Values copy as
	// stored — the encryption key is org-wide, so ciphertext is portable.
	affected, err := h.workspaceSvc.CopyInto(r.Context(), req.SourceWorkspaceID, workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	copied := make([]WorkspaceVariableResponse, 0, len(affected))
	for _, v := range affected {
		auditVar := v
		auditVar.Value = "***"
		h.auditSvc.Log(r.Context(), service.AuditEntry{
			OrgID: userCtx.OrgID, UserID: userCtx.UserID,
			Action: "variable.copy", EntityType: "variable", EntityID: v.ID,
			After: workspaceVariableResponse(auditVar), IPAddress: ip, UserAgent: ua,
		})

		if v.Sensitive {
			v.Value = "***"
		}
		copied = append(copied, workspaceVariableResponse(v))
	}

	respond.JSON(w, http.StatusCreated, copied)
}

func (h *VariableHandler) RevealValue(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")
	varID := chi.URLParam(r, "variableID")

	value, err := h.variableSvc.RevealWorkspaceVariable(r.Context(), userCtx.OrgID, workspaceID, varID, actorFrom(r))
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]string{"value": value})
}

type EffectiveVariableResponse struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Sensitive   bool   `json:"sensitive"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Source      string `json:"source"` // "org", "pipeline", or "workspace"
	SourceID    string `json:"source_id"`
}

func (h *VariableHandler) Effective(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")
	pipelineID := r.URL.Query().Get("pipeline_id")

	vars, err := h.variableSvc.EffectiveVariables(r.Context(), userCtx.OrgID, workspaceID, pipelineID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	result := make([]EffectiveVariableResponse, len(vars))
	for i, v := range vars {
		result[i] = EffectiveVariableResponse{
			Key: v.Key, Value: v.Value, Sensitive: v.Sensitive,
			Category: v.Category, Description: v.Description,
			Source: v.Source, SourceID: v.SourceID,
		}
	}
	respond.JSON(w, http.StatusOK, result)
}

func mergeEffectiveVar(merged map[string]EffectiveVariableResponse, key, val string, sensitive bool, category, description, source, sourceID string) {
	mapKey := key + "|" + category
	ev := EffectiveVariableResponse{
		Key: key, Value: val, Sensitive: sensitive,
		Category: category, Description: description,
		Source: source, SourceID: sourceID,
	}
	if existing, ok := merged[mapKey]; ok && category == "terraform" && varmerge.IsTagsKey(key) && !sensitive {
		if m := varmerge.DeepMergeJSON(existing.Value, val); m != "" {
			ev.Value = m
			ev.Description = fmt.Sprintf("Merged from %s + %s", existing.Source, source)
		}
	}
	merged[mapKey] = ev
}
