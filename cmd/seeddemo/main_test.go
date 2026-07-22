package main

import (
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/worker/executor"
)

// The demo's only approval showcase is a seeded vcs run parked at
// awaiting_approval. Approving it flips the run to an apply and enqueues it,
// and the worker refuses any vcs run whose commit_sha is not a git object id
// rather than silently applying branch head. So the seeded pin has to be one
// the worker accepts, or the feature this whole gate exists to protect fails
// the first time anyone clicks Approve.
func TestSeededCommitSHAIsAGitCommitID(t *testing.T) {
	for i := 0; i < 64; i++ {
		sha := commitSHA()
		if !executor.IsCommitSHA(sha) {
			t.Fatalf("commitSHA() = %q, which the worker refuses to execute", sha)
		}
	}
}

// The pin the seeder used to write was a ULID prefix, and this is why that can
// never be a commit id: Crockford base32 carries letters outside hex, and every
// present-day ULID starts "01K". Without this the fix reads as cosmetic.
func TestULIDPrefixIsNotAGitCommitID(t *testing.T) {
	if executor.IsCommitSHA(ulid.Make().String()[:7]) {
		t.Fatal("a ULID prefix passed IsCommitSHA; this test is no longer proving anything")
	}
}

// Runs are unique on their pin only by accident, but a demo where every run
// shows the same commit reads as broken data, so the entropy has to survive
// the encoding.
func TestSeededCommitSHAsDiffer(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		seen[commitSHA()] = true
	}
	if len(seen) < 60 {
		t.Errorf("commitSHA() produced %d distinct values out of 64; the entropy is not reaching the output", len(seen))
	}
}
