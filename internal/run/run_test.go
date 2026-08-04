package run

import (
	"strings"
	"testing"

	"github.com/damodbear/signal-garden/internal/domain"
)

func baseConfig() Config {
	return Config{
		RunID:     "run-test",
		Seed:      42,
		Ticks:     30,
		Organisms: 20,
		Controls:  domain.DefaultControls(),
	}
}

func mustExecute(t *testing.T, cfg Config) Result {
	t.Helper()
	r, err := Execute(cfg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return r
}

// TestSameSeedProducesSameSnapshot is the headline M0 exit criterion: the same
// seed and command sequence must produce the same garden state.
func TestSameSeedProducesSameSnapshot(t *testing.T) {
	cfg := baseConfig()

	first := mustExecute(t, cfg)
	for i := 0; i < 20; i++ {
		got := mustExecute(t, cfg)
		if got.Snapshot != first.Snapshot {
			t.Fatalf("run %d snapshot = %s, want %s", i, got.Snapshot, first.Snapshot)
		}
		if got.Garden != first.Garden {
			t.Fatalf("run %d stats = %+v, want %+v", i, got.Garden, first.Garden)
		}
	}
}

func TestDifferentSeedProducesDifferentSnapshot(t *testing.T) {
	a := mustExecute(t, baseConfig())

	cfg := baseConfig()
	cfg.Seed = 43
	b := mustExecute(t, cfg)

	if a.Snapshot == b.Snapshot {
		t.Error("different seeds produced identical gardens; the seed is not reaching the producer")
	}
}

// TestDuplicateDeliveryDoesNotChangeOutcome proves idempotency end to end: a
// run where every event is delivered twice must land on the same garden as a
// run with clean delivery.
func TestDuplicateDeliveryDoesNotChangeOutcome(t *testing.T) {
	clean := mustExecute(t, baseConfig())

	dup := baseConfig()
	dup.DuplicateEvery = 1
	noisy := mustExecute(t, dup)

	if noisy.Snapshot != clean.Snapshot {
		t.Errorf("duplicate delivery changed the garden:\n clean = %s\n noisy = %s", clean.Snapshot, noisy.Snapshot)
	}
	if noisy.Processor.Duplicates == 0 {
		t.Fatal("expected duplicates to be counted; the test proved nothing")
	}
	if noisy.Processor.Applied != clean.Processor.Applied {
		t.Errorf("applied = %d, want %d", noisy.Processor.Applied, clean.Processor.Applied)
	}
	if noisy.Published <= clean.Published {
		t.Errorf("published = %d, want more than the clean run's %d", noisy.Published, clean.Published)
	}
}

func TestControlChangesAreDeterministic(t *testing.T) {
	cfg := baseConfig()
	cfg.ControlChanges = map[int64]domain.Controls{
		5:  {EventsPerTick: 12, RainWeight: 1, GrowthWeight: 4, PestWeight: 1},
		10: {EventsPerTick: 3, RainWeight: 0, GrowthWeight: 0, PestWeight: 1},
		20: {EventsPerTick: 8, RainWeight: 5, GrowthWeight: 3, PestWeight: 0},
	}

	first := mustExecute(t, cfg)
	if first.Revisions != 3 {
		t.Errorf("revisions = %d, want 3", first.Revisions)
	}

	// Repeated to catch any dependence on Go's randomized map iteration order.
	for i := 0; i < 30; i++ {
		if got := mustExecute(t, cfg); got.Snapshot != first.Snapshot {
			t.Fatalf("run %d diverged; control changes are order-dependent on map iteration", i)
		}
	}
}

func TestControlChangesAffectOutcome(t *testing.T) {
	plain := mustExecute(t, baseConfig())

	cfg := baseConfig()
	cfg.ControlChanges = map[int64]domain.Controls{
		2: {EventsPerTick: 20, RainWeight: 0, GrowthWeight: 0, PestWeight: 1},
	}
	pestStorm := mustExecute(t, cfg)

	if pestStorm.Snapshot == plain.Snapshot {
		t.Fatal("control changes had no effect on the run")
	}
	if pestStorm.Garden.Alive >= plain.Garden.Alive {
		t.Errorf("pest storm left %d alive, expected fewer than the baseline %d",
			pestStorm.Garden.Alive, plain.Garden.Alive)
	}
}

// TestGardenActuallyGrows guards against a run that is deterministic but inert.
// A determinism suite passes just as happily on a simulation that does nothing.
func TestGardenActuallyGrows(t *testing.T) {
	cfg := baseConfig()
	cfg.Controls = domain.Controls{EventsPerTick: 10, RainWeight: 3, GrowthWeight: 2, PestWeight: 0}
	r := mustExecute(t, cfg)

	if r.Garden.TotalStage == 0 {
		t.Error("no organism grew across the run; the rain and growth rules are not connected")
	}
	if r.Processor.Applied == 0 {
		t.Error("no events applied")
	}
	if r.Garden.Alive != cfg.Organisms {
		t.Errorf("alive = %d, want all %d with no pest pressure", r.Garden.Alive, cfg.Organisms)
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"valid", func(*Config) {}, ""},
		{"missing run id", func(c *Config) { c.RunID = "" }, "run id is required"},
		{"zero ticks", func(c *Config) { c.Ticks = 0 }, "ticks must be at least 1"},
		{"zero organisms", func(c *Config) { c.Organisms = 0 }, "organisms must be at least 1"},
		{"negative duplicate", func(c *Config) { c.DuplicateEvery = -1 }, "duplicate_every must not be negative"},
		{"invalid initial controls", func(c *Config) { c.Controls.EventsPerTick = 0 }, "initial controls"},
		{
			"control change outside run",
			func(c *Config) {
				c.ControlChanges = map[int64]domain.Controls{99: domain.DefaultControls()}
			},
			"outside the run",
		},
		{
			"invalid control change",
			func(c *Config) {
				c.ControlChanges = map[int64]domain.Controls{5: {EventsPerTick: 0}}
			},
			"control change at tick 5",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestExecuteRejectsInvalidConfig(t *testing.T) {
	cfg := baseConfig()
	cfg.Ticks = 0

	if _, err := Execute(cfg); err == nil {
		t.Fatal("Execute accepted an invalid config")
	}
}

func TestPublishedCountMatchesRate(t *testing.T) {
	cfg := baseConfig()
	cfg.Ticks = 10
	cfg.Controls = domain.Controls{EventsPerTick: 5, RainWeight: 1, GrowthWeight: 1, PestWeight: 1}

	r := mustExecute(t, cfg)

	want := 50 // 10 ticks x 5 events, no control changes and no duplication
	if r.Published != want {
		t.Errorf("published = %d, want %d", r.Published, want)
	}
	if r.Processor.Received != want {
		t.Errorf("received = %d, want %d", r.Processor.Received, want)
	}
	if r.Processor.Rejected != 0 {
		t.Errorf("rejected = %d, want 0; the producer emitted an invalid envelope", r.Processor.Rejected)
	}
}
