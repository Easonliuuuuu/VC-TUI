package tui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	for _, want := range []string{"[0 Overview]", "[1h]", "CPU usage", "Memory active", "Disk", "CONTENTION", "CPU ready", "Co-stop", "NETWORK", "Dropped", "Sizing signal", "20 s samples"} {
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

func TestVMDetailWrapsFullPropertyValues(t *testing.T) {
	for _, width := range []int{60, 80, 99, 100, 140} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			b := twoHealthy()
			vm := &b.inventories["prod"].VMs[0]
			vm.GuestOS = "Synthetic Enterprise Linux with a very long operating system description (64-bit)"
			vm.GuestHostName = strings.Repeat("synthetic", 12) + ".example.invalid"
			vm.Path = "/synthetic/" + strings.Repeat("nested-folder/", 12) + "vm"
			vm.Folder = strings.Repeat("測試", 20)
			m := newTestModel(t, b, Options{Current: "prod"})
			m.width = width
			press(t, m, "enter")
			r, _ := m.detailRow()
			lines, spans := m.vmPropertyLines(r, false)
			for _, line := range lines {
				if ansi.StringWidth(line) > m.vmPropertyWidth() {
					t.Fatalf("property overflows at width %d: %q", width, ansi.Strip(line))
				}
			}
			for i, f := range r.detail {
				if f.label != "Guest OS" && f.label != "DNS name" && f.label != "Inventory path" && f.label != "Folder" {
					continue
				}
				span := spans[i+2]
				var chunks []string
				for _, line := range lines[span.start : span.end+1] {
					chunks = append(chunks, ansi.Strip(ansi.Cut(line, 2+labelColumnPad, m.vmPropertyWidth())))
				}
				compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
				if got := compact(strings.Join(chunks, "")); got != compact(f.value) {
					t.Errorf("%s lost content: got %q, want %q", f.label, got, compact(f.value))
				}
			}
		})
	}
}

func TestVMDetailWrappedFieldNavigationAndActions(t *testing.T) {
	b := twoHealthy()
	b.inventories["prod"].VMs[0].GuestOS = strings.Repeat("Synthetic OS description ", 8)
	b.inventories["prod"].VMs[0].GuestHostName = strings.Repeat("synthetic", 12) + ".example.invalid"
	m := newTestModel(t, b, Options{Current: "prod"})
	m.width, m.height = 100, 20
	press(t, m, "enter")
	r, _ := m.detailRow()
	for _, width := range []int{100, 60, 140} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 20})
		if m.detailCursor > 0 {
			_, spans := m.vmPropertyLines(r, false)
			span := spans[m.detailCursor]
			if span.start < m.detailY || span.end >= m.detailY+m.bodyHeight() {
				t.Fatalf("resize hid the focused field at width %d", width)
			}
		}
		m.detailCursor = 0
		for i := 0; i < len(r.detail); i++ {
			if r.detail[i].label != "DNS name" {
				continue
			}
			for m.detailCursor < i+2 {
				press(t, m, "down")
			}
			_, spans := m.vmPropertyLines(r, false)
			span := spans[m.detailCursor]
			if span.start < m.detailY || span.end >= m.detailY+m.bodyHeight() {
				t.Fatalf("wrapped DNS field %+v outside viewport at %d (offset %d)", span, width, m.detailY)
			}
			press(t, m, "enter")
			if m.actions == nil {
				t.Fatal("wrapped DNS field should open its action menu")
			}
			lines, menuSpans := m.vmPropertyLines(r, true)
			popup := ansi.Strip(strings.Join(m.actionListLines(), "\n"))
			start := menuSpans[m.detailCursor].end + 1
			if got := ansi.Strip(strings.Join(lines[start:start+len(m.actionListLines())], "\n")); got != popup {
				t.Fatalf("popup did not follow the full wrapped value: %q", got)
			}
			for j := 0; j < len(m.actions.items); j++ {
				selected := start + 1 + m.actions.cursor
				if selected < m.detailY || selected >= m.detailY+m.bodyHeight() {
					t.Fatalf("selected action is outside the viewport at width %d", width)
				}
				press(t, m, "down")
			}
			for _, action := range m.actions.items {
				if action.label == "Copy value" {
					handoff := &fakeHandoff{}
					m.handoff = handoff
					if cmd := action.run(m); cmd != nil {
						cmd()
					}
					if len(handoff.copied) != 1 || handoff.copied[0] != r.detail[i].value {
						t.Errorf("copied %v, want full value %q", handoff.copied, r.detail[i].value)
					}
				}
			}
			press(t, m, "esc")
		}
	}
}

