package tui

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/health"
	"github.com/easonliuuuuu/vsfleet/internal/humanize"
)

func (m *Model) enterChanges() tea.Cmd {
	m.mode = modeChanges
	m.historyPane = historyPaneChanges
	m.filter.Placeholder = "filter changes"
	m.filtering = false
	m.filter.Blur()
	m.changeCursor, m.changeOffset = 0, 0
	m.offset = 0
	m.scrubOffset = 0
	m.scrubHandle = "t"
	m.impactFilter = impactAll
	m.historyCoverage = nil
	m.historyHealth = nil
	m.historyHealthErr = nil
	m.historyTrendsErr = nil
	m.historyTrendsEmpty = nil
	if m.assessment == nil {
		m.historyErr = fmt.Errorf("historical assessments are unavailable")
		m.historyHealthErr = fmt.Errorf("historical assessments are unavailable")
		return nil
	}
	m.historyErr = nil
	return tea.Batch(
		loadHistoryRunsCmd(m.ctx, m.assessment),
		loadHistoryTrendsCmd(m.ctx, m.assessment, m.historyScope()),
		loadHistoryHealthCmd(m.ctx, m.assessment, 0, health.Options{Thresholds: health.DefaultThresholds()}),
	)
}

// exitHistoryToBrowse returns to the browse table from anywhere in the History
// hub, restoring every piece of browse-owned filter state that entering History
// changed. Every History-exit path routes through here so a pane added later
// cannot forget a field.
func (m *Model) exitHistoryToBrowse() tea.Cmd {
	m.mode = modeBrowse
	m.historyPane = historyPaneChanges
	m.filter.Placeholder = filterPlaceholder
	return nil
}

func (m *Model) loadDefaultHistoryDiff() {
	if len(m.runs) < 2 {
		m.changeDiff = nil
		m.baseRun, m.targetRun = 0, 0
		if m.canCapture() {
			m.historyErr = fmt.Errorf("capture at least two assessments to compare")
		} else {
			m.historyErr = fmt.Errorf("fewer than two stored assessments — nothing to compare")
		}
		return
	}
	// Runs are newest first. The newest pair is the useful default and is
	// deterministic even when an intervening run failed.
	m.baseRun, m.targetRun = m.runs[1].ID, m.runs[0].ID
	m.historyErr = nil
}

// scopedDefaultSpan picks the newest two runs that reached every vCenter in
// scope, so a scoped Changes pane opens on a comparison that can exist rather
// than on the newest runs overall. ok is false when fewer than two qualify;
// covering is how many did.
func (m *Model) scopedDefaultSpan() (base, target int64, covering int, ok bool) {
	scope := m.historyScope()
	var ids []int64
	for _, r := range m.runs {
		reached := m.historyCoverage[r.ID]
		all := len(reached) > 0
		for _, name := range scope {
			if reached[name] != "success" {
				all = false
				break
			}
		}
		if all {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) < 2 {
		return 0, 0, len(ids), false
	}
	return ids[1], ids[0], len(ids), true
}

func (m *Model) historyDiffCommand() tea.Cmd {
	if m.assessment == nil || len(m.runs) < 2 {
		return nil
	}
	if m.baseRun == 0 || m.targetRun == 0 || m.baseRun == m.targetRun {
		return nil
	}
	return loadHistoryDiffCmd(m.ctx, m.assessment, m.baseRun, m.targetRun, m.historyScope())
}

func (m *Model) changeRows() []historyRow {
	if m.changeDiff == nil {
		return nil
	}
	var rows []historyRow
	for _, v := range m.changeDiff.VMs {
		rows = append(rows, historyRow{kind: "vm", label: v.Name, context: v.Context, change: strings.Join(v.Changes, ", "), detail: changeDetail(v)})
	}
	for _, s := range m.changeDiff.Snapshots {
		rows = append(rows, historyRow{kind: "snapshot", label: s.VMName + " / " + s.Name, context: s.Context, change: "snapshot " + s.Kind, detail: nonempty(s.After, s.Before)})
	}
	for _, r := range m.changeDiff.Resources {
		rows = append(rows, historyRow{kind: r.Kind, label: r.Name, context: r.Context, change: strings.Join(r.Changes, ", "), detail: resourceDetail(r)})
	}
	needle := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if needle == "" {
		return rows
	}
	out := rows[:0]
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.label), needle) || strings.Contains(strings.ToLower(r.context), needle) || strings.Contains(strings.ToLower(r.change), needle) || strings.Contains(strings.ToLower(r.kind), needle) {
			out = append(out, r)
		}
	}
	return out
}

// historyRow is one line of the Changes list. kind names what changed — vm,
// snapshot, or an infrastructure kind (cluster, host, datastore) — as its own
// field so the CHANGE and KIND columns stop conflating "what happened" with
// "what it happened to".
type historyRow struct{ label, context, change, detail, kind string }

func (m *Model) viewChangesHeader() string {
	t := m.theme
	labels := []string{"Changes", "Trends", "Runs", "Health"}
	pane := m.historyPane
	if pane < 0 || pane >= len(labels) {
		pane = historyPaneChanges
	}
	// The run identities themselves now live on the comparison bar beneath
	// this header, so the header no longer restates "target #N" — a figure
	// the bar already shows by label, date, and coverage instead of a bare ID.
	label := "history  ·  " + labels[pane]
	left := m.headerBrand() + "  " + t.accent.Render(label)
	// The switch hint is the first thing to go when the badge leaves no room
	// for it, rather than being cut mid-word.
	full := left + t.dim.Render("   tab/⇧tab switch")
	if m.width > 0 && ansi.StringWidth(full) > m.width {
		return truncate(left, m.width)
	}
	return full
}

func (m *Model) viewHistoryTimelineHeader() string {
	return m.viewTimelineHeader("history  ·  Timeline")
}

func (m *Model) viewHistoryTimelineDetailHeader() string {
	return m.viewTimelineHeader("history  ·  Timeline  ·  Detail")
}

func (m *Model) viewTimelineHeader(label string) string {
	t := m.theme
	left := m.headerBrand() + "  " + t.accent.Render(label)
	if entity := m.timelineEntity(); entity != "" {
		left += t.dim.Render("  ·  ") + t.value.Render(entity)
	}
	if m.width > 0 {
		return truncate(left, m.width)
	}
	return left
}

func (m *Model) timelineEntity() string {
	if q := strings.TrimSpace(m.timelineQuery); q != "" {
		return q
	}
	if m.timelineCursor >= 0 && m.timelineCursor < len(m.timeline) {
		if name := strings.TrimSpace(m.timeline[m.timelineCursor].Name); name != "" {
			return name
		}
	}
	if len(m.timeline) > 0 {
		if name := strings.TrimSpace(m.timeline[0].Name); name != "" {
			return name
		}
	}
	return ""
}

// viewComparisonBar renders the baseline and target runs as a permanent,
// legible object: label, pin, id, when, status, and vCenter coverage for
// each, the gap between them, and — when the two runs did not cover the same
// vCenters — a plain-language line naming which ones were left out. This is
// the fix for the diff's loudest failure mode: a narrower capture reading as
// mass deletion because nothing on screen said the comparison was partial.
func (m *Model) viewComparisonBar(d *assessment.Diff) []string {
	t := m.theme
	lines := []string{
		"  " + t.accent.Render("b") + " " + t.dim.Render("baseline") + "  " + comparisonRunLine(t, d.Base, "", m.width),
		"  " + t.accent.Render("t") + " " + t.dim.Render("target  ") + "  " + comparisonRunLine(t, d.Target, formatInterval(d.Target.StartedAt.Sub(d.Base.StartedAt)), m.width),
	}
	missingTarget, missingBaseline := coverageGaps(d)
	if len(missingTarget) > 0 {
		lines = append(lines, "  "+t.warn.Render(fmt.Sprintf("! not compared: %s — collected in the baseline but not the target", strings.Join(missingTarget, ", "))))
	}
	if len(missingBaseline) > 0 {
		lines = append(lines, "  "+t.warn.Render(fmt.Sprintf("! not compared: %s — collected in the target but not the baseline", strings.Join(missingBaseline, ", "))))
	}
	// Two runs can report the same VCenterID for genuinely different
	// vCenters (a simulator fixture, a cloned appliance) — Diff cannot name
	// the gap by vCenter in that case, but the counts still disagree and the
	// operator still needs to know the comparison is not full-estate.
	if len(missingTarget) == 0 && len(missingBaseline) == 0 && d.Base.SuccessfulContexts != d.Target.SuccessfulContexts {
		lines = append(lines, "  "+t.warn.Render(fmt.Sprintf("! baseline covered %s, target covered %s — the counts below are not a full-estate comparison", vCenterCount(d.Base.SuccessfulContexts), vCenterCount(d.Target.SuccessfulContexts))))
	}
	for _, note := range condenseCoverageNotes(coverageNotes(d)) {
		lines = append(lines, "  "+t.dim.Render("· "+note))
	}
	for i, line := range lines {
		lines[i] = truncate(line, m.width)
	}
	return lines
}

