package domain

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Config holds all application configuration, loaded from environment variables.
type Config struct {
	// Server
	ServerAddr      string        `env:"SERVER_ADDR" envDefault:":8080"`
	ServerBaseURL   string        `env:"SERVER_BASE_URL" envDefault:"http://localhost:8080"`
	WebURL          string        `env:"WEB_URL" envDefault:"http://localhost:5173"`
	Environment     string        `env:"ENVIRONMENT" envDefault:"development"`
	LogLevel        string        `env:"LOG_LEVEL" envDefault:"info"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`

	// Database
	DatabaseURL         string        `env:"DATABASE_URL" envDefault:"postgres://portal:portal@localhost:5432/portal?sslmode=disable"`
	DBMaxConns          int32         `env:"DB_MAX_CONNS" envDefault:"25"`
	DBMinConns          int32         `env:"DB_MIN_CONNS" envDefault:"5"`
	DBMaxConnIdleTime   time.Duration `env:"DB_MAX_CONN_IDLE_TIME" envDefault:"5m"`
	DBHealthCheckPeriod time.Duration `env:"DB_HEALTH_CHECK_PERIOD" envDefault:"30s"`

	// Redis
	RedisURL string `env:"REDIS_URL" envDefault:"redis://localhost:6379"`

	// S3/MinIO
	S3Endpoint  string `env:"S3_ENDPOINT" envDefault:"localhost:9000"`
	S3Bucket    string `env:"S3_BUCKET" envDefault:"portal"`
	S3AccessKey string `env:"S3_ACCESS_KEY" envDefault:"minioadmin"`
	S3SecretKey string `env:"S3_SECRET_KEY" envDefault:"minioadmin"`
	S3UseSSL    bool   `env:"S3_USE_SSL" envDefault:"false"`
	S3Region    string `env:"S3_REGION" envDefault:"us-east-1"`

	// GitHub OAuth
	GitHubClientID     string `env:"GITHUB_CLIENT_ID"`
	GitHubClientSecret string `env:"GITHUB_CLIENT_SECRET"`

	// JWT
	JWTSecret     string        `env:"JWT_SECRET" envDefault:"dev-secret-change-in-production"`
	JWTExpiration time.Duration `env:"JWT_EXPIRATION" envDefault:"24h"`

	// Encryption
	EncryptionKey string `env:"ENCRYPTION_KEY" envDefault:"dev-encryption-key-32bytes!!!!!!"` // Must be 32 bytes for AES-256

	// VCS Webhooks
	WebhookSecret string `env:"WEBHOOK_SECRET"`

	// Worker
	WorkerConcurrency int    `env:"WORKER_CONCURRENCY" envDefault:"10"`
	WorkerHealthAddr  string `env:"WORKER_HEALTH_ADDR" envDefault:":8081"`

	// Executor
	ExecutorType        string `env:"EXECUTOR_TYPE" envDefault:"local"` // "local" or "kubernetes"
	ExecutorNamespace   string `env:"EXECUTOR_NAMESPACE" envDefault:"portal"`
	ExecutorImage       string `env:"EXECUTOR_IMAGE" envDefault:"portal-executor:tofu-1.11"`
	ExecutorImagePrefix string `env:"EXECUTOR_IMAGE_PREFIX" envDefault:"portal-executor"`

	// GitOps (tenant write path). When TenantsRepoURL is empty the worker
	// surfaces "not configured" on any tenant_apply attempt — keeps dev
	// machines without SSH keys from blowing up on startup.
	GitCacheDir                   string `env:"GITOPS_CACHE_DIR" envDefault:"/tmp/portal/git"`
	TenantsRepoURL                string `env:"GITOPS_TENANTS_REPO_URL"`
	TenantsRepoRef                string `env:"GITOPS_TENANTS_REPO_REF" envDefault:"main"`
	GitSSHKeyPath                 string `env:"GITOPS_SSH_KEY_PATH"`
	GitAuthorName                 string `env:"GITOPS_AUTHOR_NAME" envDefault:"portal"`
	GitAuthorEmail                string `env:"GITOPS_AUTHOR_EMAIL" envDefault:"portal@local"`
	EksAgentPlatformChartsRepoURL string `env:"EKS_AGENT_PLATFORM_CHARTS_REPO_URL"`
	EksAgentPlatformChartsRepoRef string `env:"EKS_AGENT_PLATFORM_CHARTS_REPO_REF" envDefault:"main"`
}

// Validate checks that the configuration is safe for the target environment.
func (c *Config) Validate() error {
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
	return nil
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
