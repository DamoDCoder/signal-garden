// Package service adapts the run engine to the generated gRPC surface.
//
// It holds no run state and makes no simulation decisions. Its whole job is
// translation: proto messages to engine types on the way in, engine types to
// proto on the way out, and engine errors to gRPC status codes. Anything that
// looks like a rule belongs in internal/engine or internal/domain, where it can
// be tested without a transport.
package service

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/engine"
	"github.com/damodbear/signal-garden/internal/event"
	gardenv1 "github.com/damodbear/signal-garden/internal/gen/signal/garden/v1"
	"github.com/damodbear/signal-garden/internal/processor"
)

// Garden serves GardenService over an engine registry.
type Garden struct {
	gardenv1.UnimplementedGardenServiceServer

	runs *engine.Registry
}

// New returns a service backed by the given registry.
func New(runs *engine.Registry) *Garden {
	return &Garden{runs: runs}
}

// StartRun creates a run and begins ticking it.
func (g *Garden) StartRun(_ context.Context, req *gardenv1.StartRunRequest) (*gardenv1.Run, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	run, err := g.runs.StartRun(engine.StartRunRequest{
		RunID:          req.GetRunId(),
		Seed:           req.GetSeed(),
		Organisms:      int(req.GetOrganisms()),
		Controls:       controlsFromProto(req.GetControls()),
		TickInterval:   req.GetTickInterval().AsDuration(),
		MaxTicks:       req.GetMaxTicks(),
		DuplicateEvery: int(req.GetDuplicateEvery()),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return runToProto(run), nil
}

// GetRun returns run metadata.
func (g *Garden) GetRun(_ context.Context, req *gardenv1.GetRunRequest) (*gardenv1.Run, error) {
	run, err := g.runs.GetRun(req.GetRunId())
	if err != nil {
		return nil, toStatus(err)
	}
	return runToProto(run), nil
}

// UpdateControls stages a control change and returns its revision. The change
// takes effect at the tick named in the response, never partway through one.
func (g *Garden) UpdateControls(_ context.Context, req *gardenv1.UpdateControlsRequest) (*gardenv1.ControlRevision, error) {
	if req.GetControls() == nil {
		return nil, status.Error(codes.InvalidArgument, "controls are required")
	}
	rev, err := g.runs.UpdateControls(req.GetRunId(), controlsFromProto(req.GetControls()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &gardenv1.ControlRevision{
		RunId:         rev.RunID,
		Revision:      int32(rev.Revision),
		Controls:      controlsToProto(rev.Controls),
		EffectiveTick: rev.EffectiveTick,
	}, nil
}

// PauseRun pauses or resumes a run, depending on the requested state.
func (g *Garden) PauseRun(_ context.Context, req *gardenv1.PauseRunRequest) (*gardenv1.Run, error) {
	var (
		run engine.Run
		err error
	)
	if req.GetPaused() {
		run, err = g.runs.PauseRun(req.GetRunId())
	} else {
		run, err = g.runs.ResumeRun(req.GetRunId())
	}
	if err != nil {
		return nil, toStatus(err)
	}
	return runToProto(run), nil
}

// FinishRun ends a run and returns its summary. Finishing an already finished
// run returns the same summary rather than an error, so a retried request is
// harmless.
func (g *Garden) FinishRun(_ context.Context, req *gardenv1.FinishRunRequest) (*gardenv1.RunSummary, error) {
	summary, err := g.runs.FinishRun(req.GetRunId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &gardenv1.RunSummary{
		Run:       runToProto(summary.Run),
		Snapshot:  snapshotToProto(summary.Snapshot),
		Telemetry: telemetryToProto(summary.Telemetry),
	}, nil
}

// GetSnapshot returns the current projection frame.
func (g *Garden) GetSnapshot(_ context.Context, req *gardenv1.GetSnapshotRequest) (*gardenv1.GardenSnapshot, error) {
	snap, err := g.runs.GetSnapshot(req.GetRunId())
	if err != nil {
		return nil, toStatus(err)
	}
	return snapshotToProto(snap), nil
}

// GetTelemetry returns the current counters.
func (g *Garden) GetTelemetry(_ context.Context, req *gardenv1.GetTelemetryRequest) (*gardenv1.TelemetrySnapshot, error) {
	tel, err := g.runs.GetTelemetry(req.GetRunId())
	if err != nil {
		return nil, toStatus(err)
	}
	return telemetryToProto(tel), nil
}

// toStatus maps engine errors to gRPC codes.
//
// The mapping is the reason engine errors are sentinel values rather than
// formatted strings: a client needs to tell "no such run" from "that run is
// over" from "those controls are nonsense", and matching on message text would
// make every error message part of the contract.
func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, engine.ErrRunNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, engine.ErrRunExists), errors.Is(err, engine.ErrRunHasHistory):
		// A live run and a finished run's history are the same answer to
		// the client: that name is taken, choose another or omit it.
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, engine.ErrRunFinished):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, engine.ErrCorruptLog):
		// The disk returned bytes that were wrong. Nothing the caller
		// sent caused it and retrying will not fix it.
		return status.Error(codes.DataLoss, err.Error())
	case errors.Is(err, engine.ErrRegistryDown), errors.Is(err, engine.ErrRunClosed):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, domain.ErrInvalidControls):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		// StartRun's own validation failures land here. They are caller
		// mistakes, not server faults, so InvalidArgument is right for
		// everything the engine rejects before a run exists.
		return status.Error(codes.InvalidArgument, err.Error())
	}
}

