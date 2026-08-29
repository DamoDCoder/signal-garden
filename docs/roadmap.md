# Signal Garden Roadmap

Each milestone must end with something runnable, measurable, or reviewable. The roadmap favors early feedback over infrastructure completeness.

## Status

`main` at `v0.13.0`, tagged — every M3 *build* item has shipped across both repos (client at
`v0.4.0`). One M3 exit criterion remains open (recovery time specifically) — see below — so the
milestone itself isn't tagged done yet; `v0.13.0`/`v0.4.0` mark "all the M3 slices landed," not "M3
is closed." M4 is now underway too, in parallel: its demo script and performance report are done.
The next tag is `v0.14.0`+ once M3's last criterion is addressed, or `v1.0.0`+ if the rest of M4
lands first and folds it in.

| Milestone | State |
| --- | --- |
| M0: Contract Spike | ✅ Done |
| M1: Local Vertical Slice | ✅ Done (client repo carries the browser half) |
| M2: Event Backbone And Replay | ✅ Done |
| M3: Failure And Performance Lab | 🚧 Build done, 1 exit criterion open |
| M4: Showcase Release | 🚧 In progress |

### M3 deliverables

- [x] **Prometheus metrics** — `GET /metrics`: throughput, p50/p95/p99 latency, drops, WebSocket
      freshness, lag. Deliberately no `run_id` label. [0016](decisions/0016-prometheus-metrics-carry-no-run-id-label.md)
- [x] **Worker count / batch size** — real `Controls` fields; a capacity model
      (`worker_count * batch_size`) rather than literal goroutines, since nothing in the event path
      is CPU-bound enough to parallelize. [0017](decisions/0017-worker-count-and-batch-size-are-a-capacity-model-not-goroutines.md)
- [x] **Load generator** — `task load` (`cmd/signalgarden -load`) drives a running daemon over its
      real gRPC API with a controlled burst and reports what it observed. Daemon-repo only.
- [x] **Failure injection** — `Controls.fail_snapshot_every`: every Nth periodic on-disk snapshot
      save fails its first attempt and retries. Targets the snapshot save deliberately, not event
      processing — every event outcome (rejected, unknown_entity, no_effect) is permanent given the
      garden's state, so there's nothing transient there to retry; disk I/O is the only real,
      transient, failable operation in the live path, and the snapshot is the lower-stakes of the
      two writes (the other being the durability-critical log append M2 depends on).
      [0018](decisions/0018-failure-injection-targets-the-snapshot-save-not-event-processing.md)
- [x] **OpenTelemetry traces** — run/event correlation, deferred out of the metrics slice (0016).
      Two granularities: every gRPC call is a span (`otelgrpc`, covering REST too), and every tick
      is a span (`run.id`/`tick`), with an event when that tick's snapshot save retried or failed —
      deliberately not per-event, which would need a `context.Context` threaded into `Sim.Step`.
      Exported OTLP/gRPC to an optional local endpoint (`SIGNAL_GARDEN_OTEL_ENDPOINT`); unset by
      default, tracing costs nothing. [0019](decisions/0019-traces-are-tick-and-rpc-grained-not-per-event.md)
- [x] **Compact in-app performance view** — client-repo. The Pressure panel (built as part of M1)
      now also shows `snapshot_save_retries`/`snapshot_save_failures`. Client `v0.4.0`.
- [x] **Client sliders for `worker_count`/`batch_size`/`fail_snapshot_every`** — the client's
      `CONTRACT` bumped to this repo's `v0.13.0`; all three are live-tunable sliders in the Controls
      panel. Client `v0.4.0`.
