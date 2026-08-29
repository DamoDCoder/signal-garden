// Command signalgardend serves the Signal Garden control surface.
//
// It runs two listeners over one run engine: gRPC for internal callers, and the
// generated REST gateway for public HTTP/JSON. The gateway dials the gRPC
// listener rather than calling the service in process, so the hop the
// architecture describes is a real one and its failures are visible locally
// instead of only in a deployed topology.
//
// The WebSocket projection stream is served directly from this process rather
// than through the gateway: it is a read stream, not a gRPC method, and it
// reads the run engine the same way the service does.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/damodbear/signal-garden/internal/engine"
	"github.com/damodbear/signal-garden/internal/eventlog"
	gardenv1 "github.com/damodbear/signal-garden/internal/gen/signal/garden/v1"
	"github.com/damodbear/signal-garden/internal/metrics"
	"github.com/damodbear/signal-garden/internal/projection"
	"github.com/damodbear/signal-garden/internal/service"
)

// shutdownGrace bounds how long in-flight requests have to finish once a
// signal arrives, so a stuck handler cannot hold the process open.
const shutdownGrace = 5 * time.Second

func main() {
	if err := realMain(); err != nil {
		fmt.Fprintf(os.Stderr, "signalgardend: %v\n", err)
		os.Exit(1)
	}
}

func realMain() error {
	var (
		grpcAddr     = flagString("SIGNAL_GARDEN_GRPC_ADDR", ":9090")
		httpAddr     = flagString("SIGNAL_GARDEN_HTTP_ADDR", ":8080")
		dataDir      = flagString("SIGNAL_GARDEN_DATA_DIR", "data")
		corsOrigin   = flagString("SIGNAL_GARDEN_CORS_ORIGIN", "*")
		tickInterval = flagDuration("SIGNAL_GARDEN_TICK_INTERVAL", engine.DefaultTickInterval)
	)
	corrupt, err := corruptPolicy(flagString("SIGNAL_GARDEN_ON_CORRUPT", "refuse"))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rec := metrics.New()

	runs := engine.NewRegistry(
		engine.WithTickInterval(tickInterval),
		engine.WithLogs(engine.DirectoryLogs(dataDir)),
		engine.WithCorruptPolicy(corrupt),
		engine.WithMetrics(rec),
	)
	defer runs.Close()
	fmt.Fprintf(os.Stderr, "run history under %s, on corrupt: %s\n", dataDir, corrupt)

	// Recover before serving. A client that connects during startup should
	// find the runs that were going when the daemon stopped, not an empty
	// registry that fills in a moment later.
	if err := recoverRuns(runs, dataDir); err != nil {
		// A run that cannot be recovered is not a reason to refuse to
		// start: the others are fine, and refusing would take a whole
		// daemon down for one bad directory. It must be loud, though.
		fmt.Fprintf(os.Stderr, "recovery: %v\n", err)
	}
	if corsOrigin == "" {
		fmt.Fprintln(os.Stderr, "cross-origin requests refused")
	} else {
		fmt.Fprintf(os.Stderr, "cross-origin requests allowed from %s\n", corsOrigin)
	}

	grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(rec.UnaryServerInterceptor()))
	gardenv1.RegisterGardenServiceServer(grpcServer, service.New(runs))

	// Reflection makes the service explorable with grpcurl during local
	// development, which is the only environment this build targets.
	reflection.Register(grpcServer)

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	listener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", grpcAddr, err)
	}

	errs := make(chan error, 2)
	go func() {
		fmt.Fprintf(os.Stderr, "grpc listening on %s\n", listener.Addr())
		if err := grpcServer.Serve(listener); err != nil {
			errs <- fmt.Errorf("grpc server: %w", err)
		}
	}()

	var ready atomic.Bool
	gateway, err := newGateway(ctx, listener.Addr().String())
	if err != nil {
		grpcServer.Stop()
		return err
	}
	ready.Store(true)

	httpServer := &http.Server{
		Addr:              httpAddr,
		Handler:           withCORS(routes(gateway, projection.Handler(runs), &ready, rec), corsOrigin),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		fmt.Fprintf(os.Stderr, "http listening on %s\n", httpAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case err := <-errs:
		ready.Store(false)
		grpcServer.Stop()
		return err
	case <-ctx.Done():
	}

	// Report unready before tearing anything down, so a load balancer stops
	// sending work while in-flight requests drain.
	ready.Store(false)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	fmt.Fprintln(os.Stderr, "shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	grpcServer.GracefulStop()
	return nil
}

// recoverRuns brings back the runs a previous process left interrupted.
//
// The registry does not know where logs live, so the IDs are read here and
// handed over. A data directory that does not exist yet lists no runs, which is
// what a first start looks like.
func recoverRuns(runs *engine.Registry, dataDir string) error {
	ids, err := eventlog.RunIDs(dataDir)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}

	revived, err := runs.Recover(ids)
	for _, r := range revived {
		fmt.Fprintf(os.Stderr, "resumed %s at tick %d, %s\n", r.RunID, r.Tick, r.State)
	}
	// Only count runs as finished when nothing failed. A failure is also a
	// run that did not come back, and reporting it as "already finished"
	// would describe a problem as a normal outcome.
	if skipped := len(ids) - len(revived); skipped > 0 && err == nil {
		fmt.Fprintf(os.Stderr, "%d of %d runs had already finished\n", skipped, len(ids))
	}
	return err
}

