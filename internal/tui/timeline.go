package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/humanize"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// The VM timeline has three sources, one tab each. Changes is the stored
// assessment history: what changed between runs, offline, as far back as the
// history goes. vCenter events is the VM's live event log: who did what and
// when, bounded by what vCenter still keeps. Combined lines the two up, so a
// stored change can be read next to the event that caused it.
//
// The two sources stay visibly separate. Nothing on the Changes tab depends
// on a vCenter, and nothing on the events tab is read from the store.
const (
	timelineSourceChanges = iota
	timelineSourceEvents
	timelineSourceCombined
	timelineSourceCount
)

var timelineSourceNames = [timelineSourceCount]string{"Changes", "vCenter events", "Combined"}

// vmEventsBackend is the live-query extension behind the timeline's vCenter
// events. Like vmPerfBackend it is optional and type-asserted: a backend
// without it shows the events tabs with a note instead of failing. The query
// is read-only and reads one VM's event log in one request.
type vmEventsBackend interface {
	VMEvents(ctx context.Context, cc *config.Context, vmID string, limit int) (vsphere.VMEventListing, error)
}

// vmEventsTarget is one VM to read events for: the lineage's managed object
// reference on one vCenter. A VM that moved between vCenters has one target
// per vCenter it was seen on.
type vmEventsTarget struct {
	context string
	vmID    string
}

// vmEventsResult is one target's read: its listing, or why there is none.
type vmEventsResult struct {
	target  vmEventsTarget
	listing vsphere.VMEventListing
	err     error
}

// timelineEventsState is the vCenter events read for the open timeline.
type timelineEventsState struct {
	loading  bool
	loaded   bool
	results  []vmEventsResult
	loadedAt time.Time
}

type vmEventsMsg struct {
	generation int
	results    []vmEventsResult
	at         time.Time
}

// timelineRow is one line of the Combined tab. Header and note rows are not
// selectable; change and event rows are, and enter opens their detail.
type timelineRow struct {
	header string
	note   string
	change *assessment.VMHistoryEvent
	event  *vsphere.VMEvent
	// explains marks an event row that accounts for the change above it.
	explains bool
	// inline is the event that caused a change row's change, drawn beside
	// it on a wide terminal; inlineNote says why there is none.
	inline     *vsphere.VMEvent
	inlineNote string
}

func (r timelineRow) selectable() bool { return r.change != nil || r.event != nil }

// openTimeline opens the VM timeline for query, remembering which screen to
// return to. seed is the live VM the timeline was opened from, when there is
// one: its context and managed object reference let the events tabs read
// vCenter even before (or without) stored history naming it.
func (m *Model) openTimeline(query string, from mode, seed *vmEventsTarget) tea.Cmd {
	if m.assessment == nil {
		return nil
	}
	m.timelineQuery = query
	m.timelineAll, m.timelineMinor = false, false
	m.timelineCursor, m.timelineOffset = 0, 0
	m.eventsCursor, m.combinedCursor = 0, 0
	m.timeline, m.timelineFull = nil, nil
	m.timelineSeed = seed
	m.tlEvents = nil
	m.tlGen++
	m.historyErr = nil
	m.timelineFrom = from
	m.mode = modeHistoryTimeline
	cmds := []tea.Cmd{loadHistoryTimelineCmd(m.ctx, m.assessment, query, true, false)}
	if m.timelineSource != timelineSourceChanges {
		cmds = append(cmds, m.loadTimelineEvents(false))
	}
	return tea.Batch(cmds...)
}

// applyHistoryTimeline stores a loaded timeline. The store is always asked
// for every observation: the Combined tab draws its spans between every run
// that saw the VM, and the Changes tab filters the unchanged ones out unless
// "a" asks for them.
func (m *Model) applyHistoryTimeline(msg historyTimelineMsg) tea.Cmd {
	m.timelineFull, m.historyErr = msg.events, msg.err
	m.refilterTimeline()
	m.timelineCursor, m.timelineOffset = 0, 0
	if m.mode == modeHistoryTimeline && m.timelineSource != timelineSourceChanges {
		return m.loadTimelineEvents(false)
	}
	return nil
}

func (m *Model) refilterTimeline() {
	m.timeline = m.timeline[:0]
	for _, e := range m.timelineFull {
		if e.Kind == "observed" && !m.timelineAll {
			continue
		}
		m.timeline = append(m.timeline, e)
	}
	m.timelineCursor = clamp(m.timelineCursor, 0, max(0, len(m.timeline)-1))
}

