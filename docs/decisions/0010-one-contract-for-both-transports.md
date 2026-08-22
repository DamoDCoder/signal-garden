# 0010: One contract for both transports, with the JSON shape written down

- **Date:** 2026-08-22
- **Status:** Accepted

## Context

The browser client moved to its own repository, so the contract now crosses a repository boundary instead of living beside its only consumer. That made an inconsistency that had been merely untidy into a defect.

A garden reached a client two ways, and the two disagreed:

| | REST gateway | Projection stream |
| --- | --- | --- |
| casing | `{"runId": …}` | `{"run_id": …}` |
| int64 | `"tick": "45"` — string | `"tick": 45` — number |
| unset fields | present, `EmitUnpopulated` | absent |

The stream's frames were hand-written Go structs in `internal/projection`; the REST shapes came from protojson through grpc-gateway. Neither was wrong on its own. Together they meant `snapshot.tick` was a string from one transport and a number from the other, and `snapshot.sequence` was `"0"` from one and `undefined` from the other at exactly the moment a run starts — a client comparing ticks would compare a string against a number and be quietly wrong rather than loudly broken.

## Options Considered

1. **Document it; let the client normalise.** Cheapest now, a tax on every consumer forever, and it puts the trap in a paragraph rather than in a type.
2. **Set `UseProtoNames` on the gateway marshaller.** Fixes casing only. The number types still differ, and the wire names now live in a Go flag in `main.go` rather than in the contract.
3. **Define the stream frames as protobuf messages and marshal them with protojson.** One definition, one marshaller configuration, both transports identical.

## Decision

Option 3, with the JSON shape stated explicitly in the `.proto` rather than left to a marshaller's defaults.

`Event`, `EventPayload`, `Catchup`, `FrameType`, and `ProjectionFrame` are messages in `garden.proto` that belong to **no rpc**. The architecture rule holds — the projection stream is still a read transport served directly by the daemon and not a gRPC method — while the thing it puts on the wire is the same contract the REST routes use. `internal/wire` holds the one translation from engine types to messages, and both `internal/service` and `internal/projection` call it.

Every field carries an explicit `json_name`. The JSON these messages produce is the public shape, and the default would leave the wire names determined by a marshaller option somewhere else. Written down, the `.proto` answers what a client will see without anyone having to know how protojson behaves.

The result is snake_case everywhere, matching the envelope already documented in [events.md](../events.md).

## What This Does Not Fix

**int64 encodes as a JSON string.** That is the protobuf JSON mapping, not a choice: JSON numbers lose precision above 2^53 and the spec requires 64-bit integers to be strings. `protojson` offers no option to emit them as numbers, and taking that away means abandoning protojson and the conformance that comes with it.

So the fix is uniformity rather than the more obvious shape: `tick`, `sequence`, `folded_offset`, `log_offset`, `committed_offset`, and `seed` are strings on **both** transports. A client parses them once at the edge. What it never has to do is know which transport a value arrived on, which was the actual defect.

`EmitUnpopulated` is set on the stream because grpc-gateway's default marshaller sets it. This is not cosmetic: without it a zero-valued field is present over REST and missing over the stream. Flipping the option makes `TestStreamAndGatewayAgreeOnTheWire` fail with `sequence` and every `moisture: 0` missing from the stream side, which is how we know the test is worth having.

That test resolves the gateway's marshaller the same way the running gateway does, rather than reconstructing what its defaults are believed to be, and compares the bytes. A change to either side's options fails there rather than in a browser.

## Consequences

- `internal/projection` no longer defines `Frame` or `Catchup` Go types. A consumer generates them from the contract.
- The contract lives in this repository and is generated here. The client repository consumes it; it does not define its own view of a garden.
- `event.Event` has a wire form for the first time. `recorded_at` is deliberately absent from it, because wall-clock time is never written to the log and a record read back has none to report.
- `event_type` stays a string rather than becoming an enum. Those values are declared wire strings in [events.md](../events.md), and a build meeting a type it did not know would decode an enum to `UNSPECIFIED` and lose which one it was — a client that cannot render an event should still be able to name it.

## What Would Revisit This

- A client that genuinely cannot afford to parse strings at the edge, at which point the question is whether to leave protojson rather than whether to have one contract.
- The stream needing a frame that has no business in a request/response contract — backpressure signals, server hints — which would be a reason for a stream-only message, not for a second encoding of a garden.
