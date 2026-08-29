package sim

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DamoDCoder/event-spine/core"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/event"
	"github.com/damodbear/signal-garden/internal/eventlog"
	"github.com/damodbear/signal-garden/internal/metrics"
	"github.com/damodbear/signal-garden/internal/processor"
	"github.com/damodbear/signal-garden/internal/producer"
)

// Snapshot is the projection state written into the log, so a restart does not
// have to fold a run's whole history to know where it got to.
//
// It is a shortcut, never a second source of truth: everything here is
// derivable by replaying the records below the snapshot's offset, and
// TestSnapshotMatchesAFullReplay is what keeps that true.
//
// What is deliberately *not* here is the producer's random stream. A garden is
// the fold of a history and restores from one; a producer is a position in a
// seeded sequence, and math/rand exposes no way to write that down. Resuming a
// finished run's projection therefore works, and resuming a *live* run so it
// keeps producing where it left off does not — see the package comment.
type Snapshot struct {
	SchemaVersion int             `json:"schema_version"`
	RunID         string          `json:"run_id"`
	Seed          int64           `json:"seed"`
	Tick          int64           `json:"tick"`
	Revision      int             `json:"revision"`
	Controls      domain.Controls `json:"controls"`

	Organisms []domain.Organism `json:"organisms"`
	Processor processor.Stats   `json:"processor"`
	Published int               `json:"published"`

	// Chain is the determinism digest at the moment of the snapshot. A
	// restore cannot continue the chain — it did not fold those records —
	// but carrying the value means a replay can be checked against the run
	// that wrote it.
	Chain      string `json:"chain"`
	ChainSteps int64  `json:"chain_steps"`

	// State is the run's lifecycle at the moment of the snapshot. It is the
	// only way a restarted daemon can tell a run that finished from one that
	// was interrupted: the log records what a run produced, never what it
	// was doing. A finished run writes a final snapshot, so "finished" here
	// is a fact rather than an inference.
	State string `json:"state"`

	// The operational parameters of the run. They are not derivable from any
	// record — nothing a producer emits mentions how fast the run was paced
	// or when it was due to stop — so a resumed run would otherwise silently
	// adopt the defaults instead of continuing as itself.
	MaxTicks       int64         `json:"max_ticks"`
	TickInterval   time.Duration `json:"tick_interval"`
	DuplicateEvery int           `json:"duplicate_every"`
}

// SnapshotSchemaVersion is bumped when a snapshot's fields change
// incompatibly. A snapshot from an unknown version is refused rather than
// guessed at: the records are still there, and folding them is always correct.
//
// Version 2 added the run's lifecycle state and its operational parameters, so
// a restarted daemon can resume a run as itself rather than as a fresh run that
// happens to share its garden.
const SnapshotSchemaVersion = 2

// SnapshotState captures the current projection.
func (s *Sim) SnapshotState() Snapshot {
	return Snapshot{
		SchemaVersion: SnapshotSchemaVersion,
		RunID:         s.cfg.RunID,
		Seed:          s.cfg.Seed,
		Tick:          s.tick,
		Revision:      s.revision,
		Controls:      s.controls,
		Organisms:     s.garden.Organisms(),
		Processor:     s.proc.Stats(),
		Published:     s.published,
		Chain:         s.Chain(),
		ChainSteps:    s.ChainSteps(),

		State:          s.state,
		MaxTicks:       s.cfg.MaxTicks,
		TickInterval:   s.cfg.TickInterval,
		DuplicateEvery: s.cfg.DuplicateEvery,
	}
}

// Save writes a snapshot and commits the projections group to the same offset.
//
// This is the first thing in the system that commits, and it is why nothing
// committed before it: a commit promises the records below it never need
// delivering again, which is only true once the state built from them is on
// disk.
func (s *Sim) Save() error {
	state, err := json.Marshal(s.SnapshotState())
	if err != nil {
		return fmt.Errorf("encode snapshot for run %s: %w", s.cfg.RunID, err)
	}
	at := s.log.Read()
	if err := s.log.Save(state); err != nil {
		return fmt.Errorf("save run %s at tick %d: %w", s.cfg.RunID, s.tick, err)
	}
	s.committed = at
	return nil
}

// Rebuild reconstructs a run's projection from its log: the newest snapshot,
// then every record after it.
//
// This is what a restart does and what the replay command does. With no
// snapshot it folds the whole history, which is the same answer more slowly —
// so a snapshot can always be deleted, and never has to be trusted more than
// the records.
func Rebuild(l *eventlog.Log) (*domain.Garden, Snapshot, error) {
	state, offset, tail, err := l.Restore()
	if err != nil {
		return nil, Snapshot{}, err
	}

	var (
		snapshot Snapshot
		garden   *domain.Garden
	)
	if state == nil {
		// No snapshot: the organism count comes from the records
		// themselves, because a garden that never folded anything has no
		// other way to know how big it is.
		garden, snapshot.Processor, err = Fold(organismCount(tail), tail)
		if err != nil {
			return nil, Snapshot{}, err
		}
		return garden, snapshot, nil
	}

	if err := json.Unmarshal(state, &snapshot); err != nil {
		return nil, Snapshot{}, fmt.Errorf("decode snapshot at %d: %w", offset, err)
	}
	if snapshot.SchemaVersion != SnapshotSchemaVersion {
		return nil, Snapshot{}, fmt.Errorf("snapshot at %d is schema version %d, this build writes %d; delete it and replay the records",
			offset, snapshot.SchemaVersion, SnapshotSchemaVersion)
	}

	garden, err = domain.Restore(snapshot.Organisms)
	if err != nil {
		return nil, Snapshot{}, fmt.Errorf("restore snapshot at %d: %w", offset, err)
	}

	// The tail is folded through a processor with an empty deduplication
	// table. That is sound here because a redelivery is appended next to the
	// record it repeats, so no duplicate pair straddles a snapshot — and a
	// snapshot is only ever taken at a tick boundary.
	proc := processor.New(garden, nil) // rebuild is a fold of history, not live activity
	if err := proc.ProcessBatch(tail); err != nil {
		return nil, snapshot, fmt.Errorf("fold %d records after the snapshot at %d: %w", len(tail), offset, err)
	}

	snapshot.Tick += ticksIn(tail)
	snapshot.Published += len(tail)
	snapshot.Processor = merge(snapshot.Processor, proc.Stats())
	return garden, snapshot, nil
}

