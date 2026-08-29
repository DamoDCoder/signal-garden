package wire_test

import (
	"testing"
	"time"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/engine"
	"github.com/damodbear/signal-garden/internal/wire"
)

// TestControlsRoundTrips keeps ControlsFrom and Controls as inverses. A field
// added to one side and forgotten on the other silently drops a control on
// the wire rather than failing loudly, so the round trip is what catches it.
func TestControlsRoundTrips(t *testing.T) {
	want := domain.Controls{
		EventsPerTick:     6,
		RainWeight:        3,
		GrowthWeight:      2,
		PestWeight:        1,
		WorkerCount:       4,
		BatchSize:         5,
		FailSnapshotEvery: 7,
	}

	got := wire.ControlsFrom(wire.Controls(want))
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// TestTelemetryCarriesSnapshotSaveCounters guards the mechanical field-by-field
// mapping in wire.Telemetry — the same class of bug TestControlsRoundTrips
// catches: a field added to engine.TelemetrySnapshot and forgotten in the
// wire conversion silently drops it instead of failing loudly.
func TestTelemetryCarriesSnapshotSaveCounters(t *testing.T) {
	in := engine.TelemetrySnapshot{
		RunID:                "run-test",
		TickInterval:         time.Second,
		Uptime:               time.Minute,
		SnapshotSaveRetries:  3,
		SnapshotSaveFailures: 1,
	}

	got := wire.Telemetry(in)
	if got.SnapshotSaveRetries != 3 {
		t.Errorf("SnapshotSaveRetries = %d, want 3", got.SnapshotSaveRetries)
	}
	if got.SnapshotSaveFailures != 1 {
		t.Errorf("SnapshotSaveFailures = %d, want 1", got.SnapshotSaveFailures)
	}
}