- [x] **At least one bottleneck measured and improved or explicitly documented** (exit criterion) —
      chain-digest cost (`Garden.Digest()`, once per event in `fold()`), measured with `task load`
      across a 7-scenario matrix up to 2000 organisms × 200 events/tick. Not a bottleneck at demo
      scale (single-digit ms, under 4% of tick budget); real but fixed-overhead-masked below a few
      hundred organisms; only the most extreme scenario spends a majority of its tick budget on it.
      Explicitly documented rather than fixed — sampling stays unwritten. See
      [0008](decisions/0008-assert-determinism-on-a-chain-not-a-terminal-hash.md#measured-at-m3) and
      [performance-report.md](performance-report.md).
- [ ] **Recovery time and failure behavior have repeatable scenarios** (exit criterion) — partially
      addressed: M2's crash matrix (`internal/sim/crash_test.go`) already gives repeatable
      unrecoverable-failure scenarios, and `fail_snapshot_every` now gives a repeatable recoverable
      one. Recovery *time* specifically is not measured anywhere yet.

## M0: Contract Spike

**Goal:** prove the core event path and simulation rules with no external dependencies.

**Build:** Go domain rules, an event envelope, an in-memory event bus, a deterministic seeded producer, an idempotent processor, and a CLI projection.

**Feedback demo:** change producer rate and event mix, then see garden state plus event counters change within one local process.

**Exit criteria:**

- Same seed and command sequence produces the same garden state.
- Invalid controls are rejected consistently.
- Domain tests cover rain, growth, pest, and duplicate event behavior.
- The CLI can start a run, apply control changes, and print garden state with event counters.

**Deliberately deferred:** protobuf, gRPC, the REST gateway, the durable event log, Docker, and snapshot storage. M0 has one process and no service boundary, so a wire contract has nothing to separate yet. Service boundaries and their protobuf definitions arrive at M1, where a second process makes them load-bearing. The Go interfaces written here are the seams those contracts will follow.

## M1: Local Vertical Slice

**Goal:** connect one user action to a live garden projection.

**Build:** a live run engine, Go services, gRPC internal calls, generated REST gateway for public control/query endpoints, WebSocket projection stream, React control surface, and Docker Compose.

The WebSocket stream landed with M2's reconnect catch-up rather than here, because a stream that cannot resume is not the thing the exit criteria are about. The React surface and Compose are what remain, and both now live in [app.signal-garden](https://github.com/DamoDCoder/app.signal-garden) — so M1's remaining exit criteria are met over there, against the daemon this repository serves. See [0011](decisions/0011-the-ui-is-a-separate-repository.md).

The run engine lands first and carries no transport: run lifecycle, control revisions, and projection fan-out are testable against a manual clock before any codegen exists, and the gRPC service and WebSocket gateway both become adapters over it. See [0002](decisions/0002-run-lifecycle-lives-in-an-engine-package.md).

**Feedback demo:** start a run, adjust four controls, observe the garden, event rate, processing latency, and connection status.

**Exit criteria:**

- `docker compose up` starts the local stack.
- A run can start, update controls, pause, and finish.
- Browser tests cover the primary journey.
- Health and readiness checks are available.

## M2: Event Backbone And Replay

**Goal:** introduce durable run history without changing the user-facing contract.

**Build:** the [Event Spine](https://github.com/DamoDCoder/event-spine) log as the event transport, one log per run, a `projections` consumer group, snapshots, a replay command, idempotent processing, and reconnect catch-up.

Reconnect catch-up is where M2 and M1 meet: the durable half is a read from a log offset, and the visible half is the WebSocket projection stream M1 left outstanding. Both land together, because neither is demonstrable alone.

Kafka was the original plan and is not the plan now. The log is an in-process library, so the durability properties this milestone exists to prove become unit tests against a crashable filesystem rather than assertions about a broker in Compose. See [0004](decisions/0004-event-spine-replaces-kafka-as-the-event-backbone.md); ownership, corrupt-recovery policy, and compaction are [0005](decisions/0005-one-log-per-run-owned-by-the-run-goroutine.md), [0006](decisions/0006-refuse-to-start-on-corrupt-recovery.md), and [0007](decisions/0007-never-compact-a-run-log.md).

**Feedback demo:** stop the consumer, create lag, restart it, and replay the run to the same final state.

**Exit criteria:**

- Duplicate delivery does not corrupt the projection.
- A disconnected client receives a snapshot and missed updates. The catch-up records must end exactly where the snapshot after them begins, so the handover has neither a gap nor a repeat.
- Replay determinism is tested from a fixture event log, and asserted on a `core.Chain` digest rather than a terminal garden hash.
- A run survives a simulated power cut at every tick boundary, in all three crash shapes, losing nothing the log acknowledged as durable.
- Partition keys, snapshot cadence, and run retention are documented.

## M3: Failure And Performance Lab

**Goal:** make system behavior measurable and tunable.

**Build:** load generator, failure injection, OpenTelemetry traces, Prometheus metrics, latency histograms, and a compact in-app performance view.

**Feedback demo:** compare worker count and batch size under a controlled event burst.

**Done:**

- Throughput, p50/p95/p99 latency, and drops are visible, via Prometheus at `GET /metrics`:
  `signal_garden_tick_duration_seconds`, `signal_garden_rpc_duration_seconds`,
  `signal_garden_events_processed_total`, `signal_garden_snapshots_dropped_total`. WebSocket
  freshness is visible too, via `signal_garden_last_publish_timestamp_seconds`. Deliberately global,
  not per-run — see [0016](decisions/0016-prometheus-metrics-carry-no-run-id-label.md).
- Worker count and batch size are real `Controls` fields. Together they cap how many records one
  tick folds — `worker_count * batch_size` — as a capacity model rather than literal goroutines
  (nothing in the event-application path is CPU-bound enough to benefit from real parallelism); see
  [0017](decisions/0017-worker-count-and-batch-size-are-a-capacity-model-not-goroutines.md). A
  capacity below the production rate builds a genuine backlog and drains once it's raised again —
  the feedback demo works end to end via `curl` or `task load`, and now via sliders in the client's
  Controls panel too (client `v0.4.0`).
- `lag` is visible: `TelemetrySnapshot.pending` is real now that capacity can fall below production,
  and `signal_garden_pending_events` sums it across every run this process is serving (not
  last-writer-wins across concurrent runs — see 0016's amended revisit note).
- **A local load test runs without cloud services.** `task load` (`cmd/signalgarden -load`) drives a
  running daemon over its real gRPC API with a controlled event burst — the same feedback demo that
  worked by hand via `curl`, now one command. It dials the generated `GardenServiceClient` directly
  rather than hand-rolling REST/JSON, so it exercises the same instrumented `grpcServer` the REST
  gateway calls internally.
- `retries` are visible: `Controls.fail_snapshot_every` makes the periodic on-disk snapshot save
  fail its first attempt and retry, deterministically. `snapshot_save_retries`/
  `snapshot_save_failures` on `TelemetrySnapshot` and as global Prometheus counters. Targets the
  snapshot save, not event processing — every event outcome is permanent given the garden's state,
  so there's nothing transient to retry there. See
  [0018](decisions/0018-failure-injection-targets-the-snapshot-save-not-event-processing.md).
- OpenTelemetry traces: a span per gRPC call (`otelgrpc`, covering REST too) and a span per tick
  (`run.id`/`tick` attributes, with an event when that tick's snapshot save retried or failed) —
  deliberately not per-event. Exported OTLP/gRPC to an optional local endpoint
  (`SIGNAL_GARDEN_OTEL_ENDPOINT`), unset by default so tracing costs nothing. See
  [0019](decisions/0019-traces-are-tick-and-rpc-grained-not-per-event.md). The client's
  `compose.observability.yaml` (`task observability:up`) brings up Prometheus and Jaeger alongside
  the stack to see both live.
- The worker-count/batch-size/fail-snapshot-every sliders have client UI now — the Controls panel in
  app.signal-garden `v0.4.0`. A compact in-app performance view exists too: the client's Pressure
  panel already had a rolling pressure history from M1 and now also shows the two new snapshot-save
  counters.
- **At least one bottleneck is measured and explicitly documented.** Chain-digest cost
  (`Garden.Digest()`, once per event in `fold()`) — a 7-scenario `task load` matrix up to 2000
  organisms × 200 events/tick found it real, growing the way `organisms * events_per_tick` predicts,
  and not a bottleneck at demo scale: single-digit milliseconds, under 4% of the 200ms tick budget.
  A fixed per-tick floor (most likely `Log.Append`'s one fsync a tick) dominates below a few hundred
  organisms; only the most extreme scenario tested spent a majority of its tick budget here.
  Documented rather than fixed — sampling, 0008's own proposed fix, stays unwritten. See
  [0008](decisions/0008-assert-determinism-on-a-chain-not-a-terminal-hash.md#measured-at-m3) and
  [performance-report.md](performance-report.md).

**Exit criteria still open:**

- Recovery time and failure behavior have repeatable scenarios — partially: M2's crash matrix
  (`internal/sim/crash_test.go`) gives repeatable unrecoverable-failure scenarios, and
  `fail_snapshot_every` gives a repeatable recoverable one. Recovery *time* specifically is not
  measured anywhere yet.

## M4: Showcase Release

**Goal:** make the project understandable and enjoyable in five minutes.

**Build:** polished garden interactions, deterministic demo seed, architecture diagrams, setup guide, test guide, demo script, and short performance report.

**Feedback demo:** a fresh checkout reaches a live run using documented commands.

**Done:**

- **Demo script** — [docs/demo.md](demo.md): a five-minute, documented-commands-only walkthrough
  spanning both repos — clone, build, start a run, fall behind on purpose (worker/batch capacity),
  fail and recover on purpose (`fail_snapshot_every`), drop and reconnect, look under the hood
  (`task load`, `task observability:up`), finish and read the scorecard.
- **Short performance report** — [docs/performance-report.md](performance-report.md), the
  chain-digest-cost measurement that also closed an M3 exit criterion (see above).
- **`docs/demo.md`'s setup section runs clean.** Fresh clones of both repos into a scratch
  directory, `docs/demo.md`'s Setup section run verbatim: `task docker:build`, `nvm use`,
  `task setup`, `task up`. All four succeeded; the daemon and client both came up healthy and a run
  started over the REST API. `fetch-contract.sh`'s GitHub-fallback path got exercised for real too —
  the sibling checkout wasn't on the pinned tag (a fresh clone at a commit past `v0.13.0`, tag not
  re-cut), so it fell through to downloading `garden.proto` from GitHub, byte-identical to the local
  copy. Not a fully clean *machine*, though: same Docker image store, Go module cache, npm cache,
  and already-installed toolchain (Docker, Node, Go, `task`) as every other check in this session —
  a bare OS install isn't something this environment can test, and stays the real gap.

**Exit criteria still open:**

- Unit, integration, and browser tests are documented and passing.
- The demo explains both the game loop and the event-processing system.
- Known limits and next experiments are recorded.

## Later Extensions

- Read-only iOS companion using the versioned API and WebSocket contracts.
- Multi-garden or spectator mode.
- Adaptive worker policies and partitioning experiments.
- Kafka, if the event path ever needs to cross a process boundary. The Event Spine log is one machine, one copy, one writer, and that is the case for reaching for a broker.
- Shared replay links and comparative run analysis.