// comparisonRunLine formats one run's identity for the bar: its label (with
// a pin), id, local date/time, status, and how much of the estate it
// actually covered — the field this whole bar exists to make legible.
// interval is appended when non-empty — only the target row carries the gap
// since it reads relative to the baseline above it. Below 100 columns the
// weekday and label give way first, since coverage is the figure that must
// survive an 80 column terminal without an ellipsis.
func comparisonRunLine(t theme, r assessment.Run, interval string, width int) string {
	labelW := 20
	dateFormat := "Mon 02 Jan 15:04"
	if width < 100 {
		labelW = 14
		dateFormat = "01-02 15:04"
	}
	label := r.Label
	if label == "" {
		label = "(unlabelled)"
	}
	if r.Pinned {
		label = "📌 " + label
	}
	status := t.dim.Render(pad(string(r.Status), 8, false))
	switch r.Status {
	case assessment.RunComplete:
		status = t.ok.Render(pad(string(r.Status), 8, false))
	case assessment.RunPartial, assessment.RunFailed:
		status = t.warn.Render(pad(string(r.Status), 8, false))
	}
	coverage := vCenterCount(r.SuccessfulContexts)
	coverStyle := t.dim
	if r.RequestedContexts > 0 && r.SuccessfulContexts < r.RequestedContexts {
		coverage = fmt.Sprintf("%d/%d vCenters", r.SuccessfulContexts, r.RequestedContexts)
		coverStyle = t.warn
	}
	when := "—"
	if !r.StartedAt.IsZero() {
		when = r.StartedAt.Local().Format(dateFormat)
	}
	line := fmt.Sprintf("%s %-6s %s  %s  %s", pad(label, labelW, false), historyRunLabel(r.ID), pad(when, len(dateFormat), false), status, coverStyle.Render(pad(coverage, 12, false)))
	if interval != "" {
		line += t.faint.Render(" +" + interval)
	}
	return line
}

// vCenterCount pluralizes a vCenter tally the way an operator would say it.
func vCenterCount(n int) string {
	if n == 1 {
		return "1 vCenter"
	}
	return fmt.Sprintf("%d vCenters", n)
}

// coverageGaps names, by human context name, which vCenters the diff could
// not compare on each side — everything Diff.Coverage recorded, whether the
// vCenter was missing entirely or its collection simply failed that run.
// Deduplicated because infrastructure coverage repeats one entry per kind
// (host, cluster, datastore) for the same context.
func coverageGaps(d *assessment.Diff) (missingFromTarget, missingFromBaseline []string) {
	seenTarget, seenBaseline := map[string]bool{}, map[string]bool{}
	for _, c := range d.Coverage {
		if c.Context == "" {
			continue
		}
		switch c.Scope {
		case "target":
			if !seenTarget[c.Context] {
				seenTarget[c.Context] = true
				missingFromTarget = append(missingFromTarget, c.Context)
			}
		case "baseline":
			if !seenBaseline[c.Context] {
				seenBaseline[c.Context] = true
				missingFromBaseline = append(missingFromBaseline, c.Context)
			}
		}
	}
	sort.Strings(missingFromTarget)
	sort.Strings(missingFromBaseline)
	return missingFromTarget, missingFromBaseline
}

// coverageNotes returns coverage issues that name no specific vCenter — a
// resource kind never recorded at all, typically because a run predates that
// collection existing. These do not fit the per-vCenter gap lines above, but
// dropping them silently would lose real information about what the diff
// could not check.
func coverageNotes(d *assessment.Diff) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range d.Coverage {
		if c.Context != "" || seen[c.Message] {
			continue
		}
		seen[c.Message] = true
		out = append(out, c.Message)
	}
	sort.Strings(out)
	return out
}

// condenseCoverageNotes folds the per-kind "was not recorded" notes into one
// line per side. An estate whose runs predate host, cluster, datastore,
// resource-pool and dvswitch collection produced ten separate lines here,
// which pushed the changes themselves off a short terminal — the same ten
// facts fit on one line each, named by kind.
func condenseCoverageNotes(notes []string) []string {
	const suffix = " collection was not recorded in "
	kinds := map[string][]string{}
	var scopes, other []string
	for _, note := range notes {
		i := strings.Index(note, suffix)
		if i < 0 {
			other = append(other, note)
			continue
		}
		kind, scope := note[:i], note[i+len(suffix):]
		if _, seen := kinds[scope]; !seen {
			scopes = append(scopes, scope)
		}
		kinds[scope] = append(kinds[scope], kind)
	}
	sort.Strings(scopes)
	for _, scope := range scopes {
		sort.Strings(kinds[scope])
	}
	// A ledger that predates a collection is missing it on both sides, which
	// is one fact, not two.
	if len(scopes) == 2 && strings.Join(kinds[scopes[0]], ",") == strings.Join(kinds[scopes[1]], ",") {
		return append([]string{"not collected in either run: " + strings.Join(kinds[scopes[0]], ", ")}, other...)
	}
	out := make([]string, 0, len(scopes)+len(other))
	for _, scope := range scopes {
		out = append(out, "not collected in the "+scope+": "+strings.Join(kinds[scope], ", "))
	}
	return append(out, other...)
}

// formatInterval renders the gap between two runs the way an operator would
// say it out loud: minutes below an hour, hours and minutes below a day,
// whole days and hours beyond that.
func formatInterval(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		hours, minutes := int(d.Hours()), int(d.Minutes())%60
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		days := int(d.Hours()) / 24
		hours := int(d.Hours()) % 24
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, hours)
	}
}

// countSegment colors one figure in the counts line by what it means: dim
// when it is zero (nothing to draw the eye), the semantic style otherwise.
// This is what stops "-96 vanished" and "0 moved" from reading as equally
// important, which the old single dim-bold line did not distinguish.
func countSegment(t theme, n int, label string, style lipgloss.Style) string {
	if n == 0 {
		return t.faint.Render(fmt.Sprintf("%d %s", n, label))
	}
	return style.Render(fmt.Sprintf("%d %s", n, label))
}

