# Deploying portal on a production EKS hub

This stands portal up on a real eks-fleet **hub EKS cluster** with
its IAM codified (portal#41), so the full surface works — including the parts that
stay dark on kx: cross-account EKS reads, the per-cluster EKS control-plane badge,
and tenant inventory from spoke clusters. It's the production counterpart to
`in-cluster-on-kx.md` (kind, no IRSA).

You drive this. The new, error-prone part is the four-repo IAM wiring (§1, §3, §4)
and its two **match-ups** — read those carefully.

## What's different from kx

- portal runs under **IRSA**, not static creds — `serviceAccount.roleArn` is the
  `portal-hub` role; S3 (the objectStore) and every AWS call use that role's
  ambient credentials. No keys at rest (`worker.extraEnvFrom` / static S3 keys
  are kx-only).
- The per-cluster **EKS control-plane badge** fills in (`eks:DescribeCluster` via
  the per-account `portal-spoke` role).
- **Tenant inventory** populates from each spoke (the `portal-reader` access).

## Prerequisites

- An eks-fleet hub up (a real EKS hub cluster running ArgoCD + Crossplane +
  provider-opentofu + the Cluster API), per rung-1 on real EKS — not kind.
- The clusters appset applied to the hub's ArgoCD (eks-gitops `clusters-appset`,
  the per-cluster files generator).
- A deploy key with write on the clusters repo (for portal's worker).
- AWS access to apply landing-zone components to the hub account and
  to each workload (spoke) account.

## 1. Apply the IAM

**On the hub account (where the hub EKS cluster lives) — `portal-hub`:** the
worker IRSA role + the portal OpenTofu state bucket. It reads the hub cluster's
OIDC provider, so run it in that account. Everything resolves from the live
cluster — paste the block, eyeball the echo, apply:

```bash
cd landing-zone/components/aws/portal-hub

CLUSTER=hub-eks                  # if this 404s, run `aws eks list-clusters` for the name
export AWS_REGION=us-west-2
OIDC_ISSUER=$(aws eks describe-cluster --name "$CLUSTER" --region "$AWS_REGION" \
  --query 'cluster.identity.oidc.issuer' --output text)
ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
OIDC_PROVIDER_ARN="arn:aws:iam::${ACCOUNT_ID}:oidc-provider/${OIDC_ISSUER#https://}"
STATE_BUCKET="nanohype-portal-state-${ACCOUNT_ID}"   # globally unique (account-scoped)
printf 'provider=%s\nbucket=%s\n' "$OIDC_PROVIDER_ARN" "$STATE_BUCKET"

tofu init -upgrade               # first run only (no terragrunt here = local state)
tofu apply \
  -var environment=production \
  -var oidc_provider_arn="$OIDC_PROVIDER_ARN" \
  -var oidc_issuer="$OIDC_ISSUER" \
  -var state_bucket_name="$STATE_BUCKET" \
  -var namespace=portal -var service_account_name=portal-worker
tofu output            # hub_role_arn, state_bucket_name — keep both for §2
```

> **Namespace match-up.** `namespace`/`service_account_name` pin the IRSA trust to
> `system:serviceaccount:`**`portal`**`:portal-worker`, so the chart must land the
> worker SA in the **`portal`** namespace — deploy with `NAMESPACE=portal` in §2
> (the chart names the worker SA `<release>-worker`). Mismatch → every AssumeRole
> 403s. `role_name` defaults to `portal-worker`.

**In each workload (spoke) account — `portal-spoke`:** the per-account read role.
Grab the hub role ARN from portal-hub's state (local — readable on any profile),
then switch your creds to the spoke account:

```bash
HUB_ROLE_ARN=$(tofu output -raw hub_role_arn)   # run from the portal-hub dir (the step above)
cd ../portal-spoke
# switch AWS creds to THIS workload account first (e.g. export AWS_PROFILE=<spoke>)
tofu init -upgrade
tofu apply \
  -var environment=production \
  -var portal_hub_role_arn="$HUB_ROLE_ARN" \
  -var external_id=portal          # the default; whatever you choose, see §3
tofu output            # spoke_role_arn, external_id
```

> **MATCH-UP #1 — the ExternalId.** The spoke role's trust policy requires
> `sts:ExternalId == external_id`. Whatever you set here, you must enter the SAME
> value as the Account's **ExternalID** in portal (§3). Mismatch → every
> AssumeRole silently 403s, and clusters never connect.

## 2. Deploy portal on the hub

Three things to wire: **(2a)** the container images, **(2b)** the clusters GitOps
repo portal vends through, **(2c)** the chart itself.

### 2a. Build + push the images

Portal publishes no images — build the four and push them to **ECR in the hub
account** (the node role pulls from ECR with no imagePullSecret). Build **arm64** —
the hub's system nodes are Graviton:

```bash
ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
REGION=us-west-2
ECR="${ACCOUNT_ID}.dkr.ecr.${REGION}.amazonaws.com"
TAG=$(git rev-parse --short HEAD)

aws ecr get-login-password --region "$REGION" | docker login --username AWS --password-stdin "$ECR"
for r in server worker web migrate; do
  aws ecr create-repository --repository-name "portal/$r" --region "$REGION" >/dev/null 2>&1 || true
  docker buildx build --platform linux/arm64 -f "docker/Dockerfile.$r" -t "$ECR/portal/$r:$TAG" --push .
done
echo "pushed $ECR/portal/{server,worker,web,migrate}:$TAG"
```

> Build amd64 by mistake (plain `docker build` on an Intel host) and the pods
> `CrashLoop` with an exec-format error on the arm64 nodes. `kubectl get nodes -o wide`
> confirms node arch.

### 2b. Wire the clusters GitOps repo

Portal vends git-only: the worker commits a `Cluster` CR to `nanohype/clusters`, and
the hub's ArgoCD applies it. That needs **two deploy keys** on that repo (a GitHub key
can be a deploy key on only one repo, so write and read are separate) plus the
ApplicationSet that reconciles the CRs.

