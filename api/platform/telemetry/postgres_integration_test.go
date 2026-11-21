package telemetry_test

import (
	"context"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"chaskicode.dev/saas/platform/authctx"
	"chaskicode.dev/saas/platform/db"
	"chaskicode.dev/saas/platform/db/dbtest"
	"chaskicode.dev/saas/platform/telemetry"
)

// Each query is a span named by db.QueryName, with the SQL and its placeholders and
// never the arguments: neither a value of a sqlc query, nor the user the transaction
// sets in its RLS variables, nor the argument of a SECURITY DEFINER function.
func TestPgxTracer_SpansWithoutArguments(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	pg := dbtest.Start(t)
	spans := tracetest.NewInMemoryExporter()
	reader := sdkmetric.NewManualReader()
	// No filtering exporter here: the tracer itself must leave the arguments out, so a
	// tracer that added them fails this test even though the ADR 0004 filter would
	// still drop them (two barriers, each tested on its own).
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	tel, err := telemetry.NewWith(tp, mp)
	if err != nil {
		t.Fatal(err)
	}
	// otelpgx traces queries only inside a recording span: in the API, the request's.
	ctx, parent := tp.Tracer("test").Start(t.Context(), "GET /test")
	defer parent.End()
	pool, err := db.NewPool(ctx, pg.RuntimeURL, db.WithTracer(tel.PgxTracer()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := tel.RecordPoolStats(pool); err != nil {
		t.Fatal(err)
	}
	spans.Reset() // the pool's ping

	const (
		secret = "secret-sql-argument"
		userID = "0190a1b2-0000-7000-8000-00000000aaaa"
	)
	var echoed string
	if err := pool.QueryRow(ctx, "-- name: EchoSecret :one\nSELECT $1::text", secret).Scan(&echoed); err != nil {
		t.Fatal(err)
	}
	// WithUserTx sets the RLS variables with the user, as every flow without a tenant
	// does, and then calls a SECURITY DEFINER function as the identity module does; its
	// refusal here does not matter, its span does.
	user, err := authctx.NewUser(userID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.New(pool).WithUserTx(authctx.WithUser(ctx, user), func(ctx context.Context, q db.Querier) error {
		_, err := q.Exec(ctx, "-- name: AuthMembership :one\nSELECT * FROM auth_membership($1)", userID)
		return err
	})

	names := map[string]string{}
	for _, s := range spans.GetSpans() {
		var text string
		for _, kv := range s.Attributes {
			v := kv.Value.String()
			if strings.Contains(v, secret) || strings.Contains(v, userID) {
				t.Errorf("span %q: attribute %s carries an argument: %q", s.Name, kv.Key, v)
			}
			if kv.Key == "db.query.text" {
				text = v
			}
		}
		names[s.Name] = text
	}
	for name, placeholder := range map[string]string{"EchoSecret": "$1::text", db.RLSVarsQueryName: "$1", "AuthMembership": "auth_membership($1)"} {
		text, ok := names[name]
		if !ok {
			t.Errorf("no span %q among %v", name, names)
			continue
		}
		if !strings.Contains(text, placeholder) {
			t.Errorf("span %q: db.query.text = %q, want the SQL with %s", name, text, placeholder)
		}
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var poolMetrics, all []string
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			all = append(all, m.Name)
			if strings.HasPrefix(m.Name, "pgxpool.") {
				poolMetrics = append(poolMetrics, m.Name)
			}
		}
	}
	if len(poolMetrics) == 0 {
		t.Fatalf("no pool statistics among %v", all)
	}
	t.Logf("pool metrics: %v", poolMetrics)
}
