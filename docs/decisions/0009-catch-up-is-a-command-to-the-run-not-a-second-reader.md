# 0009: Catch-up is a command to the run, not a second reader

- **Date:** 2026-08-22
- **Status:** Accepted

## Context

A client that disconnects and comes back needs the records it missed. Those records are in the run's log, and [0005](0005-one-log-per-run-owned-by-the-run-goroutine.md) gives that log to the run's own goroutine: the spine takes no locks, and its `Reader` is documented as unsafe for concurrent use alongside the log it reads.

The projection gateway lives on an HTTP goroutine, one per connection. So the obvious implementation — open a reader and walk it — is the one thing the ownership decision forbids.

There is a second problem underneath the first, and it survives even if the log were thread-safe. Catch-up has to join up with the live stream. The client needs records `[from, X)` and then a snapshot standing at exactly `X`, with live frames after it. Reading the log and then subscribing as two separate steps leaves the run free to tick in between, which produces either a gap or a repeat depending on which step wins.

## Options Considered

1. **A second reader on the gateway goroutine.** Direct, and unsound: it races the run's own reader and its appends.
2. **Open the run's directory again, read-only, from the gateway.** Sound against the ownership rule, since it is a different `Log` over the same files — but it reopens a log per reconnect, and it still cannot see the same instant the subscription attaches at.
3. **Make catch-up a command the run's goroutine executes.** The read, the frame, and the attach happen in one pass of the loop that owns everything.

## Decision

Option 3. `Registry.Resume` sends a command through the same `do` channel that `GetSnapshot` and `UpdateControls` use. Inside it, the run reads its own log with `Sim.Since`, takes its current snapshot, and registers the subscriber — all before the next tick can run, because the tick arrives on the same select.

`eventlog.Log.Since` uses its own cursor and never moves the `projections` group. A client falling behind is not the projection falling behind, and a reconnect must not commit anything.

An offset past the tail is refused with `ErrOffsetOutOfRange` rather than answered with an empty catch-up. A client holding an offset this run never wrote is confused about which run it is watching — most likely a stale tab against a restarted daemon — and an empty frame would let it carry on believing it had missed nothing.

## Consequences

Catch-up costs the run a pause proportional to how far behind the client is, on the goroutine that also runs the simulation. At M2's volumes this is a slice copy of records already in the page cache. It is not free forever: a client resuming from offset zero on a long run reads the whole history inline, and M3's load lab is where that becomes measurable. The fix, when it is needed, is to serve old records from a reopened read-only log and only take the run's goroutine for the tail — which is option 2 and option 3 together, and worth doing when there is a number to point at rather than now.

The invariant that pays for this is `catchup.to == folded_offset` of the frame that follows. `TestResumeHandsOverWithoutAGapOrARepeat` asserts the arithmetic, and `TestStreamResumeFromZeroRebuildsTheGarden` gives it teeth by folding the catch-up records into an empty garden and comparing the hash against the snapshot that follows them: a dropped, duplicated, or reordered record fails there rather than becoming a browser that quietly renders the wrong garden.

## What Would Revisit This

- A resume from a deep offset showing up as tick jitter under M3's load generator, which is the measurement that justifies splitting the read in two.
- The spine gaining a concurrent reader, which would make option 2 free and leave only the atomicity argument — still enough to keep the attach inside the run's goroutine, but not the read.
