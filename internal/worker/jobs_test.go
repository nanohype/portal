package worker

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/nanohype/portal/internal/worker/executor"
)

func TestPostPlanAction(t *testing.T) {
	tests := []struct {
		name             string
		autoApply        bool
		requiresApproval bool
		want             string
	}{
		{"default: neither set", false, false, "planned"},
		{"auto_apply alone queues the apply", true, false, "queued"},
		{"requires_approval parks for a human", false, true, "awaiting_approval"},
		// The workspace gate outranks the convenience flag: a workspace whose
		// owner asked for an approval gets one even when auto_apply is on, so
		// auto_apply cannot be used to skip the approval.
		{"both set: requires_approval wins", true, true, "awaiting_approval"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := postPlanAction(tt.autoApply, tt.requiresApproval)
			if got != tt.want {
				t.Errorf("postPlanAction(%v, %v) = %q, want %q", tt.autoApply, tt.requiresApproval, got, tt.want)
			}
		})
	}
}

func TestPostPlanAction_WithOverride(t *testing.T) {
	// When AutoApplyOverride is set, it should take precedence over workspace settings
	tests := []struct {
		name             string
		wsAutoApply      bool
		override         *bool
		requiresApproval bool
		want             string
	}{
		{"override true on non-auto workspace", false, boolPtr(true), false, "queued"},
		{"override false on auto workspace", true, boolPtr(false), false, "planned"},
		// A pipeline stage's auto_apply override cannot open a workspace's
		// approval gate — the run still parks for a human.
		{"override true with requires_approval", false, boolPtr(true), true, "awaiting_approval"},
		{"nil override uses workspace setting", false, nil, false, "planned"},
		{"nil override uses workspace auto_apply", true, nil, false, "queued"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			autoApply := tt.wsAutoApply
			if tt.override != nil {
				autoApply = *tt.override
			}
			got := postPlanAction(autoApply, tt.requiresApproval)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPipelineStageJobArgs_Kind(t *testing.T) {
	args := PipelineStageJobArgs{}
	if got := args.Kind(); got != "pipeline_stage" {
		t.Errorf("Kind() = %q, want %q", got, "pipeline_stage")
	}
}

func TestPipelineStageJobArgs_InsertOpts(t *testing.T) {
	args := PipelineStageJobArgs{}
	opts := args.InsertOpts()
	if opts.Queue != "default" {
		t.Errorf("Queue = %q, want %q", opts.Queue, "default")
	}
	if opts.Priority != 2 {
		t.Errorf("Priority = %d, want %d", opts.Priority, 2)
	}
}

func TestMergeVariables(t *testing.T) {
	v := func(key, value, cat string) executor.Variable {
		return executor.Variable{Key: key, Value: value, Category: cat}
	}

	tests := []struct {
		name      string
		org       []executor.Variable
		pipeline  []executor.Variable
		workspace []executor.Variable
		want      []executor.Variable
	}{
		{
			name: "org only",
			org:  []executor.Variable{v("region", "us-east-1", "terraform")},
			want: []executor.Variable{v("region", "us-east-1", "terraform")},
		},
		{
			name:     "pipeline overrides org",
			org:      []executor.Variable{v("region", "us-east-1", "terraform")},
			pipeline: []executor.Variable{v("region", "eu-west-1", "terraform")},
			want:     []executor.Variable{v("region", "eu-west-1", "terraform")},
		},
		{
			name:      "workspace overrides both",
			org:       []executor.Variable{v("region", "us-east-1", "terraform")},
			pipeline:  []executor.Variable{v("region", "eu-west-1", "terraform")},
			workspace: []executor.Variable{v("region", "ap-south-1", "terraform")},
			want:      []executor.Variable{v("region", "ap-south-1", "terraform")},
		},
		{
			name:      "different categories are independent",
			org:       []executor.Variable{v("AWS_REGION", "us-east-1", "env")},
			workspace: []executor.Variable{v("AWS_REGION", "eu-west-1", "terraform")},
			want: []executor.Variable{
				v("AWS_REGION", "us-east-1", "env"),
				v("AWS_REGION", "eu-west-1", "terraform"),
			},
		},
		{
			name: "empty slices",
			want: []executor.Variable{},
		},
		{
			name:      "mixed scopes no overlap",
			org:       []executor.Variable{v("account_id", "123", "terraform")},
			pipeline:  []executor.Variable{v("cluster_name", "prod", "terraform")},
			workspace: []executor.Variable{v("vpc_cidr", "10.0.0.0/16", "terraform")},
			want: []executor.Variable{
				v("account_id", "123", "terraform"),
				v("cluster_name", "prod", "terraform"),
				v("vpc_cidr", "10.0.0.0/16", "terraform"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeVariables(tt.org, tt.pipeline, tt.workspace)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			// Sort for stable comparison
			sort.Slice(got, func(i, j int) bool { return got[i].Key+got[i].Category < got[j].Key+got[j].Category })
			sort.Slice(tt.want, func(i, j int) bool { return tt.want[i].Key+tt.want[i].Category < tt.want[j].Key+tt.want[j].Category })
			if len(got) != len(tt.want) {
				t.Fatalf("got %d vars, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("var[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestMergeVariables_DeepMergeTags(t *testing.T) {
	v := func(key, value, cat string) executor.Variable {
		return executor.Variable{Key: key, Value: value, Category: cat}
	}

	tests := []struct {
		name      string
		org       []executor.Variable
		pipeline  []executor.Variable
		workspace []executor.Variable
		wantValue string
	}{
		{
			name:      "tags merged across org and workspace",
			org:       []executor.Variable{v("tags", `{"team":"platform","env":"prod"}`, "terraform")},
			workspace: []executor.Variable{v("tags", `{"app":"network","env":"staging"}`, "terraform")},
			wantValue: `{"app":"network","env":"staging","team":"platform"}`,
		},
		{
			name:      "default_tags also merges",
			org:       []executor.Variable{v("default_tags", `{"managed_by":"portal"}`, "terraform")},
			pipeline:  []executor.Variable{v("default_tags", `{"pipeline":"landing-zone"}`, "terraform")},
			wantValue: `{"managed_by":"portal","pipeline":"landing-zone"}`,
		},
		{
			name:      "custom_tags suffix merges",
			org:       []executor.Variable{v("resource_tags", `{"cost_center":"123"}`, "terraform")},
			workspace: []executor.Variable{v("resource_tags", `{"owner":"alice"}`, "terraform")},
			wantValue: `{"cost_center":"123","owner":"alice"}`,
		},
		{
			name:      "non-tags variable still replaces",
			org:       []executor.Variable{v("region", "us-east-1", "terraform")},
			workspace: []executor.Variable{v("region", "eu-west-1", "terraform")},
			wantValue: "eu-west-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeVariables(tt.org, tt.pipeline, tt.workspace)
			if len(got) != 1 {
				t.Fatalf("expected 1 var, got %d", len(got))
			}
			// Normalize JSON for comparison
			gotVal := got[0].Value
			var gotMap, wantMap map[string]interface{}
			if json.Unmarshal([]byte(gotVal), &gotMap) == nil && json.Unmarshal([]byte(tt.wantValue), &wantMap) == nil {
				gotNorm, _ := json.Marshal(gotMap)
				wantNorm, _ := json.Marshal(wantMap)
				if string(gotNorm) != string(wantNorm) {
					t.Errorf("got %s, want %s", gotNorm, wantNorm)
				}
			} else if gotVal != tt.wantValue {
				t.Errorf("got %q, want %q", gotVal, tt.wantValue)
			}
		})
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func TestSelectBrowseState(t *testing.T) {
	tests := []struct {
		name      string
		stateFile []byte
		stateJSON []byte
		want      []byte
	}{
		{"plain tofu — both present, JSON wins", []byte("raw"), []byte(`{"k":1}`), []byte(`{"k":1}`)},
		{"plain tofu — only raw", []byte("raw"), nil, []byte("raw")},
		{"terragrunt — only JSON from state pull", nil, []byte(`{"k":2}`), []byte(`{"k":2}`)},
		{"nothing captured (e.g. plan-only)", nil, nil, nil},
		{"empty slices treated as missing", []byte{}, []byte{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectBrowseState(tt.stateFile, tt.stateJSON)
			if string(got) != string(tt.want) {
				t.Errorf("selectBrowseState(file=%q, json=%q) = %q, want %q", tt.stateFile, tt.stateJSON, got, tt.want)
			}
		})
	}
}
