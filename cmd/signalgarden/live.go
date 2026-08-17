package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/engine"
	"github.com/damodbear/signal-garden/internal/eventlog"
	"github.com/damodbear/signal-garden/internal/render"
)

// liveConfig is the live mode's slice of the command line.
type liveConfig struct {
	RunID          string
	Seed           int64
	MaxTicks       int64
	Organisms      int
	Controls       domain.Controls
	TickInterval   time.Duration
	DuplicateEvery int

	// DataDir keeps the run's event history on disk under <DataDir>/runs.
	// Empty keeps it in memory, which is what a demo wants: the same code
	// path, nothing left behind.
	DataDir string
}

// runLive drives a run through the engine and prints frames as they arrive.
//
// This is the terminal stand-in for the M1 control surface: typed commands
// where the React client will have sliders, and a line per frame where it will
// have a garden. Both talk to the same engine methods, so the browser client
// replaces this file rather than reaching past it.
func runLive(cfg liveConfig) error {
	opts := []engine.Option{engine.WithTickInterval(cfg.TickInterval)}
	if cfg.DataDir != "" {
		opts = append(opts, engine.WithLogs(engine.DirectoryLogs(cfg.DataDir)))
		fmt.Fprintf(os.Stderr, "run history under %s\n", eventlog.RunDir(cfg.DataDir, cfg.RunID))
	}

	reg := engine.NewRegistry(opts...)
	defer reg.Close()

	run, err := reg.StartRun(engine.StartRunRequest{
		RunID:          cfg.RunID,
		Seed:           cfg.Seed,
		Organisms:      cfg.Organisms,
		Controls:       cfg.Controls,
		TickInterval:   cfg.TickInterval,
		MaxTicks:       cfg.MaxTicks,
		DuplicateEvery: cfg.DuplicateEvery,
	})
	if err != nil {
		return err
	}

	sub, err := reg.Subscribe(run.RunID, 32)
	if err != nil {
		return err
	}
	defer sub.Close()

	printBanner(os.Stderr, run)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// The reader blocks on stdin until the process exits. That is acceptable
	// for a CLI: there is no portable way to interrupt a blocking read, and
	// the run is already finished by the time we return.
	go readCommands(reg, run.RunID, cfg.Controls, os.Stdin, os.Stderr)

	quit := ctx.Done()
	for {
		select {
		case frame, open := <-sub.Snapshots():
			if !open {
				summary, err := reg.FinishRun(run.RunID)
				if err != nil {
					return err
				}
				return render.Summary(os.Stdout, summary)
			}
			if err := render.FrameLine(os.Stdout, frame); err != nil {
				return err
			}
		case <-quit:
			// Restore default signal handling so a second interrupt
			// kills the process outright, and stop selecting on a
			// channel that is now permanently ready.
			quit = nil
			stop()
			fmt.Fprintln(os.Stderr, "\nfinishing run...")
			if _, err := reg.FinishRun(run.RunID); err != nil {
				return err
			}
		}
	}
}

func printBanner(w io.Writer, run engine.Run) {
	fmt.Fprintf(w, "run %s  seed %d  interval %s", run.RunID, run.Seed, run.TickInterval)
	if run.MaxTicks > 0 {
		fmt.Fprintf(w, "  max ticks %d", run.MaxTicks)
	}
	fmt.Fprint(w, "\ncommands: rate N | rain N | growth N | pest N | pause | resume | finish\n\n")
}

// readCommands turns typed lines into engine calls.
//
// It keeps its own copy of the desired controls rather than reading them back
// from the run, because the engine reports the controls the producer is using
// and a change staged moments ago has not applied yet. Holding the desired set
// locally is also what a UI does: four sliders, sent as a whole.
func readCommands(reg *engine.Registry, runID string, desired domain.Controls, in io.Reader, out io.Writer) {
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		next, err := applyCommand(reg, runID, desired, line, out)
		if err != nil {
			fmt.Fprintf(out, "! %v\n", err)
			continue
		}
		desired = next
	}
}

// applyCommand runs one typed command and returns the desired controls after
// it. Rejected commands leave the desired controls unchanged.
func applyCommand(reg *engine.Registry, runID string, desired domain.Controls, line string, out io.Writer) (domain.Controls, error) {
	fields := strings.Fields(line)
	verb := strings.ToLower(fields[0])

	switch verb {
	case "pause", "resume":
		var (
			run engine.Run
			err error
		)
		if verb == "pause" {
			run, err = reg.PauseRun(runID)
		} else {
			run, err = reg.ResumeRun(runID)
		}
		if err != nil {
			return desired, err
		}
		fmt.Fprintf(out, "> %s at tick %d\n", run.State, run.Tick)
		return desired, nil

	case "finish", "quit", "exit":
		if _, err := reg.FinishRun(runID); err != nil {
			return desired, err
		}
		return desired, nil

	case "rate", "rain", "growth", "pest":
		if len(fields) != 2 {
			return desired, fmt.Errorf("%s needs one number, for example %q", verb, verb+" 12")
		}
		value, err := strconv.Atoi(fields[1])
		if err != nil {
			return desired, fmt.Errorf("%s needs a number, got %q", verb, fields[1])
		}
		next := desired
		switch verb {
		case "rate":
			next.EventsPerTick = value
		case "rain":
			next.RainWeight = value
		case "growth":
			next.GrowthWeight = value
		case "pest":
			next.PestWeight = value
		}

		rev, err := reg.UpdateControls(runID, next)
		if err != nil {
			return desired, err
		}
		fmt.Fprintf(out, "> revision %d: rate=%d rain=%d growth=%d pest=%d, effective at tick %d\n",
			rev.Revision, next.EventsPerTick, next.RainWeight, next.GrowthWeight, next.PestWeight,
			rev.EffectiveTick)
		return next, nil

	default:
		return desired, fmt.Errorf("unknown command %q; try rate, rain, growth, pest, pause, resume, finish", verb)
	}
}
