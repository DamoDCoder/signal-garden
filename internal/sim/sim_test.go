package sim

import (
	"bytes"
	"errors"
	"testing"

	"github.com/damodbear/signal-garden/internal/domain"
)

func baseConfig() Config {
	return Config{
		RunID:     "run-test",
		Seed:      42,
		Organisms: 20,
		Controls:  domain.DefaultControls(),
	}
}

func mustNew(t *testing.T, cfg Config) *Sim {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustStep(t *testing.T, s *Sim, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := s.Step(); err != nil {
			t.Fatalf("Step %d: %v", i, err)
		}
	}
}

// TestControlsStageUntilNextStep pins the rule the live engine depends on: a
// control update accepted partway through a tick does not take effect until the
// next tick boundary, so the tick it applied at is the only timing fact replay
// needs to reproduce.
func TestControlsStageUntilNextStep(t *testing.T) {
	s := mustNew(t, baseConfig())
	mustStep(t, s, 3)

	next := domain.Controls{EventsPerTick: 9, RainWeight: 1, GrowthWeight: 1, PestWeight: 1}
	rev, err := s.SetControls(next)
	if err != nil {
		t.Fatalf("SetControls: %v", err)
	}
	if rev != 1 {
		t.Errorf("revision = %d, want 1", rev)
	}
	if got := s.Controls(); got != domain.DefaultControls() {
		t.Errorf("controls = %+v before Step, want the original %+v", got, domain.DefaultControls())
	}

	mustStep(t, s, 1)
	if got := s.Controls(); got != next {
		t.Errorf("controls = %+v after Step, want %+v", got, next)
	}
}

// TestMultipleStagedControlsEachGetARevision covers the case the batch runner
// cannot reach: two updates accepted within one tick. Both are real revisions
// with their own control_changed event, and the last one wins.
func TestMultipleStagedControlsEachGetARevision(t *testing.T) {
	s := mustNew(t, baseConfig())

	first := domain.Controls{EventsPerTick: 4, RainWeight: 1, GrowthWeight: 0, PestWeight: 0}
	last := domain.Controls{EventsPerTick: 7, RainWeight: 0, GrowthWeight: 0, PestWeight: 1}
	if _, err := s.SetControls(first); err != nil {
		t.Fatalf("SetControls: %v", err)
	}
	if rev, err := s.SetControls(last); err != nil || rev != 2 {
		t.Fatalf("SetControls = %d, %v; want revision 2", rev, err)
	}

	mustStep(t, s, 1)

	if got := s.Controls(); got != last {
		t.Errorf("controls = %+v, want the last staged %+v", got, last)
	}
	if got := s.ProcessorStats().ByType["control_changed"]; got != 2 {
		t.Errorf("control_changed events = %d, want 2", got)
	}
	// Two control events plus the tick's own events, which the last staged
	// controls govern.
	if want := 2 + last.EventsPerTick; s.Published() != want {
		t.Errorf("published = %d, want %d", s.Published(), want)
	}
}

func TestSetControlsRejectsInvalid(t *testing.T) {
	s := mustNew(t, baseConfig())

	if _, err := s.SetControls(domain.Controls{EventsPerTick: 0}); !errors.Is(err, domain.ErrInvalidControls) {
		t.Fatalf("SetControls = %v, want ErrInvalidControls", err)
	}
	if s.Revision() != 0 {
		t.Errorf("revision = %d after a rejected update, want 0", s.Revision())
	}

	mustStep(t, s, 1)
	if s.ProcessorStats().ByType["control_changed"] != 0 {
		t.Error("a rejected control update still emitted an event")
	}
}

func TestStepIsDeterministic(t *testing.T) {
	a := mustNew(t, baseConfig())
	b := mustNew(t, baseConfig())
	mustStep(t, a, 25)
	mustStep(t, b, 25)

	if a.Hash() != b.Hash() {
		t.Errorf("hashes diverged:\n a = %s\n b = %s", a.Hash(), b.Hash())
	}
}

func TestPendingIsZeroBetweenSteps(t *testing.T) {
	s := mustNew(t, baseConfig())
	mustStep(t, s, 5)

	if got := s.Pending(); got != 0 {
		t.Errorf("pending = %d, want 0; default controls have no processing capacity limit, so a tick drains everything it produced", got)
	}
	if s.ProcessorStats().Received != s.Published() {
		t.Errorf("received %d of %d published", s.ProcessorStats().Received, s.Published())
	}
}

