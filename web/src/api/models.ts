// Named aliases over the generated contract (./types.ts, generated from
// api/openapi.yaml). Components import domain types from here; only
// ./client.ts consumes the raw `paths` interface directly.

import type { components } from "./types";

type schemas = components["schemas"];

export type ErrorResponse = schemas["ErrorResponse"];
export type HealthStatus = schemas["HealthStatus"];

export type User = schemas["User"];
export type UpdateRoleRequest = schemas["UpdateRoleRequest"];

export type Workspace = schemas["Workspace"];
export type CreateWorkspaceRequest = schemas["CreateWorkspaceRequest"];
export type UpdateWorkspaceRequest = schemas["UpdateWorkspaceRequest"];
export type CloneWorkspaceRequest = schemas["CloneWorkspaceRequest"];

export type Run = schemas["Run"];
export type RunStatus = schemas["RunStatus"];
export type RunOperation = schemas["RunOperation"];
export type CreateRunRequest = schemas["CreateRunRequest"];
export type TofuPlanJSON = schemas["TofuPlanJSON"];
export type TofuResourceChange = schemas["TofuResourceChange"];

export type StateVersion = schemas["StateVersion"];
export type StateResource = schemas["StateResource"];
export type StateOutput = schemas["StateOutput"];
export type StateDiff = schemas["StateDiff"];
export type ResourceDiff = schemas["ResourceDiff"];

export type WorkspaceVariable = schemas["WorkspaceVariable"];
export type OrgVariable = schemas["OrgVariable"];
export type PipelineVariable = schemas["PipelineVariable"];
export type CreateVariableRequest = schemas["CreateVariableRequest"];
export type EffectiveVariable = schemas["EffectiveVariable"];
export type DiscoveredVariable = schemas["DiscoveredVariable"];

export type Team = schemas["Team"];
export type TeamMember = schemas["TeamMember"];
export type WorkspaceTeamAccess = schemas["WorkspaceTeamAccess"];

export type Approval = schemas["Approval"];
export type ApprovalRequest = schemas["ApprovalRequest"];
export type AuditLog = schemas["AuditLog"];

export type Pipeline = schemas["Pipeline"];
export type PipelineStage = schemas["PipelineStage"];
export type PipelineRun = schemas["PipelineRun"];
export type PipelineRunStatus = schemas["PipelineRunStatus"];
export type PipelineRunStage = schemas["PipelineRunStage"];
export type PipelineStageStatus = schemas["PipelineStageStatus"];
export type CreatePipelineStageInput = schemas["CreatePipelineStageInput"];
export type CreatePipelineRequest = schemas["CreatePipelineRequest"];
export type UpdatePipelineRequest = schemas["UpdatePipelineRequest"];
export type PipelineDetailResponse = schemas["PipelineDetailResponse"];
export type PipelineRunDetailResponse = schemas["PipelineRunDetailResponse"];

export type Account = schemas["Account"];
export type CreateAccountRequest = schemas["CreateAccountRequest"];
export type UpdateAccountRequest = schemas["UpdateAccountRequest"];

export type Cluster = schemas["Cluster"];
export type ClusterConnectionStatus = schemas["ClusterConnectionStatus"];
export type CreateClusterRequest = schemas["CreateClusterRequest"];
export type UpdateClusterRequest = schemas["UpdateClusterRequest"];

export type ClusterOrderInput = schemas["ClusterOrderInput"];
export type ClusterOperation = schemas["ClusterOperation"];
export type ClusterOperationKind = schemas["ClusterOperationKind"];
export type ClusterOperationStatus = schemas["ClusterOperationStatus"];
export type VendPhaseEntry = schemas["VendPhaseEntry"];

export type Tenant = schemas["Tenant"];
export type CreateTenantRequest = schemas["CreateTenantRequest"];
export type TenantOperation = schemas["TenantOperation"];
export type TenantOperationKind = schemas["TenantOperationKind"];
export type TenantOperationStatus = schemas["TenantOperationStatus"];
export type TenantTeamAccess = schemas["TenantTeamAccess"];

export type Template = schemas["Template"];
export type CreateTemplateRequest = schemas["CreateTemplateRequest"];
export type UpdateTemplateRequest = schemas["UpdateTemplateRequest"];
export type TemplateTeamAccess = schemas["TemplateTeamAccess"];

export type OpsFeedItem = schemas["OpsFeedItem"];

// The standard list envelope every collection endpoint returns
// (respond.ListResponse in the Go backend).
export interface ListResponse<T> {
  data: T[];
  total: number;
  page: number;
  per_page: number;
}
