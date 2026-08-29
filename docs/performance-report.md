# Performance Report

One bottleneck, measured rather than guessed at — the M3 exit criterion `task load` and
`signal_garden_tick_duration_seconds` were built to satisfy. Full writeup, with the rationale for
what was and wasn't measured, is [0008](decisions/0008-assert-determinism-on-a-chain-not-a-terminal-hash.md#measured-at-m3).
This document is the short version plus how to reproduce it.

## What Was Measured

`Garden.Digest()` — a SHA-256 over every organism's state, called once per event from `Sim.fold()`
— was flagged at M0 as a cost that scales with `organisms × events_per_tick` and might matter "at
M3's event volumes." It never got a number until now.

## Method

```sh
task serve      # fresh process per scenario — metrics are process-lifetime, unlabeled by run
task load -- -organisms N -rate M -workers 0 -batch 0 -duration 8s
curl localhost:8080/metrics | grep tick_duration_seconds
```

`-workers 0 -batch 0` keeps the capacity model unbounded, so every event a tick produces folds that
same tick — the worst case, not the throttled one. p50/p95/p99 are interpolated from the Prometheus
histogram's cumulative buckets (`ExponentialBuckets(0.0005, 2, 16)` — coarse, but enough to place a
number against the 200ms tick budget).

## Results

| Scenario  | organisms | rate/tick | mean   | p50    | p95     | p99     |
| --------- | --------: | --------: | -----: | -----: | ------: | ------: |
| baseline  |        20 |         6 |  5.0ms |  6.0ms |   7.8ms |   8.0ms |
| demo-lo   |        20 |        20 |  5.4ms |  6.0ms |   7.8ms |   8.0ms |
| demo-hi   |        20 |       200 |  8.2ms |  7.6ms |  15.1ms |  15.8ms |
| mid-lo    |       200 |        20 |  5.9ms |  6.1ms |   8.0ms |  14.4ms |
| mid-hi    |       200 |       200 | 15.5ms | 13.7ms |  29.3ms |  31.5ms |
| stress-lo |      2000 |        20 | 16.6ms | 14.2ms |  30.8ms | 102.4ms |
| stress-hi |      2000 |       200 | 65.8ms | 69.8ms | 122.2ms | 126.8ms |

## Verdict

**Not a bottleneck at demo scale.** `baseline` and `demo-lo` — the scale this project's own "watch
for five minutes" framing actually asks for — sit at single-digit milliseconds, under 4% of the tick
budget. The cost is real and grows the way `organisms × events_per_tick` predicts, but a fixed
per-tick floor (most likely `Log.Append`'s one fsync a tick) dominates until organism count reaches
the hundreds; only `stress-hi` — 2000 organisms, 200 events/tick, simultaneously, a garden far past
anything demoed here — spends a majority of its tick budget (61% at p95) on it. Explicitly
documented rather than fixed: see 0008 for the revisit threshold. Sampling the chain digest, the fix
0008 itself proposed if this day came, stays unwritten.

## Reproducing This

Same three commands as above, any `(organisms, rate)` pair. `docs/local-development.md`'s
Prometheus/traces section (and the client's `task observability:up`) gets you a browsable dashboard
instead of raw `curl` output, if you want to watch a scenario rather than just scrape its ending.
