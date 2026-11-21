// Package telemetry sets up traces and metrics (OpenTelemetry, exported by OTLP) and
// error reporting (Sentry), and applies ADR 0004 to everything they send: only the
// attributes it allows leave the process. Without configuration it does nothing.
package telemetry

import (
	"context"
	"regexp"
	"strings"

	"github.com/getsentry/sentry-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Attribute keys of our own (ADR 0004). Everything else comes from the semantic
// conventions.
const (
	// KeyTenantID is the tenant a request acts in: an opaque UUID, never a person.
	KeyTenantID = attribute.Key("tenant_id")
	// KeyResult is the outcome of an operation, from a closed vocabulary (success,
	// invalid_code, …), never free text.
	KeyResult = attribute.Key("result")
	// KeyPeerService names the external service of a client span: workos, smtp.
	KeyPeerService = attribute.Key("peer.service")
)

// allowedKeys is ADR 0004's list: the only span and metric attributes that leave the
// process. Adding one means adding it to the ADR's table, with why it is safe.
func allowedKeys() []attribute.Key {
	return []attribute.Key{
		// HTTP: the route is the ServeMux pattern, never the real path.
		"http.request.method", "http.response.status_code", "http.route", "error.type",
		KeyTenantID, KeyResult, KeyPeerService,
		// Postgres: the query's name and its SQL with placeholders, never its arguments.
		"db.system", "db.system.name", "db.operation.name", "db.query.summary", "db.query.text",
		"db.response.status_code", "pgx.sql_state", "pgx.operation.type",
		// The connection pool's metrics.
		"db.client.connection.pool.name", "db.client.connection.state",
		// Exceptions recorded on spans keep their Go type only (see filterEvents).
		"exception.type",
	}
}

// keySet is a set of attribute keys.
type keySet map[attribute.Key]struct{}

func allowedSet() keySet {
	set := keySet{}
	for _, k := range allowedKeys() {
		set[k] = struct{}{}
	}
	return set
}

// AttributeFilter keeps the attributes of ADR 0004; it filters every metric stream.
func AttributeFilter() attribute.Filter {
	set := allowedSet()
	return func(kv attribute.KeyValue) bool {
		_, ok := set[kv.Key]
		return ok
	}
}

func (set keySet) filter(in []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(in))
	for _, kv := range in {
		if _, ok := set[kv.Key]; ok {
			out = append(out, kv)
		}
	}
	return out
}

// filteringExporter applies ADR 0004 to every span just before it leaves the process,
// whatever instrumentation produced it.
type filteringExporter struct {
	next     sdktrace.SpanExporter
	scrubber *Scrubber
}

// NewFilteringExporter wraps next so that it only receives the attributes ADR 0004
// allows: on spans, their events and their links.
func NewFilteringExporter(next sdktrace.SpanExporter) sdktrace.SpanExporter {
	return &filteringExporter{next: next, scrubber: NewScrubber()}
}

func (e *filteringExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		out[i] = filteredSpan{ReadOnlySpan: s, scrubber: e.scrubber}
	}
	return e.next.ExportSpans(ctx, out) //nolint:wrapcheck // a decorator stays transparent
}

func (e *filteringExporter) Shutdown(ctx context.Context) error {
	return e.next.Shutdown(ctx) //nolint:wrapcheck // a decorator stays transparent
}

// filteredSpan shows a span with only the allowed attributes.
type filteredSpan struct {
	sdktrace.ReadOnlySpan
	scrubber *Scrubber
}

func (s filteredSpan) Attributes() []attribute.KeyValue {
	return s.scrubber.allowed.filter(s.ReadOnlySpan.Attributes())
}

// Events keeps each event's name and time, and of its attributes only the allowed ones:
// an exception event keeps exception.type, never exception.message or its stack.
func (s filteredSpan) Events() []sdktrace.Event {
	events := s.ReadOnlySpan.Events()
	out := make([]sdktrace.Event, len(events))
	for i, ev := range events {
		ev.Attributes = s.scrubber.allowed.filter(ev.Attributes)
		out[i] = ev
	}
	return out
}

func (s filteredSpan) Links() []sdktrace.Link {
	links := s.ReadOnlySpan.Links()
	out := make([]sdktrace.Link, len(links))
	for i, l := range links {
		l.Attributes = s.scrubber.allowed.filter(l.Attributes)
		out[i] = l
	}
	return out
}

// Status keeps the code; the description only if it is an API error code.
func (s filteredSpan) Status() sdktrace.Status {
	st := s.ReadOnlySpan.Status()
	if st.Code == codes.Error && !s.scrubber.errorCode.MatchString(st.Description) {
		st.Description = ""
	}
	return st
}

// Sentry tags of ADR 0004.
const (
	TagTenantID = "tenant_id"
	TagRoute    = "route"
	TagTraceID  = "trace_id"
)

// Scrubber masks personal data and secrets in free text, and applies ADR 0004 to
// Sentry events.
type Scrubber struct {
	email, credential, query *regexp.Regexp
	// errorCode is the shape of an API error code (auth.mfa_required): the only status
	// description a span may keep, since other descriptions are free error text.
	errorCode *regexp.Regexp
	allowed   keySet
}

// NewScrubber compiles the masks: emails, credentials in key=value form, and the query
// string or fragment of a URL.
func NewScrubber() *Scrubber {
	return &Scrubber{
		email:      regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`),
		credential: regexp.MustCompile(`(?i)\b(token|code|password|secret|api_?key|session)=[^\s&"'#]+`),
		query:      regexp.MustCompile(`([?#])[A-Za-z0-9_.~%+-]+=[^\s"']*`),
		errorCode:  regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)*$`),
		allowed:    allowedSet(),
	}
}

// RedactText masks emails, key=value credentials and URL query strings and fragments in
// free text such as an error message.
func (sc *Scrubber) RedactText(s string) string {
	s = sc.email.ReplaceAllString(s, "[email]")
	s = sc.credential.ReplaceAllString(s, "$1=[REDACTED]")
	return sc.query.ReplaceAllString(s, "$1[REDACTED]")
}

// ScrubEvent applies ADR 0004 to a Sentry event: no user, no request headers, cookies,
// body or query string, no breadcrumbs, only the allowed tags and contexts, and messages
// with emails and tokens masked. It is the client's BeforeSend.
func (sc *Scrubber) ScrubEvent(e *sentry.Event) *sentry.Event {
	if e == nil {
		return nil
	}
	e.User = sentry.User{}
	if e.Request != nil {
		e.Request = &sentry.Request{Method: e.Request.Method, URL: withoutQuery(e.Request.URL)}
	}
	if route, ok := e.Tags[TagRoute]; ok {
		e.Transaction = route
		if e.Request != nil {
			e.Request.URL = route
		}
	} else {
		e.Transaction = ""
	}
	e.Breadcrumbs = nil
	tags := make(map[string]string, len(e.Tags))
	for _, k := range []string{TagTenantID, TagRoute, TagTraceID} {
		if v, ok := e.Tags[k]; ok {
			tags[k] = v
		}
	}
	e.Tags = tags
	for k := range e.Contexts {
		if k != "trace" && k != "runtime" && k != "os" {
			delete(e.Contexts, k)
		}
	}
	e.Message = sc.RedactText(e.Message)
	for i := range e.Exception {
		e.Exception[i].Value = sc.RedactText(e.Exception[i].Value)
	}
	return e
}

func withoutQuery(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}
