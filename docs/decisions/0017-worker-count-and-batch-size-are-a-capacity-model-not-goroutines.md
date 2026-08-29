# 0017: Worker count and batch size are a capacity model, not goroutines

- **Date:** 2026-08-29
- **Status:** Accepted

## Context

`Controls` has carried a comment since M0 that worker count and batch size "are meaningless while
the projection folds every record inside the tick that appended it" and arrive "when the consumer
can actually fall behind." M3's roadmap asks for exactly that: `docs/roadmap.md`'s feedback demo is
"compare worker count and batch size under a controlled event burst," and `pending`
(`TelemetrySnapshot.pending`) has been zero since M2 for the same reason — nothing ever falls
behind.

"Worker count" reads as an invitation to spin up goroutines. Three things say otherwise:

- `domain.Garden.Apply()` — one event, one organism mutation — is a map lookup and 2-3 integer
  operations. No I/O, no allocation, sub-microsecond. There is no CPU-bound work in the event-
  application path to parallelize; thousands of events a tick is still sub-millisecond on one core.
- `Processor.Process()` mutates unguarded shared state per call (`applied` map, `stats` counters,
  and `Garden.Apply` itself mutates organism state in place). Concurrent calls would need a lock,
  which serializes the "parallel" work anyway — or partitioning by entity, which the determinism
  chain forecloses (next point).
- The determinism chain (`docs/decisions/0008`) folds `chain.Advance` per record in **sequence**
  order, not completion order. Parallel workers would still have to hand their results back to one
  goroutine to fold in order — the same single-threaded step, just moved later and now with
  synchronization overhead added.

`internal/run/run.go`'s own comment adds the constraint that decides it: "Concurrency is what M3
measures, and introducing it here would make the determinism guarantee depend on scheduling rather
than on the rules." The tick-driving loop — shared by the live engine and the batch runner, which
`TestEngineMatchesBatchRun` pins together — has to stay synchronous regardless of what this slice
does. And the project's own invariant (`docs/decisions/0002`, `0005`): one goroutine owns each run;
its log takes no locks.

## Options Considered

1. **A literal worker pool.** N goroutines drain a tick's events, results serialized back to one
   goroutine before folding, so ordering and the ownership invariant both hold. Sound, but buys
   nothing: there is no CPU-bound work to distribute (see above), so this adds synchronization
   machinery and race surface to a codebase that has none today, in exchange for zero throughput
   change.
2. **Artificial per-event latency**, to simulate a slower consumer. Would make `pending` nonzero,
   but it's fiction — the daemon would be pretending to be slow rather than measuring anything real,
   and a "bottleneck" manufactured this way isn't one M3's exit criteria ("at least one bottleneck
   is measured and improved or explicitly documented") can honestly claim.
3. **A capacity model.** `worker_count * batch_size` caps how many records one tick folds, via a
   new `Log.UnprocessedUpTo(n)` instead of the unconditional `Unprocessed()`. When capacity is below
   the production rate, a real backlog builds in the log — genuinely, through the existing
   `Log.Pending()` mechanism — and drains on later ticks once demand drops. No goroutines, no fake
   delay, no new race surface.

## Decision

Option 3. `worker_count` and `batch_size` model the capacity a real worker pool would have, without
building one, because nothing in the hot path benefits from actually building one. Zero on either
field means unbounded — today's behavior — so every existing test and every run that doesn't set
these fields is unaffected.

This is honest about what a demo needs: `pending` genuinely growing and draining is the observable
behavior the feedback demo and the M3 exit criteria ask for. Whether it comes from real threads or
a capacity number is invisible from the metrics, the telemetry poll, or the log — the mechanism
that produces the backlog doesn't change what the backlog means.

## Consequences

`Sim.Step()` reads `s.log.UnprocessedUpTo(s.controls.Capacity())` instead of
`s.log.Unprocessed()`. `Pending()` becomes genuinely nonzero for the first time since M2, closing
that half of `docs/decisions/0016`'s deferred item — `signal_garden_pending_events` is now backed by
real data. The daemon still has exactly the concurrency it had before this slice: one goroutine per
run, nothing shared across a tick's processing.

## What Would Revisit This

- Genuinely CPU-heavy per-event work arriving later — a costlier domain rule, or a chain digest that
  stops being cheap at higher organism counts — at which point there would finally be something a
  worker pool could parallelize, and this decision's "nothing to distribute" premise would no longer
  hold.
- `docs/decisions/0009`'s catch-up cost concern (a deep-offset resume reading the whole gap inline)
  reaching the point where it needs the same kind of capacity-bounded reading this slice built for
  the tick path — `UnprocessedUpTo` may end up serving both.
