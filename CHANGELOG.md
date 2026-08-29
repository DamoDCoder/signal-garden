# Changelog

All notable changes to this project are documented here.

Versions track the roadmap in [docs/roadmap.md](docs/roadmap.md): a minor version is a milestone's worth of work, and the project stays on `0.x` until M4 makes a first release meaningful.

## [Unreleased]

Nothing yet.

## [0.10.0] — 2026-08-29

Worker count and batch size are real controls. A consumer can genuinely fall behind now.

### Added

- **`Controls.worker_count` and `Controls.batch_size`** (new proto fields, `Controls` message).
  Together they cap how many log records one tick folds into the garden —
  `worker_count * batch_size` — instead of unconditionally draining everything a tick produced.
  Zero on either means unbounded, the behavior before this pair existed, so every run that doesn't
  set them is unaffected. This is a capacity model, not a worker pool: nothing in the event-
  application path is CPU-bound enough to benefit from real goroutines, and the determinism chain
  requires strict sequence ordering regardless. See
  [0017](docs/decisions/0017-worker-count-and-batch-size-are-a-capacity-model-not-goroutines.md).
- **`signal_garden_pending_events`** (Prometheus gauge) — `TelemetrySnapshot.pending` was always
  zero before this slice; now that a capacity below the production rate genuinely builds a backlog,
  this closes the `lag` item M3's metrics slice deferred. Summed across every run this process is
  serving via a private per-run map, not a plain last-writer-wins gauge — a quiet run ticking after
  a backlogged one must not erase the backlog's visibility. See 0016's amended revisit note.
- `eventlog.Log.UnprocessedUpTo(n)` — the capacity-bounded read `Sim.Step()` now uses;
  `Unprocessed()` is unchanged, a thin wrapper over `UnprocessedUpTo(0)`.

### Changed

- `Controls.Validate()` rejects negative `worker_count`/`batch_size` and bounds them
  (`MaxWorkerCount = 64`, `MaxBatchSize = MaxEventsPerTick`), same typo-guard rationale as the
  existing `MaxEventsPerTick`.

Client-side sliders for these two controls are not built yet — this slice is daemon and contract
only. See [docs/roadmap.md](docs/roadmap.md).

## [0.9.0] — 2026-08-29

M3's metrics foundation: a `GET /metrics` Prometheus scrape target.

### Added

- **`signal_garden_tick_duration_seconds`** (histogram) — wall-clock time to advance one run one
  tick, observed around `Sim.Step()`.
- **`signal_garden_rpc_duration_seconds`** (histogram, `method`/`code`) — every unary gRPC call,
  which covers REST traffic too since the gateway dials gRPC over loopback rather than calling
  in-process.
- **`signal_garden_events_processed_total`** (counter, `outcome`) — one increment per event the
  processor disposes of: applied, no_effect, duplicate, rejected, unknown_entity.
- **`signal_garden_snapshots_dropped_total`** (counter) — projection frames dropped for a full
  subscriber channel.
- **`signal_garden_last_publish_timestamp_seconds`** (gauge) — set on every frame sent to a
  subscriber; `time() - this` in PromQL is WebSocket freshness.
- Standard Go and process collectors, registered alongside.

None of the above carry a `run_id` label — deliberately, to keep Prometheus cardinality bounded.
Per-run detail stays on the existing `GetTelemetry` poll. See
[0016](docs/decisions/0016-prometheus-metrics-carry-no-run-id-label.md).

`lag`, `retries`, OpenTelemetry traces, and the in-app performance view are still open — tracked in
[docs/roadmap.md](docs/roadmap.md)'s M3 section.

## [0.8.1] — 2026-08-28

A daemon restart told every open browser the run it interrupted had finished.

### Fixed

