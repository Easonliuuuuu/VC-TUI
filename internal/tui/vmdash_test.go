package tui

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/perf"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

var dashNow = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

// perfFakeBackend adds the chart extension and a pinned clock to fakeBackend.
type perfFakeBackend struct {
	*fakeBackend
	calls []perfCall
	err   error
}

type perfCall struct {
	vm       string
	window   time.Duration
	interval int
}

func (b *perfFakeBackend) Now() time.Time { return dashNow }

func (b *perfFakeBackend) VMPerfSeries(_ context.Context, _ *config.Context, vm vsphere.VM, window time.Duration, interval int, now time.Time) (perf.SeriesSet, error) {
	b.calls = append(b.calls, perfCall{vm.ID, window, interval})
	if b.err != nil {
		return perf.SeriesSet{}, b.err
	}
	n := int(window.Seconds()) / interval
	set := perf.SeriesSet{IntervalSeconds: interval, WindowStart: now.Add(-window), WindowEnd: now}
	for _, c := range perf.DashboardCounters {
		raw := make([]int64, n)
		for i := range raw {
			raw[i] = 2000
		}
		set.Series = append(set.Series, perf.NewSeries(c, raw, n, interval, vm.CPU))
	}
	set.Signal, set.SignalReason = perf.Classify(perf.ClassifyInput{Summaries: set.Summaries(), MemoryMB: vm.MemoryMB})
	return set, nil
}

func perfHealthy() *perfFakeBackend { return &perfFakeBackend{fakeBackend: twoHealthy()} }

func lineWith(out, needle string) (int, string) {
	for i, l := range strings.Split(ansi.Strip(out), "\n") {
		if strings.Contains(l, needle) {
			return i, l
		}
	}
	return -1, ""
}

func TestVMDetailPutsChartsBesideTheProperties(t *testing.T) {
	b := perfHealthy()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	m.height = 60
	press(t, m, "enter")
	out := m.View()
	for _, want := range []string{"[0 Overview]", "[1h]", "CPU usage", "Memory active", "Disk", "CONTENTION", "CPU ready", "Co-stop", "NETWORK", "Dropped", "Sizing signal", "live · 20 s samples"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard is missing %q:\n%s", want, ansi.Strip(out))
		}
	}
	if _, l := lineWith(out, "vCenter "); !strings.Contains(l, "│") {
		t.Errorf("at 140 columns the property and chart columns should share lines, got %q", l)
	}
	if len(b.calls) != 1 || b.calls[0].interval != vsphere.RealtimePerfInterval || b.calls[0].vm != "prod-vm-1" {
		t.Fatalf("calls = %+v; want one realtime read of the opened VM", b.calls)
	}
}

func TestVMDetailStacksChartsOnANarrowTerminal(t *testing.T) {
	b := perfHealthy()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	m.width, m.height = 80, 80
	press(t, m, "enter")
	out := m.View()
	last, _ := lineWith(out, "Managed object")
	chart, _ := lineWith(out, "CPU usage")
	if last < 0 || chart <= last || strings.Contains(ansi.Strip(out), "│") {
		t.Fatalf("at 80 columns charts should follow the properties (managed object line %d, chart line %d):\n%s", last, chart, ansi.Strip(out))
	}
	for _, l := range strings.Split(ansi.Strip(out), "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("line is %d cells wide on an 80 column terminal: %q", w, l)
		}
	}
}

func TestVMDetailRangeKeysReadEachRangeOnce(t *testing.T) {
	b := perfHealthy()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "enter", ">")
	if len(b.calls) != 2 || b.calls[1].window != 24*time.Hour || b.calls[1].interval != 300 {
		t.Fatalf("calls = %+v; want the 24h range read at the 5 minute roll-up", b.calls)
	}
	if !strings.Contains(ansi.Strip(m.View()), "[24h]") {
		t.Error("the 24h range should be the selected one")
	}
	press(t, m, "<")
	if len(b.calls) != 2 {
		t.Fatalf("returning to a loaded range re-read it: %+v", b.calls)
	}
	press(t, m, "<")
	if m.perfRangeIdx != 0 {
		t.Fatalf("range index = %d; the shortest range should stop rather than wrap", m.perfRangeIdx)
	}
	press(t, m, "r")
	if len(b.calls) != 3 {
		t.Fatalf("refresh should re-read the current range: %+v", b.calls)
	}
}

