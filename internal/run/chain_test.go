package run

import (
	"testing"

	"github.com/damodbear/signal-garden/internal/domain"
)

// The determinism gate. Same seed, same chain — and an absorbed run fails
// rather than passes, because two absorbed runs agree for reasons that have
// nothing to do with determinism.
//
// This is the check that replaced comparing terminal garden hashes. See
// docs/decisions/0008.
func TestSameSeedProducesSameChain(t *testing.T) {
	cfg := baseConfig()

	first := mustExecute(t, cfg)
	if first.Absorbed {
		t.Fatal("the reference run is absorbed, so its chain proves nothing; shorten the run or soften the controls")
	}
	if first.ChainSteps == 0 {
		t.Fatal("the chain folded nothing")
	}

	for i := range 20 {
		got := mustExecute(t, cfg)
		if got.Chain != first.Chain {
			t.Fatalf("run %d chain = %s, want %s", i, got.Chain, first.Chain)
		}
		if got.ChainSteps != first.ChainSteps {
			t.Fatalf("run %d folded %d records, want %d", i, got.ChainSteps, first.ChainSteps)
		}
	}
}

// Go randomizes map iteration per range statement, so a projection can disagree
// with itself without the process ever restarting. Repeating in-process is the
// cheapest place to catch the one determinism bug an import checker cannot see.
func TestChainAgreesWithinOneProcess(t *testing.T) {
	cfg := baseConfig()
	cfg.Ticks = 60
	cfg.Organisms = 50

	if a, b := mustExecute(t, cfg), mustExecute(t, cfg); a.Chain != b.Chain {
		t.Errorf("two runs in one process disagreed:\n a = %s\n b = %s", a.Chain, b.Chain)
	}
}

func TestDifferentSeedProducesDifferentChain(t *testing.T) {
	a := mustExecute(t, baseConfig())

	cfg := baseConfig()
	cfg.Seed = 43
	b := mustExecute(t, cfg)

	if a.Chain == b.Chain {
		t.Error("different seeds produced identical chains; the seed is not reaching the producer")
	}
}

// This is the finding decision 0008 exists for, reproduced as a test.
//
// Two runs from different seeds have genuinely different histories. Run them
// under a pest-only mix for long enough and every organism dies with zero
// moisture and zero stage, which is the same garden either way — so the terminal
// hash says they agree. The chain, which folded each record as it went, does
// not.
//
// The uncomfortable part is that the longer, more thorough-looking run is the
// one that proves nothing. If this ever fails because the snapshots differ, the
// garden has gained a way to remember its history after death and the whole
// hazard has changed shape.
func TestTerminalHashAgreesWhereTheChainDoesNot(t *testing.T) {
	pestOnly := domain.Controls{EventsPerTick: 20, RainWeight: 0, GrowthWeight: 0, PestWeight: 1}

	config := func(seed int64) Config {
		cfg := baseConfig()
		cfg.Seed = seed
		cfg.Ticks = 400
		cfg.Controls = pestOnly
		return cfg
	}

	a, b := mustExecute(t, config(42)), mustExecute(t, config(43))

	if a.Garden.Alive != 0 || b.Garden.Alive != 0 {
		t.Fatalf("the gardens are still alive (%d and %d); this test is not reaching an absorbing state",
			a.Garden.Alive, b.Garden.Alive)
	}
	if a.Snapshot != b.Snapshot {
		t.Fatalf("two absorbed gardens have different hashes:\n a = %s\n b = %s\nthe absorbing state no longer erases history",
			a.Snapshot, b.Snapshot)
	}
	if a.Chain == b.Chain {
		t.Fatal("two different histories produced the same chain; the chain is not evidence of anything")
	}
	if !a.Absorbed || !b.Absorbed {
		t.Errorf("runs that killed every organism were not reported absorbed (%v, %v)", a.Absorbed, b.Absorbed)
	}
	t.Logf("both runs ended at snapshot %s; only the chain tells them apart", a.Snapshot)
}

// A run long enough to kill the garden is one whose digest stops being evidence.
// The gate has to notice, or "run it for longer" silently weakens every
// determinism test.
func TestLongRunIsReportedAbsorbed(t *testing.T) {
	cfg := baseConfig()
	cfg.Ticks = 400
	cfg.Controls = domain.Controls{EventsPerTick: 20, RainWeight: 0, GrowthWeight: 0, PestWeight: 1}

	got := mustExecute(t, cfg)
	if got.Garden.Alive != 0 {
		t.Fatalf("%d organisms survived a 400-tick pest storm; this test is not reaching an absorbing state", got.Garden.Alive)
	}
	if !got.Absorbed {
		t.Errorf("a garden with nothing alive was not reported absorbed after %d steps", got.ChainSteps)
	}
}

// Duplicate delivery must not move the chain differently from the garden: a
// redelivered record is dropped by its idempotency key, so it folds the same
// projection digest as the record before it.
func TestDuplicateDeliveryKeepsTheChainHonest(t *testing.T) {
	clean := mustExecute(t, baseConfig())

	noisy := baseConfig()
	noisy.DuplicateEvery = 2
	dup := mustExecute(t, noisy)

	if dup.Snapshot != clean.Snapshot {
		t.Errorf("duplicate delivery changed the garden:\n clean = %s\n noisy = %s", clean.Snapshot, dup.Snapshot)
	}
	if dup.ChainSteps <= clean.ChainSteps {
		t.Errorf("duplicated run folded %d records, clean folded %d; the duplicates never reached the log",
			dup.ChainSteps, clean.ChainSteps)
	}
	if dup.Chain == clean.Chain {
		t.Error("a run with extra records produced the same chain; the chain is not folding every record")
	}
}
