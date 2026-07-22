package executor

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
)

// commitSHAPattern is what a git object id looks like: hex, abbreviated at
// worst, sha-256 at longest. The value reaches `git checkout` as an argument
// and, in the Kubernetes executor, as a shell variable, so anything that is not
// an object id is refused rather than passed through — a pin that cannot be
// resolved must stop the run, never quietly fall back to the branch.
var commitSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// IsCommitSHA reports whether s can be a git commit id.
func IsCommitSHA(s string) bool {
	return commitSHAPattern.MatchString(s)
}

// DetectBinary returns the binary the worker should invoke for a workspace
// rooted at workDir. When a terragrunt.hcl is present at the root, the run
// is driven by terragrunt (which walks parent dirs and renders terraform at
// run time); otherwise tofu drives the run directly.
//
// Precedence: terragrunt.hcl wins if both a terragrunt.hcl and *.tf are
// present — terragrunt is the higher-level wrapper and it owns the run.
func DetectBinary(workDir string) string {
	if _, err := os.Stat(filepath.Join(workDir, "terragrunt.hcl")); err == nil {
		return "terragrunt"
	}
	return "tofu"
}

// Variable represents an OpenTofu or environment variable.
type Variable struct {
	Key      string
	Value    string
	Category string // "terraform" or "env"
}

// ImportResource represents a single resource to import.
type ImportResource struct {
	Address string // e.g. "aws_vpc.main"
	ID      string // e.g. "vpc-0b7f9b9c287a313aa"
}

// ExecuteParams holds the parameters for running OpenTofu.
type ExecuteParams struct {
	RunID       string
	WorkspaceID string
	Operation   string // "plan", "apply", "destroy", "import", "test"
	RepoURL     string
	RepoBranch  string
	WorkingDir  string
	TofuVersion string
	Variables   []Variable
	LogCallback func([]byte)

	// CommitSHA is the exact commit to execute. A branch is a moving target —
	// the plan an admin signed and the apply that follows the signature are two
	// clones, and between them anyone with write access to the branch can move
	// it. When this is set the executor checks the commit out and runs that
	// tree; a branch that no longer contains it fails the run rather than
	// silently applying something else. Empty means branch head, which is what a
	// first plan gets before there is anything to pin to.
	CommitSHA string

	// PreviousState is the state file from the last successful apply.
	// If non-nil, it is restored as terraform.tfstate before execution.
	PreviousState []byte

	// StateEncryptionPassphrase enables OpenTofu 1.7+ native state encryption.
	// When set, an encryption override file is written with PBKDF2+AES-GCM.
	StateEncryptionPassphrase string

	// ImportResources is the list of resources to import (import operation only).
	ImportResources []ImportResource

	// Source is "vcs" or "upload". When "upload", ArchiveData contains the tar.gz.
	Source string

	// ArchiveData holds the uploaded tar.gz config archive for upload-source workspaces.
	ArchiveData []byte
}

// ExecuteResult holds the outcome of an OpenTofu execution.
type ExecuteResult struct {
	// CommitSHA is the commit the run actually executed, resolved from the
	// checkout. The worker records it on the run row so a later apply of the
	// same run — an approval release or an auto-apply — executes the tree the
	// plan was produced from and not whatever the branch points at by then.
	// Empty for upload-source runs, which are pinned by config version instead.
	CommitSHA string

	Output           string
	ResourcesAdded   int32
	ResourcesChanged int32
	ResourcesDeleted int32
	StateFile        []byte // raw terraform.tfstate (may be encrypted)
	StateJSON        []byte // decrypted state JSON from "tofu state pull" (for resource browsing)
	PlanJSON         []byte
}

// Executor runs OpenTofu commands in an isolated environment.
type Executor interface {
	Execute(ctx context.Context, params ExecuteParams) (*ExecuteResult, error)
}
