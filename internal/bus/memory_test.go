package bus

import (
	"sync"
	"testing"

	"github.com/damodbear/signal-garden/internal/event"
)

func testEvent(id string, seq int64) event.Event {
	return event.Event{
		EventID:       id,
		Type:          event.TypeRain,
		SchemaVersion: event.SchemaVersion,
		RunID:         "run-test",
		EntityID:      "org-000",
		Sequence:      seq,
		Attempt:       1,
	}
}

func TestPublishPreservesOrder(t *testing.T) {
	m := NewMemory()

	want := []event.Event{testEvent("a", 1), testEvent("b", 2), testEvent("c", 3)}
	if err := m.Publish(want...); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got := m.Drain()
	if len(got) != len(want) {
		t.Fatalf("drained %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDrainEmptiesQueue(t *testing.T) {
	m := NewMemory()
	m.Publish(testEvent("a", 1), testEvent("b", 2))

	if got := m.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	m.Drain()
	if got := m.Len(); got != 0 {
		t.Errorf("Len() after Drain = %d, want 0", got)
	}
	if got := m.Drain(); got != nil {
		t.Errorf("second Drain = %v, want nil", got)
	}
}

func TestPublishedCountsEverything(t *testing.T) {
	m := NewMemory()
	m.Publish(testEvent("a", 1), testEvent("b", 2))
	m.Drain()
	m.Publish(testEvent("c", 3))

	if got := m.Published(); got != 3 {
		t.Errorf("Published() = %d, want 3; draining must not reset the total", got)
	}
}

// TestConcurrentPublishIsSafe exercises the mutex under the race detector.
// M0 drives the bus from one goroutine, but M1 splits producer and processor
// across services, so the guarantee needs to hold before that lands.
func TestConcurrentPublishIsSafe(t *testing.T) {
	m := NewMemory()

	const writers, each = 8, 50
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				m.Publish(testEvent("e", int64(i)))
			}
		}()
	}
	wg.Wait()

	if got := m.Published(); got != writers*each {
		t.Errorf("Published() = %d, want %d", got, writers*each)
	}
	if got := len(m.Drain()); got != writers*each {
		t.Errorf("drained %d events, want %d", got, writers*each)
	}
}
