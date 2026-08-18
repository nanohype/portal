package config

import (
	"fmt"
	"net/url"
)

// MigrateURL rewrites a Postgres connection string onto the scheme
// golang-migrate uses to select its database driver.
//
// golang-migrate picks a driver by URL scheme, and its pgx/v5 driver registers
// as "pgx5", while DATABASE_URL is spelled postgres:// for every other consumer
// in the app — pgxpool included. Rewriting here keeps DATABASE_URL the single
// connection string the whole system shares, rather than asking an operator to
// configure a second URL differing only by a scheme nothing else understands.
//
// It lives in config rather than beside either caller because both the migrate
// binary and the four integration harnesses need it, and a copy per caller is
// how the two halves drift.
func MigrateURL(databaseURL string) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse database URL: %w", err)
	}
	switch u.Scheme {
	case "postgres", "postgresql", "pgx5":
		u.Scheme = "pgx5"
	default:
		return "", fmt.Errorf("database URL scheme %q is not a postgres connection string", u.Scheme)
	}
	return u.String(), nil
}
