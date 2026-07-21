package handler

import (
	"testing"

	"github.com/nanohype/portal/internal/repository"
)

func boolPtr(b bool) *bool { return &b }

// The settings form posts every field on every save, so "the request carries a
// value" is not the same as "the request changes the approval gate".
func TestChangesApprovalGate(t *testing.T) {
	current := repository.Workspace{AutoApply: false, RequiresApproval: true}

	tests := []struct {
		name string
		req  UpdateWorkspaceRequest
		want bool
	}{
		{"no approval fields submitted", UpdateWorkspaceRequest{Name: "renamed"}, false},
		{"resubmits the stored values", UpdateWorkspaceRequest{
			AutoApply: boolPtr(false), RequiresApproval: boolPtr(true),
		}, false},
		{"turns auto_apply on", UpdateWorkspaceRequest{AutoApply: boolPtr(true)}, true},
		{"turns requires_approval off", UpdateWorkspaceRequest{RequiresApproval: boolPtr(false)}, true},
		{"flips both", UpdateWorkspaceRequest{
			AutoApply: boolPtr(true), RequiresApproval: boolPtr(false),
		}, true},
		{"changes other fields alongside stored approval values", UpdateWorkspaceRequest{
			RepoURL: "https://example.test/repo.git", RepoBranch: "main",
			AutoApply: boolPtr(false), RequiresApproval: boolPtr(true),
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := changesApprovalGate(current, tt.req); got != tt.want {
				t.Errorf("changesApprovalGate = %v, want %v", got, tt.want)
			}
		})
	}
}

// The route lets operators edit workspace settings. Removing the approval an
// admin would otherwise have to sign is the one part of that form they may not
// touch — and everything else on it must still go through.
func TestApprovalGateChangeAllowed(t *testing.T) {
	current := repository.Workspace{AutoApply: false, RequiresApproval: true}
	disableGate := UpdateWorkspaceRequest{AutoApply: boolPtr(true), RequiresApproval: boolPtr(false)}
	renameOnly := UpdateWorkspaceRequest{Name: "renamed", AutoApply: boolPtr(false), RequiresApproval: boolPtr(true)}

	tests := []struct {
		name string
		req  UpdateWorkspaceRequest
		role string
		want bool
	}{
		{"operator cannot disable the approval gate", disableGate, "operator", false},
		{"viewer cannot disable the approval gate", disableGate, "viewer", false},
		{"unknown role cannot disable the approval gate", disableGate, "intern", false},
		{"admin can disable the approval gate", disableGate, "admin", true},
		{"owner can disable the approval gate", disableGate, "owner", true},
		{"operator can still edit everything else", renameOnly, "operator", true},
		{"viewer request untouched by this check", renameOnly, "viewer", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approvalGateChangeAllowed(current, tt.req, tt.role); got != tt.want {
				t.Errorf("approvalGateChangeAllowed(role=%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}
