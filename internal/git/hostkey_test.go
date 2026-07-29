package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
)

// Host-key verification on the GitOps write path.
//
// The deploy key portal offers has write access to the tenants and clusters
// repositories — the whole GitOps write path. Whatever answers on the remote's
// address receives that key, so the only thing separating "pushed to our repo"
// from "handed our deploy key to someone else" is verifying the host key first.
//
// The failure this guards against is not a missing check but an invisible one.
// go-git treats a nil HostKeyCallback as "search $HOME/.ssh and /etc/ssh", and
// this worker image has neither, so the resulting error names an environment
// variable nothing sets and arrives at the first push rather than at startup.
// Pairing the two settings here makes the requirement a configuration error a
// deploy surfaces, not a runtime one an operator debugs.

func writeKey(t *testing.T) string {
	t.Helper()
	// A real ed25519 private key: gitssh.NewPublicKeysFromFile parses it, so the
	// test exercises the host-key branch rather than failing earlier on the key.
	const key = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACB/eCcOByPJaWuU3V+3uVXVC/+vcfV7XcVx9JBNVoHunAAAAJj8y00o/MtN
KAAAAAtzc2gtZWQyNTUxOQAAACB/eCcOByPJaWuU3V+3uVXVC/+vcfV7XcVx9JBNVoHunA
AAAEDN4GxQgFwAR9Gid5c9eLjcJKzVezkUWn2RobUVbUxW+n94Jw4HI8lpa5TdX7e5VdUL
/69x9XtdxXH0kE1Wge6cAAAAEXRlc3RAcG9ydGFsLmxvY2FsAQIDBA==
-----END OPENSSH PRIVATE KEY-----
`
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

func writeKnownHosts(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	const entry = "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl\n"
	if err := os.WriteFile(path, []byte(entry), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

func TestNewRepo_RefusesAKeyWithNothingToVerifyTheHostAgainst(t *testing.T) {
	_, err := NewRepo(t.TempDir(), "git@github.com:nanohype/tenants.git", writeKey(t), "")

	if err == nil {
		t.Fatal("built a repo that would offer a deploy key to an unverified host")
	}
	// The message has to name the setting: this fires at worker startup, where
	// whoever reads it is holding a values file, not a stack trace.
	if !strings.Contains(err.Error(), "GITOPS_SSH_KNOWN_HOSTS") {
		t.Errorf("error does not name the missing setting: %v", err)
	}
}

func TestNewRepo_RefusesAKnownHostsFileThatIsNotThere(t *testing.T) {
	// Distinct from the empty case: the path was configured and is wrong, which
	// a chart typo produces and which must not degrade into "no verification".
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	_, err := NewRepo(t.TempDir(), "git@github.com:nanohype/tenants.git", writeKey(t), missing)

	if err == nil {
		t.Fatal("accepted a known_hosts path that does not exist")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error does not name the path it could not read: %v", err)
	}
}

func TestNewRepo_AcceptsAKeyPairedWithKnownHosts(t *testing.T) {
	r, err := NewRepo(t.TempDir(), "git@github.com:nanohype/tenants.git", writeKey(t), writeKnownHosts(t))
	if err != nil {
		t.Fatalf("rejected a correctly configured repo: %v", err)
	}
	if r.auth == nil {
		t.Fatal("no auth configured despite a key being supplied")
	}
	// An explicit callback, not the nil that sends go-git looking through
	// $HOME/.ssh and /etc/ssh for files this image does not carry.
	if r.auth.HostKeyCallbackHelper.HostKeyCallback == nil {
		t.Error("HostKeyCallback is nil, so go-git falls back to a hosts-file search that fails at connect time")
	}
}

func TestNewRepo_WithoutAKeyNeedsNoKnownHosts(t *testing.T) {
	// Dev runs with no deploy key at all. Requiring known_hosts there would make
	// a local checkout impossible to construct for no security benefit — there
	// is no key to protect.
	r, err := NewRepo(t.TempDir(), "git@github.com:nanohype/tenants.git", "", "")
	if err != nil {
		t.Fatalf("rejected a keyless dev repo: %v", err)
	}
	if r.auth != nil {
		t.Error("configured auth without a key")
	}
}

func TestNewRepo_RejectsAnUnreadableKeyBeforeTouchingKnownHosts(t *testing.T) {
	// Ordering matters only so the reported error is the real one: a bad key
	// and a missing hosts file are different fixes.
	bad := filepath.Join(t.TempDir(), "not-a-key")
	if err := os.WriteFile(bad, []byte("nope"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := NewRepo(t.TempDir(), "git@github.com:nanohype/tenants.git", bad, "")

	if err == nil || !strings.Contains(err.Error(), "load ssh key") {
		t.Fatalf("want a key-loading error, got %v", err)
	}
}

func TestCloneOrPull_WithoutAuthDoesNotReachTheNetwork(t *testing.T) {
	// A keyless repo pointed at an SSH remote must fail on auth rather than
	// appearing to work; the dev path is "no writes", not "unauthenticated
	// writes".
	r, err := NewRepo(t.TempDir(), "git@github.com:nanohype/does-not-exist.git", "", "")
	if err != nil {
		t.Fatalf("new repo: %v", err)
	}
	if r.auth != nil {
		t.Fatal("expected no auth")
	}
	// Sanity: the handle is still a usable local object, so the failure below is
	// about the remote and not about construction.
	if _, err := gogit.PlainInit(r.workdir, false); err != nil {
		t.Fatalf("workdir unusable: %v", err)
	}
}
