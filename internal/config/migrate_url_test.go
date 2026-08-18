package config_test

import (
	"strings"
	"testing"

	"github.com/nanohype/portal/internal/config"
)

// golang-migrate picks its driver by URL scheme, so this rewrite is the only
// thing standing between DATABASE_URL and migrate silently selecting a driver
// that is not installed.
func TestMigrateURL(t *testing.T) {
	t.Run("rewrites the schemes DATABASE_URL is spelled with", func(t *testing.T) {
		for _, in := range []string{
			"postgres://portal:portal@localhost:5432/portal?sslmode=disable",
			"postgresql://portal:portal@localhost:5432/portal?sslmode=disable",
			"pgx5://portal:portal@localhost:5432/portal?sslmode=disable",
		} {
			got, err := config.MigrateURL(in)
			if err != nil {
				t.Fatalf("MigrateURL(%q): %v", in, err)
			}
			if !strings.HasPrefix(got, "pgx5://") {
				t.Errorf("MigrateURL(%q) = %q, want a pgx5:// scheme", in, got)
			}
		}
	})

	t.Run("preserves everything except the scheme", func(t *testing.T) {
		got, err := config.MigrateURL("postgres://u:p@host:5432/db?sslmode=require&application_name=portal")
		if err != nil {
			t.Fatalf("MigrateURL: %v", err)
		}
		want := "pgx5://u:p@host:5432/db?sslmode=require&application_name=portal"
		if got != want {
			t.Errorf("MigrateURL = %q, want %q", got, want)
		}
	})

	t.Run("refuses a non-postgres scheme instead of passing it through", func(t *testing.T) {
		// Passing it through would hand golang-migrate a driver it has no
		// registration for, and the failure would read as a migrate bug.
		if _, err := config.MigrateURL("mysql://u:p@host:3306/db"); err == nil {
			t.Fatal("a mysql:// URL must be refused")
		}
	})

	t.Run("reports a malformed URL", func(t *testing.T) {
		if _, err := config.MigrateURL("://not a url"); err == nil {
			t.Fatal("a malformed URL must be refused")
		}
	})
}
