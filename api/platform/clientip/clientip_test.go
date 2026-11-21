package clientip_test

import (
	"net/http/httptest"
	"net/netip"
	"testing"

	"chaskicode.dev/saas/platform/clientip"
)

func TestResolver_Addr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		header     string
		remoteAddr string
		headers    map[string]string
		want       string // "" means no address
	}{
		{"remote addr without trusted header", "", "203.0.113.7:4321", nil, "203.0.113.7"},
		{"remote addr ipv6", "", "[2001:db8::1]:4321", nil, "2001:db8::1"},
		{"x-forwarded-for is ignored", "", "203.0.113.7:4321", map[string]string{"X-Forwarded-For": "198.51.100.1"}, "203.0.113.7"},
		{"trusted header wins", "CF-Connecting-IP", "10.0.0.1:1", map[string]string{"CF-Connecting-IP": " 198.51.100.9 "}, "198.51.100.9"},
		{"trusted header ignores x-forwarded-for", "CF-Connecting-IP", "10.0.0.1:1", map[string]string{"CF-Connecting-IP": "198.51.100.9", "X-Forwarded-For": "1.1.1.1"}, "198.51.100.9"},
		{"trusted header missing: no address, never remote addr", "CF-Connecting-IP", "10.0.0.1:1", map[string]string{"X-Forwarded-For": "1.1.1.1"}, ""},
		{"trusted header malformed", "CF-Connecting-IP", "10.0.0.1:1", map[string]string{"CF-Connecting-IP": "1.1.1.1, 2.2.2.2"}, ""},
		{"ipv4-mapped is unmapped", "", "[::ffff:192.0.2.1]:80", nil, "192.0.2.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
			req.RemoteAddr = tc.remoteAddr
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			got, ok := clientip.NewResolver(tc.header).Addr(req)
			if tc.want == "" {
				if ok {
					t.Fatalf("Addr = %v, want none", got)
				}
				return
			}
			if !ok || got.String() != tc.want {
				t.Fatalf("Addr = %v (%v), want %s", got, ok, tc.want)
			}
		})
	}
}

func TestPrefix(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"203.0.113.77":           "203.0.113.0/24",
		"::ffff:203.0.113.77":    "203.0.113.0/24",
		"2001:db8:abcd:12::1":    "2001:db8:abcd::/48",
		"fe80::1%eth0":           "fe80::/48",
		"2001:db8:ffff:ffff::ff": "2001:db8:ffff::/48",
	}
	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			got, ok := clientip.Prefix(netip.MustParseAddr(in))
			if !ok || got.String() != want {
				t.Fatalf("Prefix(%s) = %v, want %s", in, got, want)
			}
		})
	}
	if _, ok := clientip.Prefix(netip.Addr{}); ok {
		t.Fatal("Prefix of the zero address must fail")
	}
}

func TestLimitKey(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"203.0.113.77":                "203.0.113.77/32",
		"::ffff:203.0.113.77":         "203.0.113.77/32",
		"2001:db8:abcd:12:1:2:3:4":    "2001:db8:abcd:12::/64",
		"2001:db8:abcd:12:ffff::ffff": "2001:db8:abcd:12::/64",
	}
	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			got, ok := clientip.LimitKey(netip.MustParseAddr(in))
			if !ok || got.String() != want {
				t.Fatalf("LimitKey(%s) = %v, want %s", in, got, want)
			}
		})
	}
}

func TestContext(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	if _, ok := clientip.PrefixFrom(ctx); ok {
		t.Fatal("empty context has a prefix")
	}
	ctx = clientip.WithAddr(ctx, netip.MustParseAddr("198.51.100.200"))
	if p, ok := clientip.PrefixFrom(ctx); !ok || p.String() != "198.51.100.0/24" {
		t.Fatalf("PrefixFrom = %v", p)
	}
}
