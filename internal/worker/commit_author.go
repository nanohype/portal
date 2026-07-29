package worker

import (
	"fmt"
	"strings"
)

// attributionTrailer renders the `Co-authored-by:` trailer for a GitOps commit,
// or the empty string when the operation carries no resolved identity.
//
// portal authenticates to the GitOps repos as one deploy key, so every commit it
// writes has the same author. Naming the operator in prose put the information in
// the message but left it unreadable by anything — `git log --format` cannot
// select on a sentence. A trailer is the git-native place for a second identity,
// which makes `git log --grep '^Co-authored-by'`, `git shortlog` and every forge's
// attribution UI work on portal's commits the way they work on a human's.
//
// The identity comes off the operation row, resolved when the operation was
// enqueued. Absent means absent: a row written before that column existed, or an
// enqueue whose user lookup failed, produces no trailer rather than a malformed
// one. A parser reading `Co-authored-by:  <>` would be worse off than one reading
// nothing.
func attributionTrailer(name, email *string) string {
	if name == nil || email == nil {
		return ""
	}
	n := strings.TrimSpace(*name)
	e := strings.TrimSpace(*email)
	if n == "" || e == "" {
		return ""
	}
	// A newline or an angle bracket in either half would split the trailer or
	// forge a second one. Both come from a users row rather than from the request,
	// so this is a belt-and-braces check on data that should already be clean —
	// but a commit message is a structured record and one bad byte makes it lie.
	if strings.ContainsAny(n, "\n\r<>") || strings.ContainsAny(e, "\n\r<> ") {
		return ""
	}
	return fmt.Sprintf("Co-authored-by: %s <%s>", n, e)
}

// withAttribution appends the trailer to a commit message, separated by a blank
// line so `git interpret-trailers` sees a trailer block rather than prose.
func withAttribution(message string, name, email *string) string {
	trailer := attributionTrailer(name, email)
	if trailer == "" {
		return message
	}
	return strings.TrimRight(message, "\n") + "\n\n" + trailer + "\n"
}