// timelineTargets is every VM to read events for: the newest stored
// observation of the lineage on each vCenter, and the live VM the timeline
// was opened from, which wins for its own vCenter.
func (m *Model) timelineTargets() []vmEventsTarget {
	latest := map[string]assessment.VMHistoryEvent{}
	for _, e := range m.timelineFull {
		if e.Observation == nil || e.Observation.VM.ID == "" {
			continue
		}
		if cur, ok := latest[e.Context]; !ok || !e.Run.StartedAt.Before(cur.Run.StartedAt) {
			latest[e.Context] = e
		}
	}
	byContext := map[string]vmEventsTarget{}
	for ctx, e := range latest {
		byContext[ctx] = vmEventsTarget{context: ctx, vmID: e.Observation.VM.ID}
	}
	if s := m.timelineSeed; s != nil && s.vmID != "" {
		byContext[s.context] = *s
	}
	out := make([]vmEventsTarget, 0, len(byContext))
	for _, t := range byContext {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].context < out[j].context })
	return out
}

// targetState reports whether a target's vCenter is configured here and
// connected right now.
func (m *Model) targetState(t vmEventsTarget) (cc *config.Context, connected bool) {
	st := m.byName[t.context]
	if st == nil || st.cc == nil {
		return nil, false
	}
	return st.cc, st.inv != nil && !st.loggedOut && st.err == nil
}

// loadTimelineEvents starts a read of the open timeline's vCenter events.
// Opening an events tab reads only vCenters that are already connected;
// explicit (r) also connects the configured ones that are not, because that
// is the operator asking for it. Nothing reads events in the background.
func (m *Model) loadTimelineEvents(explicit bool) tea.Cmd {
	b, ok := m.backend.(vmEventsBackend)
	if !ok {
		return nil
	}
	if m.tlEvents != nil && (m.tlEvents.loading || (m.tlEvents.loaded && !explicit)) {
		return nil
	}
	type job struct {
		target vmEventsTarget
		cc     *config.Context
	}
	var jobs []job
	for _, t := range m.timelineTargets() {
		cc, connected := m.targetState(t)
		if cc == nil || (!connected && !explicit) {
			continue
		}
		jobs = append(jobs, job{target: t, cc: cc})
	}
	if len(jobs) == 0 {
		return nil
	}
	if m.tlEvents == nil {
		m.tlEvents = &timelineEventsState{}
	}
	m.tlEvents.loading = true
	gen, ctx, now := m.tlGen, m.ctx, m.now
	load := func() tea.Msg {
		results := make([]vmEventsResult, len(jobs))
		for i, j := range jobs {
			listing, err := b.VMEvents(ctx, j.cc, j.target.vmID, vsphere.DefaultVMEventLimit)
			results[i] = vmEventsResult{target: j.target, listing: listing, err: err}
		}
		return vmEventsMsg{generation: gen, results: results, at: now()}
	}
	return tea.Batch(load, m.spin.Tick)
}

func (m *Model) applyVMEvents(msg vmEventsMsg) {
	if msg.generation != m.tlGen || m.tlEvents == nil {
		return
	}
	// A reload that only reached some vCenters keeps the others' last read.
	merged := map[vmEventsTarget]vmEventsResult{}
	for _, r := range m.tlEvents.results {
		merged[r.target] = r
	}
	for _, r := range msg.results {
		merged[r.target] = r
	}
	m.tlEvents.results = m.tlEvents.results[:0]
	for _, r := range merged {
		m.tlEvents.results = append(m.tlEvents.results, r)
	}
	sort.Slice(m.tlEvents.results, func(i, j int) bool {
		return m.tlEvents.results[i].target.context < m.tlEvents.results[j].target.context
	})
	m.tlEvents.loading, m.tlEvents.loaded, m.tlEvents.loadedAt = false, true, msg.at
	m.eventsCursor = clamp(m.eventsCursor, 0, max(0, len(m.visibleEvents())-1))
}

// timelineEvents is every event read, from every vCenter, oldest first.
func (m *Model) timelineEvents() []vsphere.VMEvent {
	if m.tlEvents == nil {
		return nil
	}
	var out []vsphere.VMEvent
	for _, r := range m.tlEvents.results {
		if r.err == nil {
			out = append(out, r.listing.Events...)
		}
	}
	vsphere.SortVMEvents(out)
	return out
}

// visibleEvents is what the events tab lists: routine events only when "a"
// asked for them.
func (m *Model) visibleEvents() []vsphere.VMEvent {
	all := m.timelineEvents()
	if m.timelineMinor {
		return all
	}
	out := all[:0:0]
	for _, e := range all {
		if !e.Minor {
			out = append(out, e)
		}
	}
	return out
}

func (m *Model) eventContexts() int {
	seen := map[string]bool{}
	for _, e := range m.timelineEvents() {
		seen[e.Context] = true
	}
	return len(seen)
}

