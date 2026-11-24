// Package apperr defines the application's error vocabulary. Domain and app code create
// errors here; transports (HTTP, jobs) translate the Kind into their own status codes.
// It depends only on the standard library so the domain may import it.
package apperr

import (
	"errors"
	"fmt"
	"time"
)

// Kind classifies an error by how a caller should react to it.
type Kind uint8

// The zero Kind is KindInternal, so an Error built without a kind fails closed.
const (
	KindInternal Kind = iota
	KindNotFound
	KindConflict
	KindValidation
	KindUnauthorized
	KindForbidden
	KindRateLimited
	KindBadRequest
	KindUnavailable
)

// String returns the kind's name.
func (k Kind) String() string {
	switch k {
	case KindInternal:
		return "internal"
	case KindNotFound:
		return "not_found"
	case KindConflict:
		return "conflict"
	case KindValidation:
		return "validation"
	case KindUnauthorized:
		return "unauthorized"
	case KindForbidden:
		return "forbidden"
	case KindRateLimited:
		return "rate_limited"
	case KindBadRequest:
		return "bad_request"
	case KindUnavailable:
		return "unavailable"
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

// FieldError describes why a single input field is invalid.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error is an application error. Code is a stable machine-readable identifier and Message
// is safe to show to clients; the wrapped cause is for logs only.
type Error struct {
	Kind    Kind
	Code    string
	Message string
	Fields  []FieldError
	// RetryAfter, on a KindRateLimited error, is how long until the quota allows another
	// request; zero when unknown.
	RetryAfter time.Duration
	cause      error
}

// Error renders the code, message and cause for logs.
func (e *Error) Error() string {
	if e.cause == nil {
		return e.Code + ": " + e.Message
	}
	return e.Code + ": " + e.Message + ": " + e.cause.Error()
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.cause }

// Is makes errors.Is match by Code, so a freshly built error can be compared with one
// returned from deep in a call chain. Errors without a code never match this way.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code != "" && t.Code == e.Code
}

// The constructors return error rather than *Error so a nil *Error can never become a
// non-nil error interface.

// NotFound reports a missing resource.
func NotFound(code, msg string) error { return newError(KindNotFound, code, msg) }

// Conflict reports a state conflict such as a duplicate or a stale version.
func Conflict(code, msg string) error { return newError(KindConflict, code, msg) }

// Validation reports invalid input, optionally per field.
func Validation(code, msg string, fields ...FieldError) error {
	return &Error{Kind: KindValidation, Code: code, Message: msg, Fields: fields}
}

// Unauthorized reports missing or invalid credentials.
func Unauthorized(code, msg string) error { return newError(KindUnauthorized, code, msg) }

// Forbidden reports authenticated access to something the caller may not use.
func Forbidden(code, msg string) error { return newError(KindForbidden, code, msg) }

// RateLimited reports that the caller exceeded a quota, which allows another request
// after retryAfter (zero when unknown).
func RateLimited(code, msg string, retryAfter time.Duration) error {
	return &Error{Kind: KindRateLimited, Code: code, Message: msg, RetryAfter: retryAfter}
}

// BadRequest reports a request that is malformed, as opposed to well-formed but invalid.
func BadRequest(code, msg string) error { return newError(KindBadRequest, code, msg) }

// Unavailable reports that a dependency is down; the caller may retry later.
func Unavailable(code, msg string) error { return newError(KindUnavailable, code, msg) }

// Wrap classifies err, keeping it as the cause. It returns nil when err is nil so it can
// wrap a call result directly.
func Wrap(err error, kind Kind, code, msg string) error {
	if err == nil {
		return nil
	}
	return &Error{Kind: kind, Code: code, Message: msg, cause: err}
}

// As returns the outermost *Error in err's chain.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// KindOf returns the kind of err, or KindInternal when err is not an *Error.
func KindOf(err error) Kind {
	if e, ok := As(err); ok {
		return e.Kind
	}
	return KindInternal
}

func newError(kind Kind, code, msg string) *Error {
	return &Error{Kind: kind, Code: code, Message: msg}
}
