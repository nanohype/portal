// Hand-written pgx query layer (sqlc-style); not generated, edit directly.

package repository

import (
	"encoding/json"
	"time"
)

type Organization struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type User struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	AvatarURL   string    `json:"avatar_url"`
	GithubID    *int64    `json:"github_id"`
	Role        string    `json:"role"`
	LastLoginAt time.Time `json:"last_login_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Workspace struct {
	ID                     string    `json:"id"`
	OrgID                  string    `json:"org_id"`
	Name                   string    `json:"name"`
	Description            string    `json:"description"`
	RepoURL                string    `json:"repo_url"`
	RepoBranch             string    `json:"repo_branch"`
	WorkingDir             string    `json:"working_dir"`
	TofuVersion            string    `json:"tofu_version"`
	Environment            string    `json:"environment"`
	AutoApply              bool      `json:"auto_apply"`
	RequiresApproval       bool      `json:"requires_approval"`
	VcsTriggerEnabled      bool      `json:"vcs_trigger_enabled"`
	Locked                 bool      `json:"locked"`
	LockedBy               *string   `json:"locked_by"`
	CurrentRunID           *string   `json:"current_run_id"`
	CreatedBy              string    `json:"created_by"`
	Source                 string    `json:"source"`
	CurrentConfigVersionID string    `json:"current_config_version_id,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type WorkspaceSummary struct {
	Workspace
	LastRunStatus *string    `json:"last_run_status"`
	LastRunAt     *time.Time `json:"last_run_at"`
	ResourceCount int32      `json:"resource_count"`
}

