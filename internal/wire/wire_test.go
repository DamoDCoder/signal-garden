package wire_test

import (
	"testing"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/wire"
)

// TestControlsRoundTrips keeps ControlsFrom and Controls as inverses. A field
// added to one side and forgotten on the other silently drops a control on
// the wire rather than failing loudly, so the round trip is what catches it.
func TestControlsRoundTrips(t *testing.T) {
	want := domain.Controls{
		EventsPerTick: 6,
		RainWeight:    3,
		GrowthWeight:  2,
		PestWeight:    1,
		WorkerCount:   4,
		BatchSize:     5,
	}

	got := wire.ControlsFrom(wire.Controls(want))
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}
