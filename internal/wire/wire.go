// Package wire translates engine types into the generated protobuf messages.
//
// It exists because there are two transports over one contract. The gRPC
// service answers requests and the projection gateway streams frames, and both
// have to describe a garden the same way — a second mapping would be a second
// definition, free to drift from this one until a client noticed.
//
// Nothing here makes a decision. Every function is total, takes an engine or
// domain value, and returns the message that stands for it.
package wire

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/engine"
	"github.com/damodbear/signal-garden/internal/event"
	gardenv1 "github.com/damodbear/signal-garden/internal/gen/signal/garden/v1"
	"github.com/damodbear/signal-garden/internal/processor"
)

func ControlsFrom(c *gardenv1.Controls) domain.Controls {
	if c == nil {
		return domain.Controls{}
	}
	return domain.Controls{
		EventsPerTick: int(c.GetEventsPerTick()),
		RainWeight:    int(c.GetRainWeight()),
		GrowthWeight:  int(c.GetGrowthWeight()),
		PestWeight:    int(c.GetPestWeight()),
	}
}

func Controls(c domain.Controls) *gardenv1.Controls {
	return &gardenv1.Controls{
		EventsPerTick: int32(c.EventsPerTick),
		RainWeight:    int32(c.RainWeight),
		GrowthWeight:  int32(c.GrowthWeight),
		PestWeight:    int32(c.PestWeight),
	}
}

func State(s engine.State) gardenv1.RunState {
	switch s {
	case engine.StateRunning:
		return gardenv1.RunState_RUN_STATE_RUNNING
	case engine.StatePaused:
		return gardenv1.RunState_RUN_STATE_PAUSED
	case engine.StateFinished:
		return gardenv1.RunState_RUN_STATE_FINISHED
	default:
		return gardenv1.RunState_RUN_STATE_UNSPECIFIED
	}
}

func Run(r engine.Run) *gardenv1.Run {
	return &gardenv1.Run{
		RunId:         r.RunID,
		Seed:          r.Seed,
		Organisms:     int32(r.Organisms),
		State:         State(r.State),
		Tick:          r.Tick,
		MaxTicks:      r.MaxTicks,
		TickInterval:  durationpb.New(r.TickInterval),
		Controls:      Controls(r.Controls),
		Revision:      int32(r.Revision),
		StartedAt:     timestampOf(r.StartedAt),
		UpdatedAt:     timestampOf(r.UpdatedAt),
		FinishedAt:    timestampOf(r.FinishedAt),
		Failure:       r.Failure,
		SchemaVersion: event.SchemaVersion,
	}
}

func Snapshot(s engine.GardenSnapshot) *gardenv1.GardenSnapshot {
	organisms := make([]*gardenv1.Organism, 0, len(s.Organisms))
	for _, o := range s.Organisms {
		organisms = append(organisms, &gardenv1.Organism{
			Id:       o.ID,
			Moisture: int32(o.Moisture),
			Health:   int32(o.Health),
			Stage:    int32(o.Stage),
		})
	}
	return &gardenv1.GardenSnapshot{
		RunId:    s.RunID,
		Sequence: s.Sequence,
		Tick:     s.Tick,
		Revision: int32(s.Revision),
		State:    State(s.State),
		Stats: &gardenv1.GardenStats{
			Organisms:       int32(s.Stats.Organisms),
			Alive:           int32(s.Stats.Alive),
			AverageMoisture: s.Stats.AverageMoist,
			AverageHealth:   s.Stats.AverageHP,
			AverageStage:    s.Stats.AverageStage,
			TotalStage:      int32(s.Stats.TotalStage),
		},
		Organisms:     organisms,
		Hash:          s.Hash,
		ObservedAt:    timestampOf(s.ObservedAt),
		SchemaVersion: event.SchemaVersion,
		FoldedOffset:  s.FoldedOffset,
	}
}

func Telemetry(t engine.TelemetrySnapshot) *gardenv1.TelemetrySnapshot {
	return &gardenv1.TelemetrySnapshot{
		RunId:            t.RunID,
		State:            State(t.State),
		Tick:             t.Tick,
		Revision:         int32(t.Revision),
		TickInterval:     durationpb.New(t.TickInterval),
		Published:        int64(t.Published),
		Processor:        processorStats(t.Processor),
		Pending:          int64(t.Pending),
		Subscribers:      int32(t.Subscribers),
		SnapshotsSent:    t.SnapshotsSent,
		SnapshotsDropped: t.SnapshotsDropped,
		Uptime:           durationpb.New(t.Uptime),
		ObservedAt:       timestampOf(t.ObservedAt),
		SchemaVersion:    event.SchemaVersion,
		LogOffset:        t.LogOffset,
		CommittedOffset:  t.CommittedOffset,
	}
}

func processorStats(p processor.Stats) *gardenv1.ProcessorStats {
	byType := make(map[string]int64, len(p.ByType))
	for k, v := range p.ByType {
		byType[k] = int64(v)
	}
	return &gardenv1.ProcessorStats{
		Received:      int64(p.Received),
		Applied:       int64(p.Applied),
		NoEffect:      int64(p.NoEffect),
		Duplicates:    int64(p.Duplicates),
		Rejected:      int64(p.Rejected),
		UnknownEntity: int64(p.UnknownEntity),
		ByType:        byType,
	}
}

// timestampOf leaves a zero time unset rather than encoding year 1. A run that
// has not finished has no finished_at, and the wire should say so.
func timestampOf(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// Event maps the durable envelope onto the wire.
//
// RecordedAt is dropped rather than translated: it is wall-clock time, it is
// never written to the log, and a record read back has none to report. See
// docs/events.md.
func Event(e event.Event) *gardenv1.Event {
	return &gardenv1.Event{
		EventId:       e.EventID,
		EventType:     string(e.Type),
		SchemaVersion: int32(e.SchemaVersion),
		RunId:         e.RunID,
		EntityId:      e.EntityID,
		PartitionKey:  e.PartitionKey,
		Sequence:      e.Sequence,
		OccurredAt:    e.OccurredAt,
		Attempt:       int32(e.Attempt),
		Payload: &gardenv1.EventPayload{
			Amount:   int32(e.Payload.Amount),
			Revision: int32(e.Payload.Revision),
		},
	}
}

// Events maps a batch, preserving log order.
func Events(in []event.Event) []*gardenv1.Event {
	out := make([]*gardenv1.Event, 0, len(in))
	for _, e := range in {
		out = append(out, Event(e))
	}
	return out
}

// SnapshotFrame is one projection frame carrying a garden.
func SnapshotFrame(runID string, s engine.GardenSnapshot) *gardenv1.ProjectionFrame {
	return &gardenv1.ProjectionFrame{
		Type:     gardenv1.FrameType_FRAME_TYPE_SNAPSHOT,
		RunId:    runID,
		Snapshot: Snapshot(s),
	}
}

// CatchupFrame is the gap a resuming client missed. `to` is derived from the
// records rather than passed in, so the bound a client checks against cannot
// disagree with what the frame actually carries.
func CatchupFrame(runID string, from int64, missed []event.Event) *gardenv1.ProjectionFrame {
	return &gardenv1.ProjectionFrame{
		Type:  gardenv1.FrameType_FRAME_TYPE_CATCHUP,
		RunId: runID,
		Catchup: &gardenv1.Catchup{
			From:   from,
			To:     from + int64(len(missed)),
			Events: Events(missed),
		},
	}
}
