// Package metrics is the daemon's Prometheus surface.
//
// Every metric here is either unlabeled or labeled only by a bounded
// dimension (event outcome, gRPC method and status code) — never by run ID,
// which is unbounded across a session. Per-run drill-down already exists via
// GetTelemetry; this package is the process-wide view a scrape target wants.
// See docs/decisions/0016-prometheus-metrics-carry-no-run-id-label.md.
package metrics

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// Recorder is the daemon's metrics registry and the typed methods call sites
// use to record against it. A nil *Recorder is safe to call every method on,
// so callers that don't wire one up (mainly tests) need no separate no-op
// type.
type Recorder struct {
	registry *prometheus.Registry

	tickDuration     prometheus.Histogram
	rpcDuration      *prometheus.HistogramVec
	eventsProcessed  *prometheus.CounterVec
	snapshotsDropped prometheus.Counter
	lastPublish      prometheus.Gauge

	// pendingByRun backs the pending gauge. It is a private map rather than a
	// run_id-labeled Prometheus vector — a Gauge here would be last-writer-wins
	// across every run ticking this process, silently reporting whichever run
	// happened to tick most recently instead of the real total, which is wrong
	// in exactly the case this metric exists for: a backlogged run's lag
	// hidden behind an idle run's zero. Summing a private map at scrape time
	// (via GaugeFunc) gets the right number without a run_id label on the
	// exposed series. Entries are removed when a run finishes (ForgetRun), so
	// the map stays bounded by live runs, not every run a session ever saw.
	pendingMu    sync.Mutex
	pendingByRun map[string]float64
}

// New constructs a Recorder with its own registry, so a process wires at
// most one of these rather than reaching for prometheus's global default.
func New() *Recorder {
	reg := prometheus.NewRegistry()
	r := &Recorder{
		registry: reg,
		tickDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "signal_garden_tick_duration_seconds",
			Help:    "Wall-clock time to advance one run one tick.",
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 16), // 0.5ms .. ~16s
		}),
		rpcDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "signal_garden_rpc_duration_seconds",
			Help:    "gRPC call duration, by method and status code. Covers REST traffic too — the gateway dials gRPC over loopback.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "code"}),
		eventsProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "signal_garden_events_processed_total",
			Help: "Events the processor has handled, by outcome.",
		}, []string{"outcome"}),
		snapshotsDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "signal_garden_snapshots_dropped_total",
			Help: "Projection frames dropped because a subscriber's channel was full.",
		}),
		lastPublish: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "signal_garden_last_publish_timestamp_seconds",
			Help: "Unix time of the last projection frame sent to any subscriber. time() minus this is WebSocket freshness.",
		}),
		pendingByRun: make(map[string]float64),
	}

	pendingTotal := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "signal_garden_pending_events",
		Help: "Records appended but not yet folded into a garden, summed across every run this process is serving. Consumer lag.",
	}, r.totalPending)

	reg.MustRegister(
		r.tickDuration,
		r.rpcDuration,
		r.eventsProcessed,
		r.snapshotsDropped,
		r.lastPublish,
		pendingTotal,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return r
}

// Handler serves the registry in the Prometheus exposition format.
func (r *Recorder) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

// ObserveTick records how long one tick took to advance.
func (r *Recorder) ObserveTick(d time.Duration) {
	if r == nil {
		return
	}
	r.tickDuration.Observe(d.Seconds())
}

// ObserveEvent records one processed event's outcome.
func (r *Recorder) ObserveEvent(outcome string) {
	if r == nil {
		return
	}
	r.eventsProcessed.WithLabelValues(outcome).Inc()
}

// ObserveSnapshotDropped records one projection frame dropped for a full
// subscriber channel.
func (r *Recorder) ObserveSnapshotDropped() {
	if r == nil {
		return
	}
	r.snapshotsDropped.Inc()
}

// ObservePublish records that a projection frame was just sent to at least
// one subscriber.
func (r *Recorder) ObservePublish() {
	if r == nil {
		return
	}
	r.lastPublish.SetToCurrentTime()
}

// ObservePending records how many records one run has appended but not yet
// folded, as of the tick that just completed.
func (r *Recorder) ObservePending(runID string, n int) {
	if r == nil {
		return
	}
	r.pendingMu.Lock()
	r.pendingByRun[runID] = float64(n)
	r.pendingMu.Unlock()
}

// ForgetRun drops a run's contribution to the pending total. Call it once a
// run finishes, so the total reflects only runs still capable of falling
// behind, and the map does not grow for the life of the process.
func (r *Recorder) ForgetRun(runID string) {
	if r == nil {
		return
	}
	r.pendingMu.Lock()
	delete(r.pendingByRun, runID)
	r.pendingMu.Unlock()
}

// totalPending sums every live run's pending count. It is the ValueFunc
// behind the signal_garden_pending_events GaugeFunc.
func (r *Recorder) totalPending() float64 {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	var total float64
	for _, n := range r.pendingByRun {
		total += n
	}
	return total
}

// UnaryServerInterceptor times every unary gRPC call and observes
// rpcDuration labeled by method and resulting status code. Also wraps
// reflection and health-check calls registered on the same server — noise
// that isn't worth excluding.
func (r *Recorder) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if r == nil {
			return handler(ctx, req)
		}
		start := time.Now()
		resp, err := handler(ctx, req)
		r.rpcDuration.WithLabelValues(info.FullMethod, status.Code(err).String()).Observe(time.Since(start).Seconds())
		return resp, err
	}
}
