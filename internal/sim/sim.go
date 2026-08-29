// Package sim holds one simulation's moving parts: producer, event log,
// processor, and the garden they act on.
//
// A Sim advances exactly one tick per Step and never decides when to Step. That
// separation is the point: the batch runner in internal/run drives it as fast
// as the CPU allows to prove determinism, and the live engine in
// internal/engine drives it from a clock so a player can watch. Both get the
// same simulation, so a run replayed offline lands where the live run landed.
package sim

import (
	"errors"
	"fmt"
	"time"

	"github.com/DamoDCoder/event-spine/core"
	spinesim "github.com/DamoDCoder/event-spine/sim"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/event"
	"github.com/damodbear/signal-garden/internal/eventlog"
	"github.com/damodbear/signal-garden/internal/metrics"
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

	// SnapshotEvery writes a snapshot and commits the projections group
	// every N ticks. Zero never snapshots, which means a restart folds the
	// run's whole history — correct, and slower the longer the run.
	SnapshotEvery int64

	// MaxTicks and TickInterval are the run's operational parameters. The
	// simulation does not read them — a Sim ticks when it is told to and
	// stops when it is told to — but it writes them into its snapshots,
	// because they are the part of a run that no record mentions and a
	// resumed run would otherwise have to guess at.
	MaxTicks     int64
	TickInterval time.Duration

	// Log is the run's durable event history. A nil Log gets one backed by
	// an in-memory filesystem, which is what the batch runner and the tests
	// want: a real log, real records, and nothing left on disk afterwards.
	//
	// Passing a Log hands over its ownership. Sim closes it, because a run's
	// log lives exactly as long as the run — see docs/decisions/0005.
	Log *eventlog.Log

	// Metrics is where this simulation's processor reports event outcomes. A
	// nil Metrics records nothing — Fold and Rebuild's tail-folding leave it
	// nil deliberately, because refolding recorded history is not new live
	// activity and shouldn't inflate throughput metrics that describe the
	// running system. See docs/decisions/0016.
	Metrics *metrics.Recorder
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
	if c.SnapshotEvery < 0 {
		return fmt.Errorf("snapshot_every must not be negative, got %d", c.SnapshotEvery)
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
	log    *eventlog.Log
	proc   *processor.Processor
	chain  *core.Chain

	tick      int64
	controls  domain.Controls
	revision  int
	pending   []staged
	published int

	// committed is the offset the projections group was last committed to.
	// See Committed for why it is cached rather than read.
	committed int64

	// state is the run's lifecycle, owned by the engine and recorded here so
	// snapshots carry it. A Sim never acts on it.
	state string
}

// New creates a simulation positioned at tick zero.
//
// A Sim owns a log from this point on, so a Sim that is finished with must be
// Closed. Callers that need to see what opening the log recovered pass an
// already-open one in Config; the ephemeral log created here is fresh by
// construction and has nothing to recover.
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

	l := cfg.Log
	if l == nil {
		if l, _, err = eventlog.Open(spinesim.NewFS()); err != nil {
			return nil, fmt.Errorf("open ephemeral log for run %s: %w", cfg.RunID, err)
		}
	}

	// A log handed in may already carry a commit from a previous process, so
	// the cached offset starts where that log left off rather than at zero.
	committed, err := l.Committed()
	if err != nil {
		return nil, fmt.Errorf("read committed offset for run %s: %w", cfg.RunID, err)
	}

	return &Sim{
		cfg:       cfg,
		garden:    garden,
		prod:      prod,
		log:       l,
		proc:      processor.New(garden, cfg.Metrics),
		chain:     core.NewChain(),
		controls:  cfg.Controls,
		committed: committed,
	}, nil
}

// SetState records the run's lifecycle so the next snapshot carries it. The
// engine owns the transitions; the simulation only writes them down.
func (s *Sim) SetState(state string) { s.state = state }

// State returns the lifecycle the last caller recorded.
func (s *Sim) State() string { return s.state }

