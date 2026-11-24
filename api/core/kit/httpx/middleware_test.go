package httpx_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"chaskicode.dev/saas/platform/kit/config"
	"chaskicode.dev/saas/platform/kit/httpx"
	"chaskicode.dev/saas/platform/kit/logx"
)

func TestChain_FirstIsOutermost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mws  []string
		want []string
	}{
		{"no middlewares", nil, []string{"handler"}},
		{"one", []string{"a"}, []string{"a>", "handler", "<a"}},
		{"three", []string{"a", "b", "c"}, []string{"a>", "b>", "c>", "handler", "<c", "<b", "<a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var trace []string
			mws := make([]httpx.Middleware, 0, len(tc.mws))
			for _, name := range tc.mws {
				mws = append(mws, func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						trace = append(trace, name+">")
						next.ServeHTTP(w, r)
						trace = append(trace, "<"+name)
					})
				})
			}
			h := httpx.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				trace = append(trace, "handler")
			}), mws...)

			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
			if !slices.Equal(trace, tc.want) {
				t.Fatalf("trace = %v, want %v", trace, tc.want)
			}
		})
	}
}

func newLogger(buf *bytes.Buffer) *slog.Logger {
	return logx.New(buf, config.Log{Level: slog.LevelDebug, Format: config.LogFormatJSON})
}

func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestRequestID(t *testing.T) {
	t.Parallel()

	generated := regexp.MustCompile(`^[0-9a-f]{32}$`)
	tests := []struct {
		name     string
		incoming string
		wantKept bool
	}{
		{"valid id is kept", "abc-DEF_123", true},
		{"64 characters is kept", strings.Repeat("a", 64), true},
		{"missing is generated", "", false},
		{"65 characters is replaced", strings.Repeat("a", 65), false},
		{"invalid characters are replaced", "abc def", false},
		{"log injection is replaced", "abc\ninjected", false},
		{"non ascii is replaced", "añb", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var seen string
			h := httpx.RequestID()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen, _ = logx.RequestIDFrom(r.Context())
			}))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tc.incoming != "" {
				req.Header.Set(httpx.RequestIDHeader, tc.incoming)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			got := rec.Header().Get(httpx.RequestIDHeader)
			if got != seen {
				t.Fatalf("response header %q != context id %q", got, seen)
			}
			if tc.wantKept && got != tc.incoming {
				t.Fatalf("id = %q, want incoming %q", got, tc.incoming)
			}
			if !tc.wantKept && !generated.MatchString(got) {
				t.Fatalf("id = %q, want 32 hex characters", got)
			}
		})
	}
}

func TestAccessLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		target      string
		status      int
		wantLogged  bool
		wantLevel   string
		wantPattern string
	}{
		{"info for 2xx with route pattern", "/users/42?token=secret-query", 200, true, "INFO", "GET /users/{id}"},
		{"info for 4xx", "/users/42", 404, true, "INFO", "GET /users/{id}"},
		{"info for 5xx too", "/users/42", 503, true, "INFO", "GET /users/{id}"},
		{"quiet path 2xx is skipped", "/healthz", 200, false, "", ""},
		{"quiet path failure is logged", "/readyz", 503, true, "INFO", "GET /readyz"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			respond := func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, "hello")
			}
			mux.HandleFunc("GET /users/{id}", respond)
			mux.HandleFunc("GET /healthz", respond)
			mux.HandleFunc("GET /readyz", respond)

			var buf bytes.Buffer
			h := httpx.Chain(mux, httpx.RequestID(), httpx.AccessLog(newLogger(&buf), "/healthz", "/readyz"))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.target, nil))

			lines := logLines(t, &buf)
			if !tc.wantLogged {
				if len(lines) != 0 {
					t.Fatalf("expected no log, got %v", lines)
				}
				return
			}
			if len(lines) != 1 {
				t.Fatalf("want exactly one record, got %d: %v", len(lines), lines)
			}
			line := lines[0]
			if strings.Contains(buf.String(), "secret-query") || strings.Contains(buf.String(), "token=") {
				t.Fatalf("access log contains the query string: %s", buf.String())
			}
			checks := map[string]any{
				"level": tc.wantLevel, "method": "GET", "pattern": tc.wantPattern,
				"status": float64(tc.status), "bytes": float64(5),
			}
			for k, want := range checks {
				if line[k] != want {
					t.Errorf("%s = %v, want %v", k, line[k], want)
				}
			}
			if _, ok := line["duration_ms"].(float64); !ok {
				t.Errorf("duration_ms missing: %v", line)
			}
			if _, ok := line["request_id"].(string); !ok {
				t.Errorf("request_id missing: %v", line)
			}
		})
	}
}

