package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the pgx driver for database/sql, which goose uses
	"github.com/pressly/goose/v3"
)

// Migrate applies every pending migration of fsys (goose files at its root) with url,
// which must be app_owner's (MIGRATIONS_DATABASE_URL): migrations own the schema, the
// API's app_runtime never runs them. It returns the versions it applied, in order.
func Migrate(ctx context.Context, url string, fsys fs.FS) (applied []int64, err error) {
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("open migrations connection: %w", err)
	}
	defer func() { err = errors.Join(err, sqlDB.Close()) }()

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys)
	if err != nil {
		return nil, fmt.Errorf("goose provider: %w", err)
	}
	results, err := provider.Up(ctx)
	for _, r := range results {
		if r.Error == nil {
			applied = append(applied, r.Source.Version)
		}
	}
	if err != nil {
		return applied, fmt.Errorf("apply migrations: %w", err)
	}
	return applied, nil
}