func (m *Model) handleChangesKey(msg tea.KeyMsg) tea.Cmd {
	if m.historyPane != historyPaneChanges {
		switch {
		case (m.historyPane == historyPaneHealth || m.historyPane == historyPaneTrends) && key.Matches(msg, m.keys.Up):
			m.offset = max(0, m.offset-1)
			return nil
		case (m.historyPane == historyPaneHealth || m.historyPane == historyPaneTrends) && key.Matches(msg, m.keys.Down):
			m.offset++
			return nil
		case key.Matches(msg, m.keys.PrevPane):
			m.historyPane = (m.historyPane + historyPaneCount - 1) % historyPaneCount
			m.offset = 0
			return nil
		case key.Matches(msg, m.keys.NextPane):
			m.historyPane = (m.historyPane + 1) % historyPaneCount
			m.offset = 0
			return nil
		case key.Matches(msg, m.keys.Capture):
			// Capture is the hub's primary action, so it answers from every
			// pane rather than only from Changes.
			return m.captureCommand()
		case key.Matches(msg, m.keys.Back):
			return m.exitHistoryToBrowse()
		case key.Matches(msg, m.keys.Up):
			if m.historyPane == historyPaneRuns {
				m.runCursor = clamp(m.runCursor-1, 0, max(0, len(m.runs)-1))
			}
			return nil
		case key.Matches(msg, m.keys.Down):
			if m.historyPane == historyPaneRuns {
				m.runCursor = clamp(m.runCursor+1, 0, max(0, len(m.runs)-1))
			}
			return nil
		case key.Matches(msg, m.keys.EditRun):
			if m.historyPane == historyPaneRuns {
				return m.beginRunEdit("label")
			}
		case key.Matches(msg, m.keys.NoteRun):
			if m.historyPane == historyPaneRuns {
				return m.beginRunEdit("note")
			}
		case key.Matches(msg, m.keys.PinRun):
			if m.historyPane == historyPaneRuns && len(m.runs) > 0 && m.assessment != nil {
				return toggleHistoryRunPinCmd(m.ctx, m.assessment, m.runs[clamp(m.runCursor, 0, len(m.runs)-1)])
			}
		}
		return nil
	}
	rows := m.scopeRows()
	switch {
	case key.Matches(msg, m.keys.PrevPane):
		m.historyPane = (m.historyPane + historyPaneCount - 1) % historyPaneCount
		return nil
	case key.Matches(msg, m.keys.NextPane):
		m.historyPane = historyPaneTrends
		return nil
	case key.Matches(msg, m.keys.History), key.Matches(msg, m.keys.Back):
		return m.exitHistoryToBrowse()
	case key.Matches(msg, m.keys.Timeline):
		if len(rows) == 0 || m.changeCursor >= len(rows) {
			return nil
		}
		return m.openTimelineForRow(rows[m.changeCursor].lead(), modeChanges)
	case key.Matches(msg, m.keys.Up):
		m.changeCursor = clamp(m.changeCursor-1, 0, max(0, len(rows)-1))
		m.scrollChangesIntoView(len(rows))
	case key.Matches(msg, m.keys.Down):
		m.changeCursor = clamp(m.changeCursor+1, 0, max(0, len(rows)-1))
		m.scrollChangesIntoView(len(rows))
	case key.Matches(msg, m.keys.Filter):
		m.filtering = true
		return m.filter.Focus()
	case key.Matches(msg, m.keys.Capture):
		return m.captureCommand()
	case key.Matches(msg, m.keys.ImpactFilter):
		m.setImpactFilter(msg.String())
		return nil
	case key.Matches(msg, m.keys.ClipSpan):
		return m.clipSpanToSharedCoverage()
	case key.Matches(msg, m.keys.ScrubPrev):
		return m.moveScrubHandle(1)
	case key.Matches(msg, m.keys.ScrubNext):
		return m.moveScrubHandle(-1)
	case key.Matches(msg, m.keys.Swap):
		if m.baseRun == 0 || m.targetRun == 0 {
			return nil
		}
		m.baseRun, m.targetRun = m.targetRun, m.baseRun
		return m.historyDiffCommand()
	case key.Matches(msg, m.keys.Base):
		m.scrubHandle = "b"
		return nil
	case key.Matches(msg, m.keys.Target):
		m.scrubHandle = "t"
		return nil
	case key.Matches(msg, m.keys.PickRun):
		// The axis is the everyday way to move an end; this is the way to
		// reach a run the axis window is nowhere near.
		if len(m.runs) == 0 {
			return nil
		}
		m.pickerRole = "target"
		m.runCursor = m.runIndex(m.targetRun)
		if m.scrubHandle == "b" {
			m.pickerRole = "base"
			m.runCursor = m.runIndex(m.baseRun)
		}
		m.mode = modeHistoryRuns
		return nil
	case key.Matches(msg, m.keys.Open):
		// Above the split threshold the inspector is already showing beside
		// the list and follows the cursor, so there is nothing left for
		// Enter to open — that is the whole point of the split.
		if len(rows) > 0 && m.changeCursor < len(rows) && m.historySplitWidth() == 0 {
			m.mode = modeChangeDetail
		}
	}
	return nil
}

// setImpactFilter narrows the stream to one class of change, or clears the
// filter when the same key is pressed again or "0" is. The cursor returns to
// the top because the row it was on is usually not in the new set.
func (m *Model) setImpactFilter(key string) {
	want := impactAll
	switch key {
	case "1":
		want = impactBlocks
	case "2":
		want = impactSizing
	case "3":
		want = impactGrowth
	case "4":
		want = impactChurn
	}
	if want == m.impactFilter {
		want = impactAll
	}
	m.impactFilter = want
	m.changeCursor, m.changeOffset = 0, 0
}

// moveScrubHandle slides the active end of the comparison along the run axis.
// step is in run-list indices, so +1 is one run older and -1 one run newer.
// The other end is stepped over rather than landed on: a comparison of a run
// with itself is not a state worth passing through.
func (m *Model) moveScrubHandle(step int) tea.Cmd {
	if len(m.runs) < 2 {
		return nil
	}
	active, other := m.targetRun, m.baseRun
	if m.scrubHandle == "b" {
		active, other = m.baseRun, m.targetRun
	}
	i := m.runIndex(active) + step
	if i >= 0 && i < len(m.runs) && m.runs[i].ID == other {
		i += step
	}
	if i < 0 || i >= len(m.runs) {
		return nil
	}
	if m.scrubHandle == "b" {
		m.baseRun = m.runs[i].ID
	} else {
		m.targetRun = m.runs[i].ID
	}
	m.centreScrubWindow()
	m.historyErr = nil
	return m.historyDiffCommand()
}

// clipSpanToSharedCoverage moves the baseline to the newest older run that
// reached exactly the vCenters the target reached. This is the fix for the
// diff's loudest failure mode — a narrower capture reading as mass deletion —
// as one key rather than as a warning the operator has to act on by hand.
func (m *Model) clipSpanToSharedCoverage() tea.Cmd {
	if m.historyCoverage == nil {
		m.setMessage("coverage is still loading", false)
		return nil
	}
	id, ok := clipTarget(m.runs, m.historyCoverage, m.targetRun)
	if !ok {
		m.setMessage("no older assessment covered the same vCenters as "+historyRunLabel(m.targetRun), true)
		return nil
	}
	if id == m.baseRun {
		m.setMessage("baseline "+historyRunLabel(id)+" already covers the same vCenters as the target", false)
		return nil
	}
	m.baseRun = id
	m.centreScrubWindow()
	m.historyErr = nil
	m.setMessage("clipped the span to "+historyRunLabel(id)+" — the newest baseline with the target's coverage", false)
	return m.historyDiffCommand()
}

// centreScrubWindow scrolls the run axis so both ends of the comparison are
// on it. An end you moved off the edge of the axis is an end you can no
// longer see you are moving.
func (m *Model) centreScrubWindow() {
	if len(m.runs) == 0 {
		return
	}
	_, count := scrubWindow(m.runs, m.scrubOffset, m.width)
	newest := min(m.runIndex(m.baseRun), m.runIndex(m.targetRun))
	oldest := max(m.runIndex(m.baseRun), m.runIndex(m.targetRun))
	offset := m.scrubOffset
	if newest < offset {
		offset = newest
	}
	if oldest >= offset+count {
		offset = oldest - count + 1
	}
	m.scrubOffset = clamp(offset, 0, max(0, len(m.runs)-count))
}

// openTimelineForRow opens the VM timeline for one Changes row, remembering
// from so Esc on the timeline returns to whichever screen — the Changes
// pane, or its narrow-terminal change-detail fallback — actually opened it.
func (m *Model) openTimelineForRow(row historyRow, from mode) tea.Cmd {
	if m.assessment == nil {
		return nil
	}
	query := row.label
	if i := strings.Index(query, " / "); i >= 0 {
		query = query[:i]
	}
	m.timelineQuery = query
	m.timelineAll = false
	m.timelineCursor, m.timelineOffset = 0, 0
	m.historyErr = nil
	m.timelineFrom = from
	m.mode = modeHistoryTimeline
	return loadHistoryTimelineCmd(m.ctx, m.assessment, query, false, false)
}