func TestVMDetailKeepsTheRangeWhenMovingBetweenVMs(t *testing.T) {
	b := perfHealthy()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "enter", ">", "]")
	got := b.calls[len(b.calls)-1]
	if got.vm != "prod-vm-2" || got.window != 24*time.Hour {
		t.Fatalf("last call = %+v; want the next VM over the same 24h range", got)
	}
}

func TestVMDetailWithoutTheLiveQuerySaysWhy(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "enter")
	if out := ansi.Strip(m.View()); !strings.Contains(out, "need a live vCenter connection") {
		t.Fatalf("a backend without charts should say so:\n%s", out)
	}
}

func TestVMDetailShowsAFailedReadInPlaceOfCharts(t *testing.T) {
	b := perfHealthy()
	b.err = errors.New("permission denied reading performance data")
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "enter")
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "Performance unavailable: permission denied") || !strings.Contains(out, "Managed object") {
		t.Fatalf("a failed read should be reported beside intact properties:\n%s", out)
	}
}

func TestSupersededVMPerfReplyIsDropped(t *testing.T) {
	b := perfHealthy()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "enter")
	stale := m.ensureVMPerf(true)
	fresh := m.ensureVMPerf(true)
	staleMsg := stale().(vmPerfMsg)
	staleMsg.err = errors.New("stale")
	drive(t, m, fresh)
	m.applyVMPerf(staleMsg)
	r, _ := m.detailRow()
	if e := m.vmPerf[vmPerfKey(r, m.perfRange())]; e == nil || e.err != nil {
		t.Fatalf("entry = %+v; a reply superseded by a refresh must not replace the newer one", e)
	}
}

func TestVMFieldMarksFollowTheHealthDefaults(t *testing.T) {
	base := vsphere.VM{PowerState: "poweredOn", ToolsState: "guestToolsRunning", ConfigurationAvailable: true}

	old := base
	old.Snapshots = []vsphere.VMSnapshot{{Name: "a", CreateTime: dashNow.Add(-31 * 24 * time.Hour)}, {Name: "b", CreateTime: dashNow.Add(-2 * time.Hour)}}
	if mk := vmFieldMarks(old, dashNow)["Snapshots"]; mk.status != statusWarn || mk.note != "31d old" {
		t.Errorf("a 31 day snapshot = %+v; want a warning naming its age", mk)
	}
	if v := snapshotsValue(old); v != "2 · oldest a" {
		t.Errorf("snapshots value = %q", v)
	}

	young := base
	young.Snapshots = []vsphere.VMSnapshot{{Name: "a", CreateTime: dashNow.Add(-5 * 24 * time.Hour)}}
	if mk := vmFieldMarks(young, dashNow)["Snapshots"]; mk.status != statusGood {
		t.Errorf("a 5 day snapshot = %+v; want a pass", mk)
	}

	summary := vsphere.VM{PowerState: "poweredOn"}
	if mk, ok := vmFieldMarks(summary, dashNow)["Snapshots"]; ok {
		t.Errorf("a VM read without configuration has no snapshot evidence, got %+v", mk)
	}
	if mk, ok := vmFieldMarks(summary, dashNow)["VMware Tools"]; ok {
		t.Errorf("an empty Tools state is missing evidence, got %+v", mk)
	}

	full := base
	full.Partitions = []vsphere.VMPartition{
		{Path: "/", CapacityBytes: 100 << 30, FreeBytes: 50 << 30},
		{Path: "/var/log", CapacityBytes: 10 << 30, FreeBytes: 512 << 20},
	}
	if mk := vmFieldMarks(full, dashNow)["Guest disks"]; mk.status != statusWarn || mk.note != "5% free" {
		t.Errorf("a 95%% full filesystem = %+v; want a warning", mk)
	}
	if v := guestDisksValue(full); !strings.HasPrefix(v, "/var/log 95% used") || !strings.Contains(v, "2 filesystems") {
		t.Errorf("guest disks value = %q; want the fullest filesystem first", v)
	}

	stopped := base
	stopped.ToolsState = "guestToolsNotRunning"
	if mk := vmFieldMarks(stopped, dashNow)["VMware Tools"]; mk.status != statusWarn || mk.note != "not running" {
		t.Errorf("stopped tools = %+v", mk)
	}
	outdated := base
	outdated.ToolsVersionStatus = "guestToolsNeedUpgrade"
	if mk := vmFieldMarks(outdated, dashNow)["VMware Tools"]; mk.status != statusWarn || mk.note != "needs upgrade" {
		t.Errorf("outdated tools = %+v", mk)
	}
}

