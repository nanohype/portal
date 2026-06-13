# Deployment

How to run portal in each place it runs: local dev on docker-compose, and the
Helm chart for a cluster (kx or a real EKS hub). The end of this page is a
runbook index — the table that tells you which step-by-step doc to follow for
the job you're actually doing.

Portal is three processes plus its data plane:

- **server** — the HTTP API
- **worker** — runs tofu/terragrunt and drives the cluster watchers (River)
- **web** — nginx serving the React SPA, proxying `/api` to the server
- **postgres** — data + the River job queue
- **redis** — log-streaming pub/sub
- an **S3-compatible object store** — OpenTofu state + run logs

## Docker images

Build everything from the repo root:

```bash
task docker:build
```

That produces `portal/server`, `portal/worker`, `portal/web`, and
`portal/migrate` (the one-shot migration runner). Multi-stage Alpine builds,
all running as a non-root `portal` user.

If you run the Kubernetes executor (the worker spawns ephemeral pods to run
tofu instead of running it in-process), also build the executor image — one per
tofu version you need:

```bash
docker build -f docker/Dockerfile.executor -t portal-executor:tofu-1.11 .
```

The worker resolves the image as `{EXECUTOR_IMAGE_PREFIX}:tofu-{workspace.tofu_version}`.

## Local dev (docker-compose)

`docker-compose.yaml` brings up the data plane for local dev: postgres, redis,
minio, and a one-shot migrate job. minio here is a dev convenience — a local
S3 endpoint so you don't need a cloud bucket to push state at. In a cluster the
object store is external (see the Helm chart below); the chart bundles nothing.

```bash
docker compose up -d   # postgres, redis, minio + auto-migrate
task dev               # migrates, then runs server + worker + web
```

The compose data plane:

```yaml
services:
  postgres:
    image: postgres:17-alpine
    ports: ["5432:5432"]
    environment:
      POSTGRES_USER: portal
      POSTGRES_PASSWORD: portal
      POSTGRES_DB: portal
    volumes: [pgdata:/var/lib/postgresql/data]

  redis:
    image: redis:8-alpine
    ports: ["6379:6379"]

  minio:
    image: minio/minio
    ports: ["9000:9000", "9001:9001"]
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    command: server /data --console-address ":9001"
    volumes: [miniodata:/data]

  migrate:
    build:
      context: .
      dockerfile: docker/Dockerfile.migrate
    environment:
      DATABASE_URL: "postgres://portal:portal@postgres:5432/portal?sslmode=disable"
    depends_on:
      postgres:
        condition: service_healthy

volumes:
  pgdata:
  miniodata:
```

`task dev` reads dev defaults from `internal/domain/config.go` — minio at
`localhost:9000` with `minioadmin`/`minioadmin`, postgres/redis on their default
ports. With `ENVIRONMENT=development` the server relaxes validation (default
JWT/encryption keys are allowed, dev login is enabled). The full env-var
surface, including the cluster-watcher and ArgoCD knobs, is in
[configuration.md](./configuration.md).

To reset the database — drop everything and re-migrate:

```bash
docker compose down -v && docker compose up -d
```

## Helm chart

The chart lives at `deploy/helm/portal`. It deploys the three Deployments
(server, worker, web), a migrate Job that runs on install/upgrade, a ConfigMap
for non-secret config, a Secret for credentials, and an optional Ingress.
Source of truth for every value is `deploy/helm/portal/values.yaml`.

```bash
cd deploy/helm/portal
helm dependency build          # pull the postgresql/redis subcharts
helm install portal . -f values-hub.yaml
```

### Images

`image.<component>.{repository,tag,pullPolicy}` for each of `server`, `worker`,
`web`, `migrate`. Point these at your registry and pin a tag for anything past
local dev.

### Config and secrets

`config.*` carries the non-secret settings (URLs, environment, log level,
executor type, worker concurrency, DB pool sizing). The secret-bearing fields —
`jwtSecret`, `encryptionKey`, `githubClientSecret`, `webhookSecret`, and the
object-store keys — land in a Kubernetes Secret the chart renders. For anything
real, source these from a sealed secret or external-secrets rather than
plaintext in your values file.

