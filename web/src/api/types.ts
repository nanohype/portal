// API types matching the OpenAPI spec
// In production, these would be generated via openapi-typescript

export interface User {
  id: string;
  org_id: string;
  email: string;
  name: string;
  avatar_url?: string;
  role: "owner" | "admin" | "operator" | "viewer";
  last_login_at?: string;
  created_at: string;
  updated_at: string;
}

export interface Workspace {
  id: string;
  org_id: string;
  name: string;
  description?: string;
  source: "vcs" | "upload";
  repo_url: string;
  repo_branch: string;
  working_dir: string;
  tofu_version: string;
  environment: "development" | "staging" | "production";
  auto_apply: boolean;
  requires_approval: boolean;
  vcs_trigger_enabled: boolean;
  locked: boolean;
  locked_by?: string | null;
  current_run_id?: string | null;
  current_config_version_id?: string;
  created_by: string;
  created_at: string;
  updated_at: string;
  last_run_status?: string | null;
  last_run_at?: string | null;
  resource_count?: number;
}

export type RunStatus =
  | "pending"
  | "queued"
  | "planning"
  | "planned"
  | "awaiting_approval"
  | "applying"
  | "applied"
  | "errored"
  | "cancelled"
  | "discarded";

export type RunOperation = "plan" | "apply" | "destroy" | "import" | "test";

export interface Run {
  id: string;
  workspace_id: string;
  org_id: string;
  operation: RunOperation;
  status: RunStatus;
  plan_output?: string;
  plan_log_url?: string;
  apply_log_url?: string;
  resources_added?: number;
  resources_changed?: number;
  resources_deleted?: number;
  error_message?: string;
  commit_sha?: string;
  plan_json_url?: string;
  created_by: string;
  started_at?: string | null;
  finished_at?: string | null;
  created_at: string;
  updated_at: string;
}

export interface TofuResourceChange {
  address: string;
  module_address?: string;
  mode: string;
  type: string;
  name: string;
  provider_name: string;
  change: {
    actions: string[];
    before: Record<string, unknown> | null;
    after: Record<string, unknown> | null;
  };
}

export interface TofuPlanJSON {
  format_version?: string;
  resource_changes?: TofuResourceChange[];
}

export interface StateVersion {
  id: string;
  workspace_id: string;
  org_id: string;
  run_id: string;
  serial: number;
  state_url: string;
  resource_count: number;
  resource_summary: string;
  created_at: string;
}

export interface StateResource {
  type: string;
  name: string;
  module: string;
  provider: string;
  mode: string;
  attributes: Record<string, unknown>;
}

export interface ResourceDiff {
  type: string;
  name: string;
  module: string;
  action: "added" | "removed" | "changed" | "unchanged";
  before?: Record<string, unknown>;
  after?: Record<string, unknown>;
  changed_keys?: string[];
}

export interface StateDiff {
  added: number;
  removed: number;
  changed: number;
  unchanged: number;
  diffs: ResourceDiff[];
}

export interface WorkspaceVariable {
  id: string;
  workspace_id: string;
  org_id: string;
  key: string;
  value: string;
  sensitive: boolean;
  category: "terraform" | "env";
  description: string;
  created_at: string;
  updated_at: string;
}

export interface Team {
  id: string;
  org_id: string;
  name: string;
  slug: string;
  created_at: string;
  updated_at: string;
}

export interface TeamMember {
  id: string;
  team_id: string;
  user_id: string;
  role: string;
  cloud_identity: string;
  created_at: string;
  email: string;
  user_name: string;
  avatar_url: string;
}

export interface WorkspaceTeamAccess {
  id: string;
  workspace_id: string;
  team_id: string;
  role: string;
  created_at: string;
  team_name: string;
  team_slug: string;
}

export interface Approval {
  id: string;
  run_id: string;
  org_id: string;
  user_id: string;
  status: "approved" | "rejected";
  comment: string;
  created_at: string;
  user_name?: string;
  avatar_url?: string;
}

