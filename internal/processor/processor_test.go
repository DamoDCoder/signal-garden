package processor

import (
	"testing"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/event"
)

func newProcessor(t *testing.T, organisms int) *Processor {
	t.Helper()
	g, err := domain.NewGarden(organisms)
	if err != nil {
		t.Fatalf("NewGarden: %v", err)
	}
	return New(g, nil)
}

func rainEvent(id string, seq int64, amount int) event.Event {
	return event.Event{
		EventID:       id,
		Type:          event.TypeRain,
		SchemaVersion: event.SchemaVersion,
		RunID:         "run-test",
		EntityID:      domain.OrganismID(0),
		PartitionKey:  "run-test",
		Sequence:      seq,
		Attempt:       1,
		Payload:       event.Payload{Amount: 10},
	}
}

func TestProcessAppliesOnce(t *testing.T) {
	p := newProcessor(t, 4)
	r := p.Process(rainEvent("evt-1", 1, 10))

	if r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}
	if r.Duplicate {
		t.Error("first delivery reported as duplicate")
	}
	if r.Outcome != domain.OutcomeApplied {
		t.Errorf("outcome = %q, want %q", r.Outcome, domain.OutcomeApplied)
	}

	o, _ := p.Garden().Get(domain.OrganismID(0))
	if o.Moisture != 10 {
		t.Errorf("moisture = %d, want 10", o.Moisture)
	}
}

// TestDuplicateDeliveryIsInert is the core M0 idempotency guarantee: at-least-
// once delivery must not corrupt the projection.
func TestDuplicateDeliveryIsInert(t *testing.T) {
	p := newProcessor(t, 4)
	e := rainEvent("evt-1", 1, 10)

	p.Process(e)
	afterFirst := p.Garden().Hash()

	redelivery := e
	redelivery.Attempt = 2
	r := p.Process(redelivery)

	if !r.Duplicate {
		t.Error("redelivery not flagged as duplicate")
	}
	if got := p.Garden().Hash(); got != afterFirst {
		t.Error("redelivery mutated the projection; processing must be idempotent")
	}
	if s := p.Stats(); s.Duplicates != 1 {
		t.Errorf("duplicates = %d, want 1", s.Duplicates)
	}
}

func TestManyRedeliveriesStayInert(t *testing.T) {
	p := newProcessor(t, 4)
	e := rainEvent("evt-1", 1, 10)

	p.Process(e)
	want := p.Garden().Hash()

	for i := 0; i < 50; i++ {
		p.Process(e)
	}
	if got := p.Garden().Hash(); got != want {
		t.Error("repeated redelivery changed state")
	}
	if s := p.Stats(); s.Duplicates != 50 {
		t.Errorf("duplicates = %d, want 50", s.Duplicates)
	}
}

// TestControlEventIdempotencyUsesRevision covers the docs/events.md rule that
// control changes key on run plus revision rather than event id, so a producer
// that regenerates an id cannot double-apply a revision.
func TestControlEventIdempotencyUsesRevision(t *testing.T) {
	p := newProcessor(t, 4)

	first := event.Event{
		EventID:       "ctl-a",
		Type:          event.TypeControlChanged,
		SchemaVersion: event.SchemaVersion,
		RunID:         "run-test",
		Sequence:      1,
		Attempt:       1,
		Payload:       event.Payload{Revision: 1},
	}
	second := first
	second.EventID = "ctl-b" // different id, same revision
	second.Sequence = 2

	if r := p.Process(first); r.Duplicate {
		t.Fatal("first control change flagged as duplicate")
	}
	if r := p.Process(second); !r.Duplicate {
		t.Error("same revision under a new event id was not deduplicated")
	}

	third := first
	third.EventID = "ctl-c"
	third.Payload.Revision = 2
	if r := p.Process(third); r.Duplicate {
		t.Error("a new revision was incorrectly deduplicated")
	}
}

func TestProcessRejectsInvalidEnvelope(t *testing.T) {
	p := newProcessor(t, 4)

	bad := rainEvent("", 1, 10) // missing event id
	r := p.Process(bad)

	if r.Err == nil {
		t.Fatal("expected a validation error")
	}
	if s := p.Stats(); s.Rejected != 1 || s.Applied != 0 {
		t.Errorf("stats = %+v, want 1 rejected and 0 applied", s)
	}
}

func TestProcessBatchReportsAllErrors(t *testing.T) {
	p := newProcessor(t, 4)

	err := p.ProcessBatch([]event.Event{
		rainEvent("evt-1", 1, 10),
		rainEvent("", 2, 10),
		rainEvent("evt-3", 3, 10),
		{EventID: "evt-4", Type: "not_a_type", SchemaVersion: 1, RunID: "r", Attempt: 1},
	})
	if err == nil {
		t.Fatal("expected joined errors from the batch")
	}

	s := p.Stats()
	if s.Received != 4 {
		t.Errorf("received = %d, want 4", s.Received)
	}
	if s.Rejected != 2 {
		t.Errorf("rejected = %d, want 2", s.Rejected)
	}
	if s.Applied != 2 {
		t.Errorf("applied = %d, want 2", s.Applied)
	}
}

func TestStatsIsACopy(t *testing.T) {
	p := newProcessor(t, 4)
	p.Process(rainEvent("evt-1", 1, 10))

	s := p.Stats()
	s.ByType["rain"] = 999
	s.Applied = 999

	if got := p.Stats(); got.ByType["rain"] == 999 || got.Applied == 999 {
		t.Error("Stats() exposed internal state to mutation")
	}
}
