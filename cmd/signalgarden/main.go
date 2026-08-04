// Command signalgarden runs a Signal Garden simulation and prints the result.
//
// This is the M0 projection surface: one process, an in-memory bus, and a text
// scorecard. Its job is to make the event loop legible and reproducible before
// any of the M1+ infrastructure exists to complicate it.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/render"
	"github.com/damodbear/signal-garden/internal/run"
)

func main() {
	if err := realMain(); err != nil {
		fmt.Fprintf(os.Stderr, "signalgarden: %v\n", err)
		os.Exit(1)
	}
}

func realMain() error {
	defaults := domain.DefaultControls()

	var (
		runID     = flag.String("run", "run-local", "run identifier")
		seed      = flag.Int64("seed", 1, "deterministic simulation seed")
		ticks     = flag.Int64("ticks", 30, "number of simulation ticks")
		organisms = flag.Int("organisms", 20, "number of organisms in the garden")
		rate      = flag.Int("rate", defaults.EventsPerTick, "events produced per tick")
		rain      = flag.Int("rain", defaults.RainWeight, "relative weight of rain events")
		growth    = flag.Int("growth", defaults.GrowthWeight, "relative weight of growth events")
		pest      = flag.Int("pest", defaults.PestWeight, "relative weight of pest events")
		duplicate = flag.Int("duplicate-every", 0, "republish every Nth event to exercise idempotency (0 disables)")
	)
	flag.Parse()

	cfg := run.Config{
		RunID:     *runID,
		Seed:      *seed,
		Ticks:     *ticks,
		Organisms: *organisms,
		Controls: domain.Controls{
			EventsPerTick: *rate,
			RainWeight:    *rain,
			GrowthWeight:  *growth,
			PestWeight:    *pest,
		},
		DuplicateEvery: *duplicate,
	}

	result, err := run.Execute(cfg)
	if err != nil {
		return err
	}
	return render.Scorecard(os.Stdout, result)
}
