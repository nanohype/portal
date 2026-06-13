package service

import (
	"encoding/json"
	"testing"
	"time"
)

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
