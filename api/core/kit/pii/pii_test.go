package pii_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"chaskicode.dev/saas/core/kit/pii"
)

// renderings returns every way a value commonly ends up in output.
func renderings(t *testing.T, v any) map[string]string {
	t.Helper()

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("msg", slog.Any("v", v))
	j, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return map[string]string{
		"%s":         fmt.Sprintf("%s", v),
		"%v":         fmt.Sprintf("%v", v),
		"%+v":        fmt.Sprintf("%+v", v),
		"%q":         fmt.Sprintf("%q", v),
		"%#v":        fmt.Sprintf("%#v", v),
		"slog":       buf.String(),
		"json":       string(j),
		"struct %+v": fmt.Sprintf("%+v", struct{ V any }{v}),
	}
}

func TestMasking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		value  any
		raw    string
		masked string
	}{
		{"email", pii.Email("juan@dominio.com"), "juan@dominio.com", "j***@d***.com"},
		{"email subdomain", pii.Email("ana@mail.example.co"), "ana@mail.example.co", "a***@m***.co"},
		{"email unicode", pii.Email("ñandú@correo.es"), "ñandú@correo.es", "ñ***@c***.es"},
		{"email no tld", pii.Email("root@localhost"), "root@localhost", "r***@l***"},
		{"email empty", pii.Email(""), "", "***"},
		{"email without at", pii.Email("juandominio.com"), "juandominio.com", "***"},
		{"email empty local", pii.Email("@dominio.com"), "@dominio.com", "***"},
		{"email empty domain", pii.Email("juan@"), "juan@", "***"},
		{"email two ats", pii.Email("a@b@c.com"), "a@b@c.com", "***"},
		{"phone", pii.Phone("+34 600 111 234"), "600 111 234", "***1234"},
		{"phone short", pii.Phone("123"), "123", "***"},
		{"phone exactly visible digits", pii.Phone("1234"), "1234", "***"},
		{"phone empty", pii.Phone(""), "", "***"},
		{"phone no digits", pii.Phone("n/a"), "n/a", "***"},
		{"secret", pii.Secret("s3cr3t-t0k3n"), "s3cr3t-t0k3n", "[REDACTED]"},
		{"secret empty", pii.Secret(""), "", "[REDACTED]"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := fmt.Sprint(tc.value); got != tc.masked {
				t.Fatalf("String() = %q, want %q", got, tc.masked)
			}
			for verb, out := range renderings(t, tc.value) {
				if !strings.Contains(out, tc.masked) {
					t.Errorf("%s: %q does not contain masked value %q", verb, out, tc.masked)
				}
				if tc.raw != "" && strings.Contains(out, tc.raw) {
					t.Errorf("%s: output leaks the raw value", verb)
				}
			}
			if got, want := fmt.Sprintf("%x", tc.value), fmt.Sprintf("%x", tc.masked); got != want {
				t.Errorf("%%x = %s, want hex of masked value %s", got, want)
			}
		})
	}
}

func TestReveal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"email", pii.Email("juan@dominio.com").Reveal(), "juan@dominio.com"},
		{"phone", pii.Phone("+34 600 111 234").Reveal(), "+34 600 111 234"},
		{"secret", pii.Secret("token").Reveal(), "token"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Fatalf("Reveal() = %q, want %q", tc.got, tc.want)
			}
		})
	}
}
