package render

import (
	"strings"
	"testing"
	"time"

	"github.com/damodbear/signal-garden/internal/domain"
	gardenv1 "github.com/damodbear/signal-garden/internal/gen/signal/garden/v1"
)

func TestPeakPending(t *testing.T) {
	r := LoadResult{Samples: []LoadSample{
		{Pending: 4}, {Pending: 12}, {Pending: 7}, {Pending: 0},
	}}
	if got := r.PeakPending(); got != 12 {
		t.Errorf("PeakPending() = %d, want 12", got)
	}
}

func TestPeakPendingEmpty(t *testing.T) {
	if got := (LoadResult{}).PeakPending(); got != 0 {
		t.Errorf("PeakPending() on no samples = %d, want 0", got)
	}
}

func TestLoadReportsBacklogWhenCapacityFellBehind(t *testing.T) {
	r := LoadResult{
		RunID:     "run-test",
		Daemon:    "localhost:9090",
		Requested: 8 * time.Second,
		Elapsed:   8 * time.Second,
		Controls:  domain.Controls{EventsPerTick: 20, WorkerCount: 2, BatchSize: 2},
		Samples: []LoadSample{
			{Elapsed: time.Second, Tick: 5, Published: 100, Pending: 80},
			{Elapsed: 2 * time.Second, Tick: 10, Published: 200, Pending: 160},
		},
		Summary: &gardenv1.RunSummary{
			Telemetry: &gardenv1.TelemetrySnapshot{
				Published: 200,
				Pending:   160,
				Processor: &gardenv1.ProcessorStats{
					Received: 200, Applied: 180, NoEffect: 20,
					ByType: map[string]int64{"rain": 100, "growth": 60, "pest": 40},
				},
			},
		},
	}

	var b strings.Builder
	if err := Load(&b, r); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	out := b.String()

	for _, want := range []string{"run-test", "localhost:9090", "workers=2 batch=2", "peaked at 160", "160"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

func TestLoadReportsUnboundedWhenPendingStaysZero(t *testing.T) {
	r := LoadResult{
		RunID: "run-test",
		Samples: []LoadSample{
			{Pending: 0}, {Pending: 0},
		},
	}

	var b strings.Builder
	if err := Load(&b, r); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !strings.Contains(b.String(), "never left zero") {
		t.Errorf("report should say pending never left zero:\n%s", b.String())
	}
}
