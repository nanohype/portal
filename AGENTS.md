# portal — agent entry point

You're an AI client (or the author of one) about to work on portal — add an API endpoint or a page, wire a worker job, change the chart, or drive the substrate through its surfaces. This file gets you running. For stack context, read the [Platform Reference](https://github.com/nanohype/nanohype/blob/main/docs/platform-reference.md).

portal is the nanohype stack's self-hosted operations portal: a Go backend (chi HTTP API + River job worker) plus a React 19 SPA that runs the cloud substrate from one UI with one audit trail.

## What this repo gives you

One domain model, four surfaces:

1. **Infrastructure execution** — OpenTofu/Terragrunt workspaces, pipelines, runs, plan diff, state versions, `org → pipeline → workspace` variable inheritance, VCS webhooks, terragrunt auto-detection.
2. **Fleet** — AWS accounts (stored assume-role creds) and EKS clusters (slim encrypted creds + an async connection-test job), plus the operations daily-driver: cluster vend timeline, deprovision watch, org-wide ops feed, per-cluster ArgoCD/EKS health.
3. **Tenant management** — a per-cluster watcher walks `platform.nanohype.dev` Tenant CRDs and reconciles a DB inventory; a UI form helm-renders the eks-agent-platform `charts/tenant` chart, commits to a tenants GitOps repo, ArgoCD reconciles; curated templates with server-side cap enforcement (budget / model-family / compliance).
4. **Access control** — teams + RBAC, team-scoped self-service, and a unified catalog across every entity a user can see.

The write paths render manifests and commit to GitOps repos for ArgoCD; the read paths are in-cluster watchers that project live substrate state onto DB rows — the UI reads the projection, the cluster always wins.

## Architecture at a glance

Three processes, talking through Postgres (data + the River job queue) and Redis (log-streaming pub/sub):

- **server** (`:8080`) — chi HTTP API: auth, CRUD, WebSocket run-log streaming, webhook ingestion. All routes live in `internal/server/server.go` `setupRouter()`.
- **worker** (`:8081`) — River job processor: runs tofu/terragrunt, uploads state/logs/plans to S3, drives the cluster watchers.
- **web** (`:5173` dev / nginx prod) — React 19 / Vite 8 / Tailwind 4 SPA.

Backend (Go 1.26) is layered **handler → service → repository**:

- `internal/handler/` — one file per domain.
- `internal/service/` — business logic.
- `internal/repository/` — **hand-written** pgx queries in `*.sql.go` (sqlc-style typed Params + `scanX` helpers, but **not generated** — there is no codegen step; edit them directly).
- `internal/worker/` — River workers. Job kinds: `run`, `pipeline_stage`, `cluster_connection_test`, `cluster_watch`, `tenant_apply`, `cluster_apply`. Two queues: `default` (tofu runs) and `reconcile` (per-cluster watch jobs) so they don't starve each other. Run worker stays **1 replica** (River handles concurrency internally).

**Executor model:** `local` (tofu in-process on the worker host) vs `kubernetes` (ephemeral pods, per-workspace tofu version via `EXECUTOR_IMAGE_PREFIX:tofu-<version>`).

Frontend: TanStack Query + Router, Zustand, sonner toasts, xterm.js for the run-log WebSocket terminal. The API contract is `api/openapi.yaml`; `web/src/api/types.ts` is **generated** from it (`npm run generate:api` in `web/`, drift-checked in CI) and consumed by the openapi-fetch client (`web/src/api/client.ts`). Components import domain types from `web/src/api/models.ts` (named aliases over the generated schemas). Routing is TanStack Router in `web/src/router.tsx` (auth-gated layout route, lazy chunks).

## Dev / build / test

Prereqs: Go 1.26+, Node 22+ (what CI uses), Docker, Task.

```sh
docker compose up -d        # Postgres + Redis + MinIO + one-shot migrate
task dev                    # migrate, then server + worker + web in parallel
# open http://localhost:5173 → Dev Login (no OAuth locally; first user → owner)
task seed                   # AWS org vars + 4 landing-zone leaf workspaces + a prereqs pipeline (idempotent)
task seed:demo              # WIPES the DB, populates a full demo across every surface (dev-only)
```

Verify changes — **the real checks**:

