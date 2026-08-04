# Signal Garden

Tune a living digital garden by controlling event producers and processors, then watch it flourish or collapse as throughput, lag, retries, and failures shape the simulation in real time.

Signal Garden is a local-first real-time event-processing laboratory disguised as a strategy game. Garden health is a direct consequence of system behavior: a steady event stream makes organisms grow, while overload, duplication, and failure produce visible decay.

## Status

**M0: Contract Spike.** One Go process, in-memory event bus, deterministic simulation, CLI projection. No Kafka, no gRPC, no containers yet — see [docs/roadmap.md](docs/roadmap.md) for what each milestone adds and why.

## Quick Start

```sh
make test    # domain, replay determinism, and idempotency tests
make run     # run a garden simulation and print the result
```

`make run` accepts the same flags as the binary:

```sh
go run ./cmd/signalgarden -seed 42 -ticks 40 -rate 6 -rain 3 -growth 2 -pest 1
```

Two runs with the same seed, tick count, and controls always produce the same final garden. That property is the point of M0, and it is what the replay tests assert.

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
