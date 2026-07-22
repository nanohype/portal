// Hand-written pgx query layer (sqlc-style); not generated, edit directly.
//
// Row structs are storage types: they carry no json tags and never serialize
// to the wire. The API shapes live in internal/handler's *Response types.

package repository

import (
	"encoding/json"
	"time"
)

type Organization struct {
	ID        string
	Name      string
	Slug      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type User struct {
	ID          string
	OrgID       string
	Email       string
	Name        string
	AvatarURL   string
	GithubID    *int64
	Role        string
	LastLoginAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Workspace struct {
	ID                     string
	OrgID                  string
	Name                   string
	Description            string
	RepoURL                string
	RepoBranch             string
	WorkingDir             string
	TofuVersion            string
	Environment            string
	AutoApply              bool
	RequiresApproval       bool
	VcsTriggerEnabled      bool
	Locked                 bool
	LockedBy               *string
	CurrentRunID           *string
	CreatedBy              string
	Source                 string
	CurrentConfigVersionID string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type WorkspaceSummary struct {
	Workspace
	LastRunStatus *string
	LastRunAt     *time.Time
	ResourceCount int32
}

type Run struct {
	ID               string
	WorkspaceID      string
	OrgID            string
	Operation        string
	Status           string
	PlanOutput       string
	PlanLogURL       string
	ApplyLogURL      string
	ResourcesAdded   int32
	ResourcesChanged int32
	ResourcesDeleted int32
	ErrorMessage     string
	CommitSHA        string

	// The configuration this run executes, frozen from the workspace at
	// creation. The worker runs these, not the live workspace row.
	ConfigSource      string
	ConfigRepoURL     string
	ConfigRepoBranch  string
	ConfigWorkingDir  string
	ConfigVersionID   string
	ConfigTofuVersion string

	PlanJSONURL string
	CreatedBy   string
	StartedAt   *time.Time
	FinishedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type StateVersion struct {
	ID              string
	WorkspaceID     string
	OrgID           string
	RunID           string
	Serial          int32
	StateURL        string
	ResourceCount   int32
	ResourceSummary string
	CreatedAt       time.Time
}

type AuditLog struct {
	ID         string
	OrgID      string
	UserID     string
	Action     string
	EntityType string
	EntityID   string
	BeforeData json.RawMessage
	AfterData  json.RawMessage
	IPAddress  string
	UserAgent  string
	CreatedAt  time.Time
}

type WorkspaceVariable struct {
	ID          string
	WorkspaceID string
	OrgID       string
	Key         string
	Value       string
	Sensitive   bool
	Category    string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type OrgVariable struct {
	ID          string
	OrgID       string
	Key         string
	Value       string
	Sensitive   bool
	Category    string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type PipelineVariable struct {
	ID          string
	PipelineID  string
	OrgID       string
	Key         string
	Value       string
	Sensitive   bool
	Category    string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Pipeline struct {
	ID          string
	OrgID       string
	Name        string
	Description string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type PipelineStage struct {
	ID          string
	PipelineID  string
	WorkspaceID string
	StageOrder  int32
	AutoApply   bool
	OnFailure   string
	CreatedAt   time.Time
}

type PipelineStageWithWorkspace struct {
	PipelineStage
	WorkspaceName string
}

type PipelineRun struct {
	ID           string
	PipelineID   string
	OrgID        string
	Status       string
	CurrentStage int32
	TotalStages  int32
	CreatedBy    string
	StartedAt    time.Time
	FinishedAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type PipelineRunStage struct {
	ID            string
	PipelineRunID string
	StageID       string
	WorkspaceID   string
	RunID         *string
	StageOrder    int32
	Status        string
	AutoApply     bool
	OnFailure     string
	StartedAt     *time.Time
	FinishedAt    *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type PipelineRunStageWithWorkspace struct {
	PipelineRunStage
	WorkspaceName string
}

type Account struct {
	ID                  string
	OrgID               string
	Name                string
	Description         string
	AWSAccountID        string
	AssumeRoleARN       string
	ExternalIDEncrypted string
	DefaultRegion       string
	CreatedBy           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Cluster struct {
	ID                string
	OrgID             string
	AccountID         string
	Name              string
	Description       string
	Environment       string
	APIEndpoint       string
	CABundleEncrypted string
	SATokenEncrypted  string
	Region            string
	AuthMode          string
	EKSClusterName    string
	ConnectionStatus  string
	LastConnectedAt   *time.Time
	ConnectionError   string
	NodeCount         int32
	K8sVersion        string
	// Health projections written by the hub-side cluster-health watcher.
	ArgoCDSyncStatus     string
	ArgoCDHealthStatus   string
	ControlPlaneStatus   string
	PlatformVersion      string
	LastHealthObservedAt *time.Time
	CreatedBy            string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type Tenant struct {
	ID             string
	OrgID          string
	ClusterID      string
	Name           string
	Phase          string
	Spec           json.RawMessage
	Status         json.RawMessage
	LastObservedAt time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type TenantOperation struct {
	ID           string
	OrgID        string
	ClusterID    string
	TenantName   string
	Operation    string
	Status       string
	GitCommitSHA string
	Error        string
	ValuesJSON   json.RawMessage
	TemplateID   *string
	CreatedBy    string
	CreatedAt    time.Time
	CompletedAt  *time.Time
}

// ClusterOperation is the vend order-desk log row. spec_json holds the
// clusterspec.Input the portal templated the Cluster CR from; cluster_id is set
// when the watch-back auto-registers the vended cluster (status -> 'active').
type ClusterOperation struct {
	ID           string
	OrgID        string
	Name         string
	Environment  string
	Team         string
	Operation    string
	Status       string
	GitCommitSHA string
	Error        string
	SpecJSON     json.RawMessage
	ClusterID    *string
	CreatedBy    string
	CreatedAt    time.Time
	CompletedAt  *time.Time
	VendPhases   json.RawMessage
}

type Template struct {
	ID                   string
	OrgID                string
	Name                 string
	Description          string
	Persona              string
	DefaultValues        json.RawMessage
	AllowedOverrides     json.RawMessage
	MaxBudgetUSD         int32
	AllowedModelFamilies json.RawMessage
	RequiredCompliance   json.RawMessage
	CreatedBy            string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type TenantTeamAccess struct {
	ID         string
	OrgID      string
	ClusterID  string
	TenantName string
	TeamID     string
	GrantedBy  string
	GrantedAt  time.Time
}

type TemplateTeamAccess struct {
	ID         string
	OrgID      string
	TemplateID string
	TeamID     string
	GrantedBy  string
	GrantedAt  time.Time
}