// newGateway dials the gRPC listener and returns the generated REST handler.
func newGateway(ctx context.Context, target string) (http.Handler, error) {
	mux := runtime.NewServeMux()
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if err := gardenv1.RegisterGardenServiceHandlerFromEndpoint(ctx, mux, target, opts); err != nil {
		return nil, fmt.Errorf("register gateway against %s: %w", target, err)
	}
	return mux, nil
}

// routes assembles the public HTTP surface: generated REST under /v1, the
// projection stream, the Prometheus scrape target, plus the two checks
// Compose health gating needs.
//
// Liveness answers "is this process running"; readiness answers "can it serve".
// They differ during startup and shutdown, which is exactly when a health check
// has to tell them apart.
func routes(gateway http.Handler, stream http.Handler, ready *atomic.Bool, rec *metrics.Recorder) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/", gateway)
	mux.Handle("GET /metrics", rec.Handler())

	// The stream sits under /v1 and is not a generated route, so it needs a
	// more specific pattern than the gateway's prefix. It wins because
	// ServeMux prefers the more specific pattern, not because of the order
	// these are registered in.
	mux.Handle("GET /v1/runs/{run_id}/stream", stream)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writePlain(w, http.StatusOK, "ok")
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			writePlain(w, http.StatusServiceUnavailable, "not ready")
			return
		}
		writePlain(w, http.StatusOK, "ready")
	})

	return mux
}

func writePlain(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintln(w, body)
}

// corruptPolicy parses SIGNAL_GARDEN_ON_CORRUPT.
//
// An unrecognised value is refused rather than defaulted. The whole point of
// this setting is that somebody chose it deliberately, and a typo that silently
// selected the permissive branch would be the worst possible way to find out
// how it was spelled.
func corruptPolicy(value string) (engine.CorruptPolicy, error) {
	switch value {
	case "refuse":
		return engine.RefuseCorrupt, nil
	case "continue":
		return engine.ContinueCorrupt, nil
	default:
		return engine.RefuseCorrupt, fmt.Errorf("SIGNAL_GARDEN_ON_CORRUPT=%q is not refuse or continue", value)
	}
}

// flagString reads configuration from the environment with a default.
// Compose passes environment variables, and there is no flag here a container
// would set differently.
func flagString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func flagDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "signalgardend: %s=%q is not a duration, using %s\n", key, v, fallback)
		return fallback
	}
	return d
}
