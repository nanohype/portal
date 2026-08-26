package config

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/caarlos0/env/v11"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "dev env with defaults passes",
			cfg: Config{
				Environment:   "development",
				JWTSecret:     "dev-secret-change-in-production",
				EncryptionKey: "dev-encryption-key-32bytes!!!!!!",
			},
			wantErr: false,
		},
		{
			name: "prod env with default JWT fails",
			cfg: Config{
				Environment:        "production",
				JWTSecret:          "dev-secret-change-in-production",
				EncryptionKey:      "prod-key-exactly-32-bytesXXXXXXX",
				GitHubClientID:     "id",
				GitHubClientSecret: "secret",
			},
			wantErr: true,
		},
		{
			name: "prod env with default encryption key fails",
			cfg: Config{
				Environment:        "production",
				JWTSecret:          "prod-secret",
				EncryptionKey:      "dev-encryption-key-32bytes!!!!!!",
				GitHubClientID:     "id",
				GitHubClientSecret: "secret",
			},
			wantErr: true,
		},
		{
			name: "prod env with missing GitHub creds fails",
			cfg: Config{
				Environment:   "production",
				JWTSecret:     "prod-secret",
				EncryptionKey: "prod-key-exactly-32-bytesXXXXXXX",
			},
			wantErr: true,
		},
		{
			name: "prod env with missing webhook secret fails",
			cfg: Config{
				Environment:        "production",
				JWTSecret:          "prod-secret",
				EncryptionKey:      "prod-key-exactly-32-bytesXXXXXXX",
				GitHubClientID:     "id",
				GitHubClientSecret: "secret",
				S3AccessKey:        "prod-key",
				S3SecretKey:        "prod-secret-key",
			},
			wantErr: true,
		},
		{
			name: "prod env with default S3 creds fails",
			cfg: Config{
				Environment:        "production",
				JWTSecret:          "prod-secret",
				EncryptionKey:      "prod-key-exactly-32-bytesXXXXXXX",
				GitHubClientID:     "id",
				GitHubClientSecret: "secret",
				WebhookSecret:      "whsec",
				S3AccessKey:        "minioadmin",
				S3SecretKey:        "minioadmin",
			},
			wantErr: true,
		},
		{
			// GitHub OAuth authenticates every GitHub account, so an unset
			// ALLOWED_GITHUB_ORG leaves nothing restricting who may sign in.
			// Startup fails instead of coming up open.
			name: "prod env with missing allowed GitHub org fails",
			cfg: Config{
				Environment:        "production",
				JWTSecret:          "prod-secret",
				EncryptionKey:      "prod-key-exactly-32-bytesXXXXXXX",
				GitHubClientID:     "id",
				GitHubClientSecret: "secret",
				WebhookSecret:      "whsec",
				S3AccessKey:        "prod-key",
				S3SecretKey:        "prod-secret-key",
			},
			wantErr: true,
		},
		{
			name: "dev env without allowed GitHub org passes",
			cfg: Config{
				Environment:   "development",
				JWTSecret:     "dev-secret-change-in-production",
				EncryptionKey: "dev-encryption-key-32bytes!!!!!!",
			},
			wantErr: false,
		},
		{
			name: "prod env with all set passes",
			cfg: Config{
				Environment:        "production",
				JWTSecret:          "prod-secret",
				EncryptionKey:      "prod-key-exactly-32-bytesXXXXXXX",
				GitHubClientID:     "id",
				GitHubClientSecret: "secret",
				AllowedGitHubOrg:   "nanohype",
				WebhookSecret:      "whsec",
				S3AccessKey:        "prod-key",
				S3SecretKey:        "prod-secret-key",
			},
			wantErr: false,
		},
		{
			name: "custom encryption key wrong length fails",
			cfg: Config{
				Environment:   "development",
				JWTSecret:     "dev-secret-change-in-production",
				EncryptionKey: "short",
			},
			wantErr: true,
		},
		{
			name: "custom encryption key exactly 32 bytes passes",
			cfg: Config{
				Environment:   "development",
				JWTSecret:     "dev-secret-change-in-production",
				EncryptionKey: "abcdefghijklmnopqrstuvwxyz123456",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfigSlogLevel(t *testing.T) {
	tests := []struct {
		level string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			cfg := Config{LogLevel: tt.level}
			got := cfg.SlogLevel()
			if got != tt.want {
				t.Errorf("SlogLevel() = %v, want %v", got, tt.want)
			}
		})
	}
}

