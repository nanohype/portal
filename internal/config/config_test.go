package config

import (
	"log/slog"
	"testing"
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
