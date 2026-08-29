# Known Limits

Things this daemon cannot do, or hasn't measured, with what it does instead. Each one is a
candidate for a change rather than a defect to be quietly worked around — most already have a
decision record behind them; this page is the index, not a duplicate of the reasoning.

## Catch-Up Cost Is Still A Guess

A client resuming from a deep offset reads the whole gap on the run's own goroutine
([0009](decisions/0009-catch-up-is-a-command-to-the-run-not-a-second-reader.md)) — a slice copy at
M2's volumes, flagged as possibly measurable "at M3's load lab." It wasn't: `task load` drives the
gRPC control plane, not the WebSocket projection stream catch-up lives on, so the tool built for
exactly this kind of measurement doesn't reach it. The chain-digest-cost measurement
([0008](decisions/0008-assert-determinism-on-a-chain-not-a-terminal-hash.md#measured-at-m3)) used
what was reachable; this one still needs `task load` (or something like it) taught to speak the
stream first.

## Recovery Time Isn't Measured

M2's crash matrix (`internal/sim/crash_test.go`) proves a run loses nothing across a simulated power
cut, and `fail_snapshot_every` ([0018](decisions/0018-failure-injection-targets-the-snapshot-save-not-event-processing.md))
proves the same for a transient save failure — both repeatable scenarios. Neither times how long
recovery takes. "Repeatable" and "fast enough" are different claims, and only the first one has a
test.

## Worker Count And Batch Size Cap Throughput; They Don't Add It

`worker_count`/`batch_size` read like they should spin up goroutines. They don't —
[0017](decisions/0017-worker-count-and-batch-size-are-a-capacity-model-not-goroutines.md) found
nothing in the event-application path CPU-bound enough to benefit from real parallelism, so they cap
how much of a tick's production folds that tick rather than distributing the work. Raising them past
what a single goroutine can fold in a tick interval doesn't make ticks faster; it just raises the
cap.

## Only The Snapshot Save Retries

`fail_snapshot_every` targets the periodic on-disk snapshot deliberately, not the tick's event-log
append — [0018](decisions/0018-failure-injection-targets-the-snapshot-save-not-event-processing.md)
explains why (the log append is durability-critical, the snapshot is a provably-optional
optimization). A real `Log.Append` failure still propagates and fails the run today; there is no
controllable, demoable retry story for it.

## Traces Stop At The Tick, Not The Event

A trace names which run and which tick, and — when it's interesting — whether that tick's snapshot
save retried. It does not name which specific event was rejected or hit `unknown_entity` inside a
tick. [0019](decisions/0019-traces-are-tick-and-rpc-grained-not-per-event.md) is the reasoning:
per-event spans at real event volumes would be noise, and getting finer than tick-level would need a
`context.Context` threaded into `Sim.Step`/`fold`, a signature change across the batch runner and
every test that calls `Step()` — not done reflexively for a demo feature.

## Metrics Can't Tell You Which Run

Every Prometheus metric is global or labeled by a bounded, closed set (event outcome, gRPC method) —
never by `run_id`, deliberately
([0016](decisions/0016-prometheus-metrics-carry-no-run-id-label.md)). "Is the daemon under load
right now" is a `/metrics` question; "which run is slow" is a `GET /v1/runs/{id}/telemetry` question
— the two surfaces answer different questions on purpose, and neither substitutes for the other.

## Setup Has Only Been Verified On A Machine That Already Had The Toolchain

`docs/demo.md`'s setup path has been run from a fresh clone, but not a fresh *machine* — Docker
image store, Go module cache, npm cache, and Docker/Node/`task` itself were all already installed.
A truly bare OS install hasn't been tested.

## Next Experiments

- Teach `task load` (or a sibling tool) to drive the projection stream, so catch-up cost gets the
  same real-measurement treatment chain-digest cost just did.
- A repeatable recovery-*time* scenario, not just a repeatable recovery scenario — time from kill to
  resumed-and-caught-up, across the crash matrix and `fail_snapshot_every` alike.
- Sample the chain digest if a real (not load-test) scenario ever needs organism counts and event
  rates simultaneously past 0008's measured threshold — the fix was scoped there, just not needed
  yet.
- Per-event trace detail, if tick-level ever turns out not to be enough for a real debugging session
  rather than a demo.
