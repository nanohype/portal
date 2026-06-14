# Configuration

All configuration is via environment variables. In development every variable has a working default except `ENVIRONMENT` (set it to `development`) and the GitHub OAuth credentials. The source of truth is `internal/domain/config.go` — this doc tracks it.

## Local Dev

Set `ENVIRONMENT=development` (the `dev:*` and `seed:demo` Taskfile targets already do). That enables the **Dev Login** button on the login page — a local user without GitHub OAuth — and relaxes the secret validation. `ENVIRONMENT` has **no default**: anything other than `development` is treated as production and fails closed, so a missing or typo'd value can't silently boot an insecure instance with default keys.

To use GitHub sign-in locally, set `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET` from a [GitHub OAuth App](https://github.com/settings/developers) with:
- Homepage URL: `http://localhost:5173`
- Authorization callback URL: `http://localhost:8080/api/v1/auth/github/callback`

## Server

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_ADDR` | `:8080` | HTTP listen address |
| `SERVER_BASE_URL` | `http://localhost:8080` | Public URL of the API server (used for OAuth callbacks) |
| `WEB_URL` | `http://localhost:5173` | Public URL of the web frontend (used for CORS) |
| `ENVIRONMENT` | _(none)_ | Only `development` relaxes security (dev login + default keys allowed) and adds the localhost CORS origin. Unset or anything else → treated as production, fail closed. The Helm chart defaults `config.environment` to `production`. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SHUTDOWN_TIMEOUT` | `15s` | Graceful shutdown timeout for in-progress requests |

## Database

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | `postgres://portal:portal@localhost:5432/portal?sslmode=disable` | Postgres connection string |
| `DB_MAX_CONNS` | `25` | Maximum open connections |
| `DB_MIN_CONNS` | `5` | Minimum idle connections |
| `DB_MAX_CONN_IDLE_TIME` | `5m` | Close idle connections after this duration |
| `DB_HEALTH_CHECK_PERIOD` | `30s` | How often to ping idle connections |

## Redis

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_URL` | `redis://localhost:6379` | Redis connection string. Used for log streaming pub/sub. If empty or unavailable, falls back to in-memory streaming (single-server only) |

## Object store (S3)

Portal persists OpenTofu state, run logs, plan JSON, config archives, and module bundles in an S3-compatible object store via `aws-sdk-go-v2`. The store is external to the chart. The same client speaks the generic S3 API two ways, picked by whether static keys are set:

- **Static keys** — set `S3_ACCESS_KEY` and `S3_SECRET_KEY`. Use this for dev (a local minio / SeaweedFS on a custom `S3_ENDPOINT`) or any S3-compatible store that isn't AWS. Path-style addressing is always on, so you don't need per-bucket virtual-host DNS.
- **IRSA / default chain** — leave `S3_ACCESS_KEY` and `S3_SECRET_KEY` empty. The AWS SDK default credential chain supplies credentials: env, then EKS IRSA web-identity / EC2 / ECS. This is the hub path — the worker's IRSA role grants S3 access and no long-lived key sits at rest. Point `S3_ENDPOINT` at the regional AWS endpoint (or leave it for the SDK default) and set `S3_REGION` / `S3_USE_SSL` to match.

| Variable | Default | Description |
|----------|---------|-------------|
| `S3_ENDPOINT` | `localhost:9000` | S3-compatible endpoint, without scheme. Scheme is derived from `S3_USE_SSL`. For AWS, the regional S3 endpoint (e.g. `s3.us-west-2.amazonaws.com`) |
| `S3_BUCKET` | `portal` | Bucket name for state, logs, plans, config archives, and module bundles |
| `S3_ACCESS_KEY` | `minioadmin` | Static access key. Leave empty to use the AWS SDK default credential chain (IRSA) |
| `S3_SECRET_KEY` | `minioadmin` | Static secret key. Leave empty to use the AWS SDK default credential chain (IRSA) |
| `S3_USE_SSL` | `false` | Use HTTPS for S3 connections |
| `S3_REGION` | `us-east-1` | S3 region |

Config is wired through the `objectStore` Helm block (and surfaces as the `S3_*` env on the server and worker).

## Authentication

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_SECRET` | `dev-secret-change-in-production` | Signing key for JWT tokens. **Must be changed in non-dev environments.** |
| `JWT_EXPIRATION` | `24h` | Token lifetime |

## Encryption

| Variable | Default | Description |
|----------|---------|-------------|
| `ENCRYPTION_KEY` | `dev-encryption-key-32bytes!!!!!!` | AES-256 key for encrypting sensitive variable values. **Must be exactly 32 bytes.** Must be changed in non-dev environments. |

## Webhooks

| Variable | Default | Description |
|----------|---------|-------------|
| `WEBHOOK_SECRET` | _(empty)_ | HMAC-SHA256 secret for verifying GitHub webhook signatures. Set this to the same value configured in your GitHub webhook settings. Required in non-dev environments. |

## Worker

| Variable | Default | Description |
|----------|---------|-------------|
| `WORKER_CONCURRENCY` | `10` | Max concurrent tofu/terragrunt runs (the default River queue) |
| `WORKER_RECONCILE_CONCURRENCY` | `8` | Max concurrent per-cluster watch/reconcile jobs (the `reconcile` queue, separate from runs so they don't starve each other) |
| `WORKER_HEALTH_ADDR` | `:8081` | Address for the worker's `/healthz` (pings the DB) and `/metrics` endpoints |

## Observability

Server and worker each expose Prometheus metrics on `/metrics` (the server on `SERVER_ADDR`, the worker on `WORKER_HEALTH_ADDR`), unauthenticated for in-cluster scraping. The Helm chart annotates both pods with `prometheus.io/scrape` so a Grafana Agent (or any Prometheus) picks them up; logs go to stdout (structured slog) for Loki. Key series: HTTP RED (`portal_http_request_duration_seconds`), DB pool stats, tofu run duration, River job errors + queue depth by state, and per-watcher tick heartbeats.

### Tracing

Distributed tracing is opt-in (off by default). When enabled, server and worker export OTLP traces to the cluster's Grafana Agent (Tempo behind it), so a request that enqueues a River job that shells out to tofu reads as **one trace** — the HTTP span propagates its W3C context into the job's metadata, and the worker continues it. DB queries (via pgx) are spans too.

| Variable | Default | Description |
|----------|---------|-------------|
| `OTEL_TRACES_ENABLED` | `false` | Master switch. When false, a no-op tracer is installed (context still propagates, nothing exports). |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | OTLP receiver, e.g. the agent's `http://grafana-agent.observability.svc:4318`. Read by the OTel SDK directly. |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` | OTLP transport. Read by the SDK directly. |
| `OTEL_TRACES_SAMPLER_ARG` | `1` | Head-sampling fraction for new traces (`1` = all, `0.1` = 10%). A job inherits its enqueuing request's decision. |

`service.name` is set in code (`portal-server` / `portal-worker`). In the Helm chart these are gated behind `tracing.enabled`; traces are pushed (OTLP), not scraped, so no pod annotation is added.

## Executor

| Variable | Default | Description |
|----------|---------|-------------|
| `EXECUTOR_TYPE` | `local` | `local` runs tofu on the worker host. `kubernetes` runs tofu in ephemeral pods. |
| `EXECUTOR_NAMESPACE` | `portal` | Kubernetes namespace for executor pods (K8s executor only) |
| `EXECUTOR_IMAGE` | `portal-executor:tofu-1.11` | Default container image for executor pods |
| `EXECUTOR_IMAGE_PREFIX` | `portal-executor` | Image name prefix. When a workspace specifies a tofu version, the pod uses `{prefix}:tofu-{version}` as the image tag. |

## GitOps — tenant write path

The tenant write path helm-renders the eks-agent-platform `charts/tenant` chart and commits the result to a tenants GitOps repo for ArgoCD to reconcile. When `GITOPS_TENANTS_REPO_URL` is empty the worker surfaces "not configured" on any `tenant_apply` attempt — dev machines without SSH keys don't blow up on startup.

| Variable | Default | Description |
|----------|---------|-------------|
| `GITOPS_CACHE_DIR` | `/tmp/portal/git` | Local clone cache for GitOps repos |
| `GITOPS_TENANTS_REPO_URL` | _(empty)_ | Tenants GitOps repo. Empty disables the tenant write path |
| `GITOPS_TENANTS_REPO_REF` | `main` | Branch/ref to commit tenant changes to |
| `GITOPS_SSH_KEY_PATH` | _(empty)_ | Path to the SSH key used to push to the GitOps repos (tenant + cluster) |
| `GITOPS_AUTHOR_NAME` | `portal` | Git commit author name |
| `GITOPS_AUTHOR_EMAIL` | `portal@local` | Git commit author email |
| `EKS_AGENT_PLATFORM_CHARTS_REPO_URL` | _(empty)_ | eks-agent-platform charts repo, source of `charts/tenant` for rendering |
| `EKS_AGENT_PLATFORM_CHARTS_REPO_REF` | `main` | Branch/ref of the charts repo |

## GitOps — cluster vend path

The cluster vend path templates the eks-fleet `Cluster` CR directly (no chart) and commits it to a clusters GitOps repo. When `GITOPS_CLUSTERS_REPO_URL` is empty the worker surfaces "not configured" on any `cluster_apply` attempt. It reuses the shared SSH key + author from the tenant path, so it only needs the clusters repo.

| Variable | Default | Description |
|----------|---------|-------------|
| `GITOPS_CLUSTERS_REPO_URL` | _(empty)_ | Clusters GitOps repo. Empty disables the cluster vend path |
| `GITOPS_CLUSTERS_REPO_REF` | `main` | Branch/ref to commit cluster changes to |
| `FLEET_HUB_ROLE_ARN` | _(empty)_ | The hub's Crossplane role ARN (`eks-fleet-crossplane`). On a cross-account vend (the `Cluster` sets `vendRoleArn`) the worker stamps it onto `spec.bootstrapAccessRoleArn` so cluster-stack grants the hub a cluster-admin EKS access entry and the bootstrap Workspace's get-token can reach the spoke API. Empty = same-account only (no stamping) |

## ArgoCD cluster-registry sync

Read path. When enabled, the worker reads ArgoCD's cluster Secrets (in `ARGOCD_NAMESPACE`, via the pod's in-cluster ServiceAccount) every `ARGOCD_SYNC_INTERVAL` and upserts the cluster inventory — a cluster registered with ArgoCD is onboarded with no manual portal registration. Discovered clusters attach to the configured org + account, attributed to the configured user. Inert unless all three IDs are set and the worker runs in-cluster.

