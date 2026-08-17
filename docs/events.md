# Signal Garden Events

## Envelope

Every durable event should carry an envelope equivalent to:

```text
event_id       unique identifier
event_type     rain | growth | pest | control_changed | run_state_changed
schema_version version of the event payload
run_id         simulation run
entity_id      organism or garden target
partition_key deterministic log key; ordering is guaranteed within a key
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

## Log Planning Defaults

The durable transport is the [Event Spine](https://github.com/DamoDCoder/event-spine) log, one per run — see [0004](decisions/0004-event-spine-replaces-kafka-as-the-event-backbone.md).

- Location: `<data>/runs/<run_id>/`. A run's history is a directory, which is what the replay command reads.
- Partition key: `run_id` for ordering within a run during the first release.
- Durability: `log.Sync`. One `Append` call per tick means one fsync per tick, which is free at a 200ms pace and makes the crash story "nothing acknowledged is lost."
- Consumer group: `projections`. Reading does not move it; committing does, and only after the projection has folded the records.
- Retention: whole runs expire; records inside a run do not. Run logs are never compacted ([0007](decisions/0007-never-compact-a-run-log.md)).
- Snapshot cadence: every 50 ticks, and once more when a run finishes. Ten seconds of play at the default pace — short enough that a restart is not visibly slow, long enough that the writes are not the workload.
- Committing: only ever immediately after a snapshot, and to the same offset. A commit promises those records never need delivering again, which is true only once the state built from them is durable.

A snapshot is a shortcut past records already folded, never a second source of truth. Deleting every snapshot in a run's directory costs replay time and changes nothing else, which is the property that makes it safe to keep.

The durable record carries the envelope above **minus `recorded_at`**. Wall-clock time is not part of the simulation, and putting it in the payload would make two byte-identical runs produce different records.

These are starting hypotheses, not permanent infrastructure decisions. M3 should measure whether run-level ordering limits useful parallelism and whether organism-level partitioning is worth the added complexity.

## Replay Rules

- Replaying the same event fixture with the same rules version must produce the same chain digest. A terminal snapshot hash is not sufficient evidence — see [0008](decisions/0008-assert-determinism-on-a-chain-not-a-terminal-hash.md).
- Wall-clock timestamps must not influence simulation outcomes, and are not written to the log.
- Delivery is at-least-once. Duplicate events must be safe to reprocess or explicitly rejected as already applied.
- Rules changes require a versioned replay strategy and a documented incompatibility decision.