package producer

import (
	"testing"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/event"
)

func TestTickIsDeterministic(t *testing.T) {
	c := domain.DefaultControls()

	a, err := New("run-test", 7, 20)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b, err := New("run-test", 7, 20)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for tick := int64(0); tick < 25; tick++ {
		ea, err := a.Tick(tick, c)
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		eb, err := b.Tick(tick, c)
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		if len(ea) != len(eb) {
			t.Fatalf("tick %d produced %d and %d events", tick, len(ea), len(eb))
		}
		for i := range ea {
			if ea[i] != eb[i] {
				t.Fatalf("tick %d event %d diverged:\n a = %+v\n b = %+v", tick, i, ea[i], eb[i])
			}
		}
	}
}

func TestTickEmitsValidEnvelopes(t *testing.T) {
	p, err := New("run-test", 3, 20)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	events, err := p.Tick(0, domain.DefaultControls())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(events) != domain.DefaultControls().EventsPerTick {
		t.Fatalf("produced %d events, want %d", len(events), domain.DefaultControls().EventsPerTick)
	}

	seen := make(map[string]bool)
	for i, e := range events {
		if err := e.Validate(); err != nil {
			t.Errorf("event %d invalid: %v", i, err)
		}
		if seen[e.EventID] {
			t.Errorf("event %d reused event id %q", i, e.EventID)
		}
		seen[e.EventID] = true

		if e.PartitionKey != "run-test" {
			t.Errorf("event %d partition key = %q, want the run id", i, e.PartitionKey)
		}
		if e.Attempt != 1 {
			t.Errorf("event %d attempt = %d, want 1 on first emission", i, e.Attempt)
		}
	}
}

func TestSequenceIsMonotonic(t *testing.T) {
	p, err := New("run-test", 1, 20)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var last int64
	for tick := int64(0); tick < 10; tick++ {
		events, err := p.Tick(tick, domain.DefaultControls())
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		for _, e := range events {
			if e.Sequence <= last {
				t.Fatalf("sequence %d did not advance past %d", e.Sequence, last)
			}
			last = e.Sequence
			if e.OccurredAt != tick {
				t.Errorf("occurred_at = %d, want the tick %d", e.OccurredAt, tick)
			}
		}
	}
}

func TestMixWeightsSelectTypes(t *testing.T) {
	tests := []struct {
		name string
		c    domain.Controls
		want event.Type
	}{
		{"rain only", domain.Controls{EventsPerTick: 50, RainWeight: 1}, event.TypeRain},
		{"growth only", domain.Controls{EventsPerTick: 50, GrowthWeight: 1}, event.TypeGrowth},
		{"pest only", domain.Controls{EventsPerTick: 50, PestWeight: 1}, event.TypePest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New("run-test", 5, 20)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			events, err := p.Tick(0, tc.c)
			if err != nil {
				t.Fatalf("Tick: %v", err)
			}
			for i, e := range events {
				if e.Type != tc.want {
					t.Fatalf("event %d type = %q, want %q", i, e.Type, tc.want)
				}
			}
		})
	}
}

func TestTickRejectsInvalidControls(t *testing.T) {
	p, err := New("run-test", 1, 20)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Tick(0, domain.Controls{EventsPerTick: 0}); err == nil {
		t.Error("Tick accepted invalid controls")
	}
}

func TestNewRejectsBadArguments(t *testing.T) {
	if _, err := New("", 1, 20); err == nil {
		t.Error("New accepted an empty run id")
	}
	if _, err := New("run-test", 1, 0); err == nil {
		t.Error("New accepted a garden with no organisms")
	}
}

func TestGrowthCarriesNoAmount(t *testing.T) {
	p, err := New("run-test", 9, 20)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events, err := p.Tick(0, domain.Controls{EventsPerTick: 20, GrowthWeight: 1})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	for i, e := range events {
		if e.Payload.Amount != 0 {
			t.Errorf("growth event %d carried amount %d; growth magnitude is fixed by the rules", i, e.Payload.Amount)
		}
	}
}
