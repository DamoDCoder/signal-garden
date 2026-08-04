package engine

import (
	"sync"
	"time"
)

// Clock is the engine's only source of time.
//
// It exists so tests drive ticks explicitly instead of sleeping. A live run is
// still deterministic in outcome — simulation time is the tick counter, and
// docs/events.md forbids wall-clock time from influencing results — but a test
// that waits for real milliseconds is flaky regardless of what it asserts.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
}

// Ticker delivers a tick every interval until stopped.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// SystemClock returns the clock backed by the time package.
func SystemClock() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) NewTicker(d time.Duration) Ticker {
	return &systemTicker{t: time.NewTicker(d)}
}

type systemTicker struct{ t *time.Ticker }

func (s *systemTicker) C() <-chan time.Time { return s.t.C }
func (s *systemTicker) Stop()               { s.t.Stop() }

// ManualClock fires ticks on demand.
//
// Its tick channels are unbuffered, so Tick returns only once the run loop has
// taken the tick. Because the loop processes commands from the same select, a
// GetSnapshot issued after Tick returns is guaranteed to observe that tick's
// result — which is how the engine tests stay deterministic without sleeping.
type ManualClock struct {
	mu      sync.Mutex
	now     time.Time
	step    time.Duration
	tickers []*manualTicker
}

// NewManualClock returns a clock starting at start, where each fired tick
// advances the clock by step. Step stands in for the run's tick interval; it
// only affects reported timestamps, never simulation outcomes.
func NewManualClock(start time.Time, step time.Duration) *ManualClock {
	return &ManualClock{now: start, step: step}
}

// Now returns the current manual time.
func (m *ManualClock) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

// NewTicker registers a ticker that fires only when Tick is called. The
// requested interval is ignored; the clock's own step governs time.
func (m *ManualClock) NewTicker(time.Duration) Ticker {
	t := &manualTicker{ch: make(chan time.Time), stopped: make(chan struct{})}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickers = append(m.tickers, t)
	return t
}

// Tick advances the clock and fires every live ticker n times. Stopped tickers
// are skipped rather than blocking, so a finished run cannot wedge a test.
func (m *ManualClock) Tick(n int) {
	for i := 0; i < n; i++ {
		m.mu.Lock()
		m.now = m.now.Add(m.step)
		now := m.now
		live := make([]*manualTicker, len(m.tickers))
		copy(live, m.tickers)
		m.mu.Unlock()

		for _, t := range live {
			select {
			case t.ch <- now:
			case <-t.stopped:
			}
		}
	}
}

type manualTicker struct {
	ch       chan time.Time
	stopped  chan struct{}
	stopOnce sync.Once
}

func (t *manualTicker) C() <-chan time.Time { return t.ch }

func (t *manualTicker) Stop() {
	t.stopOnce.Do(func() { close(t.stopped) })
}