| Variable | Default | Description |
|----------|---------|-------------|
| `ARGOCD_CLUSTER_SYNC` | `false` | Enable the ArgoCD cluster-registry sync |
| `ARGOCD_NAMESPACE` | `argocd` | Namespace holding ArgoCD's cluster Secrets and the per-cluster Applications |
| `ARGOCD_SYNC_INTERVAL` | `120s` | How often to read the cluster Secrets |
| `ARGOCD_SYNC_ORG_ID` | _(empty)_ | Org discovered clusters attach to. Required to enable sync |
| `ARGOCD_SYNC_ACCOUNT_ID` | _(empty)_ | Account discovered clusters attach to. Required to enable sync |
| `ARGOCD_SYNC_CREATED_BY` | _(empty)_ | User ID discovered clusters are attributed to. Required to enable sync |

## Cluster watchers

In-cluster hub watchers that close the vend loop and project per-cluster health. Both are inert unless enabled and the worker runs in-cluster on the hub.

- **Watch-back** (`clusterWatchback`) — the vend loop's closing leg. Reads each committed provision op's eks-fleet `Cluster` XR every interval and, once the EKS endpoint + CA are up, auto-registers the new cluster as `eks_iam` and flips the op to `active`.
- **Health** (`clusterHealth`) — steady-state per-cluster health. Reads each registered cluster's per-cluster ArgoCD Application (sync + health) and, for `eks_iam` clusters, its EKS control plane via `eks:DescribeCluster`, every interval, and projects them onto the cluster row. Uses `ARGOCD_NAMESPACE` as the hub namespace the per-cluster Applications live in.

| Variable | Default | Description |
|----------|---------|-------------|
| `CLUSTER_WATCHBACK_ENABLED` | `false` | Enable the vend watch-back leg |
| `CLUSTER_WATCHBACK_INTERVAL` | `60s` | How often to poll committed provision ops |
| `CLUSTER_HEALTH_ENABLED` | `false` | Enable the per-cluster health watcher |
| `CLUSTER_HEALTH_INTERVAL` | `120s` | How often to refresh ArgoCD + EKS control-plane health |

## Non-dev requirements

When `ENVIRONMENT` is not `development` (including unset — it has no default, so this is the fail-closed path), the server validates that the following are set to non-default values and refuses to start otherwise:

- `JWT_SECRET`
- `ENCRYPTION_KEY` (must be exactly 32 bytes)
- `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET`
- `WEBHOOK_SECRET`
- `S3_ACCESS_KEY` and `S3_SECRET_KEY` — must not be `minioadmin`. On a hub running off IRSA, leave both **empty** so the AWS SDK default chain supplies credentials; the empty case passes validation. The check only rejects the literal `minioadmin` default.
