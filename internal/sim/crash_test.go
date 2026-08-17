package sim

import (
	"bytes"
	"fmt"
	"testing"

	spinelog "github.com/DamoDCoder/event-spine/log"
	spinesim "github.com/DamoDCoder/event-spine/sim"

	"github.com/damodbear/signal-garden/internal/event"
	"github.com/damodbear/signal-garden/internal/eventlog"
)

// crashTicks is how far each crash case runs before the power goes. It is small
// because the matrix is the cost: every tick boundary, in three crash shapes,
// in two durability modes.
const crashTicks = 8

// reference runs the same simulation with nothing going wrong, and reports the
// records it wrote plus the garden after each tick.
//
// Every crash case is judged against this: what survived must be a prefix of
// these records, and the garden it folds to must be the garden that prefix
// produces. Anything else is a log that describes a run that never happened.
func reference(t *testing.T) (records [][]byte, countAfterTick []int, hashAfterTick []string) {
	t.Helper()
	s := mustNew(t, baseConfig())

	countAfterTick = make([]int, crashTicks+1)
	hashAfterTick = make([]string, crashTicks+1)
	hashAfterTick[0] = s.Hash()

	for tick := 1; tick <= crashTicks; tick++ {
		if err := s.Step(); err != nil {
			t.Fatalf("Step %d: %v", tick, err)
		}
		countAfterTick[tick] = int(s.Log().Next())
		hashAfterTick[tick] = s.Hash()
	}

	events, err := s.Log().Replay()
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return canonical(t, events), countAfterTick, hashAfterTick
}

func canonical(t *testing.T, events []event.Event) [][]byte {
	t.Helper()
	out := make([][]byte, len(events))
	for i, e := range events {
		rec, err := e.ToCore()
		if err != nil {
			t.Fatalf("ToCore: %v", err)
		}
		out[i] = rec.AppendCanonical(nil)
	}
	return out
}

// crashShapes are the three things a power cut does to a file. The middle one is
// what ext4 actually does, and the spine records that its simulator could not
// produce it for four milestones — a bug hid in the difference.
var crashShapes = map[string]func(*spinesim.FS){
	"nothing unsynced survives": func(fs *spinesim.FS) { fs.Crash() },
	"length kept, zeros in gap": func(fs *spinesim.FS) { fs.CrashExtend() },
	"half the unsynced bytes":   func(fs *spinesim.FS) { fs.CrashTorn(50) },
}

// TestCrashAtEveryTickBoundary is M2's durability criterion.
//
// In sync mode nothing acknowledged may be lost: the run appends one call per
// tick and that call syncs, so a crash between ticks must leave exactly the
// records those ticks wrote. In batch mode records may go, but only from the
// tail — what survives has to be a prefix, because a log missing something from
// the middle would replay into a garden no history explains.
func TestCrashAtEveryTickBoundary(t *testing.T) {
	records, countAfterTick, hashAfterTick := reference(t)

	modes := map[string]spinelog.Durability{
		"sync":  spinelog.Sync,
		"batch": spinelog.Batch,
	}

	// A crash matrix that never loses anything is a matrix that proves
	// nothing, so the batch half is checked for teeth at the end. Subtests
	// here run synchronously, so counting across them needs no locking.
	var batchLosses, partialTicks int

	for modeName, durability := range modes {
		for shapeName, crash := range crashShapes {
			for tick := 1; tick <= crashTicks; tick++ {
				name := fmt.Sprintf("%s/%s/tick-%d", modeName, shapeName, tick)
				t.Run(name, func(t *testing.T) {
					survived, events := runAndCrash(t, durability, crash, tick)

					assertPrefix(t, records, survived)

					if modeName == "sync" {
						if len(survived) != countAfterTick[tick] {
							t.Fatalf("sync mode kept %d of %d acknowledged records",
								len(survived), countAfterTick[tick])
						}
						// Every record is there, so the fold has to
						// be the garden the run was actually showing.
						assertFoldsTo(t, events, hashAfterTick[tick])
					}
					if modeName == "batch" {
						if len(survived) < countAfterTick[tick] {
							batchLosses++
						}
						if !isTickBoundary(countAfterTick, len(survived)) {
							partialTicks++
						}
					}
				})
			}
		}
	}

	if batchLosses == 0 {
		t.Error("batch mode lost nothing anywhere in the matrix; the durability claim above it is untested")
	}
	if partialTicks == 0 {
		t.Error("no crash ever cut a tick in half, so the prefix invariant was only ever checked on whole ticks")
	}
}

// isTickBoundary reports whether a record count is one a tick ended on. A count
// that is not means a crash landed inside a tick's append, which is the case the
// prefix invariant exists for and the reason it is stated over records rather
// than over ticks.
func isTickBoundary(countAfterTick []int, count int) bool {
	for _, c := range countAfterTick {
		if c == count {
			return true
		}
	}
	return false
}

// runAndCrash drives a fresh simulation for the given ticks, cuts the power, and
// returns the canonical records that came back.
func runAndCrash(t *testing.T, durability spinelog.Durability, crash func(*spinesim.FS), ticks int) ([][]byte, []event.Event) {
	t.Helper()
	fs := spinesim.NewFS()

	runLog, _, err := eventlog.OpenWith(fs, spinelog.Config{Durability: durability})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}

	cfg := baseConfig()
	cfg.Log = runLog
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for tick := range ticks {
		if err := s.Step(); err != nil {
			t.Fatalf("Step %d: %v", tick, err)
		}
	}

	crash(fs)
	_ = s.Close()

	reopened, recovery, err := eventlog.Open(fs)
	if err != nil {
		t.Fatalf("reopen after a crash: %v", err)
	}
	defer reopened.Close()

	// Corruption is allowed here — a crash is exactly how it arises — but
	// it must not be silent, and the log must still be readable afterwards.
	if recovery.Corrupt != nil && recovery.Discarded == 0 {
		t.Errorf("corruption reported with nothing discarded: %v", recovery.Corrupt)
	}

	events, err := reopened.Replay()
	if err != nil {
		t.Fatalf("Replay after a crash: %v", err)
	}
	return canonical(t, events), events
}

// assertPrefix is the invariant that matters in every mode. Losing the tail of a
// log is a power cut; losing something from the middle is a log that replays
// into a garden no sequence of events produces.
func assertPrefix(t *testing.T, reference, survived [][]byte) {
	t.Helper()
	if len(survived) > len(reference) {
		t.Fatalf("a crash produced %d records, more than the %d the run wrote", len(survived), len(reference))
	}
	for i := range survived {
		if !bytes.Equal(survived[i], reference[i]) {
			t.Fatalf("record %d of %d differs from the run that wrote it; what survived is not a prefix",
				i, len(survived))
		}
	}
}

func assertFoldsTo(t *testing.T, events []event.Event, wantHash string) {
	t.Helper()
	garden, _, err := Fold(baseConfig().Organisms, events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if got := garden.Hash(); got != wantHash {
		t.Errorf("replaying %d records reached garden %s, want %s", len(events), got, wantHash)
	}
}
