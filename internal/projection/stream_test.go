package projection_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/engine"
	"github.com/damodbear/signal-garden/internal/event"
	gardenv1 "github.com/damodbear/signal-garden/internal/gen/signal/garden/v1"
	"github.com/damodbear/signal-garden/internal/projection"
	"github.com/damodbear/signal-garden/internal/sim"
)

var epoch = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

const organisms = 20

// newHarness serves the stream over a real HTTP listener against a run driven
// by a manual clock, so a test states how many ticks happened rather than
// waiting for them.
func newHarness(t *testing.T) (*engine.Registry, *engine.ManualClock, string) {
	t.Helper()

	clock := engine.NewManualClock(epoch, 100*time.Millisecond)
	reg := engine.NewRegistry(engine.WithClock(clock))
	t.Cleanup(func() {
		if err := reg.Close(); err != nil {
			t.Errorf("Close registry: %v", err)
		}
	})

	mux := http.NewServeMux()
	mux.Handle("GET /v1/runs/{run_id}/stream", projection.Handler(reg))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	if _, err := reg.StartRun(engine.StartRunRequest{
		RunID:        "run-test",
		Seed:         42,
		Organisms:    organisms,
		Controls:     domain.DefaultControls(),
		TickInterval: 100 * time.Millisecond,
	}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	return reg, clock, "ws" + strings.TrimPrefix(server.URL, "http")
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial %s: %v (status %d)", url, err, status)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func readFrame(t *testing.T, conn *websocket.Conn) *gardenv1.ProjectionFrame {
	t.Helper()
	frame := &gardenv1.ProjectionFrame{}
	if err := protojson.Unmarshal(readRaw(t, conn), frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return frame
}

func readRaw(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	return payload
}

// eventsOf turns wire events back into envelopes so a test can fold them. The
// stream is the only place events leave this process, so nothing in the daemon
// needs this direction.
func eventsOf(t *testing.T, in []*gardenv1.Event) []event.Event {
	t.Helper()
	out := make([]event.Event, 0, len(in))
	for _, e := range in {
		out = append(out, event.Event{
			EventID:       e.GetEventId(),
			Type:          event.Type(e.GetEventType()),
			SchemaVersion: int(e.GetSchemaVersion()),
			RunID:         e.GetRunId(),
			EntityID:      e.GetEntityId(),
			PartitionKey:  e.GetPartitionKey(),
			Sequence:      e.GetSequence(),
			OccurredAt:    e.GetOccurredAt(),
			Attempt:       int(e.GetAttempt()),
			Payload: event.Payload{
				Amount:   int(e.GetPayload().GetAmount()),
				Revision: int(e.GetPayload().GetRevision()),
			},
		})
	}
	return out
}

func dialStatus(t *testing.T, url string) int {
	t.Helper()
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		t.Fatal("dial succeeded, want a refused handshake")
	}
	if resp == nil {
		t.Fatalf("dial failed without a response: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestStreamDeliversASnapshotThenUpdates(t *testing.T) {
	_, clock, base := newHarness(t)
	clock.Tick(2)

	conn := dial(t, base+"/v1/runs/run-test/stream")

	first := readFrame(t, conn)
	if first.GetType() != gardenv1.FrameType_FRAME_TYPE_SNAPSHOT {
		t.Fatalf("first frame type = %s, want a snapshot", first.GetType())
	}
	if first.GetSnapshot() == nil {
		t.Fatal("first frame carries no snapshot")
	}
	if first.GetSnapshot().GetTick() != 2 {
		t.Errorf("first frame tick = %d, want 2: a new client renders the garden it arrived at",
			first.GetSnapshot().GetTick())
	}
	if first.GetCatchup() != nil {
		t.Error("a new client was sent catch-up records it never asked for")
	}

	clock.Tick(1)
	next := readFrame(t, conn)
	if next.GetSnapshot() == nil || next.GetSnapshot().GetTick() != 3 {
		t.Fatalf("second frame = %v, want tick 3", next.GetSnapshot())
	}
	if next.GetSnapshot().GetSequence() <= first.GetSnapshot().GetSequence() {
		t.Errorf("sequence went %d then %d, want it to advance",
			first.GetSnapshot().GetSequence(), next.GetSnapshot().GetSequence())
	}
}

// TestStreamResumesWithoutAGap is the reconnect exit criterion at the
// transport: a client that drops, misses ticks, and comes back naming its last
// offset receives the records it missed and then a snapshot that stands exactly
// at the end of them.
func TestStreamResumesWithoutAGap(t *testing.T) {
	_, clock, base := newHarness(t)
	clock.Tick(2)

	conn := dial(t, base+"/v1/runs/run-test/stream")
	left := readFrame(t, conn)
	conn.Close()

	clock.Tick(3)

	back := dial(t, base+"/v1/runs/run-test/stream?from="+strconv.FormatInt(left.GetSnapshot().GetFoldedOffset(), 10))

	catchup := readFrame(t, back)
	if catchup.GetType() != gardenv1.FrameType_FRAME_TYPE_CATCHUP {
		t.Fatalf("first frame type = %s, want a catch-up", catchup.GetType())
	}
	if catchup.GetCatchup() == nil || len(catchup.GetCatchup().GetEvents()) == 0 {
		t.Fatal("resumed after three ticks away with nothing to catch up on")
	}
	if catchup.GetCatchup().GetFrom() != left.GetSnapshot().GetFoldedOffset() {
		t.Errorf("catch-up starts at %d, resumed from %d",
			catchup.GetCatchup().GetFrom(), left.GetSnapshot().GetFoldedOffset())
	}
	if got := catchup.GetCatchup().GetFrom() + int64(len(catchup.GetCatchup().GetEvents())); got != catchup.GetCatchup().GetTo() {
		t.Errorf("catch-up says [%d,%d) but carries %d records",
			catchup.GetCatchup().GetFrom(), catchup.GetCatchup().GetTo(), len(catchup.GetCatchup().GetEvents()))
	}

	snap := readFrame(t, back)
	if snap.GetType() != gardenv1.FrameType_FRAME_TYPE_SNAPSHOT || snap.GetSnapshot() == nil {
		t.Fatalf("second frame type = %s, want a snapshot", snap.GetType())
	}
	if snap.GetSnapshot().GetFoldedOffset() != catchup.GetCatchup().GetTo() {
		t.Errorf("catch-up ends at %d, the snapshot after it stands at %d: the client has a %d record hole",
			catchup.GetCatchup().GetTo(), snap.GetSnapshot().GetFoldedOffset(),
			snap.GetSnapshot().GetFoldedOffset()-catchup.GetCatchup().GetTo())
	}
	if snap.GetSnapshot().GetTick() != 5 {
		t.Errorf("resumed snapshot tick = %d, want 5", snap.GetSnapshot().GetTick())
	}
}

// TestStreamResumeFromZeroRebuildsTheGarden gives the handover teeth. Folding
// the catch-up records into an empty garden must reach the hash of the snapshot
// that follows them, so a dropped, duplicated, or reordered record fails here
// rather than becoming a browser that quietly renders the wrong garden.
func TestStreamResumeFromZeroRebuildsTheGarden(t *testing.T) {
	_, clock, base := newHarness(t)
	clock.Tick(5)

	conn := dial(t, base+"/v1/runs/run-test/stream?from=0")

	catchup := readFrame(t, conn)
	if catchup.GetCatchup() == nil {
		t.Fatalf("first frame type = %s, want catch-up", catchup.GetType())
	}
	snap := readFrame(t, conn)
	if snap.GetSnapshot() == nil {
		t.Fatalf("second frame type = %s, want a snapshot", snap.GetType())
	}

	garden, _, err := sim.Fold(organisms, eventsOf(t, catchup.GetCatchup().GetEvents()))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if garden.Hash() != snap.GetSnapshot().GetHash() {
		t.Errorf("folded catch-up = %s, streamed snapshot = %s", garden.Hash(), snap.GetSnapshot().GetHash())
	}
}

func TestStreamClosesWhenTheRunFinishes(t *testing.T) {
	reg, clock, base := newHarness(t)
	clock.Tick(1)

	conn := dial(t, base+"/v1/runs/run-test/stream")
	readFrame(t, conn)

	if _, err := reg.FinishRun("run-test"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	// The final frame lands first, then the close.
	readFrame(t, conn)
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, _, err := conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		t.Errorf("read after finish: err = %v, want a normal close", err)
	}
}

// TestStreamClosesGoingAwayWhenTheRegistryShutsDown is the regression for the
// bug this session's M2 reconnect demo actually found: docker compose stop
// closed every open stream with CloseNormalClosure ("run finished") even
// though the run was mid-run and came right back on restart. A client reading
// only the close code — the one thing a browser's CloseEvent reliably exposes
// — could not tell that from a run genuinely ending, so it gave up instead of
// reconnecting.
func TestStreamClosesGoingAwayWhenTheRegistryShutsDown(t *testing.T) {
	reg, clock, base := newHarness(t)
	clock.Tick(1)

	conn := dial(t, base+"/v1/runs/run-test/stream")
	readFrame(t, conn)

	if err := reg.Close(); err != nil {
		t.Fatalf("Close registry: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, _, err := conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseGoingAway) {
		t.Errorf("read after registry shutdown: err = %v, want going-away, not the finished run's code", err)
	}
}

func TestStreamRefusesBadRequests(t *testing.T) {
	_, clock, base := newHarness(t)
	clock.Tick(1)

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"unknown run", base + "/v1/runs/run-nope/stream", http.StatusNotFound},
		{"offset past the tail", base + "/v1/runs/run-test/stream?from=99999", http.StatusBadRequest},
		{"offset that is not a number", base + "/v1/runs/run-test/stream?from=soon", http.StatusBadRequest},
		{"negative offset", base + "/v1/runs/run-test/stream?from=-1", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dialStatus(t, tc.url); got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestStreamAndGatewayAgreeOnTheWire is the reason the frames are protobuf.
//
// A client parses one GardenSnapshot type whichever transport delivered it, so
// the two must produce byte-identical JSON for the same message. This compares
// the stream's marshaller against the one grpc-gateway installs by default —
// the one actually serving /v1/runs/{run_id}/snapshot — so a change to either
// side's options fails here rather than in a browser.
func TestStreamAndGatewayAgreeOnTheWire(t *testing.T) {
	_, clock, base := newHarness(t)
	clock.Tick(2)

	conn := dial(t, base+"/v1/runs/run-test/stream")
	streamed := readRaw(t, conn)

	frame := &gardenv1.ProjectionFrame{}
	if err := protojson.Unmarshal(streamed, frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}

	// Resolve the marshaller the same way the running gateway does, rather
	// than reconstructing what its defaults are believed to be.
	_, outbound := runtime.MarshalerForRequest(
		runtime.NewServeMux(),
		httptest.NewRequest(http.MethodGet, "/v1/runs/run-test/snapshot", nil),
	)
	gateway, err := outbound.Marshal(frame.GetSnapshot())
	if err != nil {
		t.Fatalf("gateway marshal: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(streamed, &got); err != nil {
		t.Fatalf("decode streamed frame: %v", err)
	}
	if diff := string(got["snapshot"]); diff != string(gateway) {
		t.Errorf("the two transports disagree on a garden\n stream: %s\ngateway: %s", diff, gateway)
	}
}

// TestStreamUsesTheContractsFieldNames pins the shape a browser actually sees.
// The explicit json_name options are what produce it, and a marshaller
// configured with UseProtoNames on one side only would still pass a round-trip
// test while breaking every client.
func TestStreamUsesTheContractsFieldNames(t *testing.T) {
	_, clock, base := newHarness(t)
	clock.Tick(2)

	conn := dial(t, base+"/v1/runs/run-test/stream")
	raw := string(readRaw(t, conn))

	for _, want := range []string{`"run_id"`, `"folded_offset"`, `"schema_version"`, `"observed_at"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("frame is missing %s\n%s", want, raw)
		}
	}
	for _, unwanted := range []string{`"runId"`, `"foldedOffset"`, `"schemaVersion"`} {
		if strings.Contains(raw, unwanted) {
			t.Errorf("frame carries camelCase %s; json_name should have prevented it", unwanted)
		}
	}

	// int64 encodes as a JSON string under the protobuf JSON mapping. It is
	// uniform across both transports, which is the point: a client never has
	// to know which one a number came from.
	if !strings.Contains(raw, `"tick":"2"`) {
		t.Errorf(`want "tick":"2" as a string, per the protobuf JSON mapping`+"\n%s", raw)
	}
}
