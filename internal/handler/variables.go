package handler

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/stxkxs/tofui/internal/auth"
	"github.com/stxkxs/tofui/internal/handler/respond"
	"github.com/stxkxs/tofui/internal/repository"
	"github.com/stxkxs/tofui/internal/secrets"
	"github.com/stxkxs/tofui/internal/service"
	"github.com/stxkxs/tofui/internal/storage"
	"github.com/stxkxs/tofui/internal/tfparse"
	"github.com/stxkxs/tofui/internal/tfstate"
)

type VariableHandler struct {
	queries      *repository.Queries
	encryptor    *secrets.Encryptor
	auditSvc     *service.AuditService
	workspaceSvc *service.WorkspaceService
	storage      *storage.S3Storage
}

func NewVariableHandler(queries *repository.Queries, encryptor *secrets.Encryptor, auditSvc *service.AuditService, workspaceSvc *service.WorkspaceService, store *storage.S3Storage) *VariableHandler {
	return &VariableHandler{queries: queries, encryptor: encryptor, auditSvc: auditSvc, workspaceSvc: workspaceSvc, storage: store}
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

	respond.JSON(w, http.StatusOK, vars)
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

type DiscoverVariableResponse struct {
	Name         string  `json:"name"`
	Type         string  `json:"type,omitempty"`
	Description  string  `json:"description,omitempty"`
	Default      *string `json:"default,omitempty"`
	Required     bool    `json:"required"`
	Configured   bool    `json:"configured"`
	ConfiguredBy string  `json:"configured_by,omitempty"` // "terragrunt" | "tofui"
}

func (h *VariableHandler) Discover(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	ws, err := h.workspaceSvc.Get(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "workspace not found")
		return
	}

	tmpDir, err := os.MkdirTemp("", "tofui-discover-*")
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to create temp directory")
		return
	}
	defer os.RemoveAll(tmpDir)

	if ws.Source == "upload" {
		// Extract config archive from S3
		if ws.CurrentConfigVersionID == "" {
			respond.Error(w, http.StatusBadRequest, "no configuration uploaded yet")
			return
		}
		if h.storage == nil {
			respond.Error(w, http.StatusServiceUnavailable, "storage not configured")
			return
		}
		key := fmt.Sprintf("configs/%s/%s.tar.gz", workspaceID, ws.CurrentConfigVersionID)
		data, err := h.storage.GetConfigArchive(r.Context(), key)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "failed to download configuration")
			return
		}
		if err := extractDiscoverArchive(data, tmpDir); err != nil {
			respond.Error(w, http.StatusInternalServerError, "failed to extract configuration")
			return
		}
	} else {
		if ws.RepoURL == "" {
			respond.Error(w, http.StatusBadRequest, "workspace has no repository URL configured")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", ws.RepoBranch, ws.RepoURL, tmpDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			slog.Error("git clone failed", "error", err, "output", string(output), "repo", ws.RepoURL)
			respond.Error(w, http.StatusBadGateway, "failed to clone repository")
			return
		}
	}

	parseDir := tmpDir
	if ws.WorkingDir != "" && ws.WorkingDir != "." {
		parseDir = filepath.Join(tmpDir, ws.WorkingDir)
	}

	// Load existing tofui-managed workspace variables for cross-reference.
	existing, err := h.queries.ListWorkspaceVariables(r.Context(), repository.ListWorkspaceVariablesParams{
		WorkspaceID: workspaceID, OrgID: userCtx.OrgID,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list existing variables")
		return
	}
	configuredKeys := make(map[string]bool, len(existing))
	for _, v := range existing {
		configuredKeys[v.Key] = true
	}

	// Terragrunt-driven workspaces: shell out to `terragrunt render --json`
	// to get the resolved inputs (merged from leaf + includes + _envcommon)
	// and the absolute path to the underlying terraform module. Parse the
	// module's variables.tf for the canonical schema and merge.
	if _, statErr := os.Stat(filepath.Join(parseDir, "terragrunt.hcl")); statErr == nil {
		respond.JSON(w, http.StatusOK, discoverTerragrunt(r.Context(), parseDir, configuredKeys))
		return
	}

	discovered, err := tfparse.ParseDirectory(parseDir)
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "failed to parse terraform files")
		return
	}

	result := make([]DiscoverVariableResponse, len(discovered))
	for i, d := range discovered {
		result[i] = DiscoverVariableResponse{
			Name:        d.Name,
			Type:        d.Type,
			Description: d.Description,
			Default:     d.Default,
			Required:    d.Required,
			Configured:  configuredKeys[d.Name],
		}
		if result[i].Configured {
			result[i].ConfiguredBy = "tofui"
		}
	}

	respond.JSON(w, http.StatusOK, result)
}

