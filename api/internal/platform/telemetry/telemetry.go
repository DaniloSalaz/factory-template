package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/DaniloSalaz/factory-template/api/internal/platform/config"
)

// ServiceName is the service.name of the API's telemetry.
const ServiceName = "factory-template-api"

// Settings is what New needs: the configuration, and the build and environment that
// go in every span, metric and error report.
type Settings struct {
	Config  config.Telemetry
	Env     config.Environment
	Version string
	Logger  *slog.Logger
}

// Telemetry holds the process's tracer and meter providers. Without an OTLP endpoint
// they are no-ops: instrumented code runs the same and nothing is sent.
type Telemetry struct {
	tracers    trace.TracerProvider
	meters     metric.MeterProvider
	propagator propagation.TextMapPropagator
	metrics    *Metrics
	shutdown   []func(context.Context) error
}

// NewWith builds a Telemetry on the given providers, with no exporters of its own: for
// tests, which pass in-memory SDK providers.
func NewWith(tracers trace.TracerProvider, meters metric.MeterProvider) (*Telemetry, error) {
	m, err := newMetrics(meters)
	if err != nil {
		return nil, err
	}
	return &Telemetry{tracers: tracers, meters: meters, propagator: propagation.TraceContext{}, metrics: m}, nil
}

// New sets up traces and metrics from s. Exporting never blocks a request: spans go out
// in batches and metrics periodically, both in the background, and a collector that
// does not answer only costs failed exports, which are logged.
func New(ctx context.Context, s Settings) (*Telemetry, error) {
	t, err := NewWith(tracenoop.NewTracerProvider(), metricnoop.NewMeterProvider())
	if err != nil {
		return nil, err
	}
	if !s.Config.TracesEnabled() && !s.Config.MetricsEnabled() {
		return t, nil
	}
	// The SDK reports failed exports through this hook, which is global in the OTel API;
	// without it they would go to the standard library's log on stderr.
	// It runs long after New returns, so it keeps ctx's values but not its cancellation.
	logCtx := context.WithoutCancel(ctx)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		s.Logger.WarnContext(logCtx, "telemetry export failed", slog.Any("error", err))
	}))
	res := resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(ServiceName),
		semconv.ServiceVersion(s.Version),
		semconv.DeploymentEnvironmentName(string(s.Env)),
		// The pre-1.27 key, which some backends still group by.
		attribute.String("deployment.environment", string(s.Env)),
	)

	if s.Config.TracesEnabled() {
		// Endpoint, headers and timeout come from OTEL_EXPORTER_OTLP_*; the sampler from
		// OTEL_TRACES_SAMPLER(_ARG), parent-based always-on by default.
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("otlp trace exporter: %w", err)
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(NewFilteringExporter(exp)),
			sdktrace.WithResource(res),
		)
		t.tracers = tp
		t.shutdown = append(t.shutdown, tp.Shutdown)
	}
	if s.Config.MetricsEnabled() {
		exp, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("otlp metric exporter: %w", err), t.Shutdown(ctx))
		}
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)),
			sdkmetric.WithResource(res),
			// ADR 0004 on every instrument, ours and the libraries'.
			sdkmetric.WithView(sdkmetric.NewView(
				sdkmetric.Instrument{Name: "*"},
				sdkmetric.Stream{AttributeFilter: AttributeFilter()},
			)),
		)
		t.meters = mp
		t.shutdown = append(t.shutdown, mp.Shutdown)
		if t.metrics, err = newMetrics(mp); err != nil {
			return nil, errors.Join(err, t.Shutdown(ctx))
		}
	}
	return t, nil
}

// Tracers is the tracer provider to hand to instrumentation; a nil Telemetry gives a
// no-op one, so tests may go without telemetry.
func (t *Telemetry) Tracers() trace.TracerProvider {
	if t == nil {
		return tracenoop.NewTracerProvider()
	}
	return t.tracers
}

// Meters is the meter provider to hand to instrumentation (no-op for a nil Telemetry).
func (t *Telemetry) Meters() metric.MeterProvider {
	if t == nil {
		return metricnoop.NewMeterProvider()
	}
	return t.meters
}

// Metrics are the API's own counters; nil, which records nothing, for a nil Telemetry.
func (t *Telemetry) Metrics() *Metrics {
	if t == nil {
		return nil
	}
	return t.metrics
}

// Propagator reads and writes W3C traceparent.
func (t *Telemetry) Propagator() propagation.TextMapPropagator { return t.propagator }

// Shutdown flushes what is pending and stops the exporters, within ctx's deadline: the
// process must not hang on a collector that does not answer.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for _, f := range t.shutdown {
		if err := f(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	t.shutdown = nil
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("telemetry shutdown: %w", err)
	}
	return nil
}
