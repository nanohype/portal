package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
	"github.com/nanohype/portal/internal/service"
	"github.com/nanohype/portal/internal/storage"
	"github.com/nanohype/portal/internal/tfstate"
)

type VariableHandler struct {
	queries      *repository.Queries
	encryptor    *secrets.Encryptor
	auditSvc     *service.AuditService
	workspaceSvc *service.WorkspaceService
	discoverySvc *service.DiscoveryService
	storage      *storage.S3Storage
}

func NewVariableHandler(queries *repository.Queries, encryptor *secrets.Encryptor, auditSvc *service.AuditService, workspaceSvc *service.WorkspaceService, discoverySvc *service.DiscoveryService, store *storage.S3Storage) *VariableHandler {
	return &VariableHandler{queries: queries, encryptor: encryptor, auditSvc: auditSvc, workspaceSvc: workspaceSvc, discoverySvc: discoverySvc, storage: store}
}

type CreateVariableRequest struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Sensitive   bool   `json:"sensitive"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

type VariableResponse struct {
	repository.WorkspaceVariable
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
	for i := range vars {
		if vars[i].Sensitive {
			vars[i].Value = "***"
		}
	}

	respond.List(w, vars)
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
		After: auditVar, IPAddress: ip, UserAgent: ua,
	})

	if v.Sensitive {
		v.Value = "***"
	}

	respond.JSON(w, http.StatusCreated, v)
}

func (h *VariableHandler) Update(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	varID := chi.URLParam(r, "variableID")

	// Fetch current state for audit log
	before, err := h.queries.GetWorkspaceVariable(r.Context(), repository.GetWorkspaceVariableParams{
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

	v, err := h.queries.UpdateWorkspaceVariable(r.Context(), repository.UpdateWorkspaceVariableParams{
		ID: varID, OrgID: userCtx.OrgID, Value: value, Sensitive: req.Sensitive, Description: req.Description, Category: req.Category,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update variable")
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
		Before: auditBefore, After: auditVar, IPAddress: ip, UserAgent: ua,
	})

	if v.Sensitive {
		v.Value = "***"
	}

	respond.JSON(w, http.StatusOK, v)
}

func (h *VariableHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	varID := chi.URLParam(r, "variableID")

	if err := h.queries.DeleteWorkspaceVariable(r.Context(), repository.DeleteWorkspaceVariableParams{
		ID: varID, OrgID: userCtx.OrgID,
	}); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to delete variable")
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

	created := make([]repository.WorkspaceVariable, 0, len(req.Variables))
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
			After: auditVar, IPAddress: ip, UserAgent: ua,
		})

		if v.Sensitive {
			v.Value = "***"
		}
		created = append(created, v)
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

	if h.storage == nil {
		respond.Error(w, http.StatusServiceUnavailable, "storage not configured")
		return
	}

	// Get latest state from the source workspace
	sv, err := h.queries.GetLatestStateVersion(r.Context(), repository.GetLatestStateVersionParams{
		WorkspaceID: req.SourceWorkspaceID, OrgID: userCtx.OrgID,
	})
	if err != nil {
		respond.Error(w, http.StatusNotFound, "source workspace has no state")
		return
	}

	data, err := h.storage.GetState(r.Context(), sv.StateURL)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to fetch source state")
		return
	}

	outputs, err := tfstate.ParseOutputs(data)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to parse source outputs")
		return
	}

	if len(outputs) == 0 {
		respond.Error(w, http.StatusBadRequest, "source workspace has no outputs")
		return
	}

	// Get existing variables to detect duplicates
	existing, err := h.queries.ListWorkspaceVariables(r.Context(), repository.ListWorkspaceVariablesParams{
		WorkspaceID: workspaceID, OrgID: userCtx.OrgID,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list existing variables")
		return
	}
	existingByKey := make(map[string]repository.WorkspaceVariable, len(existing))
	for _, v := range existing {
		if v.Category == "terraform" {
			existingByKey[v.Key] = v
		}
	}

	// Create or update variables from outputs
	ip, ua := auditContext(r)
	var result []repository.WorkspaceVariable
	for _, out := range outputs {
		desc := fmt.Sprintf("Imported from workspace output (%s)", out.Type)

		// Convert output value to string for variable storage
		var valueStr string
		switch v := out.Value.(type) {
		case string:
			valueStr = v
		default:
			b, _ := json.Marshal(v)
			valueStr = string(b)
		}

		if ev, exists := existingByKey[out.Name]; exists {
			// Update existing variable with the output value
			v, err := h.queries.UpdateWorkspaceVariable(r.Context(), repository.UpdateWorkspaceVariableParams{
				ID: ev.ID, OrgID: userCtx.OrgID, Value: valueStr, Sensitive: false, Description: desc,
			})
			if err != nil {
				continue
			}
			auditVar := v
			auditVar.Value = "***"
			h.auditSvc.Log(r.Context(), service.AuditEntry{
				OrgID: userCtx.OrgID, UserID: userCtx.UserID,
				Action: "variable.import", EntityType: "variable", EntityID: v.ID,
				After: auditVar, IPAddress: ip, UserAgent: ua,
			})
			result = append(result, v)
		} else {
			// Create new variable
			v, err := h.queries.CreateWorkspaceVariable(r.Context(), repository.CreateWorkspaceVariableParams{
				ID:          ulid.Make().String(),
				WorkspaceID: workspaceID,
				OrgID:       userCtx.OrgID,
				Key:         out.Name,
				Value:       valueStr,
				Sensitive:   false,
				Category:    "terraform",
				Description: desc,
			})
			if err != nil {
				continue
			}
			auditVar := v
			auditVar.Value = "***"
			h.auditSvc.Log(r.Context(), service.AuditEntry{
				OrgID: userCtx.OrgID, UserID: userCtx.UserID,
				Action: "variable.import", EntityType: "variable", EntityID: v.ID,
				After: auditVar, IPAddress: ip, UserAgent: ua,
			})
			result = append(result, v)
		}
	}

	respond.JSON(w, http.StatusCreated, result)
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

	// Verify source workspace belongs to same org
	_, err := h.workspaceSvc.Get(r.Context(), req.SourceWorkspaceID, userCtx.OrgID)
	if err != nil {
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
	copied := make([]repository.WorkspaceVariable, 0, len(affected))
	for _, v := range affected {
		auditVar := v
		auditVar.Value = "***"
		h.auditSvc.Log(r.Context(), service.AuditEntry{
			OrgID: userCtx.OrgID, UserID: userCtx.UserID,
			Action: "variable.copy", EntityType: "variable", EntityID: v.ID,
			After: auditVar, IPAddress: ip, UserAgent: ua,
		})

		if v.Sensitive {
			v.Value = "***"
		}
		copied = append(copied, v)
	}

	respond.JSON(w, http.StatusCreated, copied)
}

func (h *VariableHandler) RevealValue(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	varID := chi.URLParam(r, "variableID")

	v, err := h.queries.GetWorkspaceVariable(r.Context(), repository.GetWorkspaceVariableParams{
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
