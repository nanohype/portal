package executor

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Keys the run script reads back off the mounted volume. They are the same
// filenames whichever object carries them — the split below is about the
// Kubernetes API surface, not about what the container sees.
const (
	scriptKey     = "run.sh"
	archiveKey    = "source.tar.gz"
	tfvarsKey     = "portal.auto.tfvars"
	overrideKey   = "portal_encryption_override.tf"
	prevStateKey  = "terraform.tfstate"
	varKeyPattern = "var-%d"
)

// mountedSecretKeys are the Secret entries that have to exist as files for the
// run script to find them. Everything else in the Secret — the variable values —
// is reached through an env reference and never touches the pod's filesystem.
var mountedSecretKeys = []string{tfvarsKey, overrideKey, prevStateKey}

// runPayload is everything a run needs staged into /config, already split by
// whether the apiserver should be treating it as a secret.
//
// The split is not about the bytes inside the pod. Both objects land on the same
// projected volume at the same path and the container reads them identically.
// What changes is who can ask the apiserver for them: `get configmaps` is a verb
// handed out freely — it is how most operators read application config — while
// `get secrets` is the one cluster admins actually guard, and a Secret is what an
// encryption-at-rest provider covers in etcd.
//
// What moves, and why each is not config:
//
//   - portal.auto.tfvars — every terraform-category variable in cleartext,
//     including the ones the database holds AES-GCM encrypted and the API only
//     releases behind ActionRevealSecret. The worker decrypts them to build this.
//   - portal_encryption_override.tf — the workspace's derived state-encryption
//     passphrase, as an HCL string literal.
//   - terraform.tfstate — the prior state, restored onto the next run, carrying
//     whatever the last apply wrote into it.
//   - the variable values themselves — see below.
//
// run.sh stays public: it references the repo URL, branch and working directory
// as shell variables and interpolates no value of its own. source.tar.gz stays
// public too — it is the user's own configuration tree, already held at the
// config-version bar in object storage.
type runPayload struct {
	public    map[string]string
	archive   []byte
	sensitive map[string][]byte
	env       []corev1.EnvVar
}

// buildRunPayload sorts a run's inputs into the two objects. secretName is the
// Secret the env references resolve against; it is the run's deterministic name,
// the same one the ConfigMap and pod carry.
func buildRunPayload(script, secretName string, params ExecuteParams) runPayload {
	p := runPayload{
		public:    map[string]string{scriptKey: script},
		sensitive: map[string][]byte{},
		env: []corev1.EnvVar{
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
		},
	}

	var tfVarLines []string

	for i, v := range params.Variables {
		var name string
		switch v.Category {
		case "env":
			name = v.Key
		case "terraform":
			// Mirror the local executor: terraform vars are always passed as
			// TF_VAR_* env so they flow into both tofu mode (redundant with
			// portal.auto.tfvars; the file wins) and terragrunt mode (the only
			// source — and it OVERRIDES the leaf's `inputs`, per the precedence
			// note in local.go).
			name = "TF_VAR_" + v.Key
			if isHCLLiteral(v.Value) {
				tfVarLines = append(tfVarLines, fmt.Sprintf("%s = %s", v.Key, v.Value))
			} else {
				tfVarLines = append(tfVarLines, fmt.Sprintf("%s = %q", v.Key, v.Value))
			}
		default:
			continue
		}

		// A variable's VALUE never appears in the pod spec. A spec is readable by
		// anyone holding `get pods` on the namespace and `kubectl describe pod`
		// prints it in full, so the value is staged in the Secret and the
		// container reaches it through a reference.
		//
		// The Secret key is positional rather than the variable's own name. The
		// name is user input and a Secret key has a character set: a workspace
		// variable called `a/b` would make the Secret itself invalid and fail
		// every run on that workspace. The env var name is still the user's —
		// only the key portal files it under is portal's.
		key := fmt.Sprintf(varKeyPattern, i)
		p.sensitive[key] = []byte(v.Value)
		p.env = append(p.env, corev1.EnvVar{
			Name: name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  key,
				},
			},
		})
	}

	if len(tfVarLines) > 0 {
		p.sensitive[tfvarsKey] = []byte(strings.Join(tfVarLines, "\n") + "\n")
	}
	if params.StateEncryptionPassphrase != "" {
		p.sensitive[overrideKey] = []byte(GenerateEncryptionOverride(params.StateEncryptionPassphrase))
	}
	if len(params.PreviousState) > 0 {
		p.sensitive[prevStateKey] = params.PreviousState
	}
	if params.Source == "upload" && len(params.ArchiveData) > 0 {
		p.archive = params.ArchiveData
	}

	return p
}

// volume renders the projected volume the run mounts at /config.
//
// Projected rather than two mounts so the run script's paths stay one directory:
// it reads /config/run.sh and /config/portal.auto.tfvars without caring which
// object each arrived in. Only the file-shaped Secret keys are projected —
// listing them explicitly is what keeps the variable values out of the pod's
// filesystem, since a SecretProjection with no Items writes every key it holds.
func (p runPayload) volume(name string) corev1.Volume {
	sources := []corev1.VolumeProjection{{
		ConfigMap: &corev1.ConfigMapProjection{
			LocalObjectReference: corev1.LocalObjectReference{Name: name},
		},
	}}

	var items []corev1.KeyToPath
	for _, k := range mountedSecretKeys {
		if _, ok := p.sensitive[k]; ok {
			items = append(items, corev1.KeyToPath{Key: k, Path: k})
		}
	}
	if len(items) > 0 {
		sources = append(sources, corev1.VolumeProjection{
			Secret: &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: name},
				Items:                items,
			},
		})
	}

	return corev1.Volume{
		Name: "config",
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources:     sources,
				DefaultMode: int32Ptr(0755),
			},
		},
	}
}
