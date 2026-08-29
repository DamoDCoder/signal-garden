# 0016: Prometheus metrics carry no run ID label

- **Date:** 2026-08-29
- **Status:** Accepted

## Context

M3's exit criteria ask that throughput, p50/p95/p99 latency, lag, retries, drops, and WebSocket
freshness be visible. [docs/architecture.md](../architecture.md) already said metrics would be
"correlated with run ID and event ID" — written before there was a metrics implementation to hold
that promise to.

A run ID is not a bounded label. A long local dev session accumulates dozens of them, each one used
once and then never again — exactly the shape Prometheus's own documentation warns labels away
from, because every distinct label combination is a permanent time series the registry never
forgets. A `signal_garden_tick_duration_seconds{run_id=...}` histogram would grow a new series per
run for the life of the process, whether or not anyone is watching that run any more.

## Options Considered

1. **Label everything by `run_id`, delete series on finish.** Correct in principle, but every
   deletion point has to be found and kept in sync with wherever a run stops mattering — `FinishRun`,
   a crash that never reaches it, eviction that doesn't exist yet. A missed path leaks a series
   forever, silently, and the failure mode is a metrics registry that slowly fills memory rather
   than an error anyone sees.
2. **A custom aggregate collector**, queried at scrape time by asking the engine's live runs for
   their current state (as `GetTelemetry` already does, per run, on demand). Sound, but it means
   building cross-goroutine fan-out machinery this slice doesn't otherwise need, to produce a number
   ("worst freshness across N runs") that's less precise than what already exists per-run.
3. **No `run_id` label. Global counters/gauges, plus bounded labels only** (event outcome, gRPC
   method and status code). Per-run drill-down stays where it already lives — the existing
   `GetTelemetry` REST poll.

## Decision

Option 3. Every metric this slice adds is either unlabeled or labeled by a small fixed set of
values known at compile time. `signal_garden_last_publish_timestamp_seconds` is a global gauge —
"how fresh is projection delivery right now" for whichever runs are being watched, not a per-run
breakdown. `signal_garden_snapshots_dropped_total` is a global counter for the same reason.
`signal_garden_events_processed_total` is labeled by outcome (five known values);
`signal_garden_rpc_duration_seconds` is labeled by gRPC method and status code — both closed sets.

Run/event correlation — the thing the architecture doc originally promised — is deferred to
OpenTelemetry traces, not built this slice. Traces are the tool built for exactly this: a trace
carries whatever IDs it wants without becoming a permanently retained Prometheus series, because a
trace is a single emitted record, not a running time series keyed by cardinality.
`docs/architecture.md`'s correlation sentence is amended to say so.

## Consequences

A dashboard can show "how much work is the daemon doing right now" and "how fresh is delivery"
without per-run cardinality risk. It cannot show "which run is slow" from Prometheus alone — that
question goes to `GET /v1/runs/{run_id}/telemetry`, which already answers it, or to a future trace.

## What Would Revisit This

- A real need to compare metrics *across* runs on a dashboard, in a way the REST poll can't serve —
  at which point option 2 (aggregate collector) is worth building, since the fan-out machinery would
  finally be paying for something the REST poll cannot do.
- `TelemetrySnapshot` gaining a `pending`/lag field with real values once the worker-count/batch-size
  slice makes processing concurrent — at that point a *global* lag gauge (still no `run_id`, same
  argument as freshness) becomes worth adding here.
