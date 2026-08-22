package engine

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/run"
)

var epoch = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// newHarness returns a registry driven by a manual clock, so every test states
// exactly how many ticks happened instead of waiting for real time.
func newHarness(t *testing.T) (*Registry, *ManualClock) {
	t.Helper()
	clock := NewManualClock(epoch, 100*time.Millisecond)
	reg := NewRegistry(WithClock(clock))
	t.Cleanup(func() {
		if err := reg.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return reg, clock
}

func baseRequest() StartRunRequest {
	return StartRunRequest{
		RunID:        "run-test",
		Seed:         42,
		Organisms:    20,
		Controls:     domain.DefaultControls(),
		TickInterval: 100 * time.Millisecond,
	}
}

func mustStart(t *testing.T, reg *Registry, req StartRunRequest) Run {
	t.Helper()
	r, err := reg.StartRun(req)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return r
}

func mustGet(t *testing.T, reg *Registry, runID string) Run {
	t.Helper()
	r, err := reg.GetRun(runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	return r
}

// TestEngineMatchesBatchRun is the load-bearing test of this package. A live
// run, paced by a clock and steered by control updates that arrive between
// ticks, must land on exactly the garden the batch runner reaches from the same
// seed and the same control-change ticks.
//
// If this ever fails, the live path and the replay path have diverged, and
// nothing the player watches can be trusted to be reproducible.
func TestEngineMatchesBatchRun(t *testing.T) {
	changed := map[int64]domain.Controls{
		5:  {EventsPerTick: 12, RainWeight: 1, GrowthWeight: 4, PestWeight: 1},
		10: {EventsPerTick: 3, RainWeight: 0, GrowthWeight: 0, PestWeight: 1},
	}

	batch, err := run.Execute(run.Config{
		RunID:          "run-test",
		Seed:           42,
		Ticks:          30,
		Organisms:      20,
		Controls:       domain.DefaultControls(),
		ControlChanges: changed,
	})
	if err != nil {
		t.Fatalf("run.Execute: %v", err)
	}

	reg, clock := newHarness(t)
	req := baseRequest()
	req.MaxTicks = 30
	mustStart(t, reg, req)

	// Ticks 0-4, then steer, exactly as the batch config says.
	clock.Tick(5)
	rev, err := reg.UpdateControls("run-test", changed[5])
	if err != nil {
		t.Fatalf("UpdateControls: %v", err)
	}
	if rev.EffectiveTick != 5 {
		t.Errorf("effective tick = %d, want 5", rev.EffectiveTick)
	}

	clock.Tick(5)
	if _, err := reg.UpdateControls("run-test", changed[10]); err != nil {
		t.Fatalf("UpdateControls: %v", err)
	}
	clock.Tick(20)

	live := mustGet(t, reg, "run-test")
	if live.State != StateFinished {
		t.Fatalf("state = %s, want finished after %d ticks", live.State, req.MaxTicks)
	}

	snap, err := reg.GetSnapshot("run-test")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if snap.Hash != batch.Snapshot {
		t.Fatalf("live garden diverged from the batch run:\n live  = %s\n batch = %s", snap.Hash, batch.Snapshot)
	}
	if snap.Stats != batch.Garden {
		t.Errorf("stats = %+v, want %+v", snap.Stats, batch.Garden)
	}

	tel, err := reg.GetTelemetry("run-test")
	if err != nil {
		t.Fatalf("GetTelemetry: %v", err)
	}
	if tel.Published != batch.Published {
		t.Errorf("published = %d, want %d", tel.Published, batch.Published)
	}
	if tel.Processor.Applied != batch.Processor.Applied {
		t.Errorf("applied = %d, want %d", tel.Processor.Applied, batch.Processor.Applied)
	}
	if live.Revision != batch.Revisions {
		t.Errorf("revision = %d, want %d", live.Revision, batch.Revisions)
	}
}

func TestStartRunGeneratesRunID(t *testing.T) {
	reg, _ := newHarness(t)

	req := baseRequest()
	req.RunID = ""
	first := mustStart(t, reg, req)
	second := mustStart(t, reg, req)

	if first.RunID == "" || second.RunID == "" {
		t.Fatal("registry did not generate run ids")
	}
	if first.RunID == second.RunID {
		t.Fatalf("both runs got id %s", first.RunID)
	}
	if got := len(reg.ListRuns()); got != 2 {
		t.Errorf("ListRuns returned %d runs, want 2", got)
	}
}

func TestStartRunRejectsDuplicateID(t *testing.T) {
	reg, _ := newHarness(t)
	mustStart(t, reg, baseRequest())

	if _, err := reg.StartRun(baseRequest()); !errors.Is(err, ErrRunExists) {
		t.Fatalf("StartRun = %v, want ErrRunExists", err)
	}
}

func TestStartRunRejectsInvalidRequest(t *testing.T) {
	reg, _ := newHarness(t)

	tests := []struct {
		name    string
		mutate  func(*StartRunRequest)
		wantErr string
	}{
		{"zero organisms", func(r *StartRunRequest) { r.Organisms = 0 }, "organisms must be at least 1"},
		{"negative max ticks", func(r *StartRunRequest) { r.MaxTicks = -1 }, "max_ticks must not be negative"},
		{"negative duplicate", func(r *StartRunRequest) { r.DuplicateEvery = -1 }, "duplicate_every must not be negative"},
		{"invalid controls", func(r *StartRunRequest) { r.Controls.EventsPerTick = 0 }, "events_per_tick"},
		{"empty event mix", func(r *StartRunRequest) { r.Controls = domain.Controls{EventsPerTick: 5} }, "at least one event weight"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest()
			req.RunID = ""
			tc.mutate(&req)

			_, err := reg.StartRun(req)
			if err == nil {
				t.Fatalf("StartRun accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("StartRun = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestPauseStopsTicksAndResumeContinues(t *testing.T) {
	reg, clock := newHarness(t)
	mustStart(t, reg, baseRequest())

	clock.Tick(3)
	if got := mustGet(t, reg, "run-test").Tick; got != 3 {
		t.Fatalf("tick = %d, want 3", got)
	}

	paused, err := reg.PauseRun("run-test")
	if err != nil {
		t.Fatalf("PauseRun: %v", err)
	}
	if paused.State != StatePaused {
		t.Fatalf("state = %s, want paused", paused.State)
	}

	clock.Tick(5)
	after := mustGet(t, reg, "run-test")
	if after.Tick != 3 {
		t.Errorf("tick advanced to %d while paused, want 3", after.Tick)
	}

	if _, err := reg.ResumeRun("run-test"); err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	clock.Tick(2)

	resumed := mustGet(t, reg, "run-test")
	if resumed.State != StateRunning {
		t.Errorf("state = %s, want running", resumed.State)
	}
	if resumed.Tick != 5 {
		t.Errorf("tick = %d, want 5; a paused run must resume where it stopped", resumed.Tick)
	}
}

func TestUpdateControlsTakesEffectOnNextTick(t *testing.T) {
	reg, clock := newHarness(t)
	mustStart(t, reg, baseRequest())
	clock.Tick(2)

	next := domain.Controls{EventsPerTick: 11, RainWeight: 1, GrowthWeight: 1, PestWeight: 1}
	rev, err := reg.UpdateControls("run-test", next)
	if err != nil {
		t.Fatalf("UpdateControls: %v", err)
	}
	if rev.Revision != 1 || rev.EffectiveTick != 2 {
		t.Fatalf("revision = %d at tick %d, want revision 1 at tick 2", rev.Revision, rev.EffectiveTick)
	}

	// Staged, not applied: the producer is still on the old mix until the
	// tick boundary the revision named.
	if got := mustGet(t, reg, "run-test").Controls; got != domain.DefaultControls() {
		t.Errorf("controls = %+v before the effective tick, want the original %+v", got, domain.DefaultControls())
	}

	clock.Tick(1)
	if got := mustGet(t, reg, "run-test").Controls; got != next {
		t.Errorf("controls = %+v after the effective tick, want %+v", got, next)
	}
}

func TestUpdateControlsRejectsInvalid(t *testing.T) {
	reg, _ := newHarness(t)
	mustStart(t, reg, baseRequest())

	_, err := reg.UpdateControls("run-test", domain.Controls{EventsPerTick: 0})
	if !errors.Is(err, domain.ErrInvalidControls) {
		t.Fatalf("UpdateControls = %v, want ErrInvalidControls", err)
	}
	if got := mustGet(t, reg, "run-test").Revision; got != 0 {
		t.Errorf("revision = %d after a rejected update, want 0", got)
	}
}

func TestFinishedRunRejectsCommands(t *testing.T) {
	reg, _ := newHarness(t)
	mustStart(t, reg, baseRequest())

	if _, err := reg.FinishRun("run-test"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	if _, err := reg.UpdateControls("run-test", domain.DefaultControls()); !errors.Is(err, ErrRunFinished) {
		t.Errorf("UpdateControls = %v, want ErrRunFinished", err)
	}
	if _, err := reg.PauseRun("run-test"); !errors.Is(err, ErrRunFinished) {
		t.Errorf("PauseRun = %v, want ErrRunFinished", err)
	}
}

func TestFinishRunIsIdempotent(t *testing.T) {
	reg, clock := newHarness(t)
	mustStart(t, reg, baseRequest())
	clock.Tick(4)

	first, err := reg.FinishRun("run-test")
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	second, err := reg.FinishRun("run-test")
	if err != nil {
		t.Fatalf("second FinishRun: %v", err)
	}

	if first.Run.State != StateFinished || second.Run.State != StateFinished {
		t.Fatalf("states = %s, %s; want both finished", first.Run.State, second.Run.State)
	}
	if first.Snapshot.Hash != second.Snapshot.Hash || first.Run.Tick != second.Run.Tick {
		t.Error("a repeated FinishRun changed the summary; it must be safe to retry")
	}
	if first.Run.FinishedAt.IsZero() {
		t.Error("finished_at was not recorded")
	}
}

func TestFinishedRunStopsTicking(t *testing.T) {
	reg, clock := newHarness(t)
	mustStart(t, reg, baseRequest())
	clock.Tick(3)

	summary, err := reg.FinishRun("run-test")
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	clock.Tick(5)
	if got := mustGet(t, reg, "run-test").Tick; got != summary.Run.Tick {
		t.Errorf("tick = %d after finishing, want %d", got, summary.Run.Tick)
	}
}

func TestMaxTicksFinishesRun(t *testing.T) {
	reg, clock := newHarness(t)
	req := baseRequest()
	req.MaxTicks = 4
	mustStart(t, reg, req)

	clock.Tick(10)

	got := mustGet(t, reg, "run-test")
	if got.State != StateFinished {
		t.Errorf("state = %s, want finished", got.State)
	}
	if got.Tick != 4 {
		t.Errorf("tick = %d, want it to stop at max_ticks 4", got.Tick)
	}
}

func TestSubscribeDeliversSnapshotThenUpdates(t *testing.T) {
	reg, clock := newHarness(t)
	mustStart(t, reg, baseRequest())

	sub, err := reg.Subscribe("run-test", 8)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	first := <-sub.Snapshots()
	if first.Tick != 0 || first.RunID != "run-test" {
		t.Fatalf("first frame = tick %d of %s, want tick 0 of run-test", first.Tick, first.RunID)
	}
	if len(first.Organisms) != 20 {
		t.Errorf("first frame carried %d organisms, want 20", len(first.Organisms))
	}

	clock.Tick(3)
	for want := int64(1); want <= 3; want++ {
		frame := <-sub.Snapshots()
		if frame.Tick != want {
			t.Fatalf("frame tick = %d, want %d", frame.Tick, want)
		}
		if frame.Sequence != want {
			t.Errorf("frame sequence = %d, want %d", frame.Sequence, want)
		}
	}

	if _, err := reg.FinishRun("run-test"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	final := <-sub.Snapshots()
	if final.State != StateFinished {
		t.Errorf("final frame state = %s, want finished", final.State)
	}
	if _, open := <-sub.Snapshots(); open {
		t.Error("stream stayed open after the run finished")
	}
}

func TestSubscriptionCloseDetaches(t *testing.T) {
	reg, clock := newHarness(t)
	mustStart(t, reg, baseRequest())

	sub, err := reg.Subscribe("run-test", 4)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	<-sub.Snapshots()

	sub.Close()
	sub.Close() // must be safe twice

	if _, open := <-sub.Snapshots(); open {
		t.Fatal("channel stayed open after Close")
	}

	clock.Tick(2)
	tel, err := reg.GetTelemetry("run-test")
	if err != nil {
		t.Fatalf("GetTelemetry: %v", err)
	}
	if tel.Subscribers != 0 {
		t.Errorf("subscribers = %d after Close, want 0", tel.Subscribers)
	}
}

func TestSubscribeAfterFinishGetsFinalFrame(t *testing.T) {
	reg, clock := newHarness(t)
	mustStart(t, reg, baseRequest())
	clock.Tick(2)
	if _, err := reg.FinishRun("run-test"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	sub, err := reg.Subscribe("run-test", 1)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	frame := <-sub.Snapshots()
	if frame.State != StateFinished || frame.Tick != 2 {
		t.Errorf("frame = tick %d in state %s, want tick 2 finished", frame.Tick, frame.State)
	}
	if _, open := <-sub.Snapshots(); open {
		t.Error("stream for a finished run must close")
	}
}

// TestSlowSubscriberDropsFramesInsteadOfStalling is the backpressure rule: one
// unread stream must not hold up the simulation for everyone else.
func TestSlowSubscriberDropsFrames(t *testing.T) {
	reg, clock := newHarness(t)
	mustStart(t, reg, baseRequest())

	sub, err := reg.Subscribe("run-test", 1)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()

	// The buffer is full with the initial frame and never read.
	clock.Tick(3)

	got := mustGet(t, reg, "run-test")
	if got.Tick != 3 {
		t.Fatalf("tick = %d, want 3; the run stalled behind a slow subscriber", got.Tick)
	}

	tel, err := reg.GetTelemetry("run-test")
	if err != nil {
		t.Fatalf("GetTelemetry: %v", err)
	}
	if tel.SnapshotsDropped != 3 {
		t.Errorf("dropped = %d, want 3", tel.SnapshotsDropped)
	}
}

func TestUnknownRun(t *testing.T) {
	reg, _ := newHarness(t)

	if _, err := reg.GetRun("nope"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("GetRun = %v, want ErrRunNotFound", err)
	}
	if _, err := reg.GetSnapshot("nope"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("GetSnapshot = %v, want ErrRunNotFound", err)
	}
	if _, err := reg.GetTelemetry("nope"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("GetTelemetry = %v, want ErrRunNotFound", err)
	}
	if _, err := reg.Subscribe("nope", 1); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("Subscribe = %v, want ErrRunNotFound", err)
	}
}

func TestCloseStopsRuns(t *testing.T) {
	clock := NewManualClock(epoch, 100*time.Millisecond)
	reg := NewRegistry(WithClock(clock))
	mustStart(t, reg, baseRequest())

	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if _, err := reg.GetRun("run-test"); !errors.Is(err, ErrRegistryDown) {
		t.Errorf("GetRun = %v, want ErrRegistryDown", err)
	}
	if _, err := reg.StartRun(baseRequest()); !errors.Is(err, ErrRegistryDown) {
		t.Errorf("StartRun = %v, want ErrRegistryDown", err)
	}
}

// TestConcurrentAccess exercises the actor boundary under the race detector:
// commands and reads from many goroutines while the clock keeps ticking.
func TestConcurrentAccess(t *testing.T) {
	reg, clock := newHarness(t)
	mustStart(t, reg, baseRequest())

	sub, err := reg.Subscribe("run-test", 4)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range sub.Snapshots() {
		}
	}()

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		clock.Tick(50)
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			//nolint:errcheck // the run may finish underneath this loop
			_, _ = reg.UpdateControls("run-test", domain.Controls{
				EventsPerTick: 1 + i%9,
				RainWeight:    1,
				GrowthWeight:  1,
				PestWeight:    1,
			})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_, _ = reg.GetSnapshot("run-test")
			_, _ = reg.GetTelemetry("run-test")
		}
	}()
	wg.Wait()
	sub.Close()
	<-drained

	got := mustGet(t, reg, "run-test")
	if got.Tick != 50 {
		t.Errorf("tick = %d, want 50", got.Tick)
	}
	if got.Revision != 50 {
		t.Errorf("revision = %d, want 50", got.Revision)
	}
}

// TestOffsetsDescribeTheLog pins what the three offsets on the wire mean.
//
// LogOffset counts records the run appended, FoldedOffset is where the garden
// has read to, and CommittedOffset is what a restart would resume from. The
// interesting one is the gap: a commit only happens with a snapshot, so
// CommittedOffset must sit still through ticks that write no snapshot. A
// client watching it move every tick would be reading a promise the log has
// not made.
func TestOffsetsDescribeTheLog(t *testing.T) {
	clock := NewManualClock(epoch, 100*time.Millisecond)
	reg := NewRegistry(WithClock(clock), WithSnapshotEvery(3))
	t.Cleanup(func() {
		if err := reg.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	mustStart(t, reg, baseRequest())

	mustTelemetry := func() TelemetrySnapshot {
		t.Helper()
		tel, err := reg.GetTelemetry("run-test")
		if err != nil {
			t.Fatalf("GetTelemetry: %v", err)
		}
		return tel
	}

	clock.Tick(3)
	saved := mustTelemetry()
	if saved.LogOffset != int64(saved.Published) {
		t.Errorf("log offset = %d, published = %d; every published event is one record",
			saved.LogOffset, saved.Published)
	}
	if saved.LogOffset == 0 {
		t.Fatal("log offset = 0 after three ticks, want the tick's records")
	}
	if saved.CommittedOffset != saved.LogOffset {
		t.Errorf("committed offset = %d, want %d: the third tick wrote a snapshot",
			saved.CommittedOffset, saved.LogOffset)
	}

	clock.Tick(2)
	between := mustTelemetry()
	if between.LogOffset <= saved.LogOffset {
		t.Errorf("log offset = %d after two more ticks, want more than %d",
			between.LogOffset, saved.LogOffset)
	}
	if between.CommittedOffset != saved.LogOffset {
		t.Errorf("committed offset = %d, want %d: ticks four and five wrote no snapshot",
			between.CommittedOffset, saved.LogOffset)
	}

	snap, err := reg.GetSnapshot("run-test")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if snap.FoldedOffset != between.LogOffset {
		t.Errorf("folded offset = %d, log offset = %d; the projection drains inside the tick",
			snap.FoldedOffset, between.LogOffset)
	}

	summary, err := reg.FinishRun("run-test")
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if summary.Telemetry.CommittedOffset != summary.Telemetry.LogOffset {
		t.Errorf("committed offset = %d, log offset = %d; finishing writes a final snapshot",
			summary.Telemetry.CommittedOffset, summary.Telemetry.LogOffset)
	}
	if summary.Snapshot.FoldedOffset != summary.Telemetry.LogOffset {
		t.Errorf("folded offset = %d, log offset = %d at finish",
			summary.Snapshot.FoldedOffset, summary.Telemetry.LogOffset)
	}
}
