package render

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/damodbear/signal-garden/internal/domain"
	gardenv1 "github.com/damodbear/signal-garden/internal/gen/signal/garden/v1"
	"github.com/damodbear/signal-garden/internal/processor"
)

// LoadSample is one telemetry poll taken during a load burst.
type LoadSample struct {
	Elapsed   time.Duration
	Tick      int64
	Published int64
	Pending   int64
}

// LoadResult is what a load burst produced, from the client's point of view:
// what was asked for, what was observed along the way, and how the run ended.
type LoadResult struct {
	RunID     string
	Daemon    string
	Requested time.Duration
	Elapsed   time.Duration
	Controls  domain.Controls
	Samples   []LoadSample
	Summary   *gardenv1.RunSummary
}

// PeakPending is the highest pending count any sample observed — the
// clearest single number for "how far behind did this capacity fall."
func (r LoadResult) PeakPending() int64 {
	var peak int64
	for _, s := range r.Samples {
		if s.Pending > peak {
			peak = s.Pending
		}
	}
	return peak
}

// Load writes a load burst's report to w.
func Load(w io.Writer, r LoadResult) error {
	var b strings.Builder

	b.WriteString("\nSignal Garden load burst\n")
	b.WriteString(strings.Repeat("=", 52) + "\n")
	fmt.Fprintf(&b, "run        %s\n", r.RunID)
	fmt.Fprintf(&b, "daemon     %s\n", r.Daemon)
	fmt.Fprintf(&b, "duration   %s requested, %s elapsed\n", r.Requested, r.Elapsed.Round(time.Millisecond))
	fmt.Fprintf(&b, "controls   rate=%d rain=%d growth=%d pest=%d workers=%d batch=%d\n",
		r.Controls.EventsPerTick, r.Controls.RainWeight, r.Controls.GrowthWeight, r.Controls.PestWeight,
		r.Controls.WorkerCount, r.Controls.BatchSize)

	b.WriteString("\nTimeline\n")
	b.WriteString(strings.Repeat("-", 52) + "\n")
	b.WriteString("elapsed    tick       published  pending\n")
	for _, s := range r.Samples {
		fmt.Fprintf(&b, "%-10s %-10d %-10d %d\n", s.Elapsed.Round(10*time.Millisecond), s.Tick, s.Published, s.Pending)
	}

	if r.Summary != nil && r.Summary.Telemetry != nil {
		eventsSection(&b, int(r.Summary.Telemetry.Published), processorStatsFrom(r.Summary.Telemetry.GetProcessor()))
	}

	peak := r.PeakPending()
	finalPending := int64(0)
	if r.Summary != nil && r.Summary.Telemetry != nil {
		finalPending = r.Summary.Telemetry.Pending
	}
	if peak == 0 {
		b.WriteString("\nPending never left zero: this capacity kept up with production for the whole burst.\n")
	} else {
		fmt.Fprintf(&b, "\nPending peaked at %d during the burst and stood at %d when the run finished — finishing\nstops ticking, so a nonzero figure here is backlog the run stopped folding, not backlog\nit cleared.\n", peak, finalPending)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// processorStatsFrom adapts the wire ProcessorStats into the processor.Stats
// shape eventsSection already renders, so the two CLI reports share one
// rendering path rather than two that could drift.
func processorStatsFrom(p *gardenv1.ProcessorStats) processor.Stats {
	if p == nil {
		return processor.Stats{}
	}
	byType := make(map[string]int, len(p.ByType))
	for k, v := range p.ByType {
		byType[k] = int(v)
	}
	return processor.Stats{
		Received:      int(p.Received),
		Applied:       int(p.Applied),
		NoEffect:      int(p.NoEffect),
		Duplicates:    int(p.Duplicates),
		Rejected:      int(p.Rejected),
		UnknownEntity: int(p.UnknownEntity),
		ByType:        byType,
	}
}