// handleChangeDetailKey drives the narrow-terminal change-detail fallback.
// Up/Down move the underlying cursor and re-render this row's detail without
// leaving the mode, so paging through changes below the split threshold does
// not mean back, down, enter, repeat. Timeline opens that VM's full history —
// previously advertised in the footer here but silently dropped by the
// dispatch, so pressing it did nothing.
func (m *Model) handleChangeDetailKey(msg tea.KeyMsg) tea.Cmd {
	rows := m.scopeRows()
	switch {
	case key.Matches(msg, m.keys.Back):
		m.mode = modeChanges
	case key.Matches(msg, m.keys.Up):
		m.changeCursor = clamp(m.changeCursor-1, 0, max(0, len(rows)-1))
		m.scrollChangesIntoView(len(rows))
	case key.Matches(msg, m.keys.Down):
		m.changeCursor = clamp(m.changeCursor+1, 0, max(0, len(rows)-1))
		m.scrollChangesIntoView(len(rows))
	case key.Matches(msg, m.keys.Timeline):
		if len(rows) == 0 || m.changeCursor >= len(rows) {
			return nil
		}
		return m.openTimelineForRow(rows[m.changeCursor].lead(), modeChangeDetail)
	}
	return nil
}

// captureContexts is what a capture reads: the vCenters in scope, not every
// vCenter configured. The all-vCenters view is how an estate-wide capture is
// asked for; with one vCenter on screen, "n" must not connect to the others or
// ask for their passwords.
func (m *Model) captureContexts() []*config.Context {
	states := m.inScope()
	contexts := make([]*config.Context, 0, len(states))
	for _, st := range states {
		contexts = append(contexts, st.cc)
	}
	return contexts
}

