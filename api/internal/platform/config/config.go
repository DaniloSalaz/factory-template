// Package config loads the process configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Config is the typed process configuration.
type Config struct {
	Env             string
	HTTPAddr        string
	LogLevel        slog.Level
	DatabaseURL     string
	ShutdownTimeout time.Duration
}

// LookupFunc matches os.LookupEnv; injecting it keeps Load free of global state and testable.
type LookupFunc func(key string) (string, bool)

// Load builds a Config, reporting every missing or invalid variable at once.
func Load(lookup LookupFunc) (Config, error) {
	var errs []error

	required := func(key string) string {
		v, ok := lookup(key)
		if !ok || strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Errorf("%s is required", key))
		}
		return v
	}
	optional := func(key, fallback string) string {
		if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
			return v
		}
		return fallback
	}

	cfg := Config{
		Env:         required("APP_ENV"),
		DatabaseURL: required("DATABASE_URL"),
		HTTPAddr:    optional("HTTP_ADDR", ":8080"),
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(optional("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	timeout, err := time.ParseDuration(optional("SHUTDOWN_TIMEOUT", "10s"))
	if err != nil {
		errs = append(errs, fmt.Errorf("SHUTDOWN_TIMEOUT: %w", err))
	}
	cfg.ShutdownTimeout = timeout

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}
