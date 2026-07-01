// Package tracing wires OpenTelemetry trace export for portal's server and
// worker. The deployment cluster runs Tempo behind the Grafana Agent's OTLP
// receivers, so a request that enqueues a River job that shells out to tofu
// becomes one trace spanning HTTP → job → run instead of three disconnected
// signals (the metrics package's third pillar, finally connected).
//
// Off by default; set OTEL_TRACES_ENABLED=true and point
// OTEL_EXPORTER_OTLP_ENDPOINT at the agent's OTLP receiver to turn it on. The
// exporter reads the standard OTEL_EXPORTER_OTLP_* env vars itself, so this
// package only owns the enable switch + the sample ratio.
package tracing

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/nanohype/portal/internal/config"
)

// Init installs the global W3C trace-context propagator and a TracerProvider.
// When cfg.TracingEnabled is false it installs a never-sampling provider (the
// propagator still flows context, but nothing is exported) so dev / no-collector
// boots clean. serviceName is "portal-server" or "portal-worker". The caller
// owns Shutdown.
func Init(ctx context.Context, serviceName, version string, cfg *config.Config) (*sdktrace.TracerProvider, error) {
	// Always install the propagator so trace context crosses process and job
	// boundaries even when this process isn't exporting.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	if !cfg.TracingEnabled {
		tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
		otel.SetTracerProvider(tp)
		return tp, nil
	}

	// otlptracehttp.New reads OTEL_EXPORTER_OTLP_ENDPOINT / _PROTOCOL / _HEADERS
	// from the environment itself — don't re-parse them here.
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp trace exporter: %w", err)
	}

	// Schemaless so this merges cleanly with resource.Default()'s schema URL.
	// service.name is the attribute Tempo / Grafana key traces on.
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", serviceName),
		attribute.String("service.version", version),
		attribute.String("deployment.environment", cfg.Environment),
	))
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		// Parent-based ratio: honor an upstream sampling decision, otherwise
		// sample a configurable fraction of new roots. A job inherits the
		// request's decision via the propagated parent.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.TracingSampleRatio))),
	)
	otel.SetTracerProvider(tp)
	return tp, nil
}

// Shutdown flushes buffered spans with a bounded timeout. Call on process exit
// AFTER the river client has stopped, with a fresh context (the signal context
// is already cancelled by then) so a clean SIGTERM doesn't drop buffered spans.
func Shutdown(tp *sdktrace.TracerProvider) {
	if tp == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tp.Shutdown(ctx)
}
