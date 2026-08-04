// Package producer turns accepted controls into deterministic domain events.
//
// Determinism is the whole contract here: given the same seed and the same
// sequence of controls, Tick must emit byte-identical events. Everything that
// varies draws from the producer's own seeded source, never from time or map
// iteration.
package producer

import (
	"fmt"
	"math/rand"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/event"
)

// Event magnitude ranges, expressed as [min, max] inclusive.
const (
	rainMin, rainMax = 5, 25
	pestMin, pestMax = 5, 20
)

// Producer emits events for one run.
type Producer struct {
	runID     string
	rng       *rand.Rand
	seq       int64
	organisms int
}

// New returns a producer for the given run, seeded deterministically.
func New(runID string, seed int64, organisms int) (*Producer, error) {
	if runID == "" {
		return nil, fmt.Errorf("producer needs a run id")
	}
	if organisms < 1 {
		return nil, fmt.Errorf("producer needs at least 1 organism, got %d", organisms)
	}
	return &Producer{
		runID:     runID,
		rng:       rand.New(rand.NewSource(seed)),
		organisms: organisms,
	}, nil
}

// Tick emits one tick's worth of events under the given controls.
//
// Controls are validated by the caller before they reach the producer; an
// invalid mix here would mean a control change bypassed the control service,
// so Tick treats it as a programming error and returns an error rather than
// silently producing nothing.
func (p *Producer) Tick(tick int64, c domain.Controls) ([]event.Event, error) {
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("producer tick %d: %w", tick, err)
	}
	out := make([]event.Event, 0, c.EventsPerTick)
	for i := 0; i < c.EventsPerTick; i++ {
		typ := p.pickType(c)
		entity := domain.OrganismID(p.rng.Intn(p.organisms))
		p.seq++
		out = append(out, event.Event{
			EventID:       fmt.Sprintf("%s-%08d", p.runID, p.seq),
			Type:          typ,
			SchemaVersion: event.SchemaVersion,
			RunID:         p.runID,
			EntityID:      entity,
			PartitionKey:  p.runID,
			Sequence:      p.seq,
			OccurredAt:    tick,
			Attempt:       1,
			Payload:       event.Payload{Amount: p.amountFor(typ)},
		})
	}
	return out, nil
}

// ControlChanged emits the control-change event for a new revision. The
// processor treats these as idempotent on run plus revision, so a redelivery
// cannot double-apply a control change.
func (p *Producer) ControlChanged(tick int64, revision int) event.Event {
	p.seq++
	return event.Event{
		EventID:       fmt.Sprintf("%s-ctl-%08d", p.runID, p.seq),
		Type:          event.TypeControlChanged,
		SchemaVersion: event.SchemaVersion,
		RunID:         p.runID,
		PartitionKey:  p.runID,
		Sequence:      p.seq,
		OccurredAt:    tick,
		Attempt:       1,
		Payload:       event.Payload{Revision: revision},
	}
}

// pickType selects an event type weighted by the control mix. Weights are
// consumed in a fixed order so the draw is reproducible.
func (p *Producer) pickType(c domain.Controls) event.Type {
	roll := p.rng.Intn(c.TotalWeight())
	if roll < c.RainWeight {
		return event.TypeRain
	}
	if roll < c.RainWeight+c.GrowthWeight {
		return event.TypeGrowth
	}
	return event.TypePest
}

// amountFor returns the magnitude for an event type. Growth carries no
// magnitude: its cost and effect are fixed by the domain rules, so the player
// tunes how often growth is attempted rather than how much each one yields.
func (p *Producer) amountFor(t event.Type) int {
	switch t {
	case event.TypeRain:
		return rainMin + p.rng.Intn(rainMax-rainMin+1)
	case event.TypePest:
		return pestMin + p.rng.Intn(pestMax-pestMin+1)
	default:
		return 0
	}
}
