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
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := logx.New(os.Stdout, cfg.Log).With(slog.String("env", string(cfg.Env)))
	logger.InfoContext(ctx, "config loaded", slog.Any("config", cfg))

	pool, err := db.NewPool(ctx, cfg.DB.URL.Reveal())
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	mux := http.NewServeMux()
	health.New(health.Deps{DB: pool, Logger: logger}).Register(mux)

	if err := httpx.Run(ctx, logger, httpx.ServerConfig{Addr: cfg.HTTP.Addr(), ShutdownTimeout: cfg.HTTP.ShutdownTimeout}, mux); err != nil {
		return fmt.Errorf("run server: %w", err)
	}
	return nil
}
