-- Portal schema
-- Uses ULIDs for sortable unique IDs, stored as TEXT

-- Custom types
CREATE TYPE run_status AS ENUM (
    'pending',
    'queued',
    'planning',
    'planned',
    'awaiting_approval',
    'applying',
    'applied',
    'errored',
    'cancelled',
    'discarded'
);

CREATE TYPE run_operation AS ENUM (
    'plan',
    'apply',
    'destroy',
    'import',
    'test'
);

CREATE TYPE user_role AS ENUM (
    'owner',
    'admin',
    'operator',
    'viewer'
);

-- Organizations (multi-tenant root)
CREATE TABLE organizations (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_organizations_slug ON organizations(slug);

-- Users
CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL REFERENCES organizations(id),
    email         TEXT NOT NULL,
    name          TEXT NOT NULL,
    avatar_url    TEXT NOT NULL DEFAULT '',
    github_id     BIGINT,
    role          user_role NOT NULL DEFAULT 'viewer',
    last_login_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(org_id, email),
    UNIQUE(github_id)
);

CREATE INDEX idx_users_org_id ON users(org_id);
CREATE INDEX idx_users_email ON users(email);

-- Workspaces
CREATE TABLE workspaces (
    id                        TEXT PRIMARY KEY,
    org_id                    TEXT NOT NULL REFERENCES organizations(id),
    name                      TEXT NOT NULL,
    description               TEXT NOT NULL DEFAULT '',
    source                    TEXT NOT NULL DEFAULT 'vcs',
    repo_url                  TEXT NOT NULL DEFAULT '',
    repo_branch               TEXT NOT NULL DEFAULT 'main',
    working_dir               TEXT NOT NULL DEFAULT '.',
    tofu_version              TEXT NOT NULL DEFAULT '1.11.0',
    environment               TEXT NOT NULL DEFAULT 'development',
    auto_apply                BOOLEAN NOT NULL DEFAULT FALSE,
    requires_approval         BOOLEAN NOT NULL DEFAULT FALSE,
    vcs_trigger_enabled       BOOLEAN NOT NULL DEFAULT FALSE,
    locked                    BOOLEAN NOT NULL DEFAULT FALSE,
    locked_by                 TEXT REFERENCES users(id),
    current_run_id            TEXT,
    current_config_version_id TEXT NOT NULL DEFAULT '',
    created_by                TEXT NOT NULL REFERENCES users(id),
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(org_id, name)
);

CREATE INDEX idx_workspaces_org_id ON workspaces(org_id);
CREATE INDEX idx_workspaces_vcs_trigger ON workspaces(repo_url, repo_branch) WHERE vcs_trigger_enabled = TRUE;

-- Runs
CREATE TABLE runs (
    id                 TEXT PRIMARY KEY,
    workspace_id       TEXT NOT NULL REFERENCES workspaces(id),
    org_id             TEXT NOT NULL REFERENCES organizations(id),
    operation          run_operation NOT NULL DEFAULT 'plan',
    status             run_status NOT NULL DEFAULT 'pending',
    plan_output        TEXT NOT NULL DEFAULT '',
    plan_log_url       TEXT NOT NULL DEFAULT '',
    apply_log_url      TEXT NOT NULL DEFAULT '',
    plan_json_url      TEXT NOT NULL DEFAULT '',
    resources_added    INT NOT NULL DEFAULT 0,
    resources_changed  INT NOT NULL DEFAULT 0,
    resources_deleted  INT NOT NULL DEFAULT 0,
    error_message      TEXT NOT NULL DEFAULT '',
    commit_sha         TEXT NOT NULL DEFAULT '',
    created_by         TEXT NOT NULL REFERENCES users(id),
    started_at         TIMESTAMPTZ,
    finished_at        TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_runs_workspace_id ON runs(workspace_id);
CREATE INDEX idx_runs_org_id ON runs(org_id);
CREATE INDEX idx_runs_status ON runs(status);
CREATE INDEX idx_runs_workspace_status ON runs(workspace_id, status);
CREATE INDEX idx_runs_workspace_commit ON runs(workspace_id, commit_sha) WHERE commit_sha != '';

-- workspaces.current_run_id and runs.workspace_id reference each other, so the
-- workspaces-side FK lands after both tables exist.
ALTER TABLE workspaces ADD CONSTRAINT fk_workspaces_current_run FOREIGN KEY (current_run_id) REFERENCES runs(id);

-- State versions
CREATE TABLE state_versions (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id),
    org_id           TEXT NOT NULL REFERENCES organizations(id),
    run_id           TEXT NOT NULL REFERENCES runs(id),
    serial           INT NOT NULL,
    state_url        TEXT NOT NULL,
    resource_count   INT NOT NULL DEFAULT 0,
    resource_summary TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(workspace_id, serial)
);

