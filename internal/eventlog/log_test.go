package eventlog

import (
	"errors"
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

// UnprocessedUpTo(0) has to behave exactly like Unprocessed — it is the
// implementation Unprocessed delegates to.
func TestUnprocessedUpToZeroIsUnbounded(t *testing.T) {
	l := open(t, spinesim.NewFS())
	defer l.Close()

	want := []event.Event{rain("evt-1", 0, 1), rain("evt-2", 0, 2), rain("evt-3", 1, 3)}
	if err := l.Append(want...); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	got, err := l.UnprocessedUpTo(0)
	if err != nil {
		t.Fatalf("UnprocessedUpTo(0) error = %v", err)
	}
	if !equal(ids(got), ids(want)) {
		t.Errorf("UnprocessedUpTo(0) = %v, want %v", ids(got), ids(want))
	}
}

// A capacity below what was appended must leave the rest for a later call —
// that is the mechanism a processing capacity uses to build a real backlog.
func TestUnprocessedUpToLeavesTheRestForNextTime(t *testing.T) {
	l := open(t, spinesim.NewFS())
	defer l.Close()

	all := []event.Event{rain("evt-1", 0, 1), rain("evt-2", 0, 2), rain("evt-3", 0, 3), rain("evt-4", 0, 4)}
	if err := l.Append(all...); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	first, err := l.UnprocessedUpTo(2)
	if err != nil {
		t.Fatalf("UnprocessedUpTo(2) error = %v", err)
	}
	if !equal(ids(first), ids(all[:2])) {
		t.Errorf("first UnprocessedUpTo(2) = %v, want %v", ids(first), ids(all[:2]))
	}
	if got := l.Pending(); got != 2 {
		t.Errorf("Pending() after first call = %d, want 2", got)
	}

	second, err := l.UnprocessedUpTo(10) // more than remains — should return only what's left
	if err != nil {
		t.Fatalf("second UnprocessedUpTo error = %v", err)
	}
	if !equal(ids(second), ids(all[2:])) {
		t.Errorf("second UnprocessedUpTo = %v, want %v", ids(second), ids(all[2:]))
	}
	if got := l.Pending(); got != 0 {
		t.Errorf("Pending() after draining = %d, want 0", got)
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

// crashAfterCommit builds the state Rewind exists for: a group committed at an
// offset that recovery then truncated past.
//
// It is reachable because a commit is synced whatever the log's durability mode
// says, so the commit can outlive the records it committed. Nothing about that
// is exotic — it is one power cut in os mode.
func crashAfterCommit(t *testing.T) (*spinesim.FS, int) {
	t.Helper()
	fs := spinesim.NewFS()

	l, _, err := OpenWith(fs, spinelog.Config{Durability: spinelog.OS})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	if err := fs.Sync(); err != nil {
		t.Fatalf("fs.Sync: %v", err)
	}

	const records = 6
	for i := range records {
		if err := l.Append(rain(fmt.Sprintf("evt-%d", i), int64(i), int64(i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if _, err := l.Unprocessed(); err != nil {
		t.Fatalf("Unprocessed: %v", err)
	}
	if err := l.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	fs.CrashExtend()
	_ = l.Close()
	return fs, records
}

func TestReopenSurvivesACommitBeyondTheLog(t *testing.T) {
	fs, records := crashAfterCommit(t)

	l, recovery, err := Open(fs)
	if err != nil {
		t.Fatalf("Open after a crash past the commit: %v", err)
	}
	defer l.Close()

	if recovery.Corrupt == nil {
		t.Fatal("the crash did not produce corruption; this test is not exercising what it claims")
	}
	committed, err := l.Committed()
	if err != nil {
		t.Fatalf("Committed: %v", err)
	}
	if committed != int64(records) {
		t.Fatalf("committed = %d, want the pre-crash %d", committed, records)
	}
	if committed <= l.Next() {
		t.Fatalf("committed %d is not beyond the log's %d; the setup did not truncate", committed, l.Next())
	}

	// Opening did not fail, and the cursor is somewhere the log holds.
	if l.Read() > l.Next() {
		t.Errorf("cursor at %d is past the log's %d", l.Read(), l.Next())
	}
}

func TestRewindPullsTheCommitBackToTheTruncation(t *testing.T) {
	fs, _ := crashAfterCommit(t)

	l, recovery, err := Open(fs)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	if err := l.Rewind(recovery); err != nil {
		t.Fatalf("Rewind: %v", err)
	}

	committed, err := l.Committed()
	if err != nil {
		t.Fatalf("Committed: %v", err)
	}
	if committed != int64(recovery.Next) {
		t.Errorf("committed = %d after a rewind, want the truncation point %d", committed, recovery.Next)
	}
	if l.Read() != committed {
		t.Errorf("cursor at %d, want the rewound commit %d", l.Read(), committed)
	}

	// The rewind has to survive a restart, or the next open resumes from
	// the stale commit again.
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	again, _, err := Open(fs)
	if err != nil {
		t.Fatalf("reopen after a rewind: %v", err)
	}
	defer again.Close()
	if got, _ := again.Committed(); got != committed {
		t.Errorf("committed = %d after a restart, want the rewound %d", got, committed)
	}
}

// A clean open has nothing to reconcile, and Rewind must not invent work.
func TestRewindIsANoOpWithoutDiscardedBytes(t *testing.T) {
	fs := spinesim.NewFS()
	l := open(t, fs)
	defer l.Close()

	if err := l.Append(rain("evt-1", 0, 1)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := l.Unprocessed(); err != nil {
		t.Fatalf("Unprocessed: %v", err)
	}
	if err := l.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := l.Rewind(Recovery{}); err != nil {
		t.Fatalf("Rewind on a clean recovery: %v", err)
	}
	if got, _ := l.Committed(); got != 1 {
		t.Errorf("committed = %d, want the commit left alone at 1", got)
	}
}

// A snapshot built from records that no longer exist cannot be repaired, and
// deleting a projection's only durable state is not this function's call.
func TestRewindRefusesAStrandedSnapshot(t *testing.T) {
	fs := spinesim.NewFS()

	// Three records made durable, so the log survives the crash holding
	// them.
	durable := open(t, fs)
	if err := durable.Append(rain("evt-0", 0, 0), rain("evt-1", 1, 1), rain("evt-2", 2, 2)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := fs.Sync(); err != nil {
		t.Fatalf("fs.Sync: %v", err)
	}
	if err := durable.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Two more without syncing, then a snapshot that folds all five. The
	// snapshot is made durable on its own; the records behind it are not.
	loose, _, err := OpenWith(fs, spinelog.Config{Durability: spinelog.OS})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	if err := loose.Append(rain("evt-3", 3, 3), rain("evt-4", 4, 4)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := loose.log.Snapshot(5, []byte("garden folded to 5")); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	fs.CrashExtend()
	_ = loose.Close()

	l, recovery, err := Open(fs)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	if recovery.Corrupt == nil {
		t.Fatal("the crash did not truncate; this test is not exercising what it claims")
	}
	if recovery.Next >= 5 {
		t.Fatalf("the log recovered to %d, so the snapshot at 5 is not stranded", recovery.Next)
	}

	if err := l.Rewind(recovery); !errors.Is(err, ErrStrandedSnapshot) {
		t.Fatalf("Rewind error = %v, want ErrStrandedSnapshot", err)
	}
}