// Close releases the run's log. It is safe to call more than once.
func (s *Sim) Close() error {
	if s.log == nil {
		return nil
	}
	l := s.log
	s.log = nil
	return l.Close()
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

// Step advances the simulation by exactly one tick: stage control changes,
// produce this tick's events, append them, then fold whatever the projection
// has not seen.
//
// The whole tick is one Append call. The log makes its durability decision once
// per call rather than once per record, so a tick costs one fsync no matter how
// many events it produced — and appending them one at a time would pay that
// cost per event while making exactly the same records durable.
//
// Reading is separate from committing. The only thing that commits is Save,
// which writes a snapshot first: a commit promises those records never need
// delivering again, which is true only once the state built from them is on
// disk. A tick with no snapshot due therefore leaves the group where it was.
func (s *Sim) Step() error {
	batch := make([]event.Event, 0, len(s.pending))
	for _, p := range s.pending {
		batch = append(batch, s.prod.ControlChanged(s.tick, p.revision))
		s.controls = p.controls
	}
	s.pending = nil

	events, err := s.prod.Tick(s.tick, s.controls)
	if err != nil {
		return err
	}
	for i, e := range events {
		batch = append(batch, e)
		if s.cfg.DuplicateEvery > 0 && (i+1)%s.cfg.DuplicateEvery == 0 {
			redelivery := e
			redelivery.Attempt++
			batch = append(batch, redelivery)
		}
	}

	if err := s.log.Append(batch...); err != nil {
		return fmt.Errorf("append at tick %d: %w", s.tick, err)
	}
	s.published += len(batch)

	unprocessed, err := s.log.Unprocessed()
	if err != nil {
		return fmt.Errorf("read at tick %d: %w", s.tick, err)
	}
	if err := s.fold(unprocessed); err != nil {
		return fmt.Errorf("process tick %d: %w", s.tick, err)
	}
	s.tick++

	// Snapshotting after the tick counter moves means the snapshot records
	// the tick it is actually past, and the cursor it is written at is the
	// first record of the next tick.
	if s.cfg.SnapshotEvery > 0 && s.tick%s.cfg.SnapshotEvery == 0 {
		if err := s.Save(); err != nil {
			return err
		}
	}
	return nil
}

// fold applies records to the garden and advances the determinism chain one
// step per record.
//
// The chain is folded here rather than once per tick because a tick is not a
// unit the ordering guarantee is about: two runs that applied the same events in
// a different order within one tick would agree at every tick boundary. Folding
// per record is what makes a reordering visible.
//
// Both halves go in. The event alone would miss a projection that applies an
// event wrongly; the digest alone would miss two different events that happen to
// land on the same state — which is the absorbing case, and this garden is full
// of absorbing states. See docs/decisions/0008.
func (s *Sim) fold(events []event.Event) error {
	var errs []error
	for _, e := range events {
		// Re-encoding the decoded envelope is what the log holds: the
		// codec's encoding is stable and round-trips, so the chain folds
		// the same bytes a replay reads back.
		rec, err := e.ToCore()
		if err != nil {
			return fmt.Errorf("encode %s for the chain: %w", e.EventID, err)
		}
		if r := s.proc.Process(e); r.Err != nil {
			errs = append(errs, r.Err)
		}
		s.chain.Advance(rec, s.garden.Digest())
	}
	return errors.Join(errs...)
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

// Published returns the total events this simulation appended, duplicates
// included.
func (s *Sim) Published() int { return s.published }

// Pending returns the records appended but not yet folded into the garden —
// consumer lag. It is zero between Steps, because the projection drains inside
// the tick.
func (s *Sim) Pending() int {
	if s.log == nil {
		return 0
	}
	return s.log.Pending()
}

// Offset returns the offset the log will assign to the next record, which is
// also how many records this run has appended.
func (s *Sim) Offset() int64 {
	if s.log == nil {
		return 0
	}
	return s.log.Next()
}

// Folded returns the offset of the first record the garden has not folded. It
// is what a projection frame carries, and it is where a client that has seen
// that frame asks to resume from.
func (s *Sim) Folded() int64 {
	if s.log == nil {
		return 0
	}
	return s.log.Read()
}

// Committed returns how far the projections group has durably folded.
//
// It is the offset this Sim last committed rather than a read of the group
// file, because telemetry is a read path with nowhere to put an I/O error, and
// a wrong offset on the wire is worse than no offset. The two agree: Save is
// the only thing that commits, and New seeds this from the log it was handed.
func (s *Sim) Committed() int64 { return s.committed }

// Since returns the records from an offset up to the tail, without moving the
// projection's cursor. It is what a client that fell behind is missing.
func (s *Sim) Since(from int64) ([]event.Event, error) {
	if s.log == nil {
		return nil, nil
	}
	return s.log.Since(from)
}

// Log returns the run's event history. Callers read offsets and commit through
// it; it is not safe to use from another goroutine.
func (s *Sim) Log() *eventlog.Log { return s.log }

// Stats returns the current garden summary.
func (s *Sim) Stats() domain.Stats { return s.garden.Stats() }

// Organisms returns a copy of the garden's organisms.
func (s *Sim) Organisms() []domain.Organism { return s.garden.Organisms() }

// Hash returns the fingerprint of where the garden currently is. It is what
// the snapshot frame carries; determinism is asserted on the chain.
func (s *Sim) Hash() string { return s.garden.Hash() }

// Chain returns the determinism chain digest: every record folded together with
// the projection state it produced.
//
// This is the value replay compares. Two runs that reached the same garden by
// different routes agree on Hash and disagree here, which is the whole reason
// it exists — see docs/decisions/0008.
func (s *Sim) Chain() string { return s.chain.Digest().String() }

// ChainSteps is how many records the chain has folded.
func (s *Sim) ChainSteps() int64 { return s.chain.Steps() }

// Absorbed reports whether the garden has stopped responding to events for the
// last window records.
//
// An absorbed run's digest is evidence about the absorbing state rather than
// about determinism: once every organism is dead, rain adds no moisture and
// pest removes no health, so every history folds to the same place. A
// determinism test that cannot fail is not a test, so the gate fails an
// absorbed run instead of passing it.
func (s *Sim) Absorbed(window int64) bool { return s.chain.Absorbed(window) }

// Fold replays events into a fresh garden and returns it.
//
// This is what a restart does and what the replay command does: the garden is
// not durable state, it is the fold of a history. Duplicates in that history are
// dropped by the processor's idempotency keys, so a log holding redeliveries
// folds to the same garden as one without them.
func Fold(organisms int, events []event.Event) (*domain.Garden, processor.Stats, error) {
	garden, err := domain.NewGarden(organisms)
	if err != nil {
		return nil, processor.Stats{}, err
	}
	proc := processor.New(garden, nil) // replay is a fold of history, not live activity
	if err := proc.ProcessBatch(events); err != nil {
		return nil, proc.Stats(), fmt.Errorf("replay %d events: %w", len(events), err)
	}
	return garden, proc.Stats(), nil
}

// ProcessorStats returns a copy of the processor counters.
func (s *Sim) ProcessorStats() processor.Stats { return s.proc.Stats() }

// Garden returns the projection the processor owns. Read-only by convention:
// only the processor mutates it, per docs/architecture.md.
func (s *Sim) Garden() *domain.Garden { return s.garden }
