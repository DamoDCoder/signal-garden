// Command signalgardend serves the Signal Garden control surface.
//
// It runs two listeners over one run engine: gRPC for internal callers, and the
// generated REST gateway for public HTTP/JSON. The gateway dials the gRPC
// listener rather than calling the service in process, so the hop the
// architecture describes is a real one and its failures are visible locally
// instead of only in a deployed topology.
//
// The WebSocket projection stream is not here yet; it arrives with the next M1
// slice, alongside the React control surface.
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
	gardenv1 "github.com/damodbear/signal-garden/internal/gen/signal/garden/v1"
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
		tickInterval = flagDuration("SIGNAL_GARDEN_TICK_INTERVAL", engine.DefaultTickInterval)
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runs := engine.NewRegistry(engine.WithTickInterval(tickInterval))
	defer runs.Close()

	grpcServer := grpc.NewServer()
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
		Handler:           routes(gateway, &ready),
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

// newGateway dials the gRPC listener and returns the generated REST handler.
func newGateway(ctx context.Context, target string) (http.Handler, error) {
	mux := runtime.NewServeMux()
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if err := gardenv1.RegisterGardenServiceHandlerFromEndpoint(ctx, mux, target, opts); err != nil {
		return nil, fmt.Errorf("register gateway against %s: %w", target, err)
	}
	return mux, nil
}

// routes assembles the public HTTP surface: generated REST under /v1, plus the
// two checks Compose health gating needs.
//
// Liveness answers "is this process running"; readiness answers "can it serve".
// They differ during startup and shutdown, which is exactly when a health check
// has to tell them apart.
func routes(gateway http.Handler, ready *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/", gateway)

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