func (m *Model) handleHistoryTimelineKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Back):
		m.mode = m.timelineFrom
		return nil
	case key.Matches(msg, m.keys.TimelineSource):
		if s := msg.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '3' {
			return m.switchTimelineSource(int(s[0] - '1'))
		}
		return m.switchTimelineSource((m.timelineSource + 1) % timelineSourceCount)
	case key.Matches(msg, m.keys.PrevPane):
		return m.switchTimelineSource((m.timelineSource + timelineSourceCount - 1) % timelineSourceCount)
	case key.Matches(msg, m.keys.Reload):
		return m.loadTimelineEvents(true)
	case key.Matches(msg, m.keys.TimelineAll):
		if m.timelineSource == timelineSourceChanges {
			m.timelineAll = !m.timelineAll
			m.refilterTimeline()
		} else {
			m.timelineMinor = !m.timelineMinor
			m.eventsCursor = clamp(m.eventsCursor, 0, max(0, len(m.visibleEvents())-1))
			m.combinedCursor = clamp(m.combinedCursor, 0, max(0, m.combinedSelectable()-1))
		}
		return nil
	case key.Matches(msg, m.keys.Up):
		m.moveTimelineCursor(-1)
	case key.Matches(msg, m.keys.Down):
		m.moveTimelineCursor(1)
	case key.Matches(msg, m.keys.Open):
		m.openTimelineDetail()
	}
	return nil
}

func (m *Model) switchTimelineSource(source int) tea.Cmd {
	m.timelineSource = clamp(source, 0, timelineSourceCount-1)
	if m.timelineSource == timelineSourceChanges {
		return nil
	}
	return m.loadTimelineEvents(false)
}

func (m *Model) moveTimelineCursor(delta int) {
	switch m.timelineSource {
	case timelineSourceEvents:
		m.eventsCursor = clamp(m.eventsCursor+delta, 0, max(0, len(m.visibleEvents())-1))
	case timelineSourceCombined:
		m.combinedCursor = clamp(m.combinedCursor+delta, 0, max(0, m.combinedSelectable()-1))
	default:
		m.timelineCursor = clamp(m.timelineCursor+delta, 0, max(0, len(m.timeline)-1))
	}
}

func (m *Model) openTimelineDetail() {
	m.timelineDetailEvent, m.timelineDetailChange = nil, nil
	switch m.timelineSource {
	case timelineSourceEvents:
		events := m.visibleEvents()
		if m.eventsCursor >= len(events) {
			return
		}
		e := events[m.eventsCursor]
		m.timelineDetailEvent = &e
	case timelineSourceCombined:
		row, ok := m.selectedCombinedRow()
		if !ok {
			return
		}
		m.timelineDetailEvent, m.timelineDetailChange = row.event, row.change
	default:
		if len(m.timeline) == 0 || m.timelineCursor >= len(m.timeline) {
			return
		}
	}
	m.mode = modeHistoryTimelineDetail
}

// selectedTimelineChange is the stored change the detail screen shows: the
// one picked on the Combined tab, or the Changes tab's cursor row.
func (m *Model) selectedTimelineChange() (assessment.VMHistoryEvent, bool) {
	if m.timelineDetailChange != nil {
		return *m.timelineDetailChange, true
	}
	if m.timelineCursor >= 0 && m.timelineCursor < len(m.timeline) {
		return m.timeline[m.timelineCursor], true
	}
	return assessment.VMHistoryEvent{}, false
}

// ---- rendering ----

func (m *Model) viewHistoryTimeline() []string {
	t := m.theme
	lines := []string{"  " + m.timelineTabs(), m.timelineSubtitle(), ""}
	var body []string
	cursor := 0
	switch m.timelineSource {
	case timelineSourceEvents:
		body, cursor = m.viewTimelineEvents()
	case timelineSourceCombined:
		body, cursor = m.viewTimelineCombined()
	default:
		if m.historyErr != nil {
			body = []string{t.warn.Render("  " + m.historyErr.Error())}
		} else if len(m.timeline) == 0 {
			body = []string{t.dim.Render("  no timeline events")}
		} else {
			body, cursor = m.viewTimelineChanges()
		}
	}
	h := m.bodyHeight() - len(lines)
	offset := 0
	if cursor >= h {
		offset = cursor - h + 1
	}
	out := append(lines, scrollLines(body, offset, max(1, h))...)
	for i := range out {
		out[i] = truncate(out[i], max(1, m.width))
	}
	return out
}

