package main

import (
	"context"
	"log/slog"
	"os"

	appmigrations "chaskicode.dev/saas/app/migrations"
	"chaskicode.dev/saas/platform/kit/config"
	"chaskicode.dev/saas/platform/kit/db"
	"chaskicode.dev/saas/platform/kit/logx"
	platformmigrations "chaskicode.dev/saas/platform/migrations"
)

// migrationSets are the migrations embedded in this binary, in the order they run: the
// platform's first, because the app's may use its tables.
func migrationSets() []db.MigrationSet {
	return []db.MigrationSet{platformmigrations.Set(), appmigrations.Set()}
}

// migrateCommand applies the migrations embedded in this binary with
// MIGRATIONS_DATABASE_URL (app_owner), so a deployment runs the ones of the version it
// is about to start, before starting it. It never starts the HTTP server and returns the
// process's exit code: non-zero on any failure, and the app's set never runs if the
// platform's failed.
func migrateCommand(ctx context.Context) int {
	cfg, err := config.LoadMigrate()
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).ErrorContext(ctx, "load config", slog.Any("error", err))
		return 1
	}
	logger := logx.New(os.Stdout, cfg.Log).With(slog.String("env", string(cfg.Env)), slog.String("version", version()))

	results, err := db.Migrate(ctx, cfg.URL.Reveal(), migrationSets())
	for i, r := range results {
		// The last result of a failed run is the set that failed: "nothing to apply"
		// would be false there, the error below says what happened.
		failed := err != nil && i == len(results)-1
		switch {
		case len(r.Applied) > 0:
			logger.InfoContext(ctx, "migrations applied", slog.String("set", r.Set), slog.Any("versions", r.Applied))
		case !failed:
			logger.InfoContext(ctx, "no migrations to apply", slog.String("set", r.Set))
		}
	}
	if err != nil {
		logger.ErrorContext(ctx, "migrate failed", slog.Any("error", err))
		return 1
	}
	return 0
}
