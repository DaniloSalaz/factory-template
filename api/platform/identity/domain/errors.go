// Package domain holds the identity module's rules: return_to validation, login intents,
// session tokens and their validity, and how MFA is established. It imports only the
// standard library and platform/kit/apperr.
package domain

import (
	"time"

	"chaskicode.dev/saas/platform/kit/apperr"
)

// Every session failure is the same 401, so a client cannot tell a revoked session from
// an expired or unknown one.

// ErrSessionInvalid is returned for a missing, unknown, revoked or expired session, or
// one whose user is disabled.
func ErrSessionInvalid() error {
	return apperr.Unauthorized("auth.session_invalid", "The session is missing, expired or revoked.")
}

// ErrReturnToInvalid is returned when return_to does not resolve to the front end.
func ErrReturnToInvalid() error {
	return apperr.BadRequest("auth.return_to_invalid", "return_to must be a location of the front end.")
}

// ErrIntentInvalid is returned when the callback's state names no live login intent.
func ErrIntentInvalid() error {
	return apperr.BadRequest("auth.intent_invalid", "The login attempt is unknown or has expired; start again.")
}

// ErrEmailUnverified is returned when the provider has not verified the user's email.
func ErrEmailUnverified() error {
	return apperr.Forbidden("auth.email_unverified", "The email address is not verified.")
}

// ErrUserDisabled is returned when a disabled user tries to log in.
func ErrUserDisabled() error {
	return apperr.Forbidden("auth.user_disabled", "The user is disabled.")
}

// ErrEmailInUse is returned when a new identity's email already belongs to another user;
// identities are never linked by email (design, section 2).
func ErrEmailInUse() error {
	return apperr.Conflict("auth.email_in_use", "The email address is already used by another account.")
}

// ErrImpersonationRefused is returned when the provider reports that someone signed in
// as the user: impersonation for support is out of this version (design, section 12).
func ErrImpersonationRefused() error {
	return apperr.Forbidden("auth.impersonation_refused", "Signing in as another user is not allowed.")
}

// ErrStaleAuthentication is returned when the provider reports an authentication older
// than the login attempt: the user was not asked to authenticate again.
func ErrStaleAuthentication() error {
	return apperr.Unauthorized("auth.stale_authentication", "The identity provider did not authenticate you again; start the login again.")
}

// ErrTenantForbidden is returned when the request may not act in the tenant it names:
// a malformed or missing X-Tenant-ID, a tenant that does not exist or is suspended, no
// membership, or a disabled user. All are the same 403, so a caller cannot learn which
// tenants exist (design, 5.4.2).
func ErrTenantForbidden() error {
	return apperr.Forbidden("auth.tenant_forbidden", "You cannot act in this tenant.")
}

// The two answers of a role that requires MFA (design, 5.4.3) differ in what the front
// end must do next, so they have different codes.

// ErrMFAVerificationRequired is returned when the user has a verified factor but the
// session's MFA is missing or too old: the front end starts a challenge and asks for a
// code (POST /me/mfa/challenges, then POST /me/mfa/totp/verify).
func ErrMFAVerificationRequired() error {
	return apperr.Forbidden("auth.mfa_required", "Verify your second factor to continue.")
}

// ErrMFAEnrollmentRequired is returned when the role requires MFA and the user has no
// verified factor yet: the front end leads them to enroll one (POST /me/mfa/totp).
func ErrMFAEnrollmentRequired() error {
	return apperr.Forbidden("auth.mfa_enrollment_required", "Set up a second factor to continue.")
}

// ErrMFACodeInvalid is returned when the provider rejects the code. The challenge is
// used up all the same: the next attempt needs a new one.
func ErrMFACodeInvalid() error {
	return apperr.Validation("mfa.code_invalid", "The code is not valid; request a new challenge and try again.")
}

// ErrMFARateLimited is returned when the user or the session has made too many
// verification attempts or challenges in the window.
func ErrMFARateLimited(retryAfter time.Duration) error {
	return apperr.RateLimited("mfa.rate_limited", "Too many attempts; try again later.", retryAfter)
}

// ErrMFALastFactor is returned when removing the factor would leave the user without a
// verified one while a role of theirs, in an active tenant, requires MFA.
func ErrMFALastFactor() error {
	return apperr.Forbidden("mfa.last_factor_required",
		"A role of yours requires multi-factor authentication: enroll another factor before removing this one.")
}