func (m *Model) timelineTabs() string {
	t := m.theme
	parts := make([]string, timelineSourceCount)
	for i, name := range timelineSourceNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		if i == m.timelineSource {
			parts[i] = t.tabOn.Render("[" + label + "]")
		} else {
			parts[i] = t.tabOff.Render(" " + label + " ")
		}
	}
	return truncate(strings.Join(parts, " "), max(1, m.width-2))
}

func (m *Model) timelineSubtitle() string {
	t := m.theme
	w := max(1, m.width-2)
	switch m.timelineSource {
	case timelineSourceChanges:
		runs := map[int64]bool{}
		for _, e := range m.timelineFull {
			runs[e.Run.ID] = true
		}
		s := fmt.Sprintf("stored assessments · %s", countWord(len(runs), "run", "runs"))
		if m.timelineAll {
			s += " · showing unchanged runs"
		} else {
			s += " · changes only"
		}
		return "  " + truncate(t.dim.Render(s), w)
	case timelineSourceEvents:
		return "  " + truncate(m.eventsSourceLine(), w)
	default:
		if m.tlEvents == nil || !m.tlEvents.loaded {
			return "  " + truncate(t.dim.Render("stored changes, grouped by the runs that recorded them, beside the vCenter events in each gap"), w)
		}
		return "  " + truncate(t.dim.Render("stored changes by run gap   ")+t.accent.Render("←")+t.dim.Render(" caused it   ")+t.faint.Render("·")+t.dim.Render(" left no stored change"), w)
	}
}

// eventsSourceLine is the events tab's provenance: always "live", always
// which vCenter, and how far back the read reaches, so an empty list never
// reads as "nothing happened".
func (m *Model) eventsSourceLine() string {
	t := m.theme
	if _, ok := m.backend.(vmEventsBackend); !ok {
		return t.dim.Render("vCenter events are not available in this session")
	}
	if m.tlEvents != nil && m.tlEvents.loading {
		return m.spin.View() + t.dim.Render(" reading vCenter events…")
	}
	if m.tlEvents == nil || !m.tlEvents.loaded {
		return t.dim.Render("live from vCenter · r reads the event log")
	}
	var ctxs []string
	oldest := time.Time{}
	truncated := false
	for _, r := range m.tlEvents.results {
		if r.err != nil {
			continue
		}
		ctxs = append(ctxs, r.target.context)
		if o := r.listing.Oldest(); !o.IsZero() && (oldest.IsZero() || o.Before(oldest)) {
			oldest = o
		}
		truncated = truncated || r.listing.Truncated
	}
	s := t.ok.Render("LIVE")
	if len(ctxs) > 0 {
		s += t.dim.Render(" from " + strings.Join(ctxs, ", "))
	}
	if !oldest.IsZero() {
		s += t.dim.Render(" · oldest " + oldest.Local().Format("2006-01-02"))
	}
	if truncated {
		s += t.warn.Render(fmt.Sprintf(" · newest %d only", vsphere.DefaultVMEventLimit))
	}
	if !m.tlEvents.loadedAt.IsZero() {
		if age := m.now().Sub(m.tlEvents.loadedAt); age < time.Second {
			s += t.dim.Render(" · read just now")
		} else {
			s += t.dim.Render(" · read " + humanize.Age(age) + " ago")
		}
	}
	return s + t.dim.Render(" · r reload")
}

// eventsProblems are the lines that name what the events read could not
// cover: a vCenter that is not connected or not configured, one that refused,
// or a backend that cannot read events at all.
func (m *Model) eventsProblems() []string {
	t := m.theme
	if _, ok := m.backend.(vmEventsBackend); !ok {
		return nil
	}
	read := map[string]vmEventsResult{}
	if m.tlEvents != nil {
		for _, r := range m.tlEvents.results {
			read[r.target.context] = r
		}
	}
	var out []string
	for _, target := range m.timelineTargets() {
		if r, ok := read[target.context]; ok {
			if r.err != nil {
				out = append(out, t.bad.Render("  "+target.context+": ")+t.dim.Render(firstLine(r.err.Error())))
			}
			continue
		}
		cc, connected := m.targetState(target)
		switch {
		case cc == nil:
			out = append(out, t.warn.Render("  "+target.context)+t.dim.Render(" is not configured here, so its events cannot be read"))
		case !connected:
			out = append(out, t.warn.Render("  "+target.context+" is not connected")+t.dim.Render(" · r connects and reads its events"))
		}
	}
	return out
}

func (m *Model) viewTimelineChanges() ([]string, int) {
	t := m.theme
	var lines []string
	for i, e := range m.timeline {
		line := fmt.Sprintf("%s %-16s %-16s %-12s %s", listCursor(i == m.timelineCursor), e.Run.StartedAt.Local().Format("2006-01-02 15:04"), e.Kind, e.Context, truncate(timelineChangeDetail(e), max(1, m.width-52)))
		if i == m.timelineCursor {
			line = t.focused.Render(line)
		} else {
			line = t.text.Render(line)
		}
		lines = append(lines, line)
	}
	return lines, m.timelineCursor
}

