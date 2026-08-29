# 0008: Assert determinism on a chain, not a terminal garden hash

- **Date:** 2026-08-17
- **Status:** Accepted

## Context

`domain.Garden.Hash()` is a SHA-256 over every organism's moisture, health, and stage. [events.md](../events.md) required that replaying the same fixture with the same rules version produce the same snapshot hash, and the M0 and M1 replay tests compare exactly that — including `TestEngineMatchesBatchRun`, which pins the live path to the batch path.

The event spine's `core.Chain` folds the event *and* the resulting projection digest at every step, rather than looking only at where the run ended up.

## Options Considered

1. **Keep the terminal hash.** No new dependency in the domain package, and the tests already pass.
2. **Fold a chain per event and assert on its digest.** `domain.Garden` gains a `Digest`, and determinism tests compare chains.
3. **Compare full event histories.** Maximally strict and unreadable on failure; a diff of thousands of records to find one reordered pair.

## Decision

Option 2. `Hash()` stays as the projection's fingerprint on the wire — it is what the snapshot frame carries and what a client can compare cheaply. Determinism *tests* assert on a `core.Chain` digest, and a run whose projection has stopped responding is failed rather than passed.

## Evidence

This is not a theoretical improvement. It was measured on this project's garden, and it is the finding that paid for the spine's M0 spike.

A projection that reaches an **absorbing state** folds every history to the same place. Once every organism is dead, rain changes no moisture and pest reduces no health, so two runs that genuinely diverged earlier agree on the terminal hash. Across 40 runs of one identical live scenario: **7 distinct final hashes while the garden was still alive, and 1 hash across all 40** once the run was long enough to kill everything — with the control change still landing on 12 different ticks. The longer, more thorough-looking test was the one that proved nothing.

Signal Garden is unusually exposed to this. `Organism.Alive()` is documented as absorbing by design — a dead organism accepts rain and pest events without effect, which keeps event counts honest — and the obvious way to make a determinism test more convincing is to run it for more ticks. That change makes the test weaker, and nothing about it looks wrong.

Both halves of the chain are load-bearing. Folding only the events would miss a projection that applies an event incorrectly; folding only the digests would miss two different events that happen to land on the same state, which is the absorbing case again.

`Chain.Absorbed(window)` is why the second half of the decision exists: agreement between two absorbed runs is evidence about the absorbing state, not about determinism, so the determinism gate must fail an absorbed run rather than count it. A determinism test that cannot fail is not a test.

**Reproduced here.** `TestTerminalHashAgreesWhereTheChainDoesNot` runs seeds 42 and 43 under a pest-only mix for 400 ticks. Every organism dies with zero moisture and zero stage either way, so both land on snapshot `81636e1a…` — and their chains differ, because their histories did. The test asserts all three facts, so if the garden ever gains a way to remember its history after death, it fails and says so rather than quietly becoming redundant.

Two implementation notes that the plan for this record got wrong:

- **`Garden` does not implement `core.Projection`.** `Apply(event.Event) Outcome` already exists and means something different from `Apply(core.Event) error`, and one type cannot have both. Only `Chain` is needed, and it needs a digest rather than an interface — so `Garden` grew `Digest`, `Hash` became its hex form, and there is still one hash implementation.
- **The chain folds per record, not per tick.** A tick is not a unit the ordering guarantee is about: two runs that applied the same events in a different order within one tick would agree at every tick boundary.

## What Would Revisit This

- The garden rules lose their absorbing states entirely, at which point the terminal hash and the chain carry the same information — though the chain would still localise a divergence to a step, and the hash would not.
- Chain digests become expensive enough at M3's event volumes to matter, which would be a reason to sample rather than to go back to the terminal hash.

## Measured At M3

The bullet above got a real number rather than staying a guess, using `task load` (which didn't
exist when this record was written) against `signal_garden_tick_duration_seconds` (same). `fold()`
calls `Garden.Digest()` once per event — a SHA-256 over every organism's state, `internal/domain/garden.go:151-159`
— so the cost this bullet worried about is `organisms × events_per_tick` per tick, not per run.

Seven scenarios, one fresh daemon each (metrics are process-lifetime and unlabeled by run — 0016 —
so a shared process would let one scenario's numbers bleed into the next), `-workers 0 -batch 0` so
the full production rate folds every tick rather than being throttled by the capacity model, `-duration 8s`,
default 200ms tick interval. p50/p95/p99 are interpolated from the Prometheus histogram's cumulative
buckets, which are coarse (`ExponentialBuckets(0.0005, 2, 16)`) — enough to place a number relative
to the 200ms tick budget, not a precise one:

| Scenario  | organisms | rate/tick | mean   | p50    | p95     | p99     |
| --------- | --------: | --------: | -----: | -----: | ------: | ------: |
| baseline  |        20 |         6 |  5.0ms |  6.0ms |   7.8ms |   8.0ms |
| demo-lo   |        20 |        20 |  5.4ms |  6.0ms |   7.8ms |   8.0ms |
| demo-hi   |        20 |       200 |  8.2ms |  7.6ms |  15.1ms |  15.8ms |
| mid-lo    |       200 |        20 |  5.9ms |  6.1ms |   8.0ms |  14.4ms |
| mid-hi    |       200 |       200 | 15.5ms | 13.7ms |  29.3ms |  31.5ms |
| stress-lo |      2000 |        20 | 16.6ms | 14.2ms |  30.8ms | 102.4ms |
| stress-hi |      2000 |       200 | 65.8ms | 69.8ms | 122.2ms | 126.8ms |

**Not a straight line in organisms alone.** `baseline` → `mid-lo` is a 10× organism increase at the
same rate and barely moves p50 (6.0ms → 6.1ms); `mid-lo` → `stress-lo` is another 10× and roughly
doubles it. Something else — almost certainly the one-fsync-per-tick `Log.Append` does regardless of
batch size (`internal/sim/sim.go`'s `Step()` doc comment) — sets a floor around 5–6ms that digest
cost doesn't clear until organism count is already in the hundreds. Where it does show plainly is
holding organisms fixed and raising the rate: `demo-lo` → `demo-hi`, `mid-lo` → `mid-hi`, and
`stress-lo` → `stress-hi` all grow with event rate, and the *size* of that growth itself grows with
organism count (roughly +2ms, +8ms, +56ms in mean respectively for the same 10× rate increase) — the
multiplicative `organisms × events_per_tick` shape the bullet predicted, just masked by fixed
per-tick overhead below a few hundred organisms.

**Verdict: measured, not currently a bottleneck, explicitly documented rather than fixed.** No
scenario's p95 reaches the 200ms tick budget — the worst, `stress-hi` at 2000 organisms and 200
events/tick simultaneously, sits at 122ms (61% of budget) at p95 and 127ms at p99. That is a real
garden two orders of magnitude past anything this project's own demo framing asks for ("garden
interactions worth watching for five minutes," a handful of organisms). Sampling stays unwritten.

**Revisit threshold:** a real (not load-test) scenario needing organism counts in the hundreds *and*
event rates in the hundreds simultaneously — `stress-hi`'s regime, 2000 × 200 — where digest cost
starts eating a majority of the tick budget rather than a fixed I/O floor dominating. Below that,
this isn't worth the complexity sampling would add. Full methodology and the scrape data are in
[docs/performance-report.md](../performance-report.md).
