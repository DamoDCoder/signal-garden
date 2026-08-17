# 0005: One log per run, owned by the run's goroutine

- **Date:** 2026-08-17
- **Status:** Accepted

## Context

[0004](0004-event-spine-replaces-kafka-as-the-event-backbone.md) adopts the event spine's log as the event transport. The log and its readers **are not safe for concurrent use**, and this is deliberate on the spine's part: it takes no locks, because a run being a function of its inputs is the property it exists to protect, and lock ordering under contention is not. Its adoption guide says to own the log from a single goroutine and route everything else through whatever queue the program already has.

Signal Garden has goroutines and select loops. `Registry` holds many runs; each `liveRun` has a `loop` goroutine; gRPC handlers, the CLI, and future WebSocket subscribers all call in from elsewhere. So the question is which goroutine owns a log, and how many logs there are.

## Options Considered

1. **One daemon-wide log, owned by a new writer goroutine.** All runs interleave into one history. Needs a command channel, a new goroutine, and a shutdown ordering invented for it. Records from concurrent runs interleave, so a per-run reader has to filter.
2. **One log per run, owned by that run's existing `loop` goroutine.** No new goroutine, no new queue. A run's history is a directory, which is also what replay wants.
3. **One log per run behind a mutex.** Smallest diff. Reintroduces exactly the thing the spine removed on purpose.

## Decision

Option 2. Each run owns a log at `<data>/runs/<runID>/`, opened when the run starts and closed when it finishes. The log lives inside `sim.Sim`, which `liveRun` already owns exclusively.

## Evidence

The ownership rule is already satisfied by code that exists. `liveRun.loop` is a single `select` over ticks, commands, and quit; every external caller reaches run state by submitting a closure through `liveRun.do`, which runs it *on* the loop goroutine and waits. Put the log inside the `Sim` that `liveRun` holds and there is no path to it that is not already serialized. Option 1 would require building that machinery a second time, next to the machinery that already does it.

The same holds for the batch path without any extra work: `run.Execute` drives a `Sim` from one goroutine and returns.

Three obligations fall out, and they are the whole cost of this choice:

- **Nothing may touch the log outside `loop` or a `do` closure.** That includes the projection subscriber path, which sends the current snapshot from inside `do` today and must keep doing so.
- **The log's lifetime becomes the run's.** `liveRun.finish` and `Registry.Close` stop tickers and close subscriber channels today; they must now also `Close()` the log. A log that is never closed is a segment that is never sealed.
- **An append failure fails the run.** `advance` already routes a `Sim.Step` error into `r.failure` and finishes the run. Appending is a new source of that error, not a new path.

Option 3 was rejected on the spine's own reasoning rather than on performance: a mutex would make the log safe against concurrent use while leaving the *order* of concurrent appends unreproducible, which is the property replay depends on. If the ordering does not come from a queue that can be replayed, the run is no longer a function of its inputs.

## What Would Revisit This

- Cross-run queries — "what happened across every run last hour" — become a product requirement, which a directory per run answers badly and one interleaved history answers well.
- Run counts grow far enough that one directory and one set of open file handles per run becomes the constraint.
- A future transport needs to append to a run's log from outside the run's goroutine, which would mean the seam is drawn at the wrong level.
