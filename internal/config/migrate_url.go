package config

import (
	"errors"
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
		return "", fmt.Errorf("DATABASE_URL is not a valid connection string: %w", parseReason(err))
	}
	switch u.Scheme {
	case "postgres", "postgresql", "pgx5":
		u.Scheme = "pgx5"
	default:
		return "", fmt.Errorf("database URL scheme %q is not a postgres connection string", u.Scheme)
	}
	return u.String(), nil
}

// parseReason is the reason a connection string would not parse, with the
// connection string itself removed.
//
// net/url reports a failure as *url.Error, which carries the URL it was given
// and prints it. A DATABASE_URL contains the database password, so wrapping one
// of those puts the password in the message of every caller that logs the error
// — and a caller logging an error it was handed is doing the ordinary thing.
// Dropping the field that holds the value is what keeps the DSN out of every
// message derived from it, rather than each caller filtering the sentence it
// ends up with.
//
// url.Error.Err is the reason alone: "missing protocol scheme", "invalid URL
// escape". That is what an operator needs and it names no credential.
func parseReason(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
