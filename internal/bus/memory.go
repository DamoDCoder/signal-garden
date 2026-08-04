// Package bus provides the M0 in-memory event transport.
//
// This is the seam Kafka replaces at M2. Publish is the producer side and
// Drain is the consumer side; keeping them behind this boundary means the
// processor never learns whether its events arrived from memory or a topic.
package bus

import (
	"sync"

	"github.com/damodbear/signal-garden/internal/event"
)

// Memory is an ordered, in-process event queue.
//
// It preserves publish order because M0 asserts replay determinism, and an
// unordered transport would make that property untestable before Kafka's
// per-partition ordering exists to provide it.
type Memory struct {
	mu     sync.Mutex
	queue  []event.Event
	total  int
	closed bool
}

// NewMemory returns an empty in-memory bus.
func NewMemory() *Memory {
	return &Memory{}
}

// Publish appends events to the queue in the order given.
func (m *Memory) Publish(events ...event.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, events...)
	m.total += len(events)
	return nil
}

// Drain removes and returns every queued event, preserving order. It returns
// nil when the queue is empty, so callers can range over the result directly.
func (m *Memory) Drain() []event.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queue) == 0 {
		return nil
	}
	out := m.queue
	m.queue = nil
	return out
}

// Len reports the number of events currently waiting.
//
// At M0 this is always drained to zero each tick. At M2 the same measurement
// becomes consumer lag, which is the number the player is meant to react to.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queue)
}

// Published reports the total number of events ever published to this bus.
func (m *Memory) Published() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.total
}
