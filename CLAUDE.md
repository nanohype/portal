# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is this project?

Portal is a self-hosted operations portal for the nanohype stack. It started
as an OpenTofu lifecycle management UI (Terraform Cloud / Spacelift
alternative) and grew into a unified portal that also manages AWS accounts,
EKS clusters, and eks-agent-platform tenants. Go backend (API server + job worker) and
React frontend.

Capability layers (each can be used independently):

1. **OpenTofu lifecycle.** Workspaces, pipelines, runs, plan diff, state
   versions, variable inheritance (org/pipeline/workspace, deep-merge for
   tags), VCS webhooks, terragrunt auto-detection.
2. **AWS account surface.** Account entities with stored assume-role creds.
   Foundation for cross-account operations.
3. **EKS cluster surface.** Cluster entities with slim creds (api endpoint
   + CA + service-account token, encrypted). Async connection-test job.
4. **eks-agent-platform tenant surface (read).** Periodic per-cluster watcher walks the
   `platform.nanohype.dev/v1alpha1` Tenant CRDs and reconciles a DB inventory.
5. **eks-agent-platform tenant surface (write).** UI form → helm-renders the eks-agent-platform
   `charts/tenant` chart → commits to a tenants GitOps repo → ArgoCD
   reconciles. Operations are tracked in a tenant_operations log.
6. **Curated templates.** Admins define default tenant values + caps;
   operators instantiate from them. Server-side enforcement of budget
   caps + model-family intersection + required-compliance flags.
7. **Team-scoped self-service.** tenant_team_access and
   template_team_access pivot tables. Non-admins see only their teams'
   entities; admins see everything and manage the grants.
8. **Unified catalog.** Frontend-only aggregation page that surfaces
   every entity a user can see in one searchable + filterable grid.

## Quick Reference

```bash
# Start dev environment
docker compose up -d          # Postgres, Redis, MinIO + auto-migrate
task dev                      # Migrates, then starts server + worker + web

# Or start individually
task dev:server               # API server on :8080
task dev:worker               # Job worker on :8081
task dev:web                  # Vite dev server on :5173

# Bootstrap a fresh instance (idempotent; configurable via env vars — see scripts/seed.sh)
task seed                     # AWS org vars + 4 landing-zone workspaces + eks-gitops-prereqs pipeline

# Verify changes
go build ./...                                   # Backend compiles
go test ./internal/handler/ ./internal/auth/ \
  ./internal/worker/ ./internal/vcs/ \
  ./internal/server/ ./internal/domain/ \
  ./internal/tfparse/                            # Run tests
cd web && npx tsc -b && npx vite build            # Frontend compiles (tsc -b is the real check)

# Reset database (drops everything, re-migrates)
docker compose down -v && docker compose up -d
```

## Architecture

Three processes: **server** (HTTP API), **worker** (runs tofu commands), **web** (React SPA). They communicate through Postgres (data + job queue via River) and Redis (log streaming pub/sub).

### Backend (Go 1.26)

- **Router**: chi (`internal/server/server.go` — all routes defined here)
- **Handlers**: `internal/handler/` — one file per domain (auth, workspace, run, pipeline, pipeline_variables, org_variables, variables, teams, state, user, audit, webhook, approvals, health)
- **Services**: `internal/service/` — business logic layer (workspace, run, pipeline, audit)
- **Repository**: `internal/repository/` — hand-written pgx queries in `*.sql.go` (sqlc-style: typed Params structs + `scanX` helpers, but NOT generated — edit them directly; there is no sqlc/codegen step)
- **Worker**: `internal/worker/jobs.go` — River job worker with pipeline callback; `pipeline_jobs.go` for pipeline stage jobs
- **Auth**: GitHub OAuth → JWT, RBAC with roles owner > admin > operator > viewer
- **Response helpers**: `internal/handler/respond/respond.go` — use `respond.JSON()`, `respond.Error()`, `respond.NoContent()`

### Worker Variable Merge

The worker loads variables from three scopes and merges them at run time:
- **org_variables** → lowest precedence
- **pipeline_variables** → middle (only when run belongs to a pipeline)
- **workspace_variables** → highest, always wins

