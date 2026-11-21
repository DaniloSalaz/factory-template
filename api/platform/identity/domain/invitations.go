package domain

import (
	"net/mail"
	"strings"
	"time"

	"chaskicode.dev/saas/platform/apperr"
)

// Invitations (design, 5.6). Who may offer which role is CanOffer; the token is a
// NewRandom value whose hash is the only form stored, like a session's.

// InvitationTTL is how long an invitation, or a resent one, can be accepted.
const InvitationTTL = 7 * 24 * time.Hour

// emailMaxLen is the longest address SMTP carries (RFC 5321 path limit).
const emailMaxLen = 254

// NormalizeInviteEmail trims raw and checks it is a bare address (no display name, no
// angle brackets). Case is kept: the column is citext, so it compares without it.
func NormalizeInviteEmail(raw string) (string, error) {
	email := strings.TrimSpace(raw)
	if email == "" || len(email) > emailMaxLen {
		return "", ErrInvitationEmailInvalid()
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email {
		return "", ErrInvitationEmailInvalid()
	}
	return email, nil
}

// InvitationLink is where the invitee goes: the token travels in the fragment, which
// the browser never sends to a server nor puts in a Referer (5.6.3).
func InvitationLink(frontBaseURL, token string) string {
	return strings.TrimRight(frontBaseURL, "/") + "/invite#token=" + token
}

// ErrInvitationEmailInvalid is returned for an address that is not a bare email.
func ErrInvitationEmailInvalid() error {
	return apperr.Validation("invitation.email_invalid", "The email address is not valid.",
		apperr.FieldError{Field: "email", Code: "invalid", Message: "a bare email address"})
}

// ErrInvitationUnknownRole is returned when the offered role is not in the catalog.
func ErrInvitationUnknownRole() error {
	return apperr.Validation("invitation.unknown_role", "The role does not exist.",
		apperr.FieldError{Field: "role", Code: "unknown", Message: "not a role of the catalog"})
}

// ErrInvitationRoleNotOfferable is returned when the actor may not offer the role:
// it is not included in theirs, or it is owner and they are not.
func ErrInvitationRoleNotOfferable() error {
	return apperr.Forbidden("invitation.role_not_offerable", "You can only invite to roles included in yours.")
}

// ErrInvitationPending is returned when the email already has an unexpired invitation
// to the tenant: resend that one (POST /invitations/{id}/resend).
func ErrInvitationPending() error {
	return apperr.Conflict("invitation.pending", "This email already has a pending invitation; resend it instead.")
}

// ErrInvitationAlreadyMember is returned when the email belongs to a member already.
// The code is the one accept_invitation's TN016 maps to.
func ErrInvitationAlreadyMember() error {
	return apperr.Conflict("invitation.already_member", "The user is already a member of the tenant.")
}

// ErrInvitationNotFound is returned for an invitation that does not exist in the tenant,
// or a token that names none. The code is the one of TN010.
func ErrInvitationNotFound() error {
	return apperr.NotFound("invitation.not_found", "invitation not found")
}

// ErrInvitationAccepted is returned when resending or revoking an accepted invitation.
func ErrInvitationAccepted() error {
	return apperr.Conflict("invitation.already_accepted", "the invitation has already been accepted")
}

// ErrInvitationRevoked is returned when resending or revoking a revoked invitation.
func ErrInvitationRevoked() error {
	return apperr.Conflict("invitation.revoked", "the invitation has been revoked")
}

// ErrMembershipChanged is returned when the actor's role is no longer the one the
// request started with. The code is the one of TN062.
func ErrMembershipChanged() error {
	return apperr.Conflict("membership.changed", "A role changed meanwhile; read the members again and retry.")
}
