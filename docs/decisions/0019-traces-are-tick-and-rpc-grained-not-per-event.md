# 0019: Traces are tick- and RPC-grained, not per-event

- **Date:** 2026-08-29
- **Status:** Accepted

## Context

`docs/decisions/0016` kept Prometheus metrics free of `run_id` labels for cardinality reasons and
deferred run/event correlation to traces explicitly: "a trace carries whatever IDs it wants without
becoming a permanently retained Prometheus series." This slice builds that. Two questions had to be
answered before any code: where do spans go, and how fine-grained are they.

**Where spans go.** This project has stayed local-first throughout — Prometheus metrics needed no
collector, `curl /metrics` was enough. A trace waterfall genuinely needs a viewer, though; there is
no equivalent of `curl`. The decision was to export OTLP/gRPC to an optional, externally-run local
endpoint (e.g. a `docker run jaegertracing/all-in-one` one-liner) rather than a stdout exporter (too
low-ceiling to be worth calling "traces" — no waterfall, no span-hierarchy browsing) or a collector
this repository bundles or owns (which neither repo's Compose file does today, and adding one would
be new permanent infrastructure for a demo feature).

**How fine-grained.** The event-processing path handles hundreds of events per tick. A span per
event would be pure noise: the overwhelming majority are the unremarkable `Applied`/`NoEffect`
case, and nothing about tracing that volume would be more informative than the existing
`signal_garden_events_processed_total` counter already is in aggregate.

## Options Considered

1. **Per-event spans.** Maximal correlation, but noisy at this project's scale and — because
   `Sim.Step`/`fold` take no `context.Context` today — would require threading one through the
   simulation package, the batch runner (`internal/run`), and every test that calls `Step()`. A
   real, invasive signature change for a feature whose value at this volume is doubtful.
2. **RPC spans only**, via the standard `otelgrpc` contrib instrumentation. Zero custom span code,
   real correlation for the control plane (a `StartRun` or `FinishRun` trace shows method, status,
   duration), but says nothing about what happened *inside* a tick — no visibility into the
   snapshot-save retries the failure-injection slice just added, for instance.
3. **RPC spans + one span per tick**, with per-event detail folded into span *events* on the tick
   span rather than child spans — added only for what's actually interesting (a snapshot save that
   retried or failed), not for the ordinary case.

## Decision

Option 3. `otelgrpc.NewServerHandler` as a `grpc.StatsHandler` alongside the existing metrics
interceptor covers RPC spans with no custom code — the same server both interceptors sit on, so it
covers REST traffic too for the same reason the metrics interceptor does. Tick spans are added at
the exact call site that already wraps `Step()` for the tick-duration metric —
`(*liveRun).advance()` in `internal/engine/engine.go` — with `run.id`/`tick` attributes. Before/after
snapshots of `Sim.SnapshotSaveRetries()`/`SnapshotSaveFailures()` (already-exported getters from
`docs/decisions/0018`) let `advance()` add a span event when *that tick's* snapshot save retried or
failed, entirely from outside `internal/sim` — `Sim`/`Save` know nothing about tracing. This is
exactly the "zero context-threading into sim" property option 1 would have broken.

An empty `SIGNAL_GARDEN_OTEL_ENDPOINT` (the default) returns a noop `TracerProvider` — every span
call is inert and nothing dials anywhere, so `task serve` behaves exactly as before tracing existed
unless a backend is explicitly configured.

## Consequences

Run/event correlation exists for exactly two things: which run and tick a piece of tick-level work
belongs to, and which specific ticks had a snapshot-save retry — genuinely useful for the demo this
project is about (comparing worker/batch capacity, watching failure injection recover) without new
infrastructure this repository must run. Per-event detail (which specific event was rejected or hit
`unknown_entity`) is not traceable yet.

## What Would Revisit This

- A real need to see individual event outcomes on a trace, not just the tick they happened in — at
  that point `Sim.Step`/`fold` gaining a `context.Context` parameter becomes worth the signature
  change across the batch runner and its tests, which this decision deliberately avoided.
- A collector-based setup (e.g. an OTel Collector doing sampling/processing before Jaeger) becoming
  worth this repository owning — not needed at today's local, single-daemon scale.
