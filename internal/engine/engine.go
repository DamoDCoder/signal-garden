// Package engine runs simulations live: ticks arrive from a clock, controls
// arrive while the run is going, and subscribers watch the garden change.
//
// The method set mirrors the service definitions in docs/contracts.md —
// StartRun, GetRun, UpdateControls, PauseRun, FinishRun, GetSnapshot,
// GetTelemetry — so the M1 gRPC service is an adapter over this package rather
// than a second implementation of run lifecycle. Subscribe has no gRPC
// counterpart on purpose: WebSocket projection is a separate transport, not a
// second command API.
//
// Each run is owned by one goroutine. Ticks and commands arrive on the same
// select, so every mutation is serialized without a lock around simulation
// state, and a control update accepted mid-tick still lands on a tick boundary.
package engine

import (
	"errors"
	"fmt"
	"sync"
	"time"

	spinesim "github.com/DamoDCoder/event-spine/sim"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/event"
	"github.com/damodbear/signal-garden/internal/eventlog"
	"github.com/damodbear/signal-garden/internal/metrics"
	"github.com/damodbear/signal-garden/internal/processor"
	"github.com/damodbear/signal-garden/internal/sim"
)

// DefaultSnapshotEvery is how many ticks a run puts between snapshots.
//
// It is a tradeoff with nothing clever in it: snapshotting more often means a
// restart folds fewer records, and means more writes. Fifty ticks is ten seconds
// at the default pace, which is short enough that a restart is not visibly slow
// and long enough that the writes are not the workload.
const DefaultSnapshotEvery = 50

// DefaultTickInterval paces a run slowly enough that a person can watch a
// garden change without the projection stream becoming a firehose.
const DefaultTickInterval = 200 * time.Millisecond

// defaultSubscriberBuffer is how many snapshots a slow subscriber may fall
// behind before the engine starts dropping them. Dropping is deliberate: a
// projection stream carries whole snapshots, so a late consumer wants the
// newest one, not a backlog of stale ones.
const defaultSubscriberBuffer = 16

// State is the run lifecycle state.
type State string

const (
	StateRunning  State = "running"
	StatePaused   State = "paused"
	StateFinished State = "finished"
)

// Errors returned by the registry. Callers classify on these rather than on
// message text, because the gRPC layer maps them to status codes.
var (
	ErrRunNotFound  = errors.New("run not found")
	ErrRunExists    = errors.New("run already exists")
	ErrRunFinished  = errors.New("run is finished")
	ErrRunClosed    = errors.New("run is closed")
	ErrRegistryDown = errors.New("registry is closed")

	// ErrRunHasHistory means a run was started against a log that already
	// holds records. Starting a fresh run into an existing history would
	// interleave two runs in one log, and the second one's replay would
	// reproduce neither.
	ErrRunHasHistory = errors.New("run log already holds records")

	// ErrCorruptLog means opening a run's log found bytes that were present
	// and wrong. Whether that stops the run is a policy — see
	// docs/decisions/0006.
	ErrCorruptLog = errors.New("run log is corrupt")
)

// CorruptPolicy decides what a corrupt log means.
//
// The zero value refuses, which is the safe direction: a projection built from
// a log the disk got wrong is one nobody can describe, and the contract has no
// field for "this garden may be wrong."
type CorruptPolicy int

const (
	// RefuseCorrupt fails rather than serving a run whose history the disk
	// returned incorrectly.
	RefuseCorrupt CorruptPolicy = iota

	// ContinueCorrupt starts anyway, records the damage on the run, and
	// pulls any commit back to the truncation point.
	ContinueCorrupt
)

func (p CorruptPolicy) String() string {
	if p == ContinueCorrupt {
		return "continue"
	}
	return "refuse"
}

// LogOpener creates the log for a run.
//
// It is injected so the engine never learns whether a run's history is on a
// disk or in memory: the daemon supplies a directory-backed opener, and tests
// and the batch runner get an in-memory one that leaves nothing behind.
type LogOpener func(runID string) (*eventlog.Log, eventlog.Recovery, error)

// EphemeralLogs returns an opener whose logs live in memory and vanish with the
// process. It is the default, so a Registry created without a data directory
// behaves exactly as it did before durability existed.
func EphemeralLogs() LogOpener {
	return func(string) (*eventlog.Log, eventlog.Recovery, error) {
		return eventlog.Open(spinesim.NewFS())
	}
}

// DirectoryLogs returns an opener rooted at a data directory, one directory per
// run.
func DirectoryLogs(root string) LogOpener {
	return func(runID string) (*eventlog.Log, eventlog.Recovery, error) {
		return eventlog.OpenDir(root, runID)
	}
}

