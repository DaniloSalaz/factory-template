package httpx_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/httpx"
)

func TestWithRouteFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
		wantCode   string // empty: the route's own response, not a problem
		wantAllow  string
	}{
		{"matched route is served", http.MethodGet, "/items", 200, "", ""},
		{"HEAD is served by a GET route", http.MethodHead, "/items", 200, "", ""},
		{"unknown path", http.MethodGet, "/no-existe", 404, "route_not_found", ""},
		{"wrong method keeps Allow", http.MethodPost, "/items", 405, "method_not_allowed", "GET, HEAD"},
		{"wrong method with several handlers", http.MethodPut, "/things", 405, "method_not_allowed", "DELETE, GET, HEAD"},
		{"non-canonical path still redirects", http.MethodGet, "/items/../items", 307, "", ""},
		{"handler's own 404 is untouched", http.MethodGet, "/missing-thing", 404, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
			mux.HandleFunc("GET /items", ok)
			mux.HandleFunc("GET /things", ok)
			mux.HandleFunc("DELETE /things", ok)
			mux.HandleFunc("GET /missing-thing", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			})

			h := httpx.Chain(httpx.WithRouteFallback(mux, slog.New(slog.DiscardHandler)), httpx.RequestID())
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tc.method, tc.target, nil))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Allow"); got != tc.wantAllow {
				t.Fatalf("Allow = %q, want %q", got, tc.wantAllow)
			}
			isProblem := rec.Header().Get("Content-Type") == httpx.ProblemContentType
			if tc.wantCode == "" {
				if isProblem {
					t.Fatalf("unexpected problem response: %s", rec.Body.String())
				}
				return
			}
			if !isProblem {
				t.Fatalf("content-type = %q, want problem+json", rec.Header().Get("Content-Type"))
			}
			var p httpx.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if p.Code != tc.wantCode || p.Status != tc.wantStatus || p.RequestID == "" || p.Instance != tc.target {
				t.Fatalf("problem = %+v, want code %q with request_id", p, tc.wantCode)
			}
		})
	}
}
