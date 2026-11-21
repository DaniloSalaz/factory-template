package db_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"chaskicode.dev/saas/platform/kit/apperr"
	"chaskicode.dev/saas/platform/kit/db"
)

func TestMapError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		sqlState string
		kind     apperr.Kind
		code     string
	}{
		{"TN001", apperr.KindUnauthorized, "auth.user_not_found"},
		{"TN002", apperr.KindUnauthorized, "auth.user_disabled"},
		{"TN010", apperr.KindNotFound, "invitation.not_found"},
		{"TN011", apperr.KindConflict, "invitation.already_accepted"},
		{"TN012", apperr.KindConflict, "invitation.revoked"},
		{"TN013", apperr.KindConflict, "invitation.expired"},
		{"TN014", apperr.KindForbidden, "tenant.not_active"},
		{"TN015", apperr.KindForbidden, "invitation.email_mismatch"},
		{"TN016", apperr.KindConflict, "invitation.already_member"},
		{"TN020", apperr.KindConflict, "tenant.slug_taken"},
		{"TN021", apperr.KindForbidden, "tenant.limit_reached"},
		{"TN060", apperr.KindNotFound, "membership.not_found"},
		{"TN061", apperr.KindConflict, "membership.last_owner"},
		{"TN062", apperr.KindConflict, "membership.changed"},
		{"TN040", apperr.KindNotFound, "mfa.factor_not_found"},
		{"TN041", apperr.KindNotFound, "mfa.challenge_not_found"},
		{"TN042", apperr.KindConflict, "mfa.challenge_used"},
		{"TN043", apperr.KindConflict, "mfa.challenge_expired"},
		{"TN044", apperr.KindConflict, "mfa.code_reused"},
		{"TN045", apperr.KindConflict, "mfa.too_many_pending"},
		{"TN050", apperr.KindUnauthorized, "auth.session_invalid"},
		{"TN030", apperr.KindInternal, "audit.tenant_mismatch"},
		{"TN031", apperr.KindInternal, "audit.tenant_not_set"},
	}
	for _, tc := range tests {
		t.Run(tc.sqlState, func(t *testing.T) {
			t.Parallel()

			pgErr := &pgconn.PgError{Code: tc.sqlState, Message: "from the function"}
			// Wrapped as a repository would return it.
			got := db.MapError(fmt.Errorf("accept invitation: %w", pgErr))

			e, ok := apperr.As(got)
			if !ok {
				t.Fatalf("MapError returned %v, want an *apperr.Error", got)
			}
			if e.Kind != tc.kind || e.Code != tc.code {
				t.Fatalf("got %v %q, want %v %q", e.Kind, e.Code, tc.kind, tc.code)
			}
			if e.Message == "" {
				t.Fatal("empty message")
			}
			var cause *pgconn.PgError
			if !errors.As(got, &cause) || cause != pgErr {
				t.Fatal("the database error is not kept as the cause")
			}
		})
	}
}

func TestMapErrorPassesThrough(t *testing.T) {
	t.Parallel()

	plain := errors.New("connection reset")
	unique := &pgconn.PgError{Code: "23505"}
	unknownTN := &pgconn.PgError{Code: "TN099"}
	for name, err := range map[string]error{
		"nil":                nil,
		"not a pg error":     plain,
		"other SQLSTATE":     unique,
		"unknown TN code":    unknownTN,
		"already app errors": apperr.NotFound("x.not_found", "x"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := db.MapError(err); got != err { //nolint:errorlint // identity is the point: it must come back untouched
				t.Fatalf("MapError(%v) = %v, want it unchanged", err, got)
			}
		})
	}
}
