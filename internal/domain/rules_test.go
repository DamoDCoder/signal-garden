package domain

import (
	"testing"

	"github.com/damodbear/signal-garden/internal/event"
)

// newTestGarden returns a one-organism garden with the given starting state,
// so rule tests can assert on a single deterministic subject.
func newTestGarden(t *testing.T, moisture, health, stage int) (*Garden, string) {
	t.Helper()
	g, err := NewGarden(1)
	if err != nil {
		t.Fatalf("NewGarden: %v", err)
	}
	id := OrganismID(0)
	i := g.index[id]
	g.organisms[i].Moisture = moisture
	g.organisms[i].Health = health
	g.organisms[i].Stage = stage
	return g, id
}

func organismEvent(t event.Type, entityID string, amount int) event.Event {
	return event.Event{
		EventID:       "evt-1",
		Type:          t,
		SchemaVersion: event.SchemaVersion,
		RunID:         "run-test",
		EntityID:      entityID,
		PartitionKey:  "run-test",
		Sequence:      1,
		Attempt:       1,
		Payload:       event.Payload{Amount: amount},
	}
}

func TestApplyRain(t *testing.T) {
	tests := []struct {
		name         string
		moisture     int
		health       int
		amount       int
		wantMoisture int
		wantOutcome  Outcome
	}{
		{"adds moisture", 10, 100, 15, 25, OutcomeApplied},
		{"caps at max", 95, 100, 20, MaxMoisture, OutcomeApplied},
		{"already saturated", MaxMoisture, 100, 10, MaxMoisture, OutcomeNoEffect},
		{"dead organism absorbs nothing", 0, 0, 20, 0, OutcomeNoEffect},
		{"zero amount", 10, 100, 0, 10, OutcomeNoEffect},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, id := newTestGarden(t, tc.moisture, tc.health, 0)
			got := g.Apply(organismEvent(event.TypeRain, id, tc.amount))
			if got != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", got, tc.wantOutcome)
			}
			o, _ := g.Get(id)
			if o.Moisture != tc.wantMoisture {
				t.Errorf("moisture = %d, want %d", o.Moisture, tc.wantMoisture)
			}
		})
	}
}

func TestApplyGrowth(t *testing.T) {
	tests := []struct {
		name         string
		moisture     int
		health       int
		stage        int
		wantStage    int
		wantMoisture int
		wantOutcome  Outcome
	}{
		{
			name: "grows and spends moisture", moisture: 50, health: 100, stage: 1,
			wantStage: 2, wantMoisture: 30, wantOutcome: OutcomeApplied,
		},
		{
			name: "too dry to grow", moisture: GrowthMoistureCost - 1, health: 100, stage: 1,
			wantStage: 1, wantMoisture: GrowthMoistureCost - 1, wantOutcome: OutcomeNoEffect,
		},
		{
			name: "too unhealthy to grow", moisture: 80, health: GrowthHealthFloor - 1, stage: 1,
			wantStage: 1, wantMoisture: 80, wantOutcome: OutcomeNoEffect,
		},
		{
			name: "already fully grown", moisture: 80, health: 100, stage: MaxStage,
			wantStage: MaxStage, wantMoisture: 80, wantOutcome: OutcomeNoEffect,
		},
		{
			name: "dead organism cannot grow", moisture: 80, health: 0, stage: 2,
			wantStage: 2, wantMoisture: 80, wantOutcome: OutcomeNoEffect,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, id := newTestGarden(t, tc.moisture, tc.health, tc.stage)
			got := g.Apply(organismEvent(event.TypeGrowth, id, 0))
			if got != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", got, tc.wantOutcome)
			}
			o, _ := g.Get(id)
			if o.Stage != tc.wantStage {
				t.Errorf("stage = %d, want %d", o.Stage, tc.wantStage)
			}
			if o.Moisture != tc.wantMoisture {
				t.Errorf("moisture = %d, want %d", o.Moisture, tc.wantMoisture)
			}
		})
	}
}

func TestApplyPest(t *testing.T) {
	tests := []struct {
		name        string
		health      int
		amount      int
		wantHealth  int
		wantAlive   bool
		wantOutcome Outcome
	}{
		{"reduces health", 100, 30, 70, true, OutcomeApplied},
		{"floors at zero and kills", 10, 40, 0, false, OutcomeApplied},
		{"exact lethal blow", 20, 20, 0, false, OutcomeApplied},
		{"already dead", 0, 20, 0, false, OutcomeNoEffect},
		{"zero amount", 50, 0, 50, true, OutcomeNoEffect},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, id := newTestGarden(t, 0, tc.health, 0)
			got := g.Apply(organismEvent(event.TypePest, id, tc.amount))
			if got != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", got, tc.wantOutcome)
			}
			o, _ := g.Get(id)
			if o.Health != tc.wantHealth {
				t.Errorf("health = %d, want %d", o.Health, tc.wantHealth)
			}
			if o.Alive() != tc.wantAlive {
				t.Errorf("alive = %v, want %v", o.Alive(), tc.wantAlive)
			}
		})
	}
}

func TestApplyUnknownEntity(t *testing.T) {
	g, _ := newTestGarden(t, 0, 100, 0)
	got := g.Apply(organismEvent(event.TypeRain, "org-999", 10))
	if got != OutcomeUnknownEntity {
		t.Errorf("outcome = %q, want %q", got, OutcomeUnknownEntity)
	}
}

func TestApplyNonOrganismEventIsInert(t *testing.T) {
	g, _ := newTestGarden(t, 40, 100, 1)
	before := g.Hash()

	e := event.Event{
		EventID:       "evt-ctl",
		Type:          event.TypeControlChanged,
		SchemaVersion: event.SchemaVersion,
		RunID:         "run-test",
		Attempt:       1,
		Payload:       event.Payload{Revision: 1},
	}
	if got := g.Apply(e); got != OutcomeNoEffect {
		t.Errorf("outcome = %q, want %q", got, OutcomeNoEffect)
	}
	if after := g.Hash(); after != before {
		t.Error("control event mutated garden state; it must not")
	}
}

// TestWallClockDoesNotAffectOutcome guards the replay rule in docs/events.md:
// wall-clock timestamps must never influence simulation outcomes.
func TestWallClockDoesNotAffectOutcome(t *testing.T) {
	g1, id1 := newTestGarden(t, 50, 100, 1)
	g2, id2 := newTestGarden(t, 50, 100, 1)

	e1 := organismEvent(event.TypeGrowth, id1, 0)
	e2 := organismEvent(event.TypeGrowth, id2, 0)
	e2.RecordedAt = e2.RecordedAt.AddDate(1, 2, 3)

	g1.Apply(e1)
	g2.Apply(e2)

	if g1.Hash() != g2.Hash() {
		t.Error("recorded_at changed the outcome; simulation must ignore wall-clock time")
	}
}
