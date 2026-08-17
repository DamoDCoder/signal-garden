# 0007: Never compact a run log

- **Date:** 2026-08-17
- **Status:** Accepted

## Context

The event spine offers `Compact` and `CompactAll`. Compaction keeps, for each key, the newest record that supersedes the others, and drops the rest. It preserves offsets — a dropped offset returns `ErrNotFound` from `Read`, and a `Reader` walks past it — so a committed offset still means the same record afterwards. The spine describes it as lossless, and for its intended use it is: "compaction only removes records a newer record for the same key supersedes."

That sentence contains the condition. It holds when a key's newest record is a complete statement of that key's state.

## Options Considered

1. **Compact on a schedule to bound disk.** The obvious use of the API.
2. **Compact only the keys that are genuinely last-write-wins.** Control changes are one such key: the newest revision for a run supersedes the previous ones.
3. **Never compact. Bound cost with snapshots instead.**

## Decision

Option 3. Signal Garden never calls `Compact` or `CompactAll` on a run log. Disk cost is bounded by snapshots and by run retention.

## Evidence

Signal Garden's events are **cumulative deltas, not state**. A rain event partitioned on `org-004` says "add this much moisture," and a growth event says "advance a stage." The newest rain event for that organism supersedes nothing; the organism's moisture is the fold of every one of them. Compacting the log would keep the last event per organism, report success, and leave a history that replays to a completely different garden. The failure is silent, it appears only on replay, and by then the records are gone.

Option 2 is technically true — `control_changed` really is last-write-wins per run — and was still rejected. Compaction is per segment, not per key, and a segment holds both kinds of record. Making it safe would mean partitioning control events into their own log so a compactable segment contains nothing else, which is real machinery bought to reclaim the smallest category of record in the run. Control changes are a handful per run; organism events are thousands per tick.

There is a second reason to keep the whole history even where compaction would be sound. [events.md](../events.md) makes replay from the event log the source of truth, and the document store holds snapshots. A compacted log can reproduce final state but not the *path*, and the path is what the replay demo and the determinism chain both check.

The risk this record exists to head off is not that someone argues for compaction. It is that the API is right there, it is named for something everyone wants, and its documentation says nothing is lost — which is true under an assumption Signal Garden does not meet.

## What Would Revisit This

- A record type is introduced whose newest value genuinely supersedes its predecessors *and* lives in its own log, at which point compacting that log is sound.
- Snapshots plus retention stop being enough to bound disk, which would be a reason to expire whole runs rather than to thin the records inside one.
