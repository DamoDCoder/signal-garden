package domain

import (
	"errors"
	"fmt"
)

// MaxEventsPerTick bounds producer pressure so a typo cannot allocate an
// unbounded batch. M3 raises this once load generation is a measured activity
// rather than an accident.
const MaxEventsPerTick = 1000

// MaxWorkerCount and MaxBatchSize bound the processing capacity controls for
// the same reason MaxEventsPerTick bounds production: a typo should not be
// able to claim an unbounded number.
const (
	MaxWorkerCount = 64
	MaxBatchSize   = MaxEventsPerTick
)

// Controls are the knobs a player turns during a run.
//
// WorkerCount and BatchSize cap how many records one tick folds into the
// garden — worker_count * batch_size — rather than draining everything the
// tick produced. Zero on either means unbounded, the behavior before this
// pair existed. See docs/decisions/0017.
//
// FailSnapshotEvery makes every Nth periodic on-disk snapshot save fail its
// first attempt and retry — deterministic, like DuplicateEvery, not
// probabilistic. See docs/decisions/0018.
type Controls struct {
	EventsPerTick     int `json:"events_per_tick"`
	RainWeight        int `json:"rain_weight"`
	GrowthWeight      int `json:"growth_weight"`
	PestWeight        int `json:"pest_weight"`
	WorkerCount       int `json:"worker_count"`
	BatchSize         int `json:"batch_size"`
	FailSnapshotEvery int `json:"fail_snapshot_every"`
}

// Capacity returns how many records one tick may fold into the garden.
// Zero means unbounded: the tick drains everything Unprocessed returns.
func (c Controls) Capacity() int {
	if c.WorkerCount <= 0 || c.BatchSize <= 0 {
		return 0
	}
	return c.WorkerCount * c.BatchSize
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
	if c.WorkerCount < 0 {
		return fmt.Errorf("%w: worker_count must not be negative, got %d", ErrInvalidControls, c.WorkerCount)
	}
	if c.WorkerCount > MaxWorkerCount {
		return fmt.Errorf("%w: worker_count must not exceed %d, got %d", ErrInvalidControls, MaxWorkerCount, c.WorkerCount)
	}
	if c.BatchSize < 0 {
		return fmt.Errorf("%w: batch_size must not be negative, got %d", ErrInvalidControls, c.BatchSize)
	}
	if c.BatchSize > MaxBatchSize {
		return fmt.Errorf("%w: batch_size must not exceed %d, got %d", ErrInvalidControls, MaxBatchSize, c.BatchSize)
	}
	if c.FailSnapshotEvery < 0 {
		return fmt.Errorf("%w: fail_snapshot_every must not be negative, got %d", ErrInvalidControls, c.FailSnapshotEvery)
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
