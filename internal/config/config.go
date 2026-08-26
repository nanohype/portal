package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"
)

// Config holds all application configuration, loaded from environment variables.
type Config struct {
	// Server
	ServerAddr    string `env:"SERVER_ADDR" envDefault:":8080"`
	ServerBaseURL string `env:"SERVER_BASE_URL" envDefault:"http://localhost:8080"`
	WebURL        string `env:"WEB_URL" envDefault:"http://localhost:5173"`
	// No default: only an explicit ENVIRONMENT=development relaxes security
	// (dev login, default keys). Unset/anything else is treated as production by
	// Validate(), so a missing env var fails closed instead of silently booting
	// a prod instance with dev secrets. Dev tooling sets ENVIRONMENT=development.
	Environment     string        `env:"ENVIRONMENT"`
	LogLevel        string        `env:"LOG_LEVEL" envDefault:"info"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`
	// CIDRs of the proxies in front of this server, if any. Empty means the
	// connection's own address is the client address and X-Forwarded-For is
	// ignored — the rate limiter and the audit ledger both key on the result, so
	// trusting that header without knowing who set it hands the caller both.
	// Set this to the ranges your ingress speaks from; anything left of a hop
	// listed here was written before the request reached something portal trusts.
	TrustedProxyCIDRs []string `env:"TRUSTED_PROXY_CIDRS" envSeparator:","`

	// Database
	DatabaseURL         string        `env:"DATABASE_URL" envDefault:"postgres://portal:portal@localhost:5432/portal?sslmode=disable"`
	DBMaxConns          int32         `env:"DB_MAX_CONNS" envDefault:"25"`
	DBMinConns          int32         `env:"DB_MIN_CONNS" envDefault:"5"`
	DBMaxConnIdleTime   time.Duration `env:"DB_MAX_CONN_IDLE_TIME" envDefault:"5m"`
	DBHealthCheckPeriod time.Duration `env:"DB_HEALTH_CHECK_PERIOD" envDefault:"30s"`

	// Redis
	RedisURL string `env:"REDIS_URL" envDefault:"redis://localhost:6379"`

	// S3/MinIO
	//
	// The credentials carry NO default, and that is load-bearing rather than
	// tidiness. Empty here is what selects the AWS default credential chain —
	// Pod Identity in-cluster — because NewS3Storage only installs a static
	// provider when both are non-empty.
	//
	// A dev default made that branch unreachable. env applies envDefault when a
	// variable is SET BUT EMPTY, not only when it is absent, so the chart
	// passing `S3_ACCESS_KEY: ""` did not disable the default — it selected it.
	// Portal then signed real S3 requests with `minioadmin` and got
	// `InvalidAccessKeyId`, on a cluster where Pod Identity was configured,
	// working, and never consulted. The guard below rejects minioadmin outside
	// development, and this install is labelled development, so nothing caught
	// it.
	//
	// Local development supplies these explicitly in Taskfile.yaml's dev tasks,
	// matching docker-compose's minio root user. That is the right place for a
	// value that is only ever true on a laptop.
	S3Endpoint  string `env:"S3_ENDPOINT"`
	S3Bucket    string `env:"S3_BUCKET" envDefault:"portal"`
	S3AccessKey string `env:"S3_ACCESS_KEY"`
	S3SecretKey string `env:"S3_SECRET_KEY"`
	S3UseSSL    bool   `env:"S3_USE_SSL" envDefault:"false"`
	S3Region    string `env:"S3_REGION" envDefault:"us-east-1"`

	// GitHub OAuth. AllowedGitHubOrg restricts login to active members of that
	// GitHub organization — the callback verifies membership with the
	// already-requested read:org scope and rejects non-members. A GitHub OAuth
	// App authenticates every GitHub account, not just the ones you want, so this
	// is the only thing narrowing sign-in to people you trust. It is required
	// outside development (see Validate) and the auth handler refuses GitHub
	// sign-in without it rather than admitting everyone.
	GitHubClientID     string `env:"GITHUB_CLIENT_ID"`
	GitHubClientSecret string `env:"GITHUB_CLIENT_SECRET"`
	AllowedGitHubOrg   string `env:"ALLOWED_GITHUB_ORG"`

	// JWT
	JWTSecret     string        `env:"JWT_SECRET" envDefault:"dev-secret-change-in-production"`
	JWTExpiration time.Duration `env:"JWT_EXPIRATION" envDefault:"24h"`

	// Encryption
	EncryptionKey string `env:"ENCRYPTION_KEY" envDefault:"dev-encryption-key-32bytes!!!!!!"` // Must be 32 bytes for AES-256

	// VCS Webhooks
	WebhookSecret string `env:"WEBHOOK_SECRET"`

	// Worker. WorkerConcurrency bounds simultaneous tofu runs on the default
	// queue; WorkerReconcileConcurrency bounds the separate reconcile queue
	// (per-cluster watch jobs) so the two can be tuned independently and never
	// starve each other.
	WorkerConcurrency          int    `env:"WORKER_CONCURRENCY" envDefault:"10"`
	WorkerReconcileConcurrency int    `env:"WORKER_RECONCILE_CONCURRENCY" envDefault:"8"`
	WorkerHealthAddr           string `env:"WORKER_HEALTH_ADDR" envDefault:":8081"`

	// Observability. Metrics are always on (scraped at /metrics); distributed
	// tracing is opt-in. When enabled, the OTel SDK reads
	// OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_EXPORTER_OTLP_PROTOCOL from the
	// environment itself — these two fields only gate it on and set the
	// head-sampling fraction (a job inherits its enqueuing request's decision).
	TracingEnabled     bool    `env:"OTEL_TRACES_ENABLED" envDefault:"false"`
	TracingSampleRatio float64 `env:"OTEL_TRACES_SAMPLER_ARG" envDefault:"1"`

	// Executor
	ExecutorType        string `env:"EXECUTOR_TYPE" envDefault:"local"` // "local" or "kubernetes"
	ExecutorNamespace   string `env:"EXECUTOR_NAMESPACE" envDefault:"portal"`
	ExecutorImage       string `env:"EXECUTOR_IMAGE" envDefault:"portal-executor:tofu-1.11"`
	ExecutorImagePrefix string `env:"EXECUTOR_IMAGE_PREFIX" envDefault:"portal-executor"`

	// GitOps (tenant write path). When TenantsRepoURL is empty the worker
	// surfaces "not configured" on any tenant_apply attempt — keeps dev
	// machines without SSH keys from blowing up on startup.
	GitCacheDir    string `env:"GITOPS_CACHE_DIR" envDefault:"/tmp/portal/git"`
	TenantsRepoURL string `env:"GITOPS_TENANTS_REPO_URL"`
	TenantsRepoRef string `env:"GITOPS_TENANTS_REPO_REF" envDefault:"main"`
	GitSSHKeyPath  string `env:"GITOPS_SSH_KEY_PATH"`
	// GitSSHKnownHosts is required whenever GitSSHKeyPath is set. The worker
	// image carries no hosts file, so without it there is nothing to verify the
	// GitOps remote's host key against — and the deploy key being offered has
	// write access to every repo portal's write path pushes to.
	GitSSHKnownHosts              string `env:"GITOPS_SSH_KNOWN_HOSTS"`
	GitAuthorName                 string `env:"GITOPS_AUTHOR_NAME" envDefault:"portal"`
	GitAuthorEmail                string `env:"GITOPS_AUTHOR_EMAIL" envDefault:"portal@local"`
	EksAgentPlatformChartsRepoURL string `env:"EKS_AGENT_PLATFORM_CHARTS_REPO_URL"`
	EksAgentPlatformChartsRepoRef string `env:"EKS_AGENT_PLATFORM_CHARTS_REPO_REF" envDefault:"main"`

	// GitOps (cluster vend path). When ClustersRepoURL is empty the worker
	// surfaces "not configured" on any cluster_apply attempt. The Cluster CR is
	// templated directly (no chart), so unlike the tenant path this needs no
	// charts repo — just the clusters repo + the shared SSH key + author.
	ClustersRepoURL string `env:"GITOPS_CLUSTERS_REPO_URL"`
	ClustersRepoRef string `env:"GITOPS_CLUSTERS_REPO_REF" envDefault:"main"`

	// ClusterAllowedRegions restricts which AWS regions a cluster may be vended
	// into. Empty (the default) means any region, matching the Cluster XRD,
	// which documents spec.region as "any region is valid" and decouples it from
	// the state backend. This is deployment policy, not product behavior: an
	// estate whose accounts sit under a region-locking SCP sets it to that
	// region, an adopter sets their own or leaves it open.
	ClusterAllowedRegions []string `env:"CLUSTER_ALLOWED_REGIONS"`

	// FleetHubRoleArn is the hub's Crossplane role ARN (eks-fleet-crossplane).
	// On a cross-account vend (the Cluster sets vendRoleArn) the worker stamps it
	// onto spec.bootstrapAccessRoleArn so cluster-stack grants the hub a
	// cluster-admin EKS access entry and the bootstrap Workspace's get-token can
	// reach the spoke API. Empty = same-account only (no stamping).
	FleetHubRoleArn string `env:"FLEET_HUB_ROLE_ARN"`

	// ArgoCD cluster-registry sync (read path). When enabled, the worker reads
	// ArgoCD's cluster Secrets (in ArgoCDNamespace, via the pod's in-cluster
	// ServiceAccount) every ArgoCDSyncInterval and upserts the cluster inventory
	// — so a cluster registered with ArgoCD is onboarded with no manual portal
	// registration. Discovered clusters attach to the configured org + account,
	// attributed to the configured user. Inert unless all three IDs are set and
	// the worker runs in-cluster.
	ArgoCDClusterSync   bool          `env:"ARGOCD_CLUSTER_SYNC" envDefault:"false"`
	ArgoCDNamespace     string        `env:"ARGOCD_NAMESPACE" envDefault:"argocd"`
	ArgoCDSyncInterval  time.Duration `env:"ARGOCD_SYNC_INTERVAL" envDefault:"120s"`
	ArgoCDSyncOrgID     string        `env:"ARGOCD_SYNC_ORG_ID"`
	ArgoCDSyncAccountID string        `env:"ARGOCD_SYNC_ACCOUNT_ID"`
	ArgoCDSyncCreatedBy string        `env:"ARGOCD_SYNC_CREATED_BY"`

	// Cluster provision watch-back (the vend loop's closing leg). When enabled,
	// the worker — running in-cluster on the hub — reads each committed
	// provision op's eks-fleet Cluster XR every ClusterWatchbackInterval, and
	// once the EKS endpoint + CA are up, auto-registers the new cluster as
	// eks_iam and flips the op to 'active'. Inert unless enabled and in-cluster.
	ClusterWatchback         bool          `env:"CLUSTER_WATCHBACK_ENABLED" envDefault:"false"`
	ClusterWatchbackInterval time.Duration `env:"CLUSTER_WATCHBACK_INTERVAL" envDefault:"60s"`

	// Cluster health watcher (steady-state per-cluster health). When enabled, the
	// worker — running in-cluster on the hub — reads each registered cluster's
	// per-cluster ArgoCD Application (sync+health) and, for eks_iam clusters, its
	// EKS control plane via eks:DescribeCluster, every ClusterHealthInterval, and
	// projects them onto the cluster row. ArgoCDNamespace is the hub namespace the
	// per-cluster Applications live in. Inert unless enabled and in-cluster.
	ClusterHealth         bool          `env:"CLUSTER_HEALTH_ENABLED" envDefault:"false"`
	ClusterHealthInterval time.Duration `env:"CLUSTER_HEALTH_INTERVAL" envDefault:"120s"`
}