// timelineChangeDetail is a stored change's one-line detail: its field changes, or,
// for a change that has none (the VM first seen, or seen again), what the VM
// looked like then.
func timelineChangeDetail(e assessment.VMHistoryEvent) string {
	if len(e.Changes) > 0 || e.Observation == nil {
		return changeSummary(e.Changes)
	}
	vm := e.Observation.VM
	var parts []string
	if vm.CPU > 0 {
		parts = append(parts, fmt.Sprintf("%d vCPU", vm.CPU))
	}
	if vm.MemoryMB > 0 {
		parts = append(parts, humanize.MB(vm.MemoryMB))
	}
	if vm.Host != "" {
		parts = append(parts, vm.Host)
	}
	return strings.Join(parts, " · ")
}

func changeSummary(changes []assessment.FieldChange) string {
	parts := make([]string, len(changes))
	for j, f := range changes {
		parts[j] = f.Field + ":" + historyFieldValue(f.Field, f.Before) + "→" + historyFieldValue(f.Field, f.After)
	}
	return strings.Join(parts, " ")
}

func (m *Model) viewTimelineEvents() ([]string, int) {
	t := m.theme
	if _, ok := m.backend.(vmEventsBackend); !ok {
		return []string{t.dim.Render("  This session has no live vCenter to read events from."), t.dim.Render("  Stored changes are on tab 1.")}, 0
	}
	problems := m.eventsProblems()
	if m.tlEvents == nil || !m.tlEvents.loaded {
		if m.tlEvents != nil && m.tlEvents.loading {
			return problems, 0
		}
		if len(m.timelineTargets()) == 0 {
			if m.historyErr != nil {
				return []string{t.warn.Render("  " + m.historyErr.Error())}, 0
			}
			return []string{t.dim.Render("  No stored observation names this VM's vCenter, so there is nothing to read events for.")}, 0
		}
		return append(problems, "", t.dim.Render("  Stored changes are still on tab 1.")), 0
	}
	events := m.visibleEvents()
	multi := m.eventContexts() > 1
	header := "  " + pad("WHEN", 18, false) + pad("EVENT", 18, false) + pad("BY", 22, false) + pad("RESULT", 8, false)
	if multi {
		header += pad("VCENTER", 12, false)
	}
	lines := append([]string{}, problems...)
	if len(problems) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, t.header.Render(truncate(header+"DETAIL", max(1, m.width))))
	top := len(lines)
	if len(events) == 0 {
		lines = append(lines, t.dim.Render("  no events in what vCenter returned"))
	}
	for i, e := range events {
		sel := i == m.eventsCursor
		line := listCursor(sel) + " " + pad(e.Time.Local().Format("2006-01-02 15:04"), 18, false) + pad(e.Label, 18, false) + pad(humanize.Dash(e.User), 22, false)
		res := eventResultCell(e.Result)
		if multi {
			res += pad(e.Context, 12, false)
		}
		detail := e.Detail
		if sel {
			line = t.focused.Render(truncate(line+res+detail, max(1, m.width)))
		} else {
			line = truncate(t.text.Render(line)+m.styleResult(e.Result, res)+t.dim.Render(detail), max(1, m.width))
		}
		lines = append(lines, line)
	}
	if hidden := len(m.timelineEvents()) - len(events); hidden > 0 && !m.timelineMinor {
		lines = append(lines, "", t.faint.Render(fmt.Sprintf("  %s hidden (power, guest, tools, task progress) · a shows them", countWord(hidden, "routine event", "routine events"))))
	}
	return lines, top + m.eventsCursor
}

func eventResultCell(result string) string {
	switch result {
	case vsphere.ResultOK:
		return pad("✓", 8, false)
	case vsphere.ResultFailed:
		return pad("✕ failed", 8, false)
	default:
		return pad("·", 8, false)
	}
}

func (m *Model) styleResult(result, cell string) string {
	t := m.theme
	switch result {
	case vsphere.ResultOK:
		return t.ok.Render(cell)
	case vsphere.ResultFailed:
		return t.bad.Render(cell)
	default:
		return t.faint.Render(cell)
	}
}

// ---- combined ----

func (m *Model) timelineSpans() []assessment.EventSpan {
	return assessment.ExplainTimeline(m.timelineFull, m.timelineEvents())
}

// combinedSide reports whether the terminal is wide enough to show each
// stored change with the event that caused it on the same line.
func (m *Model) combinedSide() bool { return m.width >= 110 }