func TestVMActionMenuReflowsThePropertyColumn(t *testing.T) {
	for _, width := range []int{100, 140, 200} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			b := perfHealthy()
			b.inventories["prod"].VMs[0].GuestOS = strings.Repeat("synthetic", 4)
			m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
			m.backend = b
			m.width, m.height = width, 60
			press(t, m, "enter")
			r, _ := m.detailRow()
			before := strings.Join(m.vmDetailLines(r, true), "\n")
			_, beforeSpans := m.vmPropertyLines(r, false)
			guestIdx := 4        // Guest OS follows vCenter and Power state.
			press(t, m, "enter") // The VM header's SSH/action menu.
			if m.actions == nil {
				t.Fatal("VM header should open the action menu")
			}
			menuWidth := 0
			for _, line := range m.actionListLines() {
				menuWidth = max(menuWidth, ansi.StringWidth(line))
			}
			if menuWidth <= dashLeftWidth {
				t.Fatal("fixture needs a menu wider than the default property column")
			}
			if m.vmPropertyWidth() < menuWidth {
				t.Fatalf("properties still wrap at %d cells while the menu uses %d", m.vmPropertyWidth(), menuWidth)
			}
			leftW, split := m.vmDetailLayout()
			if split && width-leftW-ansi.StringWidth(dashRule) < dashChartMinWidth {
				t.Fatal("action menu leaves too little space for readable charts")
			}
			firstLine := ansi.Strip(m.vmDetailLines(r, true)[0])
			if split {
				if ansi.Cut(firstLine, leftW, leftW+ansi.StringWidth(dashRule)) != dashRule {
					t.Fatal("chart divider and property wrapping must use the same width")
				}
			} else if strings.Contains(firstLine, "[0 Overview]") {
				t.Fatal("charts should stack below the properties when the menu fills the terminal")
			}
			_, spans := m.vmPropertyLines(r, false)
			if spans[guestIdx].end-spans[guestIdx].start >= beforeSpans[guestIdx].end-beforeSpans[guestIdx].start {
				t.Fatal("Guest OS should use the extra width while the menu is open")
			}
			for _, line := range m.vmDetailLines(r, true) {
				if ansi.StringWidth(line) > width {
					t.Fatalf("menu pushes the dashboard past the terminal edge: %q", ansi.Strip(line))
				}
			}
			press(t, m, "esc")
			if got := strings.Join(m.vmDetailLines(r, true), "\n"); got != before {
				t.Fatal("closing the menu should restore the original dashboard layout")
			}
		})
	}
}

func TestVMOverviewNetworkSparklinesHaveAGutter(t *testing.T) {
	b := perfHealthy()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "enter")
	r, _ := m.detailRow()
	out := ansi.Strip(strings.Join(m.vmDashLines(r, 87), "\n"))
	rx, _ := lineWith(out, "Received")
	tx, _ := lineWith(out, "Transmitted")
	dropped, _ := lineWith(out, "Dropped")
	lines := strings.Split(out, "\n")
	if rx < 0 || tx != rx+2 || dropped != tx+2 || lines[rx+1] != "" || lines[tx+1] != "" {
		t.Fatalf("network sparklines need a blank row between them:\n%s", out)
	}
}

func TestVMDetailCanPageThroughAValueTallerThanThePane(t *testing.T) {
	b := twoHealthy()
	b.inventories["prod"].VMs[0].GuestOS = strings.Repeat("Synthetic long OS description ", 25) + "END-OF-VALUE"
	b.inventories["prod"].VMs[0].GuestState = "syntheticGuestState"
	m := newTestModel(t, b, Options{Current: "prod"})
	m.width, m.height = 100, 20
	press(t, m, "enter", "down", "down", "down")
	r, _ := m.detailRow()
	_, spans := m.vmPropertyLines(r, false)
	span := spans[m.detailCursor]
	if span.end-span.start < m.bodyHeight() || m.detailY != span.start {
		t.Fatalf("tall selected value should start at the viewport top: span=%+v offset=%d", span, m.detailY)
	}
	for m.detailY+m.bodyHeight() <= span.end {
		press(t, m, "pgdown")
	}
	if !strings.Contains(ansi.Strip(m.View()), "END-OF-VALUE") {
		t.Fatal("paging should reveal the end of the wrapped value")
	}
	press(t, m, "down")
	if m.detailCursor != 5 || !strings.Contains(ansi.Strip(m.View()), "▸ Guest state") {
		t.Fatal("Down should advance to the next logical field after paging")
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
	r, _ := m.detailRow()
	e := m.vmPerf[vmPerfKey(r, m.perfRange())]
	old := e.gen
	first := m.loadVMPerf(true)
	if again := m.loadVMPerf(true); again != nil {
		t.Fatal("a read in flight must not be doubled")
	}
	drive(t, m, first)
	m.applyVMPerf(vmPerfMsg{key: vmPerfKey(r, m.perfRange()), gen: old, err: errors.New("stale")})
	if e.err != nil || !e.hasData {
		t.Fatalf("entry = %+v; a reply from an earlier read must not replace the newer one", e)
	}
}

// liveModel is a model with live refresh on and its timer captured, so a
// test can fire ticks by hand instead of waiting on a real one.
func liveModel(t *testing.T, b *perfFakeBackend) (*Model, *[]func(time.Time) tea.Msg, *[]time.Duration) {
	t.Helper()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	m.refreshInterval = DefaultRefreshInterval
	var fires []func(time.Time) tea.Msg
	var delays []time.Duration
	m.vmPerfAfter = func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		fires, delays = append(fires, fn), append(delays, d)
		return nil
	}
	return m, &fires, &delays
}