// historyScope is the stored-context filter the History panes read under: the
// names of the vCenter(s) in scope, or nil for the all-vCenters view, which is
// every stored context. It is deliberately the same scope "n" captures, so the
// pane answers for the vCenter the operator is actually looking at.
func (m *Model) historyScope() []string {
	if m.allScope {
		return nil
	}
	states := m.inScope()
	names := make([]string, 0, len(states))
	for _, st := range states {
		if st.cc != nil && st.cc.Name != "" {
			names = append(names, st.cc.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

// historyScopeLabel names that scope for a pane header, so a number is never
// read against the wrong estate.
func (m *Model) historyScopeLabel() string {
	names := m.historyScope()
	switch {
	case len(names) == 0:
		return "all vCenters"
	case len(names) == 1:
		return names[0]
	default:
		return fmt.Sprintf("%d vCenters", len(names))
	}
}

// historyScopeError translates the store's "never recorded that context" error
// into what it actually means here. The operator did not mistype a selector:
// the vCenter they have selected simply has not been captured yet, which is an
// empty pane to explain rather than a failure to report.
func (m *Model) historyScopeError(err error, evidence string) error {
	if err == nil || !errors.Is(err, assessment.ErrUnknownStoredContext) {
		return err
	}
	msg := "no " + evidence + " for " + m.historyScopeLabel()
	if m.canCapture() {
		msg += " — press n to capture"
	}
	return fmt.Errorf("%s", msg)
}

// captureCommand starts a capture across the vCenter(s) in scope, ignoring
// the keypress when history is unavailable, a capture is already running, or
// scope is empty.
func (m *Model) captureCommand() tea.Cmd {
	if m.assessment == nil || m.capturing {
		return nil
	}
	// A store-only service has nothing to capture with. Bail before touching
	// m.capturing so the hub cannot enter a state only a collector can leave.
	if !m.canCapture() {
		m.historyErr = fmt.Errorf("capture is not available in this session — the stored assessments are read-only")
		return nil
	}
	states := m.inScope()
	if len(states) == 0 {
		m.historyErr = fmt.Errorf("no vCenter in scope to capture")
		return nil
	}
	m.capturing = true
	m.historyErr = nil
	for _, st := range states {
		st.capturing = true
	}
	m.setMessage(captureScopeMessage(states), false)
	// A capture can be the only thing running, in which case no spinner tick
	// is in flight to inherit — start one, the way every other background
	// action does, so the spinner beside "capturing…" actually turns.
	return tea.Batch(captureHistoryCmd(m.ctx, m.assessment, m.captureContexts()), m.spin.Tick)
}

// captureScopeLabel names a set of contexts: the single vCenter's name, or a
// count when there are several — the all-vCenters view is the only way to
// reach more than one.
func captureScopeLabel(states []*contextState) string {
	if len(states) == 1 {
		return states[0].cc.Name
	}
	return fmt.Sprintf("%d vCenters", len(states))
}

// captureScopeMessage is the status line shown the moment "n" is pressed.
func captureScopeMessage(states []*contextState) string {
	return "capturing " + captureScopeLabel(states) + "…"
}

// capturingStates is the vCenter(s) a running capture actually covers, fixed
// at the moment "n" was pressed. The operator's current scope may have moved
// on since (e.g. narrowing back from the all-vCenters view), so the in-flight
// message reports what was captured, not what is on screen now.
func (m *Model) capturingStates() []*contextState {
	var states []*contextState
	for _, st := range m.states {
		if st.capturing {
			states = append(states, st)
		}
	}
	return states
}

func (m *Model) runIndex(id int64) int {
	for i, r := range m.runs {
		if r.ID == id {
			return i
		}
	}
	return 0
}

func (m *Model) handleHistoryRunsKey(msg tea.KeyMsg) tea.Cmd {
	if len(m.runs) == 0 {
		if key.Matches(msg, m.keys.Back) {
			m.mode = modeChanges
		}
		return nil
	}
	switch {
	case key.Matches(msg, m.keys.Back):
		m.mode = modeChanges
	case key.Matches(msg, m.keys.Up):
		m.runCursor = clamp(m.runCursor-1, 0, len(m.runs)-1)
	case key.Matches(msg, m.keys.Down):
		m.runCursor = clamp(m.runCursor+1, 0, len(m.runs)-1)
	case key.Matches(msg, m.keys.Home):
		m.runCursor = 0
	case key.Matches(msg, m.keys.End):
		m.runCursor = len(m.runs) - 1
	case key.Matches(msg, m.keys.Open):
		selected := m.runs[clamp(m.runCursor, 0, len(m.runs)-1)].ID
		if (m.pickerRole == "base" && selected == m.targetRun) || (m.pickerRole == "target" && selected == m.baseRun) {
			m.historyErr = fmt.Errorf("baseline and target must be different runs")
			return nil
		}
		if m.pickerRole == "base" {
			m.baseRun = selected
		} else {
			m.targetRun = selected
		}
		m.mode = modeChanges
		m.historyErr = nil
		return m.historyDiffCommand()
	}
	return nil
}

func (m *Model) viewHistoryRuns() []string {
	t := m.theme
	role := m.pickerRole
	if role == "" {
		role = "run"
	}
	lines := []string{t.title.Render("Select " + role + " assessment"), "", t.dim.Render("  newest first · enter selects · esc cancels")}
	for i, r := range m.runs {
		// The cursor takes the first column and the B/T role the second, so
		// a row can be both under the cursor and the baseline, and the
		// cursor survives a palette with no highlight (see cursorMark).
		role := " "
		if r.ID == m.baseRun {
			role = "B"
		}
		if r.ID == m.targetRun {
			role = "T"
		}
		marker := listCursor(i == m.runCursor) + role + " "
		label := r.Label
		if label == "" {
			label = "—"
		}
		if r.Pinned {
			label = "📌 " + label
		}
		line := fmt.Sprintf("%s%-5s %-18s %-8s %-10s %s", marker, historyRunLabel(r.ID), truncate(label, 18), r.Status, r.StartedAt.Local().Format("2006-01-02 15:04"), r.Source)
		if i == m.runCursor {
			line = t.focused.Render(line)
		} else {
			line = t.text.Render(line)
		}
		lines = append(lines, line)
	}
	return scrollLines(lines, 0, m.bodyHeight())
}

func (m *Model) viewChanges() []string {
	if m.historyPane == historyPaneHealth {
		return m.viewHistoryHealth()
	}
	if m.historyPane == historyPaneTrends {
		return m.viewHistoryTrends()
	}
	if m.historyPane == historyPaneRuns {
		return m.viewHistoryHubRuns()
	}
	t := m.theme
	lines := m.changesHeaderLines()
	if m.historyErr != nil {
		lines = append(lines, "  "+t.warn.Render(m.historyErr.Error()))
		if m.canCapture() {
			lines = append(lines, t.dim.Render("  press n to capture "+captureScopeLabel(m.inScope())))
		}
		return lines
	}
	if m.changeDiff == nil {
		return append(lines, t.dim.Render("  no comparable assessments"))
	}
	rows := m.scopeRows()
	if len(rows) == 0 {
		if m.impactFilter != impactAll {
			return append(lines, t.dim.Render("  nothing in this span "+m.impactFilter.why()+" — 0 clears the filter"))
		}
		return append(lines, t.dim.Render("  no changes in the selected assessments"))
	}
	avail := m.changesListHeight()
	// Above the split threshold the inspector that used to be a whole
	// separate screen — Enter, look, Esc, repeat — sits beside the stream and
	// follows the cursor. Below it, the stream keeps the full width and Enter
	// still opens a full-screen inspector.
	if splitW := m.historySplitWidth(); splitW > 0 {
		rightW := m.width - splitW - 3
		list := m.renderScopeStream(rows, splitW, avail)
		var inspector []string
		if m.changeCursor >= 0 && m.changeCursor < len(rows) {
			inspector = m.scopeInspector(rows[m.changeCursor])
		}
		return append(lines, joinSideBySide(t, list, inspector, splitW, rightW, avail)...)
	}
	return append(lines, m.renderScopeStream(rows, m.width, avail)...)
}

// changesHeaderLines is everything above the change stream: the run axis, the
// coverage under it, the identity of the two runs being compared, and the
// counts. It is one function rather than inline rendering so the stream's
// height budget is measured against exactly what was drawn, instead of an
// arithmetic estimate that drifts every time a band gains a line.
func (m *Model) changesHeaderLines() []string {
	t := m.theme
	var lines []string
	if m.capturing {
		lines = append(lines, "  "+m.spin.View()+t.dim.Render("capturing VM inventory from "+captureScopeLabel(m.capturingStates())+"…"))
	}
	// The capture-size row is the first thing to go on a short terminal: it
	// is the only band on this screen that is decoration rather than an
	// answer, since the counts below say what actually changed.
	lines = append(lines, m.viewScrubber(m.width, m.bodyHeight() >= 18)...)
	lines = append(lines, m.viewCoverage(m.width)...)
	if m.changeDiff == nil {
		return append(lines, "")
	}
	lines = append(lines, m.viewComparisonBar(m.changeDiff)...)
	c := m.changeDiff.Counts
	counts := strings.Join([]string{
		countSegment(t, c.Vanished, "vanished", t.bad),
		countSegment(t, c.Appeared, "appeared", t.ok),
		countSegment(t, c.Moved, "moved", t.warn),
		countSegment(t, c.Modified, "modified", t.warn),
		countSegment(t, c.Resources, "infra", t.warn),
		countSegment(t, c.Snapshots, "snapshots", t.warn),
	}, t.faint.Render("   "))
	lines = append(lines, truncate("  "+counts, m.width))
	if m.impactFilter != impactAll {
		lines = append(lines, truncate("  "+t.accent.Render(m.impactFilter.String())+t.dim.Render(" only — "+m.impactFilter.why()+" · 0 clears"), m.width))
	}
	return append(lines, "")
}

func (m *Model) viewHistoryHealth() []string {
	t := m.theme
	// Health is not scoped: its rules run over the whole stored assessment, so
	// the header says so rather than letting it be read as the selected
	// vCenter's verdict.
	lines := []string{t.title.Render("Health"), "", t.dim.Render("  all vCenters · read-only findings from the latest stored assessment")}
	if m.historyHealthErr != nil {
		return append(lines, "  "+t.warn.Render(m.historyHealthErr.Error()))
	}
	if m.historyHealth == nil {
		return append(lines, t.dim.Render("  loading health findings…"))
	}
	r := m.historyHealth
	readiness := health.Readiness(*r)
	// Critical leads the counts and the total comes last. The line is cut at
	// the terminal width, and on an 80 column terminal the figure that goes
	// missing must be the least actionable one, never the critical count.
	c := r.Counts
	sep := t.faint.Render(" · ")
	summary := "  assessment " + historyRunLabel(r.RunID) + "  " + strings.ToUpper(readiness.Verdict) + sep +
		countSegment(t, c.Critical, "critical", t.bad) + sep +
		countSegment(t, c.Warning, "warning", t.warn) + sep +
		countSegment(t, c.Info, "info", t.dim) + sep +
		t.faint.Render(fmt.Sprintf("%d total", c.Total))
	lines = append(lines, truncate(summary, m.width), "")
	if len(r.Findings) == 0 {
		lines = append(lines, t.ok.Render("  no findings"))
	} else {
		cols := healthColumns(r.Findings, m.width)
		head := make([]string, 0, len(cols))
		for _, col := range cols {
			head = append(head, pad(col.title, col.width, false))
		}
		lines = append(lines, t.header.Render(truncate("  "+strings.Join(head, " "), m.width)))
		for _, finding := range sortFindingsBySeverity(r.Findings) {
			cells := make([]string, 0, len(cols))
			for _, col := range cols {
				cells = append(cells, pad(col.cell(finding), col.width, false))
			}
			style := t.warn
			if finding.Severity == health.SeverityCritical {
				style = t.bad
			} else if finding.Severity == health.SeverityInfo {
				style = t.dim
			}
			lines = append(lines, style.Render(truncate("  "+strings.Join(cells, " "), m.width)))
		}
	}
	for _, rule := range r.Rules {
		if rule.Status == "not-evaluated" || rule.Result == "unknown" {
			reason := rule.Reason
			if len(rule.Blind) > 0 {
				reason = strings.TrimSpace(strings.TrimSuffix(reason, ".") + "; blind contexts: " + strings.Join(rule.Blind, ", "))
			}
			lines = append(lines, t.dim.Render("  not evaluated: "+rule.Rule+" — "+reason))
		}
	}
	return scrollLines(lines, m.offset, m.bodyHeight())
}

// sortFindingsBySeverity lists critical findings first. The stored order is
// by rule, which on a real estate puts hundreds of informational findings in
// front of the handful that block a migration, and the pane scrolls: the
// critical rows were off the bottom of the screen. The sort is stable, so
// within a severity the report's own order is kept and the list does not
// shuffle between renders.
func sortFindingsBySeverity(in []health.Finding) []health.Finding {
	out := append([]health.Finding(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		return findingSeverityRank(out[i].Severity) > findingSeverityRank(out[j].Severity)
	})
	return out
}

func findingSeverityRank(s health.Severity) int {
	switch s {
	case health.SeverityCritical:
		return 2
	case health.SeverityWarning:
		return 1
	default:
		return 0
	}
}

// healthColumn is one column of the findings table: its heading, how to read
// it off a finding, and the width it is drawn at.
type healthColumn struct {
	title string
	width int
	cell  func(health.Finding) string
}

// healthMessageMin is the least width the MESSAGE column is given before
// lower-value columns are dropped to make room. The message is what tells the
// operator what to do, so it is worth more than CATEGORY or CONFIDENCE.
const healthMessageMin = 24

// healthMessageSqueezed is the floor once RULE and OBJECT are being
// shortened as well: below it a message is too clipped to read.
const healthMessageSqueezed = 20

// healthColumns sizes the findings table to the data and the terminal.
// Columns take the width of their longest value rather than a fixed
// allotment, and a column with no values at all is not drawn: CONFIDENCE is
// only ever set by the orphan-VMDK rule, and reserving 26 columns for it
// pushed OBJECT off an 80 column screen. When even that is too wide,
// CONFIDENCE goes first, then CATEGORY, and last RULE and OBJECT are
// shortened. OBJECT is never dropped: a finding that does not say what it is
// about cannot be acted on.
func healthColumns(findings []health.Finding, width int) []healthColumn {
	sev := healthColumn{"SEVERITY", 8, func(f health.Finding) string { return string(f.Severity) }}
	conf := healthColumn{title: "CONFIDENCE", cell: func(f health.Finding) string { return string(f.Confidence) }}
	cat := healthColumn{title: "CATEGORY", cell: func(f health.Finding) string { return string(f.Category) }}
	rule := healthColumn{title: "RULE", cell: func(f health.Finding) string { return f.Rule }}
	obj := healthColumn{title: "OBJECT", cell: func(f health.Finding) string { return f.Object.Kind + "/" + f.Object.Name }}
	msg := healthColumn{"MESSAGE", 0, func(f health.Finding) string { return f.Message }}

	// fit sets a column to the width of its longest value, never wider than
	// limit, and reports whether any finding had a value at all.
	fit := func(c *healthColumn, limit int) bool {
		c.width = ansi.StringWidth(c.title)
		found := false
		for _, f := range findings {
			if v := c.cell(f); v != "" {
				found = true
				c.width = max(c.width, ansi.StringWidth(v))
			}
		}
		c.width = min(c.width, limit)
		return found
	}
	hasConfidence := fit(&conf, 12)
	fit(&cat, 14)
	fit(&rule, 28)
	fit(&obj, 30)

	// fits reports whether the columns, their single-space gaps, the two
	// column indent and the minimum message width all fit.
	fits := func(cols []healthColumn) bool {
		used := 2 + healthMessageMin
		for _, c := range cols {
			used += c.width + 1
		}
		return used <= width
	}
	cols := []healthColumn{sev}
	if hasConfidence {
		cols = append(cols, conf)
	}
	cols = append(cols, cat, rule, obj)
	if !fits(cols) && hasConfidence {
		cols = []healthColumn{sev, cat, rule, obj}
	}
	if !fits(cols) {
		cols = []healthColumn{sev, rule, obj}
	}
	if !fits(cols) {
		// Still too wide: hold the message at a smaller floor, cap RULE (its
		// ids are the most compressible text here), and give OBJECT what the
		// rest leaves.
		rule.width = min(rule.width, 22)
		obj.width = max(12, min(obj.width, width-2-(sev.width+1)-(rule.width+1)-1-healthMessageSqueezed))
		cols = []healthColumn{sev, rule, obj}
	}
	used := 2
	for _, c := range cols {
		used += c.width + 1
	}
	msg.width = max(1, width-used)
	return append(cols, msg)
}

// changesListHeight is how many rows the change stream can draw — its own
// heading included — under the bands above it. It measures the rendered
// header rather than re-deriving its height, so cursor movement scrolls
// against the exact budget the view draws with; without it, Up/Down could
// move the cursor past what is drawn with no way to see where it went.
func (m *Model) changesListHeight() int {
	return max(1, m.bodyHeight()-len(m.changesHeaderLines()))
}

// scrollChangesIntoView keeps changeCursor's row inside the window
// renderScopeStream draws, the same job scrollIntoView does for the browse
// table's m.offset.
func (m *Model) scrollChangesIntoView(n int) {
	h := m.changesListHeight() - 1 // the list's own heading claims one row
	if h <= 0 {
		return
	}
	if m.changeCursor < m.changeOffset {
		m.changeOffset = m.changeCursor
	}
	if m.changeCursor >= m.changeOffset+h {
		m.changeOffset = m.changeCursor - h + 1
	}
	m.changeOffset = clamp(m.changeOffset, 0, max(0, n-h))
}

// historySplitWidth is the left column's width once the terminal is wide
// enough to show the inspector beside the list instead of behind a
// full-screen mode; 0 means the split does not apply at this width.
func (m *Model) historySplitWidth() int {
	const minSplitWidth = 92
	if m.width < minSplitWidth {
		return 0
	}
	w := m.width * 42 / 100
	if w < 34 {
		w = 34
	}
	if w > 52 {
		w = 52
	}
	return w
}

// joinSideBySide pads two blocks of lines to fixed widths and joins them
// with a vertical rule, for the wide-terminal split between the change
// stream and its inspector.
func joinSideBySide(t theme, left, right []string, leftW, rightW, height int) []string {
	rule := t.rule.Render("│")
	out := make([]string, height)
	for i := 0; i < height; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = pad(l, leftW, false) + " " + rule + " " + pad(r, rightW, false)
	}
	return out
}

func (m *Model) viewHistoryHubRuns() []string {
	t := m.theme
	// The key hints live in the footer with every other pane's bindings; this
	// line only says how the list is ordered.
	lines := []string{t.title.Render("Runs"), "", t.dim.Render("  newest first")}
	for i, r := range m.runs {
		label := r.Label
		if label == "" {
			label = "—"
		}
		if r.Pinned {
			label = "📌 " + label
		}
		line := fmt.Sprintf("%s%-5s %-18s %-9s %s", listCursor(i == m.runCursor)+" ", historyRunLabel(r.ID), truncate(label, 18), r.Status, r.StartedAt.Local().Format("2006-01-02 15:04"))
		if i == m.runCursor {
			line = t.focused.Render(line)
		} else {
			line = t.text.Render(line)
		}
		lines = append(lines, line)
	}
	if len(m.runs) == 0 {
		lines = append(lines, t.dim.Render("  no assessments stored"))
	}
	return scrollLines(lines, 0, m.bodyHeight())
}

func newRunEditInput() textinput.Model {
	input := textinput.New()
	input.CharLimit = 256
	input.Prompt = ""
	return input
}

func (m *Model) beginRunEdit(field string) tea.Cmd {
	if m.assessment == nil || len(m.runs) == 0 {
		return nil
	}
	run := m.runs[clamp(m.runCursor, 0, len(m.runs)-1)]
	m.runEditRunID, m.runEditKind = run.ID, field
	value := run.Label
	if field == "note" {
		value = run.Note
	}
	m.runEditInput.SetValue(value)
	m.runEditInput.Placeholder = "empty clears " + field
	m.runEditInput.Focus()
	m.mode = modeHistoryRunEdit
	m.historyErr = nil
	return nil
}

func (m *Model) handleHistoryRunEditKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEsc:
		m.runEditInput.Blur()
		m.runEditKind, m.runEditRunID = "", 0
		m.mode = modeChanges
		return nil
	case tea.KeyEnter:
		if m.assessment == nil || m.runEditRunID == 0 {
			return nil
		}
		return updateHistoryRunCmd(m.ctx, m.assessment, m.runEditRunID, m.runEditKind, m.runEditInput.Value())
	}
	var cmd tea.Cmd
	m.runEditInput, cmd = m.runEditInput.Update(msg)
	return cmd
}

func (m *Model) viewHistoryRunEdit() []string {
	t := m.theme
	field := m.runEditKind
	if field == "" {
		field = "metadata"
	}
	return []string{t.title.Render("Edit run " + field), "", "  " + t.dim.Render("enter saves · esc cancels"), "", "  " + m.runEditInput.View()}
}

func (m *Model) viewHistoryTrends() []string {
	t := m.theme
	// Scrolling is advertised by the shared footer, alongside the rest of the
	// History pane bindings. Keep this subtitle about the data being shown.
	sub := "last 30 complete assessments"
	if scope := m.historyScopeLabel(); scope != "" {
		sub = scope + " · " + sub
	}
	head := []string{t.title.Render("Trends"), "", t.dim.Render("  " + sub)}
	if len(m.historyTrendsEmpty) > 0 {
		lines := append(head, "")
		explanation := append([]string(nil), m.historyTrendsEmpty...)
		action := "Fix collection failures, then capture a complete assessment."
		if m.canCapture() {
			action = "Fix collection failures, then press n to capture again."
		}
		if m.demo {
			action = "Demo history is fixed; complete runs are needed for Trends."
		}
		explanation = append(explanation, action)
		for _, paragraph := range explanation {
			for _, line := range wrap(paragraph, m.width-4) {
				lines = append(lines, "  "+t.warn.Render(line))
			}
		}
		m.offset = clamp(m.offset, 0, max(0, len(lines)-m.bodyHeight()))
		return scrollLines(lines, m.offset, m.bodyHeight())
	}
	if m.historyTrendsErr != nil {
		return append(head, t.warn.Render("  "+m.historyTrendsErr.Error()))
	}
	if m.historyChurn == nil || m.historySnapshots == nil {
		return append(head, t.dim.Render("  loading history trends…"))
	}
	if len(m.historyChurn.Points) == 0 {
		return append(head, t.dim.Render("  no complete assessments stored — capture a complete run"))
	}
	lines := append(head, "")
	lines = append(lines, m.trendCapacityLines()...)
	lines = append(lines, m.trendDatastoreLines()...)
	lines = append(lines, m.trendHistoryLines()...)
	h := m.bodyHeight()
	m.offset = clamp(m.offset, 0, max(0, len(lines)-h))
	return scrollLines(lines, m.offset, h)
}

// trendCapacityLines is the estate's latest capacity, one row per resource
// kind, so the numbers beside it read as "what the estate holds now".
func (m *Model) trendCapacityLines() []string {
	t := m.theme
	if m.historyCapacity == nil {
		return nil
	}
	var rows []string
	for _, series := range m.historyCapacity.Series {
		if series.Scope != "estate" || len(series.Points) == 0 {
			continue
		}
		point := series.Points[len(series.Points)-1]
		value := t.dim.Render("—")
		if point.StorageCapacity != nil {
			value = "storage " + tuiBytes(*point.StorageCapacity)
		} else if point.CPUCapacity != nil {
			value = "cpu " + humanize.MHz(int64(*point.CPUCapacity))
		}
		rows = append(rows, fmt.Sprintf("  %-12s %s", series.Kind, value))
	}
	if len(rows) == 0 {
		return nil
	}
	return append(append([]string{t.header.Render("  CAPACITY NOW")}, rows...), "")
}

// trendDatastoreLines ranks datastores by how soon they run out, then by
// growth. Datastores with neither are folded into one line: a wall of
// "growth — projected unknown" rows says nothing and buries the ones that do.
func (m *Model) trendDatastoreLines() []string {
	t := m.theme
	if m.historyCapacityReport == nil || len(m.historyCapacityReport.Datastores) == 0 {
		return nil
	}
	var known, unknown []assessment.DatastoreCapacity
	for _, ds := range m.historyCapacityReport.Datastores {
		if ds.UsedGrowthBytes == nil && (ds.Projection == nil || ds.Projection.DaysRemaining == nil) {
			unknown = append(unknown, ds)
			continue
		}
		known = append(known, ds)
	}
	sort.SliceStable(known, func(i, j int) bool {
		di, dj := known[i].Projection, known[j].Projection
		hi := di != nil && di.DaysRemaining != nil
		hj := dj != nil && dj.DaysRemaining != nil
		switch {
		case hi && hj:
			return *di.DaysRemaining < *dj.DaysRemaining
		case hi != hj:
			return hi
		}
		gi, gj := known[i].UsedGrowthBytes, known[j].UsedGrowthBytes
		return gi != nil && gj != nil && *gi > *gj
	})
	nameW := clamp(m.width-4-11-11, 16, 48)
	out := []string{t.header.Render(fmt.Sprintf("  %-*s  %10s  %9s", nameW, "DATASTORE", "GROWTH", "RUNS OUT"))}
	for _, ds := range known {
		growth := t.dim.Render(fmt.Sprintf("%10s", "—"))
		if ds.UsedGrowthBytes != nil {
			sign := ""
			if *ds.UsedGrowthBytes > 0 {
				sign = "+"
			}
			growth = fmt.Sprintf("%10s", sign+tuiBytes(*ds.UsedGrowthBytes))
		}
		runway := t.dim.Render(fmt.Sprintf("%9s", "—"))
		if ds.Projection != nil && ds.Projection.DaysRemaining != nil {
			days := *ds.Projection.DaysRemaining
			style := t.text
			switch {
			case days <= 7:
				style = t.bad
			case days <= 30:
				style = t.warn
			}
			runway = style.Render(fmt.Sprintf("%9s", fmt.Sprintf("in %.0fd", days)))
		}
		out = append(out, fmt.Sprintf("  %-*s  %s  %s", nameW, middleTruncate(ds.Object.Name, nameW), growth, runway))
	}
	if len(unknown) > 0 {
		noun := "datastores"
		if len(unknown) == 1 {
			noun = "datastore"
		}
		out = append(out, t.dim.Render(fmt.Sprintf("  + %d %s without enough history to show growth", len(unknown), noun)))
	}
	return append(out, "")
}

// trendHistoryLines is the per-assessment table, newest first so the current
// state is at the top, under sparklines that read oldest to newest.
func (m *Model) trendHistoryLines() []string {
	t := m.theme
	points := m.historyChurn.Points
	var vmValues, snapValues []float64
	for _, p := range points {
		vmValues = append(vmValues, float64(p.VMCount))
	}
	for _, p := range m.historySnapshots.Points {
		snapValues = append(snapValues, float64(p.Total))
	}
	latest := func(v []float64) string {
		if len(v) == 0 {
			return ""
		}
		return t.dim.Render(fmt.Sprintf("  now %.0f", v[len(v)-1]))
	}
	out := []string{
		t.header.Render("  ASSESSMENT HISTORY") + t.dim.Render("  (sparklines: oldest → newest)"),
		fmt.Sprintf("  %-10s %s%s", "VMs", tuiSparkline(vmValues), latest(vmValues)),
		fmt.Sprintf("  %-10s %s%s", "snapshots", tuiSparkline(snapValues), latest(snapValues)),
		"",
		t.header.Render("  RUN    WHEN              VMS   NEW  GONE MOVED  EDIT  SNAPS STALE"),
	}
	for i := len(points) - 1; i >= 0; i-- {
		p := points[i]
		sp := assessment.SnapshotTrendPoint{}
		if i < len(m.historySnapshots.Points) {
			sp = m.historySnapshots.Points[i]
		}
		row := fmt.Sprintf("  %-6s %-16s %5d %5d %5d %5d %5d %6d %5d",
			historyRunLabel(p.Run.ID), p.Run.StartedAt.Local().Format("2006-01-02 15:04"),
			p.VMCount, p.Appeared, p.Vanished, p.Moved, p.Modified, sp.Total, sp.Stale)
		if p.Appeared+p.Vanished+p.Moved+p.Modified == 0 {
			row = t.dim.Render(row)
		}
		out = append(out, row)
	}
	return out
}

// middleTruncate shortens a long identifier from the middle, keeping both the
// distinguishing prefix and suffix that datastore names tend to differ in.
func middleTruncate(s string, w int) string {
	r := []rune(s)
	if len(r) <= w || w < 5 {
		return s
	}
	keep := w - 1
	head := keep / 2
	return string(r[:head]) + "…" + string(r[len(r)-(keep-head):])
}

func tuiBytes(value float64) string {
	if value < 0 {
		return "-" + humanize.Bytes(int64(-value))
	}
	return humanize.Bytes(int64(value))
}

func tuiSparkline(values []float64) string {
	if len(values) == 0 {
		return "—"
	}
	glyphs := []rune("▁▂▃▄▅▆▇█")
	min, max := values[0], values[0]
	for _, value := range values[1:] {
		if value < min {
			min = value
		}
		if value > max {
			max = value
		}
	}
	if min == max {
		return strings.Repeat(string(glyphs[0]), len(values))
	}
	var out strings.Builder
	for _, value := range values {
		i := int((value - min) / (max - min) * float64(len(glyphs)-1))
		out.WriteRune(glyphs[i])
	}
	return out.String()
}

// viewChangeDetail is the narrow-terminal fallback for a row's detail, below
// the width historySplitWidth needs to show the inspector beside the list.
// It renders the same content historyInspector puts in the split's right
// column, full width and full height, reached by Enter instead of always on.
func (m *Model) viewChangeDetail() []string {
	rows := m.scopeRows()
	if m.changeCursor < 0 || m.changeCursor >= len(rows) {
		return []string{m.theme.dim.Render("nothing selected")}
	}
	return scrollLines(m.scopeInspector(rows[m.changeCursor]), 0, m.bodyHeight())
}

// historyInspector is the detail for one Changes row: the summary already on
// the row, then whatever the diff's own Before/After observations add. This
// is what the old change-detail screen was missing for appeared and vanished
// rows — 97 of 110 changes in the scenario that motivated this screen's
// redesign — because it read only the row's flattened summary and never the
// full observation the diff had already loaded.
func (m *Model) historyInspector(r historyRow) []string {
	t := m.theme
	lines := []string{
		t.title.Render(r.label),
		"  " + t.label.Render("context") + "  " + r.context,
		"  " + t.label.Render("kind") + "     " + r.kind,
		"  " + t.label.Render("change") + "   " + r.change,
		"",
	}
	d := m.changeDiff
	if d == nil {
		return lines
	}
	switch r.kind {
	case "vm":
		for _, v := range d.VMs {
			if v.Name == r.label && v.Context == r.context {
				lines = append(lines, m.vmInspectorFields(v)...)
				break
			}
		}
	case "snapshot":
		if r.detail != "" {
			for _, line := range wrap(r.detail, m.width-4) {
				lines = append(lines, "  "+t.value.Render(line))
			}
		}
	default:
		for _, resource := range d.Resources {
			if resource.Kind == r.kind && resource.Name == r.label && resource.Context == r.context {
				lines = append(lines, resourceInspectorFields(t, resource)...)
				break
			}
		}
	}
	return lines
}

// vmInspectorFields renders a VM's current state — from After when the VM
// still exists, from Before when it vanished — plus the field-level changes
// a modified or moved VM carries. "Current state" is dated to whichever run
// the observation came from, since a vanished VM's last-known state belongs
// to the baseline, not the target.
func (m *Model) vmInspectorFields(v assessment.VMChange) []string {
	t := m.theme
	d := m.changeDiff
	obs, run := v.After, d.Target
	if obs == nil {
		obs, run = v.Before, d.Base
	}
	if obs == nil {
		return nil
	}
	vm := obs.VM
	lines := []string{
		t.header.Render("  State"),
		fmt.Sprintf("  %-12s %s · %s", "as of", historyRunLabel(run.ID), run.StartedAt.Local().Format("2006-01-02 15:04")),
		fmt.Sprintf("  %-12s %s", "power", historyFieldValue("power_state", vm.PowerState)),
		fmt.Sprintf("  %-12s %s", "host", nonempty(vm.Host, "—")),
		fmt.Sprintf("  %-12s %s", "cluster", nonempty(vm.Cluster, "—")),
		fmt.Sprintf("  %-12s %d vCPU · %s", "size", vm.CPU, humanize.MB(vm.MemoryMB)),
		fmt.Sprintf("  %-12s %s", "datastore", nonempty(strings.Join(vm.Datastores, ", "), "—")),
		fmt.Sprintf("  %-12s %s", "ip address", nonempty(vm.IPAddress, "—")),
		fmt.Sprintf("  %-12s %s", "guest os", nonempty(vm.GuestOS, "—")),
		fmt.Sprintf("  %-12s %s", "uuid", nonempty(vm.InstanceUUID, "—")),
	}
	if v.MatchBasis != "" {
		lines = append(lines, fmt.Sprintf("  %-12s %s", "matched by", v.MatchBasis))
	}
	if len(v.Fields) > 0 {
		lines = append(lines, "", t.header.Render("  Field changes"))
		for _, f := range v.Fields {
			lines = append(lines, fmt.Sprintf("  %-18s %s → %s", f.Field, truncate(historyFieldValue(f.Field, f.Before), 24), truncate(historyFieldValue(f.Field, f.After), 24)))
		}
	}
	return lines
}

func resourceInspectorFields(t theme, r assessment.ResourceChange) []string {
	if len(r.Fields) == 0 {
		return nil
	}
	lines := []string{t.header.Render("  Field changes")}
	for _, field := range r.Fields {
		lines = append(lines, fmt.Sprintf("  %-22s %s → %s", field.Field, truncate(historyFieldValue(field.Field, field.Before), 24), truncate(historyFieldValue(field.Field, field.After), 24)))
	}
	return lines
}

func resourceDetail(r assessment.ResourceChange) string {
	if len(r.Fields) == 0 {
		return ""
	}
	parts := make([]string, len(r.Fields))
	for i, field := range r.Fields {
		parts[i] = field.Field + ":" + historyFieldValue(field.Field, field.Before) + "→" + historyFieldValue(field.Field, field.After)
	}
	return strings.Join(parts, " ")
}

func (m *Model) handleHistoryTimelineKey(msg tea.KeyMsg) tea.Cmd {
	if key.Matches(msg, m.keys.Back) {
		m.mode = m.timelineFrom
		return nil
	}
	if key.Matches(msg, m.keys.Up) {
		m.timelineCursor = clamp(m.timelineCursor-1, 0, max(0, len(m.timeline)-1))
	}
	if key.Matches(msg, m.keys.Down) {
		m.timelineCursor = clamp(m.timelineCursor+1, 0, max(0, len(m.timeline)-1))
	}
	if key.Matches(msg, m.keys.TimelineAll) {
		m.timelineAll = !m.timelineAll
		if m.assessment != nil {
			return loadHistoryTimelineCmd(m.ctx, m.assessment, m.timelineQuery, m.timelineAll, false)
		}
	}
	if key.Matches(msg, m.keys.Open) && len(m.timeline) > 0 && m.timelineCursor < len(m.timeline) {
		m.mode = modeHistoryTimelineDetail
	}
	return nil
}

func (m *Model) viewHistoryTimeline() []string {
	t := m.theme
	lines := []string{t.title.Render("VM timeline · " + m.timelineQuery), "", t.dim.Render("  changes only · a toggles unchanged observations")}
	if m.historyErr != nil {
		return append(lines, t.warn.Render("  "+m.historyErr.Error()))
	}
	if len(m.timeline) == 0 {
		return append(lines, t.dim.Render("  no timeline events"))
	}
	for i, e := range m.timeline {
		detail := ""
		if len(e.Changes) > 0 {
			parts := make([]string, len(e.Changes))
			for j, f := range e.Changes {
				parts[j] = f.Field + ":" + historyFieldValue(f.Field, f.Before) + "→" + historyFieldValue(f.Field, f.After)
			}
			detail = strings.Join(parts, " ")
		}
		line := fmt.Sprintf("%s %-16s %-16s %-12s %s", listCursor(i == m.timelineCursor), e.Run.StartedAt.Local().Format("2006-01-02 15:04"), e.Kind, e.Context, truncate(detail, max(1, m.width-52)))
		if i == m.timelineCursor {
			line = t.focused.Render(line)
		} else {
			line = t.text.Render(line)
		}
		lines = append(lines, line)
	}
	return scrollLines(lines, 0, m.bodyHeight())
}

func (m *Model) viewHistoryTimelineDetail() []string {
	t := m.theme
	if m.timelineCursor < 0 || m.timelineCursor >= len(m.timeline) {
		return []string{t.dim.Render("nothing selected")}
	}
	e := m.timeline[m.timelineCursor]
	lines := []string{t.title.Render(e.Name), "", "  " + t.label.Render("Event") + "    " + e.Kind, "  " + t.label.Render("Run") + "      " + historyRunLabel(e.Run.ID), "  " + t.label.Render("Date") + "     " + e.Run.StartedAt.Local().Format("2006-01-02 15:04:05"), "  " + t.label.Render("Context") + "  " + e.Context}
	if len(e.Changes) > 0 {
		lines = append(lines, "", t.header.Render("Changes"))
		for _, f := range e.Changes {
			lines = append(lines, fmt.Sprintf("  %-18s %s → %s", f.Field, truncate(historyFieldValue(f.Field, f.Before), 24), truncate(historyFieldValue(f.Field, f.After), 24)))
		}
	}
	return scrollLines(lines, 0, m.bodyHeight())
}

func nonempty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func changeDetail(v assessment.VMChange) string {
	if len(v.Fields) == 0 {
		return ""
	}
	parts := make([]string, len(v.Fields))
	for i, f := range v.Fields {
		parts[i] = f.Field + ":" + historyFieldValue(f.Field, f.Before) + "→" + historyFieldValue(f.Field, f.After)
	}
	return strings.Join(parts, " ")
}

func historyRunLabel(id int64) string {
	if id <= 0 {
		return "—"
	}
	return "#" + strconv.FormatInt(id, 10)
}
