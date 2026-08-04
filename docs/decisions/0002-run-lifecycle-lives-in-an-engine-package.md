# 0002: Run lifecycle lives in an engine package, not in the gRPC service

- **Date:** 2026-08-05
- **Status:** Accepted

## Context

M1 connects one user action to a live garden projection. That needs something M0 does not have: a run that exists over time. `run.Execute` computes a whole run and returns, so there is nothing to pause, nothing to steer while it is going, and nothing for a WebSocket to stream.

Three consumers need that live run at M1 — the gRPC control service, the WebSocket projection gateway, and the React client through both. The question is where run lifecycle lives.

## Options Considered

1. **Lifecycle inside the gRPC service implementation.** Fewest packages. The service handler owns the ticking goroutine, the run map, and the subscriber list.
2. **A transport-free `internal/engine` package, with gRPC and WebSocket as adapters over it.** One more package, and lifecycle is testable before any codegen exists.
3. **Client-driven ticks.** The browser asks for the next tick. No server-side scheduling at all.

## Decision

Option 2. `internal/engine` owns run lifecycle, control revisions, and projection fan-out, with method shapes matching the service definitions in [contracts.md](../contracts.md). The M1 gRPC service is an adapter over it.

Option 3 was rejected outright: it makes the browser the pacing authority, which contradicts the boundary rule that the processor owns garden state and clients render projections.

## Evidence

Lifecycle in the service handler makes every lifecycle test a transport test. Pausing a run would need a running gRPC server, a client, and a port, and a test for "controls apply on a tick boundary" would be timing-sensitive on top of that. With the engine separate, the same behavior is tested against a manual clock in milliseconds, and `TestEngineMatchesBatchRun` can pin the live path to the batch path by comparing snapshot hashes — a check that has no meaningful transport-level equivalent.

The seam also has to hold twice, not once. WebSocket projection is a separate transport from gRPC by the architecture's own rule, so lifecycle inside the gRPC service would leave the projection gateway reaching into another service's internals for the subscriber list.

Two design points fell out of building it, and both are load-bearing:

- **Control updates stage until the next tick.** A change accepted partway through a tick applies at the following tick boundary, so the tick it applied at is the only timing fact replay needs. Wall-clock acceptance time never enters the simulation, which is what [events.md](../events.md) requires.
- **Slow subscribers lose frames rather than stalling the run.** A projection stream carries whole snapshots, so a late consumer wants the newest one, not a backlog. The drop count is telemetry, not a silent failure.

## What Would Revisit This

- The gRPC service turns out to need lifecycle state the engine does not expose, which would mean the method shapes were copied from the contract without matching its semantics.
- M2 moves run state into the document store, at which point the registry becomes a cache over durable state rather than the authority, and its ownership model needs revisiting.
