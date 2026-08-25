// Package producer turns accepted controls into deterministic domain events.
//
// Determinism is the whole contract here: given the same seed and the same
// sequence of controls, Tick must emit byte-identical events. Everything that
// varies draws from a seeded source, never from time or map iteration.
//
// A tick's randomness is *derived* rather than carried. Each tick seeds its own
// stream from (seed, tick), so the producer's position is a number the run
// already knows rather than the internal state of a generator. That is what
// makes a restarted run able to carry on producing: there is nothing to write
// down and restore. See docs/decisions/0013.
package producer

import (
	"fmt"
	"math/rand/v2"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/event"
)

// Event magnitude ranges, expressed as [min, max] inclusive.
const (
	rainMin, rainMax = 5, 25
	pestMin, pestMax = 5, 20
)

// Producer emits events for one run.
//
// It holds no generator state. seq is the only thing that accumulates, and it
// is a count rather than a position — recoverable from the run's own log.
type Producer struct {
	runID     string
	seed      int64
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
		seed:      seed,
		organisms: organisms,
	}, nil
}

// stream returns the generator for one tick.
//
// PCG takes two seed words, which is exactly the shape of the problem: the run
// picks one and the tick picks the other, and two ticks of the same run share
// no more than two runs of the same tick do. Folding the tick into a single
// seed would need a mixing step to avoid adjacent ticks producing visibly
// related streams; taking two words means there is nothing to get wrong.
//
// Constructing one per tick is deliberate and cheap. PCG is two words of state,
// so this costs a struct rather than the several-hundred-element table
// math/rand's v1 source seeds.
func (p *Producer) stream(tick int64) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(p.seed), uint64(tick)))
}

// Seq returns how many events this producer has emitted. It is the only state
// a resumed run has to restore, and the run's log already holds it: the last
// record's sequence is this number.
func (p *Producer) Seq() int64 { return p.seq }

// Resume positions the producer after an existing history, so a restarted run
// continues numbering where it stopped rather than reissuing event IDs that
// are already on disk.
func (p *Producer) Resume(seq int64) { p.seq = seq }

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
	rng := p.stream(tick)
	out := make([]event.Event, 0, c.EventsPerTick)
	for i := 0; i < c.EventsPerTick; i++ {
		typ := pickType(rng, c)
		entity := domain.OrganismID(rng.IntN(p.organisms))
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
			Payload:       event.Payload{Amount: amountFor(rng, typ)},
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
func pickType(rng *rand.Rand, c domain.Controls) event.Type {
	roll := rng.IntN(c.TotalWeight())
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
func amountFor(rng *rand.Rand, t event.Type) int {
	switch t {
	case event.TypeRain:
		return rainMin + rng.IntN(rainMax-rainMin+1)
	case event.TypePest:
		return pestMin + rng.IntN(pestMax-pestMin+1)
	default:
		return 0
	}
}
