// Command signalgarden runs a Signal Garden simulation and prints the result.
//
// It has three modes. Batch is the default: run to completion as fast as the
// CPU allows and print a scorecard, which is the reproducibility check. Live
// mode (-live) paces the run on a clock and streams frames while accepting
// typed control changes, which is the behavior the M1 web client will drive
// over gRPC and WebSockets. Batch and live both run the same simulation
// in-process. Load mode (-load) is different in kind: it drives a running
// signalgardend over its real gRPC API, for measuring the daemon under a
// controlled burst rather than simulating one.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

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
		dataDir   = flag.String("data", "", "keep a live run's event history under this directory; empty keeps it in memory")
		replay    = flag.Bool("replay", false, "rebuild a run's garden from its log under -data and print it")
		workers   = flag.Int("workers", 0, "worker_count control; 0 means unbounded processing capacity")
		batch     = flag.Int("batch", 0, "batch_size control; 0 means unbounded processing capacity")
		load      = flag.Bool("load", false, "drive a running daemon over gRPC with a controlled event burst")
		daemon    = flag.String("daemon", "localhost:9090", "daemon gRPC address for -load")
		duration  = flag.Duration("duration", 10*time.Second, "how long a -load burst runs")
		poll      = flag.Duration("poll", 500*time.Millisecond, "how often -load samples telemetry")
	)
	flag.Parse()

	// -run's default ("run-local") suits batch and live, which each start
	// from a clean or in-memory history. A load burst usually hits a daemon
	// with durable history from every previous burst, and the run ID a
	// finished run leaves behind stays taken — so unless the caller asked
	// for a specific one, load mode omits it and lets the daemon generate a
	// free one, same as any other client that does not care what it is
	// called. See docs/contracts.md.
	runIDExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "run" {
			runIDExplicit = true
		}
	})

	controls := domain.Controls{
		EventsPerTick: *rate,
		RainWeight:    *rain,
		GrowthWeight:  *growth,
		PestWeight:    *pest,
		WorkerCount:   *workers,
		BatchSize:     *batch,
	}

	if *replay {
		if *dataDir == "" {
			return fmt.Errorf("-replay needs -data to say where the run's history is")
		}
		return runReplay(os.Stdout, *dataDir, *runID)
	}

	if *load {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		loadRunID := ""
		if runIDExplicit {
			loadRunID = *runID
		}
		result, err := runLoad(ctx, loadConfig{
			Daemon:         *daemon,
			RunID:          loadRunID,
			Seed:           *seed,
			Organisms:      *organisms,
			Controls:       controls,
			TickInterval:   *interval,
			DuplicateEvery: *duplicate,
			Duration:       *duration,
			Poll:           *poll,
		})
		if err != nil {
			return err
		}
		return render.Load(os.Stdout, result)
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
			DataDir:        *dataDir,
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
