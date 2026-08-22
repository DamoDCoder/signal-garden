// Package projection serves the garden projection stream over WebSockets.
//
// It is a read transport and not a second command API: nothing a client sends
// changes a run, and every control path stays on gRPC or the generated REST
// routes. That boundary is why the stream is not a gRPC method and carries no
// protobuf — see docs/architecture.md, and docs/contracts.md for the frame
// shapes.
//
// The gateway owns connection state and nothing else. Run lifecycle, the
// snapshot fan-out, and the catch-up read all belong to internal/engine, which
// serializes them against the run's own goroutine; this package turns what it
// hands back into frames.
package projection

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"github.com/damodbear/signal-garden/internal/engine"
	"github.com/damodbear/signal-garden/internal/event"
	"github.com/damodbear/signal-garden/internal/eventlog"
)

// Frame types on the stream.
const (
	// FrameCatchup carries the records a reconnecting client missed. It
	// arrives at most once, before any snapshot, and only when the client
	// asked to resume.
	FrameCatchup = "catchup"

	// FrameSnapshot carries one projection frame.
	FrameSnapshot = "snapshot"
)

// Timeouts. A projection stream is paced by the run's tick interval, so these
// are generous: the point is to notice a dead peer eventually, not to police
// latency.
const (
	pingEvery   = 20 * time.Second
	pongWait    = 60 * time.Second
	writeWait   = 10 * time.Second
	maxClientIn = 512
)

// Frame is one message on the stream. Exactly one of Catchup or Snapshot is
// set, and Type says which — a client switches on Type rather than sniffing
// which field is present.
type Frame struct {
	Type  string `json:"type"`
	RunID string `json:"run_id"`

	// Catchup is the records between the offset the client resumed from and
	// the garden the next snapshot describes.
	Catchup *Catchup `json:"catchup,omitempty"`

	Snapshot *engine.GardenSnapshot `json:"snapshot,omitempty"`
}

// Catchup is the gap a reconnecting client missed, in log order.
//
// From and To bound it so a client can check the handover joins up with the
// offset it asked for and the folded_offset of the snapshot that follows,
// rather than trusting the length of a slice.
type Catchup struct {
	From   int64         `json:"from"`
	To     int64         `json:"to"`
	Events []event.Event `json:"events"`
}

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
			frame := Frame{
				Type:    FrameCatchup,
				RunID:   runID,
				Catchup: &Catchup{From: from, To: from + int64(len(missed)), Events: missed},
			}
			if err := write(conn, frame); err != nil {
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
			if err := write(conn, Frame{Type: FrameSnapshot, RunID: runID, Snapshot: &snap}); err != nil {
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

func write(conn *websocket.Conn, frame Frame) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}
