package db

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"
)

type tnError struct {
	kind apperr.Kind
	code string
	msg  string
}

// tnErrors maps the SQLSTATEs raised by the SECURITY DEFINER functions (design, 7.4) to
// stable application errors. TN030 and TN031 mean the code recorded an audit event for
// the wrong tenant: a bug, never the client's fault.
func tnErrors() map[string]tnError {
	return map[string]tnError{
		"TN001": {apperr.KindUnauthorized, "auth.user_not_found", "the user does not exist"},
		"TN002": {apperr.KindUnauthorized, "auth.user_disabled", "the user is disabled"},
		"TN010": {apperr.KindNotFound, "invitation.not_found", "invitation not found"},
		"TN011": {apperr.KindConflict, "invitation.already_accepted", "the invitation has already been accepted"},
		"TN012": {apperr.KindConflict, "invitation.revoked", "the invitation has been revoked"},
		"TN013": {apperr.KindConflict, "invitation.expired", "the invitation has expired"},
		"TN014": {apperr.KindForbidden, "tenant.not_active", "the tenant is not active"},
		"TN015": {apperr.KindForbidden, "invitation.email_mismatch", "the invitation is for another email address"},
		"TN016": {apperr.KindConflict, "invitation.already_member", "the user is already a member of the tenant"},
		"TN020": {apperr.KindConflict, "tenant.slug_taken", "the tenant slug is already taken"},
		"TN030": {apperr.KindInternal, "audit.tenant_mismatch", "internal error"},
		"TN031": {apperr.KindInternal, "audit.tenant_not_set", "internal error"},
	}
}

// MapError translates the TN0xx errors of the SECURITY DEFINER functions into apperr
// errors, keeping the database error as the cause. Any other error is returned as is.
func MapError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	e, ok := tnErrors()[pgErr.Code]
	if !ok {
		return err
	}
	return apperr.Wrap(err, e.kind, e.code, e.msg)
}
