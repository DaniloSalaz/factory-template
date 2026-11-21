// Package postgres implements the health module's ports on PostgreSQL.
package postgres

import (
	"context"
	"fmt"

	healthdb "chaskicode.dev/saas/gen/sqlc/health"
	"chaskicode.dev/saas/modules/health/app"
)

var _ usecases.DBChecker = (*Checker)(nil)

// Checker implements usecases.DBChecker with a trivial query.
type Checker struct {
	queries healthdb.Querier
}

// NewChecker builds a Checker over any pgx connection or pool.
func NewChecker(db healthdb.DBTX) *Checker {
	return &Checker{queries: healthdb.New(db)}
}

// CheckDB runs a round trip to the database.
func (c *Checker) CheckDB(ctx context.Context) error {
	if _, err := c.queries.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}
