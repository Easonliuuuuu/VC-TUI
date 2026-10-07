package vsphere

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/perf"
)

var perfNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// fakePerf answers QueryPerf from a per-VM, per-counter function.
type fakePerf struct {
	keys map[string]int32
	// levels, when set, gives a counter's statistics level by name.
	levels    map[string]int32
	retention []types.PerfInterval
	// data returns raw samples for a VM and counter key; nil means no series.
	data func(vm string, key int32, n int) []int64
	// byInstance, when set, answers a "*" instance request with one series
	// per named device instead of a single aggregate.
	byInstance func(vm string, key int32, n int) map[string][]int64
	// fault, when set, decides whether a call fails.
	fault    func(specs []types.PerfQuerySpec) error
	requests int
}

func newFakePerf() *fakePerf {
	f := &fakePerf{keys: map[string]int32{}}
	for i, c := range perf.DashboardCounters {
		f.keys[c.VSphereName()] = int32(i + 1)
	}
	f.retention = []types.PerfInterval{
		{SamplingPeriod: 7200, Length: 30 * 86400},
		{SamplingPeriod: 300, Length: 86400},
		{SamplingPeriod: 1800, Length: 7 * 86400},
		{SamplingPeriod: 86400, Length: 365 * 86400},
	}
	return f
}

func desc(key string) *types.ElementDescription {
	return &types.ElementDescription{Description: types.Description{Label: key, Summary: key}, Key: key}
}

func (f *fakePerf) counters(context.Context) ([]types.PerfCounterInfo, error) {
	var out []types.PerfCounterInfo
	for _, c := range perf.DashboardCounters {
		key, ok := f.keys[c.VSphereName()]
		if !ok {
			continue
		}
		out = append(out, types.PerfCounterInfo{Key: key, NameInfo: desc(c.Name), GroupInfo: desc(c.Group), RollupType: types.PerfSummaryType(c.Rollup),
			Level: f.levels[c.VSphereName()]})
	}
	return out, nil
}

func (f *fakePerf) intervals(context.Context) ([]types.PerfInterval, error) { return f.retention, nil }

func (f *fakePerf) query(_ context.Context, specs []types.PerfQuerySpec) ([]types.BasePerfEntityMetricBase, error) {
	f.requests++
	if f.fault != nil {
		if err := f.fault(specs); err != nil {
			return nil, err
		}
	}
	var out []types.BasePerfEntityMetricBase
	for _, spec := range specs {
		m := &types.PerfEntityMetric{}
		m.Entity = spec.Entity
		for _, id := range spec.MetricId {
			if id.Instance == "*" && f.byInstance != nil {
				for name, raw := range f.byInstance(spec.Entity.Value, id.CounterId, int(spec.MaxSample)) {
					s := &types.PerfMetricIntSeries{Value: raw}
					s.Id = types.PerfMetricId{CounterId: id.CounterId, Instance: name}
					m.Value = append(m.Value, s)
				}
				continue
			}
			raw := f.data(spec.Entity.Value, id.CounterId, int(spec.MaxSample))
			if raw == nil {
				continue
			}
			s := &types.PerfMetricIntSeries{Value: raw}
			s.Id = id
			m.Value = append(m.Value, s)
		}
		out = append(out, m)
	}
	return out, nil
}

func constant(v int64) func(string, int32, int) []int64 {
	return func(_ string, _ int32, n int) []int64 {
		out := make([]int64, n)
		for i := range out {
			out[i] = v
		}
		return out
	}
}

func vmsN(n int) []VM {
	out := make([]VM, n)
	for i := range out {
		out[i] = VM{ID: fmt.Sprintf("vm-%03d", i), Name: fmt.Sprintf("vm%03d", i), CPU: 4, MemoryMB: 4096, PowerState: "poweredOn"}
	}
	return out
}

func opts() PerfOptions {
	return PerfOptions{Window: 24 * time.Hour, Now: func() time.Time { return perfNow }}
}

func run(t *testing.T, f *fakePerf, vms []VM, o PerfOptions) perf.Window {
	t.Helper()
	return collectPerf(context.Background(), f, "test vCenter", vms, o)
}

