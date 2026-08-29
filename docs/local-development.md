# Signal Garden Local Development

## Local Stack By Milestone

| Milestone | Dependencies |
| --- | --- |
| M0 | Go only; in-memory adapters and a CLI projection |
| M1 | Go services, generated gateway, WebSocket endpoint; Docker to build the daemon image. React and the Compose file live in [app.signal-garden](https://github.com/DamoDCoder/app.signal-garden) |
| M2 | Go only; the event log is a library and needs a data directory, plus snapshot storage and replay tooling |
| M3 | Go only — Prometheus metrics (`/metrics`), `task load`, failure injection, and traces are all done, and none of them need this repository to own or run any infrastructure. Traces need an external, optional local viewer to be worth looking at (e.g. `docker run` a Jaeger container) — not a collector; OTLP goes straight to it |

## Task Runner

Commands live in [Taskfile.yml](../Taskfile.yml) and run with [go-task](https://taskfile.dev):

```sh
brew install go-task        # or: go install github.com/go-task/task/v3/cmd/task@latest
task --list                 # the index; running bare `task` prints the same
```

`task --list` is the contract rather than this document — a task's own `desc` is what a fresh clone reads, and a list here would drift out of date the first time one changes.

What exists today: `test`, `test-race`, `check`, `run`, `live`, `load`, `serve`, `demo`, `build`, `build:docker`, `docker:build`, `docker:run`, `docker:images`, `fmt`, `vet`, `proto`, `tools`, and `clean`.

Arguments after `--` reach the underlying binary, so a one-off run needs no task of its own:

```sh
task run -- -seed 42 -ticks 40 -pest 4
task live -- -run demo -data ./data
```

`task load` drives a running daemon over gRPC with a controlled event burst — `task serve` (or `task up`) needs to already be serving, since it exercises the real serving path rather than simulating one; see `task load -- -h`.

To see a trace: `docker run --rm -p 16686:16686 -p 4317:4317 jaegertracing/all-in-one:latest`, then `SIGNAL_GARDEN_OTEL_ENDPOINT=localhost:4317 task serve`, then drive some activity (`task load`, or plain `curl`) and open `http://localhost:16686`. Unset the env var and tracing costs nothing — that's the default, including in `task up`'s compose stack. See [0019](decisions/0019-traces-are-tick-and-rpc-grained-not-per-event.md) for what's traced and at what granularity.

`dev` and `reset` are not coming here — the compose file that would back them lives in the client repository, because the dependency runs that way round. This repository serves with `task serve` and needs no orchestration to be useful. It does build its own image, though: `docker:build` produces the `signalgardend` container that compose over there runs, because an artifact belongs to the repository that knows how to make it and a *stack* belongs to the one that has the dependency. See [0011](decisions/0011-the-ui-is-a-separate-repository.md) and [0015](decisions/0015-ship-an-image-but-not-a-stack.md). `replay` is deliberately not a task — it takes a run ID and a data directory that only the person running it knows, so it stays `go run ./cmd/signalgarden -replay -run <id> -data <dir>`.

`task proto` needs protoc on the PATH. The plugins are pinned by the tool directives in `go.mod` and built into `bin/tools`, and the googleapis annotation protos are vendored in `third_party/`, so generation runs offline. Generated code is committed, so building and testing need none of this.

## Local Principles

- No cloud credentials for any milestone through M4.
- Every command a contributor needs is a task, and `task --list` describes it.
- External feeds are not required; use deterministic fixture events.
- Generated code is reproducible from pinned tool versions.
- Docker Compose health checks gate dependent services.
- Test data and run state can be reset without manually deleting opaque volumes.
- A fresh clone should have a documented path from setup to the first live garden.
- The contract is defined and generated here. A consumer pins a tag rather than tracking `main`.