Set per environment:

```yaml
config:
  environment: "production"
  serverBaseURL: "https://portal.example.com"
  webURL: "https://portal.example.com"
  githubClientID: "<from-github>"
  githubClientSecret: "<from-github>"
  jwtSecret: "<random-string>"
  encryptionKey: "<exactly-32-bytes>"
  webhookSecret: "<random-string>"
  executorType: "kubernetes"   # or "local"
  executorNamespace: "portal"
```

### Object store (external S3, IRSA)

Portal speaks the generic S3 API on aws-sdk-go-v2 and points at object storage
you provide — there is no bundled store. Config goes under `objectStore` (the
`S3_*` env vars):

```yaml
objectStore:
  endpoint: ""        # e.g. s3.us-west-2.amazonaws.com  (required)
  bucket: portal
  region: us-east-1
  useSSL: true
  accessKey: ""       # leave empty on a hub to use IRSA
  secretKey: ""
```

Two credential paths, picked by whether keys are set:

- **Static keys** — set `accessKey`/`secretKey` and the client uses them. This
  is the path for any S3-compatible store (a standalone minio/SeaweedFS you
  install yourself, or an access key against a bucket).
- **IRSA / default chain** — leave both empty and the client falls back to the
  AWS SDK default credential chain, which on a hub resolves to the worker's
  IRSA role. No keys in the Secret. This is the production path against an AWS
  S3 bucket: `endpoint: s3.<region>.amazonaws.com`, `useSSL: true`, `region`
  set to match.

### PostgreSQL and Redis subcharts

`postgresql` and `redis` are Bitnami subcharts, on by default for a
self-contained install (kx, dev). They carry the Bitnami-catalog image caveat
tracked in **portal#43** — the public Bitnami image tags churn. For production,
turn the subcharts off and point at managed services:

```yaml
postgresql:
  enabled: false
redis:
  enabled: false
```

Then supply the connection strings via `worker`/`server` `extraEnv`/`extraEnvFrom`
(below) — `DATABASE_URL` at managed RDS, `REDIS_URL` at ElastiCache.

### GitOps write paths

The worker commits rendered manifests (cluster CRs, tenant charts) to GitOps
repos over SSH. `gitops.sshKey` is a deploy key (PEM) with write access; it
mounts read-only on the worker and is never exposed as an env var. Setting a
repo URL enables that vend path:

```yaml
gitops:
  sshKey: "<deploy-key-pem>"
  clustersRepoURL: "git@github.com:nanohype/clusters.git"   # enables cluster vend
  tenantsRepoURL: "git@github.com:nanohype/tenants.git"     # enables tenant vend
  authorName: "portal"
  authorEmail: "portal@local"
```

### Cluster ops watchers

These run inside the worker and only do anything when the worker runs
in-cluster on the hub. Off the hub they're inert.

- **`argocdSync`** — the read path for cluster discovery. When enabled, the
  worker discovers clusters from ArgoCD's cluster Secrets (no manual portal
  registration) and the chart grants it the RBAC to read those Secrets and
  watch Platform/Tenant CRs cluster-wide. Point `orgID`/`accountID`/`createdBy`
  at existing portal IDs.
- **`clusterWatchback`** — the closing leg of the vend loop. The worker polls
  each committed provision op's eks-fleet `Cluster` XR and auto-registers the
  cluster (as `eks_iam`) once its endpoint + CA are up.
- **`clusterHealth`** — the per-cluster health badges. The worker reads each
  cluster's ArgoCD Application for sync/health, and — for `eks_iam` clusters
  with `serviceAccount.roleArn` set — its EKS control plane via
  `eks:DescribeCluster`, projecting both onto the cluster row. The EKS read
  degrades to empty until the assume-role grants `eks:DescribeCluster`.

```yaml
argocdSync:
  enabled: true
  namespace: argocd
clusterWatchback:
  enabled: true
clusterHealth:
  enabled: true
```

### Worker IRSA

