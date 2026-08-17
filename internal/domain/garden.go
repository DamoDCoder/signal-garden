package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/DamoDCoder/event-spine/core"
)

// Organism bounds. Health and moisture are percentages; stage is a discrete
// growth step so the CLI and, later, the UI can render a shape rather than a
// float.
const (
	MaxMoisture = 100
	MaxHealth   = 100
	MaxStage    = 5
)

// Organism is one plant in the garden.
type Organism struct {
	ID       string `json:"id"`
	Moisture int    `json:"moisture"`
	Health   int    `json:"health"`
	Stage    int    `json:"stage"`
}

// Alive reports whether the organism can still respond to events. A dead
// organism absorbs rain and pest events without effect, which keeps event
// counts honest rather than silently dropping deliveries.
func (o Organism) Alive() bool {
	return o.Health > 0
}

// Garden is the projection the processor owns. Organisms are held in a slice so
// iteration order is deterministic; index maps lookup by ID without ever being
// ranged over.
type Garden struct {
	organisms []Organism
	index     map[string]int
}

// NewGarden creates a garden of n organisms at full health, no moisture, and
// stage zero. IDs are stable and zero-padded so they sort lexically.
func NewGarden(n int) (*Garden, error) {
	if n < 1 {
		return nil, fmt.Errorf("garden needs at least 1 organism, got %d", n)
	}
	g := &Garden{
		organisms: make([]Organism, n),
		index:     make(map[string]int, n),
	}
	for i := range g.organisms {
		id := OrganismID(i)
		g.organisms[i] = Organism{ID: id, Health: MaxHealth}
		g.index[id] = i
	}
	return g, nil
}

// Restore rebuilds a garden from organisms read out of a snapshot.
//
// It is the inverse of Organisms, and it exists because a garden is the fold of
// a history: a snapshot is a shortcut past the records already folded, not a
// second source of truth. The IDs are checked against the positions they would
// have had, so a snapshot written by a different build cannot quietly become a
// garden whose organisms are in the wrong order.
func Restore(organisms []Organism) (*Garden, error) {
	if len(organisms) == 0 {
		return nil, fmt.Errorf("garden needs at least 1 organism, got 0")
	}
	g := &Garden{
		organisms: make([]Organism, len(organisms)),
		index:     make(map[string]int, len(organisms)),
	}
	for i, o := range organisms {
		if want := OrganismID(i); o.ID != want {
			return nil, fmt.Errorf("organism %d has id %q, want %q", i, o.ID, want)
		}
		g.organisms[i] = o
		g.index[o.ID] = i
	}
	return g, nil
}

// OrganismID returns the stable ID for the organism at position i.
func OrganismID(i int) string {
	return fmt.Sprintf("org-%03d", i)
}

// Len returns the organism count.
func (g *Garden) Len() int { return len(g.organisms) }

// Organisms returns a copy of the organism slice, so callers cannot mutate
// projection state without going through the rules.
func (g *Garden) Organisms() []Organism {
	out := make([]Organism, len(g.organisms))
	copy(out, g.organisms)
	return out
}

// Get returns the organism with the given ID.
func (g *Garden) Get(id string) (Organism, bool) {
	i, ok := g.index[id]
	if !ok {
		return Organism{}, false
	}
	return g.organisms[i], true
}

// Stats summarizes garden condition for the projection and the run scorecard.
type Stats struct {
	Organisms    int     `json:"organisms"`
	Alive        int     `json:"alive"`
	AverageMoist float64 `json:"average_moisture"`
	AverageHP    float64 `json:"average_health"`
	AverageStage float64 `json:"average_stage"`
	TotalStage   int     `json:"total_stage"`
}

// Stats computes the current garden summary.
func (g *Garden) Stats() Stats {
	s := Stats{Organisms: len(g.organisms)}
	if len(g.organisms) == 0 {
		return s
	}
	var moist, hp, stage int
	for _, o := range g.organisms {
		if o.Alive() {
			s.Alive++
		}
		moist += o.Moisture
		hp += o.Health
		stage += o.Stage
	}
	n := float64(len(g.organisms))
	s.AverageMoist = float64(moist) / n
	s.AverageHP = float64(hp) / n
	s.AverageStage = float64(stage) / n
	s.TotalStage = stage
	return s
}

// Digest returns a stable fingerprint of garden state.
//
// It is what the determinism chain folds after every event, so it has to be
// cheap and it has to depend on nothing but the organisms. Iteration is over
// the slice rather than the index map, because ranging a map here would make
// the digest depend on Go's hash seed — the one determinism bug an import
// checker cannot see.
func (g *Garden) Digest() core.Digest {
	h := sha256.New()
	for _, o := range g.organisms {
		fmt.Fprintf(h, "%s|%d|%d|%d\n", o.ID, o.Moisture, o.Health, o.Stage)
	}
	var d core.Digest
	copy(d[:], h.Sum(nil))
	return d
}

// Hash returns the digest as hex, which is what the snapshot frame carries and
// what a client can compare cheaply.
//
// It is a fingerprint of where the garden ended up, and on its own that is weak
// evidence of determinism: a garden whose organisms are all dead folds every
// history to the same value. The chain in docs/decisions/0008 is what the
// replay tests compare.
func (g *Garden) Hash() string {
	d := g.Digest()
	return hex.EncodeToString(d[:])
}
