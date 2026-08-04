package event

import (
	"errors"
	"testing"
)

func validEvent() Event {
	return Event{
		EventID:       "evt-1",
		Type:          TypeRain,
		SchemaVersion: SchemaVersion,
		RunID:         "run-test",
		EntityID:      "org-000",
		PartitionKey:  "run-test",
		Sequence:      1,
		OccurredAt:    0,
		Attempt:       1,
		Payload:       Payload{Amount: 10},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Event)
		wantErr bool
	}{
		{"valid", func(*Event) {}, false},
		{"zero sequence is allowed", func(e *Event) { e.Sequence = 0 }, false},
		{"control event needs no entity", func(e *Event) {
			e.Type = TypeControlChanged
			e.EntityID = ""
		}, false},

		{"missing event id", func(e *Event) { e.EventID = "" }, true},
		{"unknown type", func(e *Event) { e.Type = "compost" }, true},
		{"empty type", func(e *Event) { e.Type = "" }, true},
		{"zero schema version", func(e *Event) { e.SchemaVersion = 0 }, true},
		{"missing run id", func(e *Event) { e.RunID = "" }, true},
		{"negative sequence", func(e *Event) { e.Sequence = -1 }, true},
		{"negative occurred_at", func(e *Event) { e.OccurredAt = -1 }, true},
		{"zero attempt", func(e *Event) { e.Attempt = 0 }, true},
		{"organism event without entity", func(e *Event) { e.EntityID = "" }, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := validEvent()
			tc.mutate(&e)
			err := e.Validate()

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Validate() = nil, want error for %+v", e)
				}
				if !errors.Is(err, ErrInvalidEvent) {
					t.Errorf("error %v does not wrap ErrInvalidEvent", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestIdempotencyKey(t *testing.T) {
	t.Run("organism events key on event id", func(t *testing.T) {
		for _, typ := range []Type{TypeRain, TypeGrowth, TypePest} {
			e := validEvent()
			e.Type = typ
			if got := e.IdempotencyKey(); got != e.EventID {
				t.Errorf("%s key = %q, want %q", typ, got, e.EventID)
			}
		}
	})

	t.Run("control events key on run and revision", func(t *testing.T) {
		a := validEvent()
		a.Type = TypeControlChanged
		a.Payload = Payload{Revision: 3}

		b := a
		b.EventID = "a-completely-different-id"

		if a.IdempotencyKey() != b.IdempotencyKey() {
			t.Error("same run and revision produced different keys")
		}

		c := a
		c.Payload.Revision = 4
		if a.IdempotencyKey() == c.IdempotencyKey() {
			t.Error("different revisions produced the same key")
		}
	})

	t.Run("run state and control events do not collide", func(t *testing.T) {
		ctl := validEvent()
		ctl.Type = TypeControlChanged
		ctl.Payload = Payload{Revision: 1}

		state := ctl
		state.Type = TypeRunStateChanged

		if ctl.IdempotencyKey() == state.IdempotencyKey() {
			t.Error("control and run-state events share a key at the same revision")
		}
	})
}

func TestTypeValid(t *testing.T) {
	valid := []Type{TypeRain, TypeGrowth, TypePest, TypeControlChanged, TypeRunStateChanged}
	for _, typ := range valid {
		if !typ.Valid() {
			t.Errorf("%q reported invalid", typ)
		}
	}
	for _, typ := range []Type{"", "rain ", "RAIN", "harvest"} {
		if typ.Valid() {
			t.Errorf("%q reported valid", typ)
		}
	}
}

func TestIsOrganismEvent(t *testing.T) {
	for _, typ := range []Type{TypeRain, TypeGrowth, TypePest} {
		e := Event{Type: typ}
		if !e.IsOrganismEvent() {
			t.Errorf("%q should be an organism event", typ)
		}
	}
	for _, typ := range []Type{TypeControlChanged, TypeRunStateChanged} {
		e := Event{Type: typ}
		if e.IsOrganismEvent() {
			t.Errorf("%q should not be an organism event", typ)
		}
	}
}
