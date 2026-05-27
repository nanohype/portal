# Configuration

All configuration is via environment variables. In development, every variable has a working default except the GitHub OAuth credentials.

## Local Dev

No environment variables are required for local development. When `ENVIRONMENT=development` (the default), a **Dev Login** button appears on the login page that creates a local user without GitHub OAuth.

To use GitHub sign-in locally, set `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET` from a [GitHub OAuth App](https://github.com/settings/developers) with:
- Homepage URL: `http://localhost:5173`
- Authorization callback URL: `http://localhost:8080/api/v1/auth/github/callback`

## Server

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_ADDR` | `:8080` | HTTP listen address |
| `SERVER_BASE_URL` | `http://localhost:8080` | Public URL of the API server (used for OAuth callbacks) |
| `WEB_URL` | `http://localhost:5173` | Public URL of the web frontend (used for CORS) |
| `ENVIRONMENT` | `development` | `development`, `staging`, or `production`. Controls config validation and CORS |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SHUTDOWN_TIMEOUT` | `15s` | Graceful shutdown timeout for in-progress requests |

## Database

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | `postgres://tofui:tofui@localhost:5432/tofui?sslmode=disable` | Postgres connection string |
| `DB_MAX_CONNS` | `25` | Maximum open connections |
| `DB_MIN_CONNS` | `5` | Minimum idle connections |
| `DB_MAX_CONN_IDLE_TIME` | `5m` | Close idle connections after this duration |
| `DB_HEALTH_CHECK_PERIOD` | `30s` | How often to ping idle connections |

## Redis

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_URL` | `redis://localhost:6379` | Redis connection string. Used for log streaming pub/sub. If empty or unavailable, falls back to in-memory streaming (single-server only) |

## S3 / MinIO

| Variable | Default | Description |
|----------|---------|-------------|
| `S3_ENDPOINT` | `localhost:9000` | S3-compatible endpoint |
| `S3_BUCKET` | `portal` | Bucket name for state, logs, plans, and config archives |
| `S3_ACCESS_KEY` | `minioadmin` | Access key |
| `S3_SECRET_KEY` | `minioadmin` | Secret key |
| `S3_USE_SSL` | `false` | Use HTTPS for S3 connections |
| `S3_REGION` | `us-east-1` | S3 region |

## Authentication

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_SECRET` | `dev-secret-...` | Signing key for JWT tokens. **Must be changed in production.** |
| `JWT_EXPIRATION` | `24h` | Token lifetime |

## Encryption

| Variable | Default | Description |
|----------|---------|-------------|
| `ENCRYPTION_KEY` | `dev-encryption-...` | AES-256 key for encrypting sensitive variable values. **Must be exactly 32 bytes.** Must be changed in production. |

## Webhooks

| Variable | Default | Description |
|----------|---------|-------------|
| `WEBHOOK_SECRET` | _(empty)_ | HMAC-SHA256 secret for verifying GitHub webhook signatures. Set this to the same value configured in your GitHub webhook settings. Required in non-dev environments. |

## Worker

| Variable | Default | Description |
|----------|---------|-------------|
| `WORKER_CONCURRENCY` | `10` | Maximum concurrent job executions |
| `WORKER_HEALTH_ADDR` | `:8081` | Health check endpoint address (`/healthz`) |

## Executor

| Variable | Default | Description |
|----------|---------|-------------|
| `EXECUTOR_TYPE` | `local` | `local` runs tofu on the worker host. `kubernetes` runs tofu in ephemeral pods. |
| `EXECUTOR_NAMESPACE` | `portal` | Kubernetes namespace for executor pods (K8s executor only) |
| `EXECUTOR_IMAGE` | `portal-executor:tofu-1.11` | Default container image for executor pods |
| `EXECUTOR_IMAGE_PREFIX` | `portal-executor` | Image name prefix. When a workspace specifies a tofu version, the pod uses `{prefix}:tofu-{version}` as the image tag. |

## Production Requirements

When `ENVIRONMENT` is not `development`, the server validates that the following are set to non-default values:

- `JWT_SECRET`
- `ENCRYPTION_KEY` (must be exactly 32 bytes)
- `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET`
- `WEBHOOK_SECRET`
- `S3_ACCESS_KEY` and `S3_SECRET_KEY` (must not be `minioadmin`)

The server will refuse to start if validation fails.
