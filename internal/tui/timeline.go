package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
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
// reference on one vCenter, read through one context. A VM that moved between
// vCenters has one target per vCenter it was seen on. Several contexts can
// reach the same vCenter; only one of them is read.
type vmEventsTarget struct {
	context string
	vmID    string
	// vcenter is the vCenter's identity, as assessments record it. It is
	// empty when unknown, which keeps the target apart from every other.
	vcenter string
}

// vmEventsKey is what one read answers for: a managed object on a vCenter,
// whichever context read it.
type vmEventsKey struct {
	vcenter string
	vmID    string
}

func (t vmEventsTarget) key() vmEventsKey {
	if t.vcenter == "" {
		return vmEventsKey{vcenter: "context:" + t.context, vmID: t.vmID}
	}
	return vmEventsKey{vcenter: t.vcenter, vmID: t.vmID}
}

// vmEventsResult is one target's read: its listing, or why there is none.
type vmEventsResult struct {
	target  vmEventsTarget
	listing vsphere.VMEventListing
	err     error
}

// timelineEventsState is the vCenter events read for the open timeline.
// Reads can overlap: stored history can name a vCenter the first read did
// not know about, and it is read as soon as it is named.
type timelineEventsState struct {
	loading  bool
	loaded   bool
	inflight int
	// requested is every managed object a read has been started for.
	requested map[vmEventsKey]bool
	results   []vmEventsResult
	loadedAt  time.Time
}

// timelineDerived is what the timeline computes from its stored history and
// its events: kept until either changes, because every render and keypress
// reads it.
type timelineDerived struct {
	events   []vsphere.VMEvent
	spans    []assessment.EventSpan
	contexts int
}

type vmEventsMsg struct {
	generation int
	results    []vmEventsResult
	at         time.Time
}

// eventMark is how the Combined tab marks an event: the stored change it
// caused, no stored change, or a time before any run saw the VM.
type eventMark int

const (
	markNone eventMark = iota
	markCaused
	markPredates
)

// predatesFirstRun reports whether an event in span s happened before the
// first run that saw the VM. Whatever it did was already part of that run's
// first observation, so no stored change could record it. A failed event did
// nothing, and is not counted.
func predatesFirstRun(s assessment.EventSpan, e vsphere.VMEvent) bool {
	return s.From == nil && s.To != nil && e.Result != vsphere.ResultFailed
}

// timelineRow is one line of the Combined tab. Header and note rows are not
// selectable; change and event rows are, and enter opens their detail.
type timelineRow struct {
	header string
	note   string
	change *assessment.VMHistoryEvent
	event  *vsphere.VMEvent
	// mark says what the event did to the stored history.
	mark eventMark
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
	m.tlEvents, m.tlDerived = nil, nil
	m.tlGen++
	m.historyErr = nil
	m.timelineFrom = from
	m.mode = modeHistoryTimeline
	cmds := []tea.Cmd{loadHistoryTimelineCmd(m.ctx, m.assessment, query, m.tlGen)}
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
	if msg.generation != m.tlGen {
		return nil
	}
	m.timelineFull, m.historyErr = msg.events, msg.err
	m.tlDerived = nil
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

// timelineTargets is every VM to read events for: each managed object
// reference the lineage was stored under on each vCenter — a VM that was
// re-registered or restored keeps its older events under its older one — and
// the live VM the timeline was opened from. Contexts that reach the same
// vCenter share its event log, so each managed object is read through one of
// them: a connected one when there is one.
func (m *Model) timelineTargets() []vmEventsTarget {
	groups := map[vmEventsKey][]vmEventsTarget{}
	add := func(t vmEventsTarget) {
		groups[t.key()] = append(groups[t.key()], t)
	}
	for _, e := range m.timelineFull {
		if e.Observation == nil || e.Observation.VM.ID == "" {
			continue
		}
		add(vmEventsTarget{context: e.Context, vmID: e.Observation.VM.ID, vcenter: e.Observation.VCenterID})
	}
	var seed *vmEventsTarget
	if s := m.timelineSeed; s != nil && s.vmID != "" {
		t := *s
		t.vcenter = m.seedVCenterID(t, groups)
		seed = &t
		add(t)
	}
	out := make([]vmEventsTarget, 0, len(groups))
	for _, group := range groups {
		out = append(out, m.preferredTarget(group, seed))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].context != out[j].context {
			return out[i].context < out[j].context
		}
		return out[i].vmID < out[j].vmID
	})
	return out
}

// seedVCenterID is the identity of the vCenter the live VM was opened from:
// the one stored history already recorded for that context and managed
// object, else the one assessments would record for the context now — the
// connected vCenter's instance id, or its endpoint when it reports none.
func (m *Model) seedVCenterID(seed vmEventsTarget, stored map[vmEventsKey][]vmEventsTarget) string {
	for _, group := range stored {
		for _, t := range group {
			if t.context == seed.context && t.vmID == seed.vmID {
				return t.vcenter
			}
		}
	}
	if st, _ := m.backend.Status(seed.context); st.InstanceID != "" {
		return st.InstanceID
	}
	if s := m.byName[seed.context]; s != nil && s.cc != nil {
		return s.cc.Endpoint
	}
	return ""
}

