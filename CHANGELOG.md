# Changelog

All notable changes to this project are documented here.

The project has not reached its first versioned release. Until then, work is grouped under `Unreleased`.

## [Unreleased]

### Added

- Project pack imported from the ideas cradle: roadmap, architecture, contracts, event model, local development, and feedback plan.
- `internal/engine`: live run lifecycle — start, pause, resume, finish, control revisions applied on tick boundaries, and projection fan-out to subscribers. Its method shapes match the service definitions in `docs/contracts.md`, so the M1 gRPC service becomes an adapter rather than a second implementation.
- `internal/sim`: the per-tick simulation core, shared by the batch runner and the live engine so replay and live play cannot drift apart.
- `signalgarden -live`: paces a run on a clock, streams a frame per tick, and accepts typed control changes. The terminal stand-in for the M1 control surface.
- `proto/signal/garden/v1/garden.proto`: the versioned contract, with the seven methods and the exact REST routes from `docs/contracts.md`. Generated Go is committed under `internal/gen/`; `make proto` regenerates it from plugins pinned in `go.mod`.
- `internal/service`: the gRPC adapter over the run engine. It holds no state and maps engine sentinel errors to status codes, so clients branch on codes rather than message text.
- `cmd/signalgardend`: serves gRPC on `:9090` and the generated REST gateway on `:8080`, with `/healthz`, `/readyz`, gRPC health, and reflection. The gateway dials the gRPC listener rather than calling in process, so the hop the architecture describes is a real one.

- `github.com/DamoDCoder/event-spine v0.2.0` as a dependency, ahead of the M2 work that uses it. Its surface is v0 and expected to move, so it is pinned rather than tracked.
- Decision records 0004 through 0008: the Event Spine log replaces Kafka as the M2 backbone, one log per run owned by that run's goroutine, the daemon refuses to start on corrupt recovery, run logs are never compacted, and determinism is asserted on a chain rather than a terminal garden hash.

### Changed

- `run.Execute` now drives `internal/sim` instead of owning the tick loop. Behavior and determinism are unchanged; the M0 test suite passes untouched.
- M2 is no longer a Kafka milestone. The event transport becomes an in-process append-only log, which turns the durability properties the milestone exists to prove into unit tests against a crashable filesystem. `docs/architecture.md`, `docs/events.md`, `docs/roadmap.md`, `docs/local-development.md`, and `docs/feedback.md` were rewritten to match; Kafka moves to Later Extensions.

- M0 no longer requires protobuf or gRPC. A single process has no service boundary to separate, so M0 defines its seams as Go interfaces and M1 introduces the wire contracts when a second process makes them load-bearing.
- M0 projection is a CLI rather than a React screen, which keeps the milestone free of the Node toolchain.

## Commit Guidance

Use a short imperative subject with a small scope:

```text
<type>(<scope>): <imperative summary>
```

Useful types are `docs`, `feat`, `test`, `perf`, `refactor`, `chore`, and `fix`. Keep each commit focused on one coherent batch. Add a body when the decision or tradeoff would be difficult to infer from the subject.
