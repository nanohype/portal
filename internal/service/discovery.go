package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/storage"
	"github.com/nanohype/portal/internal/tfparse"
)

// DiscoveryService resolves the variable surface of a workspace's terraform /
// terragrunt config for the Discover endpoint. It acquires the config — an S3
// upload archive or an in-process git clone — into a temp dir, parses it, and
// cross-references portal's existing workspace variables. It stays synchronous
// and request-scoped: the UI consumes the result inline (no job), so this is a
// service method, not a River worker.
type DiscoveryService struct {
	queries *repository.Queries
	storage *storage.S3Storage
}

func NewDiscoveryService(queries *repository.Queries, store *storage.S3Storage) *DiscoveryService {
	return &DiscoveryService{queries: queries, storage: store}
}

// DiscoverVariableResponse is the wire shape the Discover endpoint returns —
// one row per variable the workspace's config exposes, annotated with whether
// portal or terragrunt already supplies a value.
type DiscoverVariableResponse struct {
	Name         string  `json:"name"`
	Type         string  `json:"type,omitempty"`
	Description  string  `json:"description,omitempty"`
	Default      *string `json:"default,omitempty"`
	Required     bool    `json:"required"`
	Configured   bool    `json:"configured"`
	ConfiguredBy string  `json:"configured_by,omitempty"` // "terragrunt" | "portal"
}

// DiscoverVariables acquires the workspace's config and returns its variable
// surface. Bad-input cases (no upload, no repo URL) come back as
// apperr.Validation so the handler maps them to 400; acquisition/parse failures
// are plain errors (→ 500, logged). The temp dir is cleaned up before return.
func (s *DiscoveryService) DiscoverVariables(ctx context.Context, ws repository.Workspace, orgID string) ([]DiscoverVariableResponse, error) {
	tmpDir, err := os.MkdirTemp("", "portal-discover-*")
	if err != nil {
		return nil, fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if ws.Source == "upload" {
		if ws.CurrentConfigVersionID == "" {
			return nil, apperr.Validation("no configuration uploaded yet")
		}
		if s.storage == nil {
			return nil, fmt.Errorf("storage not configured")
		}
		key := fmt.Sprintf("configs/%s/%s.tar.gz", ws.ID, ws.CurrentConfigVersionID)
		data, err := s.storage.GetConfigArchive(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("download configuration: %w", err)
		}
		if err := extractDiscoverArchive(data, tmpDir); err != nil {
			return nil, fmt.Errorf("extract configuration: %w", err)
		}
	} else {
		if ws.RepoURL == "" {
			return nil, apperr.Validation("workspace has no repository URL configured")
		}
		if err := shallowClone(ctx, ws.RepoURL, ws.RepoBranch, tmpDir); err != nil {
			slog.Error("discover clone failed", "error", err, "repo", ws.RepoURL)
			return nil, fmt.Errorf("clone repository: %w", err)
		}
	}

	parseDir := tmpDir
	if ws.WorkingDir != "" && ws.WorkingDir != "." {
		parseDir = filepath.Join(tmpDir, ws.WorkingDir)
	}

	// Load existing portal-managed workspace variables for cross-reference.
	existing, err := s.queries.ListWorkspaceVariables(ctx, repository.ListWorkspaceVariablesParams{
		WorkspaceID: ws.ID, OrgID: orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("list existing variables: %w", err)
	}
	configuredKeys := make(map[string]bool, len(existing))
	for _, v := range existing {
		configuredKeys[v.Key] = true
	}

	// Terragrunt-driven workspaces: shell out to `terragrunt render --json` to
	// get the resolved inputs (merged from leaf + includes + _envcommon) and the
	// underlying module path, then parse that module's variables.tf and merge.
	if _, statErr := os.Stat(filepath.Join(parseDir, "terragrunt.hcl")); statErr == nil {
		return discoverTerragrunt(ctx, parseDir, configuredKeys), nil
	}

	discovered, err := tfparse.ParseDirectory(parseDir)
	if err != nil {
		return nil, fmt.Errorf("parse terraform files: %w", err)
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
			result[i].ConfiguredBy = "portal"
		}
	}
	return result, nil
}

// shallowClone does an in-process depth-1 single-branch clone via go-git, so
// the API server needs no `git` binary on PATH (the deployed image ships
// without one). Auth is left unset: this covers public repos and HTTPS URLs
// that carry their own credentials. A private SSH repo with no embedded creds
// is out of scope — the lean server image carries no keys, the same constraint
// the previous exec-based clone had.
func shallowClone(ctx context.Context, url, branch, dir string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	opts := &gogit.CloneOptions{URL: url, Depth: 1, SingleBranch: true}
	if branch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(branch)
	}
	if _, err := gogit.PlainCloneContext(cctx, dir, false, opts); err != nil {
		return fmt.Errorf("clone %s: %w", url, err)
	}
	return nil
}