// StartRunRequest describes a run to start.
type StartRunRequest struct {
	// RunID is optional; the registry generates one when it is empty.
	RunID     string
	Seed      int64
	Organisms int
	Controls  domain.Controls

	// TickInterval is the wall-clock pace of the run. Zero uses the
	// registry default. It affects only pacing: simulation time is the tick
	// counter, so two runs at different intervals reach the same garden.
	TickInterval time.Duration

	// MaxTicks finishes the run automatically once reached. Zero runs until
	// FinishRun is called.
	MaxTicks int64

	// DuplicateEvery republishes every Nth event of a tick, so the
	// idempotency demo is a control rather than a test-only code path.
	DuplicateEvery int

	// SnapshotEvery overrides the registry's snapshot cadence for this run.
	// Zero uses the registry default; a negative value is rejected.
	SnapshotEvery int64
}

// Validate checks the request before a run is created.
func (r StartRunRequest) Validate() error {
	if r.Organisms < 1 {
		return fmt.Errorf("organisms must be at least 1, got %d", r.Organisms)
	}
	if r.TickInterval < 0 {
		return fmt.Errorf("tick_interval must not be negative, got %s", r.TickInterval)
	}
	if r.MaxTicks < 0 {
		return fmt.Errorf("max_ticks must not be negative, got %d", r.MaxTicks)
	}
	if r.DuplicateEvery < 0 {
		return fmt.Errorf("duplicate_every must not be negative, got %d", r.DuplicateEvery)
	}
	if r.SnapshotEvery < 0 {
		return fmt.Errorf("snapshot_every must not be negative, got %d", r.SnapshotEvery)
	}
	return r.Controls.Validate()
}

