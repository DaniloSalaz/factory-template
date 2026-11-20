package domain

import (
	"net/url"
	"strings"
	"unicode"
)

// ResolveReturnTo resolves raw against frontendOrigin with a URL parser and returns the
// absolute URL to redirect to after login, which must have exactly that origin (design,
// 5.1.1). An empty raw means the front end's root.
//
// Backslashes, whitespace and control characters are refused before parsing: browsers
// read "/\evil.com" as "//evil.com" and drop tabs and newlines, so the URL they would
// follow is not the one Go parsed.
func ResolveReturnTo(frontendOrigin, raw string) (string, error) {
	base, err := url.Parse(frontendOrigin)
	if err != nil || base.Host == "" {
		return "", ErrReturnToInvalid()
	}
	if raw == "" {
		raw = "/"
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r == '\\' || unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", ErrReturnToInvalid()
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", ErrReturnToInvalid()
	}
	resolved := base.ResolveReference(ref)
	if !strings.EqualFold(resolved.Scheme, base.Scheme) || !strings.EqualFold(resolved.Host, base.Host) ||
		resolved.User != nil || resolved.Opaque != "" {
		return "", ErrReturnToInvalid()
	}
	return resolved.String(), nil
}
