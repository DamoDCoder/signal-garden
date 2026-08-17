package eventlog

import (
	"fmt"
	"testing"

	spinelog "github.com/DamoDCoder/event-spine/log"
	spinesim "github.com/DamoDCoder/event-spine/sim"

	"github.com/damodbear/signal-garden/internal/event"
)

func rain(id string, tick int64, seq int64) event.Event {
	return event.Event{
		EventID:       id,
		Type:          event.TypeRain,
		SchemaVersion: event.SchemaVersion,
		RunID:         "run-test",
		EntityID:      "org-000",
		PartitionKey:  "run-test",
		Sequence:      seq,
		OccurredAt:    tick,
		Attempt:       1,
		Payload:       event.Payload{Amount: 5},
	}
}

func open(t *testing.T, fs *spinesim.FS) *Log {
	t.Helper()
	l, recovery, err := Open(fs)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if recovery.Corrupt != nil {
		t.Fatalf("Open() reported corruption in a test log: %v", recovery.Corrupt)
	}
	return l
}

func ids(events []event.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.EventID
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAppendThenRead(t *testing.T) {
	l := open(t, spinesim.NewFS())
	defer l.Close()

	want := []event.Event{rain("evt-1", 0, 1), rain("evt-2", 0, 2), rain("evt-3", 1, 3)}
	if err := l.Append(want...); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	got, err := l.Unprocessed()
	if err != nil {
		t.Fatalf("Unprocessed() error = %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("read %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

// Catching up is a place a reader waits, not a place it stops. The same cursor
// has to keep going once more records arrive, because the run appends a tick at
// a time and reads after every one.
func TestReaderResumesAfterCatchingUp(t *testing.T) {
	l := open(t, spinesim.NewFS())
	defer l.Close()

	for tick := range int64(4) {
		if err := l.Append(rain(fmt.Sprintf("evt-%d", tick), tick, tick)); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
		got, err := l.Unprocessed()
		if err != nil {
			t.Fatalf("Unprocessed() error = %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("tick %d read %d events, want 1", tick, len(got))
		}
		if got[0].OccurredAt != tick {
			t.Errorf("tick %d read an event from tick %d", tick, got[0].OccurredAt)
		}

		empty, err := l.Unprocessed()
		if err != nil {
			t.Fatalf("second Unprocessed() error = %v", err)
		}
		if len(empty) != 0 {
			t.Errorf("tick %d re-read %d events without an append", tick, len(empty))
		}
	}
}

func TestPendingIsLag(t *testing.T) {
	l := open(t, spinesim.NewFS())
	defer l.Close()

	if got := l.Pending(); got != 0 {
		t.Errorf("empty log Pending() = %d, want 0", got)
	}
	if err := l.Append(rain("evt-1", 0, 1), rain("evt-2", 0, 2)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if got := l.Pending(); got != 2 {
		t.Errorf("Pending() = %d after two appends, want 2", got)
	}
	if _, err := l.Unprocessed(); err != nil {
		t.Fatalf("Unprocessed() error = %v", err)
	}
	if got := l.Pending(); got != 0 {
		t.Errorf("Pending() = %d after folding, want 0", got)
	}
	if got := l.Next(); got != 2 {
		t.Errorf("Next() = %d, want 2", got)
	}
}

// A projection that never committed has to be rebuilt from the beginning. This
// is the state Signal Garden ships in until snapshots land, and it is correct:
// replaying every record reproduces the garden exactly.
func TestUncommittedGroupReplaysFromTheStart(t *testing.T) {
	fs := spinesim.NewFS()

	first := open(t, fs)
	want := []event.Event{rain("evt-1", 0, 1), rain("evt-2", 1, 2), rain("evt-3", 2, 3)}
	if err := first.Append(want...); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if _, err := first.Unprocessed(); err != nil {
		t.Fatalf("Unprocessed() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second := open(t, fs)
	defer second.Close()

	got, err := second.Unprocessed()
	if err != nil {
		t.Fatalf("Unprocessed() after reopen error = %v", err)
	}
	if !equal(ids(got), ids(want)) {
		t.Errorf("after reopen read %v, want every record replayed: %v", ids(got), ids(want))
	}
}

// Committing is what moves the group, and reading never does. A consumer that
// committed resumes past those records; one that crashed between reading and
// committing sees them again.
func TestCommitMovesTheResumePoint(t *testing.T) {
	fs := spinesim.NewFS()

	first := open(t, fs)
	if err := first.Append(rain("evt-1", 0, 1), rain("evt-2", 1, 2)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if _, err := first.Unprocessed(); err != nil {
		t.Fatalf("Unprocessed() error = %v", err)
	}
	if err := first.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if got, err := first.Committed(); err != nil || got != 2 {
		t.Fatalf("Committed() = %d, %v; want 2, nil", got, err)
	}
	if err := first.Append(rain("evt-3", 2, 3)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second := open(t, fs)
	defer second.Close()

	got, err := second.Unprocessed()
	if err != nil {
		t.Fatalf("Unprocessed() after reopen error = %v", err)
	}
	if !equal(ids(got), []string{"evt-3"}) {
		t.Errorf("after reopen read %v, want only the uncommitted record [evt-3]", ids(got))
	}
}

func TestUncommittedReadsAreRedelivered(t *testing.T) {
	fs := spinesim.NewFS()

	first := open(t, fs)
	if err := first.Append(rain("evt-1", 0, 1), rain("evt-2", 1, 2)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	// Read but never commit — the crash-between-the-two case.
	if _, err := first.Unprocessed(); err != nil {
		t.Fatalf("Unprocessed() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second := open(t, fs)
	defer second.Close()

	got, err := second.Unprocessed()
	if err != nil {
		t.Fatalf("Unprocessed() after reopen error = %v", err)
	}
	if len(got) != 2 {
		t.Errorf("read %d records after an uncommitted read, want both redelivered", len(got))
	}
}

// Sync mode's promise: a record whose Append returned survives a power cut.
func TestSyncedRecordsSurviveACrash(t *testing.T) {
	fs := spinesim.NewFS()

	l := open(t, fs)
	if err := l.Append(rain("evt-1", 0, 1), rain("evt-2", 1, 2)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	// The records are synced by Append; the directory entry naming the
	// segment is not, and a file whose directory entry never reached the
	// disk does not survive at all.
	if err := fs.Sync(); err != nil {
		t.Fatalf("fs.Sync() error = %v", err)
	}
	fs.Crash()
	_ = l.Close()

	reopened := open(t, fs)
	defer reopened.Close()

	got, err := reopened.Unprocessed()
	if err != nil {
		t.Fatalf("Unprocessed() after crash error = %v", err)
	}
	if !equal(ids(got), []string{"evt-1", "evt-2"}) {
		t.Errorf("after a crash the log holds %v, want both acknowledged records", ids(got))
	}
}

// The counterpart: os mode acknowledges without syncing, so a crash is allowed
// to take the records. Without this the test above would pass on a log that
// never synced anything.
func TestUnsyncedRecordsDoNotSurviveACrash(t *testing.T) {
	fs := spinesim.NewFS()

	l, _, err := OpenWith(fs, spinelog.Config{Durability: spinelog.OS})
	if err != nil {
		t.Fatalf("OpenWith() error = %v", err)
	}
	if err := fs.Sync(); err != nil {
		t.Fatalf("fs.Sync() error = %v", err)
	}
	if err := l.Append(rain("evt-1", 0, 1)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	fs.Crash()
	_ = l.Close()

	reopened := open(t, fs)
	defer reopened.Close()

	got, err := reopened.Unprocessed()
	if err != nil {
		t.Fatalf("Unprocessed() after crash error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("os mode kept %d records across a crash, want none", len(got))
	}
}

func TestAppendRejectsAnInvalidEnvelope(t *testing.T) {
	l := open(t, spinesim.NewFS())
	defer l.Close()

	bad := rain("evt-1", 0, 1)
	bad.EventID = ""

	if err := l.Append(bad); err == nil {
		t.Fatal("Append() accepted an envelope with no event id")
	}
	if got := l.Next(); got != 0 {
		t.Errorf("a rejected append wrote %d records", got)
	}
}

func TestAppendNothingIsANoOp(t *testing.T) {
	l := open(t, spinesim.NewFS())
	defer l.Close()

	if err := l.Append(); err != nil {
		t.Fatalf("Append() with no events error = %v", err)
	}
	if got := l.Next(); got != 0 {
		t.Errorf("Next() = %d after an empty append, want 0", got)
	}
}
