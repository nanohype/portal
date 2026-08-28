// Named aliases over the generated contract (./gen/types.gen.ts, generated
// from api/openapi.yaml). Components import domain types from here.

import type * as gen from './gen/types.gen';

export type ErrorResponse = gen.ErrorResponse;
export type HealthStatus = gen.HealthStatus;

export type User = gen.User;
export type UpdateRoleRequest = gen.UpdateRoleRequest;

export type Workspace = gen.Workspace;
export type CreateWorkspaceRequest = gen.CreateWorkspaceRequest;
export type UpdateWorkspaceRequest = gen.UpdateWorkspaceRequest;
export type CloneWorkspaceRequest = gen.CloneWorkspaceRequest;

export type Run = gen.Run;
export type RunStatus = gen.RunStatus;
export type RunOperation = gen.RunOperation;
export type CreateRunRequest = gen.CreateRunRequest;
export type TofuPlanJSON = gen.TofuPlanJson;
export type TofuResourceChange = gen.TofuResourceChange;

export type StateVersion = gen.StateVersion;
export type StateResource = gen.StateResource;
export type StateOutput = gen.StateOutput;
export type StateDiff = gen.StateDiff;
export type ResourceDiff = gen.ResourceDiff;

export type WorkspaceVariable = gen.WorkspaceVariable;
export type OrgVariable = gen.OrgVariable;
export type PipelineVariable = gen.PipelineVariable;
export type CreateVariableRequest = gen.CreateVariableRequest;
export type EffectiveVariable = gen.EffectiveVariable;
export type DiscoveredVariable = gen.DiscoveredVariable;

export type Team = gen.Team;
export type TeamMember = gen.TeamMember;
export type WorkspaceTeamAccess = gen.WorkspaceTeamAccess;

export type Approval = gen.Approval;
export type ApprovalRequest = gen.ApprovalRequest;
export type AuditLog = gen.AuditLog;

export type Pipeline = gen.Pipeline;
export type PipelineStage = gen.PipelineStage;
export type PipelineRun = gen.PipelineRun;
export type PipelineRunStatus = gen.PipelineRunStatus;
export type PipelineRunStage = gen.PipelineRunStage;
export type PipelineStageStatus = gen.PipelineStageStatus;
export type CreatePipelineStageInput = gen.CreatePipelineStageInput;
export type CreatePipelineRequest = gen.CreatePipelineRequest;
export type UpdatePipelineRequest = gen.UpdatePipelineRequest;
export type PipelineDetailResponse = gen.PipelineDetailResponse;
export type PipelineRunDetailResponse = gen.PipelineRunDetailResponse;

export type Account = gen.Account;
export type CreateAccountRequest = gen.CreateAccountRequest;
export type UpdateAccountRequest = gen.UpdateAccountRequest;

export type Cluster = gen.Cluster;
export type ClusterConnectionStatus = gen.ClusterConnectionStatus;
export type CreateClusterRequest = gen.CreateClusterRequest;
export type UpdateClusterRequest = gen.UpdateClusterRequest;

export type ClusterOrderInput = gen.ClusterOrderInput;
export type ClusterOrderNetwork = gen.ClusterOrderNetwork;
export type ClusterOrderNetworkCreate = gen.ClusterOrderNetworkCreate;
export type ClusterOrderNetworkAdopt = gen.ClusterOrderNetworkAdopt;
export type ClusterOrderSystemNodes = gen.ClusterOrderSystemNodes;
export type ClusterOperation = gen.ClusterOperation;
export type ClusterOperationKind = gen.ClusterOperationKind;
export type ClusterOperationStatus = gen.ClusterOperationStatus;
export type VendPhaseEntry = gen.VendPhaseEntry;

export type Tenant = gen.Tenant;
export type CreateTenantRequest = gen.CreateTenantRequest;
export type TenantOperation = gen.TenantOperation;
export type TenantOperationKind = gen.TenantOperationKind;
export type TenantOperationStatus = gen.TenantOperationStatus;
export type TenantTeamAccess = gen.TenantTeamAccess;

export type Template = gen.Template;
export type CreateTemplateRequest = gen.CreateTemplateRequest;
export type UpdateTemplateRequest = gen.UpdateTemplateRequest;
export type TemplateTeamAccess = gen.TemplateTeamAccess;

export type OpsFeedItem = gen.OpsFeedItem;

// The standard list envelope every collection endpoint returns
// (respond.ListResponse in the Go backend).
export interface ListResponse<T> {
  data: T[];
  total: number;
  page: number;
  per_page: number;
}
