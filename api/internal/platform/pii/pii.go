// Package pii provides string types for personal and secret data that mask themselves
// wherever they can leak by accident: fmt verbs, slog and JSON. Only Reveal returns the
// real value, so every disclosure is explicit and greppable.
package pii

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// Redacted replaces values that cannot be partially shown.
	Redacted = "[REDACTED]"
	stars    = "***"
	// phoneVisibleDigits is how many trailing digits of a phone number stay visible.
	phoneVisibleDigits = 4
)

// Email is an email address. It renders as j***@d***.com.
type Email string

// Reveal returns the unmasked email.
func (e Email) Reveal() string { return string(e) }

// String implements fmt.Stringer with the masked value.
func (e Email) String() string { return maskEmail(string(e)) }

// GoString implements fmt.GoStringer so %#v is masked too.
func (e Email) GoString() string { return strconv.Quote(e.String()) }

// LogValue implements slog.LogValuer with the masked value.
func (e Email) LogValue() slog.Value { return slog.StringValue(e.String()) }

// MarshalJSON masks the value; JSON is a common accidental leak path (logs, error payloads).
func (e Email) MarshalJSON() ([]byte, error) { return marshalMasked(e.String()) }

// Phone is a phone number. It renders as ***1234.
type Phone string

// Reveal returns the unmasked phone number.
func (p Phone) Reveal() string { return string(p) }

// String implements fmt.Stringer with the masked value.
func (p Phone) String() string { return maskPhone(string(p)) }

// GoString implements fmt.GoStringer so %#v is masked too.
func (p Phone) GoString() string { return strconv.Quote(p.String()) }

// LogValue implements slog.LogValuer with the masked value.
func (p Phone) LogValue() slog.Value { return slog.StringValue(p.String()) }

// MarshalJSON masks the value.
func (p Phone) MarshalJSON() ([]byte, error) { return marshalMasked(p.String()) }

// Secret is a credential or token. It always renders as [REDACTED].
type Secret string

// Reveal returns the unmasked secret.
func (s Secret) Reveal() string { return string(s) }

// String implements fmt.Stringer with the masked value.
func (Secret) String() string { return Redacted }

// GoString implements fmt.GoStringer so %#v is masked too.
func (s Secret) GoString() string { return strconv.Quote(s.String()) }

// LogValue implements slog.LogValuer with the masked value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalJSON masks the value.
func (s Secret) MarshalJSON() ([]byte, error) { return marshalMasked(s.String()) }

func marshalMasked(masked string) ([]byte, error) {
	b, err := json.Marshal(masked)
	if err != nil {
		return nil, err //nolint:wrapcheck // marshalling a plain string cannot fail.
	}
	return b, nil
}

// maskEmail keeps the first character of the local part and of the domain name plus the
// TLD. Anything not shaped like local@domain is fully masked rather than guessed at.
func maskEmail(v string) string {
	local, domain, ok := strings.Cut(v, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return stars
	}

	masked := firstRune(local) + stars + "@"
	dot := strings.LastIndexByte(domain, '.')
	if dot <= 0 || dot == len(domain)-1 {
		return masked + firstRune(domain) + stars
	}
	return masked + firstRune(domain) + stars + domain[dot:]
}

// maskPhone keeps the last digits only when there are more digits than that, so a
// short number is never shown in full.
func maskPhone(v string) string {
	digits := make([]rune, 0, len(v))
	for _, r := range v {
		if unicode.IsDigit(r) {
			digits = append(digits, r)
		}
	}
	if len(digits) <= phoneVisibleDigits {
		return stars
	}
	return stars + string(digits[len(digits)-phoneVisibleDigits:])
}

func firstRune(s string) string {
	r, _ := utf8.DecodeRuneInString(s)
	return string(r)
}
