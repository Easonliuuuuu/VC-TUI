package tests

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vmware/govmomi/simulator"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
)

// TestAssessmentRunStoresEachContextsClockOffset: a capture measures every
// reachable vCenter's clock against the local one and stores it with the run,
// under the context's name. A simulator shares this process's clock, so the
// offsets are bounded by a round trip; the unreachable context has none, and
// that leaves the run partial rather than failing it any further.
func TestAssessmentRunStoresEachContextsClockOffset(t *testing.T) {
	t.Setenv("VSFLEET_E2E_PASSWORD", testPassword)
	tune := func(m *simulator.Model) { m.Datacenter, m.Cluster, m.ClusterHost, m.Machine = 1, 1, 1, 1 }
	r := newRunner(t)
	r.addNonInteractiveContext("alpha", startVCenter(t, tune), "env:VSFLEET_E2E_PASSWORD")
	r.addNonInteractiveContext("beta", startVCenter(t, tune), "env:VSFLEET_E2E_PASSWORD")
	addUnreachableContext(r, "down")
	historyDB := filepath.Join(t.TempDir(), "history.db")
	// The run is partial because of the unreachable context.
	_, _, _ = r.run("", "--history-db", historyDB, "assessment", "run", "--all-contexts")

	store, err := assessment.Open(historyDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run, err := store.GetRun(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		offset, ok := run.ClockOffset(name)
		if !ok {
			t.Fatalf("no clock offset stored for %s: %+v", name, run.ClockOffsetsMS)
		}
		if offset < -time.Second || offset > time.Second {
			t.Errorf("offset for %s = %v, want about zero for a simulator on this host", name, offset)
		}
	}
	if _, ok := run.ClockOffset("down"); ok {
		t.Errorf("an unreachable context has a clock offset: %+v", run.ClockOffsetsMS)
	}
}