// Run is a point-in-time view of run metadata. It is a copy: holding one never
// aliases engine state.
type Run struct {
	RunID        string          `json:"run_id"`
	Seed         int64           `json:"seed"`
	Organisms    int             `json:"organisms"`
	State        State           `json:"state"`
	Tick         int64           `json:"tick"`
	MaxTicks     int64           `json:"max_ticks,omitempty"`
	TickInterval time.Duration   `json:"tick_interval"`
	Controls     domain.Controls `json:"controls"`
	Revision     int             `json:"revision"`
	StartedAt    time.Time       `json:"started_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	FinishedAt   time.Time       `json:"finished_at,omitzero"`

	// Failure records why a run finished early. It is empty for runs that
	// finished normally.
	Failure string `json:"failure,omitempty"`

	// LogRecovery records damage found when the run's log was opened, for a
	// run allowed to start anyway under ContinueCorrupt. It is empty in the
	// normal case, and it is the only place a caller can learn that this
	// garden may not match any history.
	LogRecovery string `json:"log_recovery,omitempty"`

	// Resumed marks a run the daemon picked back up after a restart rather
	// than started. The garden and the tick counter carry on; the
	// determinism chain does not, because a resumed run did not fold the
	// records below the snapshot it restored from.
	Resumed bool `json:"resumed,omitempty"`
}

// ControlRevision is the receipt for an accepted control update.
type ControlRevision struct {
	RunID    string          `json:"run_id"`
	Revision int             `json:"revision"`
	Controls domain.Controls `json:"controls"`

	// EffectiveTick is the tick at which the producer starts obeying these
	// controls. Replay needs this number; wall-clock acceptance time is not
	// part of the simulation.
	EffectiveTick int64 `json:"effective_tick"`
}

// GardenSnapshot is one projection frame: what the garden looks like after a
// given tick.
type GardenSnapshot struct {
	RunID string `json:"run_id"`

	// Sequence orders frames within a run so a reconnecting client can tell
	// how far behind it was. It counts emitted frames, not ticks.
	Sequence   int64             `json:"sequence"`
	Tick       int64             `json:"tick"`
	Revision   int               `json:"revision"`
	State      State             `json:"state"`
	Stats      domain.Stats      `json:"stats"`
	Organisms  []domain.Organism `json:"organisms"`
	Hash       string            `json:"hash"`
	ObservedAt time.Time         `json:"observed_at"`

	// FoldedOffset is the offset of the first log record this garden has not
	// folded. Sequence orders frames; this names the history behind one, and
	// it is what a reconnecting client asks to resume from.
	FoldedOffset int64 `json:"folded_offset"`
}

// TelemetrySnapshot is what the performance panel reads. The counters here are
// the ones M3 turns into histograms; at M1 they are plain totals.
type TelemetrySnapshot struct {
	RunID        string          `json:"run_id"`
	State        State           `json:"state"`
	Tick         int64           `json:"tick"`
	Revision     int             `json:"revision"`
	TickInterval time.Duration   `json:"tick_interval"`
	Published    int             `json:"published"`
	Processor    processor.Stats `json:"processor"`

	// Pending is published-but-unprocessed events. It is zero while the
	// processor drains inside the tick, and becomes consumer lag at M2.
	Pending int `json:"pending"`

	Subscribers      int           `json:"subscribers"`
	SnapshotsSent    int64         `json:"snapshots_sent"`
	SnapshotsDropped int64         `json:"snapshots_dropped"`
	Uptime           time.Duration `json:"uptime"`
	ObservedAt       time.Time     `json:"observed_at"`

	// LogOffset is how many records the run's log holds. CommittedOffset is
	// how far the projections group has durably folded, so it moves at
	// snapshot cadence and trails LogOffset by design; the gap is what a
	// restart would redeliver.
	LogOffset       int64 `json:"log_offset"`
	CommittedOffset int64 `json:"committed_offset"`

	// SnapshotSaveRetries and SnapshotSaveFailures count attempts to write the
	// periodic on-disk snapshot — distinct from SnapshotsSent/SnapshotsDropped
	// above, which are WebSocket frames. See docs/decisions/0018.
	SnapshotSaveRetries  int64 `json:"snapshot_save_retries"`
	SnapshotSaveFailures int64 `json:"snapshot_save_failures"`
}

// RunSummary is the scorecard a finished run leaves behind.
type RunSummary struct {
	Run       Run               `json:"run"`
	Snapshot  GardenSnapshot    `json:"snapshot"`
	Telemetry TelemetrySnapshot `json:"telemetry"`
}

// SubscriptionClosedReason says why a subscription's channel closed. A
// transport needs this to report something more honest than "the run
// finished" — true for only one of the two reasons this ever happens, and
// false the other, misleadingly, this session's whole point.
type SubscriptionClosedReason int

const (
	// SubscriptionClosedRunFinished: the run reached its end. Terminal — a
	// client should not expect this run to come back.
	SubscriptionClosedRunFinished SubscriptionClosedReason = iota
	// SubscriptionClosedRegistryShutdown: the daemon is going away, not the
	// run. A restarted daemon resumes it, so a client should reconnect
	// rather than treat this the way it treats a finished run.
	SubscriptionClosedRegistryShutdown
)

// Subscription is a live projection stream for one run. The channel closes
// when the run finishes or the subscription is closed.
type Subscription struct {
	id   int
	ch   chan GardenSnapshot
	run  *liveRun
	once sync.Once

	// reason is written once, by the run's own goroutine, strictly before
	// ch is closed — so a receiver that observes the close via Snapshots()
	// is safe to read it without further synchronization, same as any other
	// close-as-broadcast pattern.
	reason SubscriptionClosedReason
}

// Snapshots returns the frame channel.
func (s *Subscription) Snapshots() <-chan GardenSnapshot { return s.ch }

// ClosedReason says why Snapshots() closed. Meaningful only after it has;
// undefined (zero value) while the subscription is still open.
func (s *Subscription) ClosedReason() SubscriptionClosedReason { return s.reason }

// Close detaches the subscriber. It is safe to call more than once, and safe
// to call after the run has finished.
func (s *Subscription) Close() {
	s.once.Do(func() {
		// A closed or finished run has already closed the channel, so a
		// failed command here means there is nothing left to detach.
		_ = s.run.do(func(r *liveRun) { r.removeSub(s.id) })
	})
}

// Registry owns every live run.
type Registry struct {
	mu       sync.Mutex
	clock    Clock
	interval time.Duration
	snapshot int64
	openLog  LogOpener
	corrupt  CorruptPolicy
	metrics  *metrics.Recorder
	runs     map[string]*liveRun
	order    []string
	nextID   int
	closed   bool
}

// Option configures a Registry.
type Option func(*Registry)

// WithClock replaces the time source. Tests pass a ManualClock so ticks are
// explicit rather than timed.
func WithClock(c Clock) Option {
	return func(r *Registry) { r.clock = c }
}

// WithTickInterval sets the default pace for runs that do not choose their own.
func WithTickInterval(d time.Duration) Option {
	return func(r *Registry) { r.interval = d }
}

// WithLogs replaces the log opener. The default keeps run history in memory.
func WithLogs(open LogOpener) Option {
	return func(r *Registry) { r.openLog = open }
}

// WithSnapshotEvery sets how many ticks runs put between snapshots. Zero turns
// snapshotting off, which means a restart folds a run's whole history.
func WithSnapshotEvery(ticks int64) Option {
	return func(r *Registry) { r.snapshot = ticks }
}

// WithCorruptPolicy chooses what happens when a run's log opens corrupt.
func WithCorruptPolicy(p CorruptPolicy) Option {
	return func(r *Registry) { r.corrupt = p }
}

// WithMetrics gives every run in this registry a Prometheus recorder. A
// Registry with none records nothing — every metrics call in this package is
// nil-receiver-safe.
func WithMetrics(m *metrics.Recorder) Option {
	return func(r *Registry) { r.metrics = m }
}

// NewRegistry returns an empty registry driven by the system clock.
func NewRegistry(opts ...Option) *Registry {
	reg := &Registry{
		clock:    SystemClock(),
		interval: DefaultTickInterval,
		snapshot: DefaultSnapshotEvery,
		openLog:  EphemeralLogs(),
		runs:     make(map[string]*liveRun),
	}
	for _, opt := range opts {
		opt(reg)
	}
	return reg
}

// StartRun creates a run and begins ticking it immediately.
func (g *Registry) StartRun(req StartRunRequest) (Run, error) {
	if err := req.Validate(); err != nil {
		return Run{}, fmt.Errorf("invalid start run request: %w", err)
	}
	if req.TickInterval == 0 {
		req.TickInterval = g.interval
	}
	if req.SnapshotEvery == 0 {
		req.SnapshotEvery = g.snapshot
	}

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return Run{}, ErrRegistryDown
	}
	generated := req.RunID == ""
	if !generated {
		if _, exists := g.runs[req.RunID]; exists {
			g.mu.Unlock()
			return Run{}, fmt.Errorf("%w: %s", ErrRunExists, req.RunID)
		}
	}

	// Opening the log is disk work under the registry lock. It is bounded
	// and infrequent — once per run start — and holding the lock is what
	// makes "this ID is free" still true by the time the log is open.
	runID, runLog, recovered, err := g.claimLogLocked(req.RunID, generated)
	if err != nil {
		g.mu.Unlock()
		return Run{}, err
	}
	req.RunID = runID

	s, err := sim.New(sim.Config{
		RunID:          req.RunID,
		Seed:           req.Seed,
		Organisms:      req.Organisms,
		Controls:       req.Controls,
		DuplicateEvery: req.DuplicateEvery,
		SnapshotEvery:  req.SnapshotEvery,
		MaxTicks:       req.MaxTicks,
		TickInterval:   req.TickInterval,
		Metrics:        g.metrics,
		Log:            runLog,
	})
	if err != nil {
		_ = runLog.Close()
		g.mu.Unlock()
		return Run{}, err
	}

	s.SetState(string(StateRunning))

	// Snapshot at tick zero, before the run has done anything.
	//
	// A snapshot is the only place a run's identity is written down — its
	// seed, its controls, its pace — because no record a producer emits
	// mentions any of them. Waiting for the cadence would leave every run
	// unrecoverable for its first fifty ticks: the log would hold records
	// and nothing would know what run they belonged to. This costs one small
	// write per run and makes a run self-describing from its first moment.
	if err := s.Save(); err != nil {
		_ = s.Close()
		g.mu.Unlock()
		return Run{}, fmt.Errorf("record run %s: %w", req.RunID, err)
	}

	now := g.clock.Now()
	lr := &liveRun{
		req:       req,
		sim:       s,
		clock:     g.clock,
		metrics:   g.metrics,
		recovered: recovered,
		state:     StateRunning,
		startedAt: now,
		updatedAt: now,
		subs:      make(map[int]*Subscription),
		cmds:      make(chan func(*liveRun)),
		quit:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	lr.ticker = g.clock.NewTicker(req.TickInterval)

	g.runs[req.RunID] = lr
	g.order = append(g.order, req.RunID)
	g.mu.Unlock()

	go lr.loop()
	return g.GetRun(req.RunID)
}

// Recover brings interrupted runs back, one per run ID it is given.
//
// A daemon that restarts has run directories on disk and no runs in memory. For
// each ID, this rebuilds the simulation from its log and starts it ticking
// again where it stopped — the same run, not a new one that happens to share a
// garden. Runs whose last snapshot says they finished are skipped: they ended
// on purpose and reviving them would restart a completed game.
//
// The caller supplies the IDs rather than the registry going looking, because
// the registry does not know where logs live — that is the LogOpener's business
// — and a caller that wants to recover a subset should be able to.
//
// A run that cannot be recovered does not stop the ones that can. Recovery
// returns the runs it revived and joins the failures, so a daemon can start
// with nine of ten runs back and say clearly what happened to the tenth.
func (g *Registry) Recover(runIDs []string) ([]Run, error) {
	var (
		revived []Run
		errs    []error
	)
	for _, id := range runIDs {
		run, ok, err := g.recoverOne(id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			revived = append(revived, run)
		}
	}
	return revived, errors.Join(errs...)
}

// recoverOne revives a single run. The bool is false for a run that was
// finished rather than interrupted, which is a skip and not a failure.
func (g *Registry) recoverOne(runID string) (Run, bool, error) {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return Run{}, false, ErrRegistryDown
	}
	if _, exists := g.runs[runID]; exists {
		g.mu.Unlock()
		return Run{}, false, fmt.Errorf("recover %s: %w", runID, ErrRunExists)
	}

	runLog, recovered, err := g.openHistory(runID)
	if err != nil {
		g.mu.Unlock()
		return Run{}, false, fmt.Errorf("recover %s: %w", runID, err)
	}

	s, snapshot, err := sim.Resume(runID, runLog, g.metrics)
	if err != nil {
		_ = runLog.Close()
		g.mu.Unlock()
		return Run{}, false, err
	}
	if State(snapshot.State) == StateFinished {
		// Finished on purpose. Closing the log here matters: a finished
		// run holds a directory, and leaving it open would keep a handle
		// on every completed run the daemon has ever served.
		_ = runLog.Close()
		g.mu.Unlock()
		return Run{}, false, nil
	}

	req := StartRunRequest{
		RunID:          runID,
		Seed:           snapshot.Seed,
		Organisms:      len(snapshot.Organisms),
		Controls:       snapshot.Controls,
		TickInterval:   snapshot.TickInterval,
		MaxTicks:       snapshot.MaxTicks,
		DuplicateEvery: snapshot.DuplicateEvery,
		SnapshotEvery:  g.snapshot,
	}
	if req.TickInterval <= 0 {
		req.TickInterval = g.interval
	}

	// A run that was paused when the daemon stopped comes back paused. It
	// is still the same run, and deciding to resume it is the player's.
	state := State(snapshot.State)
	if state != StatePaused {
		state = StateRunning
	}
	s.SetState(string(state))

	now := g.clock.Now()
	lr := &liveRun{
		req:       req,
		sim:       s,
		clock:     g.clock,
		metrics:   g.metrics,
		recovered: recovered,
		resumed:   true,
		state:     state,
		startedAt: now,
		updatedAt: now,
		subs:      make(map[int]*Subscription),
		cmds:      make(chan func(*liveRun)),
		quit:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	lr.ticker = g.clock.NewTicker(req.TickInterval)

	g.runs[runID] = lr
	g.order = append(g.order, runID)
	g.mu.Unlock()

	go lr.loop()

	run, err := g.GetRun(runID)
	return run, true, err
}

// GetRun returns current run metadata.
func (g *Registry) GetRun(runID string) (Run, error) {
	return query(g, runID, (*liveRun).view)
}

// GetSnapshot returns the current projection frame.
func (g *Registry) GetSnapshot(runID string) (GardenSnapshot, error) {
	return query(g, runID, (*liveRun).snapshot)
}

// GetTelemetry returns the current counters.
func (g *Registry) GetTelemetry(runID string) (TelemetrySnapshot, error) {
	return query(g, runID, (*liveRun).telemetry)
}

// UpdateControls validates and stages a control change, returning its revision.
// The change takes effect at the next tick, never partway through one.
func (g *Registry) UpdateControls(runID string, c domain.Controls) (ControlRevision, error) {
	lr, err := g.lookup(runID)
	if err != nil {
		return ControlRevision{}, err
	}
	var (
		rev    ControlRevision
		reject error
	)
	if err := lr.do(func(r *liveRun) {
		if r.state == StateFinished {
			reject = ErrRunFinished
			return
		}
		revision, err := r.sim.SetControls(c)
		if err != nil {
			reject = err
			return
		}
		r.updatedAt = r.clock.Now()
		rev = ControlRevision{
			RunID:         r.req.RunID,
			Revision:      revision,
			Controls:      c,
			EffectiveTick: r.sim.Tick(),
		}
	}); err != nil {
		return ControlRevision{}, err
	}
	return rev, reject
}

// PauseRun stops the run consuming ticks. Simulation state is untouched, so
// resuming continues from the same tick.
func (g *Registry) PauseRun(runID string) (Run, error) {
	return g.setPaused(runID, true)
}

// ResumeRun returns a paused run to running.
func (g *Registry) ResumeRun(runID string) (Run, error) {
	return g.setPaused(runID, false)
}

func (g *Registry) setPaused(runID string, paused bool) (Run, error) {
	lr, err := g.lookup(runID)
	if err != nil {
		return Run{}, err
	}
	var (
		out    Run
		reject error
	)
	if err := lr.do(func(r *liveRun) {
		if r.state == StateFinished {
			reject = ErrRunFinished
			return
		}
		if paused {
			r.state = StatePaused
		} else {
			r.state = StateRunning
		}
		r.sim.SetState(string(r.state))
		// A pause writes a snapshot rather than waiting for the cadence.
		// A paused run produces nothing, so the next scheduled snapshot
		// would never arrive, and a restart would find a run that still
		// looked like it was running. A failed write is not a reason to
		// refuse the pause — the run really is paused — but it must not
		// be silent, because it means a restart will resume it running.
		if err := r.sim.Save(); err != nil && r.failure == nil {
			r.failure = err
		}
		r.updatedAt = r.clock.Now()
		r.publish()
		out = r.view()
	}); err != nil {
		return Run{}, err
	}
	return out, reject
}

// FinishRun ends the run and returns its summary. Finishing an already finished
// run is not an error: it returns the same summary, so a retried request from
// the UI is harmless.
func (g *Registry) FinishRun(runID string) (RunSummary, error) {
	lr, err := g.lookup(runID)
	if err != nil {
		return RunSummary{}, err
	}
	var out RunSummary
	if err := lr.do(func(r *liveRun) {
		r.finish()
		out = RunSummary{Run: r.view(), Snapshot: r.snapshot(), Telemetry: r.telemetry()}
	}); err != nil {
		return RunSummary{}, err
	}
	return out, nil
}

// Subscribe attaches a projection stream and immediately delivers the current
// snapshot, so a new client renders a garden before the next tick rather than
// staring at an empty page. Pass buffer <= 0 for the default.
func (g *Registry) Subscribe(runID string, buffer int) (*Subscription, error) {
	sub, _, err := g.subscribe(runID, buffer, 0, false)
	return sub, err
}

// Resume attaches like Subscribe and also returns the records the client
// missed: everything the log holds from `from` up to the garden the first
// frame on the channel describes.
//
// The catch-up read, the frame, and the attach all happen in one pass of the
// run's goroutine, which is what makes the handover exact. A client that
// reconnects gets records [from, folded), then a snapshot at folded, then live
// frames after it — no gap to fill in and no record delivered twice.
//
// Reading the log from another goroutine is what decision 0005 forbids, and
// this is why the read is a command to the run rather than a second reader.
func (g *Registry) Resume(runID string, buffer int, from int64) (*Subscription, []event.Event, error) {
	return g.subscribe(runID, buffer, from, true)
}

func (g *Registry) subscribe(runID string, buffer int, from int64, catchup bool) (*Subscription, []event.Event, error) {
	lr, err := g.lookup(runID)
	if err != nil {
		return nil, nil, err
	}
	if buffer <= 0 {
		buffer = defaultSubscriberBuffer
	}
	sub := &Subscription{ch: make(chan GardenSnapshot, buffer), run: lr}
	var (
		missed []event.Event
		reject error
	)
	if err := lr.do(func(r *liveRun) {
		if catchup {
			if missed, reject = r.sim.Since(from); reject != nil {
				return
			}
		}
		sub.ch <- r.snapshot()
		r.snapshotsSent++
		r.metrics.ObservePublish()
		if r.state == StateFinished {
			// Nothing will ever feed this subscriber, so close the
			// stream now: a client that attaches late still gets the
			// final garden and a clean end, not a hang.
			close(sub.ch)
			return
		}
		r.nextSub++
		sub.id = r.nextSub
		r.subs[sub.id] = sub
	}); err != nil {
		return nil, nil, err
	}
	if reject != nil {
		return nil, nil, reject
	}
	return sub, missed, nil
}

// ListRuns returns metadata for every run in start order.
func (g *Registry) ListRuns() []Run {
	g.mu.Lock()
	live := make([]*liveRun, 0, len(g.order))
	for _, id := range g.order {
		if lr, ok := g.runs[id]; ok {
			live = append(live, lr)
		}
	}
	g.mu.Unlock()

	out := make([]Run, 0, len(live))
	for _, lr := range live {
		var v Run
		if err := lr.do(func(r *liveRun) { v = r.view() }); err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

// Close stops every run and releases their goroutines. The registry cannot be
// reused afterwards.
func (g *Registry) Close() error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil
	}
	g.closed = true
	live := make([]*liveRun, 0, len(g.runs))
	for _, lr := range g.runs {
		live = append(live, lr)
	}
	g.runs = make(map[string]*liveRun)
	g.order = nil
	g.mu.Unlock()

	for _, lr := range live {
		close(lr.quit)
		<-lr.done
	}
	return nil
}

// maxIDAttempts bounds the search for an unused generated run ID. Reaching it
// means the data directory holds that many consecutive runs, which is a real
// answer rather than a reason to keep spinning.
const maxIDAttempts = 1000

// claimLogLocked finds a run ID whose log is empty and opens it.
//
// Generated IDs are sequential and the counter starts at zero in a fresh
// process, so a restarted daemon proposes run-0001 again while last week's
// run-0001 is still a directory. An ID whose log already holds records is
// therefore skipped rather than failed: the collision is with history, not with
// a live run, and the caller asked for "a run" rather than for that name.
//
// A caller-supplied ID gets no such courtesy. It named this run, and quietly
// starting a differently-named one would be worse than refusing.
func (g *Registry) claimLogLocked(runID string, generated bool) (string, *eventlog.Log, eventlog.Recovery, error) {
	if !generated {
		l, recovered, err := g.openRunLog(runID)
		return runID, l, recovered, err
	}

	var lastErr error
	for range maxIDAttempts {
		id := g.generateIDLocked()
		l, recovered, err := g.openRunLog(id)
		if err == nil {
			return id, l, recovered, nil
		}
		if !errors.Is(err, ErrRunHasHistory) {
			return "", nil, recovered, err
		}
		lastErr = err
	}
	return "", nil, eventlog.Recovery{}, fmt.Errorf("no unused run id after %d attempts: %w", maxIDAttempts, lastErr)
}

// openRunLog opens a run's history and decides whether it is fit to run on.
//
// Three things can be wrong with it, and they are checked in the order that
// makes the message useful. Opening can fail outright. It can succeed and
// report corruption, which is the policy question in docs/decisions/0006. Or it
// can succeed cleanly onto a log that already holds records — which means this
// run ID has been used before, and appending a second run's events into the
// first one's history would leave a log that replays as neither.
func (g *Registry) openRunLog(runID string) (*eventlog.Log, eventlog.Recovery, error) {
	runLog, recovered, err := g.openHistory(runID)
	if err != nil {
		return nil, recovered, err
	}
	if runLog.Next() > 0 {
		_ = runLog.Close()
		return nil, recovered, fmt.Errorf("%w: %s holds %d records", ErrRunHasHistory, runID, runLog.Next())
	}
	return runLog, recovered, nil
}

// openHistory opens a run's log and applies the corrupt-recovery policy,
// without caring whether the log already holds records.
//
// Starting a run and recovering one want opposite things from an existing
// history — one refuses it, the other requires it — but both want the same
// answer to "the disk returned bytes that were wrong", which is why the policy
// lives here rather than in either caller.
func (g *Registry) openHistory(runID string) (*eventlog.Log, eventlog.Recovery, error) {
	runLog, recovered, err := g.openLog(runID)
	if err != nil {
		return nil, recovered, fmt.Errorf("open log for run %s: %w", runID, err)
	}

	if recovered.Corrupt != nil {
		if g.corrupt == RefuseCorrupt {
			_ = runLog.Close()
			return nil, recovered, fmt.Errorf("%w: run %s discarded %d bytes: %w",
				ErrCorruptLog, runID, recovered.Discarded, recovered.Corrupt)
		}
		// Continuing means the positions have to come back with the
		// tail, or the projection resumes at an offset that now names a
		// different record.
		if err := runLog.Rewind(recovered); err != nil {
			_ = runLog.Close()
			return nil, recovered, fmt.Errorf("%w: run %s cannot continue: %w", ErrCorruptLog, runID, err)
		}
	}
	return runLog, recovered, nil
}

func (g *Registry) lookup(runID string) (*liveRun, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, ErrRegistryDown
	}
	lr, ok := g.runs[runID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
	}
	return lr, nil
}

// generateIDLocked returns an unused run ID. IDs are sequential rather than
// random so logs and fixtures stay readable; explicit IDs are skipped over.
func (g *Registry) generateIDLocked() string {
	for {
		g.nextID++
		id := fmt.Sprintf("run-%04d", g.nextID)
		if _, taken := g.runs[id]; !taken {
			return id
		}
	}
}

// query runs a read against a run's owning goroutine.
func query[T any](g *Registry, runID string, read func(*liveRun) T) (T, error) {
	var zero T
	lr, err := g.lookup(runID)
	if err != nil {
		return zero, err
	}
	var out T
	if err := lr.do(func(r *liveRun) { out = read(r) }); err != nil {
		return zero, err
	}
	return out, nil
}

// liveRun is one run's state. Every field is owned by loop's goroutine and must
// only be touched from inside a command.
type liveRun struct {
	req     StartRunRequest
	sim     *sim.Sim
	clock   Clock
	metrics *metrics.Recorder

	state      State
	startedAt  time.Time
	updatedAt  time.Time
	finishedAt time.Time
	failure    error
	recovered  eventlog.Recovery

	// resumed marks a run the daemon picked back up rather than started.
	// It is on the wire because a client cannot tell otherwise: the garden
	// and the tick counter continue, but the determinism chain does not —
	// a resumed run did not fold the records below its snapshot.
	resumed bool

	ticker  Ticker
	subs    map[int]*Subscription
	nextSub int

	snapSeq          int64
	snapshotsSent    int64
	snapshotsDropped int64

	cmds chan func(*liveRun)
	quit chan struct{}
	done chan struct{}
}

// loop is the run's single owner. Ticks and commands share one select, which is
// what makes "controls apply on a tick boundary" true by construction instead
// of by locking discipline.
func (r *liveRun) loop() {
	defer close(r.done)

	// The log's lifetime is the run's, and the run outlives finishing: a
	// finished run still answers GetSnapshot and GetTelemetry, and telemetry
	// reads the log's offsets. So the close belongs here, where the owner
	// goroutine is actually going away, rather than in finish.
	defer func() { _ = r.sim.Close() }()

	for {
		select {
		case <-r.tickC():
			r.advance()
		case cmd := <-r.cmds:
			cmd(r)
		case <-r.quit:
			r.stopTicker()
			r.closeSubs(SubscriptionClosedRegistryShutdown)
			return
		}
	}
}

// tickC returns nil once the ticker is stopped. Receiving from a nil channel
// blocks forever, so a finished run keeps answering queries without burning a
// spin on ticks it would ignore.
func (r *liveRun) tickC() <-chan time.Time {
	if r.ticker == nil {
		return nil
	}
	return r.ticker.C()
}

func (r *liveRun) advance() {
	r.updatedAt = r.clock.Now()
	if r.state != StateRunning {
		return
	}
	stepStart := time.Now()
	err := r.sim.Step()
	r.metrics.ObserveTick(time.Since(stepStart))
	if err != nil {
		r.failure = err
		r.finish()
		return
	}
	r.metrics.ObservePending(r.req.RunID, r.sim.Pending())
	r.publish()
	if r.req.MaxTicks > 0 && r.sim.Tick() >= r.req.MaxTicks {
		r.finish()
	}
}

// finish stops the run and closes every projection stream. It is idempotent.
func (r *liveRun) finish() {
	if r.state == StateFinished {
		return
	}
	r.state = StateFinished
	r.sim.SetState(string(StateFinished))
	r.finishedAt = r.clock.Now()
	r.updatedAt = r.finishedAt

	// A final snapshot, so a finished run replays from where it ended rather
	// than from the last cadence boundary. A failure here does not un-finish
	// the run — the records are all still on disk — but it must not be
	// silent, because it means the next replay is slower than it looks.
	if err := r.sim.Save(); err != nil && r.failure == nil {
		r.failure = err
	}

	r.stopTicker()
	r.publish()
	r.closeSubs(SubscriptionClosedRunFinished)

	// A finished run cannot fall further behind, and its last observed
	// pending count (usually zero — finish drains what it can first) has
	// nothing left to say about consumer lag. Dropping it keeps the pending
	// total describing runs still capable of building a backlog.
	r.metrics.ForgetRun(r.req.RunID)
}

func (r *liveRun) stopTicker() {
	if r.ticker == nil {
		return
	}
	r.ticker.Stop()
	r.ticker = nil
}

// publish fans the current frame out to subscribers. A subscriber whose buffer
// is full loses the frame rather than stalling the run: one slow browser tab
// must not slow the simulation everyone else is watching.
func (r *liveRun) publish() {
	if len(r.subs) == 0 {
		return
	}
	r.snapSeq++
	frame := r.snapshot()
	for _, s := range r.subs {
		select {
		case s.ch <- frame:
			r.snapshotsSent++
			r.metrics.ObservePublish()
		default:
			r.snapshotsDropped++
			r.metrics.ObserveSnapshotDropped()
		}
	}
}

func (r *liveRun) removeSub(id int) {
	s, ok := r.subs[id]
	if !ok {
		return
	}
	delete(r.subs, id)
	close(s.ch)
}

func (r *liveRun) closeSubs(reason SubscriptionClosedReason) {
	for id, s := range r.subs {
		delete(r.subs, id)
		s.reason = reason
		close(s.ch)
	}
}

func (r *liveRun) view() Run {
	v := Run{
		RunID:        r.req.RunID,
		Seed:         r.req.Seed,
		Organisms:    r.req.Organisms,
		State:        r.state,
		Tick:         r.sim.Tick(),
		MaxTicks:     r.req.MaxTicks,
		TickInterval: r.req.TickInterval,
		Controls:     r.sim.Controls(),
		Revision:     r.sim.Revision(),
		StartedAt:    r.startedAt,
		UpdatedAt:    r.updatedAt,
		FinishedAt:   r.finishedAt,
	}
	if r.failure != nil {
		v.Failure = r.failure.Error()
	}
	if r.recovered.Corrupt != nil {
		v.LogRecovery = fmt.Sprintf("log opened corrupt and was allowed to continue: discarded %d bytes, resumed at offset %d: %v",
			r.recovered.Discarded, r.recovered.Next, r.recovered.Corrupt)
	}
	v.Resumed = r.resumed
	return v
}

func (r *liveRun) snapshot() GardenSnapshot {
	return GardenSnapshot{
		RunID:        r.req.RunID,
		Sequence:     r.snapSeq,
		Tick:         r.sim.Tick(),
		Revision:     r.sim.Revision(),
		State:        r.state,
		Stats:        r.sim.Stats(),
		Organisms:    r.sim.Organisms(),
		Hash:         r.sim.Hash(),
		ObservedAt:   r.clock.Now(),
		FoldedOffset: r.sim.Folded(),
	}
}

func (r *liveRun) telemetry() TelemetrySnapshot {
	return TelemetrySnapshot{
		RunID:                r.req.RunID,
		State:                r.state,
		Tick:                 r.sim.Tick(),
		Revision:             r.sim.Revision(),
		TickInterval:         r.req.TickInterval,
		Published:            r.sim.Published(),
		Processor:            r.sim.ProcessorStats(),
		Pending:              r.sim.Pending(),
		Subscribers:          len(r.subs),
		SnapshotsSent:        r.snapshotsSent,
		SnapshotsDropped:     r.snapshotsDropped,
		Uptime:               r.updatedAt.Sub(r.startedAt),
		ObservedAt:           r.clock.Now(),
		LogOffset:            r.sim.Offset(),
		CommittedOffset:      r.sim.Committed(),
		SnapshotSaveRetries:  r.sim.SnapshotSaveRetries(),
		SnapshotSaveFailures: r.sim.SnapshotSaveFailures(),
	}
}

// do runs fn on the run's goroutine and waits for it to complete. It reports
// ErrRunClosed once the registry has shut the run down.
func (r *liveRun) do(fn func(*liveRun)) error {
	ran := make(chan struct{})
	wrapped := func(lr *liveRun) {
		defer close(ran)
		fn(lr)
	}
	select {
	case r.cmds <- wrapped:
	case <-r.done:
		return ErrRunClosed
	}
	select {
	case <-ran:
		return nil
	case <-r.done:
		return ErrRunClosed
	}
}
