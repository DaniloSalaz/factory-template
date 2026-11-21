package domain

import (
	"crypto/sha256"
	"time"
)

// AuthClockSkew is how far the provider's clock may lag behind ours.
const AuthClockSkew = 60 * time.Second

// FreshAuthentication reports whether the provider authenticated the user during this
// login, that is, after the intent started (less AuthClockSkew). Every login forces a
// fresh authentication (max_age=0); if the provider ignored that, auth_time would be
// older and describe an earlier login.
func FreshAuthentication(authTime, intentStartedAt time.Time) bool {
	return !authTime.Before(intentStartedAt.Add(-AuthClockSkew))
}

// MFA is optional, and required only by the roles of the catalog that say so (auth-y-tenancy
// §5.2; ADR 0002). The identity provider reports nothing about MFA in a login, so the app
// verifies the second factor itself after the login, and a session counts as MFA only
// through the mfa_auth_time that verification sets.

// FactorChangeMFAMaxAge is how recent the session's MFA must be to enroll or remove a
// factor once the user has a verified one: a stolen session alone cannot swap the
// victim's factor for the thief's.
const FactorChangeMFAMaxAge = 10 * time.Minute

// MFASatisfied reports whether a session whose MFA was at mfaAuthTime (zero: never) has
// an MFA no older than maxAge at now.
func MFASatisfied(mfaAuthTime time.Time, maxAge time.Duration, now time.Time) bool {
	return !mfaAuthTime.IsZero() && now.Sub(mfaAuthTime) <= maxAge
}

// totpDigits is the length of the codes the provider issues (WorkOS: 6, SHA-1, 30 s).
const totpDigits = 6

// ValidTOTPCode reports whether code looks like a TOTP code, so anything else is refused
// before it reaches the provider.
func ValidTOTPCode(code string) bool {
	if len(code) != totpDigits {
		return false
	}
	for _, c := range []byte(code) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// HashCode is what the database keeps of the last code accepted with a factor, to refuse
// it if it comes again. A 6-digit code is trivial to brute-force from its hash, which is
// harmless: a used code is worthless once its 30 s step has passed.
func HashCode(code string) []byte {
	sum := sha256.Sum256([]byte(code))
	return sum[:]
}
