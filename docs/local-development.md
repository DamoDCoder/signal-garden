# Signal Garden Local Development

## Local Stack By Milestone

| Milestone | Dependencies |
| --- | --- |
| M0 | Go only; in-memory adapters and a CLI projection |
| M1 | Docker Compose, Go services, React, generated gateway, WebSocket endpoint |
| M2 | Kafka, document store, replay tooling |
| M3 | OpenTelemetry collector, Prometheus-compatible metrics, load generator |

## Expected Commands

The standalone repository should provide a Makefile or equivalent task runner with commands like:

```text
make proto       # generate Go gRPC and REST gateway code (M1+)
make test        # unit and contract tests
make test-integration
make dev         # start the local application stack
make reset       # remove local state and restart fixtures
make load        # run a deterministic event burst
make replay      # replay a fixture event log
```

M0 needed only `make test` and `make run`. `make live` arrived with the M1 run engine, and drives a clock-paced run from the terminal before any service boundary exists. The rest arrive with the milestone that makes them meaningful.

## Local Principles

- No cloud credentials for any milestone through M4.
- External feeds are not required; use deterministic fixture events.
- Generated code is reproducible from pinned tool versions.
- Docker Compose health checks gate dependent services.
- Test data and run state can be reset without manually deleting opaque volumes.
- A fresh clone should have a documented path from setup to the first live garden.