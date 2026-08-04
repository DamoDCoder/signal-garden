// Package run orchestrates a simulation: producer to bus to processor, tick by
// tick, with a deterministic result.
//
// At M1 this loop splits across processes and the direct calls become gRPC.
// The Config and Result shapes are the seam that survives that change.
package run

import (
	"fmt"
	"sort"

	"github.com/damodbear/signal-garden/internal/bus"
	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/processor"
	"github.com/damodbear/signal-garden/internal/producer"
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
// introducing it here would make the M0 determinism guarantee depend on
// scheduling rather than on the rules.
func Execute(cfg Config) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, fmt.Errorf("invalid run config: %w", err)
	}

	garden, err := domain.NewGarden(cfg.Organisms)
	if err != nil {
		return Result{}, err
	}
	prod, err := producer.New(cfg.RunID, cfg.Seed, cfg.Organisms)
	if err != nil {
		return Result{}, err
	}
	queue := bus.NewMemory()
	proc := processor.New(garden)

	controls := cfg.Controls
	revision := 0
	published := 0

	for tick := int64(0); tick < cfg.Ticks; tick++ {
		if next, ok := cfg.ControlChanges[tick]; ok {
			controls = next
			revision++
			if err := queue.Publish(prod.ControlChanged(tick, revision)); err != nil {
				return Result{}, fmt.Errorf("publish control change at tick %d: %w", tick, err)
			}
			published++
		}

		events, err := prod.Tick(tick, controls)
		if err != nil {
			return Result{}, err
		}
		for i, e := range events {
			if err := queue.Publish(e); err != nil {
				return Result{}, fmt.Errorf("publish at tick %d: %w", tick, err)
			}
			published++
			if cfg.DuplicateEvery > 0 && (i+1)%cfg.DuplicateEvery == 0 {
				redelivery := e
				redelivery.Attempt++
				if err := queue.Publish(redelivery); err != nil {
					return Result{}, fmt.Errorf("republish at tick %d: %w", tick, err)
				}
				published++
			}
		}

		// Drain and process within the tick. At M2 this becomes a consumer
		// group running independently, and the gap between published and
		// processed becomes lag the player can see.
		if err := proc.ProcessBatch(queue.Drain()); err != nil {
			return Result{}, fmt.Errorf("process tick %d: %w", tick, err)
		}
	}

	return Result{
		Config:     cfg,
		Garden:     garden.Stats(),
		Organisms:  garden.Organisms(),
		Snapshot:   garden.Hash(),
		Processor:  proc.Stats(),
		Published:  published,
		Revisions:  revision,
		FinalCtrls: controls,
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
