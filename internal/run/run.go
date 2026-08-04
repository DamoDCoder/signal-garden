// Package run executes a simulation to completion as fast as the CPU allows
// and returns a deterministic scorecard.
//
// This is the batch path: every tick and every control change is known up
// front, so a run is a pure function of its config. internal/engine is the live
// path, where ticks come from a clock and controls arrive while the run is
// going. Both drive the same internal/sim, and TestEngineMatchesBatchRun pins
// them to the same garden.
package run

import (
	"fmt"
	"sort"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/processor"
	"github.com/damodbear/signal-garden/internal/sim"
)

// Config describes one run.
type Config struct {
	RunID     string
	Seed      int64
	Ticks     int64
	Organisms int
	Controls  domain.Controls

	// ControlChanges applies new controls at the given tick. It is keyed by
	// tick and iterated in sorted tick order, never by map range, so a run
	// stays deterministic.
	ControlChanges map[int64]domain.Controls

	// DuplicateEvery republishes every Nth event to exercise idempotent
	// processing. Zero disables duplication.
	DuplicateEvery int
}

// Validate checks the run configuration before any state is created.
func (c Config) Validate() error {
	if c.RunID == "" {
		return fmt.Errorf("run id is required")
	}
	if c.Ticks < 1 {
		return fmt.Errorf("ticks must be at least 1, got %d", c.Ticks)
	}
	if c.Organisms < 1 {
		return fmt.Errorf("organisms must be at least 1, got %d", c.Organisms)
	}
	if c.DuplicateEvery < 0 {
		return fmt.Errorf("duplicate_every must not be negative, got %d", c.DuplicateEvery)
	}
	if err := c.Controls.Validate(); err != nil {
		return fmt.Errorf("initial controls: %w", err)
	}
	// Iterate in sorted tick order so an invalid config always reports the
	// same tick first, rather than whichever one map iteration happened to
	// reach.
	for _, tick := range sortedTicks(c.ControlChanges) {
		if tick < 0 || tick >= c.Ticks {
			return fmt.Errorf("control change at tick %d is outside the run", tick)
		}
		if err := c.ControlChanges[tick].Validate(); err != nil {
			return fmt.Errorf("control change at tick %d: %w", tick, err)
		}
	}
	return nil
}

// Result is the run scorecard.
type Result struct {
	Config     Config
	Garden     domain.Stats
	Organisms  []domain.Organism
	Snapshot   string
	Processor  processor.Stats
	Published  int
	Revisions  int
	FinalCtrls domain.Controls
}

// Execute runs the simulation to completion and returns the scorecard.
//
// The loop is intentionally synchronous. Concurrency is what M3 measures, and
// introducing it here would make the determinism guarantee depend on scheduling
// rather than on the rules.
func Execute(cfg Config) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, fmt.Errorf("invalid run config: %w", err)
	}

	s, err := sim.New(sim.Config{
		RunID:          cfg.RunID,
		Seed:           cfg.Seed,
		Organisms:      cfg.Organisms,
		Controls:       cfg.Controls,
		DuplicateEvery: cfg.DuplicateEvery,
	})
	if err != nil {
		return Result{}, err
	}

	for tick := int64(0); tick < cfg.Ticks; tick++ {
		if next, ok := cfg.ControlChanges[tick]; ok {
			if _, err := s.SetControls(next); err != nil {
				return Result{}, fmt.Errorf("control change at tick %d: %w", tick, err)
			}
		}
		if err := s.Step(); err != nil {
			return Result{}, err
		}
	}

	return Result{
		Config:     cfg,
		Garden:     s.Stats(),
		Organisms:  s.Organisms(),
		Snapshot:   s.Hash(),
		Processor:  s.ProcessorStats(),
		Published:  s.Published(),
		Revisions:  s.Revision(),
		FinalCtrls: s.Controls(),
	}, nil
}

// sortedTicks returns the control-change ticks in ascending order, so the run
// never depends on Go's randomized map iteration.
func sortedTicks(changes map[int64]domain.Controls) []int64 {
	out := make([]int64, 0, len(changes))
	for tick := range changes {
		out = append(out, tick)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
