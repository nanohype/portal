package handler

import (
	"testing"

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
func TestAddsAutoApplyStage(t *testing.T) {
	tests := []struct {
		name      string
		current   []repository.PipelineStageWithWorkspace
		submitted []service.CreatePipelineStageInput
		want      bool
	}{
		{
			name:      "a new pipeline asking for auto-apply",
			current:   nil,
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", true)},
			want:      true,
		},
		{
			name:      "a new pipeline with manual stages only",
			current:   nil,
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", false)},
			want:      false,
		},
		{
			name:      "turning auto-apply on for a stage that did not have it",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", false)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", true)},
			want:      true,
		},

		// Everything below is an ordinary operator edit on a pipeline that
		// already holds an admin-set auto-apply stage.
		{
			name:      "resubmitting the stored stages unchanged",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true), storedStage("ws-app", false)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", true), submittedStage("ws-app", false)},
			want:      false,
		},
		{
			name:      "reordering the stages",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true), storedStage("ws-app", false)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-app", false), submittedStage("ws-net", true)},
			want:      false,
		},
		{
			name:      "adding a manual stage alongside the auto one",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", true), submittedStage("ws-app", false)},
			want:      false,
		},
		{
			name:      "dropping the auto stage",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true), storedStage("ws-app", false)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-app", false)},
			want:      false,
		},
		{
			name:      "turning auto-apply off",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", false)},
			want:      false,
		},

		// Stages have no stable id across the write, so identity is the
		// workspace — counted, because a pipeline may run one twice.
		{
			name:      "a second auto-apply stage on the same workspace",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", true), submittedStage("ws-net", true)},
			want:      true,
		},
		{
			name:      "moving auto-apply to a different workspace",
			current:   []repository.PipelineStageWithWorkspace{storedStage("ws-net", true), storedStage("ws-app", false)},
			submitted: []service.CreatePipelineStageInput{submittedStage("ws-net", false), submittedStage("ws-app", true)},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := addsAutoApplyStage(tt.current, tt.submitted); got != tt.want {
				t.Errorf("addsAutoApplyStage = %v, want %v", got, tt.want)
			}
		})
	}
}
