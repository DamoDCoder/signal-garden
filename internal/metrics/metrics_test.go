package metrics

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestObserveTick(t *testing.T) {
	r := New()
	r.ObserveTick(5 * time.Millisecond)

	if got := testutil.CollectAndCount(r.tickDuration); got != 1 {
		t.Fatalf("tickDuration observations = %d, want 1", got)
	}
}

func TestObserveEvent(t *testing.T) {
	r := New()
	r.ObserveEvent("applied")
	r.ObserveEvent("applied")
	r.ObserveEvent("rejected")

	if got := testutil.ToFloat64(r.eventsProcessed.WithLabelValues("applied")); got != 2 {
		t.Errorf("applied count = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.eventsProcessed.WithLabelValues("rejected")); got != 1 {
		t.Errorf("rejected count = %v, want 1", got)
	}
}

func TestObserveSnapshotDropped(t *testing.T) {
	r := New()
	r.ObserveSnapshotDropped()
	r.ObserveSnapshotDropped()

	if got := testutil.ToFloat64(r.snapshotsDropped); got != 2 {
		t.Errorf("snapshotsDropped = %v, want 2", got)
	}
}

func TestObserveSnapshotSaveRetry(t *testing.T) {
	r := New()
	r.ObserveSnapshotSaveRetry()
	r.ObserveSnapshotSaveRetry()

	if got := testutil.ToFloat64(r.snapshotSaveRetries); got != 2 {
		t.Errorf("snapshotSaveRetries = %v, want 2", got)
	}
}

func TestObserveSnapshotSaveFailure(t *testing.T) {
	r := New()
	r.ObserveSnapshotSaveFailure()

	if got := testutil.ToFloat64(r.snapshotSaveFailures); got != 1 {
		t.Errorf("snapshotSaveFailures = %v, want 1", got)
	}
}

func TestObservePending(t *testing.T) {
	r := New()
	r.ObservePending("run-a", 7)

	if got := r.totalPending(); got != 7 {
		t.Errorf("totalPending = %v, want 7", got)
	}

	r.ObservePending("run-a", 0)
	if got := r.totalPending(); got != 0 {
		t.Errorf("totalPending after drain = %v, want 0", got)
	}
}

// TestObservePendingSumsAcrossRuns is the regression for the bug this design
// exists to avoid: a plain Gauge would be last-writer-wins, so a quiet run
// ticking after a backlogged one would silently report the backlog as zero.
func TestObservePendingSumsAcrossRuns(t *testing.T) {
	r := New()
	r.ObservePending("run-backlogged", 112)
	r.ObservePending("run-quiet", 0)

	if got := r.totalPending(); got != 112 {
		t.Errorf("totalPending = %v, want 112 (a quiet run's zero must not erase another run's backlog)", got)
	}
}

// TestForgetRunDropsItsContribution confirms a finished run stops counting
// toward the total, so the pending metric describes only runs still capable
// of falling behind.
func TestForgetRunDropsItsContribution(t *testing.T) {
	r := New()
	r.ObservePending("run-a", 5)
	r.ObservePending("run-b", 3)
	r.ForgetRun("run-a")

	if got := r.totalPending(); got != 3 {
		t.Errorf("totalPending after ForgetRun = %v, want 3", got)
	}
}

func TestObservePublish(t *testing.T) {
	r := New()
	before := time.Now().Unix()
	r.ObservePublish()

	got := testutil.ToFloat64(r.lastPublish)
	if got < float64(before) {
		t.Errorf("lastPublish = %v, want >= %v", got, before)
	}
}

func TestUnaryServerInterceptor(t *testing.T) {
	r := New()
	interceptor := r.UnaryServerInterceptor()

	info := &grpc.UnaryServerInfo{FullMethod: "/signal.garden.v1.GardenService/StartRun"}
	handler := func(ctx context.Context, req any) (any, error) { return nil, nil }
	if _, err := interceptor(context.Background(), nil, info, handler); err != nil {
		t.Fatalf("interceptor: %v", err)
	}

	failing := func(ctx context.Context, req any) (any, error) {
		return nil, status.Error(codes.NotFound, "no such run")
	}
	if _, err := interceptor(context.Background(), nil, info, failing); err == nil {
		t.Fatal("expected error to propagate")
	}

	if got := testutil.CollectAndCount(r.rpcDuration); got != 2 {
		t.Fatalf("rpcDuration series = %d, want 2 (OK and NotFound)", got)
	}
}

func TestHandlerServesExpositionFormat(t *testing.T) {
	r := New()
	r.ObserveTick(time.Millisecond)
	r.ObserveEvent("applied")
	r.ObserveSnapshotDropped()
	r.ObservePublish()
	r.ObservePending("run-a", 3)
	r.ObserveSnapshotSaveRetry()
	r.ObserveSnapshotSaveFailure()
	interceptor := r.UnaryServerInterceptor()
	handler := func(ctx context.Context, req any) (any, error) { return nil, nil }
	_, _ = interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/x"}, handler)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"signal_garden_tick_duration_seconds",
		"signal_garden_rpc_duration_seconds",
		"signal_garden_events_processed_total",
		"signal_garden_snapshots_dropped_total",
		"signal_garden_last_publish_timestamp_seconds",
		"signal_garden_pending_events",
		"signal_garden_snapshot_save_retries_total",
		"signal_garden_snapshot_save_failures_total",
	} {
		if !contains(body, want) {
			t.Errorf("response missing metric %q", want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestNilRecorderIsSafe(t *testing.T) {
	var r *Recorder
	r.ObserveTick(time.Millisecond)
	r.ObserveEvent("applied")
	r.ObserveSnapshotDropped()
	r.ObservePublish()
	r.ObservePending("run-a", 3)
	r.ForgetRun("run-a")
	r.ObserveSnapshotSaveRetry()
	r.ObserveSnapshotSaveFailure()

	interceptor := r.UnaryServerInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/x"}
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }
	resp, err := interceptor(context.Background(), nil, info, handler)
	if err != nil || resp != "ok" {
		t.Fatalf("nil recorder interceptor: resp=%v err=%v", resp, err)
	}

	handlerErr := func(ctx context.Context, req any) (any, error) { return nil, errors.New("boom") }
	if _, err := interceptor(context.Background(), nil, info, handlerErr); err == nil {
		t.Fatal("expected error to propagate through nil recorder")
	}
}
