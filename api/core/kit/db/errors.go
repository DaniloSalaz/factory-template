package db

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"chaskicode.dev/saas/platform/kit/apperr"
)

type tnError struct {
	kind apperr.Kind
	code string
	msg  string
}

// tnErrors maps the SQLSTATEs raised by the SECURITY DEFINER functions (auth-y-tenancy §7.4) to
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
		"TN021": {apperr.KindForbidden, "tenant.limit_reached", "You have created as many tenants as you may."},
		"TN060": {apperr.KindNotFound, "membership.not_found", "The user is not a member of this tenant."},
		"TN061": {apperr.KindConflict, "membership.last_owner", "The tenant must keep at least one owner: make someone else owner first."},
		"TN062": {apperr.KindConflict, "membership.changed", "A role changed meanwhile; read the members again and retry."},
		"TN040": {apperr.KindNotFound, "mfa.factor_not_found", "MFA factor not found"},
		"TN041": {apperr.KindNotFound, "mfa.challenge_not_found", "The challenge does not exist or is not this session's; request a new one."},
		"TN042": {apperr.KindConflict, "mfa.challenge_used", "The challenge has already been used; request a new one."},
		"TN043": {apperr.KindConflict, "mfa.challenge_expired", "The challenge has expired; request a new one."},
		"TN044": {apperr.KindConflict, "mfa.code_reused", "This code has already been used; wait for the next one."},
		"TN045": {apperr.KindConflict, "mfa.too_many_pending", "Finish or remove your pending factors before enrolling another."},
		"TN050": {apperr.KindUnauthorized, "auth.session_invalid", "The session is missing, expired or revoked."},
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
