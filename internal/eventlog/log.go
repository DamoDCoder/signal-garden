// Package eventlog is the durable event transport: one append-only log per
// run, holding the envelopes in docs/events.md.
//
// It replaces the M0 in-memory bus. The producer side is Append; the consumer
// side is a reader positioned where the projections group last committed. What
// changes is that a drain no longer destroys anything — records stay on disk,
// and a restart redelivers everything the projection has not durably folded.
//
// # Ownership
//
// A Log is not safe for concurrent use, and neither is the spine log beneath
// it, which takes no locks by design. Own one from a single goroutine. Signal
// Garden does that by putting it inside the Sim that a run's loop goroutine
// owns exclusively — see docs/decisions/0005.
//
// # Committing
//
// Commit is deliberately not called per tick. A commit says the projection has
// durably folded everything below the offset, and the garden is in memory until
// a snapshot is written, so committing after a tick would let a restart resume
// past records it can no longer replay into an empty garden. Until snapshots
// exist, the group never commits and a restart replays from the beginning,
// which is correct if slow.
package eventlog

import (
	"errors"
	"fmt"

	"github.com/DamoDCoder/event-spine/core"
	spinelog "github.com/DamoDCoder/event-spine/log"

	"github.com/damodbear/signal-garden/internal/event"
)

// GroupName is the consumer group the processor reads through. One group, one
// projection: the garden is the only thing folding these records.
const GroupName = "projections"

// Recovery is what opening the log found. It is the spine's type, passed
// through rather than wrapped, because docs/decisions/0006 makes the caller
// decide what a corrupt tail means and a wrapper would only obscure the fields
// that decision reads.
type Recovery = spinelog.Recovery

// Log is one run's durable event history.
type Log struct {
	log    *spinelog.Log
	group  *spinelog.Group
	reader *spinelog.Reader
}

// Open opens or creates a run's log in the directory the filesystem is rooted
// at, and positions a reader where the projections group resumes.
//
// Durability is Sync: the simulation makes exactly one Append call per tick, so
// this costs one fsync per tick — nothing at a 200ms pace — and buys the
// simplest possible crash story, which is that nothing acknowledged is lost.
// The spine's zero value is Batch, so this is a choice rather than a default.
//
// The Recovery is returned, never acted on. A torn tail has already been
// truncated; a corrupt one is the caller's decision.
func Open(fs core.FS) (*Log, Recovery, error) {
	return OpenWith(fs, spinelog.Config{Durability: spinelog.Sync})
}

// OpenWith opens a log with an explicit spine configuration. Crash tests use it
// to reach the other durability modes; production goes through Open.
func OpenWith(fs core.FS, cfg spinelog.Config) (*Log, Recovery, error) {
	l, recovery, err := spinelog.Open(fs, cfg)
	if err != nil {
		return nil, recovery, fmt.Errorf("open event log: %w", err)
	}

	group, err := l.Group(GroupName)
	if err != nil {
		_ = l.Close()
		return nil, recovery, fmt.Errorf("open group %s: %w", GroupName, err)
	}
	reader, err := group.Reader()
	if err != nil {
		_ = l.Close()
		return nil, recovery, fmt.Errorf("position group %s: %w", GroupName, err)
	}

	return &Log{log: l, group: group, reader: reader}, recovery, nil
}

// Append writes events in the order given.
//
// One call, one durability decision. Callers should hand over a whole tick's
// events rather than looping, because appending them one at a time in Sync mode
// costs one fsync each for no gain in what survives.
//
// An error does not mean nothing was written. The spine returns the offsets it
// assigned before failing, and those records are durable-eligible; a caller that
// treats a failed append as a no-op will disagree with the disk.
func (l *Log) Append(events ...event.Event) error {
	if len(events) == 0 {
		return nil
	}

	records := make([]core.Event, 0, len(events))
	for _, e := range events {
		rec, err := e.ToCore()
		if err != nil {
			return fmt.Errorf("encode %s: %w", e.EventID, err)
		}
		records = append(records, rec)
	}

	if _, err := l.log.Append(records...); err != nil {
		return fmt.Errorf("append %d events: %w", len(records), err)
	}
	return nil
}

// Unprocessed reads every record the projection has not folded yet and advances
// the cursor past them.
//
// Catching up with the writer is not an error and not an end: ErrEndOfLog means
// the cursor has reached the tail, and a later call after more appends
// continues from the same place.
func (l *Log) Unprocessed() ([]event.Event, error) {
	var out []event.Event
	for {
		rec, err := l.reader.Next()
		if errors.Is(err, spinelog.ErrEndOfLog) {
			return out, nil
		}
		if err != nil {
			return out, fmt.Errorf("read at offset %d: %w", l.reader.Offset(), err)
		}
		e, err := event.FromCore(rec.Event)
		if err != nil {
			return out, fmt.Errorf("decode record at offset %d: %w", rec.Offset, err)
		}
		out = append(out, e)
	}
}

// Replay reads the whole history from the log's first surviving record.
//
// It uses its own cursor, so it never moves the projections group: replaying is
// something a tool or a test does to a run, not something the run's consumer
// does. Records dropped by a truncation are simply absent — a reader walks to
// the next surviving offset rather than stopping at a gap.
func (l *Log) Replay() ([]event.Event, error) {
	reader, err := l.log.Reader(l.log.First())
	if err != nil {
		return nil, fmt.Errorf("position replay reader: %w", err)
	}

	var out []event.Event
	for {
		rec, err := reader.Next()
		if errors.Is(err, spinelog.ErrEndOfLog) {
			return out, nil
		}
		if err != nil {
			return out, fmt.Errorf("replay at offset %d: %w", reader.Offset(), err)
		}
		e, err := event.FromCore(rec.Event)
		if err != nil {
			return out, fmt.Errorf("decode record at offset %d: %w", rec.Offset, err)
		}
		out = append(out, e)
	}
}

// Commit records that the projection has durably folded everything below the
// cursor. Call it only after the state built from those records is on disk;
// see the package comment.
func (l *Log) Commit() error {
	if err := l.group.Commit(l.reader.Offset()); err != nil {
		return fmt.Errorf("commit %s at %d: %w", GroupName, l.reader.Offset(), err)
	}
	return nil
}

// Committed returns the offset the group would resume at. A group that has
// never committed reports the log's first offset, which is where a new consumer
// starts.
func (l *Log) Committed() (int64, error) {
	off, err := l.group.Committed()
	if err != nil && !errors.Is(err, spinelog.ErrNoGroup) {
		return int64(off), fmt.Errorf("read committed offset: %w", err)
	}
	return int64(off), nil
}

// Read returns the cursor: the offset of the next record the projection will
// fold. It is ahead of Committed by everything processed since the last commit.
func (l *Log) Read() int64 { return int64(l.reader.Offset()) }

// Next returns the offset the log will assign to the next record appended,
// which is also the number of records it holds.
func (l *Log) Next() int64 { return int64(l.log.Next()) }

// Pending is the number of records appended but not yet folded into the
// projection — consumer lag. It is zero between ticks while the processor
// drains inside the tick.
func (l *Log) Pending() int { return int(l.log.Next() - l.reader.Offset()) }

// Close releases the log. A run's log lives exactly as long as the run.
func (l *Log) Close() error {
	if err := l.log.Close(); err != nil {
		return fmt.Errorf("close event log: %w", err)
	}
	return nil
}
