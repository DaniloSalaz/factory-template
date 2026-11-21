package telemetry_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/telemetry"
)

func settings(c config.Telemetry) telemetry.Settings {
	return telemetry.Settings{Config: c, Env: config.EnvDevelopment, Version: "test", Logger: slog.New(slog.DiscardHandler)}
}

// Without an endpoint nothing is recorded or sent, and shutting down is instant.
func TestNew_WithoutEndpointIsNoop(t *testing.T) {
	t.Parallel()

	tel, err := telemetry.New(t.Context(), settings(config.Telemetry{}))
	if err != nil {
		t.Fatal(err)
	}
	_, span := tel.Tracers().Tracer("t").Start(t.Context(), "x")
	if span.IsRecording() {
		t.Fatal("a span records without an endpoint")
	}
	span.End()
	counter, err := tel.Meters().Meter("t").Int64Counter("c")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(t.Context(), 1)
	if err := tel.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// A collector that does not answer slows nothing down: ending a span returns at once,
// and shutting down gives up at its deadline.
func TestNew_UnreachableCollectorDoesNotBlock(t *testing.T) {
	// Not parallel: the exporters read the standard variables from the environment.
	const dead = "http://127.0.0.1:1"
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", dead)
	tel, err := telemetry.New(t.Context(), settings(config.Telemetry{OTLPEndpoint: dead}))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for range 100 {
		_, span := tel.Tracers().Tracer("t").Start(t.Context(), "x")
		if !span.IsRecording() {
			t.Fatal("spans do not record with an endpoint")
		}
		span.End()
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("100 spans took %v with a dead collector", d)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	start = time.Now()
	_ = tel.Shutdown(ctx) // the export fails; what matters is that it returns
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Shutdown took %v, beyond its deadline", d)
	}
}
