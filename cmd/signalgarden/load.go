package main

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/damodbear/signal-garden/internal/domain"
	gardenv1 "github.com/damodbear/signal-garden/internal/gen/signal/garden/v1"
	"github.com/damodbear/signal-garden/internal/render"
)

// loadConfig is the load burst's slice of the command line.
type loadConfig struct {
	Daemon         string // gRPC address, e.g. "localhost:9090"
	RunID          string
	Seed           int64
	Organisms      int
	Controls       domain.Controls
	TickInterval   time.Duration
	DuplicateEvery int

	Duration time.Duration // how long to run the burst
	Poll     time.Duration // how often to sample telemetry during it
}

// shutdownGrace bounds how long FinishRun gets to complete after an
// interrupt, so an unresponsive daemon cannot hang the tool forever.
const shutdownGrace = 5 * time.Second

// runLoad drives a controlled event burst against a running daemon over its
// gRPC API and returns what it observed.
//
// This exercises the real serving path — the same grpcServer the REST gateway
// dials internally, wrapped by the same metrics interceptor — rather than a
// second in-process simulation of it. See docs/roadmap.md's M3 section.
func runLoad(ctx context.Context, cfg loadConfig) (render.LoadResult, error) {
	conn, err := grpc.NewClient(cfg.Daemon, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return render.LoadResult{}, fmt.Errorf("dial %s: %w", cfg.Daemon, err)
	}
	defer conn.Close()
	client := gardenv1.NewGardenServiceClient(conn)

	run, err := client.StartRun(ctx, &gardenv1.StartRunRequest{
		RunId:     cfg.RunID,
		Seed:      cfg.Seed,
		Organisms: int32(cfg.Organisms),
		Controls: &gardenv1.Controls{
			EventsPerTick: int32(cfg.Controls.EventsPerTick),
			RainWeight:    int32(cfg.Controls.RainWeight),
			GrowthWeight:  int32(cfg.Controls.GrowthWeight),
			PestWeight:    int32(cfg.Controls.PestWeight),
			WorkerCount:   int32(cfg.Controls.WorkerCount),
			BatchSize:     int32(cfg.Controls.BatchSize),
		},
		TickInterval:   durationpb.New(cfg.TickInterval),
		DuplicateEvery: int32(cfg.DuplicateEvery),
	})
	if err != nil {
		if status.Code(err) == codes.Unavailable {
			return render.LoadResult{}, fmt.Errorf("is a daemon running at %s? try `task serve` or `task up`: %w", cfg.Daemon, err)
		}
		return render.LoadResult{}, fmt.Errorf("start run: %w", err)
	}

	started := time.Now()
	var samples []render.LoadSample

	ticker := time.NewTicker(cfg.Poll)
	defer ticker.Stop()
	deadline := time.After(cfg.Duration)

poll:
	for {
		select {
		case <-deadline:
			break poll
		case <-ctx.Done():
			break poll
		case <-ticker.C:
			t, err := client.GetTelemetry(ctx, &gardenv1.GetTelemetryRequest{RunId: run.RunId})
			if err != nil {
				break poll
			}
			samples = append(samples, render.LoadSample{
				Elapsed:   time.Since(started),
				Tick:      t.Tick,
				Published: t.Published,
				Pending:   t.Pending,
			})
		}
	}

	// Finishing outlives an interrupt: a cancelled ctx must not also cancel
	// the call that stops the run, or the burst leaves a run ticking forever
	// against nothing watching it.
	finishCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	summary, err := client.FinishRun(finishCtx, &gardenv1.FinishRunRequest{RunId: run.RunId})
	if err != nil {
		return render.LoadResult{}, fmt.Errorf("finish %s: %w", run.RunId, err)
	}

	return render.LoadResult{
		RunID:     run.RunId,
		Daemon:    cfg.Daemon,
		Requested: cfg.Duration,
		Elapsed:   time.Since(started),
		Controls:  cfg.Controls,
		Samples:   samples,
		Summary:   summary,
	}, nil
}
