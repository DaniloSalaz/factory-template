package apperr_test

import (
	"errors"
	"fmt"
	"testing"

	"chaskicode.dev/saas/platform/kit/apperr"
)

func TestConstructors(t *testing.T) {
	t.Parallel()

	field := apperr.FieldError{Field: "email", Code: "required", Message: "email is required"}
	tests := []struct {
		name       string
		err        error
		wantKind   apperr.Kind
		wantFields int
	}{
		{"not found", apperr.NotFound("user_not_found", "user not found"), apperr.KindNotFound, 0},
		{"conflict", apperr.Conflict("email_taken", "email taken"), apperr.KindConflict, 0},
		{"validation", apperr.Validation("invalid_input", "invalid input", field), apperr.KindValidation, 1},
		{"unauthorized", apperr.Unauthorized("no_session", "login required"), apperr.KindUnauthorized, 0},
		{"forbidden", apperr.Forbidden("not_owner", "not allowed"), apperr.KindForbidden, 0},
		{"rate limited", apperr.RateLimited("too_many", "slow down", 0), apperr.KindRateLimited, 0},
		{"bad request", apperr.BadRequest("malformed", "malformed body"), apperr.KindBadRequest, 0},
		{"unavailable", apperr.Unavailable("db_down", "try later"), apperr.KindUnavailable, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e, ok := apperr.As(tc.err)
			if !ok {
				t.Fatalf("As(%v) = false", tc.err)
			}
			if e.Kind != tc.wantKind || apperr.KindOf(tc.err) != tc.wantKind {
				t.Fatalf("kind = %v, want %v", e.Kind, tc.wantKind)
			}
			if len(e.Fields) != tc.wantFields {
				t.Fatalf("fields = %v, want %d", e.Fields, tc.wantFields)
			}
		})
	}
}

func TestWrap(t *testing.T) {
	t.Parallel()

	cause := errors.New("connection reset")
	tests := []struct {
		name      string
		err       error
		wantNil   bool
		wantKind  apperr.Kind
		wantCause bool
		wantText  string
	}{
		{name: "nil stays nil", err: apperr.Wrap(nil, apperr.KindInternal, "x", "y"), wantNil: true},
		{
			name:      "keeps cause",
			err:       apperr.Wrap(cause, apperr.KindConflict, "stale", "record changed"),
			wantKind:  apperr.KindConflict,
			wantCause: true,
			wantText:  "stale: record changed: connection reset",
		},
		{
			name:      "found through fmt wrapping",
			err:       fmt.Errorf("save user: %w", apperr.Wrap(cause, apperr.KindInternal, "db", "storage failed")),
			wantKind:  apperr.KindInternal,
			wantCause: true,
			wantText:  "save user: db: storage failed: connection reset",
		},
		{name: "plain error is internal", err: cause, wantKind: apperr.KindInternal, wantCause: true, wantText: "connection reset"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.wantNil {
				if tc.err != nil {
					t.Fatalf("Wrap(nil) = %v, want nil", tc.err)
				}
				return
			}
			if got := apperr.KindOf(tc.err); got != tc.wantKind {
				t.Fatalf("KindOf = %v, want %v", got, tc.wantKind)
			}
			if got := errors.Is(tc.err, cause); got != tc.wantCause {
				t.Fatalf("errors.Is(cause) = %v, want %v", got, tc.wantCause)
			}
			if got := tc.err.Error(); got != tc.wantText {
				t.Fatalf("Error() = %q, want %q", got, tc.wantText)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind apperr.Kind
		want string
	}{
		{apperr.KindInternal, "internal"},
		{apperr.KindNotFound, "not_found"},
		{apperr.KindConflict, "conflict"},
		{apperr.KindValidation, "validation"},
		{apperr.KindUnauthorized, "unauthorized"},
		{apperr.KindForbidden, "forbidden"},
		{apperr.KindRateLimited, "rate_limited"},
		{apperr.KindBadRequest, "bad_request"},
		{apperr.KindUnavailable, "unavailable"},
		{apperr.Kind(99), "kind(99)"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.kind.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKindInternalIsTheZeroValue(t *testing.T) {
	t.Parallel()

	var k apperr.Kind
	if k != apperr.KindInternal {
		t.Fatalf("zero Kind = %v, want internal", k)
	}
}

func TestIs_MatchesByCode(t *testing.T) {
	t.Parallel()

	target := apperr.NotFound("user.not_found", "User not found.")
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"same instance", target, true},
		{"other instance same code", apperr.NotFound("user.not_found", "different message"), true},
		{"same code different kind", apperr.Conflict("user.not_found", "x"), true},
		{"wrapped with fmt", fmt.Errorf("load profile: %w", apperr.NotFound("user.not_found", "x")), true},
		{"wrapped twice", fmt.Errorf("a: %w", fmt.Errorf("b: %w", apperr.NotFound("user.not_found", "x"))), true},
		{"as cause of another apperr", apperr.Wrap(apperr.NotFound("user.not_found", "x"), apperr.KindInternal, "outer", "x"), true},
		{"different code", apperr.NotFound("order.not_found", "x"), false},
		{"plain error", errors.New("user.not_found"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := errors.Is(tc.err, target); got != tc.want {
				t.Fatalf("errors.Is = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIs_EmptyCodeNeverMatches(t *testing.T) {
	t.Parallel()

	a := apperr.NotFound("", "x")
	b := apperr.NotFound("", "x")
	if errors.Is(a, b) {
		t.Fatal("errors without code must not match each other by code")
	}
}
