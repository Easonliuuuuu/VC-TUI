package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/health"
	"github.com/easonliuuuuu/vsfleet/internal/humanize"
	"github.com/easonliuuuuu/vsfleet/internal/perf"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// The VM detail pane is a small dashboard: the property list it always had,
// each field that a health rule judges carrying that rule's verdict, beside
// charts of the VM's own performance counters over a chosen range. On a
// terminal too narrow to hold both side by side the charts follow the
// properties instead, so the field cursor and its actions never move.
const (
	// dashSplitWidth is the narrowest terminal that gets the side-by-side
	// layout: a property column of dashLeftWidth plus a chart column wide
	// enough for a readable hour of realtime samples.
	dashSplitWidth = 100
	dashLeftWidth  = 50
	dashRule       = " │ "
	chartRows      = 4

	glyphCheckOK   = "✓"
	glyphCheckWarn = "▲"
)

// perfRange is one choice of chart window. Each pairs a window with the
// interval vSphere keeps it at: the 20-second realtime samples ESXi holds for
// about an hour, then the default historical roll-ups. Every one fits inside
// vsphere.DefaultPerfMaxSamples.
type perfRange struct {
	label    string
	window   time.Duration
	interval int
	source   string
}

var perfRanges = []perfRange{
	{"1h", time.Hour, vsphere.RealtimePerfInterval, "live · 20 s samples"},
	{"24h", 24 * time.Hour, 300, "5 min roll-up"},
	{"7d", 7 * 24 * time.Hour, 1800, "30 min roll-up"},
	{"30d", 30 * 24 * time.Hour, 7200, "2 h roll-up"},
}

// vmPerfEntry is one VM's charts for one range: loading, failed, or loaded.
type vmPerfEntry struct {
	loading bool
	gen     uint64
	set     perf.SeriesSet
	err     error
	asOf    time.Time
}

type vmPerfMsg struct {
	key  string
	gen  uint64
	set  perf.SeriesSet
	err  error
	asOf time.Time
}

// now is the instant ages in the interface are measured from: the backend's
// own clock when it pins one (the demo estate), the wall clock otherwise.
func (m *Model) now() time.Time {
	if c, ok := m.backend.(clockBackend); ok {
		return c.Now()
	}
	return time.Now()
}

func (m *Model) perfRange() perfRange {
	return perfRanges[clamp(m.perfRangeIdx, 0, len(perfRanges)-1)]
}

func vmPerfKey(r row, rng perfRange) string { return r.key + "|" + rng.label }

// ensureVMPerf starts reading the detail row's charts for the current range,
// unless they are already loaded or loading. force re-reads them anyway —
// the refresh key. Rows that are not VMs, and backends without the live
// query, return nil: the pane says why in place of the charts.
func (m *Model) ensureVMPerf(force bool) tea.Cmd {
	r, ok := m.detailRow()
	if !ok || r.vm == nil {
		return nil
	}
	b, ok := m.backend.(vmPerfBackend)
	if !ok {
		return nil
	}
	st, ok := m.byName[r.context]
	if !ok || st.cc == nil {
		return nil
	}
	rng := m.perfRange()
	k := vmPerfKey(r, rng)
	if e := m.vmPerf[k]; e != nil && !force && (e.loading || e.err == nil) {
		return nil
	}
	if m.vmPerf == nil {
		m.vmPerf = map[string]*vmPerfEntry{}
	}
	m.vmPerfGen++
	gen := m.vmPerfGen
	m.vmPerf[k] = &vmPerfEntry{loading: true, gen: gen}
	ctx, cc, vm, now := m.ctx, st.cc, *r.vm, m.now()
	return func() tea.Msg {
		set, err := b.VMPerfSeries(ctx, cc, vm, rng.window, rng.interval, now)
		return vmPerfMsg{key: k, gen: gen, set: set, err: err, asOf: now}
	}
}

func (m *Model) applyVMPerf(msg vmPerfMsg) {
	e := m.vmPerf[msg.key]
	if e == nil || e.gen != msg.gen {
		return
	}
	*e = vmPerfEntry{gen: msg.gen, set: msg.set, err: msg.err, asOf: msg.asOf}
}

