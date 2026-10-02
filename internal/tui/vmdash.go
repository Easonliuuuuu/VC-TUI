package tui

import (
	"fmt"
	"math"
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

// vmDashLines is the chart column, w cells wide.
func (m *Model) vmDashLines(r row, w int) []string {
	t := m.theme
	rng := m.perfRange()
	var tabs []string
	for _, pr := range perfRanges {
		if pr.label == rng.label {
			tabs = append(tabs, t.accent.Render("["+pr.label+"]"))
		} else {
			tabs = append(tabs, t.dim.Render(" "+pr.label+" "))
		}
	}
	head := strings.Join(tabs, "")
	if _, ok := m.backend.(vmPerfBackend); !ok {
		return []string{head, "", t.dim.Render("Performance charts need a live vCenter connection.")}
	}
	e := m.vmPerf[vmPerfKey(r, rng)]
	source := rng.source
	if e != nil && !e.loading && e.err == nil {
		source += " · as of " + e.asOf.Local().Format("15:04:05")
	}
	lines := []string{joinEnds(head, t.dim.Render(source), w), ""}
	switch {
	case e == nil || e.loading:
		return append(lines, t.dim.Render("loading performance…"))
	case e.err != nil:
		for _, l := range wrap("Performance unavailable: "+e.err.Error(), w) {
			lines = append(lines, t.warn.Render(l))
		}
		return lines
	}
	vm := *r.vm
	set := e.set
	ident := func(v float64) float64 { return v }

	cpu, _ := set.Get(perf.CPUUsage)
	lines = append(lines, m.chart("CPU usage", fmt.Sprintf("%% of %d vCPU", vm.CPU), cpu, ident, "%", 100, perf.HighPeakPercent, rng, w)...)

	ready, _ := set.Get(perf.CPUReady)
	readyScale := 2 * perf.ReadyContentionPercent
	if p := ready.Summary.Peak; p != nil && *p > readyScale {
		readyScale = *p
	}
	lines = append(lines, m.chart("CPU ready", "% per vCPU", ready, ident, "%", readyScale, perf.ReadyContentionPercent, rng, w)...)

	active, _ := set.Get(perf.MemActive)
	toPct := func(v float64) float64 {
		if vm.MemoryMB <= 0 {
			return math.NaN()
		}
		return v / float64(vm.MemoryMB) * 100
	}
	lines = append(lines, m.chart("Memory active", "% of "+humanize.MB(vm.MemoryMB), active, toPct, "%", 100, perf.HighPeakPercent, rng, w)...)
	lines = append(lines, m.pressureLine(set, w), "")

	glyph, style := glyphCheckOK, t.ok
	switch set.Signal {
	case perf.SignalContention:
		glyph, style = glyphCheckWarn, t.warn
	case perf.SignalUnavailable, perf.SignalInsufficient:
		glyph, style = glyphSkip, t.faint
	}
	signal := t.label.Render("Sizing signal  ") + style.Render(glyph+" "+string(set.Signal))
	lines = append(lines, truncate(signal, w))
	for _, l := range wrap(set.SignalReason, w) {
		lines = append(lines, t.dim.Render(l))
	}
	return lines
}

// pressureLine reports ballooning and swapping peaks, which contention makes
// matter more than any chart: a starved VM can look idle.
func (m *Model) pressureLine(set perf.SeriesSet, w int) string {
	t := m.theme
	part := func(name string, metric perf.Metric) string {
		s, ok := set.Get(metric)
		if !ok || s.Summary.Peak == nil {
			return name + " " + t.faint.Render("—")
		}
		v := *s.Summary.Peak
		style := t.text
		if v >= perf.MemPressureMiB {
			style = t.warn
		}
		return name + " " + style.Render(fmt.Sprintf("%.0f MiB", v))
	}
	return truncate(t.dim.Render("peak ")+part("balloon", perf.MemBalloon)+t.dim.Render(" · ")+part("swap", perf.MemSwapped), w)
}

// chart draws one series as a chartRows-tall block chart under a title line
// carrying its summary, with the range's ends beneath. Values are converted
// by conv into the plotted unit; scale is the top of the chart and warnAt
// the level from which a column takes the warning colour.
func (m *Model) chart(title, unit string, s perf.Series, conv func(float64) float64, suffix string, scale, warnAt float64, rng perfRange, w int) []string {
	t := m.theme
	left := t.title.Render(title) + t.dim.Render("  "+unit)
	var stats string
	sum := s.Summary
	switch {
	case sum.Status == perf.StatusOK && sum.Average != nil && sum.Peak != nil:
		parts := []string{fmt.Sprintf("avg %.1f%s", conv(*sum.Average), suffix)}
		if sum.P95 != nil {
			parts = append(parts, fmt.Sprintf("p95 %.1f%s", conv(*sum.P95), suffix))
		}
		parts = append(parts, fmt.Sprintf("peak %.1f%s", conv(*sum.Peak), suffix))
		stats = t.dim.Render(strings.Join(parts, " · "))
	case sum.Status != "":
		stats = t.faint.Render(string(sum.Status))
	}
	out := []string{joinEnds(left, stats, w)}
	if len(s.Values) == 0 {
		reason := sum.Reason
		if reason == "" {
			reason = "not collected"
		}
		return append(out, truncate(t.faint.Render("no samples: "+reason), w), "")
	}
	values := make([]float64, len(s.Values))
	for i, v := range s.Values {
		values[i] = conv(v)
	}
	out = append(out, m.blockChart(values, w, chartRows, scale, warnAt)...)
	out = append(out, joinEnds(t.faint.Render("-"+rng.label), t.faint.Render("now"), w), "")
	return out
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
