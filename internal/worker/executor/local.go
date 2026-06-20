package executor

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/nanohype/portal/internal/conv"
)

// LocalExecutor runs OpenTofu commands on the local machine (development only).
type LocalExecutor struct{}

func NewLocalExecutor() *LocalExecutor {
	return &LocalExecutor{}
}

var planSummaryRegex = regexp.MustCompile(`Plan: (\d+) to add, (\d+) to change, (\d+) to destroy`)

func (e *LocalExecutor) Execute(ctx context.Context, params ExecuteParams) (*ExecuteResult, error) {
	logger := slog.With("run_id", params.RunID, "operation", params.Operation)

	workDir, err := os.MkdirTemp("", fmt.Sprintf("portal-run-%s-", params.RunID))
	if err != nil {
		return nil, fmt.Errorf("failed to create work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	params.LogCallback([]byte(fmt.Sprintf("Preparing workspace for run %s...\r\n", params.RunID)))

	// Get source code: clone repo or extract uploaded archive
	if params.Source == "upload" {
		params.LogCallback([]byte("Extracting uploaded configuration...\r\n"))
		if err := extractArchive(params.ArchiveData, workDir); err != nil {
			params.LogCallback([]byte(fmt.Sprintf("\033[31mArchive extraction failed: %s\033[0m\r\n", err)))
			return nil, fmt.Errorf("archive extraction failed: %w", err)
		}
		params.LogCallback([]byte("Configuration extracted successfully.\r\n\r\n"))
	} else {
		params.LogCallback([]byte(fmt.Sprintf("Cloning %s (branch: %s)...\r\n", params.RepoURL, params.RepoBranch)))
		// `--branch=` binds the value even if it starts with `-`, and `--` ends
		// option parsing so a repo URL like `--upload-pack=...` is treated as a
		// positional, not a git option (argument-injection guard). The args are
		// already discrete argv (no shell), so there's no shell-injection here.
		cloneCmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch="+params.RepoBranch, "--", params.RepoURL, workDir)
		cloneOutput, err := cloneCmd.CombinedOutput()
		if err != nil {
			params.LogCallback([]byte(fmt.Sprintf("\033[31mGit clone failed: %s\033[0m\r\n", string(cloneOutput))))
			return nil, fmt.Errorf("git clone failed: %w", err)
		}
		params.LogCallback([]byte("Repository cloned successfully.\r\n\r\n"))
	}

	tfDir := filepath.Join(workDir, params.WorkingDir)

	// Detect whether this workspace is driven by terragrunt or tofu directly.
	// When terragrunt.hcl is present at the leaf, terragrunt walks parent dirs
	// and renders terraform at run time; otherwise tofu owns the run.
	binary := DetectBinary(tfDir)
	if binary == "terragrunt" {
		params.LogCallback([]byte("Detected terragrunt.hcl — using terragrunt wrapper.\r\n"))
		params.LogCallback([]byte("[portal] TG_NON_INTERACTIVE=true — terragrunt prompts auto-confirmed.\r\n"))
		params.LogCallback([]byte("[portal] TG_BACKEND_BOOTSTRAP=true — remote state bucket will be auto-created if missing.\r\n\r\n"))
	}

	// Restore previous state if available. Skipped in terragrunt mode —
	// terragrunt's state lives in the remote backend (S3/GCS/Azure), so a
	// local terraform.tfstate file is meaningless and can actively confuse
	// `tofu init` (e.g. when the cached blob was encrypted by a prior
	// portal state-encryption run that has since been disabled, init
	// prompts for migration input and fails under TF_INPUT=false).
	if len(params.PreviousState) > 0 && binary != "terragrunt" {
		statePath := filepath.Join(tfDir, "terraform.tfstate")
		if err := os.WriteFile(statePath, params.PreviousState, 0600); err != nil {
			return nil, fmt.Errorf("failed to restore state: %w", err)
		}
		params.LogCallback([]byte("Restored previous state file.\r\n"))
		logger.Info("restored previous state", "size", len(params.PreviousState))
	}

	// Write encryption override if state encryption is enabled. Skipped for
	// terragrunt — terragrunt's source-copy mechanism pulls the leaf's .tf
	// files into the rendered cache dir alongside the module source, so the
	// override would silently encrypt the user's remote state with portal's
	// derived passphrase. That breaks terragrunt `dependency` blocks (which
	// invoke `tofu output -json` in sibling workspaces without the override)
	// and conflates portal-managed encryption with the user's own backend
	// encryption setup (typically S3 SSE-KMS configured in root.hcl).
	if params.StateEncryptionPassphrase != "" && binary != "terragrunt" {
		overridePath := filepath.Join(tfDir, "portal_encryption_override.tf")
		content := GenerateEncryptionOverride(params.StateEncryptionPassphrase)
		if err := os.WriteFile(overridePath, []byte(content), 0600); err != nil {
			return nil, fmt.Errorf("failed to write encryption override: %w", err)
		}
		params.LogCallback([]byte("State encryption enabled (AES-GCM).\r\n"))
	}

	// Write variables file if any. Skipped for terragrunt — its `inputs = {}`
	// block is the source of truth and portal shouldn't interfere with it.
	if binary != "terragrunt" {
		if err := e.writeVariables(tfDir, params.Variables); err != nil {
			return nil, fmt.Errorf("failed to write variables: %w", err)
		}
	}

	// Build environment with env variables, filtering out portal-internal vars
	// that could interfere with provider SDKs (e.g. S3_ENDPOINT confusing the AWS SDK)
	var env []string
	for _, e := range os.Environ() {
		key := strings.SplitN(e, "=", 2)[0]
		switch key {
		case "S3_ENDPOINT", "S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_BUCKET", "S3_USE_SSL", "S3_REGION":
			continue // skip portal MinIO config
		default:
			env = append(env, e)
		}
	}
	env = append(env, "TF_IN_AUTOMATION=true", "TF_INPUT=false")

	// Terragrunt-specific defaults. Harmless for tofu runs (tofu ignores
	// TG_*-prefixed env vars).
	//   TG_NON_INTERACTIVE  — we're a worker, never interactive.
	//   TG_BACKEND_BOOTSTRAP — auto-create the remote state bucket on init
	//                          if it doesn't exist (no-op when it already
	//                          does). Without this, `terragrunt init`
	//                          fails on the first run when the bucket
	//                          defined in root.hcl's remote_state block
	//                          doesn't exist yet.
	env = append(env, "TG_NON_INTERACTIVE=true", "TG_BACKEND_BOOTSTRAP=true")

	// Use plugin cache to avoid re-downloading providers every run
	if os.Getenv("TF_PLUGIN_CACHE_DIR") == "" {
		cacheDir := filepath.Join(os.TempDir(), "portal-plugin-cache")
		os.MkdirAll(cacheDir, 0755)
		env = append(env, "TF_PLUGIN_CACHE_DIR="+cacheDir)
	}
	for _, v := range params.Variables {
		switch v.Category {
		case "env":
			env = append(env, fmt.Sprintf("%s=%s", v.Key, v.Value))
		case "terraform":
			// terraform-category vars always go in as TF_VAR_* env entries.
			// In tofu mode, portal.auto.tfvars (written by writeVariables()
			// above) takes precedence over TF_VAR_ — the env entries are
			// redundant but harmless. In terragrunt mode the file is not
			// written, so TF_VAR_ is the only source; terragrunt's own
			// `inputs = {}` block (passed as -var, highest precedence)
			// silently wins for any key it sets, and keys it doesn't set
			// get picked up from TF_VAR_ cleanly.
			env = append(env, fmt.Sprintf("TF_VAR_%s=%s", v.Key, v.Value))
		}
	}

	// init
	params.LogCallback([]byte(fmt.Sprintf("\033[1m$ %s init\033[0m\r\n", binary)))
	if err := e.runTool(ctx, binary, tfDir, []string{"init", "-no-color"}, env, params.LogCallback); err != nil {
		return nil, fmt.Errorf("%s init failed: %w", binary, err)
	}
	params.LogCallback([]byte("\r\n"))

	// validate
	params.LogCallback([]byte(fmt.Sprintf("\033[1m$ %s validate\033[0m\r\n", binary)))
	if err := e.runTool(ctx, binary, tfDir, []string{"validate", "-no-color"}, env, params.LogCallback); err != nil {
		return nil, fmt.Errorf("%s validate failed: %w", binary, err)
	}
	params.LogCallback([]byte("\r\n"))

	// Execute operation
	result := &ExecuteResult{}
	var tfArgs []string

	switch params.Operation {
	case "test":
		// Export outputs to JSON for smoke-test.sh
		params.LogCallback([]byte(fmt.Sprintf("\033[1m$ %s output -json\033[0m\r\n", binary)))
		outputCmd := exec.CommandContext(ctx, binary, "output", "-json")
		outputCmd.Dir = tfDir
		outputCmd.Env = env
		outputJSON, outputErr := outputCmd.Output()
		if outputErr != nil {
			params.LogCallback([]byte(fmt.Sprintf("\033[33mWarning: %s output failed: %s (continuing anyway)\033[0m\r\n", binary, outputErr)))
		} else {
			outputsPath := filepath.Join(tfDir, "outputs.json")
			if err := os.WriteFile(outputsPath, outputJSON, 0600); err != nil {
				return nil, fmt.Errorf("failed to write outputs.json: %w", err)
			}
			params.LogCallback([]byte("Outputs written to outputs.json\r\n"))
		}
		params.LogCallback([]byte("\r\n"))

		// Execute smoke-test.sh
		smokeTestPath := filepath.Join(tfDir, "smoke-test.sh")
		if _, err := os.Stat(smokeTestPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("smoke-test.sh not found in working directory")
		}
		// Ensure executable
		os.Chmod(smokeTestPath, 0755)

		params.LogCallback([]byte("\033[1m$ ./smoke-test.sh\033[0m\r\n"))

		testCmd := exec.CommandContext(ctx, "/bin/sh", smokeTestPath)
		testCmd.Dir = tfDir
		testCmd.Env = env

		testStdout, err := testCmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
		}
		testCmd.Stderr = testCmd.Stdout

		if err := testCmd.Start(); err != nil {
			return nil, fmt.Errorf("failed to start smoke-test.sh: %w", err)
		}

		var testOutputBuf strings.Builder
		scanner := bufio.NewScanner(testStdout)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			testOutputBuf.WriteString(line)
			testOutputBuf.WriteString("\n")
			params.LogCallback([]byte(line + "\r\n"))
		}

		if err := testCmd.Wait(); err != nil {
			result.Output = testOutputBuf.String()
			return result, fmt.Errorf("smoke-test.sh failed: %w", err)
		}

		result.Output = testOutputBuf.String()
		params.LogCallback([]byte("\r\n\033[32mSmoke test passed.\033[0m\r\n"))
		return result, nil

	case "import":
		params.LogCallback([]byte(fmt.Sprintf("\033[1m$ %s import\033[0m\r\n", binary)))
		for _, res := range params.ImportResources {
			params.LogCallback([]byte(fmt.Sprintf("Importing %s = %s...\r\n", res.Address, res.ID)))
			importArgs := []string{"import", "-no-color"}
			if e.hasVarFile(tfDir) {
				importArgs = append(importArgs, "-var-file=portal.auto.tfvars")
			}
			importArgs = append(importArgs, res.Address, res.ID)
			if err := e.runTool(ctx, binary, tfDir, importArgs, env, params.LogCallback); err != nil {
				params.LogCallback([]byte(fmt.Sprintf("\033[31mImport failed for %s: %s\033[0m\r\n", res.Address, err)))
				return nil, fmt.Errorf("%s import failed for %s: %w", binary, res.Address, err)
			}
			params.LogCallback([]byte(fmt.Sprintf("\033[32mImported %s\033[0m\r\n", res.Address)))
		}
		params.LogCallback([]byte(fmt.Sprintf("\r\n\033[32mSuccessfully imported %d resource(s)\033[0m\r\n", len(params.ImportResources))))

		// Capture state after import
		statePath := filepath.Join(tfDir, "terraform.tfstate")
		if stateData, err := os.ReadFile(statePath); err == nil && len(stateData) > 0 {
			result.StateFile = stateData
		}
		pullCmd := exec.CommandContext(ctx, binary, "state", "pull")
		pullCmd.Dir = tfDir
		pullCmd.Env = env
		if jsonData, err := pullCmd.Output(); err == nil && len(jsonData) > 0 {
			result.StateJSON = jsonData
		}
		return result, nil

	case "plan":
		tfArgs = []string{"plan", "-no-color", "-detailed-exitcode", "-out=planfile"}
		if e.hasVarFile(tfDir) {
			tfArgs = append(tfArgs, "-var-file=portal.auto.tfvars")
		}
		params.LogCallback([]byte(fmt.Sprintf("\033[1m$ %s plan\033[0m\r\n", binary)))
	case "apply":
		tfArgs = []string{"apply", "-no-color", "-auto-approve"}
		if e.hasVarFile(tfDir) {
			tfArgs = append(tfArgs, "-var-file=portal.auto.tfvars")
		}
		params.LogCallback([]byte(fmt.Sprintf("\033[1m$ %s apply\033[0m\r\n", binary)))
	case "destroy":
		tfArgs = []string{"destroy", "-no-color", "-auto-approve"}
		if e.hasVarFile(tfDir) {
			tfArgs = append(tfArgs, "-var-file=portal.auto.tfvars")
		}
		params.LogCallback([]byte(fmt.Sprintf("\033[1m$ %s destroy\033[0m\r\n", binary)))
	default:
		return nil, fmt.Errorf("unknown operation: %s", params.Operation)
	}

	output, err := e.runToolCapture(ctx, binary, tfDir, tfArgs, env, params.LogCallback)
	if err != nil {
		if params.Operation == "plan" && strings.Contains(err.Error(), "exit status 2") {
			logger.Info("plan detected changes")
		} else {
			// For apply/destroy, capture partial state before returning the error
			// so resources created before the failure are tracked
			if params.Operation == "apply" || params.Operation == "destroy" {
				result.Output = output
				statePath := filepath.Join(tfDir, "terraform.tfstate")
				if stateData, readErr := os.ReadFile(statePath); readErr == nil && len(stateData) > 0 {
					result.StateFile = stateData
					logger.Info("captured partial state from failed apply", "size", len(stateData))
				}
				pullCmd := exec.CommandContext(ctx, binary, "state", "pull")
				pullCmd.Dir = tfDir
				pullCmd.Env = env
				if jsonData, pullErr := pullCmd.Output(); pullErr == nil && len(jsonData) > 0 {
					result.StateJSON = jsonData
				}
				return result, fmt.Errorf("%s %s failed: %w", binary, params.Operation, err)
			}
			return nil, fmt.Errorf("%s %s failed: %w", binary, params.Operation, err)
		}
	}

	result.Output = output

	// Generate JSON plan from planfile (plan operation only)
	if params.Operation == "plan" {
		planfilePath := filepath.Join(tfDir, "planfile")
		if _, statErr := os.Stat(planfilePath); statErr == nil {
			jsonCmd := exec.CommandContext(ctx, binary, "show", "-json", "planfile")
			jsonCmd.Dir = tfDir
			jsonCmd.Env = env
			if jsonOut, jsonErr := jsonCmd.Output(); jsonErr == nil {
				result.PlanJSON = jsonOut
			} else {
				logger.Warn("failed to generate JSON plan", "error", jsonErr)
			}
		}
	}

	// Parse plan summary
	matches := planSummaryRegex.FindStringSubmatch(output)
	if len(matches) == 4 {
		added, _ := strconv.Atoi(matches[1])
		changed, _ := strconv.Atoi(matches[2])
		deleted, _ := strconv.Atoi(matches[3])
		result.ResourcesAdded = conv.Int32(added)
		result.ResourcesChanged = conv.Int32(changed)
		result.ResourcesDeleted = conv.Int32(deleted)
	}

	// Capture state after apply/destroy
	if params.Operation == "apply" || params.Operation == "destroy" {
		// Raw state file (may be encrypted) — used for restoration on next run
		statePath := filepath.Join(tfDir, "terraform.tfstate")
		if stateData, err := os.ReadFile(statePath); err == nil && len(stateData) > 0 {
			result.StateFile = stateData
			logger.Info("captured state file", "size", len(stateData))
		}

		// Decrypted state via "state pull" — used for resource browsing
		pullCmd := exec.CommandContext(ctx, binary, "state", "pull")
		pullCmd.Dir = tfDir
		pullCmd.Env = env
		if jsonData, err := pullCmd.Output(); err == nil && len(jsonData) > 0 {
			result.StateJSON = jsonData
		}
	}

	return result, nil
}

