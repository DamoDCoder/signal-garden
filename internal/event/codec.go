package event

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/DamoDCoder/event-spine/core"
)

// The durable record and the envelope are not the same shape, and the
// difference is deliberate.
//
// A log record carries a partition key, a logical timestamp, and a schema
// version in its header, where the log itself can read them without decoding a
// payload. Repeating those three inside the payload would create a second place
// each could be wrong, so the payload holds exactly the fields the header does
// not.
//
// The envelope's recorded_at is dropped entirely. It is wall-clock ingestion
// time, docs/events.md forbids it from influencing outcomes, and writing it
// would mean two runs of the same seed produce different bytes — which would
// make every byte-level replay assertion untestable. FromCore therefore returns
// an envelope with a zero RecordedAt, and that is not a lossy round trip so
// much as the point of the encoding.

// payload is the durable body: the envelope minus what the record header
// already carries and minus wall-clock time. Field order is fixed by the struct
// and there are no maps, so encoding the same envelope twice is byte-identical.
type payload struct {
	EventID  string  `json:"event_id"`
	Type     Type    `json:"event_type"`
	RunID    string  `json:"run_id"`
	EntityID string  `json:"entity_id,omitempty"`
	Sequence int64   `json:"sequence"`
	Attempt  int     `json:"attempt"`
	Data     Payload `json:"payload"`
}

// ToCore converts an envelope into the record the log appends.
//
// The envelope is validated first: an event the log accepts is one the
// processor can act on, and rejecting here names the event rather than a byte
// offset in a segment.
func (e Event) ToCore() (core.Event, error) {
	if err := e.Validate(); err != nil {
		return core.Event{}, err
	}
	if e.SchemaVersion > math.MaxUint16 {
		return core.Event{}, fmt.Errorf("%w: schema_version %d exceeds the record header's uint16", ErrInvalidEvent, e.SchemaVersion)
	}

	body, err := json.Marshal(payload{
		EventID:  e.EventID,
		Type:     e.Type,
		RunID:    e.RunID,
		EntityID: e.EntityID,
		Sequence: e.Sequence,
		Attempt:  e.Attempt,
		Data:     e.Payload,
	})
	if err != nil {
		return core.Event{}, fmt.Errorf("encode event %s: %w", e.EventID, err)
	}

	rec := core.Event{
		Key:     e.PartitionKey,
		Time:    core.Time(e.OccurredAt),
		Schema:  uint16(e.SchemaVersion),
		Payload: body,
	}
	if err := rec.Validate(); err != nil {
		return core.Event{}, fmt.Errorf("event %s is not a valid record: %w", e.EventID, err)
	}
	return rec, nil
}

// FromCore rebuilds an envelope from a record read back out of the log.
//
// RecordedAt comes back zero because it was never written. A caller that wants
// to know when a record was read is asking about this process, not about the
// run, and should say so with its own field.
func FromCore(rec core.Event) (Event, error) {
	var body payload
	if err := json.Unmarshal(rec.Payload, &body); err != nil {
		return Event{}, fmt.Errorf("%w: decode record payload: %w", ErrInvalidEvent, err)
	}

	e := Event{
		EventID:       body.EventID,
		Type:          body.Type,
		SchemaVersion: int(rec.Schema),
		RunID:         body.RunID,
		EntityID:      body.EntityID,
		PartitionKey:  rec.Key,
		Sequence:      body.Sequence,
		OccurredAt:    int64(rec.Time),
		Attempt:       body.Attempt,
		Payload:       body.Data,
	}
	if err := e.Validate(); err != nil {
		return Event{}, err
	}
	return e, nil
}