// renderedTerragrunt is the minimal subset of `terragrunt render --json`
// output that the Discover endpoint cares about.
type renderedTerragrunt struct {
	Terraform struct {
		Source string `json:"source"`
	} `json:"terraform"`
	Inputs map[string]any `json:"inputs"`
}

// discoverTerragrunt resolves the variable surface for a terragrunt
// workspace by shelling out to `terragrunt render --json` and parsing the
// underlying module's variables.tf. It returns the merged result, falling
// back to leaf-only parsing if render fails or the module source is
// remote.
func discoverTerragrunt(ctx context.Context, leafDir string, tofuiConfigured map[string]bool) []DiscoverVariableResponse {
	rendered, err := runTerragruntRender(ctx, leafDir)
	if err != nil {
		slog.Warn("terragrunt render failed; falling back to leaf-only discovery", "dir", leafDir, "error", err)
		return discoverTerragruntLeafOnly(leafDir)
	}

	modulePath := strings.TrimSpace(rendered.Terraform.Source)
	var moduleVars []tfparse.DiscoveredVariable
	if isLocalModuleSource(modulePath) {
		clean := filepath.Clean(modulePath)
		if mvs, parseErr := tfparse.ParseDirectory(clean); parseErr == nil {
			moduleVars = mvs
		} else {
			slog.Warn("failed to parse module variables.tf; surfacing inputs only",
				"module", clean, "error", parseErr)
		}
	}

	return mergeDiscovered(moduleVars, rendered.Inputs, tofuiConfigured)
}

func runTerragruntRender(ctx context.Context, leafDir string) (*renderedTerragrunt, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "terragrunt", "render", "--json", "--log-disable",
		"--non-interactive", "--working-dir", leafDir)
	cmd.Env = append(os.Environ(), "TG_NO_COLOR=1")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("terragrunt render: %w", err)
	}
	var r renderedTerragrunt
	if jerr := json.Unmarshal(out, &r); jerr != nil {
		return nil, fmt.Errorf("parse render output: %w", jerr)
	}
	return &r, nil
}

// isLocalModuleSource returns true when source is a filesystem path we can
// open and parse. Remote sources (git, https, terraform registry, etc.) are
// out of scope for in-process schema parsing.
func isLocalModuleSource(source string) bool {
	if source == "" {
		return false
	}
	for _, prefix := range []string{"git::", "github.com/", "bitbucket.org/", "hg::", "s3::", "gcs::", "http://", "https://", "tfr:"} {
		if strings.HasPrefix(source, prefix) {
			return false
		}
	}
	return filepath.IsAbs(source) || strings.HasPrefix(source, ".") || strings.HasPrefix(source, "/")
}

// discoverTerragruntLeafOnly is the safety net used when `terragrunt
// render` fails — surfaces only the literal inputs block from the leaf.
func discoverTerragruntLeafOnly(leafDir string) []DiscoverVariableResponse {
	leafInputs, err := tfparse.ParseTerragruntInputs(leafDir)
	if err != nil {
		return []DiscoverVariableResponse{}
	}
	result := make([]DiscoverVariableResponse, len(leafInputs))
	for i, d := range leafInputs {
		result[i] = DiscoverVariableResponse{
			Name:         d.Name,
			Default:      d.Default,
			Required:     false,
			Configured:   true,
			ConfiguredBy: "terragrunt",
			Description:  "from terragrunt.hcl inputs",
		}
	}
	return result
}

