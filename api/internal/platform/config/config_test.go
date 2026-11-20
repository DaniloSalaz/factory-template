package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
)

func lookupFrom(env map[string]string) config.LookupFunc {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(lookupFrom(map[string]string{
		"APP_ENV":      "test",
		"DATABASE_URL": "postgres://localhost/db",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.LogLevel != slog.LevelInfo || cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoad_ReportsAllMissingRequired(t *testing.T) {
	t.Parallel()

	_, err := config.Load(lookupFrom(map[string]string{}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, key := range []string{"APP_ENV", "DATABASE_URL"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not mention %s", err, key)
		}
	}
}

func TestLoad_RejectsInvalidValues(t *testing.T) {
	t.Parallel()

	_, err := config.Load(lookupFrom(map[string]string{
		"APP_ENV":          "test",
		"DATABASE_URL":     "postgres://localhost/db",
		"LOG_LEVEL":        "loud",
		"SHUTDOWN_TIMEOUT": "soon",
	}))
	if err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") || !strings.Contains(err.Error(), "SHUTDOWN_TIMEOUT") {
		t.Fatalf("expected LOG_LEVEL and SHUTDOWN_TIMEOUT errors, got %v", err)
	}
}
