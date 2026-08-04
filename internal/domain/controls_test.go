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

		{"zero rate rejected", func(c *Controls) { c.EventsPerTick = 0 }, true},
		{"negative rate rejected", func(c *Controls) { c.EventsPerTick = -1 }, true},
		{"rate above max rejected", func(c *Controls) { c.EventsPerTick = MaxEventsPerTick + 1 }, true},
		{"negative rain weight rejected", func(c *Controls) { c.RainWeight = -1 }, true},
		{"negative growth weight rejected", func(c *Controls) { c.GrowthWeight = -1 }, true},
		{"negative pest weight rejected", func(c *Controls) { c.PestWeight = -1 }, true},
		{"all-zero mix rejected", func(c *Controls) {
			c.RainWeight, c.GrowthWeight, c.PestWeight = 0, 0, 0
		}, true},
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
