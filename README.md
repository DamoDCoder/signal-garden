# Signal Garden

Tune a living digital garden by controlling event producers and processors, then watch it flourish or collapse as throughput, lag, retries, and failures shape the simulation in real time.

Signal Garden is a local-first real-time event-processing laboratory disguised as a strategy game. Garden health is a direct consequence of system behavior: a steady event stream makes organisms grow, while overload, duplication, and failure produce visible decay.

## Status

**v0.10.0 — M0, M1, and M2 complete; M3 in progress.** One Go process, an append-only event log, deterministic simulation, a live run engine behind a CLI projection, a gRPC service with a generated REST gateway, and a WebSocket projection stream a client can resume from a log offset. Run history is durable and replayable, and a run outlives the process that was serving it.

This repository owns the contract and serves it. The React client and the Compose stack are in [app.signal-garden](https://github.com/DamoDCoder/app.signal-garden), which pins a tag here rather than tracking `main` — see [0011](docs/decisions/0011-the-ui-is-a-separate-repository.md) and [docs/roadmap.md](docs/roadmap.md).

Durability arrives at M2 as the [Event Spine](https://github.com/DamoDCoder/event-spine) log — an append-only log as an in-process library — rather than Kafka. [Decision 0004](docs/decisions/0004-event-spine-replaces-kafka-as-the-event-backbone.md) says why, and what it costs.

## Quick Start

```sh
task test    # domain, replay determinism, and idempotency tests
task run     # run a garden to completion and print the scorecard
task live    # run on a clock, a frame per tick, steered while it runs
```

Tasks are [go-task](https://taskfile.dev); `task --list` is the index. Flags after `--` reach the binary:

```sh
task run -- -seed 42 -ticks 40 -rate 6 -rain 3 -growth 2 -pest 1
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
task serve   # gRPC on :9090, generated REST gateway on :8080
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

`GET /healthz` and `GET /readyz` are the liveness and readiness checks. Regenerate the contract with `task proto`, which needs protoc; building and testing do not.

### Projection Stream

```text
ws://localhost:8080/v1/runs/demo/stream          start at the garden as it is now
ws://localhost:8080/v1/runs/demo/stream?from=246 come back at log offset 246
```

A frame per tick. A client that passed `from` gets one catch-up frame first, carrying the records it missed; the snapshot right behind it stands exactly where those records end, so the handover has neither a gap nor a repeat. Folding the catch-up records into an empty garden reaches the hash of that snapshot, which is what the tests assert rather than trusting a count.

Frames are `ProjectionFrame` messages from the same contract the REST routes use, marshalled the same way, so a `GardenSnapshot` parses identically whichever transport delivered it — [0010](docs/decisions/0010-one-contract-for-both-transports.md) says why that took a decision. The stream is read-only and served from the daemon rather than the gateway, because it is not a gRPC method. See [docs/contracts.md](docs/contracts.md) for frame shapes, JSON conventions, and rejection codes, and [0009](docs/decisions/0009-catch-up-is-a-command-to-the-run-not-a-second-reader.md) for why catch-up runs on the run's own goroutine.

### As A Container

```sh
task docker:build          # cross-compile for linux, then build the image
task docker:run            # serve from the image on :8080 and :9090
```

The image is built locally and never pushed. `docker:build` tags it twice — `signal-garden/signalgardend:v0.10.0` and `:local` — and stamps the version into an image label, so a stack running `:local` can still be asked which contract it was built from. A cross-architecture build gets the version tag only: `:local` moves, and pointing it at an image this machine cannot run is a confusing way to fail. The compose file that runs it alongside the client lives in [app.signal-garden](https://github.com/DamoDCoder/app.signal-garden) and expects to find it in the local image store; this repository ships the image, not the stack. See [0015](docs/decisions/0015-ship-an-image-but-not-a-stack.md).

The image copies a binary rather than compiling one, so `task build:docker` runs first — `docker:build` does that for you. That build is a *different platform* from `task build`: `darwin/arm64` for this machine, `linux/arm64` for the container, out of separate directories because a darwin binary in a Linux container fails with `exec format error` and does not say why. `ARCH=amd64` builds for an x86 machine instead.

### Run History

The daemon keeps each run's events in an append-only log under `data/runs/<run_id>/`, one directory per run:

| Variable | Default | Meaning |
| --- | --- | --- |
| `SIGNAL_GARDEN_DATA_DIR` | `data` | Where run histories live |
| `SIGNAL_GARDEN_ON_CORRUPT` | `refuse` | `refuse` or `continue` when a log opens with bytes the disk got wrong |
| `SIGNAL_GARDEN_CORS_ORIGIN` | `*` | Origin allowed to call the API from a browser; empty disables CORS |

A run ID is taken by history as well as by a live run, so restarting the daemon and starting a run picks the next free ID rather than appending a second run's events into the first one's log. Asking for a used ID explicitly returns `409`.

On startup the daemon resumes every run in that directory that had not finished, and says what it found:

```text
run history under data, on corrupt: refuse
resumed survivor at tick 27, running
```

A resumed run reports `"resumed": true`. Its garden and tick counter carry on; its determinism chain starts fresh, because it did not fold the records below the snapshot it restored from.

The batch and live CLI keep history in memory unless told otherwise, so a demo leaves nothing behind:

```sh
go run ./cmd/signalgarden -live -run demo -data ./data   # same code path, on disk
go run ./cmd/signalgarden -replay -run demo -data ./data # rebuild that garden from its log
```

Replay reaches the same snapshot hash the live run ended on, in a different process, from the records alone. A snapshot is written every 50 ticks and once more at the end, purely so a restart folds fewer records — delete every `snapshot-*.state` in the run's directory and replay prints the same garden, more slowly.

## Picking Up From Here

`main` at `v0.10.0`, pushed to [DamoDCoder/signal-garden](https://github.com/DamoDCoder/signal-garden). Everything passes under `task check`.

Nothing here blocks the client. M1 and M2's exit criteria are both met, and the contract is generated, versioned, and checked against the generator that consumes it. M3 is in progress in *this* repository.

**M3's metrics foundation and capacity controls are done.** `GET /metrics` is a Prometheus scrape target: tick duration, gRPC/REST call duration, events processed, snapshots dropped, WebSocket freshness, and now `pending` (consumer lag), summed across every run rather than last-writer-wins. Deliberately no `run_id` label anywhere, to keep cardinality bounded — see [0016](docs/decisions/0016-prometheus-metrics-carry-no-run-id-label.md). `worker_count` and `batch_size` are real `Controls` fields: together they cap how many records one tick folds, as a capacity model rather than literal goroutines — see [0017](docs/decisions/0017-worker-count-and-batch-size-are-a-capacity-model-not-goroutines.md). Daemon and contract only so far; no client sliders yet. Still open: `retries`, OpenTelemetry traces, the in-app performance view, a load generator, and failure injection — see [docs/roadmap.md](docs/roadmap.md).

**M2's exit criteria are met.** Duplicate delivery ✓, reconnect catch-up ✓, replay determinism on a chain ✓, crash survival ✓, keys and retention documented ✓.

**Runs survive a restart.** Kill the daemon mid-run and start it again: it finds the runs it was serving, rebuilds each one from its log, and carries on producing where it stopped. A run interrupted at tick 26 comes back at tick 26 and reaches the garden a run that never stopped would have reached. Finished runs stay finished; paused runs come back paused. See [0013](docs/decisions/0013-derive-each-tick-s-randomness.md) for the producer half and [0014](docs/decisions/0014-a-restarted-daemon-resumes-its-runs.md) for the rest.

**M1 is done.** The React control surface and Docker Compose, both in [app.signal-garden](https://github.com/DamoDCoder/app.signal-garden), start a run, take control changes, pause, and finish it from the browser, and `docker compose up` starts the local stack clean. This repository owns the contract and stays runnable with `task serve` alone; the compose file describing both halves lives with the client, because the dependency runs that way round. See [0011](docs/decisions/0011-the-ui-is-a-separate-repository.md).

That client generates TypeScript with `protoc-gen-es`, straight from the `.proto` — it is the only option that covers both transports, since an OpenAPI spec describes the REST routes and cannot see `ProjectionFrame`. [docs/contracts.md](docs/contracts.md) has the invocation, and [0012](docs/decisions/0012-declare-the-js-type-of-every-64-bit-field.md) records what it does with `jstype`: `run.seed` is a `string`, and every 64-bit quantity is a `bigint`.

Pin a tag rather than tracking `main`. `v0.7.0` is the first with `Run.resumed` on it.

### 1. The rest of M3, the failure and performance lab

Metrics and worker-count/batch-size are done — see above. Left: load generator, failure injection, OpenTelemetry traces, and client sliders for the two new controls. It is also where two deferrals finally get numbers instead of guesses:

- **Catch-up cost.** Resuming from a deep offset reads the whole gap on the run's own goroutine — a slice copy at M2's volumes, a measurable pause at M3's. [0009](docs/decisions/0009-catch-up-is-a-command-to-the-run-not-a-second-reader.md) records the fix and why it is deliberately unwritten: it wants a measurement, not a guess.
- **Chain digest cost.** [0008](docs/decisions/0008-assert-determinism-on-a-chain-not-a-terminal-hash.md) allows for sampling if folding a digest per record becomes expensive at M3's event rates.

Worker count and batch size are real controls now — see above and [0017](docs/decisions/0017-worker-count-and-batch-size-are-a-capacity-model-not-goroutines.md).

## The Contract

`proto/signal/garden/v1/garden.proto` is the single definition of a run, a garden, an event, and a projection frame. Both transports serve it: the REST routes are generated from it, and the WebSocket stream marshals the same messages the same way. A consumer generates from this file and pins a tag rather than tracking `main`.

Generated Go is committed, so a clean checkout builds without protoc. Only regeneration needs the toolchain — `task proto`.

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