```sh
go build ./...
go test ./...                          # repository integration tests skip without TEST_DATABASE_URL
cd web && npx tsc -b && npx vite build  # tsc -b is the REAL typecheck (root tsconfig is files:[] + project refs,
                                        # so `tsc --noEmit` checks nothing; vite build is transpile-only)
```

`task lint` = `go vet` + `tsc -b`; CI also fails on `gofmt` drift (`task fmt` = `gofmt -w`).

**Migrations:** the schema is a single pair, `migrations/000001_initial_schema.{up,down}.sql` (25 tables, 10 enum types). For dev, edit it and reset (`docker compose down -v && docker compose up -d`); for prod, add a new numbered up/down pair run by `cmd/migrate` (it walks the directory).

## Deploy shape

The Helm chart at `deploy/helm/portal` renders three Deployments (server, worker, web), a **migrate Job that runs on install AND upgrade**, a ConfigMap, a Secret, optional Ingress, and worker RBAC. It bundles **no database, cache, or object store** (off the archived Bitnami catalog) — it points at managed external services.

- **`database.url` is REQUIRED** — `secret.yaml` wraps it in Helm `required`, so install fails closed without it. Point it at managed Postgres (`postgres://…?sslmode=require`).
- `redis.url` is optional (empty → in-memory log streaming, single-replica only).
- Object store is external S3: static `accessKey`/`secretKey` for dev/self-hosted, **or** empty keys → AWS SDK default chain → worker Pod Identity on a hub (no keys at rest).
- `config.environment` fails closed: only `ENVIRONMENT=development` relaxes (dev login + default keys); anything else is production, and `Config.Validate()` then requires a real `jwtSecret`, a 32-byte `encryptionKey`, GitHub OAuth, `webhookSecret`, and non-default S3 keys.
- **GitOps write paths over SSH** (`gitops.sshKey` deploy key, mounted read-only): `tenantsRepoURL` enables tenant vend (commits a rendered eks-agent-platform tenant chart); `clustersRepoURL` enables cluster vend (commits an eks-fleet `Cluster` CR). ArgoCD reconciles both.
- Cluster-ops watchers (`argocdSync`, `clusterWatchback`, `clusterHealth`) project live substrate state onto DB rows and are **inert off the hub** — they only act when the worker runs in-cluster.

