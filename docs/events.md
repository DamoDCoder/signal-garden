# Signal Garden Events

## Envelope

Every durable event should carry an envelope equivalent to:

```text
event_id       unique identifier
event_type     rain | growth | pest | control_changed | run_state_changed
schema_version version of the event payload
run_id         simulation run
entity_id      organism or garden target
partition_key deterministic Kafka key
sequence       producer or projection sequence where applicable
occurred_at    simulation time
recorded_at    wall-clock ingestion time
attempt        processing attempt
payload        typed event data
```

## Initial Domain Events

| Event | Producer | Processor effect | Idempotency key |
| --- | --- | --- | --- |
| `rain` | Event producer | Adds moisture to matching organisms | `event_id` |
| `growth` | Event producer | Applies growth when moisture and health allow it | `event_id` |
| `pest` | Event producer | Reduces health and may create a retryable side effect | `event_id` |
| `control_changed` | Control service | Changes future producer behavior | `run_id + revision` |
| `run_state_changed` | Control service | Starts, pauses, or finishes a run | `run_id + revision` |

## Kafka Planning Defaults

- Raw topic: `signal-garden.v1.raw-events`.
- Processed topic: `signal-garden.v1.processed-events`.
- Partition key: `run_id` for ordering within a run during the first release.
- Retention: long enough to replay local demo runs; make the exact duration configurable.
- Consumer group: `signal-garden-processor-v1`.

These are starting hypotheses, not permanent infrastructure decisions. M3 should measure whether run-level ordering limits useful parallelism and whether organism-level partitioning is worth the added complexity.

## Replay Rules

- Replaying the same event fixture with the same rules version must produce the same snapshot hash.
- Wall-clock timestamps must not influence simulation outcomes.
- Duplicate events must be safe to reprocess or explicitly rejected as already applied.
- Rules changes require a versioned replay strategy and a documented incompatibility decision.