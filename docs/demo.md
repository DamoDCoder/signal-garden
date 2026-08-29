# Demo Script

Five minutes, documented commands only — M4's own feedback demo criterion: *a fresh checkout
reaches a live run using documented commands.* Two repos, narrated as one walkthrough the way
`docs/local-development.md` already does when it talks about the sibling checkout.

## Setup (once)

```sh
git clone https://github.com/DamoDCoder/signal-garden
git clone https://github.com/DamoDCoder/app.signal-garden

cd signal-garden && task docker:build && cd ..
cd app.signal-garden && nvm use && task setup && task up
```

Open `http://localhost:5173`. `task up` checks the daemon image it just built matches the client's
pinned contract before starting anything — if that warns, `git -C signal-garden tag` and
`cat app.signal-garden/CONTRACT` should read the same version.

## The Walkthrough

**Start a run.** Seed `42`, defaults otherwise, "Start run." The garden appears as a grid of glyphs
— size is growth stage, colour is health, the ring is moisture — and the hash underneath is the
determinism claim: run it again with the same seed and same control changes, get the same hash back,
checkable rather than asserted.

**Make it fall behind, on purpose.** In the Controls panel, drag `worker count` and `batch size`
down to `2` each, `events per tick` up to `20`, Apply. Capacity (4/tick) is now below production
(20/tick) — watch `pending` in the Pressure panel climb. This is the M3 capacity model: a consumer
genuinely falling behind, not a fault, and idempotent processing is what makes catching back up
safe. Raise `worker count`/`batch size` back up and watch it drain.

**Break a snapshot save, on purpose.** Drag `fail snapshot every` to `1`, Apply. Within a few
seconds — the default snapshot cadence is 50 ticks — `snapshot save retries` in the Pressure panel
increments. `snapshot save failures` stays at zero: the injected failure always recovers on retry,
which is the point being demonstrated (a transient failure, not "the run terminates," which already
happens today on any real disk error and isn't new).

**Drop the connection.** Hit "Drop connection" next to the connection status while the stream is
`live`. The client backs off through `reconnecting`, then resumes to `live` on its own — no button,
no manual reconnect — and "Missed while away" states the true size of the gap in records, not a
guess from how long the socket was down.

**Look at what's underneath, optionally.** `cd signal-garden && task load -- -workers 2 -batch 2
-rate 20 -duration 8s` from a third terminal drives the same capacity story over the daemon's real
gRPC API and prints a report — the same thing the sliders just did, scriptable. Or
`cd app.signal-garden && task observability:up` for Prometheus (`localhost:9091`) and Jaeger
(`localhost:16686`) — a `tick` trace per tick, an RPC trace per call, `signal_garden_pending_events`
graphable instead of read off a panel.

**Finish it.** "Finish" in the Controls panel. The scorecard shows how the garden ended, the event
counters, and the chain digest — the same three numbers `task run -- -seed 42`'s CLI scorecard
would print for the same seed run offline, because both drive the same simulation
(`docs/architecture.md`).

## What This Demonstrates

- **Determinism** is checkable, not asserted — the hash on screen, `task run -- -seed 42` matching
  it offline.
- **At-least-once delivery is survived, not prevented** — `duplicate_every` in the start form, the
  duplicate counter climbing while the hash doesn't move.
- **A consumer can genuinely fall behind, and recover** — `pending`, worker/batch capacity.
- **A transient failure recovers, not just "the run keeps going"** — `snapshot_save_retries`.
- **Reconnect is automatic and provably complete** — the catch-up handover, checked live.
- **The system explains itself** — `/metrics` and traces need nothing running to be true (`curl` is
  enough); a dashboard is one command away when a browsable one is worth more than `curl`.

See [docs/performance-report.md](performance-report.md) for what happens well past this demo's
scale.
