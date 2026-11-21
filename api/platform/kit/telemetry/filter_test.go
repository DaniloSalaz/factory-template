package telemetry_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"chaskicode.dev/saas/platform/telemetry"
)

// Values that ADR 0004 forbids; none may appear in what leaves the process.
var forbidden = []string{
	"session=abc123cookie", "Bearer eyJhbGciOi", "code=oauth-code-123", "token=invite-token-456",
	"ana@example.com", "0190a1b2-0000-7000-8000-00000000aaaa", "203.0.113.7", "secret-sql-argument",
}

func assertClean(t *testing.T, what, s string) {
	t.Helper()
	for _, f := range forbidden {
		if strings.Contains(s, f) {
			t.Errorf("%s leaks %q:\n%s", what, f, s)
		}
	}
}

// A span with everything an instrumentation could add keeps only ADR 0004's attributes.
func TestFilteringExporter_KeepsOnlyAllowedAttributes(t *testing.T) {
	t.Parallel()

	mem := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.NewFilteringExporter(mem)))
	_, span := tp.Tracer("test").Start(context.Background(), "GET /tenant/members/{userId}",
		trace.WithAttributes(
			attribute.String("http.request.method", "GET"),
			attribute.String("http.route", "/tenant/members/{userId}"),
			attribute.Int("http.response.status_code", 500),
			attribute.String("tenant_id", "0190a1b2-0000-7000-8000-00000000bbbb"),
			attribute.String("db.query.text", "SELECT 1 WHERE id = $1"),
			// Forbidden.
			attribute.String("url.path", "/tenant/members/0190a1b2-0000-7000-8000-00000000aaaa"),
			attribute.String("url.query", "code=oauth-code-123"),
			attribute.String("url.full", "https://app.example.com/invite?x=1#token=invite-token-456"),
			attribute.String("client.address", "203.0.113.7"),
			attribute.String("user.id", "0190a1b2-0000-7000-8000-00000000aaaa"),
			attribute.String("enduser.id", "ana@example.com"),
			attribute.String("http.request.header.cookie", "session=abc123cookie"),
			attribute.String("http.request.header.authorization", "Bearer eyJhbGciOi"),
			attribute.String("pgx.query.parameters", "secret-sql-argument"),
		))
	span.RecordError(errors.New("user ana@example.com not found"))
	span.SetStatus(codes.Error, "lookup ana@example.com failed")
	span.End()

	spans := mem.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	got := map[attribute.Key]string{}
	for _, kv := range spans[0].Attributes {
		got[kv.Key] = kv.Value.String()
	}
	want := []attribute.Key{"http.request.method", "http.route", "http.response.status_code", "tenant_id", "db.query.text"}
	if len(got) != len(want) {
		t.Errorf("attributes = %v, want exactly %v", got, want)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("allowed attribute %s was dropped", k)
		}
	}
	assertClean(t, "span", fmt.Sprintf("%+v", spans[0]))
	if spans[0].Status.Description != "" {
		t.Errorf("status description = %q, want it dropped (not an error code)", spans[0].Status.Description)
	}
}

func TestFilteringExporter_KeepsAnErrorCodeAsStatus(t *testing.T) {
	t.Parallel()

	mem := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.NewFilteringExporter(mem)))
	_, span := tp.Tracer("test").Start(context.Background(), "x")
	span.SetStatus(codes.Error, "auth.mfa_required")
	span.End()
	if got := mem.GetSpans()[0].Status.Description; got != "auth.mfa_required" {
		t.Fatalf("status description = %q", got)
	}
}

func TestAttributeFilter(t *testing.T) {
	t.Parallel()

	f := telemetry.AttributeFilter()
	for kv, want := range map[attribute.KeyValue]bool{
		attribute.String("http.route", "/me"):         true,
		attribute.String("tenant_id", "t"):            true,
		attribute.String("result", "success"):         true,
		attribute.String("user_id", "u"):              false,
		attribute.String("client.address", "1.2.3.4"): false,
		attribute.String("url.query", "code=x"):       false,
		attribute.String("server.address", "api"):     false,
	} {
		if got := f(kv); got != want {
			t.Errorf("filter(%s) = %v, want %v", kv.Key, got, want)
		}
	}
}

// A Sentry event with a request, a user and personal data in its message comes out of
// BeforeSend without any of them.
func TestScrubEvent(t *testing.T) {
	t.Parallel()

	e := sentry.NewEvent()
	e.Message = "invite to ana@example.com failed at /auth/callback?code=oauth-code-123"
	e.Exception = []sentry.Exception{{Type: "*errors.errorString", Value: "open https://app.example.com/invite#token=invite-token-456"}}
	e.User = sentry.User{ID: "0190a1b2-0000-7000-8000-00000000aaaa", Email: "ana@example.com", IPAddress: "203.0.113.7"}
	e.Request = &sentry.Request{
		Method: "GET", URL: "https://api.example.com/auth/callback?code=oauth-code-123",
		QueryString: "code=oauth-code-123", Cookies: "session=abc123cookie", Data: `{"email":"ana@example.com"}`,
		Headers: map[string]string{"Cookie": "session=abc123cookie", "Authorization": "Bearer eyJhbGciOi", "X-Forwarded-For": "203.0.113.7"},
		Env:     map[string]string{"REMOTE_ADDR": "203.0.113.7"},
	}
	e.Tags = map[string]string{"tenant_id": "t1", "route": "GET /auth/callback", "trace_id": "abc", "user_id": "0190a1b2-0000-7000-8000-00000000aaaa"}
	e.Breadcrumbs = []*sentry.Breadcrumb{{Message: "GET /invite#token=invite-token-456"}}
	e.Contexts["user"] = sentry.Context{"email": "ana@example.com"}

	out := telemetry.NewScrubber().ScrubEvent(e)
	assertClean(t, "event", fmt.Sprintf("%+v %+v %+v %+v", out, out.Request, out.User, out.Exception))
	if out.Request == nil || out.Request.URL != "GET /auth/callback" || out.Request.Method != http.MethodGet {
		t.Errorf("request = %+v, want the route only", out.Request)
	}
	if len(out.Tags) != 3 || out.Tags["user_id"] != "" {
		t.Errorf("tags = %v, want tenant_id, route and trace_id", out.Tags)
	}
	if !strings.Contains(out.Message, "[email]") {
		t.Errorf("message = %q", out.Message)
	}
}

func TestRedactText(t *testing.T) {
	t.Parallel()

	sc := telemetry.NewScrubber()
	for in, want := range map[string]string{
		"to ana@example.com":                  "to [email]",
		"GET /auth/callback?code=abc&state=x": "GET /auth/callback?[REDACTED]",
		"/invite#token=abc":                   "/invite#[REDACTED]",
		"password=hunter2 rest":               "password=[REDACTED] rest",
		"plain error":                         "plain error",
	} {
		if got := sc.RedactText(in); got != want {
			t.Errorf("RedactText(%q) = %q, want %q", in, got, want)
		}
	}
}
