package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/humanize"
	"github.com/easonliuuuuu/vsfleet/internal/perf"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// The vApp workspace charts a vApp by adding up its member VMs' counters
// rather than reading the vApp's own. On vCenter 8.0.3 a vApp, like any
// resource pool, has no 20-second realtime statistics, only four counters at
// the default statistics level, and 5-minute roll-ups that land 20 minutes or
// more after its VMs' do. A pool's usage is the sum of its running VMs', so
// the members give the same totals sooner, with contention counters besides.

// vappChartMinHeight is the shortest body that gets full charts; below it
// the workspace keeps the members table readable with one-line sparklines.
const vappChartMinHeight = 28

// vappPerfEntry is one vApp's member charts for one range. Like vmPerfEntry
// it keeps the last good read on screen while a re-read runs or fails.
type vappPerfEntry struct {
	gen     uint64
	loading bool
	hasData bool
	// byVM holds each member's series, or why it has none, by VM ID.
	byVM  map[string]vsphere.VMSeriesResult
	start time.Time
	end   time.Time
	asOf  time.Time
	err   error
}

type vappPerfMsg struct {
	key     string
	gen     uint64
	results []vsphere.VMSeriesResult
	err     error
	asOf    time.Time
}

func vappPerfKey(rootKey string, rng perfRange) string { return "vapp|" + rootKey + "|" + rng.label }

// showingVAppPerf reports whether the vApp workspace's charts are on screen.
func (m *Model) showingVAppPerf() bool {
	if m.mode != modeVAppDetail || m.vapp == nil {
		return false
	}
	if _, ok := m.backend.(vmsPerfBackend); !ok {
		return false
	}
	root, st, ok := m.activeVApp()
	return ok && root != nil && st != nil && st.cc != nil
}

// vappPerfVMs is every VM in the vApp's tree, nested vApps included, once
// each: a vApp's limit covers everything beneath it. It stops at what one
// read covers.
func (m *Model) vappPerfVMs(root *vsphere.VApp, inv *vsphere.Inventory) []vsphere.VM {
	var vms []vsphere.VM
	seen := map[string]bool{}
	for _, member := range m.vappMembers(root, inv) {
		if member.vm == nil || seen[member.vm.ID] {
			continue
		}
		seen[member.vm.ID] = true
		vms = append(vms, *member.vm)
	}
	if len(vms) > vsphere.MaxVMsPerfSeries {
		vms = vms[:vsphere.MaxVMsPerfSeries]
	}
	return vms
}

// loadVAppPerf starts a read of the open vApp's member charts for the
// current range, on the same terms as loadVMPerf.
func (m *Model) loadVAppPerf(force bool) tea.Cmd {
	if !m.showingVAppPerf() {
		return nil
	}
	b := m.backend.(vmsPerfBackend)
	root, st, _ := m.activeVApp()
	rng := m.perfRange()
	k := vappPerfKey(m.vapp.roots[len(m.vapp.roots)-1], rng)
	e := m.vappPerf[k]
	if e != nil && e.loading {
		return nil
	}
	if e != nil && !force && e.hasData && e.err == nil {
		if !m.liveRefresh() || m.now().Sub(e.asOf) < rng.every() {
			return nil
		}
	}
	vms := m.vappPerfVMs(root, st.inv)
	if len(vms) == 0 {
		return nil
	}
	if m.vappPerf == nil {
		m.vappPerf = map[string]*vappPerfEntry{}
	}
	if e == nil {
		e = &vappPerfEntry{}
		m.vappPerf[k] = e
	}
	m.vmPerfGen++
	e.gen, e.loading = m.vmPerfGen, true
	gen := e.gen
	ctx, cc, now := m.ctx, st.cc, m.now()
	return func() tea.Msg {
		results, err := b.VMsPerfSeries(ctx, cc, vms, rng.window, rng.interval, now)
		return vappPerfMsg{key: k, gen: gen, results: results, err: err, asOf: now}
	}
}