Tag variables (`tags`, `default_tags`, `*_tags`) are deep-merged as JSON maps instead of replaced. The `mergeVariables()` function in `jobs.go` is a pure function with test coverage.

### Pipeline Orchestration

Pipeline is an orchestrator, not an executor. `PipelineStageJobWorker` imports outputs from the previous stage, creates a workspace run via `RunService.Create()`, then exits. When the run finishes, `advancePipelineIfNeeded()` in `RunJobWorker.Work()` advances the pipeline. `AutoApplyOverride` on `RunJobArgs` lets pipeline stages override workspace auto_apply settings.

### Frontend (React)

- **Stack**: Vite 7, React 19, TypeScript, Tailwind CSS 4, TanStack Query, Zustand
- **Theme**: Neutral dark palette (#0A0A0B base, #3E8E82 teal primary, #CF222E destructive) with Inter (UI) + JetBrains Mono (IDs/code), 13px base, glass effects — defined in `web/src/index.css`
- **API client**: `web/src/api/client.ts` — openapi-fetch with typed paths from `web/src/api/types.ts`
- **Components**: `web/src/components/` — organized by domain (workspace/, pipeline/, run/, teams/, settings/, ui/)
- **Routing**: TanStack Router in `web/src/router.tsx` (auth-gated layout route, typed params)
- **Notifications**: sonner toasts on all mutations
- **Terminal**: xterm.js for run log streaming via WebSocket

## Key Patterns

- **IDs**: ULIDs everywhere (`ulid.Make().String()`)
- **Multi-tenant**: `org_id` on every query for tenant isolation
- **Partial updates**: `*bool` pointers + `COALESCE` in SQL for optional fields
- **Error responses**: explicit status → `respond.Error(w, http.StatusXxx, "message")`. For a service-layer error, `respond.FromError(w, r, err)` maps it in one place (`pgx.ErrNoRows` → 404; `apperr.NotFound/Forbidden/Conflict/Validation` → their codes; anything else → 500 with the cause logged) so the same failure can't be 404 in one handler and 500 in another. Never write raw JSON
- **Audit logging**: all mutations log via `auditSvc.Log()` with before/after state, values redacted to `***`
- **Variables**: `terraform` category → tfvars file; `env` category → process environment
- **Encryption**: sensitive variables encrypted with AES-256 via `secrets.Encryptor`, decrypted in worker at run time
- **Tests**: pure functions extracted for testability; test files alongside source
- **Import cycle avoidance**: `worker` → `service` is one-directional. Pipeline stage worker uses `RunCreatorFunc` and `OutputImporter` function types instead of importing service directly.
### Terragrunt support

Terragrunt is a co-equal first-class wrapper alongside plain tofu. The worker
auto-detects which to use and adapts; nothing in the workspace model carries
a "wrapper" flag.

- **Auto-detection.** `executor.DetectBinary(workDir)` in
  `internal/worker/executor/executor.go` checks for `terragrunt.hcl` at the
  staged `working_dir`. Present → `terragrunt` drives the run (it walks parent
  dirs and renders terraform itself). Absent → `tofu` drives. Terragrunt
  repos must upload (or have their VCS source clone) the full directory tree
  so `find_in_parent_folders` lookups resolve — set `working_dir` to the leaf.

- **Run-time env defaults.** The worker always sets two env vars for
  terragrunt runs (harmless in tofu mode; tofu ignores `TG_*`):
  - `TG_NON_INTERACTIVE=true` — no human to prompt; terragrunt auto-confirms.
  - `TG_BACKEND_BOOTSTRAP=true` — auto-create the S3 remote-state bucket on
    first init (no-op once it exists). Without this, the first `init`
    against a fresh backend errors with "S3 bucket does not exist".

  Both are surfaced in the run log so users see what portal is silently
  doing on their behalf.

- **Variables.**
  - `terraform`-category vars → injected as `TF_VAR_<key>=<value>` process
    env (worker + K8s executors). `TF_VAR_` is terraform's
    lowest-precedence source — keys terragrunt also sets via `inputs = {}`
    (passed as `-var`, highest precedence) silently win; keys terragrunt
    doesn't set are picked up cleanly from the env.
  - `env`-category vars → plain process env (`AWS_PROFILE`, `AWS_REGION`,
    etc.).
  - `portal.auto.tfvars` is **not** written in terragrunt mode — it'd land
    in the leaf, not the rendered cache dir, so it'd be ignored anyway.

- **State encryption is skipped.** portal's per-workspace AES-GCM override
  (`portal_encryption_override.tf`, derived passphrase from
  `secrets.Encryptor.DerivePassphrase("state:"+workspaceID)`) is disabled
  for terragrunt workspaces. Terragrunt copies the leaf's `.tf` files into
  the rendered cache alongside the module source, so an override at the
  leaf would silently encrypt remote state with a per-workspace passphrase
  — and break `dependency` blocks (which invoke `tofu output -json` in
  sibling workspaces without the override). State encryption for
  terragrunt workspaces is the user's backend concern (S3 SSE-KMS in
  `root.hcl`'s `remote_state` block).

- **State capture via `state pull`.** No local `terraform.tfstate` exists
  at the leaf (state lives in the remote backend). The upload +
  `CreateStateVersion` path uses `StateJSON` (from `tofu/terragrunt state
  pull`) instead — if EITHER `StateFile` or `StateJSON` is populated, a
  state-version row is written. Without this, pipeline output import
  (`ImportOutputs`) and the State tab would never see any state for
  terragrunt workspaces.

- **Variable discovery via `terragrunt render --json`.** The
  `POST /workspaces/{id}/variables/discover` endpoint shells out to
  `terragrunt render --json --log-disable --non-interactive --working-dir
  <leaf>` to get the merged inputs (leaf + includes + `_envcommon`) plus
  the resolved `terraform.source` module path. It parses the module's
  `variables.tf` via `tfparse.ParseDirectory` for the canonical schema and
  merges via `mergeDiscovered`: every module variable is returned, marked
  `configured_by: "terragrunt"` for keys terragrunt resolves,
  `configured_by: "portal"` for keys present in workspace_variables, or
  unconfigured (editable via Add). Falls back to leaf-only
  `tfparse.ParseTerragruntInputs` when render fails or the module source
  is remote.

## Common Tasks

### Adding a new API endpoint

1. Add handler method in `internal/handler/<domain>.go`
2. Wire route in `internal/server/server.go` (inside the `r.Route("/api/v1", ...)` block)
3. Add the TypeScript types + `paths` entry in `web/src/api/types.ts` (the hand-maintained API contract — there is no codegen step)

### Adding a new frontend page/component

1. Add component in `web/src/components/`
2. Add route in `web/src/App.tsx` (regex pattern matching)
3. Use `useQuery`/`useMutation` from TanStack Query for data fetching
4. Use `toast.success()`/`toast.error()` from sonner for feedback

### Adding a database migration

All schema is consolidated in `migrations/000001_initial_schema.up.sql`. For dev, modify the file directly and `docker compose down -v && docker compose up -d` to reset. For production, create a new numbered migration pair.

## Environment

- All config via env vars with dev defaults (see `internal/domain/config.go`)
- `ENVIRONMENT=development` relaxes validation (allows default JWT/encryption keys, enables dev login)
- Server: `:8080`, Worker health: `:8081`, Vite: `:5173`

## Don't

- Don't use `fmt.Fprintf(w, ...)` for HTTP responses — use `respond.JSON()` / `respond.Error()`
- Don't forget `org_id` in database queries — every query must be org-scoped
- Don't put AWS credentials as `terraform` category variables — use `env` category
- Don't import `service` from `worker` — use function types to avoid import cycles
- Don't truncate text in the UI — always show full content
- Don't expect portal-managed variables to override terragrunt's `inputs = {}` block — they go in as `TF_VAR_*` which is lower precedence than terragrunt's `-var`. The Discover UI marks terragrunt-owned keys as `configured_by: terragrunt` so users see they can't override. To change those values, edit the terragrunt.hcl itself.
- Don't upload only the leaf for a terragrunt workspace; the archive must contain the parent tree (`root.hcl`, `_envcommon/`, etc.) so `find_in_parent_folders` resolves.
