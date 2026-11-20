// Package domain holds the identity module's rules: return_to validation, login intents,
// session tokens and their validity, and how MFA is established. It imports only the
// standard library and platform/apperr.
package domain

import "github.com/DaniloSalaz/factory-template/api/internal/platform/apperr"

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

// ErrMFAMissing is returned when the provider reports how the user authenticated and
// MFA is required but was not part of it.
func ErrMFAMissing() error {
	return apperr.Forbidden("auth.mfa_required", "Multi-factor authentication is required.")
}
