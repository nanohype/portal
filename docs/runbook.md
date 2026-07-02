# Runbook

On-call guide for a deployed portal: what to watch, what the alerts mean, and the
playbook per failure mode. Deploy procedures live in
[deploy-on-hub.md](deploy-on-hub.md) / [in-cluster-on-kx.md](in-cluster-on-kx.md);
the env-var reference is [configuration.md](configuration.md).

## At a glance

The chart runs three deployments plus a pre-install/pre-upgrade migrate Job:

| Workload | What it does | Health |
|----------|--------------|--------|
| `portal-server` | HTTP API + log streaming | `/healthz` (liveness, process-only), `/readyz` (pings Postgres), `/metrics` |
| `portal-worker` | River jobs (tofu runs, cluster vend/watch) + watcher loops | same three endpoints on its own port |
| `portal-web` | nginx serving the SPA | nginx default |
| `portal-migrate` | schema migrations, runs to completion before the app rolls | Job status |

External dependencies: **Postgres** (source of truth + River queue), **S3**
(tofu state, run logs, plans, config archives), **Redis** (log-streaming pub/sub —
optional; absent Redis falls back to in-memory, single-replica only), the
**clusters GitOps repo** over SSH (worker pushes Cluster CRs, hub ArgoCD pulls
them), and the hub **Kubernetes API** (watchback + health loops).

`/healthz` is deliberately process-only: a Postgres outage makes pods **unready**
(`/readyz` fails, they leave the Service) but must not restart-storm them. If
pods are restarting during a DB outage, something else is wrong.

## Metrics

Everything is under the `portal_` namespace, scraped pod-direct from `/metrics`
by the Grafana Agent into the hub's managed Prometheus; dashboards live in the
hub Grafana. The signals that matter:

| Metric | Read it as |
|--------|-----------|
| `portal_http_request_duration_seconds{method,route,status}` | RED for the API — rate/errors/latency by route |
| `portal_tofu_run_duration_seconds{operation,status}` | is infra execution healthy, and how long plans/applies run |
| `portal_worker_job_errors_total{kind,event}` | River job errors and panics by job kind (`run`, `cluster_apply`, `cluster_watch`, `cluster_connection_test`, `cluster_unwedge`, `tenant_apply`, `pipeline_stage`) |
| `portal_worker_jobs{state}` | queue depth + backlog, sampled every 15s; watch `available` (backlog), `retryable` (failing), `discarded` (gave up) |
| `portal_watcher_last_tick_timestamp_seconds{loop}` | per-loop heartbeat; a frozen value = a stalled loop |
| `portal_watcher_panics_total{loop}` | a non-zero rate means a loop is wedging and being revived |

Watcher loops (`loop` label): `job-stats` (15s), `slot-reaper` (5m),
`watch-dispatch` (60s), `argocd-sync` (`ARGOCD_SYNC_INTERVAL`, default 120s),
`watchback` (`CLUSTER_WATCHBACK_INTERVAL`, default 60s), `cluster-health`
(`CLUSTER_HEALTH_INTERVAL`).

## Alert conditions → response

