package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the pgx driver for database/sql, which goose uses
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

// MigrationSet is one sequence of goose migrations (NNNNN_name.sql at the root of FS)
// with its own version table, so its numbering is independent of the other sets: the
// platform's and the app's never collide.
type MigrationSet struct {
	Name  string // "platform", "app": in logs and errors
	FS    fs.FS
	Table string // goose version table, e.g. goose_db_version
}

// MigrationResult is what Migrate applied from one set, in order.
type MigrationResult struct {
	Set     string
	Applied []int64
}

// Migrate applies every pending migration of sets, in the order given, with url, which
// must be app_owner's (MIGRATIONS_DATABASE_URL): migrations own the schema, the API's
// app_runtime never runs them. It stops at the first failure, so a later set never runs
// on top of an earlier one that did not finish. It returns one result per set it
// attempted, the failed one included with what it applied before failing.
func Migrate(ctx context.Context, url string, sets []MigrationSet) (results []MigrationResult, err error) {
	if err := validateSets(sets); err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("open migrations connection: %w", err)
	}
	defer func() { err = errors.Join(err, sqlDB.Close()) }()

	for _, set := range sets {
		applied, err := migrateSet(ctx, sqlDB, set)
		results = append(results, MigrationResult{Set: set.Name, Applied: applied})
		if err != nil {
			return results, fmt.Errorf("apply %s migrations: %w", set.Name, err)
		}
	}
	return results, nil
}

func validateSets(sets []MigrationSet) error {
	if len(sets) == 0 {
		return errors.New("migrate: no migration set")
	}
	names := map[string]bool{}
	tables := map[string]bool{}
	for i, s := range sets {
		switch {
		case s.Name == "":
			return fmt.Errorf("migrate: set %d has no name", i)
		case s.Table == "":
			return fmt.Errorf("migrate: set %s has no version table", s.Name)
		case s.FS == nil:
			return fmt.Errorf("migrate: set %s has no file system", s.Name)
		case names[s.Name]:
			return fmt.Errorf("migrate: set name %s is repeated", s.Name)
		case tables[s.Table]:
			// Two sets sharing a table would read each other's versions as their own.
			return fmt.Errorf("migrate: version table %s is used by two sets", s.Table)
		}
		names[s.Name] = true
		tables[s.Table] = true
	}
	return nil
}

func migrateSet(ctx context.Context, sqlDB *sql.DB, set MigrationSet) ([]int64, error) {
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, set.FS,
		goose.WithTableName(set.Table),
		// Only the files of the set count: a Go migration registered globally by some
		// package's init would otherwise be applied with every set.
		goose.WithDisableGlobalRegistry(true),
	)
	if errors.Is(err, goose.ErrNoMigrations) {
		// A set may be empty (the template ships no app migration). goose refuses to
		// build a provider for it, but the version table is still created, so the
		// database looks the same as after a set whose migrations are all applied.
		return nil, ensureVersionTable(ctx, sqlDB, set.Table)
	}
	if err != nil {
		return nil, fmt.Errorf("goose provider: %w", err)
	}
	results, err := provider.Up(ctx)
	// On a failure goose returns no results: what it applied before failing is in the
	// partial error.
	if partial := (*goose.PartialError)(nil); errors.As(err, &partial) {
		results = partial.Applied
	}
	var applied []int64
	for _, r := range results {
		if r.Error == nil {
			applied = append(applied, r.Source.Version)
		}
	}
	if err != nil {
		return applied, fmt.Errorf("up: %w", err)
	}
	return applied, nil
}

// ensureVersionTable creates a goose version table the way goose's provider does
// before its first migration: the table plus the version 0 row, in one transaction.
func ensureVersionTable(ctx context.Context, sqlDB *sql.DB, table string) (err error) {
	store, err := database.NewStore(database.DialectPostgres, table)
	if err != nil {
		return fmt.Errorf("goose store: %w", err)
	}
	ext, ok := store.(database.StoreExtender)
	if !ok {
		return fmt.Errorf("goose store for %s cannot check whether its table exists", table)
	}
	exists, err := ext.TableExists(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("check version table %s: %w", table, err)
	}
	if exists {
		return nil
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if err := store.CreateVersionTable(ctx, tx); err != nil {
		return fmt.Errorf("create version table %s: %w", table, err)
	}
	if err := store.Insert(ctx, tx, database.InsertRequest{Version: 0}); err != nil {
		return fmt.Errorf("insert version 0 in %s: %w", table, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit version table %s: %w", table, err)
	}
	return nil
}