func controlsFromProto(c *gardenv1.Controls) domain.Controls {
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

func controlsToProto(c domain.Controls) *gardenv1.Controls {
	return &gardenv1.Controls{
		EventsPerTick: int32(c.EventsPerTick),
		RainWeight:    int32(c.RainWeight),
		GrowthWeight:  int32(c.GrowthWeight),
		PestWeight:    int32(c.PestWeight),
	}
}

func stateToProto(s engine.State) gardenv1.RunState {
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

func runToProto(r engine.Run) *gardenv1.Run {
	return &gardenv1.Run{
		RunId:         r.RunID,
		Seed:          r.Seed,
		Organisms:     int32(r.Organisms),
		State:         stateToProto(r.State),
		Tick:          r.Tick,
		MaxTicks:      r.MaxTicks,
		TickInterval:  durationpb.New(r.TickInterval),
		Controls:      controlsToProto(r.Controls),
		Revision:      int32(r.Revision),
		StartedAt:     timestampOf(r.StartedAt),
		UpdatedAt:     timestampOf(r.UpdatedAt),
		FinishedAt:    timestampOf(r.FinishedAt),
		Failure:       r.Failure,
		SchemaVersion: event.SchemaVersion,
	}
}

func snapshotToProto(s engine.GardenSnapshot) *gardenv1.GardenSnapshot {
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
		State:    stateToProto(s.State),
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
	}
}

func telemetryToProto(t engine.TelemetrySnapshot) *gardenv1.TelemetrySnapshot {
	return &gardenv1.TelemetrySnapshot{
		RunId:            t.RunID,
		State:            stateToProto(t.State),
		Tick:             t.Tick,
		Revision:         int32(t.Revision),
		TickInterval:     durationpb.New(t.TickInterval),
		Published:        int64(t.Published),
		Processor:        processorToProto(t.Processor),
		Pending:          int64(t.Pending),
		Subscribers:      int32(t.Subscribers),
		SnapshotsSent:    t.SnapshotsSent,
		SnapshotsDropped: t.SnapshotsDropped,
		Uptime:           durationpb.New(t.Uptime),
		ObservedAt:       timestampOf(t.ObservedAt),
		SchemaVersion:    event.SchemaVersion,
	}
}

func processorToProto(p processor.Stats) *gardenv1.ProcessorStats {
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
