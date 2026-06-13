# portal

Self-hosted operations portal for the nanohype k8s stack. It started as an
OpenTofu lifecycle UI and grew into one place — with one audit trail — to run
the substrate: OpenTofu workspaces, AWS accounts, EKS clusters, and
eks-agent-platform tenants. Go backend (API server + River worker), React
frontend.

## Capability layers

Each layer stands on its own; pick the ones you need.

- **OpenTofu lifecycle** — workspaces, pipelines, runs, plan diff, state versions, variable inheritance, VCS webhooks, terragrunt auto-detection.
- **AWS accounts** — account entities with stored assume-role creds, the base for cross-account work.
- **EKS clusters** — cluster entities with slim encrypted creds and an async connection-test job.
- **Tenant read** — a per-cluster watcher walks the `platform.nanohype.dev` Tenant CRDs and reconciles a DB inventory.
- **Tenant write** — a UI form helm-renders the eks-agent-platform tenant chart, commits to a tenants GitOps repo, and lets ArgoCD reconcile.
- **Templates** — admins set default tenant values and caps; operators instantiate from them with server-side enforcement of budget, model-family, and compliance.
- **Team-scoping** — non-admins see only their teams' entities; admins see everything and manage the grants.
- **Unified catalog** — one searchable grid across every entity a user can see.
- **Operations daily-driver** — a vend timeline (queued → committed → building → active with the live tofu phase/error), a deprovision teardown watch, an org-wide ops feed, and per-cluster health (ArgoCD sync/health + EKS control-plane badges) fed by in-cluster hub watchers.

## Quick Start

Prerequisites: Go 1.26+, Node.js 20+, Docker, [Task](https://taskfile.dev).

```bash
git clone https://github.com/nanohype/portal.git && cd portal
task setup
task dev
```

`docker compose up -d` starts Postgres and Redis and runs migrations. `task dev`
migrates, then starts server + worker + web in parallel. Open
http://localhost:5173 and click **Dev Login** — no GitHub OAuth needed locally,
the first user gets the `owner` role.

| Process | Address | Purpose |
|---------|---------|---------|
| server | `:8080` | Go API — auth, CRUD, WebSocket log streaming |
| worker | `:8081` | River job processor — runs `tofu` / `terragrunt` |
| web | `:5173` | Vite dev server — React SPA with HMR |

## Docs

- [architecture.md](docs/architecture.md) — the three processes, how they talk, and the cluster ops surface.
- [configuration.md](docs/configuration.md) — the full env-var reference, including the S3 static-key and IRSA paths and the watcher knobs.
- [variables.md](docs/variables.md) — the org → pipeline → workspace scopes, precedence, and tag deep-merge.
- [pipelines.md](docs/pipelines.md) — sequential workspace runs with output passing between stages.
- [deployment.md](docs/deployment.md) — the deployment hub: docker-compose for dev, the Helm chart, and the runbook index.

Runbooks (the runbook index in [deployment.md](docs/deployment.md) tells you which to follow):

- [docs/in-cluster-on-kx.md](docs/in-cluster-on-kx.md) — run portal in-cluster on the kx (kind) hub with watchers on.
- [docs/deploy-on-hub.md](docs/deploy-on-hub.md) — deploy portal on a real EKS hub (IRSA + the cross-account IAM wiring).

## License

MIT
