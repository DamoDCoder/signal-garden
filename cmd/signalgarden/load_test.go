package main

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/engine"
	gardenv1 "github.com/damodbear/signal-garden/internal/gen/signal/garden/v1"
	"github.com/damodbear/signal-garden/internal/service"
)

// testDaemon starts a real gRPC server backed by a real engine.Registry, on a
// loopback port, and returns its address. This is the production code path —
// runLoad dials it exactly as it would dial a real signalgardend.
func testDaemon(t *testing.T) string {
	t.Helper()

	runs := engine.NewRegistry(engine.WithTickInterval(10 * time.Millisecond))
	t.Cleanup(func() { _ = runs.Close() })

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	gardenv1.RegisterGardenServiceServer(grpcServer, service.New(runs))
	go grpcServer.Serve(lis)
	t.Cleanup(grpcServer.Stop)

	return lis.Addr().String()
}

func TestRunLoadDrivesARealDaemon(t *testing.T) {
	daemon := testDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := runLoad(ctx, loadConfig{
		Daemon:    daemon,
		RunID:     "run-load-test",
		Seed:      42,
		Organisms: 10,
		Controls: domain.Controls{
			EventsPerTick: 10, RainWeight: 3, GrowthWeight: 2, PestWeight: 1,
			WorkerCount: 1, BatchSize: 2, // capacity 2/tick, well below 10/tick production
		},
		TickInterval: 10 * time.Millisecond,
		Duration:     300 * time.Millisecond,
		Poll:         50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("runLoad: %v", err)
	}

	if result.RunID != "run-load-test" {
		t.Errorf("RunID = %q, want run-load-test", result.RunID)
	}
	if result.Controls.WorkerCount != 1 || result.Controls.BatchSize != 2 {
		t.Errorf("Controls = %+v, want worker_count=1 batch_size=2 echoed back", result.Controls)
	}
	if len(result.Samples) == 0 {
		t.Fatal("no telemetry samples collected")
	}
	if result.Summary == nil || result.Summary.Telemetry == nil {
		t.Fatal("FinishRun did not return a telemetry-bearing summary")
	}
	if result.Summary.Telemetry.Tick == 0 {
		t.Error("tick = 0, want the run to have advanced during the burst")
	}
	// Capacity (2/tick) is well below production (10/tick), so a real
	// backlog should have built up somewhere in the timeline — this is the
	// behavior the load generator exists to make visible.
	if result.PeakPending() == 0 {
		t.Error("PeakPending() = 0, want a nonzero backlog given capacity below production")
	}
}

func TestRunLoadFailsClearlyWhenNoDaemonIsListening(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := runLoad(ctx, loadConfig{
		Daemon:       "127.0.0.1:1", // nothing listens on port 1
		Organisms:    1,
		Controls:     domain.Controls{EventsPerTick: 1, RainWeight: 1},
		TickInterval: 10 * time.Millisecond,
		Duration:     100 * time.Millisecond,
		Poll:         50 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected an error when no daemon is listening")
	}
}
