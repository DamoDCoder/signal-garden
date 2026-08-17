package event

import (
	"bytes"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/DamoDCoder/event-spine/core"
)

func TestToCoreMapsTheHeaderFields(t *testing.T) {
	e := validEvent()
	e.OccurredAt = 17
	e.PartitionKey = "run-test"

	rec, err := e.ToCore()
	if err != nil {
		t.Fatalf("ToCore() error = %v", err)
	}

	if rec.Key != e.PartitionKey {
		t.Errorf("record key = %q, want the partition key %q", rec.Key, e.PartitionKey)
	}
	if rec.Time != core.Time(e.OccurredAt) {
		t.Errorf("record time = %d, want the simulation time %d", rec.Time, e.OccurredAt)
	}
	if rec.Schema != uint16(e.SchemaVersion) {
		t.Errorf("record schema = %d, want %d", rec.Schema, e.SchemaVersion)
	}
	if err := rec.Validate(); err != nil {
		t.Errorf("record is not valid to the log: %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		event Event
	}{
		{"organism event", validEvent()},
		{"control change", func() Event {
			e := validEvent()
			e.Type = TypeControlChanged
			e.EntityID = ""
			e.Payload = Payload{Revision: 4}
			return e
		}()},
		{"run state change", func() Event {
			e := validEvent()
			e.Type = TypeRunStateChanged
			e.EntityID = ""
			e.Payload = Payload{Revision: 1}
			return e
		}()},
		{"redelivered event", func() Event {
			e := validEvent()
			e.Attempt = 3
			return e
		}()},
		{"late tick", func() Event {
			e := validEvent()
			e.OccurredAt = 4096
			e.Sequence = 900
			return e
		}()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, err := tc.event.ToCore()
			if err != nil {
				t.Fatalf("ToCore() error = %v", err)
			}
			got, err := FromCore(rec)
			if err != nil {
				t.Fatalf("FromCore() error = %v", err)
			}
			if got != tc.event {
				t.Errorf("round trip changed the envelope:\n got %+v\nwant %+v", got, tc.event)
			}
			if got.IdempotencyKey() != tc.event.IdempotencyKey() {
				t.Errorf("idempotency key = %q, want %q", got.IdempotencyKey(), tc.event.IdempotencyKey())
			}
		})
	}
}

// The record must not carry wall-clock time. Two envelopes that differ only in
// RecordedAt have to encode to the same bytes, or a replay of the same seed
// produces a different log and every byte-level assertion downstream is
// meaningless.
func TestRecordedAtIsNotDurable(t *testing.T) {
	early := validEvent()
	early.RecordedAt = time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)

	late := validEvent()
	late.RecordedAt = time.Date(2031, 1, 1, 23, 59, 59, 0, time.UTC)

	a, err := early.ToCore()
	if err != nil {
		t.Fatalf("ToCore() error = %v", err)
	}
	b, err := late.ToCore()
	if err != nil {
		t.Fatalf("ToCore() error = %v", err)
	}

	if !bytes.Equal(a.AppendCanonical(nil), b.AppendCanonical(nil)) {
		t.Fatal("wall-clock time reached the record: two envelopes differing only in RecordedAt encoded differently")
	}

	back, err := FromCore(a)
	if err != nil {
		t.Fatalf("FromCore() error = %v", err)
	}
	if !back.RecordedAt.IsZero() {
		t.Errorf("RecordedAt = %v after a round trip, want the zero time", back.RecordedAt)
	}
}

// The canonical encoding is what the log's CRC covers and what a projection
// digest is compared against. Encoding one envelope twice must produce
// identical bytes, or a record written before a restart cannot be compared with
// one written after it.
func TestEncodingIsStable(t *testing.T) {
	e := validEvent()

	first, err := e.ToCore()
	if err != nil {
		t.Fatalf("ToCore() error = %v", err)
	}
	for i := range 32 {
		next, err := e.ToCore()
		if err != nil {
			t.Fatalf("ToCore() error = %v", err)
		}
		if !bytes.Equal(first.AppendCanonical(nil), next.AppendCanonical(nil)) {
			t.Fatalf("encoding %d differs from the first", i)
		}
	}
}

func TestDistinctEventsEncodeDistinctly(t *testing.T) {
	base := validEvent()

	variants := map[string]func(*Event){
		"event id":      func(e *Event) { e.EventID = "evt-2" },
		"type":          func(e *Event) { e.Type = TypePest },
		"run id":        func(e *Event) { e.RunID = "run-other" },
		"entity id":     func(e *Event) { e.EntityID = "org-001" },
		"partition key": func(e *Event) { e.PartitionKey = "run-other" },
		"sequence":      func(e *Event) { e.Sequence = 99 },
		"occurred at":   func(e *Event) { e.OccurredAt = 99 },
		"attempt":       func(e *Event) { e.Attempt = 2 },
		"amount":        func(e *Event) { e.Payload.Amount = 99 },
	}

	rec, err := base.ToCore()
	if err != nil {
		t.Fatalf("ToCore() error = %v", err)
	}
	want := rec.AppendCanonical(nil)

	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			other := base
			mutate(&other)
			otherRec, err := other.ToCore()
			if err != nil {
				t.Fatalf("ToCore() error = %v", err)
			}
			if bytes.Equal(want, otherRec.AppendCanonical(nil)) {
				t.Errorf("changing the %s did not change the record", name)
			}
		})
	}
}

func TestToCoreRejectsInvalidEnvelopes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{"missing event id", func(e *Event) { e.EventID = "" }},
		{"unknown type", func(e *Event) { e.Type = "compost" }},
		{"missing run id", func(e *Event) { e.RunID = "" }},
		{"negative occurred at", func(e *Event) { e.OccurredAt = -1 }},
		{"zero attempt", func(e *Event) { e.Attempt = 0 }},
		{"organism event without entity", func(e *Event) { e.EntityID = "" }},
		{"schema version beyond the header", func(e *Event) { e.SchemaVersion = math.MaxUint16 + 1 }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := validEvent()
			tc.mutate(&e)
			if _, err := e.ToCore(); !errors.Is(err, ErrInvalidEvent) {
				t.Errorf("ToCore() error = %v, want one wrapping ErrInvalidEvent", err)
			}
		})
	}
}

func TestFromCoreRejectsBadRecords(t *testing.T) {
	valid, err := validEvent().ToCore()
	if err != nil {
		t.Fatalf("ToCore() error = %v", err)
	}

	tests := []struct {
		name   string
		record core.Event
	}{
		{"payload is not json", core.Event{Key: "run-test", Schema: 1, Payload: []byte("not json")}},
		{"payload is empty", core.Event{Key: "run-test", Schema: 1}},
		{"envelope decodes but does not validate", core.Event{
			Key:     valid.Key,
			Time:    valid.Time,
			Schema:  valid.Schema,
			Payload: []byte(`{"event_id":"","event_type":"rain","run_id":"run-test","sequence":1,"attempt":1}`),
		}},
		{"schema version zero", core.Event{Key: valid.Key, Time: valid.Time, Schema: 0, Payload: valid.Payload}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := FromCore(tc.record); !errors.Is(err, ErrInvalidEvent) {
				t.Errorf("FromCore() error = %v, want one wrapping ErrInvalidEvent", err)
			}
		})
	}
}
