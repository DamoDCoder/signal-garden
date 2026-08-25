package engine

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/eventlog"
)

// newDurableHarness returns a registry writing to a real directory, so a second
// registry over the same directory is a stand-in for a restarted daemon.
func newDurableHarness(t *testing.T, dir string, snapshotEvery int64) (*Registry, *ManualClock) {
	t.Helper()
	clock := NewManualClock(epoch, 100*time.Millisecond)
	reg := NewRegistry(
		WithClock(clock),
		WithLogs(DirectoryLogs(dir)),
		WithSnapshotEvery(snapshotEvery),
	)
	return reg, clock
}

// TestARestartedRunIsTheSameRun is the point of the whole slice.
//
// One registry runs a garden, dies, and a second registry over the same
// directory picks it up and keeps producing. What it must land on is the garden
// a run that never stopped would have reached — not a similar one, and not a
// fresh run that happens to share a seed.
//
// This could not have passed before the producer's randomness was derived from
// (seed, tick): the restarted run would have re-drawn from tick zero's stream
// and diverged on its first event. See docs/decisions/0013.
func TestARestartedRunIsTheSameRun(t *testing.T) {
	const (
		before = 12
		after  = 8
	)
	dir := t.TempDir()

	// The run that gets interrupted, and comes back.
	first, clock := newDurableHarness(t, dir, 5)
	mustStart(t, first, baseRequest())
	clock.Tick(before)
	interrupted, err := first.GetSnapshot("run-test")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, clock2 := newDurableHarness(t, dir, 5)
	t.Cleanup(func() { _ = second.Close() })

	revived, err := second.Recover([]string{"run-test"})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(revived) != 1 {
		t.Fatalf("recovered %d runs, want 1", len(revived))
	}
	if !revived[0].Resumed {
		t.Error("a recovered run does not report itself as resumed")
	}
	if revived[0].Tick != interrupted.Tick {
		t.Fatalf("resumed at tick %d, was interrupted at %d", revived[0].Tick, interrupted.Tick)
	}

	resumedFrame, err := second.GetSnapshot("run-test")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if resumedFrame.Hash != interrupted.Hash {
		t.Fatalf("resumed garden %s, interrupted garden %s", resumedFrame.Hash, interrupted.Hash)
	}

	// The garden being right is not enough: the log's consumer cursor has to
	// agree with it. A log opens at the last commit, which trails the tail by
	// up to a snapshot's worth of ticks, and a resumed run that left the
	// cursor there would redeliver records its garden already holds — into a
	// processor whose deduplication table the restart emptied. It applied
	// them twice, and this is the assertion that names it.
	resumedTel, err := second.GetTelemetry("run-test")
	if err != nil {
		t.Fatalf("GetTelemetry: %v", err)
	}
	if resumedFrame.FoldedOffset != resumedTel.LogOffset {
		t.Fatalf("resumed with the cursor at %d and %d records on disk: the next tick would re-fold %d of them",
			resumedFrame.FoldedOffset, resumedTel.LogOffset, resumedTel.LogOffset-resumedFrame.FoldedOffset)
	}
	if resumedTel.Pending != 0 {
		t.Errorf("resumed with %d records pending, want 0", resumedTel.Pending)
	}

	clock2.Tick(after)
	got, err := second.GetSnapshot("run-test")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	// The control: one run, never interrupted, same clock discipline.
	control, controlClock := newDurableHarness(t, t.TempDir(), 5)
	t.Cleanup(func() { _ = control.Close() })
	mustStart(t, control, baseRequest())
	controlClock.Tick(before + after)
	want, err := control.GetSnapshot("run-test")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	if got.Tick != want.Tick {
		t.Errorf("resumed run reached tick %d, uninterrupted run reached %d", got.Tick, want.Tick)
	}
	if got.Hash != want.Hash {
		t.Errorf("a restart changed the garden\n resumed: %s\n whole:   %s", got.Hash, want.Hash)
	}
}

