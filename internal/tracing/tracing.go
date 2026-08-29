// Package tracing is the daemon's OpenTelemetry trace surface.
//
// Run/event correlation is deliberately not on the Prometheus metrics in
// internal/metrics — a run_id label there would be an unbounded, permanently
// retained series. A trace carries whatever IDs it wants without that cost,
// which is what this package is for. See
// docs/decisions/0016-prometheus-metrics-carry-no-run-id-label.md.
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// New builds this daemon's TracerProvider.
//
// An empty endpoint returns a noop provider: every span call becomes a
// no-op, nothing dials anywhere, and tracing costs nothing — this is the
// default, so a plain task serve behaves exactly as it did before tracing
// existed. A configured endpoint (e.g. "localhost:4317") builds a real
// OTLP/gRPC exporter pointed at whatever is listening there — a local Jaeger
// container, not a collector this repository owns or runs. Sampling is
// AlwaysSample: a local demo tool has no volume problem worth sampling away.
//
// The returned shutdown func flushes pending spans and must be called before
// the process exits.
func New(ctx context.Context, endpoint string) (trace.TracerProvider, func(context.Context) error, error) {
	if endpoint == "" {
		return noop.NewTracerProvider(), func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("build OTLP exporter for %s: %w", endpoint, err)
	}

	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName("signalgardend")))
	if err != nil {
		return nil, nil, fmt.Errorf("build trace resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	return tp, tp.Shutdown, nil
}
