// Package sim holds one simulation's moving parts: producer, bus, processor,
// and the garden they act on.
//
// A Sim advances exactly one tick per Step and never decides when to Step. That
// separation is the point: the batch runner in internal/run drives it as fast
// as the CPU allows to prove determinism, and the live engine in
// internal/engine drives it from a clock so a player can watch. Both get the
// same simulation, so a run replayed offline lands where the live run landed.
package sim

import (
	"fmt"

	"github.com/damodbear/signal-garden/internal/bus"
	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/processor"
	"github.com/damodbear/signal-garden/internal/producer"
)

// Config describes the fixed properties of a simulation. Everything here is
// chosen once at start; controls are the part that changes during the run.
type Config struct {
	RunID     string
	Seed      int64
	Organisms int
	Controls  domain.Controls

	// DuplicateEvery republishes every Nth event of a tick to exercise
	// idempotent processing. Zero disables duplication.
	DuplicateEvery int
}

// Validate checks the configuration before any state is created.
func (c Config) Validate() error {
	if c.RunID == "" {
		return fmt.Errorf("run id is required")
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
	return nil
}

// staged is a control update accepted but not yet visible to the producer.
type staged struct {
	revision int
	controls domain.Controls
}

// Sim is one run's simulation state.
//
// It is not safe for concurrent use. Callers that share a Sim across goroutines
// must serialize access; internal/engine does that by owning each Sim from a
// single goroutine.
type Sim struct {
	cfg    Config
	garden *domain.Garden
	prod   *producer.Producer
	queue  *bus.Memory
	proc   *processor.Processor

	tick      int64
	controls  domain.Controls
	revision  int
	pending   []staged
	published int
}

// New creates a simulation positioned at tick zero.
func New(cfg Config) (*Sim, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid simulation config: %w", err)
	}
	garden, err := domain.NewGarden(cfg.Organisms)
	if err != nil {
		return nil, err
	}
	prod, err := producer.New(cfg.RunID, cfg.Seed, cfg.Organisms)
	if err != nil {
		return nil, err
	}
	return &Sim{
		cfg:      cfg,
		garden:   garden,
		prod:     prod,
		queue:    bus.NewMemory(),
		proc:     processor.New(garden),
		controls: cfg.Controls,
	}, nil
}

// SetControls accepts a control update and returns its revision number.
//
// The update is staged rather than applied: it takes effect at the start of the
// next Step, alongside the control_changed event that records it. Staging is
// what keeps a live run reproducible — a change accepted partway through a tick
// still lands on a tick boundary, so the tick at which it applied is the only
// timing fact replay needs.
func (s *Sim) SetControls(c domain.Controls) (int, error) {
	if err := c.Validate(); err != nil {
		return 0, err
	}
	s.revision++
	s.pending = append(s.pending, staged{revision: s.revision, controls: c})
	return s.revision, nil
}

// Step advances the simulation by exactly one tick: publish staged control
// changes, produce this tick's events, then drain and process them.
//
// Draining within the tick means published and processed counts stay level. At
// M2 the processor becomes an independent consumer group and the gap between
// those two numbers becomes the lag a player can see.
func (s *Sim) Step() error {
	for _, p := range s.pending {
		if err := s.queue.Publish(s.prod.ControlChanged(s.tick, p.revision)); err != nil {
			return fmt.Errorf("publish control change at tick %d: %w", s.tick, err)
		}
		s.published++
		s.controls = p.controls
	}
	s.pending = nil

	events, err := s.prod.Tick(s.tick, s.controls)
	if err != nil {
		return err
	}
	for i, e := range events {
		if err := s.queue.Publish(e); err != nil {
			return fmt.Errorf("publish at tick %d: %w", s.tick, err)
		}
		s.published++
		if s.cfg.DuplicateEvery > 0 && (i+1)%s.cfg.DuplicateEvery == 0 {
			redelivery := e
			redelivery.Attempt++
			if err := s.queue.Publish(redelivery); err != nil {
				return fmt.Errorf("republish at tick %d: %w", s.tick, err)
			}
			s.published++
		}
	}

	if err := s.proc.ProcessBatch(s.queue.Drain()); err != nil {
		return fmt.Errorf("process tick %d: %w", s.tick, err)
	}
	s.tick++
	return nil
}

// Config returns the fixed configuration of this simulation.
func (s *Sim) Config() Config { return s.cfg }

// Tick returns the number of completed ticks, which is also the index of the
// next tick Step will run.
func (s *Sim) Tick() int64 { return s.tick }

// Controls returns the controls currently visible to the producer. A staged
// update is not reflected here until it applies.
func (s *Sim) Controls() domain.Controls { return s.controls }

// Revision returns the number of control updates accepted so far.
func (s *Sim) Revision() int { return s.revision }

// Published returns the total events published to the bus, duplicates included.
func (s *Sim) Published() int { return s.published }

// Pending returns the events published but not yet processed. It is zero
// between Steps today and becomes consumer lag at M2.
func (s *Sim) Pending() int { return s.queue.Len() }

// Stats returns the current garden summary.
func (s *Sim) Stats() domain.Stats { return s.garden.Stats() }

// Organisms returns a copy of the garden's organisms.
func (s *Sim) Organisms() []domain.Organism { return s.garden.Organisms() }

// Hash returns the stable fingerprint of garden state used by replay tests.
func (s *Sim) Hash() string { return s.garden.Hash() }

// ProcessorStats returns a copy of the processor counters.
func (s *Sim) ProcessorStats() processor.Stats { return s.proc.Stats() }

// Garden returns the projection the processor owns. Read-only by convention:
// only the processor mutates it, per docs/architecture.md.
func (s *Sim) Garden() *domain.Garden { return s.garden }
