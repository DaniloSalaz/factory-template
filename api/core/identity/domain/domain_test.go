package domain

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"chaskicode.dev/saas/core/kit/apperr"
)

const front = "http://localhost:5173"

func TestResolveReturnTo(t *testing.T) {
	t.Parallel()

	accepted := map[string]string{
		"":                        front + "/",
		"/":                       front + "/",
		"/cases/42?tab=docs#top":  front + "/cases/42?tab=docs#top",
		"cases":                   front + "/cases",
		"http://localhost:5173/x": front + "/x",
		"HTTP://LOCALHOST:5173/x": "http://LOCALHOST:5173/x",
		"/../../etc":              front + "/etc",
	}
	for raw, want := range accepted {
		t.Run("accepts "+raw, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveReturnTo(front, raw)
			if err != nil || got != want {
				t.Fatalf("ResolveReturnTo(%q) = %q, %v; want %q", raw, got, err, want)
			}
		})
	}

	rejected := []string{
		"//evil.com",
		`/\evil.com`,
		"/\t/evil.com",
		`/\t/evil.com`,
		"https://evil.com",
		"http://localhost:5174/",
		"https://localhost:5173/",
		"//localhost:5173@evil.com",
		"http://user@localhost:5173/",
		"javascript:alert(1)",
		"/\n/evil.com",
		" //evil.com",
		"%",
	}
	for _, raw := range rejected {
		t.Run("rejects "+raw, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveReturnTo(front, raw)
			if !errors.Is(err, ErrReturnToInvalid()) {
				t.Fatalf("ResolveReturnTo(%q) = %q, %v; want auth.return_to_invalid", raw, got, err)
			}
		})
	}
}