CREATE INDEX idx_state_versions_workspace_id ON state_versions(workspace_id);

-- Teams
CREATE TABLE teams (
    id         TEXT PRIMARY KEY,
    org_id     TEXT NOT NULL REFERENCES organizations(id),
    name       TEXT NOT NULL,
    slug       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(org_id, slug)
);

CREATE INDEX idx_teams_org_id ON teams(org_id);

-- Team memberships
CREATE TABLE team_members (
    id           TEXT PRIMARY KEY,
    team_id      TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role         user_role NOT NULL DEFAULT 'viewer',
    cloud_identity TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(team_id, user_id)
);

CREATE INDEX idx_team_members_team_id ON team_members(team_id);
CREATE INDEX idx_team_members_user_id ON team_members(user_id);

-- Workspace team permissions
CREATE TABLE workspace_team_access (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    team_id      TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    role         user_role NOT NULL DEFAULT 'viewer',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(workspace_id, team_id)
);

CREATE INDEX idx_workspace_team_access_workspace_id ON workspace_team_access(workspace_id);

-- Run approvals
CREATE TABLE approvals (
    id         TEXT PRIMARY KEY,
    run_id     TEXT NOT NULL REFERENCES runs(id),
    org_id     TEXT NOT NULL REFERENCES organizations(id),
    user_id    TEXT NOT NULL REFERENCES users(id),
    status     TEXT NOT NULL DEFAULT 'pending',
    comment    TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_approvals_run_id ON approvals(run_id);

-- Audit logs (append-only, immutable)
CREATE TABLE audit_logs (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL REFERENCES organizations(id),
    user_id     TEXT NOT NULL,
    action      TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    before_data JSONB,
    after_data  JSONB,
    ip_address  TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_logs_org_id ON audit_logs(org_id);
CREATE INDEX idx_audit_logs_entity ON audit_logs(entity_type, entity_id);
CREATE INDEX idx_audit_logs_created_at ON audit_logs(created_at);

CREATE OR REPLACE FUNCTION prevent_audit_log_modification()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs table is append-only; modifications are not allowed';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_logs_no_update
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION prevent_audit_log_modification();

-- Workspace variables
CREATE TABLE workspace_variables (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    org_id       TEXT NOT NULL REFERENCES organizations(id),
    key          TEXT NOT NULL,
    value        TEXT NOT NULL,
    sensitive    BOOLEAN NOT NULL DEFAULT FALSE,
    category     TEXT NOT NULL DEFAULT 'terraform',
    description  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(workspace_id, key, category)
);

CREATE INDEX idx_workspace_variables_workspace_id ON workspace_variables(workspace_id);

-- Pipeline status enums
CREATE TYPE pipeline_status AS ENUM (
    'idle',
    'running',
    'completed',
    'errored',
    'cancelled'
);

CREATE TYPE pipeline_stage_status AS ENUM (
    'pending',
    'importing_outputs',
    'running',
    'awaiting_approval',
    'completed',
    'errored',
    'skipped',
    'cancelled'
);

-- Pipelines
CREATE TABLE pipelines (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL REFERENCES organizations(id),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(org_id, name)
);

CREATE INDEX idx_pipelines_org_id ON pipelines(org_id);

-- Pipeline stages (ordered workspace sequence)
CREATE TABLE pipeline_stages (
    id           TEXT PRIMARY KEY,
    pipeline_id  TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    stage_order  INT NOT NULL,
    auto_apply   BOOLEAN NOT NULL DEFAULT FALSE,
    on_failure   TEXT NOT NULL DEFAULT 'stop',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(pipeline_id, stage_order)
);

CREATE INDEX idx_pipeline_stages_pipeline_id ON pipeline_stages(pipeline_id);

-- Pipeline runs (execution instances)
CREATE TABLE pipeline_runs (
    id            TEXT PRIMARY KEY,
    pipeline_id   TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    org_id        TEXT NOT NULL REFERENCES organizations(id),
    status        pipeline_status NOT NULL DEFAULT 'running',
    current_stage INT NOT NULL DEFAULT 0,
    total_stages  INT NOT NULL DEFAULT 0,
    created_by    TEXT NOT NULL REFERENCES users(id),
    started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_pipeline_runs_pipeline_id ON pipeline_runs(pipeline_id);
CREATE INDEX idx_pipeline_runs_org_id ON pipeline_runs(org_id);
CREATE INDEX idx_pipeline_runs_status ON pipeline_runs(status);

-- Pipeline run stages (per-stage tracking within a run)
CREATE TABLE pipeline_run_stages (
    id              TEXT PRIMARY KEY,
    pipeline_run_id TEXT NOT NULL REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    stage_id        TEXT NOT NULL REFERENCES pipeline_stages(id),
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id),
    run_id          TEXT REFERENCES runs(id),
    stage_order     INT NOT NULL,
    status          pipeline_stage_status NOT NULL DEFAULT 'pending',
    auto_apply      BOOLEAN NOT NULL DEFAULT FALSE,
    on_failure      TEXT NOT NULL DEFAULT 'stop',
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_pipeline_run_stages_pipeline_run_id ON pipeline_run_stages(pipeline_run_id);
CREATE INDEX idx_pipeline_run_stages_run_id ON pipeline_run_stages(run_id) WHERE run_id IS NOT NULL;

-- Org-level variable defaults
CREATE TABLE org_variables (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL REFERENCES organizations(id),
    key         TEXT NOT NULL,
    value       TEXT NOT NULL,
    sensitive   BOOLEAN NOT NULL DEFAULT FALSE,
    category    TEXT NOT NULL DEFAULT 'terraform',
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(org_id, key, category)
);

CREATE INDEX idx_org_variables_org_id ON org_variables(org_id);

-- Pipeline-level variable defaults
CREATE TABLE pipeline_variables (
    id          TEXT PRIMARY KEY,
    pipeline_id TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    org_id      TEXT NOT NULL REFERENCES organizations(id),
    key         TEXT NOT NULL,
    value       TEXT NOT NULL,
    sensitive   BOOLEAN NOT NULL DEFAULT FALSE,
    category    TEXT NOT NULL DEFAULT 'terraform',
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(pipeline_id, key, category)
);

CREATE INDEX idx_pipeline_variables_pipeline_id ON pipeline_variables(pipeline_id);

-- AWS accounts. Stores the assume-role configuration portal needs to operate
-- against each managed AWS account. Foundation for the multi-cluster portal:
-- Cluster FKs into this table.
CREATE TABLE accounts (
    id                    TEXT PRIMARY KEY,
    org_id                TEXT NOT NULL REFERENCES organizations(id),
    name                  TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    aws_account_id        TEXT NOT NULL,
    assume_role_arn       TEXT NOT NULL,
    external_id_encrypted TEXT NOT NULL DEFAULT '',
    default_region        TEXT NOT NULL,
    created_by            TEXT NOT NULL REFERENCES users(id),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(org_id, name)
);

-- aws_account_id is unique per org only when it's set. A no-AWS account (for a
-- local/kind cluster, or any cluster portal reaches directly with its own
-- kubeconfig credentials) leaves it empty, and an org may have more than one of
-- those — so uniqueness is a partial index, not a plain UNIQUE constraint (which
-- would collide every empty string against the one already stored).
CREATE UNIQUE INDEX accounts_org_id_aws_account_id_uniq
    ON accounts (org_id, aws_account_id)
    WHERE aws_account_id <> '';
CREATE INDEX idx_accounts_org_id ON accounts(org_id);

-- Kubernetes clusters portal watches. One row per managed EKS cluster.
-- Lives inside an account (FK ON DELETE RESTRICT — accounts can't be
-- removed while clusters reference them). We store the minimum needed to
-- talk to the API server: endpoint + CA + service-account token. Kubeconfig
-- as a blob was rejected in favor of this slim shape — easier to rotate
-- one field at a time and avoids carrying exec plugins / contexts we'd
-- never use.
CREATE TYPE cluster_connection_status AS ENUM (
    'pending',
    'connecting',
    'connected',
    'failed'
);

CREATE TABLE clusters (
    id                  TEXT PRIMARY KEY,
    org_id              TEXT NOT NULL REFERENCES organizations(id),
    account_id          TEXT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    name                TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    environment         TEXT NOT NULL DEFAULT 'production',
    api_endpoint        TEXT NOT NULL,
    ca_bundle_encrypted TEXT NOT NULL,
    sa_token_encrypted  TEXT NOT NULL,
    region              TEXT NOT NULL,
    -- How portal authenticates to this cluster's API server:
    --   'sa_token' — a stored, encrypted ServiceAccount bearer token (sa_token_encrypted).
    --   'eks_iam'  — no stored token; portal mints a short-lived EKS token per
    --                request by assuming the parent account's role and presigning
    --                STS for eks_cluster_name. The credential-hygiene path for
    --                vended clusters — no long-lived secret at rest.
    auth_mode           TEXT NOT NULL DEFAULT 'sa_token',
    eks_cluster_name    TEXT NOT NULL DEFAULT '',
    connection_status   cluster_connection_status NOT NULL DEFAULT 'pending',
    last_connected_at   TIMESTAMPTZ,
    connection_error    TEXT NOT NULL DEFAULT '',
    node_count          INT NOT NULL DEFAULT 0,
    k8s_version         TEXT NOT NULL DEFAULT '',
    -- Per-cluster health projections written by the in-cluster hub health watcher
    -- (same shape as connection_status / node_count / k8s_version — denormalized
    -- live facts captured periodically, read straight back by the cluster surface).
    --
    -- argocd_*: the per-cluster ArgoCD Application's sync + health, read from the hub
    --   Application named cluster-<environment>-<name> (Synced/OutOfSync, Healthy/
    --   Progressing/Degraded). Empty when no such Application exists (a hand-registered
    --   cluster, or one whose CR was pruned).
    -- control_plane_status / platform_version: from eks:DescribeCluster via the
    --   account's assume-role (ACTIVE/UPDATING/DEGRADED, eks.N). The AWS-side control
    --   plane lifecycle is distinct from kube-API reachability — a cluster can be
    --   UPDATING while the API still answers. Empty when EKS describe isn't available
    --   (no IAM permission, non-EKS, or not yet observed).
    -- last_health_observed_at: when the health watcher last ran a check for this row.
    argocd_sync_status      TEXT NOT NULL DEFAULT '',
    argocd_health_status    TEXT NOT NULL DEFAULT '',
    control_plane_status    TEXT NOT NULL DEFAULT '',
    platform_version        TEXT NOT NULL DEFAULT '',
    last_health_observed_at TIMESTAMPTZ,
    created_by          TEXT NOT NULL REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(org_id, name)
);

CREATE INDEX idx_clusters_org_id ON clusters(org_id);
CREATE INDEX idx_clusters_account_id ON clusters(account_id);

-- Cluster operations: the vend "order desk" log. portal templates an eks-fleet
-- Cluster CR (fleet.nanohype.dev/v1alpha1) from the provision form and commits it
-- to the clusters GitOps repo; the hub's ArgoCD reconciles it and Crossplane vends
-- the EKS cluster. Unlike tenant_operations this has no FK to clusters — the
-- cluster doesn't exist yet during a provision; `cluster_id` is filled in only when
-- the watch-back auto-registers the vended cluster (status -> 'active').
--
-- Operation kinds:
--   'provision' / 'deprovision' — the routine lifecycle. A deprovision commits
--     (its Cluster CR is removed from the GitOps repo), then ArgoCD prunes and
--     Crossplane runs tofu destroy — a 20–40 min teardown the in-cluster
--     watch-back observes by watching the Cluster XR disappear from the hub.
--   'unwedge' — break-glass teardown for a spoke whose provider-opentofu
--     Workspace is stuck on crossplane's external-create-pending: a create call
--     went in flight and never reported back, so crossplane will neither finish
--     creating nor delete (it can't tell whether the external resources exist).
--     The operator-triggered unwedge op tears the spoke's tagged AWS resources
--     down directly — assumed into the workload account's fleet-unwedge role —
--     then drops the Workspace finalizers so the condemned object garbage-
--     collects. It's its own op kind so the timeline + audit read as break-glass,
--     not a routine deprovision.
CREATE TYPE cluster_op_kind AS ENUM ('provision', 'deprovision', 'unwedge');

-- 'deprovisioned' is the deprovision terminal: teardown done. (The in-flight
-- 'deprovisioning' state lives in vend_phases, not here — the status enum stays
-- coarse, the phase map carries the substrate detail.)
CREATE TYPE cluster_op_status AS ENUM ('pending', 'committed', 'failed', 'active', 'deprovisioned');

CREATE TABLE cluster_operations (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL REFERENCES organizations(id),
    name            TEXT NOT NULL,
    environment     TEXT NOT NULL,
    team            TEXT NOT NULL,
    operation       cluster_op_kind NOT NULL,
    status          cluster_op_status NOT NULL DEFAULT 'pending',
    git_commit_sha  TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    spec_json       JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Vend phase timeline: a regressible map keyed by phase, e.g.
    --   { "committed":     {"at": "...", "detail": ""},
    --     "tofu_running":  {"at": "...", "detail": "applying ..."},
    --     "active":        {"at": "...", "detail": ""} }
    -- Portal projects the substrate's vend journey here. The substrate is the source
    -- of truth, so a phase can move backward — a jsonb merge (`||`) overwrites a key,
    -- which is exactly the regressible-projection behaviour we want. Portal-side
    -- phases (committed/failed) are written by the order service; substrate phases
    -- (tofu_running/active) are written later by the in-cluster watcher.
    vend_phases     JSONB NOT NULL DEFAULT '{}'::jsonb,
    cluster_id      TEXT REFERENCES clusters(id) ON DELETE SET NULL,
    created_by      TEXT NOT NULL REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_cluster_operations_org_id ON cluster_operations(org_id);
CREATE INDEX idx_cluster_operations_name_env ON cluster_operations(name, environment);
CREATE INDEX idx_cluster_operations_status ON cluster_operations(status);

-- Tenants: eks-agent-platform Tenant CRDs (platform.nanohype.dev/v1alpha1) discovered by the
-- per-cluster watcher. Read-only inventory — portal populates and prunes these
-- rows from what the K8s API actually shows; tenant writes go through git
-- (tenant_operations), never directly to this table.
--
-- Schema choice: we denormalize `name` and `phase` for fast filtering, then
-- blob the full .spec and .status as JSONB so we survive CRD schema evolution
-- without migrations. Cluster cascade-delete: if a cluster row goes away,
-- its tenant rows go with it (the source of truth is gone).
CREATE TABLE tenants (
    id               TEXT PRIMARY KEY,
    org_id           TEXT NOT NULL REFERENCES organizations(id),
    cluster_id       TEXT NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    phase            TEXT NOT NULL DEFAULT '',
    spec             JSONB NOT NULL DEFAULT '{}'::jsonb,
    status           JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(cluster_id, name)
);

CREATE INDEX idx_tenants_org_id ON tenants(org_id);
CREATE INDEX idx_tenants_cluster_id ON tenants(cluster_id);

-- Templates: admin-curated tenant flavors. A template carries the default
-- helm values for a tenant ("marketing-team gets persona=marketing, budget
-- $5K, anthropic+nova models, soc2 required") plus an allowlist of dotted
-- paths within those values that an operator can override at create time.
-- Caps (max_budget_usd, allowed_model_families, required_compliance) are
-- enforced server-side so a hostile or fat-fingered operator can't escape
-- the admin-defined envelope.
--
-- Templates are admin-managed; operators read + instantiate them, scoped by
-- template_team_access.
CREATE TABLE templates (
    id                      TEXT PRIMARY KEY,
    org_id                  TEXT NOT NULL REFERENCES organizations(id),
    name                    TEXT NOT NULL,
    description             TEXT NOT NULL DEFAULT '',
    persona                 TEXT NOT NULL,
    default_values          JSONB NOT NULL DEFAULT '{}'::jsonb,
    allowed_overrides       JSONB NOT NULL DEFAULT '[]'::jsonb,
    max_budget_usd          INT NOT NULL DEFAULT 0, -- 0 = no cap
    allowed_model_families  JSONB NOT NULL DEFAULT '[]'::jsonb,
    required_compliance     JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_by              TEXT NOT NULL REFERENCES users(id),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(org_id, name)
);

CREATE INDEX idx_templates_org_id ON templates(org_id);

-- Tenant operations: append-only log of every write portal has made to the
-- tenants GitOps repo on behalf of a user. The operation row is created
-- pending → enqueues the TenantApplyJob → worker writes the commit and
-- transitions the row to `committed` (with the SHA) or `failed` (with the
-- error message). The actual Tenant CR appears in the `tenants` table once
-- ArgoCD applies the commit and the watcher observes it — so operations
-- and tenants are decoupled: an operation captures intent, a tenant row
-- captures live state.
CREATE TYPE tenant_op_kind AS ENUM ('create', 'delete');
CREATE TYPE tenant_op_status AS ENUM ('pending', 'committed', 'failed');

CREATE TABLE tenant_operations (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL REFERENCES organizations(id),
    cluster_id      TEXT NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    tenant_name     TEXT NOT NULL,
    operation       tenant_op_kind NOT NULL,
    status          tenant_op_status NOT NULL DEFAULT 'pending',
    git_commit_sha  TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    values_json     JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- The template that produced this operation (when one was used). SET NULL
    -- on delete so deleting a template doesn't invalidate the historical
    -- operation log.
    template_id     TEXT REFERENCES templates(id) ON DELETE SET NULL,
    created_by      TEXT NOT NULL REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_tenant_operations_cluster_tenant ON tenant_operations(cluster_id, tenant_name);
CREATE INDEX idx_tenant_operations_org_id ON tenant_operations(org_id);
CREATE INDEX idx_tenant_operations_status ON tenant_operations(status);

-- Tenant team access. Records which teams own (and can see) a tenant.
-- Keyed on (cluster_id, tenant_name) rather than tenants.id because portal
-- needs to record access at create time — before the watcher has observed
-- the resulting Tenant CR and inserted the tenants row. The composite
-- key matches tenants' own UNIQUE(cluster_id, name) constraint so list
-- queries can JOIN cleanly.
CREATE TABLE tenant_team_access (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL REFERENCES organizations(id),
    cluster_id  TEXT NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    tenant_name TEXT NOT NULL,
    team_id     TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    granted_by  TEXT NOT NULL REFERENCES users(id),
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(cluster_id, tenant_name, team_id)
);

CREATE INDEX idx_tenant_team_access_org_id ON tenant_team_access(org_id);
CREATE INDEX idx_tenant_team_access_team_id ON tenant_team_access(team_id);
CREATE INDEX idx_tenant_team_access_cluster_tenant ON tenant_team_access(cluster_id, tenant_name);

-- Template team access. Records which teams can instantiate from a template.
-- Simple two-column join (template_id, team_id); the presence of a row
-- means "this team can use this template". Admins ignore this table
-- entirely (they see everything).
CREATE TABLE template_team_access (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL REFERENCES organizations(id),
    template_id TEXT NOT NULL REFERENCES templates(id) ON DELETE CASCADE,
    team_id     TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    granted_by  TEXT NOT NULL REFERENCES users(id),
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(template_id, team_id)
);

CREATE INDEX idx_template_team_access_org_id ON template_team_access(org_id);
CREATE INDEX idx_template_team_access_team_id ON template_team_access(team_id);
