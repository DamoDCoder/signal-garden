# 0018: Failure injection targets the snapshot save, not event processing

- **Date:** 2026-08-29
- **Status:** Accepted

## Context

M3's exit criteria ask that `retries` be visible, and the `Controls` message has carried a comment
since the worker/batch slice: "Retry policy is failure injection's... new controls take new field
numbers when they become real." Nothing in the codebase has a retry mechanism today, so the first
question is where one could honestly go.

Every non-success outcome `processor.Process` and `domain.Apply` can produce — rejected (envelope
validation), unknown_entity, no_effect — is **permanent** given the garden's current state:

- **Rejected** means the record itself is malformed. Re-delivering identical bytes fails
  `Validate()` identically every time.
- **UnknownEntity** means `EntityID` isn't in `Garden.index`, which is fixed at construction —
  organisms are never added or removed mid-run. A retry of the same event misses the same lookup
  forever.
- **NoEffect** means the rule ran and changed nothing (a dead organism, moisture at cap, a
  pest event with `amount<=0`). Retrying the identical event changes nothing about the state that
  made it a no-op.

`domain.Apply`/`processor.Process` have no I/O and no external dependency — everything is pure
in-memory computation over already-durable data. There is no transient condition anywhere in that
path to retry. The only genuinely failable, transient operation anywhere in live processing is disk
I/O: appending a tick's events to the log (`Log.Append`, every tick, durability-critical per M2),
and writing the periodic on-disk snapshot (`Sim.Save`, every `SnapshotEvery` ticks, explicitly a
non-critical optimization — "purely so a restart folds fewer records," per the daemon's own README).

## Options Considered

1. **Inject failure into event processing anyway** — e.g., fail every Nth event with a synthetic
   "transient" error and retry it. Rejected: there is no real transient condition to model here: it
   would be pure fiction, the same category of dishonesty `docs/decisions/0017` already ruled out
   for fake per-event latency. A metric backed by a fabricated failure isn't measuring anything.
2. **Inject failure into `Log.Append`** — the durability-critical write every tick depends on.
   Real and transient in principle, but it's the exact write M2's crash-survival guarantees are
   built on; conflating a demo control with that path risks the guarantee itself, and retrying it
   incorrectly is a much more consequential bug than retrying a snapshot incorrectly.
3. **Inject failure into `Sim.Save`** (the periodic snapshot). Real disk I/O, genuinely transient,
   and — because a snapshot is provably not required for correctness (delete every
   `snapshot-*.state` and replay reaches the same garden, just slower) — the lowest-stakes place to
   get this wrong.

## Decision

Option 3. `Controls.fail_snapshot_every` makes every Nth invocation of `Save` fail its first
attempt with a synthetic error, then retry for real. The retry loop (`maxSnapshotSaveAttempts = 3`)
also covers a genuinely real `Log.Save` error the same way — a small, honest robustness improvement
riding along with the demo feature, not just a demo feature. `snapshot_save_retries` counts every
attempt beyond the first; `snapshot_save_failures` counts every time all attempts were exhausted.
Under normal `fail_snapshot_every` use, `snapshot_save_failures` stays zero — the injected failure
only ever occupies attempt one, so the demoable story is a transient failure that **recovers**,
which is the positive, new thing worth showing. "The run terminates on an unrecoverable disk error"
already exists today and isn't new; M2's crash matrix (`internal/sim/crash_test.go`, via
event-spine's `fs.Crash()`/`CrashTorn()`) already covers that scenario honestly.

The invocation counter and the two new counters live only on the in-memory `Sim`, not in the
snapshot's own schema (`SnapshotSchemaVersion`). This is an observability control for a demo, not
run state a restart needs to reproduce — persisting it would conflate the two and cost a schema
version bump for no correctness benefit.

## Consequences

`snapshot_save_retries`/`snapshot_save_failures` are visible per-run (`TelemetrySnapshot`) and
globally (`signal_garden_snapshot_save_retries_total`/`_failures_total`, unlabeled — same no-`run_id`
reasoning as every other metric since `docs/decisions/0016`, and unlike `pending` there's no
cross-run last-writer-wins risk here since these are plain monotonic counters, not a live gauge).
Event processing gains no new failure/retry concept — `rejected`, `unknown_entity`, and
`no_effect` remain what they always were.

## What Would Revisit This

- `Log.Append` gaining a controllable failure/retry story of its own, once there's a way to do it
  that cannot be mistaken for weakening M2's durability guarantee — likely its own decision record,
  not an extension of this one.
- A genuinely transient condition arriving in the event-processing path (e.g., a future external
  call `domain.Apply` makes) — at that point option 1 stops being fiction.
