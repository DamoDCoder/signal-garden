package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/engine"
)

// FrameLine writes one projection frame as a single line.
//
// A live run emits a frame per tick, so the line has to stay narrow enough to
// read as it scrolls. This is the terminal stand-in for the React garden view:
// the same snapshot, rendered with less paint.
func FrameLine(w io.Writer, s engine.GardenSnapshot) error {
	_, err := fmt.Fprintf(w, "tick %4d  %-8s alive %2d/%-2d  hp %5.1f  moist %5.1f  stage %4.2f  %s\n",
		s.Tick, s.State, s.Stats.Alive, s.Stats.Organisms,
		s.Stats.AverageHP, s.Stats.AverageMoist, s.Stats.AverageStage,
		compactGrid(s.Organisms))
	return err
}

// Summary writes the report for a finished live run. It mirrors Scorecard so a
// live run and a batch run of the same seed can be compared line by line.
func Summary(w io.Writer, sum engine.RunSummary) error {
	var b strings.Builder
	r, snap, tel := sum.Run, sum.Snapshot, sum.Telemetry

	b.WriteString("\nSignal Garden live run\n")
	b.WriteString(strings.Repeat("=", 52) + "\n")
	fmt.Fprintf(&b, "run        %s\n", r.RunID)
	fmt.Fprintf(&b, "seed       %d\n", r.Seed)
	fmt.Fprintf(&b, "ticks      %d\n", r.Tick)
	fmt.Fprintf(&b, "interval   %s\n", r.TickInterval)
	fmt.Fprintf(&b, "controls   rate=%d rain=%d growth=%d pest=%d\n",
		r.Controls.EventsPerTick, r.Controls.RainWeight,
		r.Controls.GrowthWeight, r.Controls.PestWeight)
	if r.Revision > 0 {
		fmt.Fprintf(&b, "revisions  %d control changes applied\n", r.Revision)
	}
	if r.Failure != "" {
		fmt.Fprintf(&b, "failure    %s\n", r.Failure)
	}

	gardenSection(&b, snap.Organisms, snap.Stats)
	eventsSection(&b, tel.Published, tel.Processor)

	b.WriteString("\nStream\n")
	b.WriteString(strings.Repeat("-", 52) + "\n")
	fmt.Fprintf(&b, "frames     %d sent, %d dropped to slow subscribers\n", tel.SnapshotsSent, tel.SnapshotsDropped)
	fmt.Fprintf(&b, "pending    %d events published but not processed\n", tel.Pending)
	fmt.Fprintf(&b, "log        %d records, committed through %d\n", tel.LogOffset, tel.CommittedOffset)

	fmt.Fprintf(&b, "\nsnapshot   %s\n", snap.Hash)
	b.WriteString("\nA batch run with the same seed and control ticks reaches this snapshot.\n")

	_, err := io.WriteString(w, b.String())
	return err
}

// compactGrid renders organisms as one unspaced row of glyphs, so a frame fits
// on a single terminal line.
func compactGrid(organisms []domain.Organism) string {
	var b strings.Builder
	for _, o := range organisms {
		b.WriteString(glyph(o))
	}
	return b.String()
}
