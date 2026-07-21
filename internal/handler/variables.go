package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
	"github.com/nanohype/portal/internal/service"
)

type VariableHandler struct {
	queries      *repository.Queries
	encryptor    *secrets.Encryptor
	auditSvc     *service.AuditService
	workspaceSvc *service.WorkspaceService
	discoverySvc *service.DiscoveryService
	authz        auth.WorkspaceRoleResolver
}

// NewVariableHandler builds the workspace-variable handler. It takes the
// workspace-role resolver because two of its endpoints — copy and
// import-outputs — name a SECOND workspace in the request body. The route's
// gate only covers the workspace in the path, so those two have to authorize
// the body's workspace themselves.
func NewVariableHandler(queries *repository.Queries, encryptor *secrets.Encryptor, auditSvc *service.AuditService, workspaceSvc *service.WorkspaceService, discoverySvc *service.DiscoveryService, authz auth.WorkspaceRoleResolver) *VariableHandler {
	return &VariableHandler{
		queries:      queries,
		encryptor:    encryptor,
		auditSvc:     auditSvc,
		workspaceSvc: workspaceSvc,
		discoverySvc: discoverySvc,
		authz:        authz,
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

	vars, err := h.queries.ListWorkspaceVariables(r.Context(), repository.ListWorkspaceVariablesParams{
		WorkspaceID: workspaceID, OrgID: userCtx.OrgID,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list variables")
		return
	}

	// Redact sensitive values in response
	data := make([]WorkspaceVariableResponse, len(vars))
	for i, v := range vars {
		if v.Sensitive {
			v.Value = "***"
		}
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

	v, err := h.queries.CreateWorkspaceVariable(r.Context(), repository.CreateWorkspaceVariableParams{
		ID:          ulid.Make().String(),
		WorkspaceID: workspaceID,
		OrgID:       userCtx.OrgID,
		Key:         req.Key,
		Value:       value,
		Sensitive:   req.Sensitive,
		Category:    req.Category,
		Description: req.Description,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to create variable")
		return
	}

	ip, ua := auditContext(r)
	auditVar := v
	auditVar.Value = "***" // never log variable values
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "variable.create", EntityType: "variable", EntityID: v.ID,
		After: workspaceVariableResponse(auditVar), IPAddress: ip, UserAgent: ua,
	})

	if v.Sensitive {
		v.Value = "***"
	}

	respond.JSON(w, http.StatusCreated, workspaceVariableResponse(v))
}

func (h *VariableHandler) Update(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")
	varID := chi.URLParam(r, "variableID")

	// Fetch current state for audit log. Keyed on the workspace this request
	// was authorized against, so a variable id from another workspace is not
	// found rather than editable.
	before, err := h.queries.GetWorkspaceVariable(r.Context(), repository.GetWorkspaceVariableParams{
		ID: varID, WorkspaceID: workspaceID, OrgID: userCtx.OrgID,
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

	v, err := h.queries.UpdateWorkspaceVariable(r.Context(), repository.UpdateWorkspaceVariableParams{
		ID: varID, WorkspaceID: workspaceID, OrgID: userCtx.OrgID,
		Value: value, Sensitive: req.Sensitive, Description: req.Description, Category: req.Category,
	})
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	auditBefore := before
	auditBefore.Value = "***"
	auditVar := v
	auditVar.Value = "***"
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "variable.update", EntityType: "variable", EntityID: varID,
		Before: workspaceVariableResponse(auditBefore), After: workspaceVariableResponse(auditVar),
		IPAddress: ip, UserAgent: ua,
	})

	if v.Sensitive {
		v.Value = "***"
	}

	respond.JSON(w, http.StatusOK, workspaceVariableResponse(v))
}

func (h *VariableHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")
	varID := chi.URLParam(r, "variableID")

	// The delete returns the row it removed, so a variable id belonging to
	// another workspace comes back as pgx.ErrNoRows → 404 instead of reporting
	// a success that deleted nothing.
	if _, err := h.queries.DeleteWorkspaceVariable(r.Context(), repository.DeleteWorkspaceVariableParams{
		ID: varID, WorkspaceID: workspaceID, OrgID: userCtx.OrgID,
	}); err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "variable.delete", EntityType: "variable", EntityID: varID,
		IPAddress: ip, UserAgent: ua,
	})

	respond.NoContent(w)
}