// combinedRows lays the spans out as rows: a header per span, each stored
// change followed by the events that caused it, then the span's other
// events. Routine events are left out unless "a" asked for them. On a wide
// terminal the first event that caused a change shares its row.
func (m *Model) combinedRows() []timelineRow {
	side := m.combinedSide()
	spans := m.timelineSpans()
	oldest := m.oldestEvent()
	loaded := m.tlEvents != nil && m.tlEvents.loaded
	var rows []timelineRow
	for si := range spans {
		s := spans[si]
		var events []timelineRow
		for i := range s.Unexplained {
			e := s.Unexplained[i]
			if e.Minor && !m.timelineMinor {
				continue
			}
			events = append(events, timelineRow{event: &e})
		}
		if len(s.Changes) == 0 && len(events) == 0 {
			continue
		}
		if len(rows) > 0 {
			rows = append(rows, timelineRow{})
		}
		rows = append(rows, timelineRow{header: spanLabel(s)})
		for ci := range s.Changes {
			c := s.Changes[ci]
			change := c.Change
			row := timelineRow{change: &change}
			explaining := c.Events
			if side && len(explaining) > 0 {
				first := explaining[0]
				row.inline = &first
				explaining = explaining[1:]
			}
			if side && len(c.Events) == 0 && loaded && !oldest.IsZero() && s.To != nil && s.To.StartedAt.Before(oldest) {
				row.inlineNote = "no events · before the oldest event vCenter returned"
			}
			rows = append(rows, row)
			for ei := range explaining {
				e := explaining[ei]
				rows = append(rows, timelineRow{event: &e, explains: true})
			}
			if !side && len(c.Events) == 0 && loaded && !oldest.IsZero() && s.To != nil && s.To.StartedAt.Before(oldest) {
				rows = append(rows, timelineRow{note: "no events · before the oldest event vCenter returned"})
			}
		}
		if s.To == nil && len(events) > 0 {
			rows = append(rows, timelineRow{note: "not assessed yet"})
		}
		rows = append(rows, events...)
	}
	return rows
}

func (m *Model) oldestEvent() time.Time {
	events := m.timelineEvents()
	if len(events) == 0 {
		return time.Time{}
	}
	return events[0].Time
}

func spanLabel(s assessment.EventSpan) string {
	day := func(r *assessment.Run) string { return r.StartedAt.Local().Format("Mon 02 Jan") }
	switch {
	case s.From == nil && s.To != nil:
		return fmt.Sprintf("%s  %s", historyRunLabel(s.To.ID), day(s.To))
	case s.To == nil && s.From != nil:
		return fmt.Sprintf("since %s  %s → now", historyRunLabel(s.From.ID), day(s.From))
	case s.From != nil && s.To != nil:
		return fmt.Sprintf("%s → %s  %s → %s", historyRunLabel(s.From.ID), historyRunLabel(s.To.ID), day(s.From), day(s.To))
	}
	return ""
}

func (m *Model) combinedSelectable() int {
	n := 0
	for _, r := range m.combinedRows() {
		if r.selectable() {
			n++
		}
	}
	return n
}

func (m *Model) selectedCombinedRow() (timelineRow, bool) {
	n := 0
	for _, r := range m.combinedRows() {
		if !r.selectable() {
			continue
		}
		if n == m.combinedCursor {
			return r, true
		}
		n++
	}
	return timelineRow{}, false
}

// combinedLeftWidth is the stored-change column on a terminal wide enough to
// show the events beside it.
const combinedLeftWidth = 54

