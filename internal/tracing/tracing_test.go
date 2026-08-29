package tracing

import (
	"context"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestEmptyEndpointIsANoop confirms tracing costs nothing by default: no
// endpoint means every span call is safe and inert, and nothing dials
// anywhere during construction or use.
func TestEmptyEndpointIsANoop(t *testing.T) {
	tp, shutdown, err := New(context.Background(), "")
	if err != nil {
		t.Fatalf("New(\"\") error = %v", err)
	}
	if _, ok := tp.(*sdktrace.TracerProvider); ok {
		t.Fatal("New(\"\") returned a real SDK provider, want the noop provider")
	}

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "op")
	span.End() // must not panic

	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown() error = %v, want nil for a noop provider", err)
	}
}

// TestConfiguredEndpointBuildsARealProvider confirms a configured endpoint
// builds the real SDK provider — construction succeeds without a reachable
// collector, since the OTLP/gRPC exporter dials lazily.
func TestConfiguredEndpointBuildsARealProvider(t *testing.T) {
	tp, shutdown, err := New(context.Background(), "127.0.0.1:1") // nothing listens here
	if err != nil {
		t.Fatalf("New(endpoint) error = %v", err)
	}
	// A bounded context, same as the daemon's own shutdown sequence uses —
	// flushing to an unreachable endpoint must give up, not hang the test.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = shutdown(ctx)
	}()

	if _, ok := tp.(*sdktrace.TracerProvider); !ok {
		t.Fatalf("New(endpoint) returned %T, want *sdktrace.TracerProvider", tp)
	}

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "op")
	span.End() // must not panic or block even though nothing is listening
}
