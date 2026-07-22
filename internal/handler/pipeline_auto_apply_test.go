package handler

import (
	"slices"
	"testing"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

func storedStage(workspaceID string, autoApply bool) repository.PipelineStageWithWorkspace {
	return repository.PipelineStageWithWorkspace{
		PipelineStage: repository.PipelineStage{WorkspaceID: workspaceID, AutoApply: autoApply},
	}
}

func submittedStage(workspaceID string, autoApply bool) service.CreatePipelineStageInput {
	return service.CreatePipelineStageInput{WorkspaceID: workspaceID, AutoApply: autoApply}
}

// Updating a pipeline replaces the whole stage list, so every edit resubmits
// the auto_apply each stage already carries. Only a stage that gains
// auto-apply is an authorization event — otherwise one admin-set stage would
// freeze the pipeline against every later operator edit.
func TestWorkspacesGainingAutoApply(t *testing.T) {
	tests := []struct {
		name      string
		current   []repository.PipelineStageWithWorkspace
		submitted []service.CreatePipelineStageInput
		want      []string
	}{
		{
			name:      "a new pipeline asking for auto-apply",
			current:   nil,
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", true)},
			want:      []string{"ws-net"},
		},
		{
			name:      "a new pipeline with manual stages only",
			current:   nil,
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", false)},
			want:      nil,
		},
		{
			name:      "turning auto-apply on for a stage that did not have it",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", false)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", true)},
			want:      []string{"ws-net"},
		},

		// Everything below is an ordinary operator edit on a pipeline that
		// already holds an admin-set auto-apply stage.
		{
			name:      "resubmitting the stored stages unchanged",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true), storedStage("ws-app", false)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", true), submittedStage("ws-app", false)},
			want:      nil,
		},
		{
			name:      "reordering the stages",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true), storedStage("ws-app", false)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-app", false), submittedStage("ws-net", true)},
			want:      nil,
		},
		{
			name:      "adding a manual stage alongside the auto one",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", true), submittedStage("ws-app", false)},
			want:      nil,
		},
		{
			name:      "dropping the auto stage",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true), storedStage("ws-app", false)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-app", false)},
			want:      nil,
		},
		{
			name:      "turning auto-apply off",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", false)},
			want:      nil,
		},

		// Stages have no stable id across the write, so identity is the
		// workspace — counted, because a pipeline may run one twice.
		{
			name:      "a second auto-apply stage on the same workspace",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", true), submittedStage("ws-net", true)},
			want:      []string{"ws-net"},
		},
		{
			name:      "moving auto-apply to a different workspace",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true), storedStage("ws-app", false)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", false), submittedStage("ws-app", true)},
			want:      []string{"ws-app"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workspacesGainingAutoApply(tt.current, tt.submitted); !slices.Equal(got, tt.want) {
				t.Errorf("workspacesGainingAutoApply = %v, want %v", got, tt.want)
			}
		})
	}
}

// A stage's auto_apply is worth what the workspace it targets is worth. On an
// ungated workspace it is the apply the same operator may start by hand, so it
// sits at ActionApplyRun — otherwise building a pipeline, which exists to run
// several applies in order, is admin-only for the operators who build them. On
// a gated workspace it sits at the bar that releases a gated apply.
func TestAutoApplyStageBar(t *testing.T) {
	gates := map[string]repository.WorkspaceGateRow{
		"ws-dev":  {ID: "ws-dev", Name: "dev-vpc", RequiresApproval: false},
		"ws-app":  {ID: "ws-app", Name: "dev-app", RequiresApproval: false},
		"ws-prod": {ID: "ws-prod", Name: "prod-vpc", RequiresApproval: true},
	}

	tests := []struct {
		name       string
		gaining    []string
		wantAction auth.Action
		wantGated  []string
	}{
		{"one ungated workspace", []string{"ws-dev"}, auth.ActionApplyRun, nil},
		{"several ungated workspaces", []string{"ws-dev", "ws-app"}, auth.ActionApplyRun, nil},
		{"a gated workspace", []string{"ws-prod"}, auth.ActionApplyProd, []string{"prod-vpc"}},
		{"one gated among ungated", []string{"ws-dev", "ws-prod"}, auth.ActionApplyProd, []string{"prod-vpc"}},
		// The route refuses a stage naming a workspace this org does not have
		// before the bar is read; if one ever got here it takes the high bar
		// rather than the low one.
		{"a workspace that resolved to nothing", []string{"ws-unknown"}, auth.ActionApplyProd, []string{"ws-unknown"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, gated := autoApplyStageBar(gates, tt.gaining)
			if action != tt.wantAction || !slices.Equal(gated, tt.wantGated) {
				t.Errorf("autoApplyStageBar = (%v, %v), want (%v, %v)",
					action, gated, tt.wantAction, tt.wantGated)
			}
		})
	}

	// The bar is only worth what the roles then clear against it: an operator
	// may build the ungated pipeline and may not hand auto-apply to a gated
	// workspace.
	if !auth.CanPerform("operator", auth.ActionApplyRun) {
		t.Error("an operator must clear the ungated stage bar — pipelines are their daily driver")
	}
	if auth.CanPerform("operator", auth.ActionApplyProd) {
		t.Error("an operator must not clear the gated stage bar")
	}
	if !auth.CanPerform("admin", auth.ActionApplyProd) {
		t.Error("an admin must clear the gated stage bar")
	}
}