```bash
# (i) WRITE key — portal's worker pushes the CR (no passphrase: go-git reads the file directly)
ssh-keygen -t ed25519 -f ~/.ssh/portal-clusters-rw -N "" -C "portal-clusters-rw"
gh repo deploy-key add ~/.ssh/portal-clusters-rw.pub --repo nanohype/clusters --title portal-clusters-rw --allow-write

# (ii) READ key — the hub's ArgoCD pulls the CR
ssh-keygen -t ed25519 -f ~/.ssh/portal-clusters-ro -N "" -C "argocd-clusters-ro"
gh repo deploy-key add ~/.ssh/portal-clusters-ro.pub --repo nanohype/clusters --title argocd-clusters-ro

# (iii) register the READ key + apply the appset on the hub (run against the hub context):
kubectl create secret generic clusters-repo -n argocd \
  --from-literal=type=git \
  --from-literal=url=git@github.com:nanohype/clusters.git \
  --from-file=sshPrivateKey="$HOME/.ssh/portal-clusters-ro"
kubectl label secret clusters-repo -n argocd argocd.argoproj.io/secret-type=repository
kubectl apply -f eks-gitops/applicationsets/clusters-appset.yaml
```

> Without the read cred + appset, portal pushes the CR to GitHub and **nothing
> vends**. The appset needs the `platform` AppProject — the hub's cluster-bootstrap
> already created it (`kubectl get appproject platform -n argocd` to confirm).

### 2c. Install the chart

The **WRITE** key (`~/.ssh/portal-clusters-rw`) is portal's `SSH_KEY_PATH` /
`gitops.sshKey`; the images are the ECR refs from 2a. Install with IRSA + the state
bucket + the watchers on. Create `values-hub.yaml` — a file you author (it's **not**
in the repo), and **keep it out of git**: it holds the write deploy key + secrets.
Put it wherever you run `helm` (the portal repo root → `helm … -f values-hub.yaml`),
or pass an absolute path. The `task hub:install` path below skips this file
entirely (the key goes via `SSH_KEY_PATH`, secrets via `EXTRA_ARGS`).

```yaml
config:
  environment: production          # real GitHub OAuth + non-default secrets
  # set jwtSecret, encryptionKey (32 bytes), githubClientID/Secret

image:                             # the ECR refs from 2a ($ECR/portal/<svc>:$TAG)
  server: { repository: <ECR>/portal/server, tag: <TAG> }
  worker: { repository: <ECR>/portal/worker, tag: <TAG> }
  web:    { repository: <ECR>/portal/web,    tag: <TAG> }
  migrate:{ repository: <ECR>/portal/migrate,tag: <TAG> }

serviceAccount:
  roleArn: <hub_role_arn>          # IRSA — the worker assumes spokes + uses S3

objectStore:
  endpoint: "s3.<region>.amazonaws.com"
  bucket: <state_bucket_name>      # created by portal-hub
  region: <region>
  useSSL: true
  # accessKey/secretKey EMPTY — the SDK uses the worker's IRSA credentials

gitops:
  clustersRepoURL: "git@github.com:nanohype/clusters.git"
  sshKey: |
    <contents of ~/.ssh/portal-clusters-rw — the WRITE key from 2b; keep out of git>

clusterWatchback: { enabled: true, interval: "30s" }
clusterHealth:    { enabled: true, interval: "60s" }
```

```bash
helm dependency build deploy/helm/portal
helm install portal deploy/helm/portal -n portal --create-namespace -f values-hub.yaml
```

(`-n portal` matches the namespace `portal-hub` pinned in the IRSA trust — §1.)

Or skip the `values-hub.yaml` and let `task hub:install` bake the structural
options (IRSA, the objectStore, both watchers, the gitops remote) — you pass only
the per-hub bits:

