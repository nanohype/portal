package handler

import "testing"

func TestIsValidOperation(t *testing.T) {
	tests := []struct {
		op   string
		want bool
	}{
		{"plan", true},
		{"apply", true},
		{"destroy", true},
		{"import", true},
		{"test", true},
		{"", false},
		{"init", false},
		{"refresh", false},
		{"PLAN", false},
	}

	for _, tt := range tests {
		t.Run(tt.op, func(t *testing.T) {
			got := isValidOperation(tt.op)
			if got != tt.want {
				t.Errorf("isValidOperation(%q) = %v, want %v", tt.op, got, tt.want)
			}
		})
	}
}

func TestIsCancellableStatus(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"pending", true},
		{"queued", true},
		{"planning", true},
		{"applying", true},
		{"awaiting_approval", true},
		{"planned", true},
		{"applied", false},
		{"errored", false},
		{"cancelled", false},
		{"discarded", false},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := isCancellableStatus(tt.status)
			if got != tt.want {
				t.Errorf("isCancellableStatus(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}
