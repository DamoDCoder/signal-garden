// Package projection serves the garden projection stream over WebSockets.
//
// It is a read transport and not a second command API: nothing a client sends
// changes a run, and every control path stays on gRPC or the generated REST
// routes. That boundary is why the stream is not a gRPC method — but it is
// still the same contract. Frames are ProjectionFrame messages marshalled with
// protojson, so a client parses a GardenSnapshot exactly as it would one from
// GET /v1/runs/{run_id}/snapshot, with the same field names and the same types.
// See docs/architecture.md and docs/contracts.md.
//
// The gateway owns connection state and nothing else. Run lifecycle, the
// snapshot fan-out, and the catch-up read all belong to internal/engine, which
// serializes them against the run's own goroutine; this package turns what it
// hands back into frames.
package projection

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/damodbear/signal-garden/internal/engine"
	"github.com/damodbear/signal-garden/internal/event"
	"github.com/damodbear/signal-garden/internal/eventlog"
	"github.com/damodbear/signal-garden/internal/wire"
)

// marshal produces the same JSON the REST gateway does.
//
// Field names come from the explicit json_name options in the contract rather
// than from here, so the two transports cannot drift apart by configuration.
// EmitUnpopulated does have to be set, because grpc-gateway's default marshaler
// sets it: without it a zero-valued field is present over REST and missing over
// the stream, and a client reading snapshot.sequence would get 0 from one
// transport and undefined from the other at exactly the moment a run starts.
// TestStreamAndGatewayAgreeOnTheWire is what keeps the two in step.
var marshal = protojson.MarshalOptions{EmitUnpopulated: true}

// Timeouts. A projection stream is paced by the run's tick interval, so these
// are generous: the point is to notice a dead peer eventually, not to police
// latency.
const (
	pingEvery   = 20 * time.Second
	pongWait    = 60 * time.Second
	writeWait   = 10 * time.Second
	maxClientIn = 512
)

// Handler serves the projection stream for one run.
//
// Register it at a pattern with a {run_id} wildcard. The optional `from` query
// parameter is a log offset: with it the client resumes and is told what it
// missed, without it the client is new and starts at the current garden.
func Handler(reg *engine.Registry) http.Handler {
	upgrader := websocket.Upgrader{
		// Any origin may read a local garden. This is a read stream on a
		// local-first daemon; the public gateway that would authenticate
		// and rate-limit it is a deployment concern, per docs/contracts.md.
		CheckOrigin: func(*http.Request) bool { return true },
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("run_id")
		if runID == "" {
			http.Error(w, "run_id is required", http.StatusBadRequest)
			return
		}

		from, resuming, err := offsetOf(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Subscribing before the upgrade means a rejected stream is an
		// ordinary HTTP status a fetch can read, rather than a WebSocket
		// that opens and immediately closes with a code.
		var (
			sub    *engine.Subscription
			missed []event.Event
		)
		if resuming {
			sub, missed, err = reg.Resume(runID, 0, from)
		} else {
			sub, err = reg.Subscribe(runID, 0)
		}
		if err != nil {
			writeError(w, err)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// Upgrade has already written its own response.
			sub.Close()
			return
		}
		defer conn.Close()
		defer sub.Close()

		if resuming {
			if err := write(conn, wire.CatchupFrame(runID, from, missed)); err != nil {
				return
			}
		}

		go drain(conn)
		pump(conn, runID, sub)
	})
}

// offsetOf reads the `from` query parameter. A missing parameter means a new
// client; a present one must be a non-negative offset, because a client that
// meant "start me at the beginning" says from=0.
func offsetOf(r *http.Request) (int64, bool, error) {
	raw := r.URL.Query().Get("from")
	if raw == "" {
		return 0, false, nil
	}
	from, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, errors.New("from must be a log offset")
	}
	if from < 0 {
		return 0, false, errors.New("from must not be negative")
	}
	return from, true, nil
}

// writeError maps a subscribe failure onto a status code, using the same
// classification the gRPC service uses: callers branch on the condition, never
// on message text.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrRunNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, eventlog.ErrOffsetOutOfRange):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, engine.ErrRegistryDown), errors.Is(err, engine.ErrRunClosed):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// pump forwards frames until the run closes the subscription or the connection
// fails, then closes the stream cleanly so a client can tell a finished run
// from a dropped one.
func pump(conn *websocket.Conn, runID string, sub *engine.Subscription) {
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()

	for {
		select {
		case snap, ok := <-sub.Snapshots():
			if !ok {
				_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
				_ = conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "run finished"))
				return
			}
			if err := write(conn, wire.SnapshotFrame(runID, snap)); err != nil {
				return
			}
		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// drain reads and discards whatever the client sends. Nothing a client says on
// this socket means anything, but the read loop is what delivers pongs and
// notices a closed peer, so it has to run.
func drain(conn *websocket.Conn) {
	conn.SetReadLimit(maxClientIn)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			// Closing here unblocks the writer, which is otherwise
			// waiting on a tick that may be a whole interval away.
			_ = conn.Close()
			return
		}
	}
}

func write(conn *websocket.Conn, frame proto.Message) error {
	payload, err := marshal.Marshal(frame)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}
