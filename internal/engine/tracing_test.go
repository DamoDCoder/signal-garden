package engine

import (
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// tracingHarness is newHarness plus an in-memory span exporter, synchronous
// so a span is visible the instant its End() call returns — no batching
// delay to race against in a test.
func tracingHarness(t *testing.T) (*Registry, *ManualClock, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })

	clock := NewManualClock(epoch, 100*time.Millisecond)
	reg := NewRegistry(WithClock(clock), WithTracer(tp))
	t.Cleanup(func() {
		if err := reg.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return reg, clock, exporter
}

// TestTickProducesASpanWithRunAndTickAttributes is the correlation this slice
// exists for: a trace names the run and the tick it belongs to, which a
// Prometheus label deliberately cannot (docs/decisions/0016).
func TestTickProducesASpanWithRunAndTickAttributes(t *testing.T) {
	reg, clock, exporter := tracingHarness(t)
	mustStart(t, reg, baseRequest())

	clock.Tick(3)
	mustGet(t, reg, "run-test") // synchronization barrier — see TestPauseStopsTicksAndResumeContinues

	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("spans = %d, want 3 (one per tick)", len(spans))
	}

	last := spans[len(spans)-1]
	if last.Name != "tick" {
		t.Errorf("span name = %q, want %q", last.Name, "tick")
	}
	attrs := map[string]any{}
	for _, kv := range last.Attributes {
		attrs[string(kv.Key)] = kv.Value.AsInterface()
	}
	if attrs["run.id"] != "run-test" {
		t.Errorf("run.id attribute = %v, want run-test", attrs["run.id"])
	}
	if attrs["tick"] != int64(3) {
		t.Errorf("tick attribute = %v, want 3", attrs["tick"])
	}
}

// TestTickRecordsASnapshotSaveRetryEvent confirms the failure-injection slice
// and this one connect: a tick whose cadence-triggered save retried carries
// that fact as a span event, not a separate span.
func TestTickRecordsASnapshotSaveRetryEvent(t *testing.T) {
	reg, clock, exporter := tracingHarness(t)
	req := baseRequest()
	req.SnapshotEvery = 1
	req.Controls.FailSnapshotEvery = 1 // every save's first attempt fails and retries
	mustStart(t, reg, req)

	clock.Tick(1)
	mustGet(t, reg, "run-test")

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	events := spans[0].Events
	if len(events) != 1 || events[0].Name != "snapshot_save_retried" {
		t.Fatalf("events = %+v, want one snapshot_save_retried event", events)
	}
}

// TestTickWithoutRetryHasNoSnapshotEvent confirms the event is only added
// when something actually happened this tick, not on every save.
func TestTickWithoutRetryHasNoSnapshotEvent(t *testing.T) {
	reg, clock, exporter := tracingHarness(t)
	req := baseRequest()
	req.SnapshotEvery = 1 // saves every tick, but fail_snapshot_every is unset
	mustStart(t, reg, req)

	clock.Tick(1)
	mustGet(t, reg, "run-test")

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if got := len(spans[0].Events); got != 0 {
		t.Errorf("events = %d, want 0 — no retry happened this tick", got)
	}
}
