package domain

import (
	"time"
)

// RunStatus represents the state machine for a run.
type RunStatus string

const (
	RunStatusPending          RunStatus = "pending"
	RunStatusQueued           RunStatus = "queued"
	RunStatusPlanning         RunStatus = "planning"
	RunStatusPlanned          RunStatus = "planned"
	RunStatusAwaitingApproval RunStatus = "awaiting_approval"
	RunStatusApplying         RunStatus = "applying"
	RunStatusApplied          RunStatus = "applied"
	RunStatusErrored          RunStatus = "errored"
	RunStatusCancelled        RunStatus = "cancelled"
	RunStatusDiscarded        RunStatus = "discarded"
)

// RunOperation is the type of OpenTofu operation.
type RunOperation string

const (
	RunOperationPlan    RunOperation = "plan"
	RunOperationApply   RunOperation = "apply"
	RunOperationDestroy RunOperation = "destroy"
)

// Role represents RBAC roles.
type Role string

const (
	RoleOwner    Role = "owner"
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
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
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	GitHubID    int64     `json:"github_id,omitempty"`
	OrgID       string    `json:"org_id"`
	Role        Role      `json:"role"`
	LastLoginAt time.Time `json:"last_login_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Workspace struct {
	ID                string    `json:"id"`
	OrgID             string    `json:"org_id"`
	Name              string    `json:"name"`
	Description       string    `json:"description,omitempty"`
	RepoURL           string    `json:"repo_url"`
	RepoBranch        string    `json:"repo_branch"`
	WorkingDir        string    `json:"working_dir"`
	TofuVersion       string    `json:"tofu_version"`
	Environment       string    `json:"environment"`
	AutoApply         bool      `json:"auto_apply"`
	RequiresApproval  bool      `json:"requires_approval"`
	VcsTriggerEnabled bool      `json:"vcs_trigger_enabled"`
	Locked            bool      `json:"locked"`
	LockedBy          *string   `json:"locked_by,omitempty"`
	CurrentRunID      *string   `json:"current_run_id,omitempty"`
	CreatedBy         string    `json:"created_by"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type Run struct {
	ID               string       `json:"id"`
	WorkspaceID      string       `json:"workspace_id"`
	OrgID            string       `json:"org_id"`
	Operation        RunOperation `json:"operation"`
	Status           RunStatus    `json:"status"`
	PlanOutput       string       `json:"plan_output,omitempty"`
	PlanLogURL       string       `json:"plan_log_url,omitempty"`
	ApplyLogURL      string       `json:"apply_log_url,omitempty"`
	ResourcesAdded   int          `json:"resources_added"`
	ResourcesChanged int          `json:"resources_changed"`
	ResourcesDeleted int          `json:"resources_deleted"`
	ErrorMessage     string       `json:"error_message,omitempty"`
	CommitSHA        string       `json:"commit_sha,omitempty"`
	CreatedBy        string       `json:"created_by"`
	StartedAt        *time.Time   `json:"started_at,omitempty"`
	FinishedAt       *time.Time   `json:"finished_at,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

type StateVersion struct {
	ID              string    `json:"id"`
	WorkspaceID     string    `json:"workspace_id"`
	OrgID           string    `json:"org_id"`
	RunID           string    `json:"run_id"`
	Serial          int       `json:"serial"`
	StateURL        string    `json:"state_url"`
	ResourceCount   int       `json:"resource_count"`
	ResourceSummary string    `json:"resource_summary,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type AuditLog struct {
	ID         string    `json:"id"`
	OrgID      string    `json:"org_id"`
	UserID     string    `json:"user_id"`
	Action     string    `json:"action"`
	EntityType string    `json:"entity_type"`
	EntityID   string    `json:"entity_id"`
	Before     string    `json:"before,omitempty"`
	After      string    `json:"after,omitempty"`
	IPAddress  string    `json:"ip_address,omitempty"`
	UserAgent  string    `json:"user_agent,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}
