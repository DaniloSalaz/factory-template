package domain

import (
	"slices"
	"time"
)

// ProviderMFA is what the identity provider tells about how the user authenticated.
//
// WorkOS documents no claim saying whether MFA happened (design, section 13), so MFA can
// only be inferred: every login forces a fresh authentication, and if the provider
// enforces MFA on every user of the environment, that authentication included it. If the
// provider ever reports authentication methods (an amr claim, RFC 8176), those are
// checked instead of inferring.
type ProviderMFA struct {
	// AuthTime is when the user actively authenticated, per the provider.
	AuthTime time.Time
	// CoveredByPolicy is true only when the method used is confirmed to go through the
	// provider's MFA policy. It is an allowlist: unknown methods are not covered.
	CoveredByPolicy bool
	// AMR are the authentication methods reported by the provider; HasAMR is false when
	// it reports none, which is the case of WorkOS today.
	AMR    []string
	HasAMR bool
}

// amrMFA is the RFC 8176 value for authentication with more than one factor.
const amrMFA = "mfa"

// MFAAuthTime returns when the user last passed MFA, if the login proves it.
//
// With reported methods, MFA is what they say: if the environment requires MFA and they
// do not include it, the login is refused. Without them, MFA is inferred only when the
// provider enforces it (providerEnforcesMFA) and the method is on the allowlist of
// methods confirmed to ask for the second factor (CoveredByPolicy).
func MFAAuthTime(m ProviderMFA, providerEnforcesMFA bool) (time.Time, bool, error) {
	if m.HasAMR {
		if slices.Contains(m.AMR, amrMFA) {
			return m.AuthTime, true, nil
		}
		if providerEnforcesMFA {
			return time.Time{}, false, ErrMFAMissing()
		}
		return time.Time{}, false, nil
	}
	if providerEnforcesMFA && m.CoveredByPolicy {
		return m.AuthTime, true, nil
	}
	return time.Time{}, false, nil
}
