package vsphere_test

import (
	"context"
	"testing"
	"time"

	"github.com/vmware/govmomi/simulator"

	"github.com/easonliuuuuu/vsfleet/internal/perf"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// The simulator's statistics are synthetic, so this checks the wire path and
// the shape of the result — never the values.
func TestCollectPerfAgainstSimulator(t *testing.T) {
	c, _ := newSimulator(t, func(m *simulator.Model) { m.Machine = 3 })
	ctx := context.Background()
	idx, err := c.NewIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inv := c.FetchGroup(ctx, idx, vsphere.GroupVMs)
	if len(inv.VMs) == 0 {
		t.Fatal("simulator returned no VMs")
	}
	w := c.CollectPerf(ctx, inv.VMs, vsphere.PerfOptions{Window: 24 * time.Hour})
	if w.Status == perf.WindowFailed {
		t.Fatalf("collection failed: %s", w.Error)
	}
	if w.IntervalSeconds <= 0 || w.ExpectedSamples <= 0 || w.RequestsUsed == 0 || w.Source == "" {
		t.Fatalf("window provenance incomplete: %+v", w)
	}
	if w.VMsSampled != len(inv.VMs) || len(w.VMs) != len(inv.VMs) {
		t.Fatalf("sampled %d, rows %d, want %d", w.VMsSampled, len(w.VMs), len(inv.VMs))
	}
	got := 0
	for _, vm := range w.VMs {
		for _, s := range vm.Summaries {
			got += s.Successful
		}
	}
	if got == 0 {
		t.Fatalf("no samples came back from the simulator: %+v", w.VMs[0].Summaries)
	}
	for _, vm := range w.VMs {
		if len(vm.Summaries) != len(perf.Counters) {
			t.Fatalf("%s has %d summaries, want %d", vm.Name, len(vm.Summaries), len(perf.Counters))
		}
		if vm.Signal == "" {
			t.Fatalf("%s has no signal", vm.Name)
		}
	}
}