func TestVMDetailDrawsFieldMarks(t *testing.T) {
	b := perfHealthy()
	b.inventories["prod"].VMs[0].ConfigurationAvailable = true
	b.inventories["prod"].VMs[0].Snapshots = []vsphere.VMSnapshot{{Name: "pre-patch", CreateTime: dashNow.Add(-41 * 24 * time.Hour)}}
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "enter")
	if _, l := lineWith(m.View(), "Snapshots"); !strings.Contains(l, glyphCheckWarn+" 41d old") {
		t.Fatalf("snapshot line = %q; want the age warning measured from the backend clock", l)
	}
}

func TestBlockChartKeepsPeaksAndMarksGaps(t *testing.T) {
	values := []float64{10, 90, 10, 10, math.NaN(), math.NaN()}
	if got := bucketMax(values, 3); got[0] != 90 || got[1] != 10 || !math.IsNaN(got[2]) {
		t.Fatalf("bucketMax = %v; want each bucket's peak and NaN for an all-gap bucket", got)
	}
	m := newTestModel(t, twoHealthy(), Options{})
	rows := m.blockChart(values, 6, 2, 100, 60)
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	bottom := []rune(ansi.Strip(rows[1]))
	if bottom[1] != '█' || bottom[4] != '·' || bottom[5] != '·' {
		t.Fatalf("bottom row = %q; want a full column under the peak and dots for gaps", string(bottom))
	}
	if top := []rune(ansi.Strip(rows[0])); top[1] == ' ' || top[0] != ' ' {
		t.Fatalf("top row = %q; only the 90%% column should reach it", string(top))
	}
}

func TestVMDetailFooterOffersTheRangeKeys(t *testing.T) {
	b := perfHealthy()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "enter")
	if !strings.Contains(ansi.Strip(m.viewKeys()), "</> range") {
		t.Fatalf("footer = %q", ansi.Strip(m.viewKeys()))
	}
}

func TestVMDetailPageKeysSwitchChartsWithoutANewRead(t *testing.T) {
	b := perfHealthy()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	m.height = 60
	press(t, m, "enter")
	pages := []struct {
		key  string
		tab  string
		want []string
	}{
		{"1", "[1 CPU]", []string{"CPU usage", "CPU ready", "Co-stop", "CPU limited", "Sizing signal"}},
		{"2", "[2 Memory]", []string{"Memory active", "Memory consumed", "Balloon", "Swap-in"}},
		{"3", "[3 Disk]", []string{"Read", "Write", "Max latency", "IOPS"}},
		{"4", "[4 Network]", []string{"Received", "Transmitted", "Dropped"}},
		{"0", "[0 Overview]", []string{"CONTENTION", "NETWORK"}},
	}
	for _, p := range pages {
		press(t, m, p.key)
		out := ansi.Strip(m.View())
		for _, want := range append([]string{p.tab}, p.want...) {
			if !strings.Contains(out, want) {
				t.Errorf("page %s is missing %q:\n%s", p.key, want, out)
			}
		}
	}
	if len(b.calls) != 1 {
		t.Fatalf("calls = %d; switching pages must draw from the loaded series", len(b.calls))
	}
	press(t, m, "3", "]")
	if !strings.Contains(ansi.Strip(m.View()), "[3 Disk]") {
		t.Error("the page should stay selected when moving to the next VM")
	}
}