// preferredTarget picks which context reads a managed object that several
// contexts reach: a connected one, else a configured one, else the live VM's
// own, and then the first by name.
func (m *Model) preferredTarget(group []vmEventsTarget, seed *vmEventsTarget) vmEventsTarget {
	score := func(t vmEventsTarget) (rank int, isSeed bool) {
		cc, connected := m.targetState(t)
		switch {
		case connected:
			rank = 2
		case cc != nil:
			rank = 1
		}
		return rank, seed != nil && t.context == seed.context
	}
	best := group[0]
	bestRank, bestSeed := score(best)
	for _, t := range group[1:] {
		rank, isSeed := score(t)
		if rank > bestRank || rank == bestRank && (isSeed && !bestSeed || isSeed == bestSeed && t.context < best.context) {
			best, bestRank, bestSeed = t, rank, isSeed
		}
	}
	return best
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
// Opening an events tab reads only vCenters that are already connected, and
// only targets no read has asked for yet, so a target stored history names
// after the first read began is still read. explicit (r) reads every target
// again, and also connects the configured vCenters that are not connected,
// because that is the operator asking for it. Nothing reads events in the
// background.
func (m *Model) loadTimelineEvents(explicit bool) tea.Cmd {
	b, ok := m.backend.(vmEventsBackend)
	if !ok {
		return nil
	}
	if explicit && m.tlEvents != nil && m.tlEvents.inflight > 0 {
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
		if !explicit && m.tlEvents != nil && m.tlEvents.requested[t.key()] {
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
	if m.tlEvents.requested == nil {
		m.tlEvents.requested = map[vmEventsKey]bool{}
	}
	for _, j := range jobs {
		m.tlEvents.requested[j.target.key()] = true
	}
	m.tlEvents.inflight++
	m.tlEvents.loading = true
	gen, ctx, now := m.tlGen, m.ctx, m.now
	load := func() tea.Msg {
		// Each vCenter is read at once, so a slow or unreachable one holds
		// up only its own result.
		results := make([]vmEventsResult, len(jobs))
		var wg sync.WaitGroup
		for i, j := range jobs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				listing, err := b.VMEvents(ctx, j.cc, j.target.vmID, vsphere.DefaultVMEventLimit)
				results[i] = vmEventsResult{target: j.target, listing: listing, err: err}
			}()
		}
		wg.Wait()
		return vmEventsMsg{generation: gen, results: results, at: now()}
	}
	return tea.Batch(load, m.spin.Tick)
}

func (m *Model) applyVMEvents(msg vmEventsMsg) {
	if msg.generation != m.tlGen || m.tlEvents == nil {
		return
	}
	m.tlDerived = nil
	// A reload that only reached some vCenters keeps the others' last read.
	// A managed object is listed once, from whichever context read it last.
	merged := map[vmEventsKey]vmEventsResult{}
	for _, r := range m.tlEvents.results {
		merged[r.target.key()] = r
	}
	for _, r := range msg.results {
		merged[r.target.key()] = r
	}
	m.tlEvents.results = m.tlEvents.results[:0]
	for _, r := range merged {
		m.tlEvents.results = append(m.tlEvents.results, r)
	}
	sort.Slice(m.tlEvents.results, func(i, j int) bool {
		return m.tlEvents.results[i].target.context < m.tlEvents.results[j].target.context
	})
	m.tlEvents.inflight = max(0, m.tlEvents.inflight-1)
	m.tlEvents.loading, m.tlEvents.loaded, m.tlEvents.loadedAt = m.tlEvents.inflight > 0, true, msg.at
	m.eventsCursor = clamp(m.eventsCursor, 0, max(0, len(m.visibleEvents())-1))
}

// derived computes, once per change to the timeline's history or events,
// the merged events and the spans that line them up with the stored changes.
func (m *Model) derived() *timelineDerived {
	if m.tlDerived != nil {
		return m.tlDerived
	}
	d := &timelineDerived{}
	if m.tlEvents != nil {
		// Two managed object references of one lineage can share an event
		// (one logged against both); it is listed once.
		type id struct {
			context string
			key     int32
		}
		seen := map[id]bool{}
		contexts := map[string]bool{}
		for _, r := range m.tlEvents.results {
			if r.err != nil {
				continue
			}
			for _, e := range r.listing.Events {
				k := id{e.Context, e.Key}
				if e.Key != 0 && seen[k] {
					continue
				}
				seen[k] = true
				d.events = append(d.events, e)
				contexts[e.Context] = true
			}
		}
		vsphere.SortVMEvents(d.events)
		d.contexts = len(contexts)
	}
	d.spans = assessment.ExplainTimeline(m.timelineFull, d.events)
	m.tlDerived = d
	return d
}

// timelineEvents is every event read, from every vCenter, oldest first.
func (m *Model) timelineEvents() []vsphere.VMEvent { return m.derived().events }

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

func (m *Model) eventContexts() int { return m.derived().contexts }

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
		// The Changes tab never talks to a vCenter, so r reads only from
		// the tabs that show what it read.
		if m.timelineSource == timelineSourceChanges {
			return nil
		}
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
		legend := t.dim.Render("stored changes by run gap   ") + t.accent.Render("←") + t.dim.Render(" caused it   ") + t.faint.Render("·") + t.dim.Render(" left no stored change")
		if m.showsPreFirstRunEvents() {
			legend += t.dim.Render("   ‹ before the first run")
		}
		return "  " + truncate(legend, w)
	}
}

