package service

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/damodbear/signal-garden/internal/engine"
	gardenv1 "github.com/damodbear/signal-garden/internal/gen/signal/garden/v1"
)

var epoch = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// newHarness serves the garden service over an in-memory connection, so these
// tests exercise real gRPC encoding and status codes without binding a port.
// The registry runs on a manual clock, so ticks are explicit.
func newHarness(t *testing.T) (gardenv1.GardenServiceClient, *engine.ManualClock) {
	t.Helper()

	clock := engine.NewManualClock(epoch, 100*time.Millisecond)
	runs := engine.NewRegistry(engine.WithClock(clock))

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	gardenv1.RegisterGardenServiceServer(server, New(runs))
	go func() {
		if err := server.Serve(listener); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("conn.Close: %v", err)
		}
		server.Stop()
		if err := runs.Close(); err != nil {
			t.Errorf("registry.Close: %v", err)
		}
	})

	return gardenv1.NewGardenServiceClient(conn), clock
}

func defaultControls() *gardenv1.Controls {
	return &gardenv1.Controls{EventsPerTick: 6, RainWeight: 3, GrowthWeight: 2, PestWeight: 1}
}

func startRequest() *gardenv1.StartRunRequest {
	return &gardenv1.StartRunRequest{
		RunId:        "run-test",
		Seed:         42,
		Organisms:    20,
		Controls:     defaultControls(),
		TickInterval: durationpb.New(100 * time.Millisecond),
	}
}

func mustStart(t *testing.T, client gardenv1.GardenServiceClient, req *gardenv1.StartRunRequest) *gardenv1.Run {
	t.Helper()
	run, err := client.StartRun(context.Background(), req)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return run
}

// wantCode fails unless err carries the given gRPC status code. The codes are
// the part of the contract a client actually branches on.
func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got no error", want)
	}
	if got := status.Code(err); got != want {
		t.Errorf("code = %s, want %s (%v)", got, want, err)
	}
}

func TestStartRunAndGetRun(t *testing.T) {
	client, clock := newHarness(t)

	started := mustStart(t, client, startRequest())
	if started.GetRunId() != "run-test" {
		t.Errorf("run id = %q, want run-test", started.GetRunId())
	}
	if started.GetState() != gardenv1.RunState_RUN_STATE_RUNNING {
		t.Errorf("state = %s, want running", started.GetState())
	}
	if started.GetSchemaVersion() == 0 {
		t.Error("schema_version is unset; replay messages must carry it")
	}
	if got := started.GetTickInterval().AsDuration(); got != 100*time.Millisecond {
		t.Errorf("tick interval = %s, want 100ms", got)
	}
	if started.GetFinishedAt() != nil {
		t.Error("a running run reported finished_at")
	}

	clock.Tick(4)

	got, err := client.GetRun(context.Background(), &gardenv1.GetRunRequest{RunId: "run-test"})
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.GetTick() != 4 {
		t.Errorf("tick = %d, want 4", got.GetTick())
	}
	if got.GetControls().GetEventsPerTick() != 6 {
		t.Errorf("controls = %+v, want the ones the run started with", got.GetControls())
	}
}

func TestStartRunGeneratesRunID(t *testing.T) {
	client, _ := newHarness(t)

	req := startRequest()
	req.RunId = ""
	run := mustStart(t, client, req)

	if run.GetRunId() == "" {
		t.Fatal("service returned an empty run id")
	}
}

func TestStartRunDuplicateIsAlreadyExists(t *testing.T) {
	client, _ := newHarness(t)
	mustStart(t, client, startRequest())

	_, err := client.StartRun(context.Background(), startRequest())
	wantCode(t, err, codes.AlreadyExists)
}

func TestStartRunInvalidIsInvalidArgument(t *testing.T) {
	client, _ := newHarness(t)

	tests := []struct {
		name   string
		mutate func(*gardenv1.StartRunRequest)
	}{
		{"no organisms", func(r *gardenv1.StartRunRequest) { r.Organisms = 0 }},
		{"no controls", func(r *gardenv1.StartRunRequest) { r.Controls = nil }},
		{"zero rate", func(r *gardenv1.StartRunRequest) { r.Controls.EventsPerTick = 0 }},
		{"empty event mix", func(r *gardenv1.StartRunRequest) {
			r.Controls = &gardenv1.Controls{EventsPerTick: 5}
		}},
		{"negative max ticks", func(r *gardenv1.StartRunRequest) { r.MaxTicks = -1 }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := startRequest()
			req.RunId = ""
			tc.mutate(req)

			_, err := client.StartRun(context.Background(), req)
			wantCode(t, err, codes.InvalidArgument)
		})
	}
}

