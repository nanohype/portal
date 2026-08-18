package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/repository"
)

func provisionOp(id, account, region string) repository.ClusterOperation {
	spec, _ := json.Marshal(map[string]string{"name": "eks", "account": account, "region": region})
	return repository.ClusterOperation{ID: id, Operation: "provision", SpecJSON: spec}
}

func TestPickProvisionSpec(t *testing.T) {
	// Newest-first: the first provision wins; a deprovision ahead of it is skipped.
	ops := []repository.ClusterOperation{
		{ID: "op3", Operation: "deprovision"},
		provisionOp("op2", "222222222222", "us-east-1"),
		provisionOp("op1", "111111111111", "us-west-2"),
	}
	spec, err := pickProvisionSpec(ops, "production", "eks")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Account != "222222222222" || spec.Region != "us-east-1" {
		t.Errorf("picked %s/%s, want the newest provision 222222222222/us-east-1", spec.Account, spec.Region)
	}

	// No provision on record → a 409, not a teardown with an unknown target.
	_, err = pickProvisionSpec([]repository.ClusterOperation{{Operation: "deprovision"}}, "production", "eks")
	if apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("no-provision error = %v, want apperr.Conflict", err)
	}

	// A provision missing account/region can't locate the workload account → 409.
	_, err = pickProvisionSpec([]repository.ClusterOperation{provisionOp("op1", "", "")}, "production", "eks")
	if apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("missing account/region error = %v, want apperr.Conflict", err)
	}
}