Helpers: `task docker:build` (server/worker/web/migrate images), `task hub:install` (helm upgrade --install with production options). Runbooks: `docs/in-cluster-on-kx.md` (kind hub), `docs/deploy-on-hub.md` (real EKS hub + cross-account IAM). Health: `/healthz` (liveness, process-only) + `/readyz` (readiness, pings Postgres) on both the server (8080) and worker (8081); `GET /api/v1/health` (8080) is the app-level surface the UI reads. `/metrics` is unauthenticated by design (pod-direct Grafana Agent scrape — don't route it via ingress).

## Conventions an agent must follow

- **ULIDs everywhere** (`ulid.Make().String()`).
- **`org_id` on EVERY query** — there is no cross-org access through the API (org comes from JWT claims).
- HTTP responses go through `respond.JSON()` / `respond.Error(w, http.StatusXxx, msg)` / `respond.NoContent()` / `respond.FromError(w, r, err)` (maps service errors once: `pgx.ErrNoRows`→404, `apperr.*`→their codes, else→500 with the cause logged). **Never `fmt.Fprintf` raw JSON.**
- RBAC `owner > admin > operator > viewer` via `auth.RequireRole(min)` / `auth.RequireAction(action)`; apply-to-prod gates on `ActionApplyProd` (admin). Anything that opens the approval gate carries that bar wherever it appears — `auto_apply` on workspace create, clone, update and on a pipeline stage — and so does a direct `apply`/`destroy` run on a workspace with `requires_approval`. Decrypt-and-return endpoints carry `ActionRevealSecret`, the same bar as editing the secret.
- Every `auth.Action` must be enforced somewhere — `TestEveryActionIsEnforced` scans the tree and fails on a constant nothing gates. Gate a route with the action rather than repeating its role as a string literal.
- Routes under `/workspaces/{workspaceID}` use `auth.RequireWorkspaceAction` / `RequireWorkspaceRole`, which combine the org role with any `workspace_team_access` grant the caller's teams hold on that workspace, capped by that member's role within the granted team. Grants **elevate only** — a grant never removes what an org role already allows — and an unreadable grant means no elevation, so the gate fails closed.
- **A workspace-scoped gate is only as good as the query behind it.** The gate authorizes the workspace in the URL, so anything addressed by a child id under it — a variable, a state version, a run — must be looked up by `(childID, workspaceID, orgID)`, never `(childID, orgID)`. Otherwise a caller authorized on one workspace can name any child in the org and reach it. Return 404 on a miss so the response doesn't confirm the id exists. When a query has a legitimately workspace-agnostic caller (the worker holds a run id with no workspace in hand), add a scoped variant — `GetRunInWorkspace` — rather than weakening the org-scoped one.
- **A second workspace named in a request body is not covered by the route's gate.** `/variables/copy` and `/variables/import-outputs` authorize their `source_workspace_id` in the handler via `auth.EffectiveWorkspaceRole`, at the same bar the route applied to the destination.
- **Role is not a JWT claim.** The token says who is calling; `auth.Middleware` resolves what they may do from the users table on every request, so a promotion or demotion takes effect immediately instead of waiting out the token. Both that lookup and the grant lookup come from one `service.AuthzService`.
- **JWTs never ride in URLs.** The login handoff is a short-lived `auth_token` cookie the SPA callback consumes; WebSockets authenticate via the `["bearer", <jwt>]` subprotocol (`Sec-WebSocket-Protocol`) — a `?token=` query param 401s.
- All mutations go through `auditSvc.Log()` with before/after state, sensitive values redacted to `***`.
- Sensitive variables + cluster creds are AES-256 encrypted via `secrets.Encryptor`, decrypted in the worker at run time.
- Variables: `terraform` category → tfvars / `TF_VAR_*`; `env` category → process env (put AWS creds here, **not** `terraform`).
- `worker → service` is one-directional (the pipeline-stage worker uses `RunCreatorFunc`/`OutputImporter` function types to avoid the import cycle).
- Terragrunt is co-equal to plain tofu — auto-detected by `executor.DetectBinary(workDir)`. Upload the **full parent tree** (`root.hcl`, `_envcommon/`) so `find_in_parent_folders` resolves; portal vars go in as `TF_VAR_*` (lower precedence than terragrunt's `inputs={}`, so terragrunt-owned keys win — the Discover UI marks them `configured_by: terragrunt`).
- **Adding work:** a new API endpoint = a handler method (+ its `*Response` type) in `internal/handler/<domain>.go` + the route in `internal/server/server.go` + the path/schema in `api/openapi.yaml`, then `npm run generate:api` in `web/` to regenerate `src/api/types.ts`. A new page = a component in `web/src/components/` + a lazy route in `web/src/router.tsx` + TanStack Query hooks + sonner toasts. Don't truncate text in the UI.

## CI gate

`.github/workflows/ci.yaml` (push/PR to main), two jobs:

- **ci** (with a Postgres 17 service): `gofmt -l` empty, `go build`/`go vet`, `govulncheck` (pinned, live CVE data), `go test` (with `TEST_DATABASE_URL` → the service Postgres), then `npm ci` + `npm audit --audit-level=high` + `npx tsc -b` + `npx vite build`.
- **chart**: `helm lint` + `helm template` (with a dummy `database.url`, which the chart requires) + `kubeconform -strict` + the org [`render-assert`](https://github.com/nanohype/nanohype/tree/main/.github/actions/render-assert) action (no unfilled sentinels in the rendered manifests).

Match this locally before pushing. **portal is a PUBLIC repo** — never commit real AWS account ids; use placeholders `111111111111` / `222222222222`.

## Pointers

- `README.md` + `docs/architecture.md` — the wider picture.
- `docs/in-cluster-on-kx.md` / `docs/deploy-on-hub.md` — deploy runbooks.
- `CLAUDE.md` — Claude Code instructions for working *inside* the repo.
- Sibling entry points: `eks-agent-platform` (the Tenant CRDs portal reads/writes), `eks-fleet` (the `Cluster` CRs it vends), `landing-zone` (the substrate it drives), `eks-gitops`, `cloudgov`.
