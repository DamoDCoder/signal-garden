// Package event defines the durable event envelope described in docs/events.md.
//
// The envelope is transport-neutral on purpose. At M0 it travels through an
// in-memory bus; at M2 the same fields become the Kafka record and its key.
package event

import (
	"errors"
	"fmt"
	"time"
)

// SchemaVersion is the payload version carried by every event this build emits.
// Bump it when a payload change is not backward compatible, and record the
// replay strategy in docs/decisions/.
const SchemaVersion = 1

// Type identifies a domain event. The values are wire strings, so treat them as
// part of the contract rather than internal identifiers.
type Type string

const (
	TypeRain            Type = "rain"
	TypeGrowth          Type = "growth"
	TypePest            Type = "pest"
	TypeControlChanged  Type = "control_changed"
	TypeRunStateChanged Type = "run_state_changed"
)

// Valid reports whether t is a known event type.
func (t Type) Valid() bool {
	switch t {
	case TypeRain, TypeGrowth, TypePest, TypeControlChanged, TypeRunStateChanged:
		return true
	default:
		return false
	}
}

// Payload carries typed event data. Amount is the magnitude for rain, growth,
// and pest events; Revision identifies a control or run-state change.
type Payload struct {
	Amount   int `json:"amount,omitempty"`
	Revision int `json:"revision,omitempty"`
}

// Event is the durable envelope. OccurredAt is simulation time and may
// influence outcomes; RecordedAt is wall-clock time and must not, because
// replay reproduces the former and never the latter.
type Event struct {
	EventID       string    `json:"event_id"`
	Type          Type      `json:"event_type"`
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	EntityID      string    `json:"entity_id"`
	PartitionKey  string    `json:"partition_key"`
	Sequence      int64     `json:"sequence"`
	OccurredAt    int64     `json:"occurred_at"`
	RecordedAt    time.Time `json:"recorded_at"`
	Attempt       int       `json:"attempt"`
	Payload       Payload   `json:"payload"`
}

// ErrInvalidEvent wraps every validation failure so callers can classify a
// rejection without matching on message text.
var ErrInvalidEvent = errors.New("invalid event")

// Validate checks the envelope invariants the processor depends on.
func (e Event) Validate() error {
	if e.EventID == "" {
		return fmt.Errorf("%w: event_id is required", ErrInvalidEvent)
	}
	if !e.Type.Valid() {
		return fmt.Errorf("%w: unknown event_type %q", ErrInvalidEvent, e.Type)
	}
	if e.SchemaVersion <= 0 {
		return fmt.Errorf("%w: schema_version must be positive, got %d", ErrInvalidEvent, e.SchemaVersion)
	}
	if e.RunID == "" {
		return fmt.Errorf("%w: run_id is required", ErrInvalidEvent)
	}
	if e.Sequence < 0 {
		return fmt.Errorf("%w: sequence must not be negative, got %d", ErrInvalidEvent, e.Sequence)
	}
	if e.OccurredAt < 0 {
		return fmt.Errorf("%w: occurred_at must not be negative, got %d", ErrInvalidEvent, e.OccurredAt)
	}
	if e.Attempt < 1 {
		return fmt.Errorf("%w: attempt must be at least 1, got %d", ErrInvalidEvent, e.Attempt)
	}
	if e.IsOrganismEvent() && e.EntityID == "" {
		return fmt.Errorf("%w: entity_id is required for %s", ErrInvalidEvent, e.Type)
	}
	return nil
}

// IsOrganismEvent reports whether the event targets a single organism.
func (e Event) IsOrganismEvent() bool {
	switch e.Type {
	case TypeRain, TypeGrowth, TypePest:
		return true
	default:
		return false
	}
}

// IdempotencyKey returns the value the processor deduplicates on, following the
// table in docs/events.md. Organism events key on event_id; control and
// run-state events key on the run and its revision, so a redelivered control
// change cannot apply twice.
func (e Event) IdempotencyKey() string {
	switch e.Type {
	case TypeControlChanged, TypeRunStateChanged:
		return fmt.Sprintf("%s:%s:%d", e.RunID, e.Type, e.Payload.Revision)
	default:
		return e.EventID
	}
}
