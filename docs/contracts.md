# Signal Garden Contracts

## Where These Contracts Live

The contract is [proto/signal/garden/v1/garden.proto](../proto/signal/garden/v1/garden.proto). Generated Go lands in `internal/gen/` and is committed, so a clean checkout builds without protoc; only regeneration needs the toolchain. Run `task proto`. See [0003](decisions/0003-generate-with-protoc-and-vendored-googleapis.md) for why generation uses protoc with vendored googleapis protos rather than buf.

M0 defined these seams as Go interfaces rather than protobuf, because a single process has no boundary to separate — see [0001](decisions/0001-defer-grpc-to-m1.md). The service is an adapter over `internal/engine`, which owns run lifecycle.

## Protobuf And gRPC

The contract defines package `signal.garden.v1` and generates Go server/client code plus the REST gateway. Methods:

```text
StartRun(StartRunRequest) returns (Run)
GetRun(GetRunRequest) returns (Run)
UpdateControls(UpdateControlsRequest) returns (ControlRevision)
PauseRun(PauseRunRequest) returns (Run)
FinishRun(FinishRunRequest) returns (RunSummary)
GetSnapshot(GetSnapshotRequest) returns (GardenSnapshot)
GetTelemetry(GetTelemetryRequest) returns (TelemetrySnapshot)
```

`PauseRunRequest` carries a `paused` boolean rather than pairing with a separate `ResumeRun`, so the two directions are one method with one meaning.

Generated code is never hand-edited.

Errors map to status codes rather than message text, because that mapping is what a client branches on:

| Condition | gRPC code | REST |
| --- | --- | --- |
| No such run | `NOT_FOUND` | 404 |
| Run ID already in use, live or as history on disk | `ALREADY_EXISTS` | 409 |
| Command against a finished run | `FAILED_PRECONDITION` | 400 |
| Rejected controls or start request | `INVALID_ARGUMENT` | 400 |
| Run log opened corrupt under the refuse policy | `DATA_LOSS` | 500 |
| Registry shutting down | `UNAVAILABLE` | 503 |

A run ID is taken by history as well as by a live run: a finished run leaves a directory behind, and starting a new run into it would interleave two histories in one log. Omit `run_id` and the server picks one that is free.

## Generated REST Surface

Expose only public control and query operations through generated REST/JSON routes:

- `POST /v1/runs`
- `GET /v1/runs/{run_id}`
- `PATCH /v1/runs/{run_id}/controls`
- `POST /v1/runs/{run_id}:pause`
- `POST /v1/runs/{run_id}:finish`
- `GET /v1/runs/{run_id}/snapshot`
- `GET /v1/runs/{run_id}/telemetry`

WebSocket streaming remains a separate transport because it is not a conventional request/response API. The public gateway should authenticate and rate-limit future internet-facing use, even while local development remains open.

## Projection Stream

```text
GET /v1/runs/{run_id}/stream          a new client, starting at the current garden
GET /v1/runs/{run_id}/stream?from=N   a returning client, resuming at log offset N
```

The stream is a read transport: nothing a client sends over it changes a run, and the daemon discards client messages other than the pongs that keep the socket alive. It is served from the daemon rather than the gateway, and it carries no protobuf — it is not a gRPC method, per [architecture.md](architecture.md).

Frames are JSON text messages using the field names in [events.md](events.md):

```json
{"type": "snapshot", "run_id": "demo", "snapshot": { ... }}
{"type": "catchup",  "run_id": "demo", "catchup": {"from": 246, "to": 852, "events": [ ... ]}}
```

A `catchup` frame arrives at most once, first, and only for a client that passed `from`. It carries the records between the offset that client resumed at and the garden the snapshot immediately after it describes, so `catchup.to` always equals the next frame's `folded_offset`. That equality is the handover: anything else is a record the client never sees or one it sees twice.

Rejections happen before the upgrade, so they are ordinary HTTP statuses rather than a socket that opens and immediately closes:

| Condition | Status |
| --- | --- |
| No such run | 404 |
| `from` is not a number, is negative, or names an offset the log never wrote | 400 |
| Registry shutting down | 503 |

An offset past the tail is refused rather than answered with an empty catch-up. A client holding an offset this run never reached is confused about which run it is watching, and an empty frame would let it stay that way.

A finished run sends its final frame and then a normal close, so a client can tell a completed run from a dropped connection.

Telemetry does not stream yet. The performance panel polls `GET /v1/runs/{run_id}/telemetry`, and folding it into the stream is M3's, when the counters become histograms worth pushing.

## Log Offsets

Three offsets cross the wire, and they mean different things:

| Field | Message | Meaning |
| --- | --- | --- |
| `folded_offset` | `GardenSnapshot` | The first record this garden has not folded. A client that has this frame resumes from here. |
| `log_offset` | `TelemetrySnapshot` | The offset the log will assign next, so also how many records the run holds. |
| `committed_offset` | `TelemetrySnapshot` | How far the projections group has durably folded — where a restart resumes. |

`committed_offset` moves at snapshot cadence rather than per tick, because nothing commits without first writing the state built from those records. The gap between it and `log_offset` is what a restart would redeliver, and idempotent processing is what makes that harmless.

`sequence` orders frames within a connection; `folded_offset` names the history behind one. They are not interchangeable: sequence counts frames the run emitted, and a run emits none while nobody is watching.

## Compatibility Rules

- Add fields with new field numbers; never reuse removed numbers.
- Keep enum zero values safe and explicit.
- Preserve unknown fields during compatible proxying.
- Version breaking changes under a new package or route version.
- Include `run_id`, `event_id`, `sequence`, and `schema_version` where a message participates in replay or reconciliation.