// DefaultDatabaseURL is the development DATABASE_URL. It is duplicated in the
// struct tag above because Go struct tags must be literals; TestDefaultDatabaseURLMatchesTheTag
// fails if the two drift.
const DefaultDatabaseURL = "postgres://portal:portal@localhost:5432/portal?sslmode=disable"

// ValidateDatabase checks the database configuration is safe for the target
// environment.
//
// Separate from Validate because cmd/migrate needs the database half without the
// auth/webhook/object-store half: a migration Job is handed a DATABASE_URL and
// nothing else, and refusing to run it over a missing GITHUB_CLIENT_ID would
// block migrating a perfectly good production database.
func (c *Config) ValidateDatabase() error {
	if c.Environment != "development" && c.DatabaseURL == DefaultDatabaseURL {
		return fmt.Errorf("DATABASE_URL must be set in non-development environments: the default points at a local development database")
	}
	return nil
}

// Validate checks that the configuration is safe for the target environment.
func (c *Config) Validate() error {
	// The local executor shells out to tofu or terragrunt, and the worker image
	// ships neither — it is `alpine` plus ca-certificates and git, because the
	// binaries live in the executor image the Kubernetes executor schedules.
	// Selecting it outside development therefore produces a worker that starts
	// clean, accepts runs, and fails every one of them at `init` with
	// "executable file not found in $PATH".
	//
	// It is also what an unrecognised value becomes: cmd/worker's switch falls
	// through to local, so a typo picks the executor that cannot run rather than
	// failing on the typo.
	if c.Environment != "development" {
		switch c.ExecutorType {
		case "kubernetes":
		case "local":
			return fmt.Errorf("EXECUTOR_TYPE=local is development-only: the worker image ships no tofu or terragrunt binary, so every run would fail at init. Use EXECUTOR_TYPE=kubernetes, which schedules the executor image that carries them")
		case "":
			// env.Parse fills this from envDefault, so empty means the Config was
			// built in code rather than read from the environment. Refused for
			// the same reason as "local": that is what an empty value resolves to.
			return fmt.Errorf("EXECUTOR_TYPE is empty; outside development it must be %q (an empty value resolves to the local executor, which the worker image cannot run)", "kubernetes")
		default:
			return fmt.Errorf("EXECUTOR_TYPE is %q, which is neither \"kubernetes\" nor \"local\" — an unrecognised value selects the local executor, so this would silently become the one that cannot run", c.ExecutorType)
		}
	}

	// Checked here rather than at the middleware, which panics on a bad prefix.
	// A typo in a deployment variable should name itself at startup, not arrive
	// as a stack trace.
	for _, cidr := range c.TrustedProxyCIDRs {
		if _, err := netip.ParsePrefix(strings.TrimSpace(cidr)); err != nil {
			return fmt.Errorf("TRUSTED_PROXY_CIDRS entry %q is not a CIDR block (want e.g. 10.0.0.0/8): %w", cidr, err)
		}
	}
	if c.Environment != "development" {
		if c.JWTSecret == "dev-secret-change-in-production" {
			return fmt.Errorf("JWT_SECRET must be set in non-development environments")
		}
		if c.EncryptionKey == "dev-encryption-key-32bytes!!!!!!" {
			return fmt.Errorf("ENCRYPTION_KEY must be set in non-development environments")
		}
		if c.GitHubClientID == "" || c.GitHubClientSecret == "" {
			return fmt.Errorf("GITHUB_CLIENT_ID and GITHUB_CLIENT_SECRET must be set in non-development environments")
		}
		// A GitHub OAuth App admits every GitHub account that completes the flow.
		// Without an org to check membership against there is no boundary at all,
		// so refuse to start rather than come up admitting the internet.
		if c.AllowedGitHubOrg == "" {
			return fmt.Errorf("ALLOWED_GITHUB_ORG must be set in non-development environments: GitHub OAuth admits any GitHub account, and this is what restricts sign-in to members of your organization")
		}
		if c.WebhookSecret == "" {
			return fmt.Errorf("WEBHOOK_SECRET must be set in non-development environments")
		}
		if c.S3AccessKey == "minioadmin" || c.S3SecretKey == "minioadmin" {
			return fmt.Errorf("S3_ACCESS_KEY and S3_SECRET_KEY must not use default values in non-development environments")
		}
	}
	if c.EncryptionKey != "" && c.EncryptionKey != "dev-encryption-key-32bytes!!!!!!" && len(c.EncryptionKey) != 32 {
		return fmt.Errorf("ENCRYPTION_KEY must be exactly 32 bytes, got %d", len(c.EncryptionKey))
	}
	return c.ValidateDatabase()
}

// SlogLevel returns the slog.Level corresponding to the configured log level.
func (c *Config) SlogLevel() slog.Level {
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
