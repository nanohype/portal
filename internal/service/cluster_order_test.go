package service

import (
	"encoding/json"
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