func TestVendPhaseFragment(t *testing.T) {
	at := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)

	// A committed checkpoint with no detail.
	raw, err := vendPhaseFragment("committed", "", at)
	if err != nil {
		t.Fatalf("vendPhaseFragment: %v", err)
	}
	var m map[string]map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Exactly one key is the regressible-merge contract: `vend_phases || fragment`
	// must overwrite only this phase and never clobber sibling phases.
	if len(m) != 1 {
		t.Fatalf("want exactly 1 phase key, got %d: %v", len(m), m)
	}
	c, ok := m["committed"]
	if !ok {
		t.Fatalf("missing 'committed' key: %v", m)
	}
	if c["at"] != "2026-06-12T10:00:00Z" {
		t.Errorf("at = %v, want RFC3339 2026-06-12T10:00:00Z", c["at"])
	}
	// Empty detail is omitted so a phase entry stays minimal.
	if _, present := c["detail"]; present {
		t.Errorf("empty detail should be omitted, got %v", c["detail"])
	}

	// A failed checkpoint carries its error text in detail.
	raw, err = vendPhaseFragment("failed", "git push rejected", at)
	if err != nil {
		t.Fatalf("vendPhaseFragment: %v", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := m["failed"]["detail"]; got != "git push rejected" {
		t.Errorf("detail = %v, want 'git push rejected'", got)
	}
}

// TestHasCommittedProvision pins what makes a cluster deprovisionable: a
// manifest in the clusters repo, which is what teardown removes. Without one,
// the delete removes nothing and the clean tree that follows is
// indistinguishable from a successful teardown.
func TestHasCommittedProvision(t *testing.T) {
	tests := []struct {
		name string
		ops  []repository.ClusterOperation
		want bool
	}{
		{"nothing on record", nil, false},
		{
			// The manifest is pushed on the way to 'committed'.
			"committed provision", []repository.ClusterOperation{{Operation: "provision", Status: "committed"}}, true,
		},
		{
			// Registered too — the ordinary case.
			"active provision", []repository.ClusterOperation{{Operation: "provision", Status: "active"}}, true,
		},
		{
			// Ordered but the worker hasn't written anything yet.
			"pending provision", []repository.ClusterOperation{{Operation: "provision", Status: "pending"}}, false,
		},
		{
			// The write failed, so there is nothing in the repo to remove.
			"failed provision", []repository.ClusterOperation{{Operation: "provision", Status: "failed"}}, false,
		},
		{
			// Already torn down: the manifest is gone.
			"deprovisioned provision", []repository.ClusterOperation{{Operation: "provision", Status: "deprovisioned"}}, false,
		},
		{
			// A deprovision op is not evidence that a manifest exists.
			"deprovision op only", []repository.ClusterOperation{{Operation: "deprovision", Status: "committed"}}, false,
		},
		{
			"mixed history finds the provision", []repository.ClusterOperation{
				{Operation: "deprovision", Status: "failed"},
				{Operation: "provision", Status: "active"},
			}, true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasCommittedProvision(tt.ops); got != tt.want {
				t.Errorf("hasCommittedProvision() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAssertProvisionTeam covers the team gate T11 left open: deprovision with
// the wrong ?team= would Get the XR in the wrong namespace and treat NotFound
// as teardown complete.
func TestAssertProvisionTeam(t *testing.T) {
	if err := assertProvisionTeam("apex", "production", "platform", "platform"); err != nil {
		t.Fatalf("matching team must pass: %v", err)
	}
	if err := assertProvisionTeam("apex", "production", "", "platform"); err != nil {
		t.Fatalf("empty provision team (legacy) must pass: %v", err)
	}
	err := assertProvisionTeam("apex", "production", "platform", "other-team")
	if apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("wrong team must be KindConflict, got %v (kind %v)", err, apperr.KindOf(err))
	}
	if !strings.Contains(err.Error(), "platform") || !strings.Contains(err.Error(), "other-team") {
		t.Errorf("error should name both teams: %v", err)
	}
}

// TestLatestCommittedProvision picks the newest committed/active provision.
func TestLatestCommittedProvision(t *testing.T) {
	ops := []repository.ClusterOperation{
		{ID: "d", Operation: "deprovision", Status: "committed"},
		{ID: "p2", Operation: "provision", Status: "active", Team: "platform"},
		{ID: "p1", Operation: "provision", Status: "committed", Team: "old"},
	}
	got, ok := latestCommittedProvision(ops)
	if !ok || got.ID != "p2" || got.Team != "platform" {
		t.Fatalf("got %+v ok=%v, want p2/platform", got, ok)
	}
	if _, ok := latestCommittedProvision(nil); ok {
		t.Fatal("empty list must miss")
	}
}

// TestIdentityChange pins the two fields an edit may not move. Both address
// artifacts already committed under the old value, and the row is the only
// thing an edit changes.
func TestIdentityChange(t *testing.T) {
	current := repository.Cluster{Name: "apex", Environment: "production"}

	if err := identityChange("", "", current); err != nil {
		t.Errorf("an edit that touches neither field must pass, got %v", err)
	}
	if err := identityChange("apex", "production", current); err != nil {
		t.Errorf("resubmitting the same values is not a rename, got %v", err)
	}
	if err := identityChange("apex-prod", "", current); apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("rename error = %v, want apperr.Conflict", err)
	}
	if err := identityChange("", "staging", current); apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("environment-move error = %v, want apperr.Conflict", err)
	}
}

// Region policy is deployment config, not product behavior. The zero value has
// to stay permissive: the Cluster XRD documents spec.region as "any region is
// valid", so a portal that shipped with a region baked in would refuse orders
// its own CRD accepts.
func TestAssertRegionAllowed(t *testing.T) {
	acct := repository.Account{AWSAccountID: "111111111111"}

	t.Run("unset policy permits any region", func(t *testing.T) {
		s := &ClusterOrderService{}
		for _, region := range []string{"us-east-1", "us-west-2", "eu-central-1", "ap-southeast-2"} {
			if err := s.assertRegionAllowed(context.Background(), region, acct); err != nil {
				t.Errorf("region %s should be permitted with no policy set, got: %v", region, err)
			}
		}
	})

	t.Run("region inside the policy is permitted", func(t *testing.T) {
		s := &ClusterOrderService{allowedRegions: []string{"us-east-1", "eu-west-1"}}
		for _, region := range []string{"us-east-1", "eu-west-1"} {
			if err := s.assertRegionAllowed(context.Background(), region, acct); err != nil {
				t.Errorf("region %s is in the policy, got: %v", region, err)
			}
		}
	})

	t.Run("region outside the policy is a validation error naming the allowed set", func(t *testing.T) {
		s := &ClusterOrderService{allowedRegions: []string{"us-east-1"}}
		err := s.assertRegionAllowed(context.Background(), "us-west-2", acct)
		if err == nil {
			t.Fatal("us-west-2 must be refused when the policy is us-east-1")
		}
		var appErr *apperr.Error
		if !errors.As(err, &appErr) || appErr.Kind != apperr.KindValidation {
			t.Fatalf("want a validation error (400, actionable at the order desk), got %T: %v", err, err)
		}
		// The order desk cannot act on "not permitted" without knowing what is.
		if !strings.Contains(err.Error(), "us-east-1") {
			t.Errorf("error should name the allowed set, got: %v", err)
		}
	})

	t.Run("an account default_region mismatch warns but does not refuse", func(t *testing.T) {
		// An account may legitimately vend into more than one region, so this is
		// a log line rather than a gate.
		s := &ClusterOrderService{}
		mismatched := repository.Account{AWSAccountID: "111111111111", DefaultRegion: "us-east-1"}
		if err := s.assertRegionAllowed(context.Background(), "eu-west-1", mismatched); err != nil {
			t.Errorf("a default_region mismatch must not refuse the order, got: %v", err)
		}
	})
}
