// Command signalgarden runs a Signal Garden simulation and prints the result.
//
// It has two modes. Batch is the default: run to completion as fast as the CPU
// allows and print a scorecard, which is the reproducibility check. Live mode
// (-live) paces the run on a clock and streams frames while accepting typed
// control changes, which is the behavior the M1 web client will drive over
// gRPC and WebSockets. Both modes run the same simulation.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/engine"
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
		ticks     = flag.Int64("ticks", 30, "number of simulation ticks; in live mode a limit, where 0 runs until finished")
		organisms = flag.Int("organisms", 20, "number of organisms in the garden")
		rate      = flag.Int("rate", defaults.EventsPerTick, "events produced per tick")
		rain      = flag.Int("rain", defaults.RainWeight, "relative weight of rain events")
		growth    = flag.Int("growth", defaults.GrowthWeight, "relative weight of growth events")
		pest      = flag.Int("pest", defaults.PestWeight, "relative weight of pest events")
		duplicate = flag.Int("duplicate-every", 0, "republish every Nth event to exercise idempotency (0 disables)")
		live      = flag.Bool("live", false, "run on a clock and stream frames, accepting typed control changes")
		interval  = flag.Duration("interval", engine.DefaultTickInterval, "wall-clock pace of a live run")
	)
	flag.Parse()

	controls := domain.Controls{
		EventsPerTick: *rate,
		RainWeight:    *rain,
		GrowthWeight:  *growth,
		PestWeight:    *pest,
	}

	if *live {
		// In live mode -ticks is a limit rather than a length: zero runs
		// until the player finishes it.
		return runLive(liveConfig{
			RunID:          *runID,
			Seed:           *seed,
			MaxTicks:       *ticks,
			Organisms:      *organisms,
			Controls:       controls,
			TickInterval:   *interval,
			DuplicateEvery: *duplicate,
		})
	}

	cfg := run.Config{
		RunID:          *runID,
		Seed:           *seed,
		Ticks:          *ticks,
		Organisms:      *organisms,
		Controls:       controls,
		DuplicateEvery: *duplicate,
	}

	result, err := run.Execute(cfg)
	if err != nil {
		return err
	}
	return render.Scorecard(os.Stdout, result)
}
