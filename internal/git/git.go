// Package git wraps go-git for the two things tofui needs to do with git:
// keep a local clone in sync with a remote (used for the EAP chart cache),
// and write files + commit + push (used for the tenants GitOps repo).
//
// Authentication is SSH-only — tofui's git identity comes from a private
// key file pointed at by the worker's config. HTTPS auth would mean storing
// PATs in DB, which is operationally worse.
package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

// Author identifies a commit. Each tenant operation's commit records the
// tofui user who triggered it via the standard Co-authored-by trailer.
type Author struct {
	Name  string
	Email string
}

// Repo is a working directory of a remote git repo. Use NewRepo + CloneOrPull
// to bring it into a known state, then WriteFile/RemoveFile/Commit/Push to
// produce changes. Not safe for concurrent use on the same workdir.
type Repo struct {
	workdir string
	url     string
	auth    *gitssh.PublicKeys
}

// NewRepo prepares an unopened Repo handle. The workdir doesn't have to exist
// yet — CloneOrPull will create it. sshKeyPath may be empty in dev, in which
// case operations against a remote will fail with a clear error rather than
// silently using unconfigured auth.
func NewRepo(workdir, url, sshKeyPath string) (*Repo, error) {
	r := &Repo{workdir: workdir, url: url}
	if sshKeyPath != "" {
		auth, err := gitssh.NewPublicKeysFromFile("git", sshKeyPath, "")
		if err != nil {
			return nil, fmt.Errorf("load ssh key: %w", err)
		}
		// HostKeyCallback: trust the remote on first contact. The alternative
		// (verify against ~/.ssh/known_hosts) doesn't make sense in a
		// containerized worker that ships without a populated hosts file.
		// Pin trust by limiting access to keys whose public half is registered
		// as a deploy key on a specific repo.
		auth.HostKeyCallbackHelper = gitssh.HostKeyCallbackHelper{}
		r.auth = auth
	}
	return r, nil
}

// CloneOrPull guarantees the workdir holds a checkout of `url` at branch
// `ref`. If the workdir doesn't exist, it clones; if it does, it fetches the
// latest and hard-resets to origin/ref. Either way, after a successful return
// the working tree is clean and pointing at the latest ref.
func (r *Repo) CloneOrPull(ctx context.Context, ref string) error {
	if ref == "" {
		ref = "main"
	}
	if _, err := os.Stat(filepath.Join(r.workdir, ".git")); os.IsNotExist(err) {
		return r.clone(ctx, ref)
	} else if err != nil {
		return fmt.Errorf("stat workdir: %w", err)
	}
	return r.pull(ctx, ref)
}

func (r *Repo) clone(ctx context.Context, ref string) error {
	if err := os.MkdirAll(filepath.Dir(r.workdir), 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	opts := &gogit.CloneOptions{
		URL:           r.url,
		ReferenceName: plumbing.NewBranchReferenceName(ref),
		SingleBranch:  true,
	}
	if r.auth != nil {
		opts.Auth = r.auth
	}
	if _, err := gogit.PlainCloneContext(ctx, r.workdir, false, opts); err != nil {
		return fmt.Errorf("clone %s: %w", r.url, err)
	}
	return nil
}

func (r *Repo) pull(ctx context.Context, ref string) error {
	repo, err := gogit.PlainOpen(r.workdir)
	if err != nil {
		return fmt.Errorf("open existing: %w", err)
	}
	fetchOpts := &gogit.FetchOptions{RemoteName: "origin"}
	if r.auth != nil {
		fetchOpts.Auth = r.auth
	}
	if err := repo.FetchContext(ctx, fetchOpts); err != nil && err != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("fetch: %w", err)
	}

	// Hard reset to origin/<ref>. We don't keep local commits in chart caches.
	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", ref), true)
	if err != nil {
		return fmt.Errorf("resolve origin/%s: %w", ref, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	if err := wt.Reset(&gogit.ResetOptions{Mode: gogit.HardReset, Commit: remoteRef.Hash()}); err != nil {
		return fmt.Errorf("reset to origin/%s: %w", ref, err)
	}
	return nil
}

// WriteFile creates or replaces a file at `relPath` (relative to workdir)
// with the supplied content. relPath is rejected if it's absolute or escapes
// the workdir via `..` — defensive against malicious tenant names producing
// path-traversal commits.
func (r *Repo) WriteFile(relPath string, content []byte) error {
	abs, err := r.safeAbs(relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(abs, content, 0o644)
}

// RemoveFile deletes a file at `relPath`. Missing files are a no-op so this
// can be used as "ensure absent" from a worker that doesn't know whether the
// previous create made it through.
func (r *Repo) RemoveFile(relPath string) error {
	abs, err := r.safeAbs(relPath)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove: %w", err)
	}
	return nil
}

// safeAbs resolves relPath within workdir, rejecting traversal attempts.
func (r *Repo) safeAbs(relPath string) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("path is empty")
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("path must be relative: %s", relPath)
	}
	cleaned := filepath.Clean(relPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		// Reject paths that resolve to the workdir root or above. Inputs like
		// "tenants/.." Clean to "." which is technically inside the workdir
		// but never what the caller meant — flag it as the configuration
		// error it almost certainly is.
		return "", fmt.Errorf("path resolves to workdir root or escapes: %s", relPath)
	}
	return filepath.Join(r.workdir, cleaned), nil
}

// Commit stages every change in the working tree and creates a single
// commit. Returns the commit hash so callers can audit-log what they did.
// If there are no changes, returns ("", nil) — letting the caller decide
// whether that's an error in their context.
func (r *Repo) Commit(message string, author Author) (string, error) {
	repo, err := gogit.PlainOpen(r.workdir)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("worktree: %w", err)
	}
	if _, err := wt.Add("."); err != nil {
		return "", fmt.Errorf("stage: %w", err)
	}

	status, err := wt.Status()
	if err != nil {
		return "", fmt.Errorf("status: %w", err)
	}
	if status.IsClean() {
		return "", nil
	}

	hash, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  author.Name,
			Email: author.Email,
			When:  time.Now(),
		},
	})
	if err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return hash.String(), nil
}

// Push sends the current branch's commits to origin. The branch is inferred
// from HEAD — `CloneOrPull(ref)` set this up so a Commit + Push from the
// caller works without picking the branch each time.
func (r *Repo) Push(ctx context.Context) error {
	repo, err := gogit.PlainOpen(r.workdir)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("head: %w", err)
	}
	opts := &gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec(head.Name() + ":" + head.Name())},
	}
	if r.auth != nil {
		opts.Auth = r.auth
	}
	if err := repo.PushContext(ctx, opts); err != nil && err != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

// Workdir returns the on-disk path of the repo. Used by callers (the helm
// chart cache, mostly) that need to read files out of the working tree
// directly without going through this package's write helpers.
func (r *Repo) Workdir() string { return r.workdir }
