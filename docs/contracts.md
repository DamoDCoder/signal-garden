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

Frames are `ProjectionFrame` messages, marshalled exactly as the REST routes marshal theirs, so a client parses a `GardenSnapshot` the same way whichever transport delivered it:

```json
{"type": "FRAME_TYPE_SNAPSHOT", "run_id": "demo", "catchup": null, "snapshot": { ... }}
{"type": "FRAME_TYPE_CATCHUP",  "run_id": "demo", "snapshot": null,
 "catchup": {"from": "246", "to": "852", "events": [ ... ]}}
```

`ProjectionFrame` belongs to no rpc. The stream is a read transport served directly by the daemon, and the messages are here so there is one definition of a garden rather than two that can drift.

A catch-up frame arrives at most once, first, and only for a client that passed `from`. It carries the records between the offset that client resumed at and the garden the snapshot immediately after it describes, so `catchup.to` always equals the next frame's `folded_offset`. That equality is the handover: anything else is a record the client never sees or one it sees twice.

Catch-up events carry no `recorded_at`. Wall-clock time is never written to the log, so a record read back has none to report — see [events.md](events.md).

Rejections happen before the upgrade, so they are ordinary HTTP statuses rather than a socket that opens and immediately closes:

| Condition | Status |
| --- | --- |
| No such run | 404 |
| `from` is not a number, is negative, or names an offset the log never wrote | 400 |
| Registry shutting down | 503 |

An offset past the tail is refused rather than answered with an empty catch-up. A client holding an offset this run never reached is confused about which run it is watching, and an empty frame would let it stay that way.

A finished run sends its final frame and then a normal close, so a client can tell a completed run from a dropped connection.

## Generating A Client

The contract is generated from this repository and consumed by pinning a tag. For `protoc-gen-es`:

```sh
protoc -I proto -I third_party   --plugin=protoc-gen-es=./node_modules/.bin/protoc-gen-es   --es_out=src/gen --es_opt=target=ts,import_extension=.js   signal/garden/v1/garden.proto google/api/annotations.proto google/api/http.proto
```

The vendored `google/api` protos have to be generated alongside the contract — `garden_pb` imports the annotations file, and leaving it out fails at module resolution rather than at generation.

64-bit quantities arrive as `bigint`, so they do not mix with `number` in arithmetic and `JSON.stringify` throws on them. Use `toJsonString` from `@bufbuild/protobuf`, and `Number(...)` where a value is rendered. See [0012](decisions/0012-declare-the-js-type-of-every-64-bit-field.md).

## Cross-Origin Requests

The browser client runs on its own development server, so it is a different origin from the daemon. `SIGNAL_GARDEN_CORS_ORIGIN` controls what is allowed:

| Value | Meaning |
| --- | --- |
| `*` (default) | Reflect whatever origin asked |
| a specific origin | Allow only that one |
| empty | Add no CORS headers at all |

Credentials are never allowed, which is what keeps reflecting an arbitrary origin from being a hole: a browser attaches no cookies to these requests, so a hostile page learns nothing it could not learn by connecting to the port itself. This daemon serves one machine and holds no credentials; the public gateway that would authenticate and rate-limit it is a deployment concern.

WebSockets do not preflight, so a missing CORS policy would leave the stream working while every REST call failed — which reads as "the garden streams but no button works". That failure mode is the reason the middleware exists.

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

## JSON Conventions

The same rules hold on the REST routes and on the projection stream, because both marshal the same messages with the same options. [0010](decisions/0010-one-contract-for-both-transports.md) records why.

- **Field names are snake_case**, from an explicit `json_name` on every field in the contract. The wire shape is written down in the `.proto` rather than decided by a marshaller option in Go.
- **64-bit fields are JSON strings** — `"tick": "45"`, not `45`. This is the protobuf JSON mapping rather than a choice: JSON numbers lose precision above 2^53. It applies to `tick`, `sequence`, `seed`, and every offset, on both transports, so a client parses them once at the edge and never has to know which transport a value came from.
- **Every 64-bit field declares its JS type** with `jstype`, so a generated client is told whether a value is a quantity or a token rather than inferring it. `JS_NUMBER` for bounded quantities a client does arithmetic on — ticks, sequences, offsets, counters — and `JS_STRING` for opaque tokens, of which `seed` is the only one. Measured against `protoc-gen-es`, the generator the client uses: `run.seed` is a `string` and every quantity is a `bigint`. `JS_STRING` is honoured; `JS_NUMBER` is not, because `number` cannot hold an int64 losslessly — but the distinction survives, which is what it was for. The wire is a JSON string either way. `ProcessorStats.by_type` is the exception, because protoc refuses `jstype` on a map field. See [0012](decisions/0012-declare-the-js-type-of-every-64-bit-field.md).
- **Enums are their full names**: `"state": "RUN_STATE_RUNNING"`, `"type": "FRAME_TYPE_SNAPSHOT"`.
- **Unset fields are present**, as `0`, `""`, or `null`. A message field that is not set serialises as `null` rather than being omitted, so `catchup` is `null` on a snapshot frame.
- **Timestamps are RFC 3339**; durations are seconds with a suffix, `"0.200s"`.

## Compatibility Rules

- Add fields with new field numbers; never reuse removed numbers.
- Keep enum zero values safe and explicit.
- Preserve unknown fields during compatible proxying.
- Version breaking changes under a new package or route version.
- Include `run_id`, `event_id`, `sequence`, and `schema_version` where a message participates in replay or reconciliation.