func (m *Model) applyVAppPerf(msg vappPerfMsg) {
	e := m.vappPerf[msg.key]
	if e == nil || e.gen != msg.gen {
		return
	}
	e.loading = false
	e.err = msg.err
	if msg.err != nil {
		return
	}
	e.byVM = make(map[string]vsphere.VMSeriesResult, len(msg.results))
	e.start, e.end = time.Time{}, time.Time{}
	for _, r := range msg.results {
		e.byVM[r.VM.ID] = r
		if e.start.IsZero() && !r.Set.WindowStart.IsZero() {
			e.start, e.end = r.Set.WindowStart, r.Set.WindowEnd
		}
	}
	e.asOf, e.hasData = msg.asOf, true
}

// currentVAppPerf is the open vApp's entry for the current range, if any.
func (m *Model) currentVAppPerf() *vappPerfEntry {
	if m.vapp == nil || len(m.vapp.roots) == 0 {
		return nil
	}
	return m.vappPerf[vappPerfKey(m.vapp.roots[len(m.vapp.roots)-1], m.perfRange())]
}

// vappTotals adds one metric across the members, sample by sample. A sample
// is missing only when no member has it; a powered-off member, which has no
// samples, simply adds nothing. reporting counts the members that had any.
func (e *vappPerfEntry) totals(metric perf.Metric) (values []float64, reporting int) {
	for _, r := range e.byVM {
		if r.Err != nil {
			continue
		}
		s, ok := r.Set.Get(metric)
		if !ok || len(s.Values) == 0 {
			continue
		}
		any := false
		if values == nil {
			values = make([]float64, len(s.Values))
			for i := range values {
				values[i] = math.NaN()
			}
		}
		for i, v := range s.Values {
			if i >= len(values) || math.IsNaN(v) {
				continue
			}
			if math.IsNaN(values[i]) {
				values[i] = 0
			}
			values[i] += v
			any = true
		}
		if any {
			reporting++
		}
	}
	return values, reporting
}

// failed counts the members whose read failed.
func (e *vappPerfEntry) failed() int {
	n := 0
	for _, r := range e.byVM {
		if r.Err != nil {
			n++
		}
	}
	return n
}

// memberPeak is the highest plotted value of one member's metric, when the
// member has enough samples for one.
func (e *vappPerfEntry) memberPeak(vmID string, metric perf.Metric) (float64, bool) {
	if e == nil {
		return 0, false
	}
	r, ok := e.byVM[vmID]
	if !ok || r.Err != nil {
		return 0, false
	}
	s, ok := r.Set.Get(metric)
	if !ok {
		return 0, false
	}
	st, ok := statsOf(s.Values)
	return st.peak, ok
}

func fmtMHz(v float64) string {
	if v < 1000 {
		return fmt.Sprintf("%.0f MHz", v)
	}
	return humanize.MHz(int64(math.Round(v)))
}

// vappCeiling is what the members could use at most: their vCPUs at their
// hosts' core speed, and their configured memory. It is the top of a chart
// for a vApp without a limit, so a steady sum does not fill the chart the way
// scaling to its own peak would. Powered-off members count too: the chart
// covers a window in which they may have run. A member on a host the
// inventory does not describe adds no CPU, so cpuMHz can be zero.
func vappCeiling(vms []vsphere.VM, inv *vsphere.Inventory) (cpuMHz, memMB float64) {
	coreMHz := map[string]int32{}
	if inv != nil {
		for _, h := range inv.Hosts {
			coreMHz[h.Name] = h.CPUMHz
		}
	}
	for _, vm := range vms {
		cpuMHz += float64(vm.CPU) * float64(coreMHz[vm.Host])
		memMB += float64(vm.MemoryMB)
	}
	return cpuMHz, memMB
}

