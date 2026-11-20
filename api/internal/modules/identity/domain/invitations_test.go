package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeInviteEmail(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ raw, want string }{
		{"ana@example.com", "ana@example.com"},
		{"  Ana@Example.com ", "Ana@Example.com"},
		{"a.b+tag@sub.example", "a.b+tag@sub.example"},
	} {
		if got, err := NormalizeInviteEmail(tc.raw); err != nil || got != tc.want {
			t.Errorf("NormalizeInviteEmail(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{
		"", "ana", "ana@", "@example.com", "Ana <ana@example.com>", "<ana@example.com>",
		"ana@example.com, bea@example.com", strings.Repeat("a", 250) + "@x.io",
	} {
		if _, err := NormalizeInviteEmail(raw); !errors.Is(err, ErrInvitationEmailInvalid()) {
			t.Errorf("NormalizeInviteEmail(%q) = %v, want %v", raw, err, ErrInvitationEmailInvalid())
		}
	}
}

func TestInvitationLink(t *testing.T) {
	t.Parallel()

	for base, want := range map[string]string{
		"https://app.example.com":  "https://app.example.com/invite#token=tok",
		"https://app.example.com/": "https://app.example.com/invite#token=tok",
		"https://example.com/app":  "https://example.com/app/invite#token=tok",
	} {
		if got := InvitationLink(base, "tok"); got != want {
			t.Errorf("InvitationLink(%q) = %q, want %q", base, got, want)
		}
	}
}