type Run struct {
	ID               string     `json:"id"`
	WorkspaceID      string     `json:"workspace_id"`
	OrgID            string     `json:"org_id"`
	Operation        string     `json:"operation"`
	Status           string     `json:"status"`
	PlanOutput       string     `json:"plan_output"`
	PlanLogURL       string     `json:"plan_log_url"`
	ApplyLogURL      string     `json:"apply_log_url"`
	ResourcesAdded   int32      `json:"resources_added"`
	ResourcesChanged int32      `json:"resources_changed"`
	ResourcesDeleted int32      `json:"resources_deleted"`
	ErrorMessage     string     `json:"error_message"`
	CommitSHA        string     `json:"commit_sha"`
	PlanJSONURL      string     `json:"plan_json_url"`
	CreatedBy        string     `json:"created_by"`
	StartedAt        *time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type StateVersion struct {
	ID              string    `json:"id"`
	WorkspaceID     string    `json:"workspace_id"`
	OrgID           string    `json:"org_id"`
	RunID           string    `json:"run_id"`
	Serial          int32     `json:"serial"`
	StateURL        string    `json:"state_url"`
	ResourceCount   int32     `json:"resource_count"`
	ResourceSummary string    `json:"resource_summary"`
	CreatedAt       time.Time `json:"created_at"`
}

type AuditLog struct {
	ID         string          `json:"id"`
	OrgID      string          `json:"org_id"`
	UserID     string          `json:"user_id"`
	Action     string          `json:"action"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	BeforeData json.RawMessage `json:"before_data"`
	AfterData  json.RawMessage `json:"after_data"`
	IPAddress  string          `json:"ip_address"`
	UserAgent  string          `json:"user_agent"`
	CreatedAt  time.Time       `json:"created_at"`
}

type WorkspaceVariable struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	OrgID       string    `json:"org_id"`
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Sensitive   bool      `json:"sensitive"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type OrgVariable struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Sensitive   bool      `json:"sensitive"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type PipelineVariable struct {
	ID          string    `json:"id"`
	PipelineID  string    `json:"pipeline_id"`
	OrgID       string    `json:"org_id"`
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Sensitive   bool      `json:"sensitive"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Pipeline struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type PipelineStage struct {
	ID          string    `json:"id"`
	PipelineID  string    `json:"pipeline_id"`
	WorkspaceID string    `json:"workspace_id"`
	StageOrder  int32     `json:"stage_order"`
	AutoApply   bool      `json:"auto_apply"`
	OnFailure   string    `json:"on_failure"`
	CreatedAt   time.Time `json:"created_at"`
}

type PipelineStageWithWorkspace struct {
	PipelineStage
	WorkspaceName string `json:"workspace_name"`
}

type PipelineRun struct {
	ID           string     `json:"id"`
	PipelineID   string     `json:"pipeline_id"`
	OrgID        string     `json:"org_id"`
	Status       string     `json:"status"`
	CurrentStage int32      `json:"current_stage"`
	TotalStages  int32      `json:"total_stages"`
	CreatedBy    string     `json:"created_by"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type PipelineRunStage struct {
	ID            string     `json:"id"`
	PipelineRunID string     `json:"pipeline_run_id"`
	StageID       string     `json:"stage_id"`
	WorkspaceID   string     `json:"workspace_id"`
	RunID         *string    `json:"run_id,omitempty"`
	StageOrder    int32      `json:"stage_order"`
	Status        string     `json:"status"`
	AutoApply     bool       `json:"auto_apply"`
	OnFailure     string     `json:"on_failure"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type PipelineRunStageWithWorkspace struct {
	PipelineRunStage
	WorkspaceName string `json:"workspace_name"`
}

type Account struct {
	ID                  string    `json:"id"`
	OrgID               string    `json:"org_id"`
	Name                string    `json:"name"`
	Description         string    `json:"description"`
	AWSAccountID        string    `json:"aws_account_id"`
	AssumeRoleARN       string    `json:"assume_role_arn"`
	ExternalIDEncrypted string    `json:"-"`
	DefaultRegion       string    `json:"default_region"`
	CreatedBy           string    `json:"created_by"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type Cluster struct {
	ID                string     `json:"id"`
	OrgID             string     `json:"org_id"`
	AccountID         string     `json:"account_id"`
	Name              string     `json:"name"`
	Description       string     `json:"description"`
	Environment       string     `json:"environment"`
	APIEndpoint       string     `json:"api_endpoint"`
	CABundleEncrypted string     `json:"-"`
	SATokenEncrypted  string     `json:"-"`
	Region            string     `json:"region"`
	AuthMode          string     `json:"auth_mode"`
	EKSClusterName    string     `json:"eks_cluster_name"`
	ConnectionStatus  string     `json:"connection_status"`
	LastConnectedAt   *time.Time `json:"last_connected_at"`
	ConnectionError   string     `json:"connection_error"`
	NodeCount         int32      `json:"node_count"`
	K8sVersion        string     `json:"k8s_version"`
	// Health projections written by the hub-side cluster-health watcher.
	ArgoCDSyncStatus     string     `json:"argocd_sync_status"`
	ArgoCDHealthStatus   string     `json:"argocd_health_status"`
	ControlPlaneStatus   string     `json:"control_plane_status"`
	PlatformVersion      string     `json:"platform_version"`
	LastHealthObservedAt *time.Time `json:"last_health_observed_at"`
	CreatedBy            string     `json:"created_by"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type Tenant struct {
	ID             string          `json:"id"`
	OrgID          string          `json:"org_id"`
	ClusterID      string          `json:"cluster_id"`
	Name           string          `json:"name"`
	Phase          string          `json:"phase"`
	Spec           json.RawMessage `json:"spec"`
	Status         json.RawMessage `json:"status"`
	LastObservedAt time.Time       `json:"last_observed_at"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type TenantOperation struct {
	ID           string          `json:"id"`
	OrgID        string          `json:"org_id"`
	ClusterID    string          `json:"cluster_id"`
	TenantName   string          `json:"tenant_name"`
	Operation    string          `json:"operation"`
	Status       string          `json:"status"`
	GitCommitSHA string          `json:"git_commit_sha"`
	Error        string          `json:"error"`
	ValuesJSON   json.RawMessage `json:"values_json"`
	TemplateID   *string         `json:"template_id"`
	CreatedBy    string          `json:"created_by"`
	CreatedAt    time.Time       `json:"created_at"`
	CompletedAt  *time.Time      `json:"completed_at"`
}

// ClusterOperation is the vend order-desk log row. spec_json holds the
// clusterspec.Input the portal templated the Cluster CR from; cluster_id is set
// when the watch-back auto-registers the vended cluster (status -> 'active').
type ClusterOperation struct {
	ID           string          `json:"id"`
	OrgID        string          `json:"org_id"`
	Name         string          `json:"name"`
	Environment  string          `json:"environment"`
	Team         string          `json:"team"`
	Operation    string          `json:"operation"`
	Status       string          `json:"status"`
	GitCommitSHA string          `json:"git_commit_sha"`
	Error        string          `json:"error"`
	SpecJSON     json.RawMessage `json:"spec_json"`
	ClusterID    *string         `json:"cluster_id"`
	CreatedBy    string          `json:"created_by"`
	CreatedAt    time.Time       `json:"created_at"`
	CompletedAt  *time.Time      `json:"completed_at"`
	VendPhases   json.RawMessage `json:"vend_phases"`
}

type Template struct {
	ID                   string          `json:"id"`
	OrgID                string          `json:"org_id"`
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Persona              string          `json:"persona"`
	DefaultValues        json.RawMessage `json:"default_values"`
	AllowedOverrides     json.RawMessage `json:"allowed_overrides"`
	MaxBudgetUSD         int32           `json:"max_budget_usd"`
	AllowedModelFamilies json.RawMessage `json:"allowed_model_families"`
	RequiredCompliance   json.RawMessage `json:"required_compliance"`
	CreatedBy            string          `json:"created_by"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type TenantTeamAccess struct {
	ID         string    `json:"id"`
	OrgID      string    `json:"org_id"`
	ClusterID  string    `json:"cluster_id"`
	TenantName string    `json:"tenant_name"`
	TeamID     string    `json:"team_id"`
	GrantedBy  string    `json:"granted_by"`
	GrantedAt  time.Time `json:"granted_at"`
}

type TemplateTeamAccess struct {
	ID         string    `json:"id"`
	OrgID      string    `json:"org_id"`
	TemplateID string    `json:"template_id"`
	TeamID     string    `json:"team_id"`
	GrantedBy  string    `json:"granted_by"`
	GrantedAt  time.Time `json:"granted_at"`
}