func TestUpdateControlsReportsEffectiveTick(t *testing.T) {
	client, clock := newHarness(t)
	mustStart(t, client, startRequest())
	clock.Tick(3)

	next := &gardenv1.Controls{EventsPerTick: 15, RainWeight: 1, GrowthWeight: 1, PestWeight: 4}
	rev, err := client.UpdateControls(context.Background(), &gardenv1.UpdateControlsRequest{
		RunId:    "run-test",
		Controls: next,
	})
	if err != nil {
		t.Fatalf("UpdateControls: %v", err)
	}
	if rev.GetRevision() != 1 {
		t.Errorf("revision = %d, want 1", rev.GetRevision())
	}
	if rev.GetEffectiveTick() != 3 {
		t.Errorf("effective tick = %d, want 3", rev.GetEffectiveTick())
	}
	if rev.GetControls().GetEventsPerTick() != 15 {
		t.Errorf("echoed controls = %+v, want the ones sent", rev.GetControls())
	}

	clock.Tick(1)
	run, err := client.GetRun(context.Background(), &gardenv1.GetRunRequest{RunId: "run-test"})
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.GetControls().GetEventsPerTick() != 15 {
		t.Errorf("controls = %+v after the effective tick, want the update applied", run.GetControls())
	}
}

func TestUpdateControlsRejectsBadInput(t *testing.T) {
	client, _ := newHarness(t)
	mustStart(t, client, startRequest())

	_, err := client.UpdateControls(context.Background(), &gardenv1.UpdateControlsRequest{RunId: "run-test"})
	wantCode(t, err, codes.InvalidArgument)

	_, err = client.UpdateControls(context.Background(), &gardenv1.UpdateControlsRequest{
		RunId:    "run-test",
		Controls: &gardenv1.Controls{EventsPerTick: 0, RainWeight: 1},
	})
	wantCode(t, err, codes.InvalidArgument)
}

func TestPauseAndResume(t *testing.T) {
	client, clock := newHarness(t)
	mustStart(t, client, startRequest())
	clock.Tick(2)

	paused, err := client.PauseRun(context.Background(), &gardenv1.PauseRunRequest{RunId: "run-test", Paused: true})
	if err != nil {
		t.Fatalf("PauseRun: %v", err)
	}
	if paused.GetState() != gardenv1.RunState_RUN_STATE_PAUSED {
		t.Fatalf("state = %s, want paused", paused.GetState())
	}

	clock.Tick(3)
	still, err := client.GetRun(context.Background(), &gardenv1.GetRunRequest{RunId: "run-test"})
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if still.GetTick() != 2 {
		t.Errorf("tick = %d while paused, want 2", still.GetTick())
	}

	resumed, err := client.PauseRun(context.Background(), &gardenv1.PauseRunRequest{RunId: "run-test", Paused: false})
	if err != nil {
		t.Fatalf("PauseRun(paused=false): %v", err)
	}
	if resumed.GetState() != gardenv1.RunState_RUN_STATE_RUNNING {
		t.Errorf("state = %s, want running", resumed.GetState())
	}

	clock.Tick(1)
	after, err := client.GetRun(context.Background(), &gardenv1.GetRunRequest{RunId: "run-test"})
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if after.GetTick() != 3 {
		t.Errorf("tick = %d, want 3; a resumed run continues where it paused", after.GetTick())
	}
}

func TestSnapshotAndTelemetry(t *testing.T) {
	client, clock := newHarness(t)
	mustStart(t, client, startRequest())
	clock.Tick(5)

	snap, err := client.GetSnapshot(context.Background(), &gardenv1.GetSnapshotRequest{RunId: "run-test"})
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if snap.GetHash() == "" {
		t.Error("snapshot carried no hash; replay cannot be verified without it")
	}
	if len(snap.GetOrganisms()) != 20 {
		t.Errorf("organisms = %d, want 20", len(snap.GetOrganisms()))
	}
	if snap.GetStats().GetOrganisms() != 20 {
		t.Errorf("stats organisms = %d, want 20", snap.GetStats().GetOrganisms())
	}
	if snap.GetTick() != 5 {
		t.Errorf("snapshot tick = %d, want 5", snap.GetTick())
	}
	if snap.GetSchemaVersion() == 0 {
		t.Error("snapshot schema_version is unset")
	}

	tel, err := client.GetTelemetry(context.Background(), &gardenv1.GetTelemetryRequest{RunId: "run-test"})
	if err != nil {
		t.Fatalf("GetTelemetry: %v", err)
	}
	if want := int64(5 * 6); tel.GetPublished() != want {
		t.Errorf("published = %d, want %d", tel.GetPublished(), want)
	}
	if tel.GetProcessor().GetReceived() != tel.GetPublished() {
		t.Errorf("received %d of %d published", tel.GetProcessor().GetReceived(), tel.GetPublished())
	}
	if tel.GetProcessor().GetByType()["rain"] == 0 {
		t.Error("no rain events counted; the by_type map did not survive the wire")
	}
	if tel.GetPending() != 0 {
		t.Errorf("pending = %d, want 0 until M2", tel.GetPending())
	}
}

