package vsphere_test

import (
	"context"
	"testing"
	"time"
)

// TestClockOffsetOfASameHostSimulatorIsNearZero: the simulator shares the
// test process's clock, so the measured offset is bounded by the round trip.
func TestClockOffsetOfASameHostSimulatorIsNearZero(t *testing.T) {
	c, _ := newSimulator(t, nil)
	offset, err := c.ClockOffset(context.Background())
	if err != nil {
		t.Fatalf("ClockOffset: %v", err)
	}
	if offset < -time.Second || offset > time.Second {
		t.Fatalf("offset of a same-host simulator = %v, want about zero", offset)
	}
}