func (h *VariableHandler) Discover(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	ws, err := h.workspaceSvc.Get(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	// Discover acquires the config + parses its variable surface in the service
	// layer. It is synchronous and request-scoped — the UI consumes the array
	// inline (no job). /discover is intentionally not list-enveloped.
	result, err := h.discoverySvc.DiscoverVariables(r.Context(), ws, userCtx.OrgID)
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

	if len(req.Variables) == 0 {
		respond.Error(w, http.StatusBadRequest, "variables array is required")
		return
	}
	if len(req.Variables) > 50 {
		respond.Error(w, http.StatusBadRequest, "maximum 50 variables per batch")
		return
	}

	// Check for duplicate keys within the batch
	seen := make(map[string]bool, len(req.Variables))
	for _, v := range req.Variables {
		if v.Key == "" {
			respond.Error(w, http.StatusBadRequest, "all variables must have a key")
			return
		}
		if seen[v.Key] {
			respond.Error(w, http.StatusBadRequest, "duplicate key: "+v.Key)
			return
		}
		seen[v.Key] = true
	}

	created := make([]WorkspaceVariableResponse, 0, len(req.Variables))
	ip, ua := auditContext(r)

	for _, rv := range req.Variables {
		if rv.Category == "" {
			rv.Category = "terraform"
		}
		if rv.Category != "terraform" && rv.Category != "env" {
			respond.Error(w, http.StatusBadRequest, "category must be 'terraform' or 'env' for key: "+rv.Key)
			return
		}

		value := rv.Value
		if rv.Sensitive && h.encryptor != nil {
			encrypted, err := h.encryptor.Encrypt(rv.Value)
			if err != nil {
				respond.Error(w, http.StatusInternalServerError, "failed to encrypt value")
				return
			}
			value = encrypted
		}

		v, err := h.queries.CreateWorkspaceVariable(r.Context(), repository.CreateWorkspaceVariableParams{
			ID:          ulid.Make().String(),
			WorkspaceID: workspaceID,
			OrgID:       userCtx.OrgID,
			Key:         rv.Key,
			Value:       value,
			Sensitive:   rv.Sensitive,
			Category:    rv.Category,
			Description: rv.Description,
		})
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "failed to create variable: "+rv.Key)
			return
		}

		auditVar := v
		auditVar.Value = "***"
		h.auditSvc.Log(r.Context(), service.AuditEntry{
			OrgID: userCtx.OrgID, UserID: userCtx.UserID,
			Action: "variable.create", EntityType: "variable", EntityID: v.ID,
			After: workspaceVariableResponse(auditVar), IPAddress: ip, UserAgent: ua,
		})

		if v.Sensitive {
			v.Value = "***"
		}
		created = append(created, workspaceVariableResponse(v))
	}

	respond.JSON(w, http.StatusCreated, created)
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

	imported, err := h.workspaceSvc.ImportOutputs(r.Context(), service.ImportOutputsParams{
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

	// Keyed on the workspace this request was authorized against: the caller
	// cleared the bar on THIS workspace, so this workspace's variables are the
	// only ones it can decrypt.
	v, err := h.queries.GetWorkspaceVariable(r.Context(), repository.GetWorkspaceVariableParams{
		ID: varID, WorkspaceID: workspaceID, OrgID: userCtx.OrgID,
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
		Action: "variable.reveal", EntityType: "variable", EntityID: varID,
		IPAddress: ip, UserAgent: ua,
	})

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

	merged := make(map[string]EffectiveVariableResponse) // key: "key|category"

	// Layer 1: org variables (lowest precedence)
	orgVars, err := h.queries.ListOrgVariables(r.Context(), userCtx.OrgID)
	if err == nil {
		for _, v := range orgVars {
			val := v.Value
			if v.Sensitive {
				val = "***"
			}
			merged[v.Key+"|"+v.Category] = EffectiveVariableResponse{
				Key: v.Key, Value: val, Sensitive: v.Sensitive,
				Category: v.Category, Description: v.Description,
				Source: "org", SourceID: v.ID,
			}
		}
	}

	// Layer 2: pipeline variables (if pipeline_id given)
	if pipelineID != "" {
		pipelineVars, err := h.queries.ListPipelineVariables(r.Context(), repository.ListPipelineVariablesParams{
			PipelineID: pipelineID, OrgID: userCtx.OrgID,
		})
		if err == nil {
			for _, v := range pipelineVars {
				val := v.Value
				if v.Sensitive {
					val = "***"
				}
				mergeEffectiveVar(merged, v.Key, val, v.Sensitive, v.Category, v.Description, "pipeline", v.ID)
			}
		}
	}

	// Layer 3: workspace variables (highest precedence)
	wsVars, err := h.queries.ListWorkspaceVariables(r.Context(), repository.ListWorkspaceVariablesParams{
		WorkspaceID: workspaceID, OrgID: userCtx.OrgID,
	})
	if err == nil {
		for _, v := range wsVars {
			val := v.Value
			if v.Sensitive {
				val = "***"
			}
			mergeEffectiveVar(merged, v.Key, val, v.Sensitive, v.Category, v.Description, "workspace", v.ID)
		}
	}

	result := make([]EffectiveVariableResponse, 0, len(merged))
	for _, v := range merged {
		result = append(result, v)
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
	if existing, ok := merged[mapKey]; ok && category == "terraform" && isEffectiveTagsKey(key) && !sensitive {
		if m := deepMergeJSONStrings(existing.Value, val); m != "" {
			ev.Value = m
			ev.Description = fmt.Sprintf("Merged from %s + %s", existing.Source, source)
		}
	}
	merged[mapKey] = ev
}

func isEffectiveTagsKey(key string) bool {
	return key == "tags" || key == "default_tags" || key == "extra_tags" ||
		strings.HasSuffix(key, "_tags")
}

func deepMergeJSONStrings(a, b string) string {
	var mapA, mapB map[string]interface{}
	if json.Unmarshal([]byte(a), &mapA) != nil {
		return ""
	}
	if json.Unmarshal([]byte(b), &mapB) != nil {
		return ""
	}
	for k, v := range mapB {
		mapA[k] = v
	}
	out, err := json.Marshal(mapA)
	if err != nil {
		return ""
	}
	return string(out)
}