// mergeDiscovered combines the module's variable schema (from
// variables.tf) with terragrunt's resolved inputs and tofui's existing
// workspace_variables. Every module variable is returned with its source
// of truth recorded in ConfiguredBy: "terragrunt" when terragrunt's
// resolved inputs supply the value, "tofui" when a workspace_variable is
// set, or empty (and Configured=false) when no value exists anywhere.
//
// Resolved-input keys with no matching module variable are appended as
// extra entries (configured_by=terragrunt) so the user can still see
// what terragrunt is passing in.
func mergeDiscovered(moduleVars []tfparse.DiscoveredVariable, resolved map[string]any, tofuiConfigured map[string]bool) []DiscoverVariableResponse {
	seen := make(map[string]bool, len(moduleVars))
	out := make([]DiscoverVariableResponse, 0, len(moduleVars)+len(resolved))

	for _, v := range moduleVars {
		entry := DiscoverVariableResponse{
			Name:        v.Name,
			Type:        v.Type,
			Description: v.Description,
			Default:     v.Default,
			Required:    v.Required,
		}
		if val, ok := resolved[v.Name]; ok {
			entry.Configured = true
			entry.ConfiguredBy = "terragrunt"
			if def := formatHCL(val); def != "" {
				entry.Default = &def
			}
		} else if tofuiConfigured[v.Name] {
			entry.Configured = true
			entry.ConfiguredBy = "tofui"
		}
		seen[v.Name] = true
		out = append(out, entry)
	}

	// Surface inputs that don't match any module variable — rare but
	// useful for spotting drift between hcl and module signatures.
	for k, val := range resolved {
		if seen[k] {
			continue
		}
		def := formatHCL(val)
		out = append(out, DiscoverVariableResponse{
			Name:         k,
			Default:      &def,
			Configured:   true,
			ConfiguredBy: "terragrunt",
			Description:  "set in terragrunt.hcl (no matching module variable)",
		})
	}

	return out
}

// formatHCL returns an HCL-literal string representation of an arbitrary
// JSON value coming back from `terragrunt render --json`. Strings are
// quoted; booleans and numbers are bare; maps and lists are JSON-encoded
// (which is valid HCL for those types).
func formatHCL(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return fmt.Sprintf("%q", x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		// JSON numbers all come through as float64. Render integers
		// without a trailing decimal where possible.
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func extractDiscoverArchive(data []byte, destDir string) error {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		cleanName := filepath.Clean(hdr.Name)
		if strings.HasPrefix(cleanName, "..") {
			continue
		}
		target := filepath.Join(destDir, cleanName)
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			io.Copy(f, tr)
			f.Close()
		}
	}
	return nil
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

	// List source variables
	sourceVars, err := h.queries.ListWorkspaceVariables(r.Context(), repository.ListWorkspaceVariablesParams{
		WorkspaceID: req.SourceWorkspaceID, OrgID: userCtx.OrgID,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list source variables")
		return
	}

	if len(sourceVars) == 0 {
		respond.Error(w, http.StatusBadRequest, "source workspace has no variables")
		return
	}

	// List target's existing variables, build map keyed by key+category
	targetVars, err := h.queries.ListWorkspaceVariables(r.Context(), repository.ListWorkspaceVariablesParams{
		WorkspaceID: workspaceID, OrgID: userCtx.OrgID,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list target variables")
		return
	}

	existingByKey := make(map[string]repository.WorkspaceVariable, len(targetVars))
	for _, v := range targetVars {
		existingByKey[v.Key+"|"+v.Category] = v
	}

	ip, ua := auditContext(r)
	copied := make([]repository.WorkspaceVariable, 0, len(sourceVars))

	for _, sv := range sourceVars {
		compositeKey := sv.Key + "|" + sv.Category

		var v repository.WorkspaceVariable
		if existing, ok := existingByKey[compositeKey]; ok {
			// Update existing variable — copy encrypted value directly (same org key)
			v, err = h.queries.UpdateWorkspaceVariable(r.Context(), repository.UpdateWorkspaceVariableParams{
				ID: existing.ID, OrgID: userCtx.OrgID,
				Value: sv.Value, Sensitive: sv.Sensitive, Description: sv.Description,
			})
			if err != nil {
				respond.Error(w, http.StatusInternalServerError, "failed to update variable: "+sv.Key)
				return
			}
		} else {
			// Create new variable — copy encrypted value directly (same org key)
			v, err = h.queries.CreateWorkspaceVariable(r.Context(), repository.CreateWorkspaceVariableParams{
				ID:          ulid.Make().String(),
				WorkspaceID: workspaceID,
				OrgID:       userCtx.OrgID,
				Key:         sv.Key,
				Value:       sv.Value,
				Sensitive:   sv.Sensitive,
				Category:    sv.Category,
				Description: sv.Description,
			})
			if err != nil {
				respond.Error(w, http.StatusInternalServerError, "failed to create variable: "+sv.Key)
				return
			}
		}

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