func TestVMDetailSaysWhenThereIsNoCPULimit(t *testing.T) {
	b := perfHealthy()
	b.inventories["prod"].VMs[0].CPUAllocation = &vsphere.VMResourceAllocation{Limit: func() *int64 { v := int64(-1); return &v }()}
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	m.height = 60
	press(t, m, "enter", "1")
	if _, l := lineWith(m.View(), "CPU limited"); !strings.Contains(l, "no CPU limit set") {
		t.Fatalf("limited line = %q; an unlimited VM cannot be held back by a limit", l)
	}
}

func TestVMDetailSumsDiskReadAndWrite(t *testing.T) {
	b := perfHealthy()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	m.height = 60
	press(t, m, "enter")
	// The fake reports 2000 KBps for each of read and write.
	if _, l := lineWith(m.View(), "read + write"); !strings.Contains(l, "peak 3.9 MB/s") {
		t.Fatalf("overview disk title = %q; want read and write summed", l)
	}
	press(t, m, "3")
	if _, l := lineWith(m.View(), "Read "); !strings.Contains(l, "peak 2.0 MB/s") {
		t.Fatalf("disk page read title = %q; want read on its own", l)
	}
}

func TestVMDetailShowsUptimeFromTheLastSample(t *testing.T) {
	b := perfHealthy()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "enter")
	// The fake's 2000 seconds of uptime is 33 minutes.
	if !strings.Contains(ansi.Strip(m.View()), "up 33m") {
		t.Fatalf("header should carry the uptime:\n%s", ansi.Strip(m.View()))
	}
}

func TestMetricStatsAreGatedLikeSummaries(t *testing.T) {
	few := []float64{1, 2, 3}
	if _, ok := statsOf(few); ok {
		t.Error("three samples should not support statistics")
	}
	sparse := make([]float64, 40)
	for i := range sparse {
		sparse[i] = math.NaN()
		if i < 15 {
			sparse[i] = 5
		}
	}
	if _, ok := statsOf(sparse); ok {
		t.Error("15 of 40 samples is under the coverage floor")
	}
	full := make([]float64, 60)
	for i := range full {
		full[i] = float64(i)
	}
	st, ok := statsOf(full)
	if !ok || st.peak != 59 || st.last != 59 || !st.hasP95 || st.p95 != 56 {
		t.Fatalf("stats = %+v, %v", st, ok)
	}
}

func TestHumanRateUsesReadableUnits(t *testing.T) {
	for in, want := range map[float64]string{512: "512 KB/s", 1536: "1.5 MB/s", 3 * 1024 * 1024: "3.0 GB/s"} {
		if got := humanRate(in); got != want {
			t.Errorf("humanRate(%v) = %q; want %q", in, got, want)
		}
	}
}

func TestChartTitleGivesUpDetailBeforeThePeak(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{})
	values := make([]float64, 60)
	for i := range values {
		values[i] = float64(i)
	}
	mt := metric{name: "Disk", unit: "read + write · latency 37 ms", values: values, format: fmtPct, warnAt: 50}
	st, ok := statsOf(values)
	for w, want := range map[int]string{
		90: "p95",
		60: "avg",
		40: "peak",
		22: "peak",
	} {
		got := ansi.Strip(m.chartTitle(mt, st, ok, w))
		if ansi.StringWidth(got) > w || !strings.Contains(got, want) || !strings.Contains(got, "peak 59.0%") || !strings.Contains(got, glyphCheckWarn) {
			t.Errorf("width %d: title %q; want %q, the peak and the warning within the width", w, got, want)
		}
	}
	if got := ansi.Strip(m.chartTitle(mt, st, ok, 40)); strings.Contains(got, "latency") {
		t.Errorf("at 40 columns the unit should go before the peak: %q", got)
	}
}