- **Shutdown no longer lies about why a stream closed.** `Registry.Close` and a run genuinely finishing both close a subscription's channel the same way, and the gateway could not tell them apart, so both got `CloseNormalClosure` / "run finished" — including a run mid-tick that came right back on restart. `Subscription` now carries a `SubscriptionClosedReason` set before the channel closes; shutdown gets `CloseGoingAway` (1001), the standard code for "the server is leaving, not the resource." A client reading only the close code — the one thing a browser's `CloseEvent` reliably exposes — now gets an honest answer. `TestStreamClosesGoingAwayWhenTheRegistryShutsDown` is the regression. Found live-verifying app.signal-garden's M2 reconnect demo against a real `docker compose stop`, not a synthetic drop.

## [0.8.0] — 2026-08-28

A restarted daemon's recovered runs were invisible from a browser — watching one meant already knowing its ID.

### Added

- **`ListRuns` over gRPC and REST.** `GET /v1/runs` returns every run the registry currently holds open, started here or recovered on startup. `Registry.ListRuns` has existed since M2's recovery work but was reachable only from a test; this wires it to `GardenService` and the REST gateway, which is the half a client actually needed.

## [0.7.1] — 2026-08-25

The daemon ships as a container image, so the client's stack has something to run.

### Added

- **The daemon ships its own container image.** `Dockerfile` assembles `signalgardend` onto Alpine, running as a non-root user with run history in a mounted volume and a `readyz` health check. `task docker:build` builds it, tagging both the version and `:local` and stamping the version into an image label. It is built locally and never pushed. The client repository's compose file names the image rather than building it: an artifact belongs to the repository that knows how to make it, and a *stack* belongs to the one that has the dependency. See [0015](docs/decisions/0015-ship-an-image-but-not-a-stack.md).
- **`task build:docker` cross-compiles for the container platform**, into `bin/<os>_<arch>/` rather than into `bin/`. A developer machine builds for two platforms that are not interchangeable — `darwin/arm64` for the tests and the CLI, `linux/arm64` for the container — and a shared output path would mean whichever build ran last decided whether `docker run` worked, with `exec format error` as the only clue. `CGO_ENABLED=0` for the same class of reason: a binary dynamically linked against the host's libc starts here and fails on musl. `ARCH=amd64` builds for an x86 machine.
- `task docker:run` and `task docker:images`, for exercising the image without a compose stack.

## [0.7.0] — 2026-08-25

A run outlives the process that was serving it.

### Added

- **A restarted daemon resumes its runs.** `sim.Resume` rebuilds a whole simulation rather than a garden, `Registry.Recover` revives runs by ID, and `signalgardend` finds the IDs with `eventlog.RunIDs` before it starts serving. A run interrupted at tick 26 comes back at tick 26 and reaches the garden a run that never stopped would have reached. See [0014](docs/decisions/0014-a-restarted-daemon-resumes-its-runs.md).
- `SnapshotSchemaVersion` 2: the snapshot carries the run's lifecycle state and its operational parameters — `max_ticks`, `tick_interval`, `duplicate_every`. Records describe what a run produced and say nothing about what it was or what it was doing, so this is the only place a run's identity is written down. Snapshots from version 1 are refused rather than guessed at; the records are still there and `-replay` folds them.
- A snapshot at tick zero, written when a run starts. Without it a run interrupted before its first cadence snapshot had a log full of records and nothing to say what run they belonged to — the first ten seconds of every run at the default cadence, which is exactly when a crash is most likely.
- `Run.resumed` on the wire. The garden and the tick counter carry on across a restart; the determinism chain does not, because a resumed run did not fold the records below its snapshot.
- `eventlog.Log.MarkFolded`, `eventlog.Log.Last`, `eventlog.RunIDs`, and `processor.Processor.Restore`.

### Fixed

- A resumed run re-folded every record between the last commit and the tail. A log opens with its reader where the group last committed, `Rebuild` folds the whole tail through its own cursor, and the gap was redelivered into a processor whose deduplication table a restart had necessarily emptied — so those records applied twice and the garden diverged. `MarkFolded` moves the cursor with the projection, without committing.

### Changed

