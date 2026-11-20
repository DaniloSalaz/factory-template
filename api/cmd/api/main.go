// Command api is the HTTP API process and the application's composition root.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/health"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/db"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/httpx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/logx"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	cfg, err := config.Load()
	if err != nil {
		// The configured logger needs the config, so this one error uses a plain JSON logger.
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).ErrorContext(ctx, "load config", slog.Any("error", err))
		stop()
		os.Exit(1)
	}
	logger := logx.New(os.Stdout, cfg.Log).With(slog.String("env", string(cfg.Env)))

	if err := run(ctx, cfg, logger); err != nil {
		logger.ErrorContext(ctx, "api stopped with error", slog.Any("error", err))
		stop()
		os.Exit(1)
	}
	stop()
}

// run wires the application: database, module handlers, middlewares and server.
func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.InfoContext(ctx, "config loaded", slog.Any("config", cfg))

	pool, err := db.NewPool(ctx, cfg.DB.URL.Reveal())
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	mux := http.NewServeMux()
	health.New(health.Deps{DB: pool, Logger: logger}).Register(mux)

	handler := httpx.Chain(httpx.WithRouteFallback(mux, logger),
		httpx.RequestID(),
		httpx.AccessLog(logger, "/healthz", "/readyz"),
		httpx.Recover(logger),
		httpx.SecurityHeaders(),
		httpx.BodyLimit(cfg.HTTP.MaxBodyBytes),
	)

	if err := httpx.NewServer(cfg.HTTP, handler, logger).Run(ctx); err != nil {
		return fmt.Errorf("run http server: %w", err)
	}
	return nil
}
