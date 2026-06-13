# Deploying portal on a production EKS hub

This stands portal up on a real eks-fleet **management (hub) EKS cluster** with
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

- An eks-fleet hub up (a real EKS management cluster running ArgoCD + Crossplane +
  provider-opentofu + the Cluster API), per rung-1 on real EKS — not kind.
- The clusters appset applied to the hub's ArgoCD (eks-gitops `clusters-appset`,
  the per-cluster files generator).
- A deploy key with write on the clusters repo (for portal's worker).
- AWS access to apply landing-zone components to the hub (management) account and
  to each workload (spoke) account.

## 1. Apply the IAM

**On the hub (management) account — `portal-hub`:** the worker IRSA role + the
portal OpenTofu state bucket. It needs the hub cluster's OIDC provider.

```bash
cd landing-zone/components/aws/portal-hub
tofu apply \
  -var environment=production \
  -var oidc_provider_arn=<hub cluster OIDC provider ARN> \
  -var oidc_issuer=<hub cluster OIDC issuer URL> \
  -var state_bucket_name=<your portal-state bucket, globally unique> \
  -var namespace=portal -var service_account_name=portal-worker
tofu output            # hub_role_arn, state_bucket_name
```

`role_name`/`namespace`/`service_account_name` default to `portal-worker`/`portal`/
`portal-worker` — match them to the chart's release name + namespace (the chart
names the worker SA `<release>-worker`).

**In each workload (spoke) account — `portal-spoke`:** the per-account read role.

```bash
cd landing-zone/components/aws/portal-spoke
tofu apply \
  -var environment=production \
  -var portal_hub_role_arn=<hub_role_arn from above> \
  -var external_id=portal          # the default; whatever you choose, see §3
tofu output            # spoke_role_arn, external_id
```

> **MATCH-UP #1 — the ExternalId.** The spoke role's trust policy requires
> `sts:ExternalId == external_id`. Whatever you set here, you must enter the SAME
> value as the Account's **ExternalID** in portal (§3). Mismatch → every
> AssumeRole silently 403s, and clusters never connect.

## 2. Deploy portal on the hub

Build + push portal's images to a registry the hub can pull (or use your release
images), then install the chart with IRSA + the state bucket + the watchers on.
`values-hub.yaml`:

```yaml
config:
  environment: production          # real GitHub OAuth + non-default secrets
  # set jwtSecret, encryptionKey (32 bytes), githubClientID/Secret

image:                             # point at your pushed images
  server: { repository: <registry>/portal/server, tag: <tag> }
  worker: { repository: <registry>/portal/worker, tag: <tag> }
  web:    { repository: <registry>/portal/web,    tag: <tag> }
  migrate:{ repository: <registry>/portal/migrate,tag: <tag> }

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
    <clusters-repo WRITE deploy key — keep out of git>

clusterWatchback: { enabled: true, interval: "30s" }
clusterHealth:    { enabled: true, interval: "60s" }
```

```bash
helm dependency build deploy/helm/portal
helm install portal deploy/helm/portal -f values-hub.yaml
```

> Leave `objectStore.accessKey/secretKey` empty: portal's S3 client falls back to
> the SDK default chain (IRSA web-identity), so the worker reaches the bucket as
> `portal-hub`. The `portal-hub` policy already grants r/w on that bucket.

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
