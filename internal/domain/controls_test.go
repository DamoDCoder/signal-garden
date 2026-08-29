package domain

import (
	"errors"
	"testing"
)

func TestControlsValidate(t *testing.T) {
	valid := DefaultControls()

	tests := []struct {
		name    string
		mutate  func(*Controls)
		wantErr bool
	}{
		{"defaults are valid", func(*Controls) {}, false},
		{"rate of one is valid", func(c *Controls) { c.EventsPerTick = 1 }, false},
		{"max rate is valid", func(c *Controls) { c.EventsPerTick = MaxEventsPerTick }, false},
		{"single non-zero weight is valid", func(c *Controls) {
			c.RainWeight, c.GrowthWeight, c.PestWeight = 1, 0, 0
		}, false},
		{"zero worker count and batch size is valid (unbounded)", func(c *Controls) {
			c.WorkerCount, c.BatchSize = 0, 0
		}, false},
		{"max worker count is valid", func(c *Controls) { c.WorkerCount = MaxWorkerCount }, false},
		{"max batch size is valid", func(c *Controls) { c.BatchSize = MaxBatchSize }, false},

		{"zero rate rejected", func(c *Controls) { c.EventsPerTick = 0 }, true},
		{"negative rate rejected", func(c *Controls) { c.EventsPerTick = -1 }, true},
		{"rate above max rejected", func(c *Controls) { c.EventsPerTick = MaxEventsPerTick + 1 }, true},
		{"negative rain weight rejected", func(c *Controls) { c.RainWeight = -1 }, true},
		{"negative growth weight rejected", func(c *Controls) { c.GrowthWeight = -1 }, true},
		{"negative pest weight rejected", func(c *Controls) { c.PestWeight = -1 }, true},
		{"all-zero mix rejected", func(c *Controls) {
			c.RainWeight, c.GrowthWeight, c.PestWeight = 0, 0, 0
		}, true},
		{"negative worker count rejected", func(c *Controls) { c.WorkerCount = -1 }, true},
		{"worker count above max rejected", func(c *Controls) { c.WorkerCount = MaxWorkerCount + 1 }, true},
		{"negative batch size rejected", func(c *Controls) { c.BatchSize = -1 }, true},
		{"batch size above max rejected", func(c *Controls) { c.BatchSize = MaxBatchSize + 1 }, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.mutate(&c)
			err := c.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Validate() = nil, want error for %+v", c)
				}
				if !errors.Is(err, ErrInvalidControls) {
					t.Errorf("error %v does not wrap ErrInvalidControls", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil for %+v", err, c)
			}
		})
	}
}

// TestControlsRejectionIsConsistent covers the M0 exit criterion that invalid
// controls are rejected consistently: the same input must fail the same way
// every time, not intermittently.
func TestControlsRejectionIsConsistent(t *testing.T) {
	invalid := Controls{EventsPerTick: 0, RainWeight: 1}

	first := invalid.Validate()
	if first == nil {
		t.Fatal("expected an error on the first call")
	}
	for i := 0; i < 100; i++ {
		got := invalid.Validate()
		if got == nil {
			t.Fatalf("call %d returned nil; rejection must be deterministic", i)
		}
		if got.Error() != first.Error() {
			t.Fatalf("call %d error = %q, want %q", i, got, first)
		}
	}
}

func TestTotalWeight(t *testing.T) {
	c := Controls{RainWeight: 3, GrowthWeight: 2, PestWeight: 1}
	if got := c.TotalWeight(); got != 6 {
		t.Errorf("TotalWeight() = %d, want 6", got)
	}
}

func TestCapacity(t *testing.T) {
	tests := []struct {
		name        string
		workerCount int
		batchSize   int
		want        int
	}{
		{"both zero is unbounded", 0, 0, 0},
		{"worker count zero is unbounded", 0, 5, 0},
		{"batch size zero is unbounded", 5, 0, 0},
		{"both positive multiplies", 2, 3, 6},
		{"negative worker count is unbounded", -1, 5, 0},
		{"negative batch size is unbounded", 5, -1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Controls{WorkerCount: tc.workerCount, BatchSize: tc.batchSize}
			if got := c.Capacity(); got != tc.want {
				t.Errorf("Capacity() = %d, want %d", got, tc.want)
			}
		})
	}
}
