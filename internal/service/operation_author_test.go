package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/nanohype/portal/internal/repository"
)

type fakeUsers struct {
	user repository.User
	err  error
	// calls counts lookups so the "resolved once, at enqueue" property is
	// observable rather than assumed.
	calls int
}

func (f *fakeUsers) GetUser(_ context.Context, _ string) (repository.User, error) {
	f.calls++
	return f.user, f.err
}

func TestResolveOperationAuthor(t *testing.T) {
	ctx := context.Background()

	t.Run("records the identity as it is at enqueue", func(t *testing.T) {
		q := &fakeUsers{user: repository.User{Name: "Ada Lovelace", Email: "ada@example.com"}}
		name, email := resolveOperationAuthor(ctx, q, "01JUSER")
		if name == nil || *name != "Ada Lovelace" {
			t.Errorf("name = %v, want Ada Lovelace", name)
		}
		if email == nil || *email != "ada@example.com" {
			t.Errorf("email = %v, want ada@example.com", email)
		}
		if q.calls != 1 {
			t.Errorf("looked up the user %d times, want exactly 1", q.calls)
		}
	})

	// The reason the identity is stored on the row at all. A lookup deferred to
	// commit time would return ErrNoRows here and lose the attribution for
	// exactly the operation an auditor is trying to trace.
	t.Run("a departed user degrades to no attribution, not a failed enqueue", func(t *testing.T) {
		q := &fakeUsers{err: pgx.ErrNoRows}
		name, email := resolveOperationAuthor(ctx, q, "01JGONE")
		if name != nil || email != nil {
			t.Errorf("expected no identity, got %v / %v", name, email)
		}
	})

	t.Run("any lookup error degrades the same way", func(t *testing.T) {
		q := &fakeUsers{err: errors.New("connection reset")}
		if name, email := resolveOperationAuthor(ctx, q, "01JUSER"); name != nil || email != nil {
			t.Errorf("expected no identity, got %v / %v", name, email)
		}
	})

	// Both halves are NOT NULL in the schema but may be empty — a GitHub account
	// with no public name. A trailer needs both to be parseable.
	t.Run("an empty name yields no identity rather than half of one", func(t *testing.T) {
		q := &fakeUsers{user: repository.User{Name: "", Email: "ada@example.com"}}
		if name, email := resolveOperationAuthor(ctx, q, "01JUSER"); name != nil || email != nil {
			t.Errorf("expected no identity, got %v / %v", name, email)
		}
	})

	t.Run("an empty email yields no identity", func(t *testing.T) {
		q := &fakeUsers{user: repository.User{Name: "Ada Lovelace", Email: "   "}}
		if name, email := resolveOperationAuthor(ctx, q, "01JUSER"); name != nil || email != nil {
			t.Errorf("expected no identity, got %v / %v", name, email)
		}
	})

	t.Run("no user id means no lookup at all", func(t *testing.T) {
		q := &fakeUsers{user: repository.User{Name: "Ada", Email: "ada@example.com"}}
		if name, email := resolveOperationAuthor(ctx, q, ""); name != nil || email != nil {
			t.Errorf("expected no identity, got %v / %v", name, email)
		}
		if q.calls != 0 {
			t.Errorf("looked up a user for an empty id (%d calls)", q.calls)
		}
	})

	t.Run("a nil lookup is survivable", func(t *testing.T) {
		if name, email := resolveOperationAuthor(ctx, nil, "01JUSER"); name != nil || email != nil {
			t.Errorf("expected no identity, got %v / %v", name, email)
		}
	})
}