// TestRecoverSkipsAFinishedRun keeps a completed game from being restarted.
//
// Nothing in the log says a run ended on purpose — records describe what a run
// produced, never what it was doing — so the lifecycle rides in the snapshot,
// and finishing writes one.
func TestRecoverSkipsAFinishedRun(t *testing.T) {
	dir := t.TempDir()

	first, clock := newDurableHarness(t, dir, 5)
	mustStart(t, first, baseRequest())
	clock.Tick(6)
	if _, err := first.FinishRun("run-test"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, _ := newDurableHarness(t, dir, 5)
	t.Cleanup(func() { _ = second.Close() })

	revived, err := second.Recover([]string{"run-test"})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(revived) != 0 {
		t.Errorf("recovered %d finished runs, want 0", len(revived))
	}
	if _, err := second.GetRun("run-test"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("a finished run came back live: err = %v", err)
	}
}

// TestRecoverKeepsAPausedRunPaused. A paused run is still that run, and whether
// to resume it is the player's call rather than the daemon's.
func TestRecoverKeepsAPausedRunPaused(t *testing.T) {
	dir := t.TempDir()

	first, clock := newDurableHarness(t, dir, 50)
	mustStart(t, first, baseRequest())
	clock.Tick(4)
	if _, err := first.PauseRun("run-test"); err != nil {
		t.Fatalf("PauseRun: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, clock2 := newDurableHarness(t, dir, 50)
	t.Cleanup(func() { _ = second.Close() })

	revived, err := second.Recover([]string{"run-test"})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(revived) != 1 {
		t.Fatalf("recovered %d runs, want 1", len(revived))
	}
	if revived[0].State != StatePaused {
		t.Errorf("state = %s, want paused: pausing snapshots so a restart knows", revived[0].State)
	}

	clock2.Tick(5)
	if after := mustGet(t, second, "run-test"); after.Tick != revived[0].Tick {
		t.Errorf("a resumed paused run ticked to %d, want %d", after.Tick, revived[0].Tick)
	}
}

// TestRecoverContinuesEventNumbering guards the one piece of producer state a
// restart has to restore. Reissuing an event ID already on disk would make the
// processor treat a genuinely new event as a redelivery and drop it.
func TestRecoverContinuesEventNumbering(t *testing.T) {
	dir := t.TempDir()

	first, clock := newDurableHarness(t, dir, 5)
	mustStart(t, first, baseRequest())
	clock.Tick(10)
	beforeTel, err := first.GetTelemetry("run-test")
	if err != nil {
		t.Fatalf("GetTelemetry: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, clock2 := newDurableHarness(t, dir, 5)
	t.Cleanup(func() { _ = second.Close() })
	if _, err := second.Recover([]string{"run-test"}); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	clock2.Tick(3)

	tel, err := second.GetTelemetry("run-test")
	if err != nil {
		t.Fatalf("GetTelemetry: %v", err)
	}
	if tel.Processor.Duplicates != beforeTel.Processor.Duplicates {
		t.Errorf("duplicates went from %d to %d across a restart; the producer reissued event IDs",
			beforeTel.Processor.Duplicates, tel.Processor.Duplicates)
	}
	if tel.Processor.Applied <= beforeTel.Processor.Applied {
		t.Errorf("applied went from %d to %d; the resumed run applied nothing",
			beforeTel.Processor.Applied, tel.Processor.Applied)
	}
}

// TestRunIDsListsWhatIsOnDisk covers the half of recovery the registry cannot
// do: it never learns where logs live, so the daemon has to find them.
func TestRunIDsListsWhatIsOnDisk(t *testing.T) {
	dir := t.TempDir()

	if ids, err := eventlog.RunIDs(filepath.Join(dir, "nothing-here")); err != nil || len(ids) != 0 {
		t.Errorf("a data directory that does not exist yet: ids = %v, err = %v; want none and no error", ids, err)
	}

	reg, clock := newDurableHarness(t, dir, 5)
	for _, id := range []string{"run-b", "run-a"} {
		req := baseRequest()
		req.RunID = id
		req.Controls = domain.DefaultControls()
		mustStart(t, reg, req)
	}
	clock.Tick(2)
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ids, err := eventlog.RunIDs(dir)
	if err != nil {
		t.Fatalf("RunIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "run-a" || ids[1] != "run-b" {
		t.Errorf("ids = %v, want [run-a run-b] in name order", ids)
	}
}

// TestRecoverARunKilledBeforeItsFirstSnapshot is a regression test, and it
// exists because the unit tests missed this and a real daemon did not.
//
// A run's identity — its seed, its controls, its pace — appears in no record a
// producer emits, so a snapshot is the only place it is written down. With the
// default cadence of fifty ticks, a run interrupted in its first fifty had a
// log full of records and nothing to say what run they belonged to, and came
// back as "run has records but no snapshot". Starting a run now writes a
// snapshot at tick zero.
func TestRecoverARunKilledBeforeItsFirstSnapshot(t *testing.T) {
	dir := t.TempDir()

	// A cadence far beyond anything this test reaches, so the only snapshot
	// on disk is the one StartRun writes.
	first, clock := newDurableHarness(t, dir, 1000)
	mustStart(t, first, baseRequest())
	clock.Tick(3)
	interrupted, err := first.GetSnapshot("run-test")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, clock2 := newDurableHarness(t, dir, 1000)
	t.Cleanup(func() { _ = second.Close() })

	revived, err := second.Recover([]string{"run-test"})
	if err != nil {
		t.Fatalf("Recover a run with no cadence snapshot: %v", err)
	}
	if len(revived) != 1 {
		t.Fatalf("recovered %d runs, want 1", len(revived))
	}
	if revived[0].Seed != baseRequest().Seed {
		t.Errorf("seed came back as %d, want %d: the run lost its identity",
			revived[0].Seed, baseRequest().Seed)
	}
	if revived[0].Tick != interrupted.Tick {
		t.Errorf("resumed at tick %d, interrupted at %d", revived[0].Tick, interrupted.Tick)
	}

	clock2.Tick(4)
	got, err := second.GetSnapshot("run-test")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	control, controlClock := newDurableHarness(t, t.TempDir(), 1000)
	t.Cleanup(func() { _ = control.Close() })
	mustStart(t, control, baseRequest())
	controlClock.Tick(7)
	want, err := control.GetSnapshot("run-test")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if got.Hash != want.Hash {
		t.Errorf("a restart before the first cadence snapshot changed the garden\n resumed: %s\n whole:   %s",
			got.Hash, want.Hash)
	}
}