func TestRecover(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int
		wantJSON   bool
	}{
		{
			name:       "panic becomes a 500 problem",
			handler:    func(http.ResponseWriter, *http.Request) { panic("db password is hunter2") },
			wantStatus: 500,
			wantJSON:   true,
		},
		{
			name:       "panic with an error value",
			handler:    func(http.ResponseWriter, *http.Request) { panic(errors.New("db password is hunter2")) },
			wantStatus: 500,
			wantJSON:   true,
		},
		{
			name: "panic after the response started is only logged",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				panic("db password is hunter2")
			},
			wantStatus: 202,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			rec := httptest.NewRecorder()
			httpx.Recover(newLogger(&buf))(tc.handler).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", nil))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if strings.Contains(rec.Body.String(), "hunter2") {
				t.Fatalf("response leaks the panic value: %s", rec.Body.String())
			}
			if tc.wantJSON {
				var p httpx.Problem
				if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != "internal" {
					t.Fatalf("body = %s, want internal problem (err %v)", rec.Body.String(), err)
				}
			}
			logs := buf.String()
			if !strings.Contains(logs, "hunter2") || !strings.Contains(logs, "runtime/debug.Stack") {
				t.Fatalf("log lacks panic value or stack: %s", logs)
			}
		})
	}
}

func TestRecover_RepanicsErrAbortHandler(t *testing.T) {
	t.Parallel()

	h := httpx.Recover(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // comparing the exact panic value.
			t.Fatalf("recovered %v, want http.ErrAbortHandler", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	t.Fatal("ErrAbortHandler was swallowed")
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		handler      http.HandlerFunc
		wantCacheCtl string
	}{
		{"defaults", func(http.ResponseWriter, *http.Request) {}, "no-store"},
		{"handler may override", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Cache-Control", "max-age=60") }, "max-age=60"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			httpx.SecurityHeaders()(tc.handler).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
			h := rec.Header()
			if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" {
				t.Fatalf("missing security headers: %v", h)
			}
			if h.Get("Cache-Control") != tc.wantCacheCtl {
				t.Fatalf("Cache-Control = %q, want %q", h.Get("Cache-Control"), tc.wantCacheCtl)
			}
		})
	}
}

func TestBodyLimit(t *testing.T) {
	t.Parallel()

	const limit = 16
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"under the limit", strings.Repeat("a", limit-1), 200, ""},
		{"exactly the limit", strings.Repeat("a", limit), 200, ""},
		{"over the limit", strings.Repeat("a", limit+1), 413, "request_too_large"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			logger := slog.New(slog.DiscardHandler)
			h := httpx.BodyLimit(limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.ReadAll(r.Body); err != nil {
					httpx.WriteProblem(w, r, fmt.Errorf("read body: %w", err), logger)
					return
				}
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tc.body)))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantCode != "" {
				var p httpx.Problem
				if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != tc.wantCode {
					t.Fatalf("body = %s, want code %q", rec.Body.String(), tc.wantCode)
				}
			}
		})
	}
}

func TestAccessLog_AttrsFromInnerMiddlewares(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		add  bool
	}{
		{"inner middleware adds user_id and tenant_id", true},
		{"nothing added, nothing logged", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Like the session and tenant middlewares: they replace the request with a copy
			// carrying a new context, which the outer AccessLog never sees.
			inner := func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if tc.add {
						logx.AddAccessAttrs(r.Context(), slog.String("user_id", "u1"))
						logx.AddAccessAttrs(r.Context(), slog.String("tenant_id", "t1"))
					}
					next.ServeHTTP(w, r.WithContext(logx.With(r.Context(), slog.String("k", "v"))))
				})
			}
			var buf bytes.Buffer
			h := httpx.Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
				httpx.AccessLog(newLogger(&buf)), inner)
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil))

			lines := logLines(t, &buf)
			if len(lines) != 1 {
				t.Fatalf("want one record, got %v", lines)
			}
			for _, k := range []string{"user_id", "tenant_id"} {
				if _, ok := lines[0][k]; ok != tc.add {
					t.Errorf("%s present = %v, want %v: %v", k, ok, tc.add, lines[0])
				}
			}
		})
	}
}
