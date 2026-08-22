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

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/engine"
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

func readFrame(t *testing.T, conn *websocket.Conn) projection.Frame {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	var frame projection.Frame
	if err := json.Unmarshal(payload, &frame); err != nil {
		t.Fatalf("decode frame %s: %v", payload, err)
	}
	return frame
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
	if first.Type != projection.FrameSnapshot {
		t.Fatalf("first frame type = %q, want %q", first.Type, projection.FrameSnapshot)
	}
	if first.Snapshot == nil {
		t.Fatal("first frame carries no snapshot")
	}
	if first.Snapshot.Tick != 2 {
		t.Errorf("first frame tick = %d, want 2: a new client renders the garden it arrived at",
			first.Snapshot.Tick)
	}
	if first.Catchup != nil {
		t.Error("a new client was sent catch-up records it never asked for")
	}

	clock.Tick(1)
	next := readFrame(t, conn)
	if next.Snapshot == nil || next.Snapshot.Tick != 3 {
		t.Fatalf("second frame = %+v, want tick 3", next.Snapshot)
	}
	if next.Snapshot.Sequence <= first.Snapshot.Sequence {
		t.Errorf("sequence went %d then %d, want it to advance",
			first.Snapshot.Sequence, next.Snapshot.Sequence)
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

	back := dial(t, base+"/v1/runs/run-test/stream?from="+strconv.FormatInt(left.Snapshot.FoldedOffset, 10))

	catchup := readFrame(t, back)
	if catchup.Type != projection.FrameCatchup {
		t.Fatalf("first frame type = %q, want %q", catchup.Type, projection.FrameCatchup)
	}
	if catchup.Catchup == nil || len(catchup.Catchup.Events) == 0 {
		t.Fatal("resumed after three ticks away with nothing to catch up on")
	}
	if catchup.Catchup.From != left.Snapshot.FoldedOffset {
		t.Errorf("catch-up starts at %d, resumed from %d",
			catchup.Catchup.From, left.Snapshot.FoldedOffset)
	}
	if got := catchup.Catchup.From + int64(len(catchup.Catchup.Events)); got != catchup.Catchup.To {
		t.Errorf("catch-up says [%d,%d) but carries %d records",
			catchup.Catchup.From, catchup.Catchup.To, len(catchup.Catchup.Events))
	}

	snap := readFrame(t, back)
	if snap.Type != projection.FrameSnapshot || snap.Snapshot == nil {
		t.Fatalf("second frame type = %q, want a snapshot", snap.Type)
	}
	if snap.Snapshot.FoldedOffset != catchup.Catchup.To {
		t.Errorf("catch-up ends at %d, the snapshot after it stands at %d: the client has a %d record hole",
			catchup.Catchup.To, snap.Snapshot.FoldedOffset,
			snap.Snapshot.FoldedOffset-catchup.Catchup.To)
	}
	if snap.Snapshot.Tick != 5 {
		t.Errorf("resumed snapshot tick = %d, want 5", snap.Snapshot.Tick)
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
	if catchup.Catchup == nil {
		t.Fatalf("first frame type = %q, want catch-up", catchup.Type)
	}
	snap := readFrame(t, conn)
	if snap.Snapshot == nil {
		t.Fatalf("second frame type = %q, want a snapshot", snap.Type)
	}

	garden, _, err := sim.Fold(organisms, catchup.Catchup.Events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if garden.Hash() != snap.Snapshot.Hash {
		t.Errorf("folded catch-up = %s, streamed snapshot = %s", garden.Hash(), snap.Snapshot.Hash)
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
