# Signal Garden Contracts

## Where These Contracts Live

The contract is [proto/signal/garden/v1/garden.proto](../proto/signal/garden/v1/garden.proto). Generated Go lands in `internal/gen/` and is committed, so a clean checkout builds without protoc; only regeneration needs the toolchain. Run `make proto`. See [0003](decisions/0003-generate-with-protoc-and-vendored-googleapis.md) for why generation uses protoc with vendored googleapis protos rather than buf.

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

## Compatibility Rules

- Add fields with new field numbers; never reuse removed numbers.
- Keep enum zero values safe and explicit.
- Preserve unknown fields during compatible proxying.
- Version breaking changes under a new package or route version.
- Include `run_id`, `event_id`, `sequence`, and `schema_version` where a message participates in replay or reconciliation.