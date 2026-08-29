package sim

import (
	"testing"

	spinesim "github.com/DamoDCoder/event-spine/sim"

	"github.com/damodbear/signal-garden/internal/eventlog"
)

// onDisk builds a sim over a filesystem the test keeps, so the run can be closed
// and its log reopened the way a restart does.
func onDisk(t *testing.T, fs *spinesim.FS, snapshotEvery int64) *Sim {
	t.Helper()
	runLog, recovery, err := eventlog.Open(fs)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if recovery.Corrupt != nil {
		t.Fatalf("a fresh log opened corrupt: %v", recovery.Corrupt)
	}

	cfg := baseConfig()
	cfg.SnapshotEvery = snapshotEvery
	cfg.Log = runLog

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// TestSaveRetriesOnInjectedFailure is the demoable case FailSnapshotEvery
// exists for: a transient failure that recovers, not a duplicate of the
// existing "the run terminates on a real disk error" path. See
// docs/decisions/0018.
func TestSaveRetriesOnInjectedFailure(t *testing.T) {
	fs := spinesim.NewFS()
	s := onDisk(t, fs, 0) // SnapshotEvery=0: this test calls Save directly
	defer s.Close()

	mustStep(t, s, 3)
	wantHash := s.Hash()

	s.controls.FailSnapshotEvery = 1 // every invocation's first attempt fails
	if err := s.Save(); err != nil {
		t.Fatalf("Save() with an injected failure that should recover: %v", err)
	}
	if got := s.SnapshotSaveRetries(); got == 0 {
		t.Error("SnapshotSaveRetries() = 0, want at least one retry recorded")
	}
	if got := s.SnapshotSaveFailures(); got != 0 {
		t.Errorf("SnapshotSaveFailures() = %d, want 0 — the injected failure always recovers", got)
	}

	// The injected failure must be invisible to correctness: the garden this
	// run reaches is exactly what an unaffected run reaches.
	if got := s.Hash(); got != wantHash {
		t.Errorf("Hash() after a retried save = %s, want %s (unchanged by the retry)", got, wantHash)
	}
}

// TestSaveDoesNotRetryWhenDisabled confirms the default (FailSnapshotEvery=0,
// the value every existing test already uses) never touches the retry path —
// this is what keeps every prior test in this package passing unmodified.
func TestSaveDoesNotRetryWhenDisabled(t *testing.T) {
	s := onDisk(t, spinesim.NewFS(), 0)
	defer s.Close()

	mustStep(t, s, 2)
	if err := s.Save(); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	if got := s.SnapshotSaveRetries(); got != 0 {
		t.Errorf("SnapshotSaveRetries() = %d, want 0 with FailSnapshotEvery disabled", got)
	}
	if got := s.SnapshotSaveFailures(); got != 0 {
		t.Errorf("SnapshotSaveFailures() = %d, want 0 with FailSnapshotEvery disabled", got)
	}
}

// TestSaveRetriesOnCadenceKeepsTheRunGoing exercises the path an injected
// failure actually goes through in a live run: Step's own SnapshotEvery
// cadence, not a direct Save call. A recovered retry must not fail Step or
// the run it belongs to.
func TestSaveRetriesOnCadenceKeepsTheRunGoing(t *testing.T) {
	cfg := baseConfig()
	cfg.SnapshotEvery = 2
	cfg.Controls.FailSnapshotEvery = 1
	s := mustNew(t, cfg)

	mustStep(t, s, 6) // three snapshot-cadence saves, each one's first attempt injected-failed

	if got := s.SnapshotSaveRetries(); got == 0 {
		t.Error("SnapshotSaveRetries() = 0, want retries from the cadence-triggered saves")
	}
	if got := s.SnapshotSaveFailures(); got != 0 {
		t.Errorf("SnapshotSaveFailures() = %d, want 0", got)
	}
	if got := s.Tick(); got != 6 {
		t.Errorf("Tick() = %d, want 6 — a recovered retry must not stall or fail the run", got)
	}
}

// The load-bearing property of a snapshot: it is a shortcut past records already
// folded, never a second source of truth. Rebuilding with one and rebuilding
// without one have to reach the same garden, or the snapshot is state nobody can
// check.
func TestSnapshotMatchesAFullReplay(t *testing.T) {
	fs := spinesim.NewFS()

	s := onDisk(t, fs, 5)
	mustStep(t, s, 23)
	want := s.Hash()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// With the snapshot: restore, then fold the records after it.
	viaSnapshot, _, err := eventlog.Open(fs)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer viaSnapshot.Close()

	garden, snapshot, err := Rebuild(viaSnapshot)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if snapshot.Chain == "" {
		t.Fatal("rebuilt from no snapshot; the cadence never fired and this test proves nothing")
	}
	if got := garden.Hash(); got != want {
		t.Errorf("rebuilt garden = %s, the run ended at %s", got, want)
	}

	// Without it: fold every record from the beginning.
	events, err := viaSnapshot.Replay()
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	full, _, err := Fold(baseConfig().Organisms, events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if got := full.Hash(); got != want {
		t.Errorf("folding the whole history reached %s, the run ended at %s", got, want)
	}
}

// Nothing commits until a snapshot exists, because a commit says those records
// never need delivering again — which is only true once the state built from
// them is on disk.
func TestCommitFollowsTheSnapshot(t *testing.T) {
	fs := spinesim.NewFS()

	s := onDisk(t, fs, 10)
	mustStep(t, s, 4)
	if got, err := s.Log().Committed(); err != nil || got != 0 {
		t.Fatalf("committed = %d, %v before any snapshot; want 0", got, err)
	}

	mustStep(t, s, 6)
	committed, err := s.Log().Committed()
	if err != nil {
		t.Fatalf("Committed: %v", err)
	}
	if committed == 0 {
		t.Fatal("the group never committed after a snapshot was due")
	}
	if committed != s.Log().Read() {
		t.Errorf("committed %d, cursor at %d; a snapshot commits exactly what it folded", committed, s.Log().Read())
	}
}

// A restart resumes at the commit rather than at the beginning, and folding what
// is left on top of the snapshot reaches the garden the run had.
func TestRestartResumesAtTheSnapshot(t *testing.T) {
	fs := spinesim.NewFS()

	s := onDisk(t, fs, 5)
	mustStep(t, s, 12)
	want := s.Hash()
	committed, err := s.Log().Committed()
	if err != nil {
		t.Fatalf("Committed: %v", err)
	}
	total := s.Log().Next()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if committed == 0 || committed >= total {
		t.Fatalf("committed %d of %d records; this test needs a partial commit to be meaningful", committed, total)
	}

	reopened, _, err := eventlog.Open(fs)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	// The group resumes where it committed, so only the uncommitted tail is
	// redelivered — that is the point of committing at all.
	redelivered, err := reopened.Unprocessed()
	if err != nil {
		t.Fatalf("Unprocessed: %v", err)
	}
	if int64(len(redelivered)) != total-committed {
		t.Errorf("redelivered %d records, want the %d above the commit", len(redelivered), total-committed)
	}

	garden, _, err := Rebuild(reopened)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if got := garden.Hash(); got != want {
		t.Errorf("restart reached %s, the run was at %s", got, want)
	}
}

// A log with no snapshot still rebuilds. The snapshot is an optimisation, so
// deleting one has to cost time and nothing else.
func TestRebuildWithoutASnapshot(t *testing.T) {
	fs := spinesim.NewFS()

	s := onDisk(t, fs, 0)
	mustStep(t, s, 9)
	want := s.Hash()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, _, err := eventlog.Open(fs)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	garden, snapshot, err := Rebuild(reopened)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if snapshot.Chain != "" {
		t.Error("a run with snapshotting off left a snapshot behind")
	}
	if got := garden.Hash(); got != want {
		t.Errorf("rebuilt garden = %s, the run ended at %s", got, want)
	}
}

// Snapshotting must not change what a run computes. If it did, the durable
// shortcut would be steering the simulation.
func TestSnapshotCadenceDoesNotChangeTheRun(t *testing.T) {
	hashes := map[int64]string{}
	chains := map[int64]string{}

	for _, every := range []int64{0, 1, 3, 7, 100} {
		s := onDisk(t, spinesim.NewFS(), every)
		mustStep(t, s, 20)
		hashes[every] = s.Hash()
		chains[every] = s.Chain()
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	for every, hash := range hashes {
		if hash != hashes[0] {
			t.Errorf("snapshotting every %d ticks changed the garden: %s, want %s", every, hash, hashes[0])
		}
		if chains[every] != chains[0] {
			t.Errorf("snapshotting every %d ticks changed the chain", every)
		}
	}
}