// vappCPU is the members' summed CPU usage, judged against the vApp's CPU
// limit: usage cannot pass a limit, so reaching it is the sign of a vApp
// holding its members back. The chart's top is the limit when there is one,
// so a capped vApp at its ceiling fills it, and otherwise what the running
// members could use.
func vappCPU(e *vappPerfEntry, v *vsphere.VApp, members int, ceiling float64) metric {
	values, reporting := e.totals(perf.CPUUsageMHz)
	mt := metric{name: "CPU usage", values: values, format: fmtMHz, minScale: 100, warnAt: math.Inf(1)}
	mt.unit = fmt.Sprintf("%d of %d members", reporting, members)
	if limit, ok := allocationLimit(v, true); ok {
		mt.unit += " · limit " + fmtMHz(float64(limit))
		mt.scale, mt.warnAt = float64(limit), 0.95*float64(limit)
	} else if ceiling > 0 {
		mt.unit += " · of " + fmtMHz(ceiling)
		mt.scale = ceiling
	}
	if values == nil {
		mt.missing = "no member reported CPU usage in this range"
	}
	return mt
}

// vappMemory is the members' summed active memory, against the vApp's
// memory limit when it has one, or else their configured memory; its
// reservation is context, not a ceiling.
func vappMemory(e *vappPerfEntry, v *vsphere.VApp, ceiling float64) metric {
	values, _ := e.totals(perf.MemActive)
	mt := metric{name: "Memory active", values: values, format: fmtMB, minScale: 256, warnAt: math.Inf(1)}
	if limit, ok := allocationLimit(v, false); ok {
		mt.unit = "limit " + humanize.MB(limit)
		mt.scale, mt.warnAt = float64(limit), 0.95*float64(limit)
	} else if ceiling > 0 {
		mt.unit = "of " + humanize.MB(int64(ceiling))
		mt.scale = ceiling
	}
	if a := v.Allocation; a != nil && a.MemReservationMB != nil && *a.MemReservationMB > 0 {
		mt.unit = strings.TrimPrefix(mt.unit+" · reserved "+humanize.MB(*a.MemReservationMB), " · ")
	}
	if values == nil {
		mt.missing = "no member reported active memory in this range"
	}
	return mt
}

func fmtMB(v float64) string { return humanize.MB(int64(math.Round(v))) }

// allocationLimit is the vApp's CPU (MHz) or memory (MB) limit, when it has
// one: not unlimited, and recorded.
func allocationLimit(v *vsphere.VApp, cpu bool) (int64, bool) {
	if v == nil || v.Allocation == nil {
		return 0, false
	}
	limit := v.Allocation.MemLimitMB
	if cpu {
		limit = v.Allocation.CPULimitMHz
	}
	if limit == nil || *limit < 0 {
		return 0, false
	}
	return *limit, true
}

// perfRangeStrip is the range selector shared by the VM and vApp panes.
func (m *Model) perfRangeStrip() string {
	t := m.theme
	rng := m.perfRange()
	var ranges []string
	for _, pr := range perfRanges {
		if pr.label == rng.label {
			ranges = append(ranges, t.accent.Render("["+pr.label+"]"))
		} else {
			ranges = append(ranges, t.dim.Render(" "+pr.label+" "))
		}
	}
	return strings.Join(ranges, "")
}

// vappPerfLines is the workspace's Performance section, w cells wide, with
// every line cut to fit.
func (m *Model) vappPerfLines(root *vsphere.VApp, inv *vsphere.Inventory, w int) []string {
	lines := m.vappPerfSection(root, inv, w)
	for i := range lines {
		lines[i] = truncate(lines[i], w)
	}
	return lines
}

