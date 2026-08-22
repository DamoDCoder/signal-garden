# Signal Garden Roadmap

Each milestone must end with something runnable, measurable, or reviewable. The roadmap favors early feedback over infrastructure completeness.

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

**Exit criteria:**

- Throughput, p50/p95/p99 latency, lag, retries, drops, and WebSocket freshness are visible.
- At least one bottleneck is measured and improved or explicitly documented.
- Recovery time and failure behavior have repeatable scenarios.
- A local load test runs without cloud services.

## M4: Showcase Release

**Goal:** make the project understandable and enjoyable in five minutes.

**Build:** polished garden interactions, deterministic demo seed, architecture diagrams, setup guide, test guide, demo script, and short performance report.

**Feedback demo:** a fresh checkout reaches a live run using documented commands.

**Exit criteria:**

- New setup works from a clean local environment.
- Unit, integration, and browser tests are documented and passing.
- The demo explains both the game loop and the event-processing system.
- Known limits and next experiments are recorded.

## Later Extensions

- Read-only iOS companion using the versioned API and WebSocket contracts.
- Multi-garden or spectator mode.
- Adaptive worker policies and partitioning experiments.
- Kafka, if the event path ever needs to cross a process boundary. The Event Spine log is one machine, one copy, one writer, and that is the case for reaching for a broker.
- Shared replay links and comparative run analysis.