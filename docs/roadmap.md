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

**Deliberately deferred:** protobuf, gRPC, the REST gateway, Kafka, Docker, and the document store. M0 has one process and no service boundary, so a wire contract has nothing to separate yet. Service boundaries and their protobuf definitions arrive at M1, where a second process makes them load-bearing. The Go interfaces written here are the seams those contracts will follow.

## M1: Local Vertical Slice

**Goal:** connect one user action to a live garden projection.

**Build:** Go services, gRPC internal calls, generated REST gateway for public control/query endpoints, WebSocket projection stream, React control surface, and Docker Compose.

**Feedback demo:** start a run, adjust four controls, observe the garden, event rate, processing latency, and connection status.

**Exit criteria:**

- `docker compose up` starts the local stack.
- A run can start, update controls, pause, and finish.
- Browser tests cover the primary journey.
- Health and readiness checks are available.

## M2: Event Backbone And Replay

**Goal:** introduce Kafka and durable run history without changing the user-facing contract.

**Build:** raw and processed topics, consumer group, document-store snapshots, event log metadata, replay command, idempotent processing, and reconnect catch-up.

**Feedback demo:** stop the consumer, create lag, restart it, and replay the run to the same final state.

**Exit criteria:**

- Duplicate delivery does not corrupt the projection.
- A disconnected client receives a snapshot and missed updates.
- Replay determinism is tested from a fixture event log.
- Topic keys, retention, and partition assumptions are documented.

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
- Shared replay links and comparative run analysis.