func summary(w perf.Window, vm string, m perf.Metric) perf.Summary {
	for _, v := range w.VMs {
		if v.MoRef == vm {
			for _, s := range v.Summaries {
				if s.Metric == m {
					return s
				}
			}
		}
	}
	return perf.Summary{}
}

func TestCollectPerfChoosesFinestIntervalCoveringWindow(t *testing.T) {
	for _, tc := range []struct {
		window time.Duration
		want   int
	}{
		{24 * time.Hour, 300},
		{7 * 24 * time.Hour, 1800},
		{30 * 24 * time.Hour, 7200},
	} {
		f := newFakePerf()
		f.data = constant(500)
		o := opts()
		o.Window = tc.window
		w := run(t, f, vmsN(1), o)
		if w.IntervalSeconds != tc.want || w.Status == perf.WindowFailed {
			t.Errorf("window %s: interval %d status %s (%s), want interval %d", tc.window, w.IntervalSeconds, w.Status, w.Error, tc.want)
		}
		if w.ExpectedSamples != int(tc.window.Seconds())/tc.want {
			t.Errorf("window %s: expected samples %d", tc.window, w.ExpectedSamples)
		}
	}
}

func TestCollectPerfRejectsUnboundedRequests(t *testing.T) {
	f := newFakePerf()
	f.data = constant(500)
	o := opts()
	o.Window = 30 * 24 * time.Hour
	o.Interval = 300
	if w := run(t, f, vmsN(1), o); w.Status != perf.WindowFailed || f.requests != 0 {
		t.Fatalf("30d at 300s (interval retains 1d): status %s, requests %d", w.Status, f.requests)
	}
	o = opts()
	o.Window, o.MaxSamples = 24*time.Hour, 100
	if w := run(t, f, vmsN(1), o); w.Status != perf.WindowFailed || f.requests != 0 {
		t.Fatalf("288 samples over a limit of 100: status %s, requests %d", w.Status, f.requests)
	}
}

func TestCollectPerfSummarisesAndClassifies(t *testing.T) {
	f := newFakePerf()
	f.data = func(_ string, key int32, n int) []int64 {
		out := make([]int64, n)
		for i := range out {
			switch key {
			case f.keys["cpu.usage.average"]:
				out[i] = 500
				if i%96 < 2 {
					out[i] = 9000
				}
			case f.keys["mem.active.average"]:
				out[i] = 400 * 1024
			default:
				out[i] = 0
			}
		}
		return out
	}
	w := run(t, f, vmsN(1), opts())
	if w.Status != perf.WindowSuccess || w.VMsSampled != 1 {
		t.Fatalf("window = %+v", w)
	}
	if got := w.VMs[0].Signal; got != perf.SignalPeaksObserved {
		t.Fatalf("signal = %s (%s)", got, w.VMs[0].SignalReason)
	}
	if s := summary(w, "vm-000", perf.CPUUsage); s.Peak == nil || *s.Peak != 90 {
		t.Fatalf("cpu summary = %+v", s)
	}
}

func TestCollectPerfDeniedCounterIsUnavailableNotZero(t *testing.T) {
	f := newFakePerf()
	base := constant(500)
	f.data = func(vm string, key int32, n int) []int64 {
		switch key {
		case f.keys["mem.active.average"]:
			return nil
		case f.keys["cpu.usage.average"]:
			return base(vm, key, n)
		}
		return make([]int64, n)
	}
	w := run(t, f, vmsN(1), opts())
	s := summary(w, "vm-000", perf.MemActive)
	if s.Status != perf.StatusUnavailable || s.Average != nil || s.Peak != nil {
		t.Fatalf("mem.active = %+v, want unavailable without numbers", s)
	}
	if w.VMs[0].Signal != perf.SignalCPUOnly {
		t.Fatalf("signal = %s, want %s", w.VMs[0].Signal, perf.SignalCPUOnly)
	}
}

