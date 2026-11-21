package httpx_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/httpx"
)

func TestRouteSecurity(t *testing.T) {
	t.Parallel()

	table := map[string]httpx.Security{
		"GET /public":            {},
		"GET /me":                {Session: true},
		"PATCH /members/{id}":    {Session: true, Tenant: true},
		"DELETE /never/register": {},
	}
	tests := []struct {
		name, method, target string
		want                 httpx.Security
		wantPattern          string
	}{
		{"declared public", http.MethodGet, "/public", httpx.Security{}, "GET /public"},
		{"declared session only", http.MethodGet, "/me", httpx.Security{Session: true}, "GET /me"},
		{"declared with a wildcard", http.MethodPatch, "/members/42", httpx.Security{Session: true, Tenant: true}, "PATCH /members/{id}"},
		{"registered but undeclared needs everything", http.MethodGet, "/handwritten", httpx.Security{Session: true, Tenant: true}, "GET /handwritten"},
		{"no route needs a session only", http.MethodGet, "/nowhere", httpx.Security{Session: true}, ""},
		{"wrong method needs a session only", http.MethodPost, "/public", httpx.Security{Session: true}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got httpx.Security
			var ok bool
			record := func(_ http.ResponseWriter, r *http.Request) { got, ok = httpx.SecurityFrom(r.Context()) }
			mux := http.NewServeMux()
			for _, p := range []string{"GET /public", "GET /me", "PATCH /members/{id}", "GET /handwritten"} {
				mux.HandleFunc(p, record)
			}
			var buf bytes.Buffer
			h := httpx.Chain(httpx.WithRouteFallback(mux, newLogger(&buf)),
				httpx.AccessLog(newLogger(&buf)),
				httpx.RouteSecurity(mux, table),
				// Like the session middleware: the mux sees a copy of the request.
				func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if s, found := httpx.SecurityFrom(r.Context()); found && tc.wantPattern == "" {
							got, ok = s, true
						}
						next.ServeHTTP(w, r.WithContext(r.Context()))
					})
				})
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), tc.method, tc.target, nil))

			if !ok || got != tc.want {
				t.Fatalf("security = %+v (found %v), want %+v", got, ok, tc.want)
			}
			lines := logLines(t, &buf)
			access := lines[len(lines)-1]
			if tc.wantPattern == "" {
				if _, has := access["pattern"]; has {
					t.Fatalf("unmatched request logged a pattern: %v", access)
				}
				return
			}
			if access["pattern"] != tc.wantPattern {
				t.Fatalf("access pattern = %v, want %s (through a copied request)", access["pattern"], tc.wantPattern)
			}
		})
	}
}

func TestSecurityFrom_Missing(t *testing.T) {
	t.Parallel()

	if _, ok := httpx.SecurityFrom(t.Context()); ok {
		t.Fatal("SecurityFrom without RouteSecurity must report false")
	}
}
