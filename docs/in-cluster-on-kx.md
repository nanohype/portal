# Running portal in-cluster on kx

This stands portal up **inside the kx hub** (the local kind mirror of the
eks-fleet hub) with the hub-side watchers on, so the live operations surfaces
work: place a vend in the browser and watch its full journey advance, plus the
deprovision teardown watch, the org-wide ops feed, and per-cluster ArgoCD health
badges. It's the in-cluster counterpart to the `rung-1-via-portal` runbook
(`eks-fleet/docs/rung-1-via-portal.md`), which runs portal *locally* — locally
the watchers are off, so the timeline parks at `committed` and you chase the
substrate by hand. In-cluster is where those surfaces light up.

You drive this; it's a runbook, not a script.

## What lights up on kx (and what doesn't)

- ✅ Vend timeline (committed → building → active, with the live tofu phase/error)
- ✅ Deprovision teardown watch (→ deprovisioned)
- ✅ Org-wide ops feed (`/ops`)
- ✅ Per-cluster **ArgoCD** sync·health badge (read from kx's ArgoCD on the hub)
- ⚠️ Per-cluster **EKS control-plane** badge — blank on **kx only**. kind has no
  IRSA, so `eks:DescribeCluster` can't authenticate. On a real EKS hub it works:
  the worker's IRSA role (`serviceAccount.roleArn`) assumes the per-account role,
  which grants `eks:DescribeCluster` and carries the trust-policy ExternalId, and
  the badge fills in.

## Prerequisites

The hub itself, through rung-1 steps 0–5 (kx up: Crossplane v2 + provider-opentofu
+ the `aws-creds` secret + the Cluster API + composition), plus:

1. **kx is up** with ArgoCD (`kubectl --context kind-kx get ns argocd`).
2. **The `clusters` appset applied to kx's ArgoCD** + a read repo-cred for the
   clusters repo. It's a per-cluster files generator — applying it:
   `kubectl --context kind-kx apply -f ../eks-gitops/applicationsets/clusters-appset.yaml`.
3. **The `platform` AppProject exists on kx** (hand-made — kx doesn't ship it).
4. **A deploy key with WRITE on the clusters repo** (portal's worker pushes CRs
   over SSH). Don't read or commit the key material.
5. **An S3 bucket + access key** for portal's OpenTofu state. The chart bundles
   no object store — point it at a small bucket you control, in any region.
6. **A Postgres and a Redis reachable from the namespace.** Portal bundles
   neither, and kind has no managed RDS/ElastiCache, so stand them up in the
   `portal` namespace (step 3). Redis is optional — leave `redis.url` empty and
   log streaming runs in-memory.
7. **AWS credentials reachable by the worker.** When the vended spoke
   auto-registers as `eks_iam`, the worker mints an EKS token by assuming the
   account's role — that needs a base AWS identity. On kx (no IRSA) inject static
   creds (step 4). Without them the timeline still reaches `active`, but the
   spoke's connection test fails; ArgoCD health still works.

## 1. Build + load the images into kx

The chart references local `portal/*:latest` images (pullPolicy IfNotPresent, no
registry), so build and side-load them into kind — no registry needed.

```bash
cd portal
task docker:build      # portal/server|worker|web|migrate :latest
kind load docker-image portal/server:latest portal/worker:latest \
  portal/web:latest portal/migrate:latest --name kx
```

## 2. Write `values-kx.yaml`

Keep this file out of git (it holds the deploy key + S3 creds).

```yaml
config:
  environment: development          # enables Dev Login (first user = owner)
  # in development the default jwt/encryption keys are accepted; override for real use

objectStore:                        # your state bucket (external — the chart bundles none)
  endpoint: "s3.us-west-2.amazonaws.com"
  bucket: "<your-portal-state-bucket>"
  region: "us-west-2"
  useSSL: true
  accessKey: "<S3 access key id>"
  secretKey: "<S3 secret access key>"

gitops:
  clustersRepoURL: "git@github.com:nanohype/clusters.git"
  sshKey: |
    -----BEGIN OPENSSH PRIVATE KEY-----
    <clusters-repo WRITE deploy key — do not commit this file>
    -----END OPENSSH PRIVATE KEY-----

clusterWatchback:                   # the vend loop's closing leg
  enabled: true
  interval: "30s"
clusterHealth:                      # steady-state ArgoCD (+ EKS) health
  enabled: true
  interval: "60s"

# kx is kind — no IRSA, so leave roleArn empty (the EKS-describe half stays blank).
serviceAccount:
  roleArn: ""

# AWS creds for the worker (step 4) — referenced by name; the Secret is created
# before install so the worker mounts it at start. On a real hub use IRSA
# (serviceAccount.roleArn) and drop this.
worker:
  extraEnvFrom:
    - secretRef:
        name: portal-aws

# Portal bundles no database/cache — point these at the ones from step 3.
database:
  url: "postgres://portal:portal@postgres:5432/portal?sslmode=disable"
redis:
  url: "redis://redis:6379"
```

## 3. Stand up Postgres + Redis

Both live in the `portal` namespace so the in-namespace DNS names in
`values-kx.yaml` resolve:

```bash
kubectl --context kind-kx create namespace portal
kubectl --context kind-kx -n portal create deployment postgres --image=postgres:17
kubectl --context kind-kx -n portal set env deploy/postgres \
  POSTGRES_USER=portal POSTGRES_PASSWORD=portal POSTGRES_DB=portal
kubectl --context kind-kx -n portal expose deploy/postgres --port=5432
kubectl --context kind-kx -n portal create deployment redis --image=redis:7
kubectl --context kind-kx -n portal expose deploy/redis --port=6379
```

## 4. Create the worker AWS secret (kx only)

So the worker can mint EKS tokens for the vended spoke, give it a base AWS
identity — an access key that can `sts:AssumeRole` into the account role (with
that role mapped in the spoke's access entries). The chart's `worker.extraEnvFrom`
(set in step 2) references this Secret by name; create it **before** install so
the worker mounts it at start:

```bash
kubectl --context kind-kx -n portal create secret generic portal-aws \
  --from-literal=AWS_ACCESS_KEY_ID=... --from-literal=AWS_SECRET_ACCESS_KEY=... \
  --from-literal=AWS_REGION=us-west-2
```

On a real hub use IRSA (`serviceAccount.roleArn`) instead, and drop both the
Secret and `worker.extraEnvFrom`.

## 5. Install

```bash
helm install portal deploy/helm/portal -n portal -f values-kx.yaml --kube-context kind-kx
kubectl --context kind-kx -n portal get pods -w   # migrate job → server/worker/web Ready
```

Enabling `clusterWatchback` / `clusterHealth` is what creates the worker's
in-cluster RBAC: the cluster-wide read on eks-fleet `Cluster` XRs and
provider-opentofu `Workspace`s, plus a read on ArgoCD `Application`s scoped to
`argocdSync.namespace` (default `argocd`).

## 6. Open it + log in

The web pod's nginx proxies `/api/` to the server, so one port-forward serves
both the UI and the API:

```bash
kubectl --context kind-kx -n portal port-forward svc/portal-web 8080:80
```

Open `localhost:8080`, **Dev Login** (first user becomes `owner`).

## 7. Register an account

The Provision form needs an account to pick. In portal: **Accounts → Add** (owner
or admin), with the workload account's assume-role ARN and ExternalID. This is
the same account registration as the real-hub runbook — see
`docs/deploy-on-hub.md` §3 for how the role and ExternalID are produced.

## 8. Vend, and watch it live

In the in-cluster portal: **Clusters → Provision** (account = your mgmt account,
region `us-west-2`, team `platform`, env `development`). Then watch — no kubectl:

- The order's **timeline** advances queued → committed → **building** (with the
  live tofu phase, and any provider error inline) → **active**.
- **/ops** shows the vend in the org-wide feed.
- Once ArgoCD applies the per-cluster Application (`cluster-development-<name>`), the
  cluster's **ArgoCD badge** goes Synced · Healthy.

Real EKS spend starts when Crossplane begins the build (~the same ephemeral
spoke as rung-1; ~20–40 min to active).

## 9. Teardown

In portal: **Deprovision** the cluster. Watch the timeline go committed →
**destroying** → **removed** (`deprovisioned`). Then confirm zero-billable:
`aws eks list-clusters` is `[]`; run cloudgov to sweep the log group / Karpenter
residue tofu destroy can't reach.

> ⚠️ **Never cycle the provider-opentofu pod mid-apply** — it orphans an
> empty-state vend (live AWS, empty S3 state) and `external-create-pending` then
> blocks both create and delete. If a vend wedges, follow the teardown in the
> `crossplane-vend-create-pending-deadlock` note (drop the Workspace finalizer +
> delete the AWS resources directly), don't restart the provider.

## Verify

- Order → `cluster_operations` row `committed` with a git SHA, and the CR at
  `clusters/development/<name>.yaml` in the clusters repo.
- `kubectl --context kind-kx -n argocd get applications` shows `cluster-development-<name>`
  Synced; `kubectl --context kind-kx get cluster,workspace -n platform` exists.
- Portal timeline reaches **active**; `/ops` lists it; the ArgoCD badge is set.
- Teardown → `deprovisioned`; `aws eks list-clusters` → `[]`.

## Non-goals

- The EKS control-plane badge — needs a real EKS hub with IRSA.
- Production secrets hygiene — `values-kx.yaml` holds plaintext creds for local
  use; a real hub uses sealed-secrets / external-secrets and IRSA.
