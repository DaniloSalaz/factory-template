// Package db provides the PostgreSQL connection pool and the only transactions that set
// the row-level security variables (WithTenantTx, WithUserTx and WithAnonTx).
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolOption adjusts the pool's configuration.
type PoolOption func(*pgxpool.Config)

// WithTracer traces every query of the pool with tracer (telemetry.PgxTracer).
func WithTracer(tracer pgx.QueryTracer) PoolOption {
	return func(cfg *pgxpool.Config) { cfg.ConnConfig.Tracer = tracer }
}

// NewPool creates a pgx pool and pings it so the process fails fast when the database is unreachable.
func NewPool(ctx context.Context, url string, opts ...PoolOption) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	for _, opt := range opts {
		opt(cfg)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
