# 0014: A restarted daemon resumes its runs

- **Date:** 2026-08-25
- **Status:** Accepted

## Context

[0013](0013-derive-each-tick-s-randomness.md) removed the last thing about a run that could not be written down. A producer's position became `(seed, tick)`, so the question stopped being *can* a run resume and became *what does a resumed run need to know*.

A run's log answers less than it looks like it does. Records describe what a run **produced** — they say nothing about what it **was** or what it was **doing**. Seed, controls, organism count, pace, tick limit, and lifecycle appear in no record, so a daemon reading a directory of records can rebuild a garden and cannot rebuild a run.

## Decision

The snapshot carries what the records cannot, and `SnapshotSchemaVersion` goes to 2 to say so. It gains the run's lifecycle `State` and its operational parameters — `MaxTicks`, `TickInterval`, `DuplicateEvery`. Seed, controls, and organisms were already there.

`sim.Resume` rebuilds a whole simulation rather than a garden: `Rebuild` for the projection, `Producer.Resume(lastRecord.Sequence)` for the event numbering, and the snapshot for everything else. `Registry.Recover` takes run IDs and revives each one; the daemon supplies the IDs from `eventlog.RunIDs`, because the registry does not know where logs live and should not learn.

**A run is snapshotted the moment it starts.** This is the part that is easy to leave out and does not survive contact with a real daemon — see below.

**Lifecycle rides in the snapshot rather than in an event.** `event.TypeRunStateChanged` exists and would have been the obvious home, but the idempotency key for run-state events is `run:type:revision`, so two lifecycle changes at the same revision collapse into one. Making that work needs a new payload field and a change to how those events deduplicate, to record something that is projection metadata rather than a domain fact: pauses produce no events and replay does not need them. Pausing writes a snapshot for the same reason — a paused run produces nothing, so the next cadence snapshot never arrives, and without one a restart would find a run that still looked like it was running.

**A finished run stays finished.** Its final snapshot says so, and reviving it would restart a completed game.

## Two Things That Only Showed Up When Run

**The consumer cursor has to be moved with the projection.** A log opens with its reader where the group last *committed*, which trails the tail by up to a snapshot's worth of ticks. `Rebuild` folds that whole tail into the garden but reads through its own cursor, so a resumed run had a garden at tick 12 and a reader at tick 10 — and the next tick redelivered twelve records the garden already held, into a processor whose deduplication table a restart necessarily emptied. They applied twice. `Log.MarkFolded` moves the cursor to the tail without committing, and `TestARestartedRunIsTheSameRun` compares the resumed garden against one that never stopped, which is what caught it.

**A run interrupted before its first cadence snapshot could not resume at all.** The unit tests missed this and a real daemon found it in nine seconds: killed at tick 26 with a fifty-tick cadence, the run came back as `run has records but no snapshot`. With the default cadence that is the first ten seconds of every run — the window in which a crash is most likely, since a daemon that survives ten seconds usually survives longer. Starting a run now writes a snapshot at tick zero. It costs one small write per run and makes a run self-describing from its first moment.

The general lesson is worth the sentence: a cadence-based mechanism has a first interval during which it has not happened yet, and that interval is not a corner case just because it is short.

## Consequences

- `Run.resumed` is on the wire. A client cannot tell otherwise: the garden and the tick counter carry on, but the determinism chain does not — a resumed run did not fold the records below its snapshot, so it starts a fresh chain. A client comparing chains needs to be told.
- Processor counters carry across a restart, so a resumed run reports what it has done rather than what it has done since restarting. The deduplication table does not carry, and cannot: it is keyed on every event the run ever applied. That is safe for the same reason `Rebuild` folding a tail is safe — a redelivery is appended next to the record it repeats, so no duplicate pair straddles a restart.
- A run that was paused comes back paused. Whether to resume it is the player's call.
- One failed recovery does not stop the others, and does not stop the daemon. Nine runs back out of ten is better than none, and the tenth is reported rather than swallowed.
- Snapshots from before this change are refused, not guessed at. The records are still there, and `-replay` folds them.

## What Would Revisit This

- Replay wanting to reproduce pauses, which would make lifecycle a domain event after all and require sorting out the idempotency key.
- A snapshot growing large enough that writing one per run start is not free, which would be a reason to write a smaller header record rather than to go back to waiting for the cadence.
