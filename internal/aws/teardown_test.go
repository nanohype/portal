package aws

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fakeTeardown simulates the AWS teardown surface:
//   - blockers[arn] = ARNs that must be deleted before arn (dependency ordering),
//   - hardErr[arn]  = a fatal, non-retryable error,
//   - order         = ARNs deleted so far; Delete is idempotent against it.
//
// Async deletes aren't modeled here: the real Delete polls to completion, so at
// the engine's level a delete either returns nil (gone) or DependencyError.
type fakeTeardown struct {
	resources []Resource
	gotTag    string
	order     []string // ARNs in the order they were actually deleted
	blockers  map[string][]string
	hardErr   map[string]error
}

func (f *fakeTeardown) Discover(_ context.Context, clusterTag string) ([]Resource, error) {
	f.gotTag = clusterTag
	return f.resources, nil
}

func (f *fakeTeardown) Delete(_ context.Context, r Resource) error {
	if e := f.hardErr[r.ARN]; e != nil {
		return e
	}
	if slices.Contains(f.order, r.ARN) {
		return nil // already gone — idempotent
	}
	for _, b := range f.blockers[r.ARN] {
		if !slices.Contains(f.order, b) {
			return &DependencyError{Err: fmt.Errorf("%s blocks %s", b, r.ARN)}
		}
	}
	f.order = append(f.order, r.ARN)
	return nil
}

func res(arn, service, typ string) Resource {
	return Resource{ARN: arn, Service: service, Type: typ, ID: arn}
}

func run(t *testing.T, f *fakeTeardown) (TeardownResult, error) {
	t.Helper()
	return Teardown(context.Background(), f, "production-prod-eks", WithPassDelay(0), WithMaxPasses(20))
}

func TestTeardown_Empty(t *testing.T) {
	r, err := run(t, &fakeTeardown{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Deleted != 0 {
		t.Errorf("deleted = %d, want 0", r.Deleted)
	}
}

func TestTeardown_DeletesEverythingAndScopesDiscovery(t *testing.T) {
	f := &fakeTeardown{resources: []Resource{
		res("vpc", "ec2", "vpc"),
		res("subnet", "ec2", "subnet"),
		res("cluster", "eks", "cluster"),
		res("role", "iam", "role"),
	}}
	r, err := run(t, f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Deleted != 4 || len(r.Remaining) != 0 {
		t.Errorf("deleted=%d remaining=%d, want 4/0", r.Deleted, len(r.Remaining))
	}
	if f.gotTag != "production-prod-eks" {
		t.Errorf("Discover got tag %q, want the cluster tag", f.gotTag)
	}
}

func TestTeardown_RetriesAroundDependencies(t *testing.T) {
	// A reverse-ranked blocker the static order can't pre-resolve: the EKS cluster
	// (rank 2, deleted early) is blocked until a security group (rank 10, later) is
	// gone — only the retry loop gets this right.
	f := &fakeTeardown{
		resources: []Resource{res("cluster", "eks", "cluster"), res("sg", "ec2", "security-group")},
		blockers:  map[string][]string{"cluster": {"sg"}},
	}
	r, err := run(t, f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Deleted != 2 {
		t.Fatalf("deleted = %d, want 2", r.Deleted)
	}
	if idx(f.order, "sg") > idx(f.order, "cluster") {
		t.Errorf("sg must be deleted before the cluster that depends on it: %v", f.order)
	}
}

func TestTeardown_IsIdempotent(t *testing.T) {
	// Re-running after a teardown is safe: a second pass over the same resources
	// (all already gone → every Delete returns nil) completes without error.
	f := &fakeTeardown{resources: []Resource{res("vpc", "ec2", "vpc"), res("subnet", "ec2", "subnet")}}
	if _, err := run(t, f); err != nil {
		t.Fatalf("first run: %v", err)
	}
	r, err := run(t, f)
	if err != nil {
		t.Fatalf("second run must be a safe no-op: %v", err)
	}
	if len(r.Remaining) != 0 {
		t.Errorf("remaining = %d, want 0 (all gone)", len(r.Remaining))
	}
}

func TestTeardown_StallsWhenNothingCanProgress(t *testing.T) {
	// A resource permanently blocked by something that isn't in the set → no pass
	// can make progress → the engine reports the stall instead of looping forever.
	f := &fakeTeardown{
		resources: []Resource{res("vpc", "ec2", "vpc")},
		blockers:  map[string][]string{"vpc": {"ghost"}},
	}
	_, err := run(t, f)
	if err == nil {
		t.Fatal("expected a stall error, got nil")
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Errorf("error = %q, want a stall message", err)
	}
}

func TestTeardown_FatalErrorAborts(t *testing.T) {
	// A non-dependency error (e.g. AccessDenied) aborts immediately — the operator
	// must not be told the teardown succeeded.
	boom := errors.New("AccessDenied")
	f := &fakeTeardown{
		resources: []Resource{res("vpc", "ec2", "vpc")},
		hardErr:   map[string]error{"vpc": boom},
	}
	_, err := run(t, f)
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the fatal error wrapped", err)
	}
}

func idx(s []string, v string) int { return slices.Index(s, v) }