func fire(t *testing.T, m *Model, fires *[]func(time.Time) tea.Msg, i int) {
	t.Helper()
	drive(t, m, discard(m.Update((*fires)[i](time.Time{}))))
}

func TestLiveRefreshRereadsTheOpenPaneOnTheRangeInterval(t *testing.T) {
	b := perfHealthy()
	m, fires, delays := liveModel(t, b)
	press(t, m, "enter")
	if len(*fires) != 1 || (*delays)[0] != 20*time.Second || len(b.calls) != 1 {
		t.Fatalf("opening armed %d ticks (%v) after %d reads; want one 20s tick after one read", len(*fires), *delays, len(b.calls))
	}
	fire(t, m, fires, 0)
	if len(b.calls) != 2 || len(*fires) != 2 {
		t.Fatalf("a tick should re-read and arm the next: %d reads, %d ticks", len(b.calls), len(*fires))
	}
	press(t, m, ">")
	if last := (*delays)[len(*delays)-1]; last != 5*time.Minute {
		t.Fatalf("24h range ticks every %s; want 5m", last)
	}
	fire(t, m, fires, 1)
	if len(b.calls) != 3 {
		t.Fatalf("a tick from the chain the range change replaced must be dropped: %d reads", len(b.calls))
	}
}

func TestLiveRefreshStopsWhenThePaneCloses(t *testing.T) {
	b := perfHealthy()
	m, fires, _ := liveModel(t, b)
	press(t, m, "enter", "esc")
	fire(t, m, fires, 0)
	if len(b.calls) != 1 || len(*fires) != 1 {
		t.Fatalf("after esc a tick read %d times and armed %d ticks; want it to end the chain", len(b.calls), len(*fires))
	}
}

func TestLiveRefreshResumesWhenThePaneComesBack(t *testing.T) {
	b := perfHealthy()
	m, fires, _ := liveModel(t, b)
	press(t, m, "enter", "?")
	fire(t, m, fires, 0)
	if m.vmPerfLive {
		t.Fatal("a tick under the help overlay should end the chain")
	}
	press(t, m, "?")
	if !m.vmPerfLive || len(*fires) != 2 {
		t.Fatalf("closing help should restart live refresh: live=%v, ticks=%d", m.vmPerfLive, len(*fires))
	}
}

func TestLiveRefreshIsOffWhenBackgroundRefreshIs(t *testing.T) {
	b := perfHealthy()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	armed := 0
	m.vmPerfAfter = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { armed++; return nil }
	press(t, m, "enter")
	if armed != 0 {
		t.Fatalf("RefreshInterval < 0 must leave the pane as last read, armed %d ticks", armed)
	}
	if strings.Contains(ansi.Strip(m.View()), "live ·") {
		t.Error("a pane that does not refresh should not call itself live")
	}
}

func TestFailedRefreshKeepsTheLastGoodCharts(t *testing.T) {
	b := perfHealthy()
	m, fires, _ := liveModel(t, b)
	press(t, m, "enter")
	b.err = errors.New("connection reset")
	fire(t, m, fires, 0)
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "Refresh failed, showing the read from") || !strings.Contains(out, "CPU usage") {
		t.Fatalf("a failed re-read should keep the charts and say they are old:\n%s", out)
	}
	b.err = nil
	fire(t, m, fires, 1)
	if strings.Contains(ansi.Strip(m.View()), "Refresh failed") {
		t.Error("the next good read should clear the warning")
	}
}

func TestReturningToAStaleCachedRangeRereadsIt(t *testing.T) {
	b := perfHealthy()
	m, _, _ := liveModel(t, b)
	press(t, m, "enter")
	r, _ := m.detailRow()
	m.vmPerf[vmPerfKey(r, perfRanges[0])].asOf = dashNow.Add(-time.Minute)
	press(t, m, ">", "<")
	if len(b.calls) != 3 {
		t.Fatalf("calls = %d; a cached 1h read older than 20s should be re-read on return", len(b.calls))
	}
}

