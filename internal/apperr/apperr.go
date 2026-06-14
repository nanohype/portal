// Package apperr is the small vocabulary of error kinds the service layer raises
// so the HTTP layer can map them to status codes in one place
// (respond.FromError) instead of each handler guessing — the same failure was
// a 404 in one handler and a 500 in another.
//
// A query that simply finds nothing returns pgx.ErrNoRows, which FromError
// already treats as 404; reach for these constructors when a handler needs to
// signal forbidden / conflict / bad-input, or a not-found with a specific
// message, from inside the service layer.
package apperr

import "errors"

type Kind int

const (
	KindInternal Kind = iota
	KindNotFound
	KindForbidden
	KindConflict
	KindValidation
)

// Error carries a Kind (→ HTTP status) plus a user-safe message and an optional
// wrapped cause.
type Error struct {
	Kind Kind
	Msg  string
	err  error
}

func (e *Error) Error() string {
	if e.err != nil {
		return e.Msg + ": " + e.err.Error()
	}
	return e.Msg
}

func (e *Error) Unwrap() error { return e.err }

func NotFound(msg string) error   { return &Error{Kind: KindNotFound, Msg: msg} }
func Forbidden(msg string) error  { return &Error{Kind: KindForbidden, Msg: msg} }
func Conflict(msg string) error   { return &Error{Kind: KindConflict, Msg: msg} }
func Validation(msg string) error { return &Error{Kind: KindValidation, Msg: msg} }

// Wrap attaches a kind + message to an underlying cause, preserving it for logs
// (the cause stays reachable via errors.Unwrap) while the message is user-safe.
func Wrap(kind Kind, msg string, cause error) error {
	return &Error{Kind: kind, Msg: msg, err: cause}
}

// KindOf returns the Kind of err if it's (or wraps) an *Error, else KindInternal.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindInternal
}
