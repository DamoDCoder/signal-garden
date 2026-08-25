# 0012: Declare the JS type of every 64-bit field

- **Date:** 2026-08-22
- **Status:** Accepted

## Context

[0010](0010-one-contract-for-both-transports.md) made both transports agree, and the shape they agreed on encodes int64 as a JSON **string**: `"tick": "45"`, not `45`.

That is the protobuf JSON mapping rather than a preference. JSON numbers are IEEE-754 doubles, so integers above 2^53 lose precision silently, and the mapping requires 64-bit integers to be strings to avoid it. `protojson` offers no option to change it, and neither does any conformant implementation.

The uniformity fixed the bug — a client no longer gets a string from one transport and a number from the other. It did not answer the question a client still has to ask every time: is this field a quantity I should be doing arithmetic on, or a token I should be passing around unchanged? The wire looks identical either way, and the answer lived only in whoever remembered.

## Options Considered

1. **Leave it. Document the encoding and let each consumer decide.** The rule survives exactly as long as the person who wrote it, and the eleventh int64 field added in a hurry breaks it silently.
2. **Narrow fields to int32 so they encode as JSON numbers.** Picks the domain type to suit an encoding, which is backwards — and it does not survive contact with this domain. `int32` exhausts in roughly six hours of M3's load lab at 100k events/second, so log offsets and event sequences genuinely need 64 bits.
3. **Emit numbers on the wire with a custom marshaller.** Requires abandoning `protojson`, replacing grpc-gateway's marshaller, losing conformance, and reintroducing the precision bug the mapping exists to prevent.
4. **Declare the intended JS representation in the contract, per field.**

## Decision

Option 4, using `jstype` — the field option protobuf provides for exactly this.

```proto
int64 seed = 2 [json_name = "seed", jstype = JS_STRING];
int64 tick = 5 [json_name = "tick", jstype = JS_NUMBER];
```

The rule: **`JS_NUMBER` for bounded quantities a client does arithmetic on; `JS_STRING` for opaque 64-bit tokens.**

Under it, `seed` is the only `JS_STRING` field in the contract. It is an arbitrary 64-bit value nobody adds to anything, and it is the one field here that can legitimately exceed 2^53. Everything else — ticks, sequences, log offsets, processor counters — is a quantity a client compares and displays, and every one is bounded far below 2^53. At the same load-lab rate that exhausts int32 in six hours, 2^53 log offsets is about 2,800 years.

This matches how the rest of the contract is written. Field names are stated with `json_name` rather than left to a marshaller's default, and this states the numeric representation the same way: at the definition, where a reader of the `.proto` finds it, rather than in a generator flag in another repository.

## What This Does And Does Not Change

**It does not change the wire.** JSON still carries `"tick":"45"`. `curl` sees a string, Go sees an `int64`, and [0010](0010-one-contract-for-both-transports.md)'s guarantee that both transports agree is untouched.

**It does not change the server.** Regenerating with all 26 annotations added changed 100 lines of `garden.pb.go`, every one of them inside the embedded descriptor blob. Not one Go type, struct tag, or function differs. The annotation travels in the descriptor for a client generator to read.

**It changes what a generated client is told.** The declaration travels in the descriptor and a TypeScript generator reads it. Exactly what it does with it is measured below rather than assumed.

**One field is out of reach.** protoc refuses `jstype` on a map field, so `ProcessorStats.by_type` keeps string values in a generated client. It is the only map of 64-bit values in the contract, and its values are display counters.

## Validated Against protoc-gen-es

The client repository generates with `protoc-gen-es` (`@bufbuild/protoc-gen-es` 2.14.0). Running it against this contract, and round-tripping JSON captured from the live daemon back through the generated code:

| Field | Declared | Generated TS type |
| --- | --- | --- |
| `Run.seed` | `JS_STRING` | `string` |
| `Run.tick`, `max_ticks` | `JS_NUMBER` | `bigint` |
| `GardenSnapshot.sequence`, `folded_offset` | `JS_NUMBER` | `bigint` |
| `Run.organisms`, `revision` | *(int32)* | `number` |

**`JS_STRING` is honoured. `JS_NUMBER` is not.** Generating with every `JS_NUMBER` annotation stripped produces byte-identical TypeScript — the only difference is the embedded descriptor — so for this generator `JS_NUMBER` is a no-op and `bigint` is what a 64-bit quantity becomes either way.

That is protobuf-es being right rather than incomplete. `number` cannot hold an int64 without losing precision, and a generator that silently produced one would reintroduce exactly the bug the JSON mapping avoids.

**The rule still does its job.** What we wanted was a client that can tell a quantity from a token, and it can: `run.seed` is a `string` it passes around, `snapshot.tick` is a `bigint` it does arithmetic on. `tick + 1n` works, `foldedOffset > 0n` works, and a round-trip re-emits `"42"` and `"13"` exactly as the daemon sent them. Only the name of the annotation is imprecise for this generator; the distinction it encodes is delivered.

The annotations stay. `JS_NUMBER` is the honest declaration of intent, a generator that honours it produces `number`, and the enforcement test below is what makes the question unavoidable for the next field.

**What the client has to know:** 64-bit quantities are `bigint`, so they do not mix with `number` in arithmetic, and `JSON.stringify` throws on them. Use `toJsonString` from `@bufbuild/protobuf` rather than `JSON.stringify`, and `Number(...)` at the point of rendering.

Generation needs `--es_opt=target=ts,import_extension=.js`, and the vendored `google/api` protos have to be generated alongside the contract — `garden_pb` imports the annotations file.

## Enforcement

`TestEvery64BitFieldDeclaresItsJSType` walks the compiled descriptor and fails any 64-bit field that declares no `jstype`, or declares one the rule does not call for. Removing a single annotation fails it by name, which is how we know it is doing something: the rule is a test rather than a paragraph, so the next 64-bit field cannot be added without answering the question.

## What Would Revisit This

- A field that is genuinely both — a quantity that can exceed 2^53 — which would need `JS_STRING` and a client that does its arithmetic in `BigInt`.
- protobuf-es gaining `JS_NUMBER` support, which would turn every quantity from `bigint` into `number` in one regeneration and is worth noticing before it happens silently.
- A client generator with no `jstype` support and no way to configure the same outcome, which would move the declaration into that repository's generation config and make this record the thing it points at.