export interface AuditLog {
  id: string;
  org_id: string;
  user_id: string;
  action: string;
  entity_type: string;
  entity_id: string;
  before_data: unknown;
  after_data: unknown;
  ip_address: string;
  user_agent: string;
  created_at: string;
}

export interface ListResponse<T> {
  data: T[];
  total: number;
  page: number;
  per_page: number;
}

export interface CreateWorkspaceRequest {
  name: string;
  description?: string;
  source?: "vcs" | "upload";
  repo_url?: string;
  repo_branch?: string;
  working_dir?: string;
  tofu_version?: string;
  environment?: "development" | "staging" | "production";
  auto_apply?: boolean;
  requires_approval?: boolean;
  vcs_trigger_enabled?: boolean;
}

export interface UpdateWorkspaceRequest {
  name?: string;
  description?: string;
  repo_url?: string;
  repo_branch?: string;
  working_dir?: string;
  tofu_version?: string;
  environment?: "development" | "staging" | "production";
  auto_apply?: boolean;
  requires_approval?: boolean;
  vcs_trigger_enabled?: boolean;
}

export interface CreateRunRequest {
  operation?: RunOperation;
  imports?: { address: string; id: string }[];
}

export interface CreateVariableRequest {
  key: string;
  value: string;
  sensitive: boolean;
  category: "terraform" | "env";
  description?: string;
}

export interface ApprovalRequest {
  status: "approved" | "rejected";
  comment?: string;
}

export interface WorkspaceOutput {
  name: string;
  value: string;
  type: string;
}

export interface StateOutput {
  name: string;
  value: unknown;
  type: string;
  sensitive: boolean;
}

export interface CloneWorkspaceRequest {
  name: string;
  description?: string;
  environment?: string;
}

export interface DiscoveredVariable {
  name: string;
  type?: string;
  description?: string;
  default?: string;
  required: boolean;
  configured: boolean;
  configured_by?: "terragrunt" | "portal";
}