// organismCount infers garden size from a history, for the case where there is
// no snapshot to read it from.
//
// Organism IDs are zero-padded positions, so the highest one a history mentions
// names the last organism. An organism that no event ever touched is invisible
// this way, which is why a snapshot carries the count and this is the fallback.
func organismCount(events []event.Event) int {
	highest := 0
	for _, e := range events {
		if !e.IsOrganismEvent() {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(e.EntityID, "org-"))
		if err != nil {
			continue
		}
		if n+1 > highest {
			highest = n + 1
		}
	}
	if highest == 0 {
		return 1
	}
	return highest
}

// ticksIn counts the tick boundaries a run of records crosses.
func ticksIn(events []event.Event) int64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].OccurredAt - events[0].OccurredAt + 1
}

func merge(a, b processor.Stats) processor.Stats {
	out := processor.Stats{
		Received:      a.Received + b.Received,
		Applied:       a.Applied + b.Applied,
		NoEffect:      a.NoEffect + b.NoEffect,
		Duplicates:    a.Duplicates + b.Duplicates,
		Rejected:      a.Rejected + b.Rejected,
		UnknownEntity: a.UnknownEntity + b.UnknownEntity,
		ByType:        make(map[string]int, len(a.ByType)+len(b.ByType)),
	}
	for k, v := range a.ByType {
		out.ByType[k] += v
	}
	for k, v := range b.ByType {
		out.ByType[k] += v
	}
	return out
}

// Resume reconstructs a whole simulation from a run's log, ready to keep
// producing.
//
// Rebuild answers "what garden did this run reach"; this answers "what was this
// run, and where was it". The difference is everything a garden is not: the
// producer's position, the controls in force, the pace, and the lifecycle.
//
// The producer's position needs no stored field. Its randomness is derived from
// (seed, tick) — see docs/decisions/0013 — and its one accumulating value is a
// count of events, which the last record in the log already carries.
//
// The determinism chain does not carry over. A resumed Sim did not fold the
// records below its snapshot, so it cannot continue a chain that describes
// them; it starts a fresh one and the snapshot keeps the old digest for a
// replay to check against. A resumed run is therefore not a chain-comparable
// continuation of the run it resumes, which is why replay verification uses the
// log rather than a live run.
func Resume(runID string, l *eventlog.Log, m *metrics.Recorder) (*Sim, Snapshot, error) {
	if l == nil {
		return nil, Snapshot{}, fmt.Errorf("resume run %s: no log", runID)
	}

	garden, snapshot, err := Rebuild(l)
	if err != nil {
		return nil, Snapshot{}, fmt.Errorf("resume run %s: %w", runID, err)
	}

	// Rebuild folded everything the log holds, so the consumer cursor has to
	// say so. It opens at the last commit, which trails the tail by up to a
	// snapshot's worth of ticks; leaving it there would redeliver records the
	// garden already has into a processor whose deduplication table a restart
	// emptied, and apply them twice.
	if err := l.MarkFolded(); err != nil {
		return nil, snapshot, fmt.Errorf("resume run %s: %w", runID, err)
	}
	if snapshot.RunID == "" {
		// A history with no snapshot yet: the garden folded, but nothing
		// recorded what the run was. Seed and controls are unknowable
		// from records alone, so resuming would invent a different run.
		return nil, snapshot, fmt.Errorf("resume run %s: %w", runID, ErrNoRunState)
	}

	prod, err := producer.New(snapshot.RunID, snapshot.Seed, len(snapshot.Organisms))
	if err != nil {
		return nil, snapshot, fmt.Errorf("resume run %s: %w", runID, err)
	}
	last, ok, err := l.Last()
	if err != nil {
		return nil, snapshot, fmt.Errorf("resume run %s: %w", runID, err)
	}
	if ok {
		prod.Resume(last.Sequence)
	}

	committed, err := l.Committed()
	if err != nil {
		return nil, snapshot, fmt.Errorf("resume run %s: %w", runID, err)
	}

	s := &Sim{
		cfg: Config{
			RunID:          snapshot.RunID,
			Seed:           snapshot.Seed,
			Organisms:      len(snapshot.Organisms),
			Controls:       snapshot.Controls,
			DuplicateEvery: snapshot.DuplicateEvery,
			MaxTicks:       snapshot.MaxTicks,
			TickInterval:   snapshot.TickInterval,
			Log:            l,
			Metrics:        m,
		},
		garden:    garden,
		prod:      prod,
		log:       l,
		proc:      processor.New(garden, m),
		chain:     core.NewChain(),
		tick:      snapshot.Tick,
		controls:  snapshot.Controls,
		revision:  snapshot.Revision,
		published: snapshot.Published,
		committed: committed,
		state:     snapshot.State,
	}
	s.proc.Restore(snapshot.Processor)
	return s, snapshot, nil
}

// ErrNoRunState means a run's log holds records but no snapshot, so what the
// run *was* — its seed, its controls, its pace — was never written down. The
// garden still rebuilds; the run does not.
var ErrNoRunState = errors.New("run has records but no snapshot")
