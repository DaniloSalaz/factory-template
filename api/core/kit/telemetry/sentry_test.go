package telemetry_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"chaskicode.dev/saas/core/kit/apperr"
	"chaskicode.dev/saas/core/kit/config"
	"chaskicode.dev/saas/core/kit/httpx"
	"chaskicode.dev/saas/core/kit/pii"
	"chaskicode.dev/saas/core/kit/telemetry"
)

// fakeTransport keeps the events instead of sending them.
type fakeTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (f *fakeTransport) Configure(sentry.ClientOptions)        {}
func (f *fakeTransport) Flush(time.Duration) bool              { return true }
func (f *fakeTransport) FlushWithContext(context.Context) bool { return true }
func (f *fakeTransport) Close()                                {}
func (f *fakeTransport) SendEvent(e *sentry.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeTransport) sent() []*sentry.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*sentry.Event(nil), f.events...)
}

func sentryServer(t *testing.T) (http.Handler, *fakeTransport) {
	t.Helper()
	transport := &fakeTransport{}
	tel, err := telemetry.New(t.Context(), telemetry.Settings{
		Config: config.Telemetry{SentryDSN: pii.Secret("https://examplepublickey@o1.ingest.sentry.io/42")},
		Env:    config.EnvStaging, Version: "abc1234", Logger: slog.New(slog.DiscardHandler),
		SentryTransport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom/{id}", func(http.ResponseWriter, *http.Request) {
		panic("boom for ana@example.com")
	})
	mux.HandleFunc("GET /missing", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteProblem(w, r, apperr.NotFound("thing.not_found", "no such thing"), logger)
	})
	mux.HandleFunc("GET /broken", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteProblem(w, r, errors.New("query failed for ana@example.com"), logger)
	})
	routes := map[string]httpx.Security{"GET /boom/{id}": {}, "GET /missing": {}, "GET /broken": {}}
	h := tel.HTTP(httpx.Chain(mux,
		httpx.ReportErrors(tel),
		httpx.Recover(logger),
		httpx.RouteSecurity(mux, routes),
	))
	return h, transport
}

// A panic answers 500 with problem details and reaches Sentry once, with its route and
// without anything of the request or the user.
func TestSentry_PanicIsReported(t *testing.T) {
	t.Parallel()

	h, transport := sentryServer(t)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom/"+memberID+"?code=oauth-code-123", nil)
	req.Header.Set("Cookie", "session=abc123cookie")
	req.Header.Set("Authorization", "Bearer eyJhbGciOi")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") != httpx.ProblemContentType {
		t.Fatalf("response = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != "internal" {
		t.Fatalf("problem = %+v %v", p, err)
	}
	events := transport.sent()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	e := events[0]
	if e.Release != "abc1234" || e.Environment != "staging" {
		t.Errorf("release/environment = %q/%q", e.Release, e.Environment)
	}
	if e.Tags[telemetry.TagRoute] != "/boom/{id}" {
		t.Errorf("tags = %v, want the route", e.Tags)
	}
	if len(e.Exception) == 0 {
		t.Fatal("no exception in the event")
	}
	assertClean(t, "event", fmt.Sprintf("%+v %+v %+v %+v", e, e.Request, e.User, e.Exception))
}

func TestSentry_OnlyUnexpectedErrors(t *testing.T) {
	t.Parallel()

	h, transport := sentryServer(t)
	for _, path := range []string{"/missing", "/broken"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	}
	events := transport.sent()
	if len(events) != 1 {
		t.Fatalf("events = %d, want only the 500", len(events))
	}
	if events[0].Tags[telemetry.TagRoute] != "/broken" {
		t.Errorf("tags = %v", events[0].Tags)
	}
	assertClean(t, "event", fmt.Sprintf("%+v %+v", events[0], events[0].Exception))
}

// Without a DSN, nothing is reported and ReportError is safe.
func TestSentry_WithoutDSN(t *testing.T) {
	t.Parallel()

	tel, err := telemetry.New(t.Context(), settings(config.Telemetry{}))
	if err != nil {
		t.Fatal(err)
	}
	tel.ReportError(t.Context(), errors.New("x"))
	var none *telemetry.Telemetry
	none.ReportError(t.Context(), errors.New("x"))
}
