# Signal Garden

Tune a living digital garden by controlling event producers and processors, then watch it flourish or collapse as throughput, lag, retries, and failures shape the simulation in real time.

Signal Garden is a local-first real-time event-processing laboratory disguised as a strategy game. Garden health is a direct consequence of system behavior: a steady event stream makes organisms grow, while overload, duplication, and failure produce visible decay.

## Status

**M0 complete; M1 in progress.** One Go process, in-memory event bus, deterministic simulation, and a live run engine behind a CLI projection. No Kafka, no gRPC, no containers yet — see [docs/roadmap.md](docs/roadmap.md) for what each milestone adds and why.

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
