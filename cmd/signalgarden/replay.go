package main

import (
	"fmt"
	"io"

	"github.com/damodbear/signal-garden/internal/eventlog"
	"github.com/damodbear/signal-garden/internal/sim"
)

// runReplay rebuilds a run's garden from its log and prints what it found.
//
// This is the claim M2 exists to make, run as a command rather than asserted in
// a test: a run is its history, and folding that history offline reaches the
// garden the live run was showing. The snapshot is only a shortcut past records
// already folded — delete it and this prints the same garden, more slowly.
func runReplay(w io.Writer, root, runID string) error {
	runLog, recovery, err := eventlog.OpenDir(root, runID)
	if err != nil {
		return err
	}
	defer runLog.Close()

	if recovery.Corrupt != nil {
		// Replay reports rather than refuses. Refusing is the daemon's
		// policy because it is about to serve the result; a person reading
		// a damaged log wants to see what is left of it.
		fmt.Fprintf(w, "warning: this log is damaged. %d bytes were discarded and it now ends at offset %d.\n         %v\n\n",
			recovery.Discarded, recovery.Next, recovery.Corrupt)
	}
	if runLog.Next() == 0 {
		return fmt.Errorf("run %s has no records under %s", runID, eventlog.RunDir(root, runID))
	}

	garden, snapshot, err := sim.Rebuild(runLog)
	if err != nil {
		return err
	}

	committed, err := runLog.Committed()
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "run        %s\n", runID)
	fmt.Fprintf(w, "history    %s\n", eventlog.RunDir(root, runID))
	fmt.Fprintf(w, "records    %d in the log, %d committed by the projections group\n", runLog.Next(), committed)
	fmt.Fprintf(w, "tick       %d\n", snapshot.Tick)
	fmt.Fprintf(w, "revision   %d\n\n", snapshot.Revision)

	stats := garden.Stats()
	fmt.Fprintf(w, "alive      %d/%d\n", stats.Alive, stats.Organisms)
	fmt.Fprintf(w, "health     %.1f avg\n", stats.AverageHP)
	fmt.Fprintf(w, "moisture   %.1f avg\n", stats.AverageMoist)
	fmt.Fprintf(w, "stage      %.2f avg (%d total)\n\n", stats.AverageStage, stats.TotalStage)

	fmt.Fprintf(w, "received   %d\n", snapshot.Processor.Received)
	fmt.Fprintf(w, "applied    %d\n", snapshot.Processor.Applied)
	fmt.Fprintf(w, "duplicates %d (dropped by idempotency key)\n\n", snapshot.Processor.Duplicates)

	fmt.Fprintf(w, "snapshot   %s\n", garden.Hash())
	if snapshot.Chain != "" {
		fmt.Fprintf(w, "chain      %s (%d steps, at the snapshot)\n", snapshot.Chain, snapshot.ChainSteps)
	}
	fmt.Fprintln(w, "\nThis garden was rebuilt from the log, not from anything the run kept in memory.")
	return nil
}
