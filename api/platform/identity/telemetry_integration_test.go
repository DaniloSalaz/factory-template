package identity_test

import (
	"context"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"chaskicode.dev/saas/platform/telemetry"
)

// counts returns, per metric of ours, its data points as attribute set -> value.
func counts(t *testing.T, reader *sdkmetric.ManualReader) map[string]map[attribute.Distinct]metricdata.DataPoint[int64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[attribute.Distinct]metricdata.DataPoint[int64]{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			out[m.Name] = map[attribute.Distinct]metricdata.DataPoint[int64]{}
			for _, dp := range sum.DataPoints {
				out[m.Name][dp.Attributes.Equivalent()] = dp
			}
		}
	}
	return out
}

func value(t *testing.T, points map[attribute.Distinct]metricdata.DataPoint[int64], attrs ...attribute.KeyValue) int64 {
	t.Helper()
	set := attribute.NewSet(attrs...)
	return points[set.Equivalent()].Value
}

// The module's counters, through the real stack: tenant_forbidden never names a tenant,
// authz.denied does, and logins, MFA verifications and invitation emails count by
// result. The request's span carries its tenant.
func TestTelemetry_ModuleMetrics(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	reader := sdkmetric.NewManualReader()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.NewFilteringExporter(spans)))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithView(sdkmetric.NewView(
		sdkmetric.Instrument{Name: "*"}, sdkmetric.Stream{AttributeFilter: telemetry.AttributeFilter()})))
	tel, err := telemetry.NewWith(tp, mp)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, options{telemetry: tel})
	s := e.staff()
	other := e.tenant()

	// A tenant the member does not belong to, twice: two refusals, no tenant attribute.
	for range 2 {
		wantProblem(t, s.mb.tenantJSON(other, http.MethodGet, "/tenant", nil), http.StatusForbidden, "auth.tenant_forbidden")
	}
	// A permission the member lacks, in their tenant.
	wantProblem(t, s.mb.tenantJSON(s.tenant, http.MethodPatch, "/tenant/members/"+s.adminID, role("member")), http.StatusForbidden, "authz.denied")
	// An invitation, sent by the capture sender.
	s.ob.invite(s.tenant, e.person().Email, "member")
	// A wrong MFA code.
	_, b := e.signedIn()
	if res := b.verify("0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f0000", "000000"); res.status < 400 {
		t.Fatalf("verify: %d", res.status)
	}

	got := counts(t, reader)
	forbidden := got["auth.tenant_forbidden"]
	if len(forbidden) != 1 || value(t, forbidden) != 2 {
		t.Errorf("auth.tenant_forbidden = %+v, want 2 with no attributes", forbidden)
	}
	if n := value(t, got["authz.denied"], attribute.String("tenant_id", s.tenant)); n != 1 {
		t.Errorf("authz.denied{tenant_id} = %d, want 1 (%+v)", n, got["authz.denied"])
	}
	if n := value(t, got["invitation.email"], attribute.String("result", "success")); n != 1 {
		t.Errorf("invitation.email{success} = %d (%+v)", n, got["invitation.email"])
	}
	if n := value(t, got["auth.login"], attribute.String("result", "success")); n < 1 {
		t.Errorf("auth.login{success} = %d (%+v)", n, got["auth.login"])
	}
	if len(got["mfa.verify"]) == 0 || value(t, got["mfa.verify"], attribute.String("result", "success")) != 0 {
		t.Errorf("mfa.verify = %+v, want a failure by its code", got["mfa.verify"])
	}

	// The span of a request in a tenant carries tenant_id; a refused one does not.
	tenantSpans := 0
	for _, sp := range spans.GetSpans() {
		for _, kv := range sp.Attributes {
			if kv.Key == "tenant_id" {
				tenantSpans++
				if v := kv.Value.AsString(); v == other {
					t.Errorf("span %q carries the refused tenant", sp.Name)
				}
			}
		}
	}
	if tenantSpans == 0 {
		t.Error("no span carries tenant_id")
	}
}