// TestCapacityBelowProductionBuildsPending is the other half of
// TestPendingIsZeroBetweenSteps: once worker_count and batch_size cap what a
// tick can fold below what it produces, Pending has to become genuinely
// nonzero — that is the whole point of the capacity model. See
// docs/decisions/0017.
func TestCapacityBelowProductionBuildsPending(t *testing.T) {
	cfg := baseConfig()
	cfg.Controls.EventsPerTick = 10
	cfg.Controls.WorkerCount = 1
	cfg.Controls.BatchSize = 4 // capacity 4/tick, production 10/tick
	s := mustNew(t, cfg)

	mustStep(t, s, 1)
	if got := s.Pending(); got != 6 {
		t.Errorf("pending after 1 step = %d, want 6 (10 produced - 4 folded)", got)
	}

	mustStep(t, s, 1)
	if got := s.Pending(); got != 12 {
		t.Errorf("pending after 2 steps = %d, want 12 (20 produced - 8 folded)", got)
	}

	// Raise capacity above production and confirm the backlog drains rather
	// than staying stuck: a capacity model has to recover, not just fall
	// behind and never catch back up.
	if _, err := s.SetControls(domain.Controls{
		EventsPerTick: 1, RainWeight: 1, GrowthWeight: 1, PestWeight: 1,
		WorkerCount: 4, BatchSize: 10, // capacity 40/tick, production 1/tick
	}); err != nil {
		t.Fatalf("SetControls: %v", err)
	}
	mustStep(t, s, 20) // far more ticks than the backlog needs to drain
	if got := s.Pending(); got != 0 {
		t.Errorf("pending after raising capacity = %d, want 0 (drained)", got)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"missing run id", func(c *Config) { c.RunID = "" }},
		{"zero organisms", func(c *Config) { c.Organisms = 0 }},
		{"negative duplicate", func(c *Config) { c.DuplicateEvery = -1 }},
		{"invalid controls", func(c *Config) { c.Controls.EventsPerTick = 0 }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			tc.mutate(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatalf("New accepted %s", tc.name)
			}
		})
	}
}

// The log is the run's history, so determinism has to hold at the byte level
// and not only at the garden. Two runs of one seed must write records that are
// identical in the encoding the log checksums — the same bytes a replay reads
// back after a restart.
func TestSameSeedWritesTheSameRecords(t *testing.T) {
	const ticks = 25

	record := func() [][]byte {
		t.Helper()
		s := mustNew(t, baseConfig())
		mustStep(t, s, ticks)

		events, err := s.Log().Replay()
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if len(events) == 0 {
			t.Fatal("Replay returned nothing; the run wrote no records")
		}

		out := make([][]byte, len(events))
		for i, e := range events {
			rec, err := e.ToCore()
			if err != nil {
				t.Fatalf("ToCore: %v", err)
			}
			out[i] = rec.AppendCanonical(nil)
		}
		return out
	}

	first, second := record(), record()

	if len(first) != len(second) {
		t.Fatalf("runs wrote %d and %d records", len(first), len(second))
	}
	for i := range first {
		if !bytes.Equal(first[i], second[i]) {
			t.Fatalf("record %d differs between two runs of seed %d", i, baseConfig().Seed)
		}
	}
}

// Every event the producer emits reaches the log, duplicates included, and the
// projection folds exactly what the log holds. A gap between these two numbers
// is an event that was produced and never durably recorded.
func TestEveryPublishedEventIsRecorded(t *testing.T) {
	cfg := baseConfig()
	cfg.DuplicateEvery = 3
	s := mustNew(t, cfg)
	mustStep(t, s, 12)

	events, err := s.Log().Replay()
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(events) != s.Published() {
		t.Errorf("log holds %d records, the run published %d", len(events), s.Published())
	}
	if got := s.Log().Next(); got != int64(s.Published()) {
		t.Errorf("log assigned %d offsets, the run published %d", got, s.Published())
	}
	if got := s.ProcessorStats().Received; got != s.Published() {
		t.Errorf("processor received %d, the log holds %d", got, s.Published())
	}
	if got := s.Pending(); got != 0 {
		t.Errorf("Pending() = %d between ticks, want 0", got)
	}
}