// showsPreFirstRunEvents reports whether the Combined tab lists an event
// marked as predating the first run, which is when its legend explains the mark.
func (m *Model) showsPreFirstRunEvents() bool {
	for _, r := range m.combinedRows() {
		if r.mark == markPredates {
			return true
		}
	}
	return false
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
	read := map[vmEventsKey]vmEventsResult{}
	if m.tlEvents != nil {
		for _, r := range m.tlEvents.results {
			read[r.target.key()] = r
		}
	}
	var out []string
	for _, target := range m.timelineTargets() {
		if r, ok := read[target.key()]; ok {
			if r.err != nil {
				out = append(out, t.bad.Render("  "+r.target.context+": ")+t.dim.Render(firstLine(r.err.Error())))
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
	// WHEN, EVENT, RESULT and the cursor take 46 columns, VCENTER 12 more.
	fixed := 46
	if multi {
		fixed += 12
	}
	by := m.eventUserWidth(22, fixed)
	header := "  " + pad("WHEN", 18, false) + pad("EVENT", 18, false) + pad("BY", by, false) + pad("RESULT", 8, false)
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
		line := listCursor(sel) + " " + pad(e.Time.Local().Format("2006-01-02 15:04"), 18, false) + pad(truncate(e.Label, 17), 18, false) + pad(humanize.Dash(e.User), by, false)
		res := eventResultCell(e.Result)
		if multi {
			res += pad(e.Context, 12, false)
		}
		detail := e.DisplayDetail()
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

func (m *Model) timelineSpans() []assessment.EventSpan { return m.derived().spans }

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
			row := timelineRow{event: &e}
			if predatesFirstRun(s, e) {
				row.mark = markPredates
			}
			events = append(events, row)
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
				rows = append(rows, timelineRow{event: &e, mark: markCaused})
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
				line += m.combinedEvent(*r.inline, markCaused, multi)
			case r.inlineNote != "":
				line += t.faint.Render("· " + r.inlineNote)
			}
			lines = append(lines, m.timelineLine(line, selected))
		case r.event != nil:
			indent := "     "
			if side {
				indent = strings.Repeat(" ", combinedLeftWidth-1)
			}
			lines = append(lines, m.timelineLine(listCursor(selected)+indent+m.combinedEvent(*r.event, r.mark, multi), selected))
		default:
			lines = append(lines, "")
		}
	}
	return lines, cursorLine
}

func (m *Model) combinedEvent(e vsphere.VMEvent, em eventMark, multi bool) string {
	by := m.eventUserWidth(21, m.combinedEventFixed())
	t := m.theme
	mark := t.faint.Render("·")
	switch em {
	case markCaused:
		mark = t.accent.Render("←")
	case markPredates:
		mark = t.dim.Render("‹")
	}
	glyph := t.faint.Render("·")
	switch e.Result {
	case vsphere.ResultOK:
		glyph = t.ok.Render("✓")
	case vsphere.ResultFailed:
		glyph = t.bad.Render("✕")
	}
	detail := e.DisplayDetail()
	if multi {
		detail = strings.TrimSpace(e.Context + "  " + detail)
	}
	return mark + " " + t.text.Render(pad(e.Time.Local().Format("01-02 15:04"), 13, false)+pad(truncate(e.Label, 16), 17, false)+pad(humanize.Dash(e.User), by, false)) + glyph + "  " + t.dim.Render(detail)
}

// combinedEventFixed is the width an event takes on a Combined row besides
// its BY column: the stored-change column when it sits beside the event or
// the indent when not, the mark, time, label and result.
func (m *Model) combinedEventFixed() int {
	fixed := 2 + 13 + 17 + 3
	if m.combinedSide() {
		return fixed + combinedLeftWidth
	}
	return fixed + 6
}

// eventUserWidth is the width of the BY column. It never drops below base, so
// narrow terminals keep their layout, and grows toward the longest user name
// (VSPHERE.LOCAL\Administrator is 27) while at least minEventDetail columns
// remain for the detail after the other fixed columns.
func (m *Model) eventUserWidth(base, fixed int) int {
	need := 0
	for _, e := range m.timelineEvents() {
		need = max(need, ansi.StringWidth(humanize.Dash(e.User))+1)
	}
	return max(base, min(need, m.width-fixed-minEventDetail))
}

// minEventDetail is the room a wider BY column leaves for the detail.
const minEventDetail = 30

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
			lines = append(lines, "  "+m.combinedEvent(ev, markCaused, m.eventContexts() > 1))
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
				case predatesFirstRun(s, e):
					return where, m.theme.dim.Render("— · before the first run, so part of the first observation")
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