func newCodec(t *testing.T, key string) IntentCodec {
	t.Helper()
	c, err := NewIntentCodec([]byte(strings.Repeat(key, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newIntent(t *testing.T, started time.Time) Intent {
	t.Helper()
	state, err := NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	return Intent{State: state, Verifier: "verifier", ReturnTo: front + "/x", Purpose: PurposeLogin, StartedAt: started}
}

func TestIntentCodec(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	codec := newCodec(t, "k")
	intent := newIntent(t, now)
	value, err := codec.Seal(intent)
	if err != nil {
		t.Fatal(err)
	}

	got, err := codec.Open(intent.State, value, now.Add(time.Minute))
	if err != nil || got != intent {
		t.Fatalf("Open = %+v, %v; want %+v", got, err, intent)
	}

	other := newIntent(t, now)
	enc, sig, _ := strings.Cut(value, ".")
	invalid := map[string]struct {
		state, value string
		at           time.Time
	}{
		"expired":                 {intent.State, value, now.Add(IntentTTL + time.Second)},
		"from the future":         {intent.State, value, now.Add(-time.Hour)},
		"under another state":     {other.State, value, now},
		"signed with another key": {intent.State, mustSeal(t, newCodec(t, "x"), intent), now},
		"payload altered":         {intent.State, enc + "x." + sig, now},
		"no signature":            {intent.State, enc, now},
		"malformed state":         {"short", value, now},
	}
	for name, tc := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := codec.Open(tc.state, tc.value, tc.at); !errors.Is(err, ErrIntentInvalid()) {
				t.Fatalf("Open: %v, want auth.intent_invalid", err)
			}
		})
	}

	t.Run("an unknown purpose is refused even if signed", func(t *testing.T) {
		t.Parallel()
		forged := intent
		forged.Purpose = "stepup"
		if _, err := codec.Open(forged.State, mustSeal(t, codec, forged), now); !errors.Is(err, ErrIntentInvalid()) {
			t.Fatalf("Open: %v, want auth.intent_invalid", err)
		}
	})
}

func mustSeal(t *testing.T, c IntentCodec, i Intent) string {
	t.Helper()
	v, err := c.Seal(i)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestIntentsToDiscard(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	codec := newCodec(t, "k")
	var stored []StoredIntent
	var states []string
	for i := range 3 {
		in := newIntent(t, now.Add(time.Duration(i)*time.Second))
		stored = append(stored, StoredIntent{State: in.State, Value: mustSeal(t, codec, in)})
		states = append(states, in.State)
	}
	stored = append(stored, StoredIntent{State: "garbage", Value: "x"})

	got := codec.IntentsToDiscard(stored, now.Add(3*time.Second))
	if len(got) != 2 || !contains(got, "garbage") || !contains(got, states[0]) {
		t.Fatalf("discard = %v; want garbage and the oldest (%s)", got, states[0])
	}
	if got := codec.IntentsToDiscard(stored[:2], now); len(got) != 0 {
		t.Fatalf("two live intents leave room for a third, discard = %v", got)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestSessionPolicy_Check(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	p := SessionPolicy{IdleTTL: 7 * 24 * time.Hour, AbsoluteTTL: 30 * 24 * time.Hour}
	live := SessionState{
		LastSeenAt: now.Add(-time.Hour), IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(24 * time.Hour),
	}
	if err := p.Check(live, now); err != nil {
		t.Fatalf("live session: %v", err)
	}

	cases := map[string]func(s *SessionState){
		"revoked":             func(s *SessionState) { s.Revoked = true },
		"user disabled":       func(s *SessionState) { s.UserDisabled = true },
		"absolute expiry":     func(s *SessionState) { s.AbsoluteExpiresAt = now },
		"idle expiry":         func(s *SessionState) { s.IdleExpiresAt = now.Add(-time.Second) },
		"idle by current TTL": func(s *SessionState) { s.LastSeenAt = now.Add(-p.IdleTTL); s.IdleExpiresAt = now.Add(time.Hour) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := live
			mutate(&s)
			err := p.Check(s, now)
			if e, ok := apperr.As(err); !ok || e.Code != "auth.session_invalid" || e.Kind != apperr.KindUnauthorized {
				t.Fatalf("Check = %v, want auth.session_invalid", err)
			}
		})
	}
}

func TestNeedsTouch(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	if NeedsTouch(now.Add(-4*time.Minute), now) {
		t.Fatal("touched less than 5 minutes ago")
	}
	if !NeedsTouch(now.Add(-5*time.Minute), now) {
		t.Fatal("5 minutes is stale")
	}
}

func TestMFASatisfied(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"never verified", time.Time{}, false},
		{"recent", now.Add(-time.Minute), true},
		{"exactly the maximum", now.Add(-FactorChangeMFAMaxAge), true},
		{"older than the maximum", now.Add(-FactorChangeMFAMaxAge - time.Second), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MFASatisfied(tc.at, FactorChangeMFAMaxAge, now); got != tc.want {
				t.Fatalf("MFASatisfied = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidTOTPCode(t *testing.T) {
	t.Parallel()

	for code, want := range map[string]bool{
		"123456": true, "000000": true, "12345": false, "1234567": false, "12345a": false, "": false, "\t23456": false,
		"１２３４５６": false,
	} {
		if got := ValidTOTPCode(code); got != want {
			t.Errorf("ValidTOTPCode(%q) = %v, want %v", code, got, want)
		}
	}
	if string(HashCode("123456")) == string(HashCode("123457")) || len(HashCode("123456")) != 32 {
		t.Fatal("HashCode must be the SHA-256 of the code")
	}
}

func TestFreshAuthentication(t *testing.T) {
	t.Parallel()
	start := time.Unix(1_800_000_000, 0)
	tests := map[time.Duration]bool{
		time.Minute:       true,
		0:                 true,
		-59 * time.Second: true,
		-60 * time.Second: true,
		-61 * time.Second: false,
		-time.Hour:        false,
	}
	for offset, want := range tests {
		if got := FreshAuthentication(start.Add(offset), start); got != want {
			t.Errorf("auth_time at start%+v: fresh = %v, want %v", offset, got, want)
		}
	}
}

func TestTruncateUserAgent(t *testing.T) {
	t.Parallel()
	if got := TruncateUserAgent("Mozilla/5.0"); got != "Mozilla/5.0" {
		t.Fatalf("short user agent changed: %q", got)
	}
	long := strings.Repeat("ñ", MaxUserAgentLength+100)
	if got := TruncateUserAgent(long); got != strings.Repeat("ñ", MaxUserAgentLength) {
		t.Fatalf("truncated to %d characters, want %d", len([]rune(got)), MaxUserAgentLength)
	}
	if got := TruncateUserAgent("a\xffb"); got != "a\uFFFDb" {
		t.Fatalf("invalid UTF-8 kept: %q", got)
	}
}

func TestTokens(t *testing.T) {
	t.Parallel()

	a, err := NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewRandom()
	if a == b || !WellFormed(a) || WellFormed(a[:42]) || WellFormed(a[:42]+"!") {
		t.Fatalf("tokens %q %q", a, b)
	}
	if h := HashToken(a); len(h) != 32 || strings.Contains(hex.EncodeToString(h), a) {
		t.Fatalf("hash = %x", h)
	}
	// RFC 7636, appendix B.
	if got := CodeChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge = %s", got)
	}
}
