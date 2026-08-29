// Package processor validates, deduplicates, and applies events to the garden
// projection.
//
// The processor is the authority for garden state, per docs/architecture.md.
// Nothing else mutates the projection.
package processor

import (
	"errors"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/event"
	"github.com/damodbear/signal-garden/internal/metrics"
)

// Stats counts what the processor did, and is the raw material for the run
// scorecard and, at M3, the latency and lag panels.
type Stats struct {
	Received      int            `json:"received"`
	Applied       int            `json:"applied"`
	NoEffect      int            `json:"no_effect"`
	Duplicates    int            `json:"duplicates"`
	Rejected      int            `json:"rejected"`
	UnknownEntity int            `json:"unknown_entity"`
	ByType        map[string]int `json:"by_type"`
}

// Result reports the disposition of a single event.
type Result struct {
	Outcome   domain.Outcome
	Duplicate bool
	Err       error
}

// Processor applies events to a garden exactly once each.
type Processor struct {
	garden  *domain.Garden
	applied map[string]struct{}
	stats   Stats
	metrics *metrics.Recorder
}

// New returns a processor that owns the given garden. m may be nil, which
// records nothing — the caller decides whether this processor's activity
// counts toward the daemon's Prometheus metrics (see sim.Config.Metrics).
func New(g *domain.Garden, m *metrics.Recorder) *Processor {
	return &Processor{
		garden:  g,
		applied: make(map[string]struct{}),
		stats:   Stats{ByType: make(map[string]int)},
		metrics: m,
	}
}

// Process handles one event: validate the envelope, drop redeliveries, then
// apply the domain rule.
//
// Duplicates are counted rather than errors, because at-least-once delivery is
// the expected behavior of the transport this replaces, not a fault. That
// distinction is what makes the M2 duplicate-delivery demo a non-event.
func (p *Processor) Process(e event.Event) Result {
	p.stats.Received++

	if err := e.Validate(); err != nil {
		p.stats.Rejected++
		p.metrics.ObserveEvent("rejected")
		return Result{Err: err}
	}

	key := e.IdempotencyKey()
	if _, seen := p.applied[key]; seen {
		p.stats.Duplicates++
		p.metrics.ObserveEvent("duplicate")
		return Result{Duplicate: true}
	}
	p.applied[key] = struct{}{}

	outcome := p.garden.Apply(e)
	p.stats.ByType[string(e.Type)]++

	switch outcome {
	case domain.OutcomeApplied:
		p.stats.Applied++
		p.metrics.ObserveEvent("applied")
	case domain.OutcomeUnknownEntity:
		p.stats.UnknownEntity++
		p.metrics.ObserveEvent("unknown_entity")
	default:
		p.stats.NoEffect++
		p.metrics.ObserveEvent("no_effect")
	}
	return Result{Outcome: outcome}
}

// ProcessBatch runs Process over each event in order and returns the first
// validation error encountered, after processing every event. Callers that
// need per-event dispositions should use Process directly.
func (p *Processor) ProcessBatch(events []event.Event) error {
	var errs []error
	for _, e := range events {
		if r := p.Process(e); r.Err != nil {
			errs = append(errs, r.Err)
		}
	}
	return errors.Join(errs...)
}

// Stats returns a copy of the current counters.
func (p *Processor) Stats() Stats {
	out := p.stats
	out.ByType = make(map[string]int, len(p.stats.ByType))
	for k, v := range p.stats.ByType {
		out.ByType[k] = v
	}
	return out
}

// Restore adopts counters from a run's history, so a resumed run reports what
// it has done rather than what it has done since restarting.
//
// The deduplication table is deliberately not restored, and cannot be: it is
// keyed on every event the run ever applied, and keeping it would mean carrying
// the whole history in memory to protect against a redelivery that cannot
// happen. A redelivery is appended next to the record it repeats, so no
// duplicate pair straddles a restart — the same argument Rebuild relies on when
// it folds a snapshot's tail.
func (p *Processor) Restore(s Stats) {
	p.stats = s
	p.stats.ByType = make(map[string]int, len(s.ByType))
	for k, v := range s.ByType {
		p.stats.ByType[k] = v
	}
}

// Garden returns the projection the processor owns.
func (p *Processor) Garden() *domain.Garden { return p.garden }