- **The producer derives each tick's randomness instead of carrying a generator.** `Tick` seeds a `math/rand/v2` PCG from `(seed, tick)` and discards it, so the producer's position is a number the run already knows rather than the internal state of a `*rand.Rand` that `math/rand` gave no way to read out. This is what `v0.4.0` said was missing before a live run could resume. See [0013](docs/decisions/0013-derive-each-tick-s-randomness.md).
- **Every event stream changed.** A seed no longer means what it meant: the seed-42 scorecard moved from `732dc9ba…` to `39ced9bd…`. Run logs already on disk are unaffected, because replay folds records rather than reproducing them. The absorbing-state hash in [0008](docs/decisions/0008-assert-determinism-on-a-chain-not-a-terminal-hash.md) did *not* move — 20 dead organisms hash the same however they got there, which is that decision being right in public.

- [0012](docs/decisions/0012-declare-the-js-type-of-every-64-bit-field.md) records what `protoc-gen-es` actually does with `jstype`, measured rather than assumed: `JS_STRING` is honoured and `JS_NUMBER` is not, so a 64-bit quantity arrives as `bigint`. Stripping every `JS_NUMBER` annotation produces byte-identical TypeScript. The rule still does its job — `run.seed` is a `string` a client passes around and `snapshot.tick` is a `bigint` it does arithmetic on — and `bigint` is the right answer, since `number` cannot hold an int64 losslessly.
- `docs/contracts.md` gains the generation recipe, including the two vendored `google/api` protos that have to be generated alongside the contract.

## [0.6.0] — 2026-08-25

One contract, served identically by both transports, ready for a client in another repository to generate from.

### Added

- `jstype` on all 26 of the contract's 64-bit fields, declaring what a generated client should see: `JS_NUMBER` for bounded quantities it does arithmetic on, `JS_STRING` for opaque tokens. `seed` is the only token. The wire is unchanged — 64-bit fields are JSON strings either way, per the protobuf JSON mapping — and so is the server: regenerating changed 100 lines of `garden.pb.go`, every one inside the embedded descriptor blob, with no Go type or struct tag touched. See [0012](docs/decisions/0012-declare-the-js-type-of-every-64-bit-field.md).
- `TestEvery64BitFieldDeclaresItsJSType`, which walks the compiled descriptor and fails any 64-bit field that declares no `jstype` or the wrong one. The rule is a test rather than a paragraph, so the next such field cannot be added without answering the question.
- Cross-origin support, controlled by `SIGNAL_GARDEN_CORS_ORIGIN` and defaulting to reflecting whatever origin asked. The browser client is a separate origin, and WebSockets do not preflight — so without this the stream works while every REST call fails, which reads as "the garden streams but no button works". Credentials are never allowed, which is what keeps a permissive origin from being a hole.
- `Event`, `EventPayload`, `Catchup`, `FrameType`, and `ProjectionFrame` in `garden.proto`, belonging to no rpc. The projection stream stays a read transport rather than a gRPC method while putting the same contract on the wire.
- `internal/wire`: the one translation from engine types to protobuf messages, shared by the gRPC service and the projection gateway. Two mappings would be two definitions of a garden, free to drift until a client noticed.

### Changed

- **The two transports now produce identical JSON.** Previously a garden reached a client as `{"runId": …, "tick": "45"}` over REST and `{"run_id": …, "tick": 45}` over the stream — different casing, and `tick` was a string from one and a number from the other. `snapshot.sequence` was `"0"` over REST and `undefined` over the stream at the moment a run started. See [0010](docs/decisions/0010-one-contract-for-both-transports.md).
- Every field in the contract carries an explicit `json_name`. The JSON these messages produce is the public shape, so it is written in the `.proto` rather than left to a marshaller option in Go. The wire is snake_case throughout, matching the envelope `docs/events.md` already documented.
- Decision 0003 is superseded by [0011](docs/decisions/0011-the-ui-is-a-separate-repository.md): the React client has its own repository, this one owns the contract, and the compose file lives with the client because the dependency runs that way round.
- 64-bit fields are JSON strings on both transports. That is the protobuf JSON mapping rather than a choice — JSON numbers lose precision above 2^53 — so the fix is uniformity: a client parses them once at the edge and never has to know which transport a value arrived on.
- `internal/projection` no longer defines its own `Frame` and `Catchup` Go types, and marshals with `EmitUnpopulated` to match grpc-gateway's default. `TestStreamAndGatewayAgreeOnTheWire` resolves the gateway's marshaller the way the running gateway does and compares the bytes; turning the option off fails it with `sequence` and every `moisture: 0` missing from the stream side.

