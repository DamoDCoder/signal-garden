# Changelog

All notable changes to this project are documented here.

Versions track the roadmap in [docs/roadmap.md](docs/roadmap.md): a minor version is a milestone's worth of work, and the project stays on `0.x` until M4 makes a first release meaningful.

## [Unreleased]

### Added

- `internal/event/codec.go`: `Event.ToCore` and `FromCore`, mapping the envelope onto the durable log record. PartitionKey, OccurredAt, and SchemaVersion become the record header; the rest becomes a JSON payload. `recorded_at` is not written at all, so two runs of the same seed produce byte-identical records — asserted directly rather than assumed.
- `internal/eventlog`: one append-only log per run, with the `projections` consumer group and `Sync` durability. Appending, reading, replaying, and committing are separate operations, and committing is deliberately not one of them yet — the garden is in memory until snapshots land, so a commit would let a restart resume past records it could no longer replay.

### Changed

- `internal/sim` appends a whole tick in one call and folds what the log hands back, instead of publishing to an in-memory queue and draining it. A tick costs one fsync regardless of how many events it produced. The seed-42 scorecard is byte-identical to the previous implementation's.
- `Sim` owns a log and must be `Close`d. `run.Execute` closes it on return; a live run closes it when its goroutine exits, which is after finishing rather than at it, because a finished run still answers telemetry that reads the log's offsets.

### Removed

- `internal/bus`. The in-memory queue was the M0 seam and the log now occupies it. Tests reach a real log through the spine's in-memory filesystem, so there is one transport and the path under test is the path that ships.

## [0.3.0] — 2026-08-17

The M2 backbone decided, before any of it is built.

### Added

- `github.com/DamoDCoder/event-spine v0.2.0` as a dependency, ahead of the M2 work that uses it. Its surface is v0 and expected to move, so it is pinned rather than tracked. Nothing imports it yet.
- Decision records 0004 through 0008: the Event Spine log replaces Kafka as the M2 backbone, one log per run owned by that run's goroutine, the daemon refuses to start on corrupt recovery, run logs are never compacted, and determinism is asserted on a chain rather than a terminal garden hash.

### Changed

- M2 is no longer a Kafka milestone. The event transport becomes an in-process append-only log, which turns the durability properties the milestone exists to prove into unit tests against a crashable filesystem. `docs/architecture.md`, `docs/events.md`, `docs/roadmap.md`, `docs/local-development.md`, and `docs/feedback.md` were rewritten to match; Kafka moves to Later Extensions.
- M2 gains an exit criterion it could not have had under a broker: a run survives a simulated power cut at every tick boundary, in all three crash shapes, losing nothing the log acknowledged as durable.

## [0.2.0] — 2026-08-05

M1's server half: a live run, and a contract for talking to it.

### Added

- `internal/engine`: live run lifecycle — start, pause, resume, finish, control revisions applied on tick boundaries, and projection fan-out to subscribers. Its method shapes match the service definitions in `docs/contracts.md`, so the M1 gRPC service becomes an adapter rather than a second implementation.
- `internal/sim`: the per-tick simulation core, shared by the batch runner and the live engine so replay and live play cannot drift apart.
- `signalgarden -live`: paces a run on a clock, streams a frame per tick, and accepts typed control changes. The terminal stand-in for the M1 control surface.
- `proto/signal/garden/v1/garden.proto`: the versioned contract, with the seven methods and the exact REST routes from `docs/contracts.md`. Generated Go is committed under `internal/gen/`; `make proto` regenerates it from plugins pinned in `go.mod`.
- `internal/service`: the gRPC adapter over the run engine. It holds no state and maps engine sentinel errors to status codes, so clients branch on codes rather than message text.
- `cmd/signalgardend`: serves gRPC on `:9090` and the generated REST gateway on `:8080`, with `/healthz`, `/readyz`, gRPC health, and reflection. The gateway dials the gRPC listener rather than calling in process, so the hop the architecture describes is a real one.

### Changed

- `run.Execute` now drives `internal/sim` instead of owning the tick loop. Behavior and determinism are unchanged; the M0 test suite passes untouched.

### Not yet

- WebSocket projection stream, React control surface, and Docker Compose. M1 is not complete.

## [0.1.0] — 2026-08-04

M0: the event path and the simulation rules, with no external dependencies.

### Added

- Project pack imported from the ideas cradle: roadmap, architecture, contracts, event model, local development, and feedback plan.
- `internal/domain`: the garden, its organisms, the rain, growth, and pest rules, and control validation.
- `internal/event`: the transport-neutral envelope, with simulation time separated from wall-clock time and a per-type idempotency key.
- `internal/bus`, `internal/producer`, `internal/processor`: an ordered in-memory queue, a seeded deterministic producer, and an idempotent processor that owns the garden projection.
- `internal/run` and `signalgarden`: a batch orchestrator that runs a garden to completion from a seed, and a CLI that prints its scorecard with event counters.

### Changed

- M0 no longer requires protobuf or gRPC. A single process has no service boundary to separate, so M0 defines its seams as Go interfaces and M1 introduces the wire contracts when a second process makes them load-bearing.
- M0 projection is a CLI rather than a React screen, which keeps the milestone free of the Node toolchain.

## Commit Guidance

Use a short imperative subject with a small scope:

```text
<type>(<scope>): <imperative summary>
```

Useful types are `docs`, `feat`, `test`, `perf`, `refactor`, `chore`, and `fix`. Keep each commit focused on one coherent batch. Add a body when the decision or tradeoff would be difficult to infer from the subject.
