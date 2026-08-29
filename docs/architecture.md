# Signal Garden Architecture

## Runtime Topology

```mermaid
flowchart LR
    UI[React web client] -->|Generated REST JSON| Gateway[Go REST gateway]
    UI <-->|WebSocket projection stream| Projection[Go projection gateway]

    subgraph Daemon["signalgardend — one process"]
        Gateway -->|gRPC| Control[Go control service]
        Control --> Engine[Run engine]
        Engine --> Producer[Go event producer]
        Producer --> Log[(Event Spine log<br/>one per run)]
        Log --> Processor[Go processor]
        Processor --> Engine
        Engine --> Projection
        Processor --> Store[(Snapshots)]
    end

    Replay[Replay and load tools] --> Log
    Control --> Telemetry[Prometheus + traces]
    Processor --> Telemetry
    Projection --> Telemetry
```

The event path is in-process by [0004](decisions/0004-event-spine-replaces-kafka-as-the-event-backbone.md): the log is a library, not a broker. The boxes inside the daemon keep their responsibilities and stop being candidate process boundaries. The gRPC and REST boundary between the client and the daemon is unchanged.

## Service Responsibilities

| Service | Responsibility | State authority |
| --- | --- | --- |
| Control service | Start runs, validate controls, pause, finish, expose run metadata | Run lifecycle and control revisions |
| Event producer | Turn accepted controls into deterministic domain events | None; emits commands/events |
| Event log | Durably hold every event of a run, in order, and redeliver on restart | The run's event history |
| Processor | Validate, order, deduplicate, and apply events | Garden projection state |
| Projection gateway | Fan out snapshots over WebSockets and serve reconnect catch-up | Connection state only |
| REST gateway | Generated public HTTP/JSON adapter over gRPC | None |
| React web client | Render projections and send control commands; lives in its own repository | None |
| Replay/load tools | Reproduce fixtures and generate controlled pressure | None |

## Boundary Rules

- Business APIs are defined in protobuf and served internally over gRPC. This repository owns that definition; the client repository consumes it and never describes a garden of its own — see [0011](decisions/0011-the-ui-is-a-separate-repository.md).
- Public HTTP/JSON routes are generated from the same protobuf definitions with grpc-gateway or an equivalent generator.
- WebSockets are a projection/read stream, not a second command API. Commands go through gRPC or generated REST.
- Both transports serve one contract. The stream's frames are protobuf messages belonging to no rpc, marshalled exactly as the REST routes marshal theirs, so a client parses a garden the same way whichever delivered it — see [0010](decisions/0010-one-contract-for-both-transports.md). `internal/wire` holds the single translation both use.
- The [Event Spine](https://github.com/DamoDCoder/event-spine) log is the durable event transport from M2. The in-memory bus it replaces was permitted only for M0.
- One log per run, owned by that run's goroutine. The log takes no locks, so nothing may touch it from outside — see [0005](decisions/0005-one-log-per-run-owned-by-the-run-goroutine.md). Reconnect catch-up reads the log, so it is a command handed to the run rather than a second reader: the projection gateway asks, and the run's own goroutine answers.
- A reconnecting client is handed its missed records and the snapshot standing at the end of them in one pass of that goroutine, which is what makes the handover exact rather than approximately current.
- Run logs are never compacted. Events are cumulative deltas, so dropping superseded records changes the garden — see [0007](decisions/0007-never-compact-a-run-log.md).
- Delivery is at-least-once. The processor deduplicates on the idempotency key in [events.md](events.md); nothing depends on exactly-once.
- The processor is the authority for garden state. Clients render projections and do not calculate authoritative outcomes.
- Snapshots bound replay cost and hold run metadata; the event log remains the source for replay.
- Prometheus metrics are emitted at service boundaries: tick duration, gRPC/REST call duration,
  events processed, snapshots dropped, and projection freshness. Deliberately no `run_id` label —
  Prometheus label values must stay bounded, and a run ID is not — so metrics are global or labeled
  only by closed sets (event outcome, gRPC method and code). Per-run drill-down stays on the
  existing `GetTelemetry` poll. See [0016](decisions/0016-prometheus-metrics-carry-no-run-id-label.md).
- Run/event correlation is OpenTelemetry traces' job, per 0016 — and it is built, at two
  granularities: every gRPC call is a span (`otelgrpc`, covering REST too since the gateway dials
  this same server), and every tick is a span carrying `run.id`/`tick`, with a span event when that
  tick's periodic snapshot save retried or failed. Not per-event — see
  [0019](decisions/0019-traces-are-tick-and-rpc-grained-not-per-event.md) for why. Exported OTLP/gRPC
  to an optional local endpoint (`SIGNAL_GARDEN_OTEL_ENDPOINT`); unset, tracing is a noop and costs
  nothing.

## Initial Deployment Shape

One container for `signalgardend`, one for the React development server. The event log is a library inside the daemon, so it needs a mounted data directory rather than a service of its own. Keep the topology simple enough to inspect with ordinary Docker Compose commands.

The compose file lives in [app.signal-garden](https://github.com/DamoDCoder/app.signal-garden) rather than here. The dependency runs one way — the client needs a daemon, the daemon needs nothing from the client — so the file describing both belongs with the component that has the dependency. This repository stays runnable with `task serve` and no orchestration at all. See [0011](decisions/0011-the-ui-is-a-separate-repository.md).