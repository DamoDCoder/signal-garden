# 0006: The daemon refuses to start on corrupt recovery

- **Date:** 2026-08-17
- **Status:** Accepted

## Context

Opening a run's log returns a `Recovery` alongside it. The spine truncates a **torn** tail without asking — that is the normal result of a crash partway through an append, and at most one in-flight write goes. It does not decide what a **corrupt** tail means:

```go
if recovery.Corrupt != nil {
    // Bytes were present and wrong. The log truncated them and kept going.
}
```

The spine's position is that only the caller knows whether this disk deserves trust, so it reports and stops. Signal Garden has to answer.

The answer only matters where a log is reopened: resuming runs after a daemon restart, and the replay command. A run starting fresh opens an empty directory and has nothing to recover.

## Options Considered

1. **Refuse to start, with no override.** Simplest to reason about. A corrupt disk means manual intervention and there is no in-band escape.
2. **Refuse by default, `--on-corrupt=continue` as an explicit opt-out.** Same default, with an operator-visible way to take the other decision knowingly.
3. **Log and continue by default.** The daemon stays up; the corruption lands in run metadata and telemetry. Prioritises availability.

## Decision

Option 2. `signalgardend --on-corrupt` takes `refuse` (default) or `continue`. Under `refuse`, a corrupt recovery on any run log aborts startup with the run ID and the byte count in the error. Under `continue`, the daemon starts, and the corruption is recorded in `Run.Failure` and reflected in `/readyz`.

## Evidence

The project's claim is that the same seed and command sequence produce the same garden. A corrupt tail means the disk returned bytes that were wrong. Continuing serves a garden that no event history explains, and — worse — there is no way to tell the client which one they got, because the contract has no field for "this projection may be wrong."

`continue` carries an obligation that is not obvious and is the reason it is not the default. Compaction preserves offsets, so a committed offset keeps meaning the same record; **truncation does not**. Recovery moves the tail back, and later appends are assigned offsets that different records used to hold. A consumer group's committed offset or a snapshot offset taken before the corruption therefore points at a *different record* afterwards, and resuming from it silently folds the wrong history. Systems that solve this use leader epochs; the spine does not.

So `continue` must, before serving anything:

- discard every group commit at or after `recovery.Next`, and
- discard every snapshot at or after `recovery.Next`.

That is the whole of the mode. Without those two steps, `continue` is not "carry on with less data" — it is "resume from an offset that now means something else," which is worse than refusing and harder to notice.

Option 3 was rejected because it makes that subtle failure the default path. Option 1 was rejected because refusing to start is the right default but a bad only-option: an operator who has decided the loss is acceptable should be able to say so once, visibly, rather than by editing files under the daemon.

`recovery.Discarded` is reported in both modes, so the size of what went is always on the record.

## What Would Revisit This

- Corrupt recovery turns out to be common enough in normal operation that refusing is an availability problem, which would point at hardware or at a framing bug rather than at this policy.
- Run history gains a second copy, at which point recovering from the other copy beats both options here.
- The spine adds epochs or another mechanism that makes an offset unambiguous across truncation, which would remove the reason `continue` has to discard commits and snapshots.