// renderedTerragrunt is the minimal subset of `terragrunt render --json` output
// that Discover cares about.
type renderedTerragrunt struct {
	Terraform struct {
		Source string `json:"source"`
	} `json:"terraform"`
	Inputs map[string]any `json:"inputs"`
}

// discoverTerragrunt resolves the variable surface for a terragrunt workspace by
// shelling out to `terragrunt render --json` and parsing the underlying
// module's variables.tf. It falls back to leaf-only parsing if render fails or
// the module source is remote.
func discoverTerragrunt(ctx context.Context, leafDir string, portalConfigured map[string]bool) []DiscoverVariableResponse {
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

	return mergeDiscovered(moduleVars, rendered.Inputs, portalConfigured)
}

// runTerragruntRender shells out to the terragrunt binary. When the binary is
// absent (e.g. the lean API-server image) this errors and discoverTerragrunt
// degrades to leaf-only discovery rather than failing the request.
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

// isLocalModuleSource returns true when source is a filesystem path we can open
// and parse. Remote sources (git, https, terraform registry, etc.) are out of
// scope for in-process schema parsing.
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

// discoverTerragruntLeafOnly is the safety net used when `terragrunt render`
// fails — surfaces only the literal inputs block from the leaf.
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

// mergeDiscovered combines the module's variable schema (from variables.tf) with
// terragrunt's resolved inputs and portal's existing workspace_variables. Every
// module variable is returned with its source of truth recorded in
// ConfiguredBy: "terragrunt" when terragrunt's resolved inputs supply the value,
// "portal" when a workspace_variable is set, or empty (Configured=false) when no
// value exists anywhere. Resolved-input keys with no matching module variable
// are appended (configured_by=terragrunt) so the user can still see them.
func mergeDiscovered(moduleVars []tfparse.DiscoveredVariable, resolved map[string]any, portalConfigured map[string]bool) []DiscoverVariableResponse {
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
		} else if portalConfigured[v.Name] {
			entry.Configured = true
			entry.ConfiguredBy = "portal"
		}
		seen[v.Name] = true
		out = append(out, entry)
	}

	// Surface inputs that don't match any module variable — rare but useful for
	// spotting drift between hcl and module signatures.
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

// formatHCL returns an HCL-literal string representation of an arbitrary JSON
// value coming back from `terragrunt render --json`. Strings are quoted;
// booleans and numbers are bare; maps and lists are JSON-encoded (valid HCL for
// those types).
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
		// JSON numbers all come through as float64. Render integers without a
		// trailing decimal where possible.
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

// extractDiscoverArchive unpacks a gzipped tar of a config upload into destDir,
// rejecting entries that would escape it.
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
		// Zip-slip guard: skip any entry whose resolved path escapes destDir
		// (filepath.Join cleans, so this catches `../` escapes and absolute names).
		// filepath.IsLocal rejects absolute names and `../` escapes up front and
		// is recognized as a zip-slip sanitizer by static analysis; the cleaned
		// HasPrefix check stays as defense in depth.
		if !filepath.IsLocal(hdr.Name) {
			continue
		}
		target := filepath.Join(destDir, hdr.Name)
		if target != destDir && !strings.HasPrefix(target, destDir+string(os.PathSeparator)) {
			continue
		}
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
