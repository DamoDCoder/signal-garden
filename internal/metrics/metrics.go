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
	}

	reg.MustRegister(
		r.tickDuration,
		r.rpcDuration,
		r.eventsProcessed,
		r.snapshotsDropped,
		r.lastPublish,
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
