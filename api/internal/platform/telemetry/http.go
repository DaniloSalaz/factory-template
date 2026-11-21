package telemetry

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

// RouteOf returns the path of a ServeMux pattern: "GET /me" is "/me".
func RouteOf(pattern string) string {
	if _, path, ok := strings.Cut(pattern, " "); ok {
		return path
	}
	return pattern
}

// Routed names the request's span after its ServeMux pattern ("GET
// /tenant/members/{userId}") and records http.route on the span and on the request's
// HTTP metrics. The real path, with its ids, never becomes a name or an attribute.
// httpx.RouteSecurity calls it once ServeMux has matched the request.
func Routed(ctx context.Context, pattern string) {
	route := routeAttr(pattern)
	span := trace.SpanFromContext(ctx)
	span.SetName(pattern)
	span.SetAttributes(route)
	if l, ok := otelhttp.LabelerFromContext(ctx); ok {
		l.Add(route)
	}
}

// HTTP instruments the server: a span per request, propagated from a W3C traceparent,
// and the standard HTTP metrics, except for quietPaths (the probes), which get neither.
// Until Routed names it, a span is called by its method only. It also counts 429s by
// route (rate_limit.exceeded).
func (t *Telemetry) HTTP(next http.Handler, quietPaths ...string) http.Handler {
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == http.StatusTooManyRequests {
			t.metrics.RateLimited(r.Context(), routeFromLabeler(r.Context()))
		}
	})
	return otelhttp.NewHandler(counted, "http.server",
		otelhttp.WithTracerProvider(t.tracers),
		otelhttp.WithMeterProvider(t.meters),
		otelhttp.WithPropagators(t.propagator),
		otelhttp.WithFilter(func(r *http.Request) bool { return !slices.Contains(quietPaths, r.URL.Path) }),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method }),
	)
}

func routeFromLabeler(ctx context.Context) string {
	l, ok := otelhttp.LabelerFromContext(ctx)
	if !ok {
		return ""
	}
	for _, kv := range l.Get() {
		if kv.Key == "http.route" {
			return kv.Value.AsString()
		}
	}
	return ""
}

// statusRecorder keeps the response status for the 429 counter.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b) //nolint:wrapcheck // a writer decorator stays transparent
}

// Unwrap lets http.ResponseController reach the writer below.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
