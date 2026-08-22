# Signal Garden

Tune a living digital garden by controlling event producers and processors, then watch it flourish or collapse as throughput, lag, retries, and failures shape the simulation in real time.

Signal Garden is a local-first real-time event-processing laboratory disguised as a strategy game. Garden health is a direct consequence of system behavior: a steady event stream makes organisms grow, while overload, duplication, and failure produce visible decay.

## Status

**v0.4.0 — M0 complete; M1 in progress; M2 event backbone landed.** One Go process, an append-only event log, deterministic simulation, a live run engine behind a CLI projection, and a gRPC service with a generated REST gateway. Run history is durable and replayable. No browser client and no containers yet — see [docs/roadmap.md](docs/roadmap.md) for what each milestone adds and why.

Durability arrives at M2 as the [Event Spine](https://github.com/DamoDCoder/event-spine) log — an append-only log as an in-process library — rather than Kafka. [Decision 0004](docs/decisions/0004-event-spine-replaces-kafka-as-the-event-backbone.md) says why, and what it costs.

## Quick Start

```sh
make test    # domain, replay determinism, and idempotency tests
make run     # run a garden to completion and print the scorecard
make live    # run on a clock, a frame per tick, steered while it runs
```

`make run` accepts the same flags as the binary:

```sh
go run ./cmd/signalgarden -seed 42 -ticks 40 -rate 6 -rain 3 -growth 2 -pest 1
```

Two runs with the same seed, tick count, and controls always produce the same final garden. That property is the point of M0, and it is what the replay tests assert.

## Live Mode

```sh
go run ./cmd/signalgarden -live -ticks 0 -interval 300ms
```

A frame prints per tick. Type `rate 20`, `rain 1`, `growth 4`, `pest 5`, `pause`, `resume`, or `finish` to steer the run while it is going; a control change takes effect at the next tick boundary and reports the tick it landed on. Ctrl-C finishes the run and prints its summary.

The same controls applied at the same ticks reach the same garden in either mode, which is what `TestEngineMatchesBatchRun` asserts. Live mode is the terminal stand-in for the M1 web control surface: both drive the same engine.

## Server

```sh
make serve   # gRPC on :9090, generated REST gateway on :8080
```

The REST routes are generated from [proto/signal/garden/v1/garden.proto](proto/signal/garden/v1/garden.proto); see [docs/contracts.md](docs/contracts.md) for the full surface and its error codes.

```sh
curl -X POST localhost:8080/v1/runs -H 'content-type: application/json' \
  -d '{"seed":42,"organisms":20,"controls":{"events_per_tick":6,"rain_weight":3,"growth_weight":2,"pest_weight":1}}'

curl -X PATCH localhost:8080/v1/runs/run-0001/controls -H 'content-type: application/json' \
  -d '{"events_per_tick":20,"rain_weight":1,"growth_weight":1,"pest_weight":5}'

curl localhost:8080/v1/runs/run-0001/snapshot
curl localhost:8080/v1/runs/run-0001/telemetry
```

`GET /healthz` and `GET /readyz` are the liveness and readiness checks. Regenerate the contract with `make proto`, which needs protoc; building and testing do not.

### Projection Stream

```text
ws://localhost:8080/v1/runs/demo/stream          start at the garden as it is now
ws://localhost:8080/v1/runs/demo/stream?from=246 come back at log offset 246
```

A frame per tick, as JSON. A client that passed `from` gets one `catchup` frame first, carrying the records it missed; the snapshot right behind it stands exactly where those records end, so the handover has neither a gap nor a repeat. Folding the catch-up records into an empty garden reaches the hash of that snapshot, which is what the tests assert rather than trusting a count.

The stream is read-only — nothing sent over it changes a run — and it is served from the daemon rather than the gateway, because it is not a gRPC method. See [docs/contracts.md](docs/contracts.md) for the frame shapes and rejection codes, and [0009](docs/decisions/0009-catch-up-is-a-command-to-the-run-not-a-second-reader.md) for why catch-up runs on the run's own goroutine.

### Run History

The daemon keeps each run's events in an append-only log under `data/runs/<run_id>/`, one directory per run:

| Variable | Default | Meaning |
| --- | --- | --- |
| `SIGNAL_GARDEN_DATA_DIR` | `data` | Where run histories live |
| `SIGNAL_GARDEN_ON_CORRUPT` | `refuse` | `refuse` or `continue` when a log opens with bytes the disk got wrong |

A run ID is taken by history as well as by a live run, so restarting the daemon and starting a run picks the next free ID rather than appending a second run's events into the first one's log. Asking for a used ID explicitly returns `409`.

The batch and live CLI keep history in memory unless told otherwise, so a demo leaves nothing behind:

```sh
go run ./cmd/signalgarden -live -run demo -data ./data   # same code path, on disk
go run ./cmd/signalgarden -replay -run demo -data ./data # rebuild that garden from its log
```

Replay reaches the same snapshot hash the live run ended on, in a different process, from the records alone. A snapshot is written every 50 ticks and once more at the end, purely so a restart folds fewer records — delete every `snapshot-*.state` in the run's directory and replay prints the same garden, more slowly.

## Picking Up From Here

Branch `feat/adopt-event-spine`, tags `v0.1.0` through `v0.4.0`, no remote configured yet. Everything passes under `make check`.

**M2's exit criteria are met.** Duplicate delivery ✓, reconnect catch-up ✓, replay determinism on a chain ✓, crash survival ✓, keys and retention documented ✓. The projection stream that catch-up needed was M1's last outstanding transport, so M1 is down to the React surface and Compose.

### 1. Make the producer resumable

The blocker for resuming a live run after a restart, and the reason `v0.4.0` restores projections but not runs.

A garden is the fold of a history, so it restores from one. A producer is a *position in a seeded stream* — `producer.Producer` holds a live `*rand.Rand` — and `math/rand` offers no way to write that position down. So `sim.Rebuild` can reconstruct what a run produced, and a restarted daemon still cannot carry on producing where it left off.

Two shapes worth weighing before writing anything:

- **Derive per tick.** Seed a fresh stream from `(seed, tick)` each tick, so position is a number rather than state. Resume becomes free. Changes every existing event stream, so the seed-42 fixtures and `732dc9ba` move.
- **Carry a seekable source.** Replace `math/rand` with a source whose state serialises into the snapshot. Keeps existing streams intact; adds a dependency or a hand-written PRNG, and the snapshot gains a field that must version alongside it.

The second preserves `SnapshotSchemaVersion` compatibility; the first is simpler forever after. Either way, `TestSnapshotCadenceDoesNotChangeTheRun` and the chain tests are the guard rails.

### 2. M1's unfinished half

React control surface and Docker Compose. The stream they would both use is now there, and `GET /v1/runs/{run_id}/stream` is the only thing a browser needs beyond the REST routes.

### 3. Catch-up cost under load

Resuming from a deep offset reads the whole gap on the run's own goroutine, which is a slice copy at M2's volumes and a measurable pause at M3's. [0009](docs/decisions/0009-catch-up-is-a-command-to-the-run-not-a-second-reader.md) records the fix and why it is deliberately not written yet: it wants a number from the load lab, not a guess.

## Documentation

- [Roadmap](docs/roadmap.md): milestones, exit criteria, and what is deliberately deferred.
- [Architecture](docs/architecture.md): service boundaries and the M1+ runtime topology.
- [Contracts](docs/contracts.md): protobuf, gRPC, and REST surface (lands at M1).
- [Events](docs/events.md): envelope, domain events, keys, ordering, and replay rules.
- [Local Development](docs/local-development.md): dependencies and commands by milestone.
- [Feedback](docs/feedback.md): checkpoints and the questions each milestone must answer.
- [Decisions](docs/decisions/): short decision records with the evidence behind them.

## Non-Goals

- No real stock prices, brokerage connections, payments, accounts, or financial recommendations.
- No multiplayer synchronization before single-client replay is reliable.
- No Kubernetes deployment before the local Compose workflow is boringly dependable.
- No mobile app before the API and WebSocket contracts are stable.
