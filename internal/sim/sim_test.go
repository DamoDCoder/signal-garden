package sim

import (
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
		t.Errorf("pending = %d, want 0; the processor drains inside the tick until M2", got)
	}
	if s.ProcessorStats().Received != s.Published() {
		t.Errorf("received %d of %d published", s.ProcessorStats().Received, s.Published())
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