// isHCLLiteral returns true if the value looks like an HCL literal
// (map, list, number, bool) that should not be quoted in tfvars.
func isHCLLiteral(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	// Maps and objects: { ... }
	if strings.HasPrefix(v, "{") && strings.HasSuffix(v, "}") {
		return true
	}
	// Lists and tuples: [ ... ]
	if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		return true
	}
	// Booleans
	if v == "true" || v == "false" {
		return true
	}
	// Numbers — must consume the entire string
	var f float64
	var trailing string
	if n, _ := fmt.Sscanf(v, "%f%s", &f, &trailing); n == 1 {
		return true
	}
	return false
}

func (e *LocalExecutor) writeVariables(tfDir string, vars []Variable) error {
	var tfVars []string
	for _, v := range vars {
		if v.Category == "terraform" {
			if isHCLLiteral(v.Value) {
				tfVars = append(tfVars, fmt.Sprintf("%s = %s", v.Key, v.Value))
			} else {
				tfVars = append(tfVars, fmt.Sprintf("%s = %q", v.Key, v.Value))
			}
		}
	}
	if len(tfVars) == 0 {
		return nil
	}
	content := strings.Join(tfVars, "\n") + "\n"
	return os.WriteFile(filepath.Join(tfDir, "portal.auto.tfvars"), []byte(content), 0600)
}

