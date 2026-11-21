package httpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"chaskicode.dev/saas/platform/kit/clientip"
	"chaskicode.dev/saas/platform/kit/httpx"
	"chaskicode.dev/saas/platform/kit/ratelimit"
)

var allowedOrigins = []string{"https://app.example.com"}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, rec.Body.String())
	}
	return p.Code
}

func TestCSRF(t *testing.T) {
	t.Parallel()

	h := httpx.CSRF(allowedOrigins, slog.New(slog.DiscardHandler))(okHandler())
	tests := []struct {
		name   string
		method string
		origin string
		want   int
	}{
		{"safe method without origin", http.MethodGet, "", http.StatusOK},
		{"post from an allowed origin", http.MethodPost, "https://app.example.com", http.StatusOK},
		{"post without origin", http.MethodPost, "", http.StatusForbidden},
		{"post from another origin", http.MethodPost, "https://evil.example", http.StatusForbidden},
		{"post from a lookalike origin", http.MethodPost, "https://app.example.com.evil.example", http.StatusForbidden},
		{"delete from another origin", http.MethodDelete, "null", http.StatusForbidden},
		{"patch from another origin", http.MethodPatch, "http://app.example.com", http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequestWithContext(t.Context(), tc.method, "/x", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusForbidden && problemCode(t, rec) != "csrf.origin_rejected" {
				t.Fatalf("unexpected problem: %s", rec.Body.String())
			}
		})
	}
}

func TestCORS(t *testing.T) {
	t.Parallel()

	reached := false
	h := httpx.CORS(allowedOrigins)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("allowed origin gets its own origin and credentials", func(t *testing.T) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
		req.Header.Set("Origin", "https://app.example.com")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
			t.Fatalf("allow-origin = %q", got)
		}
		if rec.Header().Get("Access-Control-Allow-Credentials") != "true" || rec.Header().Get("Vary") != "Origin" {
			t.Fatalf("headers = %v", rec.Header())
		}
		// Retry-After is not CORS-safelisted: without this the front end on another
		// origin could not read a 429's wait.
		if got := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "Retry-After") {
			t.Fatalf("expose-headers = %q", got)
		}
	})

	t.Run("other origin gets no CORS headers", func(t *testing.T) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
		req.Header.Set("Origin", "https://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("headers = %v", rec.Header())
		}
	})

	t.Run("preflight is answered without reaching the handler", func(t *testing.T) {
		reached = false
		req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/x", nil)
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent || reached {
			t.Fatalf("status = %d, reached = %v", rec.Code, reached)
		}
		if rec.Header().Get("Access-Control-Allow-Methods") == "" || rec.Header().Get("Access-Control-Allow-Origin") == "*" {
			t.Fatalf("headers = %v", rec.Header())
		}
	})
}

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string) (ratelimit.Decision, error) {
	return ratelimit.Decision{}, errors.New("store down")
}

func TestRateLimit(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	h := httpx.Chain(okHandler(),
		httpx.ClientIP(clientip.NewResolver("")),
		httpx.RateLimit(ratelimit.NewMemory(2, 60e9, nil), logger),
	)
	do := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/login", nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for range 2 {
		if rec := do("198.51.100.1:1000"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	rec := do("198.51.100.1:2000")
	if rec.Code != http.StatusTooManyRequests || problemCode(t, rec) != "rate_limit.exceeded" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// The window is a minute and began a moment ago: a wait of up to 60 s, never 0.
	if s, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || s < 1 || s > 60 {
		t.Fatalf("Retry-After = %q, want 1 to 60 seconds", rec.Header().Get("Retry-After"))
	}
	if rec := do("198.51.100.2:1000"); rec.Code != http.StatusOK {
		t.Fatalf("another IP: status = %d, want 200", rec.Code)
	}

	t.Run("rotating IPv6 addresses inside one /64 shares the quota", func(t *testing.T) {
		for i, remote := range []string{"[2001:db8:1:2::1]:1", "[2001:db8:1:2::ffff]:1"} {
			if rec := do(remote); rec.Code != http.StatusOK {
				t.Fatalf("request %d: status = %d, want 200", i+1, rec.Code)
			}
		}
		if rec := do("[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:1"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("third address of the same /64: status = %d, want 429", rec.Code)
		}
		if rec := do("[2001:db8:1:3::1]:1"); rec.Code != http.StatusOK {
			t.Fatalf("another /64: status = %d, want 200", rec.Code)
		}
	})

	t.Run("IPv4 counts by the address", func(t *testing.T) {
		if rec := do("198.51.100.3:1000"); rec.Code != http.StatusOK {
			t.Fatalf("another IPv4 address in the same /24: status = %d, want 200", rec.Code)
		}
	})

	failing := httpx.RateLimit(failingLimiter{}, logger)(okHandler())
	rec = httptest.NewRecorder()
	failing.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("failing limiter: status = %d, want 503 (fail closed)", rec.Code)
	}
}

func TestClientIP(t *testing.T) {
	t.Parallel()

	var got netip.Addr
	h := httpx.ClientIP(clientip.NewResolver("CF-Connecting-IP"))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = clientip.AddrFrom(r.Context())
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("CF-Connecting-IP", "203.0.113.5")
	req.Header.Set("X-Forwarded-For", "192.0.2.1")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.String() != "203.0.113.5" {
		t.Fatalf("client ip = %v", got)
	}
}
