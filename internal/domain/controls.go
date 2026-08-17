package domain

import (
	"errors"
	"fmt"
)

// MaxEventsPerTick bounds producer pressure so a typo cannot allocate an
// unbounded batch. M3 raises this once load generation is a measured activity
// rather than an accident.
const MaxEventsPerTick = 1000

// Controls are the knobs a player turns during a run.
//
// M0 exposes only rate and event mix. Worker count, batch size, and retry
// policy are meaningless while the projection folds every record inside the
// tick that appended it, so they arrive when the consumer can actually fall
// behind rather than sitting here as inert fields.
type Controls struct {
	EventsPerTick int `json:"events_per_tick"`
	RainWeight    int `json:"rain_weight"`
	GrowthWeight  int `json:"growth_weight"`
	PestWeight    int `json:"pest_weight"`
}

// ErrInvalidControls wraps every control validation failure.
var ErrInvalidControls = errors.New("invalid controls")

// Validate rejects control values the producer cannot act on. Rejection must be
// consistent: the same invalid input always fails the same way, which is what
// the M0 exit criteria check.
func (c Controls) Validate() error {
	if c.EventsPerTick < 1 {
		return fmt.Errorf("%w: events_per_tick must be at least 1, got %d", ErrInvalidControls, c.EventsPerTick)
	}
	if c.EventsPerTick > MaxEventsPerTick {
		return fmt.Errorf("%w: events_per_tick must not exceed %d, got %d", ErrInvalidControls, MaxEventsPerTick, c.EventsPerTick)
	}
	if c.RainWeight < 0 {
		return fmt.Errorf("%w: rain_weight must not be negative, got %d", ErrInvalidControls, c.RainWeight)
	}
	if c.GrowthWeight < 0 {
		return fmt.Errorf("%w: growth_weight must not be negative, got %d", ErrInvalidControls, c.GrowthWeight)
	}
	if c.PestWeight < 0 {
		return fmt.Errorf("%w: pest_weight must not be negative, got %d", ErrInvalidControls, c.PestWeight)
	}
	if c.TotalWeight() == 0 {
		return fmt.Errorf("%w: at least one event weight must be positive", ErrInvalidControls)
	}
	return nil
}

// TotalWeight returns the sum of the event mix weights.
func (c Controls) TotalWeight() int {
	return c.RainWeight + c.GrowthWeight + c.PestWeight
}

// DefaultControls is a balanced starting mix: enough rain to support growth,
// enough pest pressure to make neglect visible.
func DefaultControls() Controls {
	return Controls{
		EventsPerTick: 6,
		RainWeight:    3,
		GrowthWeight:  2,
		PestWeight:    1,
	}
}
