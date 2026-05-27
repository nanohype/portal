# portal

Self-hosted operations portal. Manages OpenTofu workspaces, AWS accounts,
EKS clusters, and EAP tenants from one UI with one audit trail.

Plan, apply, and manage OpenTofu workspaces through a web interface with
pipelines, variable inheritance, team access controls, approval workflows,
audit logging, and VCS-driven runs — the Terraform Cloud / Spacelift
alternative tier. On top of that: a multi-account AWS surface, EKS cluster
watching, EAP tenant CRUD via GitOps + Helm + ArgoCD, curated tenant
templates, team-scoped self-service, and a unified catalog across every
entity.

## Quick Start

Prerequisites: Go 1.26+, Node.js 20+, Docker, [Task](https://taskfile.dev)

```bash
# Clone and set up
git clone https://github.com/nanohype/portal.git && cd portal
task setup

# Start everything
task dev
```

`docker compose up -d` starts Postgres, Redis, MinIO, and runs migrations automatically. `task dev` migrates and starts server + worker + web in parallel.

Open http://localhost:5173 and click **Dev Login**. No GitHub OAuth needed for local development — the first user gets the `owner` role.

### What `task dev` Starts

| Process | Address | Purpose |
|---------|---------|---------|
| server | `:8080` | Go API — auth, CRUD, WebSocket log streaming |
| worker | `:8081` | Job processor — runs `tofu` commands |
| web | `:5173` | Vite dev server — React SPA with HMR |

## Features

- **Pipelines** — orchestrate sequential workspace deployments with automatic output passing between stages
- **Variable Inheritance** — org, pipeline, and workspace scopes with deep-merge for tags
- **Plan / Apply / Destroy** with run queuing, cancellation, and real-time log streaming
- **VCS Integration** — GitHub push webhooks trigger automatic plan runs
- **Upload Workspaces** — manage infrastructure without a Git repo
- **Approval Workflows** — require manual approval before apply, with auto-apply option
- **Team Access Controls** — RBAC roles (owner / admin / operator / viewer) with cloud identity mapping
- **Variable Management** — encrypted sensitive values, variable discovery, bulk import, tag editor
- **State Management** — versioned state in S3, resource browser, state version diffing
- **Plan Diff Viewer** — attribute-level change visualization from JSON plan output
- **Audit Logging** — all mutations logged with before/after state
- **Real-time Logs** — WebSocket streaming via xterm.js terminal

## Architecture

```
┌─────────────┐     ┌──────────────────┐     ┌──────────┐
│  web (SPA)  │────>│  server (:8080)  │────>│ Postgres │
│ Vite+React  │     │  Go / chi        │     │ (data +  │
└─────────────┘     └──────┬───────────┘     │  jobs)   │
                           │                 └──────┬───┘
                    WebSocket (logs)          ┌──────┴──┐
                           │                 │  Redis  │ (pub/sub)
                    ┌──────┴───────────┐     └──────┬──┘
                    │ worker           │────────────┘
                    │ River jobs       │────> tofu init/plan/apply
                    │                  │────> MinIO (state + logs)
                    │ ┌──────────────┐ │
                    │ │ pipeline     │ │ stage job → import outputs
                    │ │ stage worker │ │ → create workspace run
                    │ └──────────────┘ │ → callback advances pipeline
                    └──────────────────┘
```

See [docs/architecture.md](docs/architecture.md) for the full breakdown.

## Pipelines

Pipelines run multiple workspaces in sequence, automatically importing outputs between stages:

```
network → cluster → cluster-bootstrap → cluster-addons
```

Each stage creates a regular workspace run. Outputs from the previous stage are imported as terraform variables. Supports auto-apply per stage, on_failure (stop/continue), and approval pausing.

See [docs/pipelines.md](docs/pipelines.md) for details.

## Variables

Variables exist at three scopes with clear precedence:

```
org variables  <  pipeline variables  <  workspace variables
```

Workspace always wins. Tag variables (`tags`, `default_tags`, `*_tags`) are deep-merged as JSON maps across scopes — org-wide tags combine with workspace-specific tags.

See [docs/variables.md](docs/variables.md) for details.

## Development

```bash
task dev:server     # API server only
task dev:worker     # Worker only
task dev:web        # Vite dev server only

task infra:up       # Start Postgres, Redis, MinIO
task infra:down     # Stop infrastructure
task db:migrate     # Run migrations
task db:reset       # Drop and recreate database

task test           # go test ./...
task lint           # go vet + tsc --noEmit
task build          # Build Go binaries
task docker:build   # Build all Docker images
```

## Configuration

All config is via environment variables. Only `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET` are required for local dev — everything else has working defaults.

See [docs/configuration.md](docs/configuration.md) for the full reference.

## Deployment

Docker images, Helm chart, and production configuration guide.

See [docs/deployment.md](docs/deployment.md).

## Project Structure

```
cmd/
  server/           API server entrypoint
  worker/           Job worker entrypoint
  migrate/          Database migration runner
internal/
  auth/             JWT + RBAC middleware
  domain/           Config, shared types
  handler/          HTTP handlers (workspace, run, pipeline, variables, teams, etc.)
  logstream/        Real-time log fan-out (memory + Redis)
  repository/       Database queries (pgx, hand-written sqlc-style)
  secrets/          AES-256 encryption for sensitive variables
  server/           Router setup, middleware
  service/          Business logic (workspace, run, pipeline, audit)
  storage/          S3/MinIO client
  tfparse/          .tf file parser (variable discovery)
  tfstate/          State file parsing and diffing
  vcs/              GitHub webhook parsing + HMAC verification
  worker/
    executor/       OpenTofu execution (local + kubernetes)
web/src/
  api/              API client (openapi-fetch) + types
  components/       React components by domain (workspace, pipeline, run, teams, settings)
  hooks/            Custom hooks
migrations/         SQL schema (golang-migrate)
api/openapi/        OpenAPI v3.1 spec
deploy/helm/        Helm chart for Kubernetes
docker/             Dockerfiles
docs/               Architecture, deployment, and feature docs
```

## License

MIT