func TestFinishRunReturnsSummary(t *testing.T) {
	client, clock := newHarness(t)
	mustStart(t, client, startRequest())
	clock.Tick(6)

	summary, err := client.FinishRun(context.Background(), &gardenv1.FinishRunRequest{RunId: "run-test"})
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if summary.GetRun().GetState() != gardenv1.RunState_RUN_STATE_FINISHED {
		t.Errorf("state = %s, want finished", summary.GetRun().GetState())
	}
	if summary.GetRun().GetFinishedAt() == nil {
		t.Error("finished_at was not set on a finished run")
	}
	if summary.GetSnapshot().GetHash() == "" {
		t.Error("summary carried no snapshot hash")
	}
	if summary.GetTelemetry().GetPublished() == 0 {
		t.Error("summary carried no telemetry")
	}

	// Retrying must be safe: the UI may send it twice.
	again, err := client.FinishRun(context.Background(), &gardenv1.FinishRunRequest{RunId: "run-test"})
	if err != nil {
		t.Fatalf("second FinishRun: %v", err)
	}
	if again.GetSnapshot().GetHash() != summary.GetSnapshot().GetHash() {
		t.Error("a repeated FinishRun changed the summary")
	}
}

func TestFinishedRunRejectsCommands(t *testing.T) {
	client, _ := newHarness(t)
	mustStart(t, client, startRequest())
	if _, err := client.FinishRun(context.Background(), &gardenv1.FinishRunRequest{RunId: "run-test"}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	_, err := client.UpdateControls(context.Background(), &gardenv1.UpdateControlsRequest{
		RunId:    "run-test",
		Controls: defaultControls(),
	})
	wantCode(t, err, codes.FailedPrecondition)

	_, err = client.PauseRun(context.Background(), &gardenv1.PauseRunRequest{RunId: "run-test", Paused: true})
	wantCode(t, err, codes.FailedPrecondition)
}

func TestUnknownRunIsNotFound(t *testing.T) {
	client, _ := newHarness(t)
	ctx := context.Background()

	_, err := client.GetRun(ctx, &gardenv1.GetRunRequest{RunId: "nope"})
	wantCode(t, err, codes.NotFound)

	_, err = client.GetSnapshot(ctx, &gardenv1.GetSnapshotRequest{RunId: "nope"})
	wantCode(t, err, codes.NotFound)

	_, err = client.GetTelemetry(ctx, &gardenv1.GetTelemetryRequest{RunId: "nope"})
	wantCode(t, err, codes.NotFound)

	_, err = client.FinishRun(ctx, &gardenv1.FinishRunRequest{RunId: "nope"})
	wantCode(t, err, codes.NotFound)

	_, err = client.PauseRun(ctx, &gardenv1.PauseRunRequest{RunId: "nope", Paused: true})
	wantCode(t, err, codes.NotFound)

	_, err = client.UpdateControls(ctx, &gardenv1.UpdateControlsRequest{RunId: "nope", Controls: defaultControls()})
	wantCode(t, err, codes.NotFound)
}

func TestMaxTicksFinishesRunOverRPC(t *testing.T) {
	client, clock := newHarness(t)
	req := startRequest()
	req.MaxTicks = 3
	mustStart(t, client, req)

	clock.Tick(9)

	run, err := client.GetRun(context.Background(), &gardenv1.GetRunRequest{RunId: "run-test"})
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.GetState() != gardenv1.RunState_RUN_STATE_FINISHED {
		t.Errorf("state = %s, want finished", run.GetState())
	}
	if run.GetTick() != 3 {
		t.Errorf("tick = %d, want it to stop at max_ticks 3", run.GetTick())
	}
}