func (m *Model) viewTimelineCombined() ([]string, int) {
	t := m.theme
	if m.historyErr != nil {
		return []string{t.warn.Render("  " + m.historyErr.Error())}, 0
	}
	var lines []string
	lines = append(lines, m.eventsProblems()...)
	if _, ok := m.backend.(vmEventsBackend); !ok {
		lines = append(lines, t.dim.Render("  vCenter events are not available in this session · stored changes only"))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	rows := m.combinedRows()
	if len(rows) == 0 {
		return append(lines, t.dim.Render("  no timeline events")), 0
	}
	side := m.combinedSide()
	multi := m.eventContexts() > 1
	if side {
		lines = append(lines, t.header.Render("  "+pad("STORED CHANGE", combinedLeftWidth-2, false)+"VCENTER EVENT"))
	}
	cursorLine, sel := 0, 0
	for _, r := range rows {
		selected := false
		if r.selectable() {
			selected = sel == m.combinedCursor
			sel++
		}
		if selected {
			cursorLine = len(lines)
		}
		switch {
		case r.header != "":
			lines = append(lines, "  "+t.value.Bold(true).Render(r.header))
		case r.note != "":
			lines = append(lines, "      "+t.faint.Render("· "+r.note))
		case r.change != nil:
			left := listCursor(selected) + "   " + pad(r.change.Kind, 18, false) + timelineChangeDetail(*r.change)
			if !side {
				lines = append(lines, m.timelineLine(left, selected))
				continue
			}
			line := pad(left, combinedLeftWidth-1, false) + " "
			switch {
			case r.inline != nil:
				line += m.combinedEvent(*r.inline, true, multi)
			case r.inlineNote != "":
				line += t.faint.Render("· " + r.inlineNote)
			}
			lines = append(lines, m.timelineLine(line, selected))
		case r.event != nil:
			indent := "     "
			if side {
				indent = strings.Repeat(" ", combinedLeftWidth-1)
			}
			lines = append(lines, m.timelineLine(listCursor(selected)+indent+m.combinedEvent(*r.event, r.explains, multi), selected))
		default:
			lines = append(lines, "")
		}
	}
	return lines, cursorLine
}

func (m *Model) combinedEvent(e vsphere.VMEvent, explains, multi bool) string {
	t := m.theme
	mark := t.faint.Render("·")
	if explains {
		mark = t.accent.Render("←")
	}
	glyph := t.faint.Render("·")
	switch e.Result {
	case vsphere.ResultOK:
		glyph = t.ok.Render("✓")
	case vsphere.ResultFailed:
		glyph = t.bad.Render("✕")
	}
	detail := e.Detail
	if multi {
		detail = strings.TrimSpace(e.Context + "  " + detail)
	}
	return mark + " " + t.text.Render(pad(e.Time.Local().Format("01-02 15:04"), 13, false)+pad(e.Label, 17, false)+pad(humanize.Dash(e.User), 21, false)) + glyph + "  " + t.dim.Render(detail)
}

func (m *Model) timelineLine(s string, selected bool) string {
	s = truncate(s, max(1, m.width))
	if selected {
		return m.theme.focused.Render(ansi.Strip(s))
	}
	return s
}

// ---- detail ----

func (m *Model) viewHistoryTimelineDetail() []string {
	t := m.theme
	if e := m.timelineDetailEvent; e != nil {
		lines := m.viewEventDetail(*e)
		for i := range lines {
			lines[i] = truncate(lines[i], max(1, m.width))
		}
		return scrollLines(lines, 0, m.bodyHeight())
	}
	e, ok := m.selectedTimelineChange()
	if !ok {
		return []string{t.dim.Render("nothing selected")}
	}
	lines := []string{t.title.Render(e.Name), "", "  " + t.label.Render("Event") + "    " + e.Kind, "  " + t.label.Render("Run") + "      " + historyRunLabel(e.Run.ID), "  " + t.label.Render("Date") + "     " + e.Run.StartedAt.Local().Format("2006-01-02 15:04:05"), "  " + t.label.Render("Context") + "  " + e.Context}
	if len(e.Changes) > 0 {
		lines = append(lines, "", t.header.Render("Changes"))
		for _, f := range e.Changes {
			lines = append(lines, fmt.Sprintf("  %-18s %s → %s", f.Field, truncate(historyFieldValue(f.Field, f.Before), 24), truncate(historyFieldValue(f.Field, f.After), 24)))
		}
	}
	if m.tlEvents != nil && m.tlEvents.loaded {
		lines = append(lines, "", t.header.Render("vCenter events"))
		explained := m.eventsExplaining(e)
		if len(explained) == 0 {
			if oldest := m.oldestEvent(); !oldest.IsZero() && e.Run.StartedAt.Before(oldest) {
				lines = append(lines, t.dim.Render("  none · this change is older than the oldest event vCenter returned"))
			} else {
				lines = append(lines, t.dim.Render("  none of the events read account for this change"))
			}
		}
		for _, ev := range explained {
			lines = append(lines, "  "+m.combinedEvent(ev, true, m.eventContexts() > 1))
		}
	}
	return scrollLines(lines, 0, m.bodyHeight())
}

func (m *Model) eventsExplaining(change assessment.VMHistoryEvent) []vsphere.VMEvent {
	for _, s := range m.timelineSpans() {
		for _, c := range s.Changes {
			if c.Change.Kind == change.Kind && c.Change.Run.ID == change.Run.ID && changeSummary(c.Change.Changes) == changeSummary(change.Changes) {
				return c.Events
			}
		}
	}
	return nil
}

func (m *Model) viewEventDetail(e vsphere.VMEvent) []string {
	t := m.theme
	name := m.timelineEntity()
	kv := func(k, v string) string { return "  " + t.label.Render(pad(k, 12, false)) + v }
	lines := []string{t.title.Render(e.Label + " · " + name), ""}
	lines = append(lines, kv("Source", t.ok.Render("LIVE")+t.dim.Render(" vCenter event from "+e.Context)))
	lines = append(lines, kv("Type", e.Type), kv("Time", e.Time.Local().Format("2006-01-02 15:04:05")), kv("By", humanize.Dash(e.User)))
	switch e.Result {
	case vsphere.ResultOK:
		lines = append(lines, kv("Result", t.ok.Render("✓ completed")))
	case vsphere.ResultFailed:
		lines = append(lines, kv("Result", t.bad.Render("✕ failed")))
	default:
		lines = append(lines, kv("Result", t.dim.Render("started · the event log does not say how it ended")))
	}
	if e.Task != "" {
		lines = append(lines, kv("Task", e.Task))
	}
	if e.FromHost != e.ToHost {
		lines = append(lines, kv("Host", humanize.Dash(e.FromHost)+" → "+humanize.Dash(e.ToHost)))
	} else if e.ToHost != "" {
		lines = append(lines, kv("Host", e.ToHost))
	}
	if e.FromDatastore != e.ToDatastore {
		lines = append(lines, kv("Datastore", humanize.Dash(e.FromDatastore)+" → "+humanize.Dash(e.ToDatastore)))
	} else if e.ToDatastore != "" {
		lines = append(lines, kv("Datastore", e.ToDatastore))
	}
	if e.Detail != "" && e.FromHost == "" && e.ToHost == "" {
		lines = append(lines, kv("Changed", e.Detail))
	}
	chain := fmt.Sprintf("event %d", e.Key)
	if e.ChainID != 0 && e.ChainID != e.Key {
		chain += fmt.Sprintf(" · chain %d", e.ChainID)
	}
	lines = append(lines, kv("Chain", t.dim.Render(chain)))

	lines = append(lines, "", t.header.Render("Assessments"))
	where, explains := m.eventPlacement(e)
	lines = append(lines, kv("When", where), kv("Recorded", explains))

	if e.Message != "" {
		lines = append(lines, "", t.header.Render("vCenter message"))
		for _, l := range wrap(e.Message, max(20, m.width-4)) {
			lines = append(lines, "  "+t.dim.Render(l))
		}
	}
	return lines
}

// eventPlacement says where an event sits among the stored runs and whether
// any run recorded what it did.
func (m *Model) eventPlacement(e vsphere.VMEvent) (string, string) {
	same := func(x vsphere.VMEvent) bool { return x.Context == e.Context && x.Key == e.Key && x.Time.Equal(e.Time) }
	for _, s := range m.timelineSpans() {
		where := spanPlacement(s)
		for _, c := range s.Changes {
			for _, x := range c.Events {
				if same(x) {
					return where, m.theme.ok.Render("yes") + m.theme.dim.Render(" · the "+c.Change.Kind+" stored by run "+historyRunLabel(c.Change.Run.ID))
				}
			}
		}
		for _, x := range s.Unexplained {
			if same(x) {
				why := "no stored change matches it"
				switch {
				case e.Result == vsphere.ResultFailed:
					why = "it failed, so there was nothing to record"
				case s.To == nil:
					why = "no run has assessed the VM since"
				case e.Explains == "":
					why = "assessments do not record this kind of event"
				case !spanHasKind(s, e.Explains):
					why = "the next run found nothing it changed still different"
				}
				return where, m.theme.warn.Render("no") + m.theme.dim.Render(" · "+why)
			}
		}
	}
	if len(m.timelineFull) == 0 {
		return "no stored history for this VM", m.theme.dim.Render("—")
	}
	return "—", m.theme.dim.Render("—")
}

// spanHasKind reports whether a span stored a change of the kind an event
// explains: when it did not, the event's effect was gone by the next run.
func spanHasKind(s assessment.EventSpan, explains string) bool {
	for _, c := range s.Changes {
		if assessment.ExplainedBy(c.Change.Kind) == explains {
			return true
		}
	}
	return false
}

func spanPlacement(s assessment.EventSpan) string {
	day := func(r *assessment.Run) string { return r.StartedAt.Local().Format("Mon 02 Jan") }
	switch {
	case s.From == nil && s.To != nil:
		return "before run " + historyRunLabel(s.To.ID) + ", the first to see the VM"
	case s.To == nil && s.From != nil:
		return "after run " + historyRunLabel(s.From.ID) + " " + day(s.From) + ", newer than any run"
	case s.From != nil && s.To != nil:
		return "between run " + historyRunLabel(s.From.ID) + " " + day(s.From) + " and run " + historyRunLabel(s.To.ID) + " " + day(s.To)
	}
	return "—"
}
