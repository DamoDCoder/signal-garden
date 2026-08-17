# 0008: Assert determinism on a chain, not a terminal garden hash

- **Date:** 2026-08-17
- **Status:** Accepted

## Context

`domain.Garden.Hash()` is a SHA-256 over every organism's moisture, health, and stage. [events.md](../events.md) required that replaying the same fixture with the same rules version produce the same snapshot hash, and the M0 and M1 replay tests compare exactly that — including `TestEngineMatchesBatchRun`, which pins the live path to the batch path.

The event spine's `core.Chain` folds the event *and* the resulting projection digest at every step, rather than looking only at where the run ended up.

## Options Considered

1. **Keep the terminal hash.** No new dependency in the domain package, and the tests already pass.
2. **Fold a chain per event and assert on its digest.** `domain.Garden` implements `core.Projection` (`Apply`, `Digest`), and determinism tests compare chains.
3. **Compare full event histories.** Maximally strict and unreadable on failure; a diff of thousands of records to find one reordered pair.

## Decision

Option 2. `Hash()` stays as the projection's fingerprint on the wire — it is what the snapshot frame carries and what a client can compare cheaply. Determinism *tests* assert on a `core.Chain` digest, and a run whose projection has stopped responding is failed rather than passed.

## Evidence

This is not a theoretical improvement. It was measured on this project's garden, and it is the finding that paid for the spine's M0 spike.

A projection that reaches an **absorbing state** folds every history to the same place. Once every organism is dead, rain changes no moisture and pest reduces no health, so two runs that genuinely diverged earlier agree on the terminal hash. Across 40 runs of one identical live scenario: **7 distinct final hashes while the garden was still alive, and 1 hash across all 40** once the run was long enough to kill everything — with the control change still landing on 12 different ticks. The longer, more thorough-looking test was the one that proved nothing.

Signal Garden is unusually exposed to this. `Organism.Alive()` is documented as absorbing by design — a dead organism accepts rain and pest events without effect, which keeps event counts honest — and the obvious way to make a determinism test more convincing is to run it for more ticks. That change makes the test weaker, and nothing about it looks wrong.

Both halves of the chain are load-bearing. Folding only the events would miss a projection that applies an event incorrectly; folding only the digests would miss two different events that happen to land on the same state, which is the absorbing case again.

`Chain.Absorbed(window)` is why the second half of the decision exists: agreement between two absorbed runs is evidence about the absorbing state, not about determinism, so the determinism gate must fail an absorbed run rather than count it. A determinism test that cannot fail is not a test.

## What Would Revisit This

- The garden rules lose their absorbing states entirely, at which point the terminal hash and the chain carry the same information — though the chain would still localise a divergence to a step, and the hash would not.
- Chain digests become expensive enough at M3's event volumes to matter, which would be a reason to sample rather than to go back to the terminal hash.
