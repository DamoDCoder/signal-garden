// Package service adapts the run engine to the generated gRPC surface.
//
// It holds no run state and makes no simulation decisions. Its whole job is
// dispatch: unwrap a request, call the engine, and turn an engine error into a
// gRPC status code. The message translation itself lives in internal/wire,
// which the projection stream shares, so both transports describe a garden the
// same way. Anything that looks like a rule belongs in internal/engine or
// internal/domain, where it can be tested without a transport.
package service

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/damodbear/signal-garden/internal/domain"
	"github.com/damodbear/signal-garden/internal/engine"
	gardenv1 "github.com/damodbear/signal-garden/internal/gen/signal/garden/v1"
	"github.com/damodbear/signal-garden/internal/wire"
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
		Controls:       wire.ControlsFrom(req.GetControls()),
		TickInterval:   req.GetTickInterval().AsDuration(),
		MaxTicks:       req.GetMaxTicks(),
		DuplicateEvery: int(req.GetDuplicateEvery()),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return wire.Run(run), nil
}

// GetRun returns run metadata.
func (g *Garden) GetRun(_ context.Context, req *gardenv1.GetRunRequest) (*gardenv1.Run, error) {
	run, err := g.runs.GetRun(req.GetRunId())
	if err != nil {
		return nil, toStatus(err)
	}
	return wire.Run(run), nil
}

// UpdateControls stages a control change and returns its revision. The change
// takes effect at the tick named in the response, never partway through one.
func (g *Garden) UpdateControls(_ context.Context, req *gardenv1.UpdateControlsRequest) (*gardenv1.ControlRevision, error) {
	if req.GetControls() == nil {
		return nil, status.Error(codes.InvalidArgument, "controls are required")
	}
	rev, err := g.runs.UpdateControls(req.GetRunId(), wire.ControlsFrom(req.GetControls()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &gardenv1.ControlRevision{
		RunId:         rev.RunID,
		Revision:      int32(rev.Revision),
		Controls:      wire.Controls(rev.Controls),
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
	return wire.Run(run), nil
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
		Run:       wire.Run(summary.Run),
		Snapshot:  wire.Snapshot(summary.Snapshot),
		Telemetry: wire.Telemetry(summary.Telemetry),
	}, nil
}

// GetSnapshot returns the current projection frame.
func (g *Garden) GetSnapshot(_ context.Context, req *gardenv1.GetSnapshotRequest) (*gardenv1.GardenSnapshot, error) {
	snap, err := g.runs.GetSnapshot(req.GetRunId())
	if err != nil {
		return nil, toStatus(err)
	}
	return wire.Snapshot(snap), nil
}

// GetTelemetry returns the current counters.
func (g *Garden) GetTelemetry(_ context.Context, req *gardenv1.GetTelemetryRequest) (*gardenv1.TelemetrySnapshot, error) {
	tel, err := g.runs.GetTelemetry(req.GetRunId())
	if err != nil {
		return nil, toStatus(err)
	}
	return wire.Telemetry(tel), nil
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
