// Package render turns run results and live projection frames into text.
//
// This is the terminal projection surface. The React client will consume the
// same snapshots over WebSockets; keeping rendering out of the run loop and the
// engine is what makes that a new package rather than a rewrite.
package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/processor"
	"github.com/damodbear/signal-garden/internal/run"
)

// stageGlyphs renders growth stage 0 through MaxStage as widening plants.
var stageGlyphs = [domain.MaxStage + 1]string{".", ",", "i", "Y", "T", "W"}

// Scorecard writes the full run report to w.
func Scorecard(w io.Writer, r run.Result) error {
	var b strings.Builder

	b.WriteString("\nSignal Garden run\n")
	b.WriteString(strings.Repeat("=", 52) + "\n")
	fmt.Fprintf(&b, "run        %s\n", r.Config.RunID)
	fmt.Fprintf(&b, "seed       %d\n", r.Config.Seed)
	fmt.Fprintf(&b, "ticks      %d\n", r.Config.Ticks)
	fmt.Fprintf(&b, "controls   rate=%d rain=%d growth=%d pest=%d\n",
		r.FinalCtrls.EventsPerTick, r.FinalCtrls.RainWeight,
		r.FinalCtrls.GrowthWeight, r.FinalCtrls.PestWeight)
	if r.Revisions > 0 {
		fmt.Fprintf(&b, "revisions  %d control changes applied\n", r.Revisions)
	}

	gardenSection(&b, r.Organisms, r.Garden)
	eventsSection(&b, r.Published, r.Processor)

	fmt.Fprintf(&b, "\nsnapshot   %s\n", r.Snapshot)
	b.WriteString("\nSame seed, ticks, and controls reproduce this snapshot exactly.\n")

	_, err := io.WriteString(w, b.String())
	return err
}

// gardenSection writes the garden picture and its averages. Batch and live
// reports share it so the two are comparable line by line.
func gardenSection(b *strings.Builder, organisms []domain.Organism, g domain.Stats) {
	b.WriteString("\nGarden\n")
	b.WriteString(strings.Repeat("-", 52) + "\n")
	b.WriteString(gardenGrid(organisms))

	fmt.Fprintf(b, "\nalive      %d/%d\n", g.Alive, g.Organisms)
	fmt.Fprintf(b, "health     %.1f avg\n", g.AverageHP)
	fmt.Fprintf(b, "moisture   %.1f avg\n", g.AverageMoist)
	fmt.Fprintf(b, "stage      %.2f avg (%d total)\n", g.AverageStage, g.TotalStage)
}

// eventsSection writes the processing counters.
func eventsSection(b *strings.Builder, published int, p processor.Stats) {
	b.WriteString("\nEvents\n")
	b.WriteString(strings.Repeat("-", 52) + "\n")
	fmt.Fprintf(b, "published  %d\n", published)
	fmt.Fprintf(b, "received   %d\n", p.Received)
	fmt.Fprintf(b, "applied    %d\n", p.Applied)
	fmt.Fprintf(b, "no effect  %d\n", p.NoEffect)
	fmt.Fprintf(b, "duplicates %d (dropped by idempotency key)\n", p.Duplicates)
	fmt.Fprintf(b, "rejected   %d\n", p.Rejected)
	if p.UnknownEntity > 0 {
		fmt.Fprintf(b, "unknown    %d\n", p.UnknownEntity)
	}

	// Fixed order: ranging the ByType map directly would vary the report
	// between identical runs and undermine the point of the milestone.
	b.WriteString("by type    ")
	for i, t := range []string{"rain", "growth", "pest", "control_changed"} {
		if i > 0 {
			b.WriteString("  ")
		}
		fmt.Fprintf(b, "%s=%d", t, p.ByType[t])
	}
	b.WriteString("\n")
}

// gardenGrid renders organisms as a grid of glyphs, ten per row. Dead
// organisms show as 'x' so loss is visible at a glance rather than only in the
// averages.
func gardenGrid(organisms []domain.Organism) string {
	const perRow = 10
	var b strings.Builder
	for i, o := range organisms {
		if i > 0 && i%perRow == 0 {
			b.WriteString("\n")
		}
		b.WriteString(glyph(o) + " ")
	}
	b.WriteString("\n")
	return b.String()
}

func glyph(o domain.Organism) string {
	if !o.Alive() {
		return "x"
	}
	if o.Stage < 0 || o.Stage >= len(stageGlyphs) {
		return "?"
	}
	return stageGlyphs[o.Stage]
}
