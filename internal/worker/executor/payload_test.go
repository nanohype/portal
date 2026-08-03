package executor

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// A run whose every sensitive input is set to something findable.
func sensitiveParams() ExecuteParams {
	return ExecuteParams{
		RunID:      "01JRUN",
		Operation:  "apply",
		RepoURL:    "https://github.com/acme/infra",
		RepoBranch: "main",
		WorkingDir: "live/prod",
		Variables: []Variable{
			{Key: "db_password", Value: "tfvar-secret-8f21", Category: "terraform"},
			{Key: "AWS_SECRET_ACCESS_KEY", Value: "env-secret-3c07", Category: "env"},
			{Key: "replicas", Value: "3", Category: "terraform"},
		},
		PreviousState:             []byte(`{"resources":[{"pw":"prior-state-secret-b45e"}]}`),
		StateEncryptionPassphrase: "passphrase-secret-91ad",
	}
}

// Every value the run above should never expose through a ConfigMap or a pod spec.
func sensitiveValues() []string {
	return []string{
		"tfvar-secret-8f21",
		"env-secret-3c07",
		"prior-state-secret-b45e",
		"passphrase-secret-91ad",
	}
}

// Nothing sensitive may land in the ConfigMap.
//
// `get configmaps` is a verb handed out freely — it is how most operators read
// application config — while `get secrets` is the one cluster admins guard, and a
// Secret is what an encryption-at-rest provider covers in etcd. Staging the
// decrypted tfvars, the state-encryption passphrase and the prior state as
// ConfigMap keys puts material the API only releases behind ActionRevealSecret
// behind the namespace's most freely granted read verb instead.
func TestSensitiveMaterialNeverLandsInTheConfigMap(t *testing.T) {
	params := sensitiveParams()
	p := buildRunPayload("#!/bin/sh\ntrue\n", "portal-run-01JRUN", params)

	var public strings.Builder
	for k, v := range p.public {
		public.WriteString(k)
		public.WriteString("\x00")
		public.WriteString(v)
		public.WriteString("\x00")
	}
	public.Write(p.archive)

	for _, secret := range sensitiveValues() {
		if strings.Contains(public.String(), secret) {
			t.Errorf("%q is readable by anyone holding `get configmaps` on the executor namespace", secret)
		}
	}

	// The ConfigMap still has to carry the script, or the pod has nothing to run.
	if !strings.Contains(p.public[scriptKey], "#!/bin/sh") {
		t.Errorf("the run script is missing from the ConfigMap: %q", p.public)
	}

	// ...and the Secret has to carry each file the script reads back, or the run
	// silently loses its variables, its encryption and its prior state.
	for _, k := range mountedSecretKeys {
		if len(p.sensitive[k]) == 0 {
			t.Errorf("%s is not in the Secret — the run would proceed without it", k)
		}
	}
	for _, secret := range sensitiveValues() {
		found := false
		for _, v := range p.sensitive {
			if strings.Contains(string(v), secret) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q reached neither object — the run would not see it at all", secret)
		}
	}
}

// A variable's value must not appear in the pod spec.
//
// A spec is readable by anyone holding `get pods` on the namespace and
// `kubectl describe pod` prints every env var in full, so an inline value is
// disclosed to a strictly wider audience than the Secret it came from.
func TestVariableValuesAreNotInThePodSpec(t *testing.T) {
	params := sensitiveParams()
	p := buildRunPayload("#!/bin/sh\ntrue\n", "portal-run-01JRUN", params)

	byName := map[string]corev1.EnvVar{}
	for _, e := range p.env {
		byName[e.Name] = e
		for _, secret := range sensitiveValues() {
			if strings.Contains(e.Value, secret) {
				t.Errorf("env %s carries %q inline in the pod spec", e.Name, secret)
			}
		}
	}

	// Each variable still reaches the container, by reference.
	for _, want := range []string{"TF_VAR_db_password", "AWS_SECRET_ACCESS_KEY", "TF_VAR_replicas"} {
		e, ok := byName[want]
		if !ok {
			t.Errorf("%s never reaches the container", want)
			continue
		}
		if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
			t.Errorf("%s is not a Secret reference: %+v", want, e)
			continue
		}
		ref := e.ValueFrom.SecretKeyRef
		if ref.Name != "portal-run-01JRUN" {
			t.Errorf("%s references Secret %q, not the run's own", want, ref.Name)
		}
		if len(p.sensitive[ref.Key]) == 0 {
			t.Errorf("%s references key %q, which the Secret does not hold — the pod would fail to start", want, ref.Key)
		}
	}

	// The fixed env stays inline: none of it is a value the API withholds, and
	// the script needs the repo coordinates to be plain shell variables.
	if byName["PORTAL_REPO_URL"].Value != params.RepoURL {
		t.Errorf("PORTAL_REPO_URL should stay an inline value, got %+v", byName["PORTAL_REPO_URL"])
	}
}

