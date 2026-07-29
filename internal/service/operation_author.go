package service

import (
	"context"
	"strings"

	"github.com/nanohype/portal/internal/repository"
)

// authorLookup is the slice of Queries this needs, so the resolver can be
// exercised without a database.
type authorLookup interface {
	GetUser(ctx context.Context, id string) (repository.User, error)
}

// resolveOperationAuthor turns the operator's user id into the name and email a
// commit trailer needs, at the moment the operation is enqueued.
//
// Resolving here rather than when the commit is composed is the whole point.
// portal writes to the GitOps repos as a single deploy key, so the commit author
// is always portal and the only record of who asked is what we put in the
// message. A lookup at commit time reads the users table minutes or hours later,
// and returns nothing exactly when the account has since been renamed or removed
// — which is the case someone is reading the log for. Storing the identity on
// the operation row makes the record answer correctly after the account is gone.
//
// A failed lookup is not a failed operation. Refusing to vend a cluster because
// a users row could not be read would trade a real capability for a cosmetic
// one, so this returns nils and the apply worker omits the trailer. Saying
// nothing is honest; inventing an author is not.
func resolveOperationAuthor(ctx context.Context, q authorLookup, userID string) (name, email *string) {
	if q == nil || userID == "" {
		return nil, nil
	}
	user, err := q.GetUser(ctx, userID)
	if err != nil {
		return nil, nil
	}
	// `users.name` and `users.email` are NOT NULL but may be empty strings — a
	// GitHub account with no public name, for instance. A trailer needs both
	// halves to be parseable, so a missing half means no trailer at all rather
	// than `Co-authored-by:  <>`.
	n := strings.TrimSpace(user.Name)
	e := strings.TrimSpace(user.Email)
	if n == "" || e == "" {
		return nil, nil
	}
	return &n, &e
}
