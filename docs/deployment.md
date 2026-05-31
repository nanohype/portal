# Deployment

## Docker Images

Build all images from the repo root:

```bash
task docker:build
```

This builds:
- `portal/server` — API server
- `portal/worker` — Job worker
- `portal/web` — nginx serving the React SPA
- `portal/migrate` — one-shot migration runner

Images use multi-stage Alpine builds and run as a non-root `portal` user.

If using the Kubernetes executor, also build the executor image:

```bash
docker build -f docker/Dockerfile.executor -t portal-executor:tofu-1.11 .
```

## Docker Compose (Single Server)

For simple deployments, use docker-compose with production overrides:

```yaml
services:
  server:
    image: portal/server:latest
    ports: ["8080:8080"]
    environment:
      DATABASE_URL: postgres://portal:${DB_PASSWORD}@postgres:5432/portal?sslmode=disable
      REDIS_URL: redis://redis:6379
      S3_ENDPOINT: minio:9000
      S3_ACCESS_KEY: ${S3_ACCESS_KEY}
      S3_SECRET_KEY: ${S3_SECRET_KEY}
      GITHUB_CLIENT_ID: ${GITHUB_CLIENT_ID}
      GITHUB_CLIENT_SECRET: ${GITHUB_CLIENT_SECRET}
      JWT_SECRET: ${JWT_SECRET}
      ENCRYPTION_KEY: ${ENCRYPTION_KEY}
      WEBHOOK_SECRET: ${WEBHOOK_SECRET}
      ENVIRONMENT: production
      SERVER_BASE_URL: https://portal.example.com
      WEB_URL: https://portal.example.com
    depends_on: [postgres, redis, minio]

  worker:
    image: portal/worker:latest
    environment:
      DATABASE_URL: postgres://portal:${DB_PASSWORD}@postgres:5432/portal?sslmode=disable
      REDIS_URL: redis://redis:6379
      S3_ENDPOINT: minio:9000
      S3_ACCESS_KEY: ${S3_ACCESS_KEY}
      S3_SECRET_KEY: ${S3_SECRET_KEY}
      ENCRYPTION_KEY: ${ENCRYPTION_KEY}
      EXECUTOR_TYPE: local
    depends_on: [postgres, redis, minio]

  web:
    image: portal/web:latest
    ports: ["443:443"]
    depends_on: [server]

  migrate:
    image: portal/migrate:latest
    environment:
      DATABASE_URL: postgres://portal:${DB_PASSWORD}@postgres:5432/portal?sslmode=disable
    command: ["-direction", "up"]
    depends_on: [postgres]

  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: portal
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: portal
    volumes: [pgdata:/var/lib/postgresql/data]

  redis:
    image: redis:7-alpine

  minio:
    image: minio/minio
    environment:
      MINIO_ROOT_USER: ${S3_ACCESS_KEY}
      MINIO_ROOT_PASSWORD: ${S3_SECRET_KEY}
    command: server /data
    volumes: [miniodata:/data]

volumes:
  pgdata:
  miniodata:
```

Put secrets in a `.env` file (not checked in):

```bash
DB_PASSWORD=<random>
S3_ACCESS_KEY=<random>
S3_SECRET_KEY=<random>
JWT_SECRET=<random-32-chars>
ENCRYPTION_KEY=<exactly-32-bytes>
WEBHOOK_SECRET=<random>
GITHUB_CLIENT_ID=<from-github>
GITHUB_CLIENT_SECRET=<from-github>
```

## Kubernetes (Helm)

### Install

```bash
# Add bitnami dependency charts
cd deploy/helm/portal
helm dependency build

# Install with production values
helm install portal . -f values-production.yaml
```

### Production Values

Create a `values-production.yaml`:

```yaml
config:
  environment: "production"
  serverBaseURL: "https://portal.example.com"
  webURL: "https://portal.example.com"
  jwtSecret: "<random-string>"
  encryptionKey: "<exactly-32-bytes>"
  githubClientID: "<from-github>"
  githubClientSecret: "<from-github>"
  webhookSecret: "<random-string>"
  executorType: "kubernetes"
  executorNamespace: "portal"
  logLevel: "info"

postgresql:
  auth:
    password: "<random>"

minio:
  auth:
    rootUser: "<random>"
    rootPassword: "<random>"

ingress:
  enabled: true
  className: nginx
  hosts:
    - host: portal.example.com
      paths:
        - path: /
          pathType: Prefix
```

### What the Chart Deploys

| Resource | Description |
|----------|-------------|
| **server Deployment** | API server with ConfigMap env vars + Secret for credentials |
| **worker Deployment** | Job worker with same config |
| **web Deployment** | nginx serving the SPA, reverse-proxying `/api` to the server |
| **migrate Job** | Runs migrations on install/upgrade |
| **ConfigMap** | Non-secret configuration |
| **Secret** | JWT secret, encryption key, GitHub creds, webhook secret, S3 creds |
| **Ingress** (optional) | nginx ingress for external access |
| **PostgreSQL** (subchart) | Bitnami PostgreSQL with persistence |
| **Redis** (subchart) | Bitnami Redis standalone |
| **MinIO** (subchart) | Bitnami MinIO with persistence |

### Using the Kubernetes Executor

When `executorType: kubernetes`, the worker creates ephemeral pods to run tofu instead of running it locally. This provides:

- Isolation between runs
- Resource limits per run (250m–1 CPU, 256Mi–1Gi memory)
- Per-workspace tofu versions via image tags

Requirements:
1. The worker pod needs a ServiceAccount with permissions to create/delete Pods and ConfigMaps in the executor namespace
2. Build executor images for each tofu version you need:
   ```bash
   # The executor image needs: tofu, git, sh
   docker build -f docker/Dockerfile.executor -t portal-executor:tofu-1.11 .
   ```
3. Set `EXECUTOR_IMAGE_PREFIX` to match your registry path (default: `portal-executor`)

The worker resolves the image as `{EXECUTOR_IMAGE_PREFIX}:tofu-{workspace.tofu_version}`.

### Upgrading

```bash
helm upgrade portal deploy/helm/portal -f values-production.yaml
```

The migration Job runs automatically on upgrade.

## Health Checks

| Endpoint | Port | Description |
|----------|------|-------------|
| `GET /api/v1/health` | 8080 | Server health — pings Postgres, returns 503 if degraded |
| `GET /healthz` | 8081 | Worker health — basic liveness check |

## Security Checklist

- [ ] `ENVIRONMENT` set to `production`
- [ ] `JWT_SECRET` is a unique random string
- [ ] `ENCRYPTION_KEY` is exactly 32 random bytes
- [ ] `WEBHOOK_SECRET` is set and matches GitHub webhook config
- [ ] S3 credentials are not the default `minioadmin`
- [ ] Database password is not the default `portal`
- [ ] HTTPS is terminated at the ingress/load balancer
- [ ] GitHub OAuth callback URL points to your production domain
- [ ] `WEB_URL` and `SERVER_BASE_URL` use your production domain
