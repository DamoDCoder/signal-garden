package engine

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spinelog "github.com/DamoDCoder/event-spine/log"
	spinesim "github.com/DamoDCoder/event-spine/sim"

	"github.com/damodbear/signal-garden/internal/event"
	"github.com/damodbear/signal-garden/internal/eventlog"
)

// sharedFS hands every run of a registry the same filesystem, which is what a
// data directory does across a restart. Tests use it to reopen a run's history
// without touching a disk.
func sharedFS(fs *spinesim.FS) LogOpener {
	return func(string) (*eventlog.Log, eventlog.Recovery, error) {
		return eventlog.Open(fs)
	}
}

func TestRunHistoryOutlivesTheRegistry(t *testing.T) {
	fs := spinesim.NewFS()
	clock := NewManualClock(epoch, 100*time.Millisecond)

	reg := NewRegistry(WithClock(clock), WithLogs(sharedFS(fs)))
	run := mustStart(t, reg, baseRequest())
	tickTo(t, reg, clock, run.RunID, 5)

	summary, err := reg.FinishRun(run.RunID)
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	published := summary.Telemetry.Published
	if published == 0 {
		t.Fatal("the run published nothing")
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The registry is gone; the history is not.
	reopened, _, err := eventlog.Open(fs)
	if err != nil {
		t.Fatalf("reopen the log: %v", err)
	}
	defer reopened.Close()

	events, err := reopened.Replay()
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(events) != published {
		t.Errorf("log holds %d records, the run published %d", len(events), published)
	}
	if len(events) > 0 && events[0].RunID != run.RunID {
		t.Errorf("first record belongs to run %q, want %q", events[0].RunID, run.RunID)
	}
}

// Starting a fresh run into an existing history would interleave two runs in
// one log, and the result would replay as neither.
func TestStartRunRefusesALogWithHistory(t *testing.T) {
	fs := spinesim.NewFS()
	clock := NewManualClock(epoch, 100*time.Millisecond)

	first := NewRegistry(WithClock(clock), WithLogs(sharedFS(fs)))
	run := mustStart(t, first, baseRequest())
	tickTo(t, first, clock, run.RunID, 3)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := NewRegistry(WithClock(clock), WithLogs(sharedFS(fs)))
	defer second.Close()

	if _, err := second.StartRun(baseRequest()); !errors.Is(err, ErrRunHasHistory) {
		t.Fatalf("StartRun error = %v, want ErrRunHasHistory", err)
	}
}

// Generated IDs restart at run-0001 in a fresh process while last week's
// run-0001 is still a directory. A generated ID skips history rather than
// failing on it: the caller asked for a run, not for that name.
func TestGeneratedIDsSkipUsedHistory(t *testing.T) {
	used := map[string]*spinesim.FS{}
	open := func(runID string) (*eventlog.Log, eventlog.Recovery, error) {
		fs, ok := used[runID]
		if !ok {
			fs = spinesim.NewFS()
			used[runID] = fs
		}
		return eventlog.Open(fs)
	}

	clock := NewManualClock(epoch, 100*time.Millisecond)
	first := NewRegistry(WithClock(clock), WithLogs(open))
	taken := mustStart(t, first, StartRunRequest{Seed: 1, Organisms: 5, Controls: baseRequest().Controls})
	tickTo(t, first, clock, taken.RunID, 2)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := NewRegistry(WithClock(clock), WithLogs(open))
	defer second.Close()

	next, err := second.StartRun(StartRunRequest{Seed: 1, Organisms: 5, Controls: baseRequest().Controls})
	if err != nil {
		t.Fatalf("StartRun after a restart: %v", err)
	}
	if next.RunID == taken.RunID {
		t.Errorf("reused run id %q, whose log already holds records", taken.RunID)
	}
}

// A caller-supplied ID gets no such courtesy: it named this run, and quietly
// starting a differently-named one would be worse than refusing.
func TestExplicitIDsDoNotSkipHistory(t *testing.T) {
	fs := spinesim.NewFS()
	clock := NewManualClock(epoch, 100*time.Millisecond)

	first := NewRegistry(WithClock(clock), WithLogs(sharedFS(fs)))
	mustStart(t, first, baseRequest())
	tickTo(t, first, clock, baseRequest().RunID, 2)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := NewRegistry(WithClock(clock), WithLogs(sharedFS(fs)))
	defer second.Close()

	if _, err := second.StartRun(baseRequest()); !errors.Is(err, ErrRunHasHistory) {
		t.Fatalf("StartRun error = %v, want ErrRunHasHistory for an explicit id", err)
	}
}

func TestRefuseCorruptStopsTheRun(t *testing.T) {
	reg := NewRegistry(
		WithClock(NewManualClock(epoch, 100*time.Millisecond)),
		WithLogs(corruptOpener()),
	)
	defer reg.Close()

	_, err := reg.StartRun(baseRequest())
	if !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("StartRun error = %v, want ErrCorruptLog", err)
	}
	if !strings.Contains(err.Error(), "discarded") {
		t.Errorf("error %q does not say how many bytes went", err)
	}
}

