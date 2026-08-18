package config_test

import (
	"strings"
	"testing"

	"github.com/caarlos0/env/v11"
	"github.com/nanohype/portal/internal/config"
)

// DefaultDatabaseURL is duplicated in the struct tag because Go struct tags must
// be literals. If the two drift, the non-development safety check stops matching
// the value it is meant to reject, and a production deploy that forgot
// DATABASE_URL boots against localhost instead of being refused.
func TestDefaultDatabaseURLMatchesTheTag(t *testing.T) {
	// Parse against an empty environment so the struct tag's default is what
	// lands, regardless of what the developer running the tests has exported.
	cfg := &config.Config{}
	if err := env.ParseWithOptions(cfg, env.Options{Environment: map[string]string{}}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.DatabaseURL != config.DefaultDatabaseURL {
		t.Errorf("struct tag default = %q, DefaultDatabaseURL = %q — they must be the same string",
			cfg.DatabaseURL, config.DefaultDatabaseURL)
	}
}

func TestValidateDatabase(t *testing.T) {
	t.Run("development tolerates the default", func(t *testing.T) {
		c := &config.Config{Environment: "development", DatabaseURL: config.DefaultDatabaseURL}
		if err := c.ValidateDatabase(); err != nil {
			t.Errorf("development should accept the default DATABASE_URL, got: %v", err)
		}
	})

	t.Run("production refuses the default", func(t *testing.T) {
		c := &config.Config{Environment: "production", DatabaseURL: config.DefaultDatabaseURL}
		err := c.ValidateDatabase()
		if err == nil {
			t.Fatal("production must refuse the development DATABASE_URL")
		}
		if !strings.Contains(err.Error(), "DATABASE_URL") {
			t.Errorf("error should name the variable, got: %v", err)
		}
	})

	t.Run("an unset ENVIRONMENT is treated as production", func(t *testing.T) {
		// Matches how Validate handles every other secret: anything that is not
		// exactly "development" fails closed.
		c := &config.Config{DatabaseURL: config.DefaultDatabaseURL}
		if err := c.ValidateDatabase(); err == nil {
			t.Error("an unset ENVIRONMENT must not be treated as development")
		}
	})

	t.Run("production accepts a real DATABASE_URL", func(t *testing.T) {
		c := &config.Config{Environment: "production", DatabaseURL: "postgres://u:p@db.internal:5432/portal"}
		if err := c.ValidateDatabase(); err != nil {
			t.Errorf("a configured DATABASE_URL should pass, got: %v", err)
		}
	})
}

func TestRedactedTarget(t *testing.T) {
	t.Run("drops credentials and query parameters", func(t *testing.T) {
		got := config.RedactedTarget("postgres://portal:hunter2@db.internal:5432/portal?sslmode=require")
		if got != "db.internal:5432/portal" {
			t.Errorf("RedactedTarget = %q, want db.internal:5432/portal", got)
		}
		if strings.Contains(got, "hunter2") || strings.Contains(got, "portal:") {
			t.Errorf("credentials leaked into %q", got)
		}
	})

	t.Run("names the host that makes the local footgun visible", func(t *testing.T) {
		got := config.RedactedTarget(config.DefaultDatabaseURL)
		if !strings.HasPrefix(got, "localhost:5432") {
			t.Errorf("RedactedTarget = %q, want it to name localhost:5432", got)
		}
	})

	t.Run("does not fail on a malformed URL", func(t *testing.T) {
		if got := config.RedactedTarget("://nonsense"); got != "unparseable target" {
			t.Errorf("RedactedTarget = %q, want the placeholder", got)
		}
	})
}