- The Makefile is now [Taskfile.yml](Taskfile.yml), run with [go-task](https://taskfile.dev). Every target kept its name and its behaviour — `task check` is `make check` — and `task proto` regenerates byte-identical output from the same pinned plugins. The one gain worth noting is `--`: `task run -- -seed 42 -ticks 40` reaches the binary, where the Makefile needed a bare `go run` for anything non-default.
- `docs/local-development.md` no longer lists commands. `task --list` does, from each task's own description, so the list cannot drift from what actually runs. The doc says which commands are still missing and which milestone brings them.

## [0.5.0] — 2026-08-22

M2's last exit criterion: a client can drop, miss ticks, and come back without a gap. The projection stream it needed was M1's last outstanding transport, so M1 is down to the browser.

### Added

- `internal/projection`: the WebSocket projection stream at `GET /v1/runs/{run_id}/stream`, and reconnect catch-up at `?from=<offset>`. It is a read transport — client messages are discarded — and it carries no protobuf, because it is not a gRPC method. Rejections happen before the upgrade, so an unknown run is a 404 a fetch can read rather than a socket that opens and immediately closes.
- `engine.Resume` and `eventlog.Log.Since`. A reconnecting client's catch-up read, its first frame, and its attach all happen in one pass of the run's goroutine, which is what makes the handover exact: records `[from, X)`, then a snapshot standing at `X`, then live frames. Decision 0009 records why the read is a command to the run rather than a second reader, and what it will cost when a run is long enough to matter.
- `eventlog.ErrOffsetOutOfRange`. Resuming from an offset the log never wrote is refused rather than answered with an empty catch-up: a client holding another run's offset would otherwise carry on believing it had missed nothing.
- Log offsets on the wire. `GardenSnapshot.folded_offset` is the first record a garden has not folded; `TelemetrySnapshot.log_offset` and `committed_offset` are how many records the run holds and how far the projections group has durably folded. `TestOffsetsDescribeTheLog` pins the difference: a commit only happens alongside a snapshot, so `committed_offset` must sit still through ticks that write none, and a client watching it move every tick would be reading a promise the log has not made.
- `Sim.Offset`, `Sim.Folded`, and `Sim.Committed`. `Committed` returns the offset this `Sim` last committed rather than re-reading the group file, because telemetry is a read path with nowhere to put an I/O error; `New` seeds it from the log it was handed, so a reopened log reports where the previous process left off.

### Changed

- `github.com/gorilla/websocket` as a direct dependency. It is the first runtime dependency outside the spine and the gRPC toolchain.
- The stale Kafka comment in `Controls` is gone. Worker count, batch size, and retry policy are M3's, and the surrounding regeneration is what finally removed the last one of these in a hand-written file.

## [0.4.0] — 2026-08-17

M2's event backbone: the in-memory bus is gone, run history is durable, and a run can be rebuilt from its log in another process.

### Added

- `internal/event/codec.go`: `Event.ToCore` and `FromCore`, mapping the envelope onto the durable log record. PartitionKey, OccurredAt, and SchemaVersion become the record header; the rest becomes a JSON payload. `recorded_at` is not written at all, so two runs of the same seed produce byte-identical records — asserted directly rather than assumed.
- `internal/eventlog`: one append-only log per run, with the `projections` consumer group and `Sync` durability. Appending, reading, replaying, and committing are separate operations, and committing is deliberately not one of them yet — the garden is in memory until snapshots land, so a commit would let a restart resume past records it could no longer replay.

### Changed

- `internal/sim` appends a whole tick in one call and folds what the log hands back, instead of publishing to an in-memory queue and draining it. A tick costs one fsync regardless of how many events it produced. The seed-42 scorecard is byte-identical to the previous implementation's.
- `Sim` owns a log and must be `Close`d. `run.Execute` closes it on return; a live run closes it when its goroutine exits, which is after finishing rather than at it, because a finished run still answers telemetry that reads the log's offsets.
- Generated run IDs skip an ID whose log already holds records. The counter restarts at zero in a fresh process, so without this a restarted daemon proposes `run-0001` while last week's `run-0001` is still a directory. An explicitly requested ID is refused instead of renamed.
- Reopening a log whose commit points past the recovered tail no longer fails. A commit is synced whatever the durability mode says, so it can outlive the records it committed; the reader now starts at the beginning, which is the only position that cannot skip a record, and `Rewind` rewrites the commit.

- `eventlog.OpenDir` and `engine.DirectoryLogs`: run history on the real filesystem at `<data>/runs/<run_id>/`. `signalgardend` reads `SIGNAL_GARDEN_DATA_DIR` and `SIGNAL_GARDEN_ON_CORRUPT`; `signalgarden -live -data <dir>` does the same for the terminal client. Both default to keeping history in memory only for the CLI — the daemon defaults to `data`.
- `engine.CorruptPolicy`, whose zero value refuses. `eventlog.Rewind` implements the `continue` side: it pulls a commit back to the truncation point, repositions the reader, and refuses outright when a snapshot folded records the truncation removed.
- `engine.ErrRunHasHistory`, mapped to `ALREADY_EXISTS`, and `engine.ErrCorruptLog`, mapped to `DATA_LOSS`. A run ID is taken by history as well as by a live run.
- `domain.Garden.Digest` and a `core.Chain` folded once per record inside `Sim`, with `Chain`, `ChainSteps`, and `Absorbed` on the run scorecard and the CLI output. Determinism is asserted on the chain now; the snapshot hash stays as the projection's fingerprint on the wire.
- `sim.Fold`, which replays events into a fresh garden. It is what a restart does and what the replay command will do.
- Snapshots and commits. `Sim.Save` writes the projection state into the log and then commits the group to the same offset — snapshot first, because committing first would leave a window where a crash resumes past records with no state to resume from. Cadence is every 50 ticks and once more when a run finishes.
- `sim.Rebuild` and `signalgarden -replay -run <id> -data <dir>`: rebuild a run's garden from its log, in a different process, reaching the snapshot hash the live run ended on. Deleting every snapshot in the directory changes nothing but the time it takes.
- A crash matrix over every tick boundary, three crash shapes, and two durability modes. Sync mode must keep every acknowledged record and fold back to the exact garden the run was showing; batch mode may lose records but only from the tail. The matrix asserts it observed real loss, including a crash landing inside a tick, so the invariant is not being checked on whole ticks by accident.

### Removed

- `internal/bus`. The in-memory queue was the M0 seam and the log now occupies it. Tests reach a real log through the spine's in-memory filesystem, so there is one transport and the path under test is the path that ships.

### Not yet

- Resuming a **live** run after a restart. A garden is the fold of a history and restores from one; a producer is a position in a seeded `math/rand` stream, and there is no way to write that position down. Rebuilding a run's projection works and is what `-replay` does; making a restarted daemon carry on producing where it left off needs the producer to become replayable or seekable, which is its own slice.
- Reconnect catch-up from a client's last sequence, and the log's offsets on the wire. The WebSocket stream they belong to is still outstanding from M1.

## [0.3.0] — 2026-08-17

The M2 backbone decided, before any of it is built.

### Added

- `github.com/DamoDCoder/event-spine v0.2.0` as a dependency, ahead of the M2 work that uses it. Its surface is v0 and expected to move, so it is pinned rather than tracked. Nothing imports it yet.
- Decision records 0004 through 0008: the Event Spine log replaces Kafka as the M2 backbone, one log per run owned by that run's goroutine, the daemon refuses to start on corrupt recovery, run logs are never compacted, and determinism is asserted on a chain rather than a terminal garden hash.

### Changed

- M2 is no longer a Kafka milestone. The event transport becomes an in-process append-only log, which turns the durability properties the milestone exists to prove into unit tests against a crashable filesystem. `docs/architecture.md`, `docs/events.md`, `docs/roadmap.md`, `docs/local-development.md`, and `docs/feedback.md` were rewritten to match; Kafka moves to Later Extensions.
- M2 gains an exit criterion it could not have had under a broker: a run survives a simulated power cut at every tick boundary, in all three crash shapes, losing nothing the log acknowledged as durable.

## [0.2.0] — 2026-08-05

M1's server half: a live run, and a contract for talking to it.

### Added

- `internal/engine`: live run lifecycle — start, pause, resume, finish, control revisions applied on tick boundaries, and projection fan-out to subscribers. Its method shapes match the service definitions in `docs/contracts.md`, so the M1 gRPC service becomes an adapter rather than a second implementation.
- `internal/sim`: the per-tick simulation core, shared by the batch runner and the live engine so replay and live play cannot drift apart.
- `signalgarden -live`: paces a run on a clock, streams a frame per tick, and accepts typed control changes. The terminal stand-in for the M1 control surface.
- `proto/signal/garden/v1/garden.proto`: the versioned contract, with the seven methods and the exact REST routes from `docs/contracts.md`. Generated Go is committed under `internal/gen/`; the `proto` task regenerates it from plugins pinned in `go.mod`.
- `internal/service`: the gRPC adapter over the run engine. It holds no state and maps engine sentinel errors to status codes, so clients branch on codes rather than message text.
- `cmd/signalgardend`: serves gRPC on `:9090` and the generated REST gateway on `:8080`, with `/healthz`, `/readyz`, gRPC health, and reflection. The gateway dials the gRPC listener rather than calling in process, so the hop the architecture describes is a real one.

### Changed

- `run.Execute` now drives `internal/sim` instead of owning the tick loop. Behavior and determinism are unchanged; the M0 test suite passes untouched.

### Not yet

- WebSocket projection stream, React control surface, and Docker Compose. M1 is not complete.

## [0.1.0] — 2026-08-04

M0: the event path and the simulation rules, with no external dependencies.

### Added

- Project pack imported from the ideas cradle: roadmap, architecture, contracts, event model, local development, and feedback plan.
- `internal/domain`: the garden, its organisms, the rain, growth, and pest rules, and control validation.
- `internal/event`: the transport-neutral envelope, with simulation time separated from wall-clock time and a per-type idempotency key.
- `internal/bus`, `internal/producer`, `internal/processor`: an ordered in-memory queue, a seeded deterministic producer, and an idempotent processor that owns the garden projection.
- `internal/run` and `signalgarden`: a batch orchestrator that runs a garden to completion from a seed, and a CLI that prints its scorecard with event counters.

### Changed

- M0 no longer requires protobuf or gRPC. A single process has no service boundary to separate, so M0 defines its seams as Go interfaces and M1 introduces the wire contracts when a second process makes them load-bearing.
- M0 projection is a CLI rather than a React screen, which keeps the milestone free of the Node toolchain.

## Commit Guidance

Use a short imperative subject with a small scope:

```text
<type>(<scope>): <imperative summary>
```

Useful types are `docs`, `feat`, `test`, `perf`, `refactor`, `chore`, and `fix`. Keep each commit focused on one coherent batch. Add a body when the decision or tradeoff would be difficult to infer from the subject.
