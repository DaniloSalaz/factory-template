package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/db"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/logx"
	"github.com/DaniloSalaz/factory-template/api/migrations"
)

// migrateCommand applies the migrations embedded in this binary with
// MIGRATIONS_DATABASE_URL (app_owner), so a deployment runs the ones of the version it
// is about to start, before starting it. It never starts the HTTP server and returns the
// process's exit code: non-zero on any failure.
func migrateCommand(ctx context.Context) int {
	cfg, err := config.LoadMigrate()
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).ErrorContext(ctx, "load config", slog.Any("error", err))
		return 1
	}
	logger := logx.New(os.Stdout, cfg.Log).With(slog.String("env", string(cfg.Env)))

	applied, err := db.Migrate(ctx, cfg.URL.Reveal(), migrations.FS())
	if len(applied) > 0 {
		logger.InfoContext(ctx, "migrations applied", slog.Any("versions", applied))
	}
	if err != nil {
		logger.ErrorContext(ctx, "migrate failed", slog.Any("error", err))
		return 1
	}
	if len(applied) == 0 {
		logger.InfoContext(ctx, "no migrations to apply")
	}
	return 0
}
