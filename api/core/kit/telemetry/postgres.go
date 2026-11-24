package telemetry

import (
	"context"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"chaskicode.dev/saas/core/kit/db"
)

// PgxTracer traces every query (otelpgx): a span named by db.QueryName (the sqlc
// query, the RLS variables' statement or the operation), with the SQL text and its
// placeholders, never the arguments. That includes the statement that sets the RLS
// variables and the SECURITY DEFINER functions. The database's address stays out of the
// attributes.
func (t *Telemetry) PgxTracer() pgx.QueryTracer {
	return otelpgx.NewTracer(
		otelpgx.WithTracerProvider(t.tracers),
		otelpgx.WithMeterProvider(t.meters),
		otelpgx.WithSpanNameCtxFunc(queryName),
		otelpgx.WithMetricOperationName(queryName),
		otelpgx.WithDisableConnectionDetailsInAttributes(),
	)
}

// RecordPoolStats reports the pool's connections in use and idle, and its waits.
func (t *Telemetry) RecordPoolStats(pool *pgxpool.Pool) error {
	if err := otelpgx.RecordStats(pool, otelpgx.WithStatsMeterProvider(t.meters)); err != nil {
		return fmt.Errorf("record pool stats: %w", err)
	}
	return nil
}

// queryName is db.QueryName in the signature otelpgx expects.
func queryName(_ context.Context, sql string) string { return db.QueryName(sql) }
