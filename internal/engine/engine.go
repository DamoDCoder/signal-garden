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

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/processor"
	"github.com/damodbear/signal-garden/internal/sim"
)

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
)

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
}

// RunSummary is the scorecard a finished run leaves behind.
type RunSummary struct {
	Run       Run               `json:"run"`
	Snapshot  GardenSnapshot    `json:"snapshot"`
	Telemetry TelemetrySnapshot `json:"telemetry"`
}

// Subscription is a live projection stream for one run. The channel closes
// when the run finishes or the subscription is closed.
type Subscription struct {
	id   int
	ch   chan GardenSnapshot
	run  *liveRun
	once sync.Once
}

// Snapshots returns the frame channel.
func (s *Subscription) Snapshots() <-chan GardenSnapshot { return s.ch }

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

// NewRegistry returns an empty registry driven by the system clock.
func NewRegistry(opts ...Option) *Registry {
	reg := &Registry{
		clock:    SystemClock(),
		interval: DefaultTickInterval,
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

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return Run{}, ErrRegistryDown
	}
	if req.RunID == "" {
		req.RunID = g.generateIDLocked()
	} else if _, exists := g.runs[req.RunID]; exists {
		g.mu.Unlock()
		return Run{}, fmt.Errorf("%w: %s", ErrRunExists, req.RunID)
	}

	s, err := sim.New(sim.Config{
		RunID:          req.RunID,
		Seed:           req.Seed,
		Organisms:      req.Organisms,
		Controls:       req.Controls,
		DuplicateEvery: req.DuplicateEvery,
	})
	if err != nil {
		g.mu.Unlock()
		return Run{}, err
	}

	now := g.clock.Now()
	lr := &liveRun{
		req:       req,
		sim:       s,
		clock:     g.clock,
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
	lr, err := g.lookup(runID)
	if err != nil {
		return nil, err
	}
	if buffer <= 0 {
		buffer = defaultSubscriberBuffer
	}
	sub := &Subscription{ch: make(chan GardenSnapshot, buffer), run: lr}
	if err := lr.do(func(r *liveRun) {
		sub.ch <- r.snapshot()
		r.snapshotsSent++
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
		return nil, err
	}
	return sub, nil
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
	req   StartRunRequest
	sim   *sim.Sim
	clock Clock

	state      State
	startedAt  time.Time
	updatedAt  time.Time
	finishedAt time.Time
	failure    error

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
			r.closeSubs()
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
	if err := r.sim.Step(); err != nil {
		r.failure = err
		r.finish()
		return
	}
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
	r.finishedAt = r.clock.Now()
	r.updatedAt = r.finishedAt
	r.stopTicker()
	r.publish()
	r.closeSubs()
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
		default:
			r.snapshotsDropped++
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

func (r *liveRun) closeSubs() {
	for id, s := range r.subs {
		delete(r.subs, id)
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
	return v
}

func (r *liveRun) snapshot() GardenSnapshot {
	return GardenSnapshot{
		RunID:      r.req.RunID,
		Sequence:   r.snapSeq,
		Tick:       r.sim.Tick(),
		Revision:   r.sim.Revision(),
		State:      r.state,
		Stats:      r.sim.Stats(),
		Organisms:  r.sim.Organisms(),
		Hash:       r.sim.Hash(),
		ObservedAt: r.clock.Now(),
	}
}

func (r *liveRun) telemetry() TelemetrySnapshot {
	return TelemetrySnapshot{
		RunID:            r.req.RunID,
		State:            r.state,
		Tick:             r.sim.Tick(),
		Revision:         r.sim.Revision(),
		TickInterval:     r.req.TickInterval,
		Published:        r.sim.Published(),
		Processor:        r.sim.ProcessorStats(),
		Pending:          r.sim.Pending(),
		Subscribers:      len(r.subs),
		SnapshotsSent:    r.snapshotsSent,
		SnapshotsDropped: r.snapshotsDropped,
		Uptime:           r.updatedAt.Sub(r.startedAt),
		ObservedAt:       r.clock.Now(),
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
