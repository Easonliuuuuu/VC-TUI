package vsphere

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/perf"
)

func TestVMPerfSeriesKeepsEverySampleAndMarksGapsMissing(t *testing.T) {
	f := newFakePerf()
	var spec types.PerfQuerySpec
	f.fault = func(specs []types.PerfQuerySpec) error {
		spec = specs[0]
		return nil
	}
	f.data = func(_ string, key int32, n int) []int64 {
		out := make([]int64, n)
		for i := range out {
			out[i] = 2500 // 25% CPU, or 2500 KB of memory
		}
		out[3] = perf.NoData
		return out
	}
	vm := VM{ID: "vm-7", Name: "web", CPU: 4, MemoryMB: 4096}
	set, err := collectVMSeries(context.Background(), f, vm, time.Hour, RealtimePerfInterval, perfNow)
	if err != nil {
		t.Fatal(err)
	}
	if f.requests != 1 || len(spec.MetricId) != len(perf.Counters) || spec.IntervalId != RealtimePerfInterval || spec.MaxSample != 180 {
		t.Fatalf("query = %d requests, spec %+v; want one realtime request for 180 samples of every counter", f.requests, spec)
	}
	cpu, ok := set.Get(perf.CPUUsage)
	if !ok || len(cpu.Values) != 180 {
		t.Fatalf("cpu series = %+v", cpu)
	}
	if cpu.Values[0] != 25 || !math.IsNaN(cpu.Values[3]) {
		t.Fatalf("cpu values[0], [3] = %v, %v; want 25 and NaN for the missing sample", cpu.Values[0], cpu.Values[3])
	}
	if cpu.Summary.Status != perf.StatusOK || *cpu.Summary.Peak != 25 {
		t.Fatalf("cpu summary = %+v", cpu.Summary)
	}
	if !set.WindowEnd.Equal(perfNow) || !set.WindowStart.Equal(perfNow.Add(-time.Hour)) {
		t.Fatalf("window = %s..%s", set.WindowStart, set.WindowEnd)
	}
	if set.Signal == "" {
		t.Fatal("signal was not classified")
	}
}

func TestVMPerfSeriesReportsCountersWithoutSamplesAsUnavailable(t *testing.T) {
	f := newFakePerf()
	f.data = func(_ string, key int32, n int) []int64 {
		if key == f.keys["cpu.usage.average"] {
			return nil
		}
		return make([]int64, n)
	}
	set, err := collectVMSeries(context.Background(), f, VM{ID: "vm-1", CPU: 2, MemoryMB: 2048}, 24*time.Hour, 300, perfNow)
	if err != nil {
		t.Fatal(err)
	}
	cpu, _ := set.Get(perf.CPUUsage)
	if cpu.Values != nil || cpu.Summary.Status != perf.StatusUnavailable {
		t.Fatalf("cpu = %+v; want no values and an unavailable summary", cpu)
	}
	if set.Signal != perf.SignalUnavailable {
		t.Fatalf("signal = %s; want unavailable when CPU usage is missing", set.Signal)
	}
}

func TestVMPerfSeriesNamesPermissionDenial(t *testing.T) {
	f := newFakePerf()
	f.fault = func([]types.PerfQuerySpec) error { return soap.WrapVimFault(&types.NoPermission{}) }
	_, err := collectVMSeries(context.Background(), f, VM{ID: "vm-1"}, time.Hour, RealtimePerfInterval, perfNow)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v; want a named permission denial", err)
	}
}

func TestVMPerfSeriesRefusesAnUnboundedWindow(t *testing.T) {
	f := newFakePerf()
	_, err := collectVMSeries(context.Background(), f, VM{ID: "vm-1"}, 30*24*time.Hour, RealtimePerfInterval, perfNow)
	if err == nil || f.requests != 0 {
		t.Fatalf("err = %v after %d requests; want a refusal before any query", err, f.requests)
	}
}