// shiftPerfRange moves to a shorter (delta < 0) or longer range and starts
// reading it.
func (m *Model) shiftPerfRange(delta int) tea.Cmd {
	r, ok := m.detailRow()
	if !ok || r.vm == nil {
		return nil
	}
	m.perfRangeIdx = clamp(m.perfRangeIdx+delta, 0, len(perfRanges)-1)
	return m.ensureVMPerf(false)
}

// setPerfPage switches the chart column to the page a digit names. It
// asks the vCenter nothing: every page draws from the same loaded series.
func (m *Model) setPerfPage(digit string) {
	if r, ok := m.detailRow(); !ok || r.vm == nil || len(digit) != 1 {
		return
	}
	if i := int(digit[0] - '0'); i >= 0 && i < len(perfPages) {
		m.perfPage = i
	}
}

// fieldMark is a health rule's verdict on one detail field, drawn after its
// value. Only fields a rule actually judges get one.
type fieldMark struct {
	status rowStatus
	note   string
}

// toolsOutdated mirrors the statuses the tools-outdated health rule flags.
var toolsOutdated = map[string]bool{
	"guestToolsNeedUpgrade":  true,
	"guestToolsSupportedOld": true,
	"guestToolsTooOld":       true,
	"guestToolsBlacklisted":  true,
}

// vmFieldMarks judges a VM's fields with the same rules and default
// thresholds as the health pane (internal/health), so a glyph here never
// disagrees with a finding there. Missing evidence gets no mark at all: an
// empty Tools state, or a VM read without its configuration, is not a pass.
func vmFieldMarks(vm vsphere.VM, now time.Time) map[string]fieldMark {
	th := health.DefaultThresholds()
	marks := map[string]fieldMark{}

	switch {
	case vm.ToolsVersionStatus == "guestToolsNotInstalled":
		marks["VMware Tools"] = fieldMark{statusWarn, "not installed"}
	case vm.PowerState == "poweredOn" && vm.ToolsState != "" && vm.ToolsState != "guestToolsRunning":
		marks["VMware Tools"] = fieldMark{statusWarn, "not running"}
	case toolsOutdated[vm.ToolsVersionStatus]:
		marks["VMware Tools"] = fieldMark{statusWarn, "needs upgrade"}
	case vm.ToolsState == "guestToolsRunning":
		marks["VMware Tools"] = fieldMark{statusGood, ""}
	}

	if len(vm.Snapshots) > 0 {
		oldest := oldestSnapshot(vm.Snapshots)
		if !oldest.CreateTime.IsZero() {
			age := now.Sub(oldest.CreateTime)
			mark := fieldMark{statusGood, ageWords(age) + " old"}
			if th.SnapshotAge > 0 && age >= th.SnapshotAge {
				mark.status = statusWarn
			}
			marks["Snapshots"] = mark
		}
	} else if vm.ConfigurationAvailable {
		marks["Snapshots"] = fieldMark{statusGood, ""}
	}

	if p, ok := fullestPartition(vm.Partitions); ok {
		mark := fieldMark{statusGood, ""}
		if free := freePercent(p); th.GuestDiskFreePct > 0 && free < th.GuestDiskFreePct {
			mark = fieldMark{statusWarn, fmt.Sprintf("%.0f%% free", free)}
		}
		marks["Guest disks"] = mark
	}
	return marks
}

func oldestSnapshot(snaps []vsphere.VMSnapshot) vsphere.VMSnapshot {
	oldest := snaps[0]
	for _, s := range snaps[1:] {
		if !s.CreateTime.IsZero() && (oldest.CreateTime.IsZero() || s.CreateTime.Before(oldest.CreateTime)) {
			oldest = s
		}
	}
	return oldest
}

func fullestPartition(parts []vsphere.VMPartition) (vsphere.VMPartition, bool) {
	var best vsphere.VMPartition
	found := false
	for _, p := range parts {
		if p.CapacityBytes <= 0 {
			continue
		}
		if !found || freePercent(p) < freePercent(best) {
			best, found = p, true
		}
	}
	return best, found
}

func freePercent(p vsphere.VMPartition) float64 {
	free := float64(p.FreeBytes)
	free = math.Max(0, math.Min(free, float64(p.CapacityBytes)))
	return free / float64(p.CapacityBytes) * 100
}