`serviceAccount.roleArn` binds the worker's ServiceAccount to an IAM role on the
cluster's EKS OIDC provider, giving it AWS credentials for read-only cluster
status (`eks:DescribeCluster` for the live cluster-detail view) and for the
object store when `objectStore` keys are empty. Leave it empty and the worker
does Kubernetes reads only — the vend timeline needs no AWS creds.

```yaml
serviceAccount:
  roleArn: "arn:aws:iam::111111111111:role/portal-worker"
```

On a real hub, portal's cross-account IAM is codified: the `portal-hub` worker
IRSA role plus a per-account `portal-spoke` role. The worker assumes the spoke
to mint EKS tokens and call `eks:DescribeCluster` against spoke accounts. The
full wiring is in the deploy-on-hub runbook below.

For a cross-account vend, `fleet.hubRoleArn` is the hub's Crossplane role ARN —
portal stamps it onto the `Cluster`'s `bootstrapAccessRoleArn` so the bootstrap
Workspace's get-token can reach the spoke API. Empty means same-account vends
only.

### extraEnv / extraEnvFrom

`server.extraEnv`/`worker.extraEnv` (inline `name`/`value` or `valueFrom`) and
`*.extraEnvFrom` (`secretRef`/`configMapRef`) append to the env the chart
already wires. This is the seam for managed-service connection strings, and for
AWS credentials on a non-IRSA hub like kx — create a Secret with
`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_REGION` and reference it so
the worker can mint EKS tokens (survives `helm upgrade`, unlike `kubectl set
env`). On a real hub, use `serviceAccount.roleArn` instead and leave these
empty.

```yaml
worker:
  extraEnvFrom:
    - secretRef:
        name: portal-aws
```

### Ingress

`ingress.enabled` plus `className` and `hosts`; `tls` is commented in the
defaults. HTTPS terminates at the ingress/load balancer.

### Kubernetes executor

With `config.executorType: kubernetes`, the worker creates ephemeral pods to run
tofu instead of running it in-process — isolation between runs, per-run resource
limits, and per-workspace tofu versions via image tags. The worker's
ServiceAccount needs permission to create/delete Pods and ConfigMaps in
`config.executorNamespace`, and the executor images have to exist
(`{EXECUTOR_IMAGE_PREFIX}:tofu-<version>`). With `executorType: local` the
worker runs tofu in-process.

### Upgrading

```bash
helm upgrade portal deploy/helm/portal -f values-hub.yaml
```

The migrate Job runs automatically on upgrade.

## Health checks

| Endpoint | Port | Description |
|----------|------|-------------|
| `GET /api/v1/health` | 8080 | Server health — pings Postgres, returns 503 if degraded |
| `GET /healthz` | 8081 | Worker health — basic liveness check |

## Production checklist

- [ ] `config.environment` set to `production`
- [ ] `jwtSecret` is a unique random string
- [ ] `encryptionKey` is exactly 32 random bytes
- [ ] `webhookSecret` is set and matches the GitHub webhook config
- [ ] object store uses IRSA (empty keys) or a real access key — not `minioadmin`
- [ ] database password is not the default `portal`
- [ ] `postgresql.enabled`/`redis.enabled` false, pointed at managed RDS/ElastiCache (portal#43)
- [ ] HTTPS terminates at the ingress/load balancer
- [ ] GitHub OAuth callback URL points to the production domain
- [ ] `webURL` and `serverBaseURL` use the production domain
- [ ] secrets sourced from a sealed secret / external-secrets, not plaintext values

## Runbook index

Step-by-step runbooks for the specific job you're doing. Cross-repo paths are
plain text — they live in the eks-fleet repo.

| Goal | Runbook |
|------|---------|
| Validate the raw vend loop by hand (kubectl) | `eks-fleet/docs/rung-1-local-validation.md` |
| Vend a cluster via **local** portal (`task dev`) | `eks-fleet/docs/rung-1-via-portal.md` |
| Run portal **in-cluster** on kx (watchers on, live timeline) | [in-cluster-on-kx.md](./in-cluster-on-kx.md) |
| Deploy portal on a **real EKS hub** (IRSA + the IAM wiring) | [deploy-on-hub.md](./deploy-on-hub.md) |
