package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// TouchInterval is how stale last_seen_at may get before a request refreshes it
// (design, 5.4.1): at most one write per session every five minutes.
const TouchInterval = 5 * time.Minute

// MaxUserAgentLength is how many characters of the User-Agent a session keeps: enough to
// recognise a browser in a list of sessions, and a bound on what a client can make us
// store (the header itself may be up to HTTP_MAX_HEADER_BYTES).
const MaxUserAgentLength = 512

// TruncateUserAgent returns at most MaxUserAgentLength characters of ua, as valid UTF-8
// (Postgres text refuses invalid sequences).
func TruncateUserAgent(ua string) string {
	ua = strings.ToValidUTF8(ua, "\uFFFD")
	if utf8.RuneCountInString(ua) <= MaxUserAgentLength {
		return ua
	}
	return string([]rune(ua)[:MaxUserAgentLength])
}

// SessionPolicy holds the session lifetimes (design, section 6).
type SessionPolicy struct {
	IdleTTL     time.Duration
	AbsoluteTTL time.Duration
}

// Expiries returns the idle and absolute expiry of a session created at now.
func (p SessionPolicy) Expiries(now time.Time) (idle, absolute time.Time) {
	return now.Add(p.IdleTTL), now.Add(p.AbsoluteTTL)
}

// SessionState is what a request's session check reads, with the user it belongs to.
type SessionState struct {
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	Revoked           bool
	UserDisabled      bool
}

// Check returns ErrSessionInvalid unless the session is usable at now. Inactivity is
// measured from the last_seen_at stored before this request (design, 5.4.1), and the
// current idle TTL also applies, so shortening it takes effect on existing sessions.
func (p SessionPolicy) Check(s SessionState, now time.Time) error {
	idleExpiry := s.IdleExpiresAt
	if byPolicy := s.LastSeenAt.Add(p.IdleTTL); byPolicy.Before(idleExpiry) {
		idleExpiry = byPolicy
	}
	switch {
	case s.Revoked, s.UserDisabled, !now.Before(s.AbsoluteExpiresAt), !now.Before(idleExpiry):
		return ErrSessionInvalid()
	}
	return nil
}

// NeedsTouch reports whether a request at now should refresh last_seen_at.
func NeedsTouch(lastSeenAt, now time.Time) bool {
	return now.Sub(lastSeenAt) >= TouchInterval
}