// snapshotsValue is the Snapshots field: how many, and which is oldest.
// Without configuration the VM was read at summary detail, so an empty list
// is missing evidence rather than none.
func snapshotsValue(vm vsphere.VM) string {
	switch {
	case len(vm.Snapshots) > 0:
		s := fmt.Sprint(len(vm.Snapshots))
		if name := oldestSnapshot(vm.Snapshots).Name; name != "" {
			s += " · oldest " + name
		}
		return s
	case vm.ConfigurationAvailable:
		return "none"
	}
	return "-"
}

// guestDisksValue is the Guest disks field: the fullest filesystem VMware
// Tools reports, since that is the one that runs out first.
func guestDisksValue(vm vsphere.VM) string {
	p, ok := fullestPartition(vm.Partitions)
	if !ok {
		return "-"
	}
	used := 100 - freePercent(p)
	s := fmt.Sprintf("%s %.0f%% used of %s", p.Path, used, humanize.Bytes(p.CapacityBytes))
	if n := len(vm.Partitions); n > 1 {
		s += fmt.Sprintf(" · %d filesystems", n)
	}
	return s
}

func ageWords(d time.Duration) string {
	switch {
	case d < 0:
		return "0m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// markSuffix renders a field's verdict: its glyph, then the note if any.
func (m *Model) markSuffix(mk fieldMark) string {
	t := m.theme
	glyph := glyphCheckOK
	if mk.status == statusWarn {
		glyph = glyphCheckWarn
	} else if mk.status == statusBad {
		glyph = glyphFail
	}
	s := "  " + t.statusStyle(mk.status).Render(glyph)
	if mk.note != "" {
		s += " " + t.statusStyle(mk.status).Render(mk.note)
	}
	return s
}

// vmDetailLines is the whole VM dashboard before scrolling. withActions
// splices the open action popup under its line, as viewDetailRow always has.
func (m *Model) vmDetailLines(r row, withActions bool) []string {
	split := m.width >= dashSplitWidth
	leftW := m.width
	if split {
		leftW = dashLeftWidth
	}
	marks := vmFieldMarks(*r.vm, m.now())
	left := []string{truncate(m.detailHeaderLine(r), leftW), ""}
	for i, f := range r.detail {
		suffix := ""
		if mk, ok := marks[f.label]; ok {
			suffix = m.markSuffix(mk)
		}
		valueW := max(1, leftW-2-labelColumnPad-ansi.StringWidth(suffix))
		f.value = ansi.Truncate(f.value, valueW, "…")
		left = append(left, m.detailFieldLine(2+i, f)+suffix)
	}
	t := m.theme
	for _, n := range r.notes {
		left = append(left, "", t.label.Render("  "+n.label))
		for _, l := range wrap(n.value, leftW-4) {
			left = append(left, "  "+t.value.Render(l))
		}
	}
	if withActions && m.actions != nil {
		left = spliceLines(left, m.detailCursor, m.actionListLines())
	}
	if !split {
		out := append(left, "")
		for _, l := range m.vmDashLines(r, m.width-4) {
			out = append(out, "  "+l)
		}
		return out
	}
	// An open action popup can be wider than the property column; widen the
	// column under it rather than clip the popup, for as long as it is open.
	for _, l := range left {
		leftW = max(leftW, ansi.StringWidth(l))
	}
	rightW := m.width - leftW - ansi.StringWidth(dashRule)
	right := m.vmDashLines(r, rightW)
	n := max(len(left), len(right))
	out := make([]string, n)
	rule := t.faint.Render(dashRule)
	for i := range out {
		var l, rr string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			rr = right[i]
		}
		out[i] = pad(l, leftW, false) + rule + rr
	}
	return out
}

// perfPages are the chart column's pages: an overview of everything, then
// one focused page per area, chosen with 0–4.
var perfPages = []string{"Overview", "CPU", "Memory", "Disk", "Network"}

// vmDashLines is the chart column, w cells wide.
func (m *Model) vmDashLines(r row, w int) []string {
	t := m.theme
	rng := m.perfRange()
	page := clamp(m.perfPage, 0, len(perfPages)-1)
	var pages []string
	for i, name := range perfPages {
		label := fmt.Sprintf("%d %s", i, name)
		if i == page {
			pages = append(pages, t.accent.Render("["+label+"]"))
		} else {
			pages = append(pages, t.dim.Render(" "+label+" "))
		}
	}
	var ranges []string
	for _, pr := range perfRanges {
		if pr.label == rng.label {
			ranges = append(ranges, t.accent.Render("["+pr.label+"]"))
		} else {
			ranges = append(ranges, t.dim.Render(" "+pr.label+" "))
		}
	}
	pageLine := truncate(strings.Join(pages, ""), w)
	rangeLine := strings.Join(ranges, "")
	if _, ok := m.backend.(vmPerfBackend); !ok {
		return []string{pageLine, rangeLine, "", t.dim.Render("Performance charts need a live vCenter connection.")}
	}
	e := m.vmPerf[vmPerfKey(r, rng)]
	source := rng.source
	if e != nil && !e.loading && e.err == nil {
		source += " · as of " + e.asOf.Local().Format("15:04:05")
		if up, ok := lastValue(e.set, perf.SysUptime); ok {
			source = "up " + ageWords(time.Duration(up)*time.Second) + " · " + source
		}
	}
	lines := []string{pageLine, joinEnds(rangeLine, t.dim.Render(source), w), ""}
	switch {
	case e == nil || e.loading:
		return append(lines, t.dim.Render("loading performance…"))
	case e.err != nil:
		for _, l := range wrap("Performance unavailable: "+e.err.Error(), w) {
			lines = append(lines, t.warn.Render(l))
		}
		return lines
	}
	d := dash{m: m, vm: *r.vm, set: e.set, rng: rng, w: w}
	switch page {
	case 1:
		return append(lines, d.cpuPage()...)
	case 2:
		return append(lines, d.memoryPage()...)
	case 3:
		return append(lines, d.diskPage()...)
	case 4:
		return append(lines, d.networkPage()...)
	}
	return append(lines, d.overview()...)
}

// dash renders one VM's chart pages from one loaded SeriesSet.
type dash struct {
	m   *Model
	vm  vsphere.VM
	set perf.SeriesSet
	rng perfRange
	w   int
}

// metric is one plotted reading: its values in the plotted unit, how to
// print a value, and the level from which it draws as a warning (+Inf for
// none). scale fixes the top of a chart; zero scales it to the peak, but
// never below minScale, so a quiet counter does not fill the chart.
type metric struct {
	name     string
	unit     string
	values   []float64
	missing  string
	format   func(float64) string
	scale    float64
	minScale float64
	warnAt   float64
}

func (d dash) series(m perf.Metric) ([]float64, string) {
	s, ok := d.set.Get(m)
	if !ok {
		return nil, "not collected"
	}
	if len(s.Values) == 0 {
		return nil, s.Summary.Reason
	}
	return s.Values, ""
}

// sum adds two series sample by sample, missing only where both are.
func (d dash) sum(a, b perf.Metric) ([]float64, string) {
	x, why := d.series(a)
	y, whyB := d.series(b)
	switch {
	case x == nil && y == nil:
		return nil, why
	case x == nil:
		return y, whyB
	case y == nil:
		return x, why
	}
	out := make([]float64, max(len(x), len(y)))
	for i := range out {
		v, ok := 0.0, false
		if i < len(x) && !math.IsNaN(x[i]) {
			v, ok = v+x[i], true
		}
		if i < len(y) && !math.IsNaN(y[i]) {
			v, ok = v+y[i], true
		}
		if !ok {
			v = math.NaN()
		}
		out[i] = v
	}
	return out, ""
}

func pctOf(values []float64, whole float64) []float64 {
	if values == nil {
		return nil
	}
	out := make([]float64, len(values))
	for i, v := range values {
		if whole <= 0 {
			out[i] = math.NaN()
			continue
		}
		out[i] = v / whole * 100
	}
	return out
}

func fmtPct(v float64) string     { return fmt.Sprintf("%.1f%%", v) }
func fmtMs(v float64) string      { return fmt.Sprintf("%.0f ms", v) }
func fmtMiB(v float64) string     { return fmt.Sprintf("%.0f MiB", v) }
func fmtCount(v float64) string   { return humanCount(v) }
func fmtRate(kbps float64) string { return humanRate(kbps) }

// humanRate prints a KBps value the way people read throughput.
func humanRate(kbps float64) string {
	switch {
	case kbps >= 1024*1024:
		return fmt.Sprintf("%.1f GB/s", kbps/1024/1024)
	case kbps >= 1024:
		return fmt.Sprintf("%.1f MB/s", kbps/1024)
	}
	return fmt.Sprintf("%.0f KB/s", kbps)
}

func humanCount(v float64) string {
	switch {
	case v >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case v >= 1e4:
		return fmt.Sprintf("%.0fk", v/1e3)
	case v >= 1e3:
		return fmt.Sprintf("%.1fk", v/1e3)
	}
	return fmt.Sprintf("%.0f", v)
}

func (d dash) cpuUsage() metric {
	v, why := d.series(perf.CPUUsage)
	return metric{name: "CPU usage", unit: fmt.Sprintf("%% of %d vCPU", d.vm.CPU), values: v, missing: why, format: fmtPct, scale: 100, warnAt: perf.HighPeakPercent}
}

func (d dash) cpuReady() metric {
	v, why := d.series(perf.CPUReady)
	return metric{name: "CPU ready", unit: "% per vCPU", values: v, missing: why, format: fmtPct, minScale: 2 * perf.ReadyContentionPercent, warnAt: perf.ReadyContentionPercent}
}

func (d dash) cpuCostop() metric {
	v, why := d.series(perf.CPUCostop)
	return metric{name: "Co-stop", unit: "% per vCPU", values: v, missing: why, format: fmtPct, minScale: 2 * perf.CostopContentionPercent, warnAt: perf.CostopContentionPercent}
}

func (d dash) cpuLimited() metric {
	v, why := d.series(perf.CPUMaxLimited)
	return metric{name: "CPU limited", unit: "% per vCPU held back by a limit", values: v, missing: why, format: fmtPct, minScale: 5, warnAt: perf.LimitedPercent}
}

// memPct plots a memory counter as a share of configured memory. Only
// active memory is judged: consumed memory sitting near the configured size
// is how a host normally backs a VM, not a sign of demand.
func (d dash) memPct(name string, m perf.Metric) metric {
	v, why := d.series(m)
	warnAt := math.Inf(1)
	if m == perf.MemActive {
		warnAt = perf.HighPeakPercent
	}
	return metric{name: name, unit: "% of " + humanize.MB(d.vm.MemoryMB), values: pctOf(v, float64(d.vm.MemoryMB)), missing: why, format: fmtPct, scale: 100, warnAt: warnAt}
}

func (d dash) memBalloon() metric {
	v, why := d.series(perf.MemBalloon)
	return metric{name: "Balloon", unit: "MiB reclaimed by the balloon driver", values: v, missing: why, format: fmtMiB, minScale: 64, warnAt: perf.MemPressureMiB}
}

func (d dash) memSwapin() metric {
	v, why := d.series(perf.MemSwapinRate)
	return metric{name: "Swap-in", unit: "rate read back from host swap", values: v, missing: why, format: fmtRate, minScale: 64, warnAt: perf.SwapinRateKBps}
}

func (d dash) diskThroughput() metric {
	v, why := d.sum(perf.DiskRead, perf.DiskWrite)
	return metric{name: "Disk", unit: "read + write", values: v, missing: why, format: fmtRate, minScale: 1024, warnAt: math.Inf(1)}
}

func (d dash) disk(name string, m perf.Metric) metric {
	v, why := d.series(m)
	return metric{name: name, values: v, missing: why, format: fmtRate, minScale: 1024, warnAt: math.Inf(1)}
}

func (d dash) diskLatency() metric {
	v, why := d.series(perf.DiskMaxLatency)
	return metric{name: "Max latency", unit: "worst disk, ms", values: v, missing: why, format: fmtMs, minScale: 2 * perf.DiskLatencyHighMs, warnAt: perf.DiskLatencyHighMs}
}

func (d dash) diskIOPS() metric {
	v, why := d.sum(perf.DiskReadIOPS, perf.DiskWriteIOPS)
	return metric{name: "IOPS", unit: "read + write commands per second", values: v, missing: why, format: fmtCount, minScale: 100, warnAt: math.Inf(1)}
}

func (d dash) net(name string, m perf.Metric) metric {
	v, why := d.series(m)
	return metric{name: name, unit: "", values: v, missing: why, format: fmtRate, minScale: 128, warnAt: math.Inf(1)}
}

func (d dash) netDropped() metric {
	v, why := d.sum(perf.NetDroppedRx, perf.NetDroppedTx)
	return metric{name: "Dropped", unit: "packets per sample, rx + tx", values: v, missing: why, format: fmtCount, minScale: 10, warnAt: perf.DroppedPackets}
}

// limitless reports whether the VM has no CPU limit to be held back by, so
// the limited chart would only ever draw zero.
func (d dash) limitless() bool {
	a := d.vm.CPUAllocation
	return a != nil && (a.Limit == nil || *a.Limit < 0)
}

func (d dash) overview() []string {
	m, w := d.m, d.w
	t := m.theme
	var out []string
	out = append(out, m.chart(d.cpuUsage(), d.rng, w)...)
	out = append(out, m.chart(d.memPct("Memory active", perf.MemActive), d.rng, w)...)
	disk := d.diskThroughput()
	lat := d.diskLatency()
	disk.unit = "read + write · latency " + d.peakText(lat)
	if st, ok := statsOf(lat.values); ok && st.peak >= lat.warnAt {
		disk.unit += " " + glyphCheckWarn
	}
	out = append(out, m.chart(disk, d.rng, w)...)
	out = append(out, t.header.Render("CONTENTION"))
	out = append(out, m.metricRow(d.cpuReady(), w), m.metricRow(d.cpuCostop(), w))
	if d.limitless() {
		out = append(out, m.noteRow("CPU limited", "no CPU limit set", w))
	} else {
		out = append(out, m.metricRow(d.cpuLimited(), w))
	}
	out = append(out, m.metricRow(d.memBalloon(), w), m.metricRow(d.memSwapin(), w))
	out = append(out, t.header.Render("NETWORK"))
	out = append(out, m.metricRow(d.net("Received", perf.NetReceived), w), m.metricRow(d.net("Transmitted", perf.NetTransmitted), w), m.metricRow(d.netDropped(), w))
	out = append(out, "")
	return append(out, d.signalLines()...)
}

func (d dash) cpuPage() []string {
	var out []string
	for _, mt := range []metric{d.cpuUsage(), d.cpuReady(), d.cpuCostop()} {
		out = append(out, d.m.chart(mt, d.rng, d.w)...)
	}
	if d.limitless() {
		out = append(out, d.m.noteRow("CPU limited", "no CPU limit set", d.w), "")
	} else {
		out = append(out, d.m.chart(d.cpuLimited(), d.rng, d.w)...)
	}
	return append(out, d.signalLines()...)
}

func (d dash) memoryPage() []string {
	var out []string
	for _, mt := range []metric{d.memPct("Memory active", perf.MemActive), d.memPct("Memory consumed", perf.MemConsumed), d.memBalloon(), d.memSwapin()} {
		out = append(out, d.m.chart(mt, d.rng, d.w)...)
	}
	if s, ok := d.set.Get(perf.MemSwapped); ok && s.Summary.Peak != nil {
		out = append(out, d.m.theme.dim.Render(fmt.Sprintf("swapped out at peak %s, which can be old pages rather than current pressure", fmtMiB(*s.Summary.Peak))))
	}
	return out
}

func (d dash) diskPage() []string {
	var out []string
	for _, mt := range []metric{d.disk("Read", perf.DiskRead), d.disk("Write", perf.DiskWrite), d.diskLatency(), d.diskIOPS()} {
		out = append(out, d.m.chart(mt, d.rng, d.w)...)
	}
	return out
}

func (d dash) networkPage() []string {
	var out []string
	for _, mt := range []metric{d.net("Received", perf.NetReceived), d.net("Transmitted", perf.NetTransmitted), d.netDropped()} {
		out = append(out, d.m.chart(mt, d.rng, d.w)...)
	}
	return out
}

func (d dash) peakText(mt metric) string {
	if st, ok := statsOf(mt.values); ok {
		return mt.format(st.peak)
	}
	return "—"
}

func (d dash) signalLines() []string {
	t := d.m.theme
	glyph, style := glyphCheckOK, t.ok
	switch d.set.Signal {
	case perf.SignalContention:
		glyph, style = glyphCheckWarn, t.warn
	case perf.SignalUnavailable, perf.SignalInsufficient:
		glyph, style = glyphSkip, t.faint
	}
	lines := []string{truncate(t.label.Render("Sizing signal  ")+style.Render(glyph+" "+string(d.set.Signal)), d.w)}
	for _, l := range wrap(d.set.SignalReason, d.w) {
		lines = append(lines, t.dim.Render(l))
	}
	return lines
}

func lastValue(set perf.SeriesSet, m perf.Metric) (float64, bool) {
	s, ok := set.Get(m)
	if !ok {
		return 0, false
	}
	for i := len(s.Values) - 1; i >= 0; i-- {
		if !math.IsNaN(s.Values[i]) {
			return s.Values[i], true
		}
	}
	return 0, false
}

// seriesStats is what a chart's title line and a metric row report. It is
// computed from the plotted values, so a sum or a percentage reports what
// is drawn, and it is gated by the same sample rules as perf.Summarize: too
// few samples, or too little of the window covered, and there is no number.
type seriesStats struct {
	avg, peak, last float64
	p95             float64
	hasP95          bool
}

func statsOf(values []float64) (seriesStats, bool) {
	var present []float64
	for _, v := range values {
		if !math.IsNaN(v) {
			present = append(present, v)
		}
	}
	if len(present) < perf.MinSummarySamples || float64(len(present)) < perf.MinCoverage*float64(len(values)) {
		return seriesStats{}, false
	}
	st := seriesStats{peak: present[0], last: present[len(present)-1]}
	sum := 0.0
	for _, v := range present {
		sum += v
		st.peak = math.Max(st.peak, v)
	}
	st.avg = sum / float64(len(present))
	if len(present) >= perf.MinPercentileSamples {
		sorted := append([]float64(nil), present...)
		sort.Float64s(sorted)
		st.p95, st.hasP95 = sorted[int(math.Ceil(0.95*float64(len(sorted))))-1], true
	}
	return st, true
}

func (mt metric) top(st seriesStats) float64 {
	if mt.scale > 0 {
		return mt.scale
	}
	return math.Max(mt.minScale, st.peak)
}

// chart draws one metric as a chartRows-tall block chart under a title line
// carrying its statistics, with the range's ends beneath.
func (m *Model) chart(mt metric, rng perfRange, w int) []string {
	t := m.theme
	left := t.title.Render(mt.name)
	if mt.unit != "" {
		left += t.dim.Render("  " + mt.unit)
	}
	if len(mt.values) == 0 {
		reason := mt.missing
		if reason == "" {
			reason = "not collected"
		}
		return []string{truncate(left, w), truncate(t.faint.Render("no samples: "+reason), w), ""}
	}
	st, ok := statsOf(mt.values)
	out := []string{m.chartTitle(mt, st, ok, w)}
	out = append(out, m.blockChart(mt.values, w, chartRows, mt.top(st), mt.warnAt)...)
	return append(out, joinEnds(t.faint.Render("-"+rng.label), t.faint.Render("now"), w), "")
}

// chartTitle fits a chart's name, unit and statistics on one line. When
// they do not fit it gives up detail in order of least use: the 95th
// percentile, then the average, then the unit — the peak and the warning
// stay to the last, since they are what the chart is checked for.
func (m *Model) chartTitle(mt metric, st seriesStats, ok bool, w int) string {
	t := m.theme
	name := t.title.Render(mt.name)
	full := name
	if mt.unit != "" {
		full += t.dim.Render("  " + mt.unit)
	}
	if !ok {
		return joinEnds(full, t.faint.Render(string(perf.StatusInsufficient)), w)
	}
	warn := ""
	if st.peak >= mt.warnAt {
		warn = " " + t.warn.Render(glyphCheckWarn)
	}
	peak := "peak " + mt.format(st.peak)
	avg := "avg " + mt.format(st.avg)
	var variants []string
	if st.hasP95 {
		variants = append(variants, avg+" · p95 "+mt.format(st.p95)+" · "+peak)
	}
	variants = append(variants, avg+" · "+peak, peak)
	for _, left := range []string{full, name} {
		for _, v := range variants {
			stats := t.dim.Render(v) + warn
			if ansi.StringWidth(left)+1+ansi.StringWidth(stats) <= w {
				return joinEnds(left, stats, w)
			}
		}
	}
	return truncate(name+" "+t.dim.Render(peak)+warn, w)
}

// metricRow is one line of the overview's compact table: name, a one-row
// sparkline, the latest and peak values, and a verdict where the metric has
// a threshold.
func (m *Model) metricRow(mt metric, w int) string {
	t := m.theme
	const nameW, valW, peakW = 14, 10, 14
	name := pad(mt.name, nameW, false)
	if len(mt.values) == 0 {
		reason := mt.missing
		if reason == "" {
			reason = "not collected"
		}
		return truncate(name+t.faint.Render("—  "+reason), w)
	}
	st, ok := statsOf(mt.values)
	sparkW := max(4, w-nameW-valW-peakW-3)
	spark := m.blockChart(mt.values, sparkW, 1, mt.top(st), mt.warnAt)[0]
	if !ok {
		return truncate(name+spark+"  "+t.faint.Render(string(perf.StatusInsufficient)), w)
	}
	mark := ""
	if !math.IsInf(mt.warnAt, 1) {
		if st.peak >= mt.warnAt {
			mark = " " + t.warn.Render(glyphCheckWarn)
		} else {
			mark = " " + t.ok.Render(glyphCheckOK)
		}
	}
	return truncate(name+spark+" "+pad(mt.format(st.last), valW, true)+t.dim.Render(pad("peak "+mt.format(st.peak), peakW, true))+mark, w)
}

func (m *Model) noteRow(name, note string, w int) string {
	return truncate(pad(name, 14, false)+m.theme.faint.Render("—  "+note), w)
}

// blockChart renders values, oldest first, as h rows of eighth-block glyphs
// w cells wide, newest at the right edge. When there are more values than
// cells each cell shows its bucket's largest value, so a short peak is never
// averaged away; a cell with no samples at all draws a faint dot on the
// baseline instead of a zero.
func (m *Model) blockChart(values []float64, w, h int, scale, warnAt float64) []string {
	t := m.theme
	if w <= 0 || h <= 0 {
		return nil
	}
	cols := bucketMax(values, w)
	lead := w - len(cols)
	glyphs := []rune(" ▁▂▃▄▅▆▇█")
	out := make([]string, h)
	for row := 0; row < h; row++ {
		var b strings.Builder
		b.WriteString(strings.Repeat(" ", lead))
		var run strings.Builder
		var runStyle *lipgloss.Style
		flush := func() {
			if run.Len() == 0 {
				return
			}
			b.WriteString(runStyle.Render(run.String()))
			run.Reset()
		}
		for _, v := range cols {
			style := &t.accent
			var g string
			switch {
			case math.IsNaN(v):
				style, g = &t.faint, " "
				if row == h-1 {
					g = "·"
				}
			default:
				if v >= warnAt {
					style = &t.warn
				}
				level := 0
				if scale > 0 {
					level = int(math.Round(v / scale * float64(h*8)))
				}
				level = clamp(level, 0, h*8)
				if v > 0 && level == 0 {
					level = 1
				}
				fill := clamp(level-(h-1-row)*8, 0, 8)
				g = string(glyphs[fill])
				if row == h-1 && fill == 0 {
					// The bottom row keeps a faint baseline, so a reading
					// of zero still shows the series was there, unlike a
					// gap, which draws a dot.
					style, g = &t.faint, string(glyphs[1])
				}
			}
			if runStyle != style {
				flush()
				runStyle = style
			}
			run.WriteString(g)
		}
		flush()
		out[row] = b.String()
	}
	return out
}

// bucketMax folds values into at most n buckets, each the largest non-NaN
// value it covers, or NaN when it covers nothing but gaps.
func bucketMax(values []float64, n int) []float64 {
	if len(values) <= n {
		return values
	}
	out := make([]float64, n)
	for i := range out {
		lo, hi := i*len(values)/n, (i+1)*len(values)/n
		best := math.NaN()
		for _, v := range values[lo:hi] {
			if !math.IsNaN(v) && (math.IsNaN(best) || v > best) {
				best = v
			}
		}
		out[i] = best
	}
	return out
}
