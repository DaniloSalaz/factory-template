package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"chaskicode.dev/saas/platform/httpx"
	"chaskicode.dev/saas/platform/telemetry"
)

const memberID = "0190a1b2-0000-7000-8000-00000000aaaa"

type instrumented struct {
	handler http.Handler
	spans   *tracetest.InMemoryExporter
	reader  *sdkmetric.ManualReader
}

// newInstrumented is the server's real arrangement: telemetry outermost, RouteSecurity
// inside resolving the ServeMux pattern, and spans leaving through the ADR 0004 filter.
func newInstrumented(t *testing.T) instrumented {
	t.Helper()
	spans := tracetest.NewInMemoryExporter()
	reader := sdkmetric.NewManualReader()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.NewFilteringExporter(spans)))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithView(sdkmetric.NewView(
		sdkmetric.Instrument{Name: "*"}, sdkmetric.Stream{AttributeFilter: telemetry.AttributeFilter()})))
	tel, err := telemetry.NewWith(tp, mp)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	mux.HandleFunc("GET /tenant/members/{userId}", ok)
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("POST /invitations", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	routes := map[string]httpx.Security{"GET /tenant/members/{userId}": {}, "GET /healthz": {}, "POST /invitations": {}}
	h := tel.HTTP(httpx.Chain(mux, httpx.RouteSecurity(mux, routes)), "/healthz", "/readyz")
	return instrumented{handler: h, spans: spans, reader: reader}
}

func (in instrumented) serve(t *testing.T, method, target string, header http.Header) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	for k, vs := range header {
		req.Header[k] = vs
	}
	in.handler.ServeHTTP(httptest.NewRecorder(), req)
}

// The span is named after the ServeMux pattern; the real path, with its user id, and
// the query string appear nowhere.
func TestHTTP_SpanIsNamedAfterThePattern(t *testing.T) {
	t.Parallel()

	in := newInstrumented(t)
	in.serve(t, http.MethodGet, "/tenant/members/"+memberID+"?code=oauth-code-123", nil)

	spans := in.spans.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	s := spans[0]
	if s.Name != "GET /tenant/members/{userId}" {
		t.Errorf("span name = %q, want the pattern", s.Name)
	}
	route := ""
	for _, kv := range s.Attributes {
		if kv.Key == "http.route" {
			route = kv.Value.AsString()
		}
		if v := kv.Value.String(); strings.Contains(v, memberID) || strings.Contains(v, "oauth-code-123") {
			t.Errorf("attribute %s carries the real path or the query: %q", kv.Key, v)
		}
	}
	if route != "/tenant/members/{userId}" {
		t.Errorf("http.route = %q", route)
	}
}

func TestHTTP_ProbesHaveNoSpans(t *testing.T) {
	t.Parallel()

	in := newInstrumented(t)
	in.serve(t, http.MethodGet, "/healthz", nil)
	if n := len(in.spans.GetSpans()); n != 0 {
		t.Fatalf("/healthz produced %d spans", n)
	}
	var rm metricdata.ResourceMetrics
	if err := in.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if strings.HasPrefix(m.Name, "http.server") {
				t.Fatalf("/healthz recorded %s", m.Name)
			}
		}
	}
}

// A W3C traceparent from the caller makes the request's span a child of its trace.
func TestHTTP_PropagatesTraceparent(t *testing.T) {
	t.Parallel()

	in := newInstrumented(t)
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	in.serve(t, http.MethodGet, "/tenant/members/"+memberID, http.Header{
		"Traceparent": {"00-" + traceID + "-00f067aa0ba902b7-01"},
	})
	spans := in.spans.GetSpans()
	if len(spans) != 1 || spans[0].SpanContext.TraceID().String() != traceID {
		t.Fatalf("spans = %+v, want one in trace %s", spans, traceID)
	}
}

// A 429 counts in rate_limit.exceeded, by route.
func TestHTTP_CountsRateLimitedByRoute(t *testing.T) {
	t.Parallel()

	in := newInstrumented(t)
	in.serve(t, http.MethodPost, "/invitations", nil)
	in.serve(t, http.MethodGet, "/tenant/members/"+memberID, nil)

	var rm metricdata.ResourceMetrics
	if err := in.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "rate_limit.exceeded" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("rate_limit.exceeded is a %T", m.Data)
			}
			for _, dp := range sum.DataPoints {
				route, _ := dp.Attributes.Value("http.route")
				if route.AsString() != "/invitations" || dp.Value != 1 || dp.Attributes.Len() != 1 {
					t.Fatalf("data point = %+v", dp)
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("rate_limit.exceeded was not recorded")
	}
}
