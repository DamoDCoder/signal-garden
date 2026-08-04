# Signal Garden Contracts

## When These Contracts Land

M0 is a single process with no service boundary, so it defines its seams as Go interfaces rather than protobuf. Everything below is the M1 target: it takes effect when the control service, projection gateway, and processor become separate processes. The M0 interfaces are written to match these method shapes so the translation is mechanical.

## Protobuf And gRPC

The first contract file should define a versioned package such as `signal.garden.v1` and generate Go server/client code plus the REST gateway. Initial methods:

```text
StartRun(StartRunRequest) returns (Run)
GetRun(GetRunRequest) returns (Run)
UpdateControls(UpdateControlsRequest) returns (ControlRevision)
PauseRun(PauseRunRequest) returns (Run)
FinishRun(FinishRunRequest) returns (RunSummary)
GetSnapshot(GetSnapshotRequest) returns (GardenSnapshot)
GetTelemetry(GetTelemetryRequest) returns (TelemetrySnapshot)
```

The exact `.proto` files belong in the standalone project repository. This planning pack defines the boundary before implementation; generated code should never be hand-edited.

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