func (e *LocalExecutor) hasVarFile(tfDir string) bool {
	_, err := os.Stat(filepath.Join(tfDir, "portal.auto.tfvars"))
	return err == nil
}

func (e *LocalExecutor) runTool(ctx context.Context, binary, dir string, args, env []string, logCallback func([]byte)) error {
	_, err := e.runToolCapture(ctx, binary, dir, args, env, logCallback)
	return err
}

func extractArchive(data []byte, destDir string) error {
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
			return fmt.Errorf("reading tar: %w", err)
		}

		// Prevent path traversal (zip-slip): the resolved target must stay
		// inside destDir. filepath.Join cleans the result, so requiring it to
		// be destDir or a child of it rejects both `../` escapes and absolute
		// entry names.
		target := filepath.Join(destDir, hdr.Name)
		if target != destDir && !strings.HasPrefix(target, destDir+string(os.PathSeparator)) {
			return fmt.Errorf("invalid path in archive: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

func (e *LocalExecutor) runToolCapture(ctx context.Context, binary, dir string, args, env []string, logCallback func([]byte)) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return "", err
	}

	var output strings.Builder
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		output.WriteString(line)
		output.WriteString("\n")
		logCallback([]byte(line + "\r\n"))
	}

	if scanner.Err() != nil {
		remaining, _ := io.ReadAll(stdout)
		if len(remaining) > 0 {
			output.Write(remaining)
			logCallback(remaining)
		}
	}

	err = cmd.Wait()
	return output.String(), err
}