export interface Pipeline {
  id: string;
  org_id: string;
  name: string;
  description: string;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface PipelineStage {
  id: string;
  pipeline_id: string;
  workspace_id: string;
  stage_order: number;
  auto_apply: boolean;
  on_failure: "stop" | "continue";
  created_at: string;
  workspace_name: string;
}

export type PipelineRunStatus = "idle" | "running" | "completed" | "errored" | "cancelled";

export interface PipelineRun {
  id: string;
  pipeline_id: string;
  org_id: string;
  status: PipelineRunStatus;
  current_stage: number;
  total_stages: number;
  created_by: string;
  started_at: string;
  finished_at?: string | null;
  created_at: string;
  updated_at: string;
}

export type PipelineStageStatus =
  | "pending"
  | "importing_outputs"
  | "running"
  | "awaiting_approval"
  | "completed"
  | "errored"
  | "skipped"
  | "cancelled";

export interface PipelineRunStage {
  id: string;
  pipeline_run_id: string;
  stage_id: string;
  workspace_id: string;
  run_id?: string | null;
  stage_order: number;
  status: PipelineStageStatus;
  auto_apply: boolean;
  on_failure: "stop" | "continue";
  started_at?: string | null;
  finished_at?: string | null;
  created_at: string;
  updated_at: string;
  workspace_name: string;
}

export interface CreatePipelineStageInput {
  workspace_id: string;
  auto_apply: boolean;
  on_failure: "stop" | "continue";
}

export interface CreatePipelineRequest {
  name: string;
  description?: string;
  stages: CreatePipelineStageInput[];
}

export interface UpdatePipelineRequest {
  name?: string;
  description?: string;
  stages?: CreatePipelineStageInput[];
}

export interface PipelineDetailResponse {
  pipeline: Pipeline;
  stages: PipelineStage[];
}

export interface PipelineRunDetailResponse {
  pipeline_run: PipelineRun;
  stages: PipelineRunStage[];
}

export interface OrgVariable {
  id: string;
  org_id: string;
  key: string;
  value: string;
  sensitive: boolean;
  category: "terraform" | "env";
  description: string;
  created_at: string;
  updated_at: string;
}

export interface PipelineVariable {
  id: string;
  pipeline_id: string;
  org_id: string;
  key: string;
  value: string;
  sensitive: boolean;
  category: "terraform" | "env";
  description: string;
  created_at: string;
  updated_at: string;
}

export interface EffectiveVariable {
  key: string;
  value: string;
  sensitive: boolean;
  category: "terraform" | "env";
  description: string;
  source: "org" | "pipeline" | "workspace";
  source_id: string;
}

export interface ErrorResponse {
  error: string;
  message?: string;
}

export interface UpdateRoleRequest {
  role: "owner" | "admin" | "operator" | "viewer";
}

export interface Account {
  id: string;
  org_id: string;
  name: string;
  description: string;
  aws_account_id: string;
  assume_role_arn: string;
  external_id_set: boolean;
  default_region: string;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface CreateAccountRequest {
  name: string;
  description?: string;
  aws_account_id: string;
  assume_role_arn: string;
  external_id?: string;
  default_region: string;
}

export interface UpdateAccountRequest {
  name?: string;
  description?: string;
  assume_role_arn?: string;
  external_id?: string;
  default_region?: string;
}

export type ClusterConnectionStatus =
  | "pending"
  | "connecting"
  | "connected"
  | "failed";

export interface Cluster {
  id: string;
  org_id: string;
  account_id: string;
  name: string;
  description: string;
  environment: "development" | "staging" | "production";
  api_endpoint: string;
  region: string;
  connection_status: ClusterConnectionStatus;
  last_connected_at: string | null;
  connection_error: string;
  node_count: number;
  k8s_version: string;
  credentials_set: boolean;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface CreateClusterRequest {
  account_id: string;
  name: string;
  description?: string;
  environment?: "development" | "staging" | "production";
  api_endpoint: string;
  ca_bundle: string;
  sa_token: string;
  region?: string;
}

export interface UpdateClusterRequest {
  name?: string;
  description?: string;
  environment?: string;
  api_endpoint?: string;
  ca_bundle?: string;
  sa_token?: string;
  region?: string;
}

// ClusterOrderInput is the vend form: portal templates an eks-fleet Cluster CR
// from it and commits it to the clusters GitOps repo. account is the 12-digit
// AWS account number (not a portal account id); the rest default server-side.
export interface ClusterOrderInput {
  name: string;
  account: string;
  region: string;
  team: string;
  environment?: "dev" | "staging" | "production";
  cluster_version?: string;
  endpoint_public_access?: boolean;
}

export type ClusterOperationKind = "provision" | "deprovision";
export type ClusterOperationStatus =
  | "pending"
  | "committed"
  | "failed"
  | "active"
  | "deprovisioned";

export interface ClusterOperation {
  id: string;
  org_id: string;
  name: string;
  environment: string;
  team: string;
  operation: ClusterOperationKind;
  status: ClusterOperationStatus;
  git_commit_sha: string;
  error: string;
  spec_json: Record<string, unknown>;
  cluster_id: string | null;
  created_by: string;
  created_at: string;
  completed_at: string | null;
  // Regressible map keyed by phase (committed, tofu_running, active, ...). Only
  // portal-side phases advance locally; substrate phases need the in-cluster watcher.
  vend_phases: Record<string, VendPhaseEntry>;
}

export interface VendPhaseEntry {
  at: string;
  detail?: string;
}

export interface Tenant {
  id: string;
  org_id: string;
  cluster_id: string;
  name: string;
  phase: string;
  spec: Record<string, unknown>;
  status: Record<string, unknown>;
  last_observed_at: string;
  created_at: string;
  updated_at: string;
}

export interface CreateTenantRequest {
  cluster_id: string;
  name: string;
  values: Record<string, unknown>;
  template_id?: string;
  owning_team_id?: string;
}

export interface TenantTeamAccess {
  id: string;
  org_id: string;
  cluster_id: string;
  tenant_name: string;
  team_id: string;
  granted_by: string;
  granted_at: string;
}

export interface TemplateTeamAccess {
  id: string;
  org_id: string;
  template_id: string;
  team_id: string;
  granted_by: string;
  granted_at: string;
}

export interface Template {
  id: string;
  org_id: string;
  name: string;
  description: string;
  persona: string;
  default_values: Record<string, unknown>;
  allowed_overrides: string[];
  max_budget_usd: number;
  allowed_model_families: string[];
  required_compliance: string[];
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface CreateTemplateRequest {
  name: string;
  description?: string;
  persona: string;
  default_values?: Record<string, unknown>;
  allowed_overrides?: string[];
  max_budget_usd?: number;
  allowed_model_families?: string[];
  required_compliance?: string[];
}

export interface UpdateTemplateRequest {
  name?: string;
  description?: string;
  persona?: string;
  default_values?: Record<string, unknown>;
  allowed_overrides?: string[];
  max_budget_usd?: number;
  allowed_model_families?: string[];
  required_compliance?: string[];
}

export type TenantOperationKind = "create" | "delete";
export type TenantOperationStatus = "pending" | "committed" | "failed";

export interface TenantOperation {
  id: string;
  org_id: string;
  cluster_id: string;
  tenant_name: string;
  operation: TenantOperationKind;
  status: TenantOperationStatus;
  git_commit_sha: string;
  error: string;
  values_json: Record<string, unknown>;
  template_id: string | null;
  created_by: string;
  created_at: string;
  completed_at: string | null;
}

// openapi-fetch compatible paths type
export interface paths {
  "/users": {
    get: {
      responses: {
        200: { content: { "application/json": User[] } };
      };
    };
  };
  "/users/{userId}/role": {
    put: {
      parameters: { path: { userId: string } };
      requestBody: {
        content: { "application/json": UpdateRoleRequest };
      };
      responses: {
        200: { content: { "application/json": User } };
      };
    };
  };
  "/audit-logs": {
    get: {
      parameters: { query?: { page?: number; per_page?: number } };
      responses: {
        200: { content: { "application/json": AuditLog[] } };
      };
    };
  };
  "/variables": {
    get: {
      responses: {
        200: { content: { "application/json": OrgVariable[] } };
      };
    };
    post: {
      requestBody: {
        content: { "application/json": CreateVariableRequest };
      };
      responses: {
        201: { content: { "application/json": OrgVariable } };
      };
    };
  };
  "/variables/{variableId}": {
    put: {
      parameters: { path: { variableId: string } };
      requestBody: {
        content: { "application/json": CreateVariableRequest };
      };
      responses: {
        200: { content: { "application/json": OrgVariable } };
      };
    };
    delete: {
      parameters: { path: { variableId: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/variables/{variableId}/value": {
    get: {
      parameters: { path: { variableId: string } };
      responses: {
        200: { content: { "application/json": { value: string } } };
      };
    };
  };
  "/health": {
    get: {
      responses: {
        200: { content: { "application/json": { status: string } } };
      };
    };
  };
  "/auth/me": {
    get: {
      responses: {
        200: { content: { "application/json": User } };
        401: { content: { "application/json": ErrorResponse } };
      };
    };
  };
  "/accounts": {
    get: {
      parameters: { query?: { page?: number; per_page?: number } };
      responses: {
        200: { content: { "application/json": ListResponse<Account> } };
      };
    };
    post: {
      requestBody: { content: { "application/json": CreateAccountRequest } };
      responses: {
        201: { content: { "application/json": Account } };
      };
    };
  };
  "/accounts/{accountId}": {
    get: {
      parameters: { path: { accountId: string } };
      responses: {
        200: { content: { "application/json": Account } };
      };
    };
    put: {
      parameters: { path: { accountId: string } };
      requestBody: { content: { "application/json": UpdateAccountRequest } };
      responses: {
        200: { content: { "application/json": Account } };
      };
    };
    delete: {
      parameters: { path: { accountId: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/clusters": {
    get: {
      parameters: {
        query?: { page?: number; per_page?: number; account_id?: string };
      };
      responses: {
        200: { content: { "application/json": ListResponse<Cluster> } };
      };
    };
    post: {
      requestBody: { content: { "application/json": CreateClusterRequest } };
      responses: {
        201: { content: { "application/json": Cluster } };
      };
    };
  };
  "/clusters/{clusterId}": {
    get: {
      parameters: { path: { clusterId: string } };
      responses: {
        200: { content: { "application/json": Cluster } };
      };
    };
    put: {
      parameters: { path: { clusterId: string } };
      requestBody: { content: { "application/json": UpdateClusterRequest } };
      responses: {
        200: { content: { "application/json": Cluster } };
      };
    };
    delete: {
      parameters: { path: { clusterId: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/clusters/{clusterId}/test-connection": {
    post: {
      parameters: { path: { clusterId: string } };
      responses: {
        202: { content: { "application/json": { status: string } } };
      };
    };
  };
  "/cluster-orders": {
    get: {
      responses: {
        200: { content: { "application/json": ClusterOperation[] } };
      };
    };
    post: {
      requestBody: { content: { "application/json": ClusterOrderInput } };
      responses: {
        202: { content: { "application/json": ClusterOperation } };
      };
    };
  };
  "/cluster-orders/{environment}/{name}/operations": {
    get: {
      parameters: { path: { environment: string; name: string } };
      responses: {
        200: { content: { "application/json": ListResponse<ClusterOperation> } };
      };
    };
  };
  "/tenants": {
    get: {
      parameters: {
        query?: { page?: number; per_page?: number; cluster_id?: string };
      };
      responses: {
        200: { content: { "application/json": ListResponse<Tenant> } };
      };
    };
    post: {
      requestBody: { content: { "application/json": CreateTenantRequest } };
      responses: {
        202: { content: { "application/json": TenantOperation } };
      };
    };
  };
  "/tenants/{tenantId}": {
    get: {
      parameters: { path: { tenantId: string } };
      responses: {
        200: { content: { "application/json": Tenant } };
      };
    };
    delete: {
      parameters: { path: { tenantId: string } };
      responses: {
        202: { content: { "application/json": TenantOperation } };
      };
    };
  };
  "/tenants/{tenantId}/operations": {
    get: {
      parameters: { path: { tenantId: string } };
      responses: {
        200: { content: { "application/json": TenantOperation[] } };
      };
    };
  };
  "/templates": {
    get: {
      responses: {
        200: { content: { "application/json": Template[] } };
      };
    };
    post: {
      requestBody: { content: { "application/json": CreateTemplateRequest } };
      responses: {
        201: { content: { "application/json": Template } };
      };
    };
  };
  "/templates/{templateID}": {
    get: {
      parameters: { path: { templateID: string } };
      responses: {
        200: { content: { "application/json": Template } };
      };
    };
    put: {
      parameters: { path: { templateID: string } };
      requestBody: { content: { "application/json": UpdateTemplateRequest } };
      responses: {
        200: { content: { "application/json": Template } };
      };
    };
    delete: {
      parameters: { path: { templateID: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/templates/{templateID}/access": {
    get: {
      parameters: { path: { templateID: string } };
      responses: {
        200: { content: { "application/json": TemplateTeamAccess[] } };
      };
    };
    post: {
      parameters: { path: { templateID: string } };
      requestBody: { content: { "application/json": { team_id: string } } };
      responses: {
        201: { content: { "application/json": TemplateTeamAccess } };
      };
    };
  };
  "/templates/{templateID}/access/{teamId}": {
    delete: {
      parameters: { path: { templateID: string; teamId: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/tenants/{tenantId}/access": {
    get: {
      parameters: { path: { tenantId: string } };
      responses: {
        200: { content: { "application/json": TenantTeamAccess[] } };
      };
    };
    post: {
      parameters: { path: { tenantId: string } };
      requestBody: { content: { "application/json": { team_id: string } } };
      responses: {
        201: { content: { "application/json": TenantTeamAccess } };
      };
    };
  };
  "/tenants/{tenantId}/access/{teamId}": {
    delete: {
      parameters: { path: { tenantId: string; teamId: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/teams": {
    get: {
      parameters: { query?: { member_of?: "me" } };
      responses: {
        200: { content: { "application/json": Team[] } };
      };
    };
    post: {
      requestBody: { content: { "application/json": { name: string } } };
      responses: {
        201: { content: { "application/json": Team } };
      };
    };
  };
  "/teams/{teamId}": {
    get: {
      parameters: { path: { teamId: string } };
      responses: {
        200: { content: { "application/json": Team } };
      };
    };
    delete: {
      parameters: { path: { teamId: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/teams/{teamId}/members": {
    get: {
      parameters: { path: { teamId: string } };
      responses: {
        200: { content: { "application/json": TeamMember[] } };
      };
    };
    post: {
      parameters: { path: { teamId: string } };
      requestBody: {
        content: {
          "application/json": { user_id: string; role: string; cloud_identity?: string };
        };
      };
      responses: {
        201: { content: { "application/json": TeamMember } };
      };
    };
  };
  "/teams/{teamId}/members/{userId}": {
    put: {
      parameters: { path: { teamId: string; userId: string } };
      requestBody: {
        content: {
          "application/json": { role: string; cloud_identity?: string };
        };
      };
      responses: {
        200: { content: { "application/json": TeamMember } };
      };
    };
    delete: {
      parameters: { path: { teamId: string; userId: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/workspaces": {
    get: {
      parameters: { query?: { page?: number; per_page?: number; search?: string; environment?: string } };
      responses: {
        200: { content: { "application/json": ListResponse<Workspace> } };
      };
    };
    post: {
      requestBody: { content: { "application/json": CreateWorkspaceRequest } };
      responses: {
        201: { content: { "application/json": Workspace } };
      };
    };
  };
  "/workspaces/{workspaceId}": {
    get: {
      parameters: { path: { workspaceId: string } };
      responses: {
        200: { content: { "application/json": Workspace } };
        404: { content: { "application/json": ErrorResponse } };
      };
    };
    put: {
      parameters: { path: { workspaceId: string } };
      requestBody: {
        content: { "application/json": UpdateWorkspaceRequest };
      };
      responses: {
        200: { content: { "application/json": Workspace } };
      };
    };
    delete: {
      parameters: { path: { workspaceId: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/workspaces/{workspaceId}/upload": {
    post: {
      parameters: { path: { workspaceId: string } };
      // Note: multipart/form-data, handled via fetch directly
      responses: {
        200: { content: { "application/json": Workspace } };
      };
    };
  };
  "/workspaces/{workspaceId}/clone": {
    post: {
      parameters: { path: { workspaceId: string } };
      requestBody: {
        content: { "application/json": CloneWorkspaceRequest };
      };
      responses: {
        201: { content: { "application/json": Workspace } };
      };
    };
  };
  "/workspaces/{workspaceId}/lock": {
    post: {
      parameters: { path: { workspaceId: string } };
      responses: {
        200: { content: { "application/json": Workspace } };
      };
    };
  };
  "/workspaces/{workspaceId}/unlock": {
    post: {
      parameters: { path: { workspaceId: string } };
      responses: {
        200: { content: { "application/json": Workspace } };
      };
    };
  };
  "/workspaces/{workspaceId}/variables": {
    get: {
      parameters: { path: { workspaceId: string } };
      responses: {
        200: {
          content: { "application/json": WorkspaceVariable[] };
        };
      };
    };
    post: {
      parameters: { path: { workspaceId: string } };
      requestBody: {
        content: { "application/json": CreateVariableRequest };
      };
      responses: {
        201: { content: { "application/json": WorkspaceVariable } };
      };
    };
  };
  "/workspaces/{workspaceId}/variables/discover": {
    post: {
      parameters: { path: { workspaceId: string } };
      responses: {
        200: {
          content: { "application/json": DiscoveredVariable[] };
        };
      };
    };
  };
  "/workspaces/{workspaceId}/variables/bulk": {
    post: {
      parameters: { path: { workspaceId: string } };
      requestBody: {
        content: {
          "application/json": { variables: CreateVariableRequest[] };
        };
      };
      responses: {
        201: { content: { "application/json": WorkspaceVariable[] } };
      };
    };
  };
  "/workspaces/{workspaceId}/variables/copy": {
    post: {
      parameters: { path: { workspaceId: string } };
      requestBody: {
        content: {
          "application/json": { source_workspace_id: string };
        };
      };
      responses: {
        201: { content: { "application/json": WorkspaceVariable[] } };
      };
    };
  };
  "/workspaces/{workspaceId}/variables/{variableId}": {
    put: {
      parameters: { path: { workspaceId: string; variableId: string } };
      requestBody: {
        content: { "application/json": CreateVariableRequest };
      };
      responses: {
        200: { content: { "application/json": WorkspaceVariable } };
      };
    };
    delete: {
      parameters: { path: { workspaceId: string; variableId: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/workspaces/{workspaceId}/variables/{variableId}/value": {
    get: {
      parameters: { path: { workspaceId: string; variableId: string } };
      responses: {
        200: { content: { "application/json": { value: string } } };
      };
    };
  };
  "/workspaces/{workspaceId}/state": {
    get: {
      parameters: { path: { workspaceId: string } };
      responses: {
        200: { content: { "application/json": StateVersion[] } };
      };
    };
  };
  "/workspaces/{workspaceId}/state/current": {
    get: {
      parameters: { path: { workspaceId: string } };
      responses: {
        200: { content: { "application/json": StateVersion } };
        404: { content: { "application/json": ErrorResponse } };
      };
    };
  };
  "/workspaces/{workspaceId}/state/current/resources": {
    get: {
      parameters: { path: { workspaceId: string } };
      responses: {
        200: { content: { "application/json": StateResource[] } };
      };
    };
  };
  "/workspaces/{workspaceId}/state/current/outputs": {
    get: {
      parameters: { path: { workspaceId: string } };
      responses: {
        200: { content: { "application/json": StateOutput[] } };
      };
    };
  };
  "/workspaces/{workspaceId}/variables/import-outputs": {
    post: {
      parameters: { path: { workspaceId: string } };
      requestBody: {
        content: { "application/json": { source_workspace_id: string } };
      };
      responses: {
        201: { content: { "application/json": WorkspaceVariable[] } };
      };
    };
  };
  "/workspaces/{workspaceId}/state/diff": {
    get: {
      parameters: {
        path: { workspaceId: string };
        query: { from: number; to: number };
      };
      responses: {
        200: { content: { "application/json": StateDiff } };
      };
    };
  };
  "/workspaces/{workspaceId}/state/{stateId}": {
    get: {
      parameters: { path: { workspaceId: string; stateId: string } };
      responses: {
        200: { content: { "application/json": StateVersion } };
      };
    };
  };
  "/workspaces/{workspaceId}/state/serial/{serial}": {
    delete: {
      parameters: { path: { workspaceId: string; serial: number } };
      responses: {
        204: { content: object };
        200: { content: { "application/json": { deleted: StateVersion; storage_error: string } } };
      };
    };
  };
  "/workspaces/{workspaceId}/access": {
    get: {
      parameters: { path: { workspaceId: string } };
      responses: {
        200: { content: { "application/json": WorkspaceTeamAccess[] } };
      };
    };
    post: {
      parameters: { path: { workspaceId: string } };
      requestBody: {
        content: {
          "application/json": { team_id: string; role: string };
        };
      };
      responses: {
        201: { content: { "application/json": WorkspaceTeamAccess } };
      };
    };
  };
  "/workspaces/{workspaceId}/access/{teamId}": {
    delete: {
      parameters: { path: { workspaceId: string; teamId: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/workspaces/{workspaceId}/runs": {
    get: {
      parameters: {
        path: { workspaceId: string };
        query?: { page?: number; per_page?: number };
      };
      responses: {
        200: { content: { "application/json": ListResponse<Run> } };
      };
    };
    post: {
      parameters: { path: { workspaceId: string } };
      requestBody: { content: { "application/json": CreateRunRequest } };
      responses: {
        201: { content: { "application/json": Run } };
      };
    };
  };
  "/workspaces/{workspaceId}/runs/{runId}": {
    get: {
      parameters: { path: { workspaceId: string; runId: string } };
      responses: {
        200: { content: { "application/json": Run } };
      };
    };
  };
  "/workspaces/{workspaceId}/runs/{runId}/plan-json": {
    get: {
      parameters: { path: { workspaceId: string; runId: string } };
      responses: {
        200: { content: { "application/json": TofuPlanJSON } };
      };
    };
  };
  "/workspaces/{workspaceId}/runs/{runId}/cancel": {
    post: {
      parameters: { path: { workspaceId: string; runId: string } };
      responses: {
        200: { content: { "application/json": Run } };
      };
    };
  };
  "/pipelines": {
    get: {
      responses: {
        200: { content: { "application/json": Pipeline[] } };
      };
    };
    post: {
      requestBody: {
        content: { "application/json": CreatePipelineRequest };
      };
      responses: {
        201: { content: { "application/json": Pipeline } };
      };
    };
  };
  "/pipelines/{pipelineId}": {
    get: {
      parameters: { path: { pipelineId: string } };
      responses: {
        200: { content: { "application/json": PipelineDetailResponse } };
      };
    };
    put: {
      parameters: { path: { pipelineId: string } };
      requestBody: {
        content: { "application/json": UpdatePipelineRequest };
      };
      responses: {
        200: { content: { "application/json": Pipeline } };
      };
    };
    delete: {
      parameters: { path: { pipelineId: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/pipelines/{pipelineId}/runs": {
    get: {
      parameters: {
        path: { pipelineId: string };
        query?: { page?: number; per_page?: number };
      };
      responses: {
        200: { content: { "application/json": ListResponse<PipelineRun> } };
      };
    };
    post: {
      parameters: { path: { pipelineId: string } };
      responses: {
        201: { content: { "application/json": PipelineRun } };
      };
    };
  };
  "/pipelines/{pipelineId}/runs/{runId}": {
    get: {
      parameters: { path: { pipelineId: string; runId: string } };
      responses: {
        200: { content: { "application/json": PipelineRunDetailResponse } };
      };
    };
  };
  "/pipelines/{pipelineId}/runs/{runId}/cancel": {
    post: {
      parameters: { path: { pipelineId: string; runId: string } };
      responses: {
        200: { content: { "application/json": PipelineRun } };
      };
    };
  };
  "/pipelines/{pipelineId}/variables": {
    get: {
      parameters: { path: { pipelineId: string } };
      responses: {
        200: { content: { "application/json": PipelineVariable[] } };
      };
    };
    post: {
      parameters: { path: { pipelineId: string } };
      requestBody: {
        content: { "application/json": CreateVariableRequest };
      };
      responses: {
        201: { content: { "application/json": PipelineVariable } };
      };
    };
  };
  "/pipelines/{pipelineId}/variables/{variableId}": {
    put: {
      parameters: { path: { pipelineId: string; variableId: string } };
      requestBody: {
        content: { "application/json": CreateVariableRequest };
      };
      responses: {
        200: { content: { "application/json": PipelineVariable } };
      };
    };
    delete: {
      parameters: { path: { pipelineId: string; variableId: string } };
      responses: {
        204: { content: never };
      };
    };
  };
  "/pipelines/{pipelineId}/variables/{variableId}/value": {
    get: {
      parameters: { path: { pipelineId: string; variableId: string } };
      responses: {
        200: { content: { "application/json": { value: string } } };
      };
    };
  };
  "/workspaces/{workspaceId}/variables/effective": {
    get: {
      parameters: {
        path: { workspaceId: string };
        query?: { pipeline_id?: string };
      };
      responses: {
        200: { content: { "application/json": EffectiveVariable[] } };
      };
    };
  };
  "/workspaces/{workspaceId}/runs/{runId}/approvals": {
    get: {
      parameters: { path: { workspaceId: string; runId: string } };
      responses: {
        200: { content: { "application/json": Approval[] } };
      };
    };
    post: {
      parameters: { path: { workspaceId: string; runId: string } };
      requestBody: {
        content: { "application/json": ApprovalRequest };
      };
      responses: {
        201: { content: { "application/json": Approval } };
      };
    };
  };
}