// A variable key that is not a legal Secret key must not be able to break a run.
//
// Variable names are user input; Secret keys have a character set. Filing a value
// under the variable's own name would let a workspace variable called `a/b` make
// the Secret itself invalid, and every run on that workspace would fail at
// create with an apiserver validation error rather than anything a user could
// act on.
func TestAVariableNameCannotMakeTheSecretInvalid(t *testing.T) {
	p := buildRunPayload("", "portal-run-01JRUN", ExecuteParams{
		Variables: []Variable{
			{Key: "a/b", Value: "one", Category: "env"},
			{Key: "with space", Value: "two", Category: "terraform"},
			{Key: "ok_name", Value: "three", Category: "env"},
		},
	})

	legal := func(s string) bool {
		if s == "" {
			return false
		}
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case r == '-', r == '_', r == '.':
			default:
				return false
			}
		}
		return true
	}

	for k := range p.sensitive {
		if !legal(k) {
			t.Errorf("Secret key %q is not a legal key — the apiserver would reject the whole object", k)
		}
	}

	// Each variable still arrives, under whatever key portal chose for it.
	if len(p.env) < 3 {
		t.Fatalf("expected the three variables to reach the container, got env %+v", p.env)
	}
	seen := map[string]string{}
	for _, e := range p.env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			seen[e.Name] = string(p.sensitive[e.ValueFrom.SecretKeyRef.Key])
		}
	}
	for name, want := range map[string]string{"a/b": "one", "TF_VAR_with space": "two", "ok_name": "three"} {
		if seen[name] != want {
			t.Errorf("env %q resolves to %q, want %q", name, seen[name], want)
		}
	}
}

// Only the file-shaped Secret keys are mounted.
//
// A SecretProjection with no Items writes every key it holds, which would put
// each variable's value on the pod's filesystem as a side effect of storing it —
// reachable by anything that later runs in that container, and by an archive
// step or a `tofu` provider that walks the directory it is handed.
func TestOnlyTheFileShapedSecretKeysAreMounted(t *testing.T) {
	p := buildRunPayload("#!/bin/sh\ntrue\n", "portal-run-01JRUN", sensitiveParams())
	vol := p.volume("portal-run-01JRUN")

	if vol.Projected == nil {
		t.Fatalf("expected a projected volume, got %+v", vol.VolumeSource)
	}

	var secretSource *corev1.SecretProjection
	for _, s := range vol.Projected.Sources {
		if s.Secret != nil {
			secretSource = s.Secret
		}
	}
	if secretSource == nil {
		t.Fatalf("the Secret is not projected onto the volume — the run script would not find its tfvars")
	}
	if len(secretSource.Items) == 0 {
		t.Fatalf("the Secret is projected with no Items, so every key it holds becomes a file — including each variable's value")
	}

	mounted := map[string]bool{}
	for _, it := range secretSource.Items {
		mounted[it.Key] = true
	}
	for k := range p.sensitive {
		if strings.HasPrefix(k, "var-") && mounted[k] {
			t.Errorf("variable value %s is written to the pod filesystem; it should only be an env reference", k)
		}
	}
	for _, k := range mountedSecretKeys {
		if !mounted[k] {
			t.Errorf("%s is not mounted, so the run script cannot read it", k)
		}
	}

	// The script has to arrive on the same path, or the container's command
	// (/bin/sh /config/run.sh) has nothing to execute.
	var hasConfigMap bool
	for _, s := range vol.Projected.Sources {
		if s.ConfigMap != nil {
			hasConfigMap = true
		}
	}
	if !hasConfigMap {
		t.Errorf("the ConfigMap is not projected onto the volume — the pod has no run.sh")
	}
}

// A run with nothing sensitive creates no Secret to leave behind.
func TestARunWithNoSensitiveInputsNeedsNoSecret(t *testing.T) {
	p := buildRunPayload("#!/bin/sh\ntrue\n", "portal-run-01JRUN", ExecuteParams{
		RunID:     "01JRUN",
		Operation: "plan",
	})

	if len(p.sensitive) != 0 {
		t.Errorf("expected no Secret contents, got %v", p.sensitive)
	}
	vol := p.volume("portal-run-01JRUN")
	for _, s := range vol.Projected.Sources {
		if s.Secret != nil {
			t.Errorf("a Secret is projected for a run that has none, so the pod would block on a missing object")
		}
	}
}