// An empty S3 credential must stay empty, because empty is what selects Pod
// Identity.
//
// This pins a trap rather than a preference. `env` applies envDefault when a
// variable is SET BUT EMPTY, not only when it is absent — so a dev default on
// these fields cannot be switched off by passing "". The chart passed
// `S3_ACCESS_KEY: ""` intending "no static credentials, use the instance role",
// got `minioadmin` back, and portal signed real S3 requests with it. AWS
// answered InvalidAccessKeyId on a cluster where Pod Identity was configured,
// working, and never consulted.
//
// NewS3Storage installs a static credential provider only when BOTH are
// non-empty, so these two fields are the switch between "static keys" and "the
// AWS default chain". A default on them removes the second option entirely.
func TestConfig_EmptyS3CredentialsSelectTheAWSChain(t *testing.T) {
	for _, key := range []string{"S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_ENDPOINT"} {
		t.Setenv(key, "")
	}
	var c Config
	if err := env.Parse(&c); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.S3AccessKey != "" {
		t.Errorf("S3_ACCESS_KEY=\"\" became %q — a default here makes Pod Identity unreachable, "+
			"because a static provider is installed whenever both credentials are non-empty", c.S3AccessKey)
	}
	if c.S3SecretKey != "" {
		t.Errorf("S3_SECRET_KEY=\"\" became %q — same reason", c.S3SecretKey)
	}
	// The endpoint shares the hazard: a localhost default would send an
	// in-cluster pod at its own loopback instead of skipping S3 entirely.
	if c.S3Endpoint != "" {
		t.Errorf("S3_ENDPOINT=\"\" became %q — a deployment that declares no object store "+
			"must skip S3, not dial localhost", c.S3Endpoint)
	}
}

// And an unset variable behaves the same as an empty one, so a chart that omits
// the key and a chart that blanks it agree.
func TestConfig_UnsetS3CredentialsAlsoSelectTheAWSChain(t *testing.T) {
	var c Config
	if err := env.Parse(&c); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.S3AccessKey != "" || c.S3SecretKey != "" || c.S3Endpoint != "" {
		t.Errorf("unset S3 config should be empty, got endpoint=%q access=%q",
			c.S3Endpoint, c.S3AccessKey)
	}
}

// A typo in TRUSTED_PROXY_CIDRS decides whether X-Forwarded-For is believed, so
// it has to name itself at startup. The middleware panics on a bad prefix;
// Validate turns that into a message that says which entry and what was wanted.
func TestValidateRejectsAMalformedTrustedProxyCIDR(t *testing.T) {
	for _, bad := range []string{"10.0.0.0", "not-a-cidr", "10.0.0.0/33", ""} {
		c := &Config{Environment: "development", TrustedProxyCIDRs: []string{bad}}
		err := c.Validate()
		if err == nil {
			t.Errorf("TRUSTED_PROXY_CIDRS=%q was accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "TRUSTED_PROXY_CIDRS") {
			t.Errorf("error for %q does not name the variable: %v", bad, err)
		}
	}
}

func TestValidateAcceptsTrustedProxyCIDRs(t *testing.T) {
	c := &Config{Environment: "development", TrustedProxyCIDRs: []string{"10.0.0.0/8", " 192.168.0.0/16 ", "2001:db8::/32"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid CIDRs rejected: %v", err)
	}
}
