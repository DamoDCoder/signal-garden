package domain

import "github.com/damodbear/signal-garden/internal/event"

// Simulation rule constants. These are the tuning surface for the M0 feedback
// checkpoint: if the garden is boring or dies instantly, change these before
// changing the architecture.
const (
	// GrowthMoistureCost is the moisture spent to advance one growth stage.
	GrowthMoistureCost = 20
	// GrowthHealthFloor is the health an organism needs before it can grow.
	GrowthHealthFloor = 30
)

// Outcome describes what a rule did, so the projection can report events that
// were applied but had no effect separately from events that were rejected.
type Outcome string

const (
	// OutcomeApplied means the event changed garden state.
	OutcomeApplied Outcome = "applied"
	// OutcomeNoEffect means the event was valid and consumed, but the rules
	// produced no change: rain on a dead organism, growth without moisture.
	OutcomeNoEffect Outcome = "no_effect"
	// OutcomeUnknownEntity means the event named an organism this garden has no
	// record of.
	OutcomeUnknownEntity Outcome = "unknown_entity"
)

// Apply runs the domain rule for e against the garden and reports the outcome.
//
// Apply is deliberately total: it never returns an error and never panics on a
// well-formed event. Envelope validation happens upstream in the processor, so
// this function only expresses simulation rules.
//
// Apply reads e.OccurredAt only through the caller's ordering. It never reads
// e.RecordedAt, because wall-clock time must not influence replayed outcomes.
func (g *Garden) Apply(e event.Event) Outcome {
	if !e.IsOrganismEvent() {
		return OutcomeNoEffect
	}
	i, ok := g.index[e.EntityID]
	if !ok {
		return OutcomeUnknownEntity
	}
	o := &g.organisms[i]

	switch e.Type {
	case event.TypeRain:
		return applyRain(o, e.Payload.Amount)
	case event.TypeGrowth:
		return applyGrowth(o)
	case event.TypePest:
		return applyPest(o, e.Payload.Amount)
	default:
		return OutcomeNoEffect
	}
}

// applyRain adds moisture up to the cap. Dead organisms absorb nothing.
func applyRain(o *Organism, amount int) Outcome {
	if !o.Alive() || amount <= 0 || o.Moisture >= MaxMoisture {
		return OutcomeNoEffect
	}
	o.Moisture = clamp(o.Moisture+amount, 0, MaxMoisture)
	return OutcomeApplied
}

// applyGrowth advances one stage when the organism is healthy enough and has
// banked enough moisture, spending that moisture. This coupling is what makes
// the rain/growth mix a real decision rather than two independent sliders.
func applyGrowth(o *Organism) Outcome {
	if !o.Alive() || o.Stage >= MaxStage {
		return OutcomeNoEffect
	}
	if o.Moisture < GrowthMoistureCost || o.Health < GrowthHealthFloor {
		return OutcomeNoEffect
	}
	o.Moisture -= GrowthMoistureCost
	o.Stage++
	return OutcomeApplied
}

// applyPest removes health, floored at zero. Reaching zero kills the organism;
// its accumulated stage remains as a visible record of what was lost.
func applyPest(o *Organism, amount int) Outcome {
	if !o.Alive() || amount <= 0 {
		return OutcomeNoEffect
	}
	o.Health = clamp(o.Health-amount, 0, MaxHealth)
	return OutcomeApplied
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
