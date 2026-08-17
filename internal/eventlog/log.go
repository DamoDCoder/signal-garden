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
	"path/filepath"

	"github.com/DamoDCoder/event-spine/core"
	spinelog "github.com/DamoDCoder/event-spine/log"
	spineruntime "github.com/DamoDCoder/event-spine/runtime"

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

// RunDir is where a run's log lives under a data root. One directory per run,
// which is also the unit the replay command reads and the unit retention
// expires.
func RunDir(root, runID string) string {
	return filepath.Join(root, "runs", runID)
}

// OpenDir opens a run's log on the real filesystem, creating its directory if
// it is absent.
func OpenDir(root, runID string) (*Log, Recovery, error) {
	fs, err := spineruntime.NewFS(RunDir(root, runID))
	if err != nil {
		return nil, Recovery{}, fmt.Errorf("open run directory: %w", err)
	}
	return Open(fs)
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

	wrapped := &Log{log: l, group: group}
	if err := wrapped.reposition(); err != nil {
		_ = l.Close()
		return nil, recovery, err
	}
	return wrapped, recovery, nil
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

// Save writes a snapshot of the projection and then commits the group to the
// same offset.
//
// The order is the whole point. The snapshot is the state built from every
// record below the cursor; committing says those records need never be
// delivered again. Committing first would leave a window where a crash resumes
// past records with no state to resume from — a projection that can neither
// replay nor restore. This way round, a crash between the two costs a redelivery
// of records the projection has already folded, which is what idempotency is
// for.
func (l *Log) Save(state []byte) error {
	at := l.reader.Offset()
	if err := l.log.Snapshot(at, state); err != nil {
		return fmt.Errorf("write snapshot at %d: %w", at, err)
	}
	return l.Commit()
}

// Restore returns the newest snapshot's state and every record after it.
//
// The state is nil when no snapshot exists, in which case the records are the
// whole log — a projection with no shortcut rebuilds from the beginning, which
// is correct if slow. The offset a snapshot names is the first record it did
// *not* fold, so the records returned are exactly the ones still to apply.
func (l *Log) Restore() ([]byte, int64, []event.Event, error) {
	snapshot, reader, err := l.log.Restore()
	if err != nil && !errors.Is(err, spinelog.ErrNoSnapshot) {
		return nil, 0, nil, fmt.Errorf("restore: %w", err)
	}

	var tail []event.Event
	for {
		rec, err := reader.Next()
		if errors.Is(err, spinelog.ErrEndOfLog) {
			return snapshot.State, int64(snapshot.Offset), tail, nil
		}
		if err != nil {
			return nil, 0, nil, fmt.Errorf("restore at offset %d: %w", reader.Offset(), err)
		}
		e, err := event.FromCore(rec.Event)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("decode record at offset %d: %w", rec.Offset, err)
		}
		tail = append(tail, e)
	}
}

// ErrStrandedSnapshot means a snapshot folded records that recovery then
// truncated, so the state it holds describes a history the log no longer has.
var ErrStrandedSnapshot = errors.New("snapshot is newer than the recovered log")

// Rewind reconciles the group and the snapshots with a truncation.
//
// Compaction preserves offsets, so a committed offset keeps meaning the same
// record. Truncation does not: the tail moves back, and later appends are
// assigned offsets that different records used to hold. A commit or a snapshot
// taken before the truncation therefore names a record that is either gone or
// about to be something else, and resuming from one silently folds the wrong
// history. Systems that solve this use leader epochs; the spine does not, so
// the positions have to be pulled back by hand.
//
// A stranded snapshot is refused rather than repaired. Its state was built from
// records that no longer exist and there is no way to unfold them, so the only
// honest options are to delete it deliberately or to stop — and deleting a
// projection's only durable state is not a call this function should make.
//
// See docs/decisions/0006.
func (l *Log) Rewind(rec Recovery) error {
	if rec.Discarded == 0 {
		return nil
	}

	snapshot, err := l.log.LatestSnapshot()
	switch {
	case err == nil && snapshot.Offset > rec.Next:
		return fmt.Errorf("%w: it folded records up to %d, the log now ends at %d",
			ErrStrandedSnapshot, snapshot.Offset, rec.Next)
	case err != nil && !errors.Is(err, spinelog.ErrNoSnapshot):
		return fmt.Errorf("read the latest snapshot: %w", err)
	}

	committed, err := l.Committed()
	if err != nil {
		return err
	}
	if committed <= int64(rec.Next) {
		return nil
	}
	if err := l.group.Commit(rec.Next); err != nil {
		return fmt.Errorf("rewind %s from %d to %d: %w", GroupName, committed, rec.Next, err)
	}
	return l.reposition()
}

// reposition points the reader at where the group resumes.
//
// A commit can name an offset the log no longer holds: a commit is synced
// whatever the log's durability mode says, so it can outlive the very records
// it committed when recovery truncates past them. There is no valid resume
// point in that case, and the only position that cannot silently skip a record
// is the beginning. Rewind is what then rewrites the commit; resuming from the
// start in the meantime redelivers, which the projection is required to
// tolerate anyway.
func (l *Log) reposition() error {
	committed, err := l.Committed()
	if err != nil {
		return err
	}

	if committed > l.Next() {
		reader, err := l.log.Reader(l.log.First())
		if err != nil {
			return fmt.Errorf("reposition group %s to the start: %w", GroupName, err)
		}
		l.reader = reader
		return nil
	}

	reader, err := l.group.Reader()
	if err != nil {
		return fmt.Errorf("reposition group %s: %w", GroupName, err)
	}
	l.reader = reader
	return nil
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
