# 0004: Event Spine replaces Kafka as the durable event backbone

- **Date:** 2026-08-17
- **Status:** Accepted

## Context

M2 was specified as "introduce Kafka and durable run history without changing the user-facing contract": raw and processed topics, a consumer group, replay, and reconnect catch-up. [architecture.md](../architecture.md) made that a boundary rule — Kafka is the durable event transport at M2, and the in-memory bus is permitted only for M0.

Since M0 was written, [event-spine](https://github.com/DamoDCoder/event-spine) has become available: an append-only log as an in-process Go library, with consumer groups, snapshots, recovery, and a deterministic simulation harness. Its own design notes were drawn partly from this project — the absorbing-state finding in its `docs/concepts.md` was measured on Signal Garden's garden projection, and `core.Event` has no wall-clock field because M0's envelope had one that was never populated.

M2's exit criteria are about *properties*, not about Kafka: duplicate delivery must not corrupt the projection, a disconnected client must receive a snapshot and missed updates, replay determinism must be tested from a fixture, and topic keys and retention must be documented.

## Options Considered

1. **Kafka as planned.** Matches the original roadmap and teaches a system the industry actually runs. Costs a broker in Compose, a client library, and partition/consumer-group configuration before any durability property is testable — and crash behaviour stays untestable locally, because there is no way to cut power to a container mid-fsync and assert what survived.
2. **Event Spine as the backbone, Kafka dropped.** Every M2 exit criterion is reachable, and crash behaviour becomes a unit test. Costs the multi-process event path: the log is in-process, single-writer, with no wire protocol.
3. **Both — Spine for local durability, Kafka for cross-process transport.** Keeps the planned topology. Means two durability stories, two orderings, and two places a duplicate can come from.
4. **Spine for tests only.** Adopt `sim.FS`, `sim.Clock`, and `core.Chain` for determinism and crash testing; leave the in-memory bus as the runtime transport. Smallest change, and gains no durability at all.

## Decision

Option 2. `internal/bus` is deleted and the spine's `log` becomes the event transport at M2, opened per run. Kafka moves to Later Extensions, where it belongs if this project ever needs event fan-out across processes.

Option 3 was rejected because two transports means the durability claim has to be made twice and can disagree. Option 4 was rejected because it keeps the property the milestone exists to prove — durable run history — out of the code.

## Evidence

The spine's filesystem is injected, so the batch runner and the unit tests get a real log backed by `sim.NewFS()` — an in-memory, crashable filesystem — while the daemon gets `runtime.NewFS(dir)`. There is one code path, and the path under test is the path that ships. Kafka has no equivalent: a fake in tests and a broker in Compose are two implementations of the same contract, and only one of them is exercised by `go test`.

That injected filesystem is also what makes the crash matrix possible. `fs.Crash()`, `fs.CrashExtend()`, and `fs.CrashTorn(percent)` are three distinct power-cut shapes, and the spine records that the middle one — the file keeps its length with zeros in the gap, which is what ext4 actually does — hid a bug for four milestones because the simulator could not produce it. Signal Garden inherits that test surface for free.

The cost is real and worth stating plainly. The spine is one machine, one copy: no replication, no wire protocol, no partitions, no cross-process consumer group rebalancing. The topology in [architecture.md](../architecture.md) drew producer, processor, and projection gateway as candidate process boundaries for the event path, and that stops being true. They remain packages with the same responsibilities; they stop being separable by a topic.

The user-facing contract is unaffected, which is what M2 required. gRPC and the REST gateway still separate the client from the daemon, so the wire contract landed at M1 keeps doing the job it was introduced for.

## What Would Revisit This

- The project needs more than one writer process against the same event history, which is the one thing the spine states it does not do.
- Event volume outgrows a single machine's disk, or run history needs to survive the loss of that machine.
- The spine's v0 surface breaks in a way that costs more to track than a broker costs to run. It is pinned at v0.2.0 and its own documentation says to expect the surface to move.
