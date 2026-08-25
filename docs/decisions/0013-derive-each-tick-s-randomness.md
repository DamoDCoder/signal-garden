# 0013: Derive each tick's randomness instead of carrying a generator

- **Date:** 2026-08-25
- **Status:** Accepted

## Context

A garden is the fold of a history, so it restores from one: [0007](0007-never-compact-a-run-log.md) keeps every record and `sim.Rebuild` replays them. That is why `-replay` works, and why a restarted daemon could always reconstruct what a run had produced.

What it could not do is carry on producing. `producer.Producer` held a live `*rand.Rand`, and a producer is not a fold of anything — it is a *position in a seeded stream*. `math/rand` exposes no way to read that position out or put it back, so the state that decided the next event was the one piece of a run that could not be written down. `v0.4.0` restored projections but not runs, and the changelog said so plainly.

## Options Considered

1. **Serialise the generator's state.** `math/rand`'s v1 source is a 607-element lagged Fibonacci table with an internal index. Reaching it means reimplementing the algorithm, and the snapshot gains a blob that has to version alongside it forever.
2. **Carry a seekable source.** Replace `math/rand` with a generator whose state is small and public, and put it in the snapshot. Keeps existing streams intact; adds a snapshot field, a schema version bump, and a dependency or a hand-written PRNG.
3. **Derive per tick.** Seed a fresh stream from `(seed, tick)` at the start of every tick, so the producer's position is a number the run already knows.

## Decision

Option 3, using `math/rand/v2`'s PCG.

`Producer` holds no generator. `Tick` constructs `rand.New(rand.NewPCG(uint64(seed), uint64(tick)))` and draws from it, and that generator is discarded when the tick ends. There is no position to save because position is now `(seed, tick)`, and both are already in the snapshot.

PCG takes **two** seed words, which is exactly the shape of the problem — the run picks one and the tick picks the other. Folding a tick into a single seed would need a mixing step to stop adjacent ticks producing visibly related draws; taking two words means there is nothing to get wrong. It is also cheap to construct: two words of state rather than the several-hundred-element table `math/rand`'s v1 source seeds, which matters when the construction happens once per tick and M3's load lab exists to push tick rates.

One piece of producer state survives, and it is a count rather than a position: `seq`, the number of events emitted, which numbers event IDs. It does not need a snapshot field either — the last record in the run's log carries it.

## Consequences

**Every event stream changed.** This is not a compatible change to what a seed means. The seed-42 scorecard moved from `732dc9ba…` to `39ced9bd…`, and any fixture or screenshot naming an old hash is stale. Existing run logs on disk are unaffected: they are records, and replay folds records rather than reproducing them.

**Determinism is unchanged and better localised.** The same seed and the same control ticks still reach the same garden, and `TestEngineMatchesBatchRun` still pins the live path to the batch path. What is new is that a divergence can only come from the controls or the fold, never from how many draws a previous tick happened to make.

**The absorbing-state hash did not move**, which is [0008](0008-assert-determinism-on-a-chain-not-a-terminal-hash.md) being right in public. Seeds 42 and 43 still both end at `81636e1a…` after 400 pest-heavy ticks, because 20 dead organisms at zero moisture and zero stage hash the same however they got there. A terminal hash that survives a change to every event stream in the system is exactly the evidence that it was never testing what it appeared to.

**Two tests carry the property.** `TestAProducerCanBeReconstructedMidRun` builds a fresh producer, tells it the sequence it is at, and requires the next ten ticks to match a producer that ran continuously — the thing the old design could not do at all. `TestTicksDoNotShareAStream` guards the mistake this design invites: seeding from `seed + tick` would compile, pass a determinism test, and quietly correlate adjacent ticks.

## What Would Revisit This

- A run needing per-organism or per-subsystem streams, which would want a third seed word rather than a different scheme.
- `math/rand/v2` changing PCG's output, which would break replay of stored *scorecards* though not of stored logs. The algorithm is specified rather than "whatever the implementation does", which is why it was chosen over v1's source.