func (m *Model) vappPerfSection(root *vsphere.VApp, inv *vsphere.Inventory, w int) []string {
	vms := m.vappPerfVMs(root, inv)
	t := m.theme
	rng := m.perfRange()
	header := t.header.Render("Performance")
	if _, ok := m.backend.(vmsPerfBackend); !ok {
		// A backend without live queries keeps the workspace as it was.
		return nil
	}
	if len(vms) == 0 {
		return []string{header, truncate(t.dim.Render("  no member VMs to chart"), w)}
	}
	e := m.currentVAppPerf()
	source := rng.source
	if !m.liveRefresh() {
		source = strings.Replace(source, "live · ", "", 1)
	}
	if e != nil && e.hasData {
		source += " · as of " + e.asOf.Local().Format("15:04:05")
	}
	lines := []string{header, "  " + joinEnds(m.perfRangeStrip(), t.dim.Render(source), w-2)}
	inner := w - 2
	indent := func(ls []string) []string {
		for i := range ls {
			ls[i] = "  " + ls[i]
		}
		return ls
	}
	switch {
	case e == nil || (!e.hasData && e.loading):
		return append(lines, indent([]string{t.dim.Render("loading member performance…")})...)
	case !e.hasData:
		var out []string
		for _, l := range wrap("Performance unavailable: "+e.err.Error(), inner) {
			out = append(out, t.warn.Render(l))
		}
		return append(lines, indent(out)...)
	case e.err != nil:
		var out []string
		for _, l := range wrap("Refresh failed, showing the read from "+e.asOf.Local().Format("15:04:05")+": "+e.err.Error(), inner) {
			out = append(out, t.warn.Render(l))
		}
		lines = append(lines, indent(out)...)
	}
	if n := e.failed(); n > 0 {
		lines = append(lines, indent([]string{t.warn.Render(fmt.Sprintf("%d of %d members could not be read; the totals leave them out", n, len(e.byVM)))})...)
	}
	cpuTop, memTop := vappCeiling(vms, inv)
	cpu, mem := vappCPU(e, root, len(e.byVM), cpuTop), vappMemory(e, root, memTop)
	if w < dashSplitWidth || m.bodyHeight() < vappChartMinHeight {
		return append(lines, indent([]string{m.metricRow(cpu, inner), m.metricRow(mem, inner)})...)
	}
	// Two charts side by side, CPU beside memory.
	ruleW := ansi.StringWidth(dashRule)
	half := (inner - ruleW) / 2
	d := dash{m: m, set: perf.SeriesSet{WindowStart: e.start, WindowEnd: e.end}, rng: rng, w: half}
	left, right := d.chart(cpu), d.chart(mem)
	rule := t.faint.Render(dashRule)
	for i := 0; i < max(len(left), len(right)); i++ {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		lines = append(lines, "  "+pad(l, half, false)+rule+r)
	}
	// d.chart ends each chart with a blank line; the section adds its own.
	return lines[:len(lines)-1]
}

// vappMemberPerfCells are a member row's performance cells: peak CPU in MHz,
// and peak ready and limited time per vCPU, each marked when past the
// dashboard's threshold. Rows that are not VMs, and VMs
// without samples in the range, get dashes.
func (m *Model) vappMemberPerfCells(member vappMember) []string {
	e := m.currentVAppPerf()
	if member.vm == nil || e == nil || !e.hasData {
		return []string{"-", "-", "-"}
	}
	cell := func(metric perf.Metric, warnAt float64) string {
		peak, ok := e.memberPeak(member.vm.ID, metric)
		if !ok {
			return "-"
		}
		s := fmtPct(peak)
		if peak >= warnAt {
			s += glyphCheckWarn
		}
		return s
	}
	// Peak CPU is in MHz, the unit the vApp's limit is in and the busiest
	// sort uses; it is marked when the VM was busy for its own vCPUs.
	cpu := "-"
	if mhz, ok := e.memberPeak(member.vm.ID, perf.CPUUsageMHz); ok {
		cpu = fmtMHz(mhz)
		if pct, ok := e.memberPeak(member.vm.ID, perf.CPUUsage); ok && pct >= perf.HighPeakPercent {
			cpu += glyphCheckWarn
		}
	}
	return []string{
		cpu,
		cell(perf.CPUReady, perf.ReadyContentionPercent),
		cell(perf.CPUMaxLimited, perf.LimitedPercent),
	}
}

// sortBusiest orders VM members by their peak CPU in MHz, busiest first;
// members without a reading keep their order after the rest.
func (m *Model) sortBusiest(members []vappMember) {
	e := m.currentVAppPerf()
	peak := func(v vappMember) (float64, bool) {
		if v.vm == nil {
			return 0, false
		}
		return e.memberPeak(v.vm.ID, perf.CPUUsageMHz)
	}
	sort.SliceStable(members, func(i, j int) bool {
		a, okA := peak(members[i])
		b, okB := peak(members[j])
		if okA != okB {
			return okA
		}
		return okA && a > b
	})
}