| Condition | Suggested threshold | Response |
|-----------|--------------------:|----------|
| `time() - portal_watcher_last_tick_timestamp_seconds > 5 * interval` | per loop | [Stalled watcher loop](#stalled-watcher-loop) |
| `rate(portal_worker_job_errors_total[10m]) > 0` sustained | 10m | [Failing jobs](#failing-or-discarded-jobs) |
| `portal_worker_jobs{state="discarded"}` increasing | any | [Failing jobs](#failing-or-discarded-jobs) — retries are exhausted |
| `portal_worker_jobs{state="available"}` climbing, `running` flat | 10m | worker under-concurrency or wedged — see [Worker not picking up jobs](#worker-not-picking-up-jobs) |
| 5xx rate on `portal_http_request_duration_seconds` | route-dependent | [API errors](#api-5xxs) |
| `/readyz` failing on both server and worker | 2m | [Postgres outage](#postgres-outage) |

## Failure modes

### Provision stuck or "expired"

A cluster order commits a Cluster CR to the clusters repo; the hub's ArgoCD
applies it; Crossplane/eks-fleet vends. The chase order when an order isn't
progressing:

1. **Did the CR reach the hub?** `kubectl get clusters.<eks-fleet group>` on the
   hub. If absent → ArgoCD isn't syncing the clusters repo (check the appset +
   the read deploy key) or the push failed (worker logs, `git-ssh-secret`).
2. **Is the vend itself failing?** The real error lives in the
   provider-opentofu **Workspace** conditions on the hub — tofu errors surface
   in `conditions[].message` there, not in ArgoCD and not on the XR's Ready
   flag. The portal cluster detail view surfaces this via watchback; go to the
   Workspace directly when you need the full text.
3. **Reaped after 2h?** The watchback loop expires a committed provision whose
   CR never appeared on the hub within 2 hours (`provisionReapAfter`), with the
   reason recorded on the operation. That reap means step 1 failed the whole
   window: fix the sync path, then re-order. A slow-but-live vend is never
   reaped — only a CR that never applied.

Never restart the provider-opentofu pod mid-apply to "unstick" a vend — that
orphans live AWS resources against empty state. Use the portal's unwedge flow
(`cluster_unwedge` job) for a vend stuck in `external-create-pending`.

### Stalled watcher loop

Frozen `portal_watcher_last_tick_timestamp_seconds{loop=...}`. Each tick is
panic-recovered (`portal_watcher_panics_total` climbing = the loop body is
throwing every pass; read the worker logs for the panic line, `loop` is in the
log fields). A truly frozen loop with no panics means the tick is blocked on
I/O — usually the hub API or Postgres. One worker restart is safe: loops run
once immediately on start; in-flight jobs get `SHUTDOWN_TIMEOUT` (default 15s —
raise it if long applies are routinely in flight) to drain.

### Failing or discarded jobs

`portal_worker_job_errors_total{kind,event}` tells you which job type; worker
logs carry `kind`, `job_id`, `attempt`, `max_attempts`, and the error. Jobs run
with `MaxAttempts: 5` and a 2h per-job timeout — tofu errors are terminal by
design (retries are for infra flakes), so a run that failed on a real plan/apply
error lands in the run's own logs and status, not in endless retries.
`discarded` state = all five attempts burned; fix the cause, then re-trigger
from the UI (jobs are not resurrected automatically).

### Worker not picking up jobs

`available` climbing while `running` sits at zero: check the worker is alive
and ready (`/readyz` — it needs Postgres; River polls the DB), then check
`WORKER_CONCURRENCY` (default 10, bounds simultaneous tofu runs) against what's
queued. All job state is in Postgres — killing a wedged worker pod loses
nothing; retries resume on the replacement.

### Postgres outage

Both processes go unready and drop from their Services; nothing restarts (by
design — see above). River jobs, run history, and the queue are all in
Postgres, so the system is paused, not corrupted. When the DB returns, pods go
ready on the next `/readyz` poll with no intervention. If pods ARE
crash-looping, the failure is in startup config (bad `DATABASE_URL`, failed
migration) — read the migrate Job and pod logs, not the DB.

### Log streaming broken, runs fine otherwise

Live log tails ride Redis pub/sub. If `REDIS_URL` is down or unset the streamer
falls back to in-memory, which only works single-replica — with multiple server
replicas behind the Service, tails will randomly miss lines. Logs themselves
are not lost: the full run log persists to S3 and is served after the run.
Restore Redis (or scale server to one replica as a stopgap).

### S3 errors on runs

State/log/plan persistence fails → runs error at the persist step. On the hub
the worker uses IRSA (no static keys): check the ServiceAccount role annotation
and its policy against the bucket. With static keys (dev/kx), check
`S3_ENDPOINT`/`S3_ACCESS_KEY`. tofu **state** lives here — treat bucket
misconfiguration as a stop-the-line event; never point two portal installs at
the same bucket prefix.

### Webhook ingest returning 503

Fail-closed by design: the GitHub webhook handler refuses everything with 503
when `WEBHOOK_SECRET` is unset, and 401s bad HMACs. 503s = the secret didn't
reach the pod (chart secret / env wiring); 401s = the secret differs from what
the GitHub webhook config sends.

## Routine ops

- **Deploy/upgrade** — helm upgrade; migrations run in the pre-upgrade Job
  before pods roll. A failed migrate Job halts the rollout with the old
  version still serving; fix and re-upgrade (migrations are paired up/down).
- **Restart safety** — server is stateless; worker drains in-flight jobs for
  `SHUTDOWN_TIMEOUT` then stops, and River's Postgres-backed state means
  anything cut off retries on the next worker. Prefer restarting between big
  applies anyway.
- **Scaling** — server scales horizontally (log streaming needs Redis, see
  above). Worker replicas each add `WORKER_CONCURRENCY` tofu-run slots; scale
  deliberately, applies are not free.

## Escalation

Portal is an observer/orchestrator: it never holds the only copy of
infrastructure truth. Cluster state is in the tofu state bucket + the hub's
Crossplane resources; when portal is down, vends in flight continue on the hub
and watchback catches the system up when portal returns. The severe pages are
Postgres loss (restore from backup; run history and queue live there) and the
state bucket (versioned; treat with S3-recovery care, not portal care).
