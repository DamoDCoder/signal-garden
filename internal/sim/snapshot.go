package sim

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/event"
	"github.com/damodbear/signal-garden/internal/eventlog"
	"github.com/damodbear/signal-garden/internal/processor"
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
}

// SnapshotSchemaVersion is bumped when a snapshot's fields change
// incompatibly. A snapshot from an unknown version is refused rather than
// guessed at: the records are still there, and folding them is always correct.
const SnapshotSchemaVersion = 1

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
	proc := processor.New(garden)
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