// Measured on a vCenter 8.0.3 at the default statistics level 1:
// mem.active.average and mem.swapped.average are level 2, so they are never
// kept. They must not be requested, must say why, and must not stop the VM
// getting a CPU reading.
func TestCollectPerfSkipsCountersAboveStatisticsLevel(t *testing.T) {
	f := newFakePerf()
	f.levels = map[string]int32{}
	for _, c := range perf.Counters {
		f.levels[c.VSphereName()] = 1
	}
	f.levels["mem.active.average"], f.levels["mem.swapped.average"] = 2, 2
	for i := range f.retention {
		f.retention[i].Level = 1
	}
	var requested []int32
	f.fault = func(specs []types.PerfQuerySpec) error {
		for _, id := range specs[0].MetricId {
			requested = append(requested, id.CounterId)
		}
		return nil
	}
	f.data = func(_ string, key int32, n int) []int64 {
		if key == f.keys["cpu.usage.average"] {
			return constant(500)("", key, n)
		}
		return make([]int64, n)
	}
	w := run(t, f, vmsN(1), opts())
	for _, key := range requested {
		if key == f.keys["mem.active.average"] || key == f.keys["mem.swapped.average"] {
			t.Fatalf("requested a counter above the interval's statistics level: %v", requested)
		}
	}
	if len(requested) != len(perf.Counters)-2 {
		t.Fatalf("requested %d counters, want %d", len(requested), len(perf.Counters)-2)
	}
	for _, m := range []perf.Metric{perf.MemActive, perf.MemSwapped} {
		if s := summary(w, "vm-000", m); !perf.BelowLevel(s) || s.Average != nil {
			t.Fatalf("%s = %+v, want unavailable for the statistics level", m, s)
		}
	}
	if !strings.Contains(w.Source, "statistics level 1") {
		t.Fatalf("source %q does not record the statistics level", w.Source)
	}
	if w.Status != perf.WindowSuccess {
		t.Fatalf("status = %s (%s); a statistics level is a setting, not a failure", w.Status, w.Error)
	}
	if got := w.VMs[0].Signal; got != perf.SignalCPUOnly {
		t.Fatalf("signal = %s (%s), want %s", got, w.VMs[0].SignalReason, perf.SignalCPUOnly)
	}
}

// A VM that was off for the whole window returns nothing for any counter.
// That is too little evidence, not a counter that cannot be read.
func TestCollectPerfPoweredOffVMIsInsufficient(t *testing.T) {
	f := newFakePerf()
	vms := vmsN(2)
	vms[1].PowerState = "poweredOff"
	on := constant(500)
	f.data = func(vm string, key int32, n int) []int64 {
		if vm == vms[1].ID {
			return nil
		}
		return on(vm, key, n)
	}
	w := run(t, f, vms, opts())
	off := w.VMs[1]
	if off.Signal != perf.SignalInsufficient || !strings.Contains(off.SignalReason, "powered off") {
		t.Fatalf("powered-off VM signal = %s (%s)", off.Signal, off.SignalReason)
	}
	for _, s := range off.Summaries {
		if s.Status != perf.StatusInsufficient || s.Average != nil {
			t.Fatalf("%s = %+v, want insufficient without numbers", s.Metric, s)
		}
	}
	if w.Status != perf.WindowSuccess {
		t.Fatalf("status = %s (%s)", w.Status, w.Error)
	}
}

func TestDefaultPerfBatchFitsMaxQueryMetrics(t *testing.T) {
	if n := DefaultPerfBatchVMs * len(perf.Counters); n > DefaultMaxQueryMetrics {
		t.Fatalf("a default batch asks for %d metrics, over vCenter's default limit of %d", n, DefaultMaxQueryMetrics)
	}
}

func TestCollectPerfCounterMissingOnServer(t *testing.T) {
	f := newFakePerf()
	delete(f.keys, "mem.vmmemctl.average")
	f.data = constant(500)
	w := run(t, f, vmsN(1), opts())
	if s := summary(w, "vm-000", perf.MemBalloon); s.Status != perf.StatusUnavailable {
		t.Fatalf("balloon = %+v", s)
	}
}

