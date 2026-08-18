package config

import "net/url"

// RedactedTarget renders a connection string as host:port/database, dropping the
// credentials and every query parameter.
//
// It exists so a binary can say which database it is about to write to. The
// development DATABASE_URL points at localhost:5432, and on a machine running
// more than one project that is whichever Postgres happens to hold the port —
// so the interesting failure is not a bad password, it is a successful
// connection to the wrong database. Naming the target turns that from silent
// into obvious.
//
// Returns "unparseable target" rather than an error: this is used on a log line
// beside an operation that is about to fail on its own if the URL is malformed,
// and a redaction helper that can fail is a redaction helper someone skips.
func RedactedTarget(databaseURL string) string {
	u, err := url.Parse(databaseURL)
	if err != nil || u.Host == "" {
		return "unparseable target"
	}
	return u.Host + u.Path
}