// Under continue, a corrupt log is not a reason to refuse — but a log that
// still holds records is, because this is StartRun and the run is new. The
// distinction is worth pinning: the policy governs corruption, not history.
func TestContinueCorruptStillRefusesSurvivingHistory(t *testing.T) {
	reg := NewRegistry(
		WithClock(NewManualClock(epoch, 100*time.Millisecond)),
		WithLogs(corruptOpener()),
		WithCorruptPolicy(ContinueCorrupt),
	)
	defer reg.Close()

	_, err := reg.StartRun(baseRequest())
	if !errors.Is(err, ErrRunHasHistory) {
		t.Fatalf("StartRun error = %v, want ErrRunHasHistory", err)
	}
}

func TestCorruptPolicyDefaultsToRefuse(t *testing.T) {
	if got := NewRegistry().corrupt; got != RefuseCorrupt {
		t.Errorf("default policy = %v, want refuse", got)
	}
	if got := RefuseCorrupt.String(); got != "refuse" {
		t.Errorf("RefuseCorrupt.String() = %q", got)
	}
	if got := ContinueCorrupt.String(); got != "continue" {
		t.Errorf("ContinueCorrupt.String() = %q", got)
	}
}

func TestDirectoryLogsWriteWhereTheySay(t *testing.T) {
	root := t.TempDir()
	clock := NewManualClock(epoch, 100*time.Millisecond)

	reg := NewRegistry(WithClock(clock), WithLogs(DirectoryLogs(root)))
	run := mustStart(t, reg, baseRequest())
	tickTo(t, reg, clock, run.RunID, 3)
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dir := eventlog.RunDir(root, run.RunID)
	if want := filepath.Join(root, "runs", run.RunID); dir != want {
		t.Fatalf("RunDir = %q, want %q", dir, want)
	}

	reopened, recovery, err := eventlog.OpenDir(root, run.RunID)
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	defer reopened.Close()
	if recovery.Corrupt != nil {
		t.Errorf("a cleanly closed log reopened corrupt: %v", recovery.Corrupt)
	}
	if recovery.Torn {
		t.Errorf("a cleanly closed log reopened torn")
	}
	if reopened.Next() == 0 {
		t.Error("the directory holds no records")
	}
}

// corruptOpener returns an opener over a log whose tail was lost to a power cut
// of the shape that keeps a file's length and fills the gap with zeros.
//
// That is what ext4 actually does, it is the shape the spine records as having
// hidden a bug for four milestones, and it is corruption rather than a torn
// tail: the bytes are present and wrong. Three records were synced before the
// crash and survive it, so the log is both damaged and non-empty — which is the
// only state where the corrupt policy and the history check can disagree.
func corruptOpener() LogOpener {
	fs := spinesim.NewFS()
	prepared := false

	return func(runID string) (*eventlog.Log, eventlog.Recovery, error) {
		if !prepared {
			prepared = true
			if err := seedCorruptLog(fs, runID); err != nil {
				return nil, eventlog.Recovery{}, err
			}
		}
		return eventlog.Open(fs)
	}
}

func seedCorruptLog(fs *spinesim.FS, runID string) error {
	durable, _, err := eventlog.Open(fs)
	if err != nil {
		return err
	}
	if err := durable.Append(sampleEvents(runID, 3)...); err != nil {
		return err
	}
	// Sync mode made the records durable; this makes the directory entry
	// that names their segment durable too, without which the file does not
	// survive at all.
	if err := fs.Sync(); err != nil {
		return err
	}
	if err := durable.Close(); err != nil {
		return err
	}

	// Reopened without syncing, so the next appends are still in the page
	// cache when the power goes.
	loose, _, err := eventlog.OpenWith(fs, spinelog.Config{Durability: spinelog.OS})
	if err != nil {
		return err
	}
	if err := loose.Append(sampleEvents(runID+"-lost", 2)...); err != nil {
		return err
	}
	fs.CrashExtend()
	_ = loose.Close()
	return nil
}

// tickTo advances the clock and asserts the run consumed every tick, so a test
// that depends on how far a run got fails at the cause rather than later.
func tickTo(t *testing.T, reg *Registry, clock *ManualClock, runID string, n int) {
	t.Helper()
	clock.Tick(n)
	if got := mustGet(t, reg, runID).Tick; got != int64(n) {
		t.Fatalf("run %s reached tick %d, want %d", runID, got, n)
	}
}

func sampleEvents(runID string, n int) []event.Event {
	out := make([]event.Event, n)
	for i := range out {
		out[i] = event.Event{
			EventID:       fmt.Sprintf("%s-evt-%d", runID, i),
			Type:          event.TypeRain,
			SchemaVersion: event.SchemaVersion,
			RunID:         runID,
			EntityID:      "org-000",
			PartitionKey:  runID,
			Sequence:      int64(i),
			OccurredAt:    int64(i),
			Attempt:       1,
			Payload:       event.Payload{Amount: 3},
		}
	}
	return out
}