func TestCollectPerfSparseHistoryIsInsufficient(t *testing.T) {
	f := newFakePerf()
	f.data = func(_ string, _ int32, n int) []int64 {
		out := make([]int64, n)
		for i := range out {
			out[i] = perf.NoData
		}
		out[0], out[1], out[2] = 500, 500, 500
		return out
	}
	w := run(t, f, vmsN(1), opts())
	if got := w.VMs[0].Signal; got != perf.SignalInsufficient {
		t.Fatalf("signal = %s, want insufficient-data", got)
	}
	if s := summary(w, "vm-000", perf.CPUUsage); s.Average != nil {
		t.Fatalf("average reported from 3 samples: %+v", s)
	}
}

func TestCollectPerfIsolatesAFailingVMWithinABatch(t *testing.T) {
	f := newFakePerf()
	f.data = constant(500)
	f.fault = func(specs []types.PerfQuerySpec) error {
		for _, s := range specs {
			if s.Entity.Value == "vm-002" {
				return errors.New("boom")
			}
		}
		return nil
	}
	o := opts()
	o.BatchVMs = 4
	w := run(t, f, vmsN(4), o)
	if w.Status != perf.WindowPartial {
		t.Fatalf("status = %s (%s)", w.Status, w.Error)
	}
	if s := summary(w, "vm-001", perf.CPUUsage); s.Status != perf.StatusOK {
		t.Fatalf("healthy VM lost to a neighbour's failure: %+v", s)
	}
	if s := summary(w, "vm-002", perf.CPUUsage); s.Status != perf.StatusUnavailable || s.Reason != "boom" {
		t.Fatalf("failing VM = %+v", s)
	}
	if w.RequestsUsed != f.requests {
		t.Fatalf("requests recorded %d, made %d", w.RequestsUsed, f.requests)
	}
}

func TestCollectPerfStopsOnPermissionDenied(t *testing.T) {
	f := newFakePerf()
	f.data = constant(500)
	f.fault = func([]types.PerfQuerySpec) error { return soap.WrapVimFault(&types.NoPermission{}) }
	o := opts()
	o.BatchVMs = 2
	w := run(t, f, vmsN(6), o)
	if f.requests != 1 {
		t.Fatalf("requests = %d, want 1: a denial must not be retried against every VM", f.requests)
	}
	if w.Status != perf.WindowFailed {
		t.Fatalf("status = %s (%s)", w.Status, w.Error)
	}
	if w.VMsSampled != 2 || len(w.VMs) != 6 {
		t.Fatalf("sampled %d of %d", w.VMsSampled, len(w.VMs))
	}
	for _, vm := range w.VMs[2:] {
		if vm.Sampled || vm.Signal != perf.SignalInsufficient {
			t.Fatalf("unqueried VM %+v", vm)
		}
	}
}

func TestCollectPerfHonoursRequestAndVMBudgets(t *testing.T) {
	f := newFakePerf()
	f.data = constant(500)
	o := opts()
	o.BatchVMs, o.MaxRequests = 2, 2
	w := run(t, f, vmsN(10), o)
	if f.requests != 2 || w.Status != perf.WindowPartial || w.VMsSampled != 4 || w.VMsRequested != 10 {
		t.Fatalf("requests %d status %s sampled %d/%d", f.requests, w.Status, w.VMsSampled, w.VMsRequested)
	}

	f = newFakePerf()
	f.data = constant(500)
	o = opts()
	o.MaxVMs = 3
	w = run(t, f, vmsN(10), o)
	if w.VMsSampled != 3 || w.Status != perf.WindowPartial || len(w.VMs) != 10 {
		t.Fatalf("sampled %d status %s rows %d", w.VMsSampled, w.Status, len(w.VMs))
	}
}

func TestCollectPerfSkipsTemplatesAndStopsWhenCancelled(t *testing.T) {
	f := newFakePerf()
	f.data = constant(500)
	vms := vmsN(3)
	vms[1].IsTemplate = true
	if w := run(t, f, vms, opts()); w.VMsRequested != 2 {
		t.Fatalf("requested %d, templates must be excluded", w.VMsRequested)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := collectPerf(ctx, f, "x", vmsN(3), opts())
	if w.Status != perf.WindowFailed || w.VMsSampled != 0 {
		t.Fatalf("cancelled: status %s sampled %d", w.Status, w.VMsSampled)
	}
}
