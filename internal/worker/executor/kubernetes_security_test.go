package executor

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The executor pod runs tofu against a user-supplied config, where a local-exec
// provisioner is arbitrary code by design — it is the least trusted thing portal
// starts. These assertions are the Pod Security Admission "restricted" profile,
// pinned so the posture cannot be dropped without a test failing. Portal's own
// server and worker already meet this bar in the chart; the point is that the
// pod running untrusted config is not the loosest thing in the deployment.
func TestBuildPod_MeetsTheRestrictedProfile(t *testing.T) {
	e := &KubernetesExecutor{namespace: "portal"}
	pod := e.buildPod("run-abc", ExecuteParams{RunID: "r1", WorkspaceID: "w1", Operation: "plan"}, runPayload{})

	psc := pod.Spec.SecurityContext
	if psc == nil {
		t.Fatal("pod has no securityContext; PSA restricted would reject it at admission")
	}
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Error("runAsNonRoot must be true")
	}
	if psc.RunAsUser == nil || *psc.RunAsUser == 0 {
		t.Error("runAsUser must be set to a non-root uid")
	}
	// emptyDir volumes are root-owned; without fsGroup a non-root container
	// cannot write to /work and every run fails on a permission error.
	if psc.FSGroup == nil || *psc.FSGroup == 0 {
		t.Error("fsGroup must be set, or the non-root container cannot write to the work volume")
	}
	if psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("seccompProfile must be RuntimeDefault")
	}

	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("expected exactly one container, got %d", len(pod.Spec.Containers))
	}
	c := pod.Spec.Containers[0]
	sc := c.SecurityContext
	if sc == nil {
		t.Fatal("container has no securityContext")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("allowPrivilegeEscalation must be false")
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("readOnlyRootFilesystem must be true")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("capabilities must drop ALL, got %+v", sc.Capabilities)
	}

	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("automountServiceAccountToken must stay false")
	}
}

// A read-only root filesystem is only survivable if every path the run writes to
// is a volume. These two are what tofu and git actually need beyond the working
// tree, and getting this wrong fails at run time rather than at admission.
func TestBuildPod_ReadOnlyRootHasWritablePaths(t *testing.T) {
	e := &KubernetesExecutor{namespace: "portal"}
	pod := e.buildPod("run-abc", ExecuteParams{RunID: "r1", WorkspaceID: "w1", Operation: "plan"}, runPayload{})

	mounts := map[string]bool{}
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if !m.ReadOnly {
			mounts[m.MountPath] = true
		}
	}
	for _, want := range []string{"/work", "/tmp"} {
		if !mounts[want] {
			t.Errorf("%s must be a writable mount under a read-only root filesystem", want)
		}
	}

	// HOME must resolve somewhere writable: the container runs as a uid with no
	// passwd entry, so an unset HOME lands on "/", which is read-only.
	var home string
	for _, ev := range pod.Spec.Containers[0].Env {
		if ev.Name == "HOME" {
			home = ev.Value
		}
	}
	if !mounts[home] {
		t.Errorf("HOME = %q, which is not a writable mount", home)
	}
}

// A workspace variable that sets HOME must still win: kubelet takes the last
// value for a duplicated key, so the default has to be prepended, not appended.
func TestBuildPod_HomeDefaultDoesNotOverrideAnExplicitOne(t *testing.T) {
	e := &KubernetesExecutor{namespace: "portal"}
	payload := runPayload{env: []corev1.EnvVar{{Name: "HOME", Value: "/work/custom"}}}
	pod := e.buildPod("run-abc", ExecuteParams{RunID: "r1"}, payload)

	var last string
	for _, ev := range pod.Spec.Containers[0].Env {
		if ev.Name == "HOME" {
			last = ev.Value
		}
	}
	if last != "/work/custom" {
		t.Errorf("effective HOME = %q, want the explicitly configured /work/custom", last)
	}
}
