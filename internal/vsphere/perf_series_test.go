package vsphere

import (
	"context"
	"errors"
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
	if f.requests != 1 || len(spec.MetricId) != len(perf.DashboardCounters) || spec.IntervalId != RealtimePerfInterval || spec.MaxSample != 180 {
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

func TestVMPerfSeriesRequestsEveryInstanceOnlyForAdditiveCounters(t *testing.T) {
	f := newFakePerf()
	var spec types.PerfQuerySpec
	f.fault = func(specs []types.PerfQuerySpec) error { spec = specs[0]; return nil }
	f.data = constant(100)
	if _, err := collectVMSeries(context.Background(), f, VM{ID: "vm-1", CPU: 2, MemoryMB: 2048}, time.Hour, RealtimePerfInterval, perfNow); err != nil {
		t.Fatal(err)
	}
	byKey := map[int32]string{}
	for _, id := range spec.MetricId {
		byKey[id.CounterId] = id.Instance
	}
	for _, c := range perf.DashboardCounters {
		want := ""
		if perf.SumsInstances(c.Metric) {
			want = "*"
		}
		if got := byKey[f.keys[c.VSphereName()]]; got != want {
			t.Errorf("%s instance = %q; want %q", c.VSphereName(), got, want)
		}
	}
}

func TestVMPerfSeriesSumsDevicesWhenTheAggregateIsMissing(t *testing.T) {
	f := newFakePerf()
	f.data = constant(100)
	readKey := f.keys["disk.read.average"]
	f.byInstance = func(_ string, key int32, n int) map[string][]int64 {
		a, b := make([]int64, n), make([]int64, n)
		for i := range a {
			a[i], b[i] = 300, 200
		}
		a[0], b[0] = perf.NoData, perf.NoData
		b[1] = perf.NoData
		if key != readKey {
			return map[string][]int64{"": a}
		}
		return map[string][]int64{"scsi0:0": a, "scsi0:1": b}
	}
	set, err := collectVMSeries(context.Background(), f, VM{ID: "vm-1", CPU: 2, MemoryMB: 2048}, time.Hour, RealtimePerfInterval, perfNow)
	if err != nil {
		t.Fatal(err)
	}
	read, _ := set.Get(perf.DiskRead)
	if !math.IsNaN(read.Values[0]) || read.Values[1] != 300 || read.Values[2] != 500 {
		t.Fatalf("disk read = %v...; want NaN where every disk is missing, then 300 and 500 KBps", read.Values[:3])
	}
	written, _ := set.Get(perf.DiskWrite)
	if written.Values[2] != 300 {
		t.Fatalf("disk write = %v; an aggregate instance must be used as is, not summed again", written.Values[2])
	}
}

func steadyPerf(f *fakePerf) {
	f.data = func(_ string, _ int32, n int) []int64 {
		out := make([]int64, n)
		for i := range out {
			out[i] = 1000
		}
		return out
	}
}

func groupVMs(n int) []VM {
	vms := make([]VM, n)
	for i := range vms {
		vms[i] = VM{ID: "vm-" + string(rune('a'+i)), Name: "member", CPU: 2, MemoryMB: 4096}
	}
	return vms
}

func TestVMsPerfSeriesKeepsHistoricalBatchesUnderTheMetricCap(t *testing.T) {
	f := newFakePerf()
	steadyPerf(f)
	var sizes []int
	f.fault = func(specs []types.PerfQuerySpec) error {
		metrics := 0
		for _, s := range specs {
			metrics += len(s.MetricId)
		}
		if metrics > DefaultMaxQueryMetrics {
			t.Errorf("one historical request asked for %d metrics, over vCenter's default cap of %d", metrics, DefaultMaxQueryMetrics)
		}
		sizes = append(sizes, len(specs))
		return nil
	}
	out, err := collectVMsSeries(context.Background(), f, groupVMs(7), 24*time.Hour, 300, perfNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes[0] != 3 || sizes[1] != 3 || sizes[2] != 1 {
		t.Fatalf("batches = %v; want 3, 3, 1", sizes)
	}
	for i, r := range out {
		if r.Err != nil || r.VM.ID != groupVMs(7)[i].ID {
			t.Fatalf("result %d = %+v", i, r)
		}
		if cpu, ok := r.Set.Get(perf.CPUUsage); !ok || len(cpu.Values) != 288 {
			t.Fatalf("result %d cpu = %+v", i, cpu)
		}
	}
}

func TestVMsPerfSeriesReadsRealtimeInOneRequest(t *testing.T) {
	f := newFakePerf()
	steadyPerf(f)
	if _, err := collectVMsSeries(context.Background(), f, groupVMs(7), time.Hour, RealtimePerfInterval, perfNow); err != nil {
		t.Fatal(err)
	}
	if f.requests != 1 {
		t.Fatalf("realtime read took %d requests; the metric cap does not apply to it", f.requests)
	}
}

func TestVMsPerfSeriesRetriesAFailedBatchOneVMAtATime(t *testing.T) {
	f := newFakePerf()
	steadyPerf(f)
	f.fault = func(specs []types.PerfQuerySpec) error {
		for _, s := range specs {
			if s.Entity.Value == "vm-b" {
				return errors.New("vm-b is gone")
			}
		}
		return nil
	}
	out, err := collectVMsSeries(context.Background(), f, groupVMs(3), 24*time.Hour, 300, perfNow)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Err != nil || out[2].Err != nil {
		t.Fatalf("one VM's failure cost the others their charts: %v, %v", out[0].Err, out[2].Err)
	}
	if out[1].Err == nil || !strings.Contains(out[1].Err.Error(), "vm-b is gone") {
		t.Fatalf("vm-b err = %v", out[1].Err)
	}
	if f.requests != 4 {
		t.Fatalf("requests = %d; want the batch then each VM alone", f.requests)
	}
}

func TestVMsPerfSeriesStopsAtAPermissionDenial(t *testing.T) {
	f := newFakePerf()
	f.fault = func([]types.PerfQuerySpec) error { return soap.WrapVimFault(&types.NoPermission{}) }
	_, err := collectVMsSeries(context.Background(), f, groupVMs(6), 24*time.Hour, 300, perfNow)
	if err == nil || !strings.Contains(err.Error(), "permission denied") || f.requests != 1 {
		t.Fatalf("err = %v after %d requests; want one named denial", err, f.requests)
	}
}

func TestVMsPerfSeriesRefusesAnUnboundedGroup(t *testing.T) {
	f := newFakePerf()
	if _, err := collectVMsSeries(context.Background(), f, make([]VM, MaxVMsPerfSeries+1), time.Hour, RealtimePerfInterval, perfNow); err == nil || f.requests != 0 {
		t.Fatalf("err = %v after %d requests; want a refusal before any query", err, f.requests)
	}
}