```bash
# from the portal repo root — pull the portal-hub outputs (local state, any profile):
ROLE_ARN=$(cd ../landing-zone/components/aws/portal-hub && tofu output -raw hub_role_arn)
STATE_BUCKET=$(cd ../landing-zone/components/aws/portal-hub && tofu output -raw state_bucket_name)

task hub:install \
  ROLE_ARN="$ROLE_ARN" STATE_BUCKET="$STATE_BUCKET" REGION=us-west-2 NAMESPACE=portal \
  CLUSTERS_REPO_URL=git@github.com:nanohype/clusters.git \
  SSH_KEY_PATH="$HOME/.ssh/portal-clusters-rw" \
  EXTRA_ARGS="$(for r in server worker web migrate; do \
    printf -- '--set image.%s.repository=%s/portal/%s --set image.%s.tag=%s ' "$r" "$ECR" "$r" "$r" "$TAG"; done)"
# SSH_KEY_PATH = the WRITE key from 2b; the EXTRA_ARGS loop points all four images at
# your ECR refs from 2a (reuses $ECR/$TAG from that step's shell).
# ENVIRONMENT defaults to development (dev-login). For production add
# ENVIRONMENT=production + the secrets to EXTRA_ARGS ('--set config.jwtSecret=... -f secrets.yaml').
```

> Leave `objectStore.accessKey/secretKey` empty (the task does): portal's S3
> client falls back to the SDK default chain (IRSA web-identity), so the worker
> reaches the bucket as `portal-hub`. The `portal-hub` policy already grants r/w
> on that bucket.

## 3. Register the accounts

In portal (admin): **Accounts → add** one per workload account —

- **AssumeRoleARN** = the `spoke_role_arn` from §1.
- **ExternalID** = the `external_id` from §1 (**must match** — match-up #1).

## 4. Vend a cluster wired for portal

Provision through portal as usual, but set the Cluster's `portalAccessRoleArn` so
the cluster-stack grants `portal-spoke` its read access entry. If you order via
portal's form today it doesn't set this field yet — set it on the `Cluster` CR in
the clusters repo (or via the eks-fleet example), e.g.:

```yaml
spec:
  # ...the usual vend spec...
  vendRoleArn: <the account's fleet-vend role ARN>
  portalAccessRoleArn: <the account's spoke_role_arn>   # MATCH-UP #2
```

> **MATCH-UP #2 — `portalAccessRoleArn`.** Without it, the cluster-stack adds no
> portal access entry, so portal can mint a token but the kube API rejects it
> (not mapped) — the cluster shows `failed` connection, and the EKS badge still
> works (that's the AWS API, independent of the entry).

The cluster-stack maps `portal-spoke` to the `portal-reader` Kubernetes group; the
spoke's local ArgoCD reconciles the `portal-reader` ClusterRole from the eks-gitops
catalog (read on tenants/platforms + nodes, no Secrets). Both must be present for
the kube-API path to work.

## 5. Validate (#41)

On a vended, portal-wired spoke:

- **Connection** → the cluster registers `eks_iam` and goes `connected` (the token
  path: assume `portal-spoke` → mint EKS token → mapped via the access entry).
- **EKS control-plane badge** on the cluster fills in — `ACTIVE` + the platform
  version (`eks:DescribeCluster` as `portal-spoke`).
- **ArgoCD badge** → Synced · Healthy (the hub-side per-cluster Application).
- **Tenant inventory** for the cluster populates (the `portal-reader` read on the
  spoke's `platform.nanohype.dev/tenants`). This is the one the #41 quality review
  flagged — confirm it's NOT empty.
- **Worker logs** show no AssumeRole 403s (would mean match-up #1) and no tenant
  `list 403` (would mean the portal-reader addon hasn't reconciled on the spoke).

## Troubleshooting the match-ups

- **Cluster stuck `failed` / AssumeRole denied** → ExternalId mismatch (match-up
  #1). The Account's ExternalID must equal the spoke's `external_id`.
- **`connected` but tenant inventory empty** → either `portalAccessRoleArn` wasn't
  set (match-up #2, no access entry) or the `portal-reader` ClusterRole hasn't
  reconciled on the spoke (check the spoke's ArgoCD for the `portal-reader`
  Application). The connection-test passes regardless (it needs no tenant RBAC),
  so this won't show as a connection failure.
- **EKS badge empty but cluster connected** → the spoke role lacks
  `eks:DescribeCluster`, or it's not an `eks_iam` cluster. Check `portal-spoke`'s
  policy.

## Teardown

Deprovision via portal (or remove the cluster file); after teardown run cloudgov
to sweep residue. The IAM (`portal-hub`/`portal-spoke`) and the portal-reader addon
persist across cluster lifecycles — tear them down with `tofu destroy` per
component only when retiring portal from the account.