func TestChartAxisShowsClockTimes(t *testing.T) {
	b := perfHealthy()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "enter")
	start := dashNow.Add(-time.Hour).Local().Format("15:04")
	end := dashNow.Local().Format("15:04")
	if _, l := lineWith(m.View(), start); !strings.Contains(l, end) || strings.Contains(l, "-1h") {
		t.Fatalf("axis line = %q; want %s at the start and %s at the end", l, start, end)
	}
	press(t, m, ">", ">")
	week := dashNow.Add(-7 * 24 * time.Hour).Local().Format("Jan 2")
	if _, l := lineWith(m.View(), week); l == "" {
		t.Fatalf("the 7d axis should start on %s:\n%s", week, ansi.Strip(m.View()))
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
	r, _ := m.detailRow()
	lines, spans := m.vmPropertyLines(r, false)
	for i, f := range r.detail {
		if f.label != "Snapshots" {
			continue
		}
		field := ansi.Strip(strings.Join(lines[spans[i+2].start:spans[i+2].end+1], "\n"))
		if !strings.HasSuffix(strings.TrimRight(field, " "), glyphCheckWarn+" 41d old") {
			t.Fatalf("snapshot field = %q; want the age warning measured from the backend clock after the value", field)
		}
		if !strings.Contains(strings.Join(strings.Fields(field), ""), "oldestpre-patch") {
			t.Fatalf("snapshot field = %q; the value lost content", field)
		}
	}
}

func TestVMDetailMarkFollowsTheWholeValue(t *testing.T) {
	b := perfHealthy()
	vm := &b.inventories["prod"].VMs[0]
	vm.PowerState = "poweredOn"
	vm.ToolsState = "guestToolsNotRunning"
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "enter")
	for _, width := range []int{60, 100, 140} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 60})
		r, _ := m.detailRow()
		lines, spans := m.vmPropertyLines(r, false)
		for i, f := range r.detail {
			if f.label != "VMware Tools" {
				continue
			}
			field := lines[spans[i+2].start : spans[i+2].end+1]
			if !strings.Contains(ansi.Strip(field[0]), "not running") {
				t.Fatalf("width %d: the value was broken mid-word: %q", width, ansi.Strip(strings.Join(field, "\n")))
			}
			last := ansi.Strip(field[len(field)-1])
			if !strings.Contains(last, glyphCheckWarn) || strings.Count(ansi.Strip(strings.Join(field, "\n")), "not running") != 1 {
				t.Fatalf("width %d: the mark should follow the value, got %q", width, ansi.Strip(strings.Join(field, "\n")))
			}
			for _, line := range field {
				if ansi.StringWidth(line) > m.vmPropertyWidth() {
					t.Fatalf("width %d: %q overflows the property column", width, ansi.Strip(line))
				}
			}
		}
	}
}

func TestVMDashboardPageTabsFitTheChartColumn(t *testing.T) {
	for _, width := range []int{60, 100, 101, 140} {
		m := newTestModel(t, twoHealthy(), Options{})
		for page, name := range perfPages {
			for _, w := range []int{47, 48, 30, 15} {
				tabs := ansi.Strip(m.perfPageTabs(page, w))
				if ansi.StringWidth(tabs) > w {
					t.Fatalf("tabs %q are wider than %d", tabs, w)
				}
				if w >= 30 && !strings.Contains(tabs, fmt.Sprintf("[%d %s]", page, name)) {
					t.Fatalf("width %d: selected tab %q lost its name in %q", w, name, tabs)
				}
			}
		}
		b := perfHealthy()
		m = newTestModel(t, b.fakeBackend, Options{Current: "prod"})
		m.backend = b
		press(t, m, "enter")
		m.Update(tea.WindowSizeMsg{Width: width, Height: 60})
		for page, name := range perfPages {
			press(t, m, fmt.Sprint(page))
			if out := ansi.Strip(m.View()); !strings.Contains(out, fmt.Sprintf("[%d %s]", page, name)) {
				t.Fatalf("at %d columns the selected %s tab is not shown whole", width, name)
			}
		}
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

// A title whose statistics fit with one column to spare, but not the two
// joinEnds keeps between the ends, used to lose its statistics altogether.
func TestChartTitleKeepsStatisticsAtEveryWidth(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	values := make([]float64, 60)
	for i := range values {
		values[i] = 1000
	}
	mt := metric{name: "CPU usage", unit: "2 of 2 members · limit 1.0GHz", values: values, format: fmtMHz, warnAt: 950}
	st, ok := statsOf(values)
	for w := 40; w <= 120; w++ {
		if got := ansi.Strip(m.chartTitle(mt, st, ok, w)); !strings.Contains(got, "peak") {
			t.Fatalf("at %d columns the title lost its statistics: %q", w, got)
		}
	}
}
