package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/easonliuuuuu/vsfleet/internal/humanize"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Layout constants. The interface is built for an 80x24 terminal on a jump
// host and grows from there; nothing below assumes more.
const (
	cellGap       = 2
	glyphGutter   = 3 // cursor mark, status glyph, and the space after it
	minNameWidth  = 16
	chromeHeight  = 4 // header, tab bar or rule, message, key line
	tableChrome   = 2 // the browse rule and its column headings
	searchChrome  = 1 // headings only: the search screen's rule is the one under the header
	minTermWidth  = 40
	minTermHeight = 10
	// demoBadge marks synthetic data in the header. It stays short so the
	// scope and connection summary beside it survive a narrow terminal.
	demoBadge       = "DEMO · SAMPLE DATA"
	labelColumnPad  = 20
	tabGap          = 3
	tabGapTight     = 2
	tabGapCompact   = 1
	helpColumnWidth = 34
	ctxNameWidth    = 18
)

// View renders the whole frame.
func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width < minTermWidth || m.height < minTermHeight {
		return m.theme.dim.Render(fmt.Sprintf("terminal too small: %dx%d, need at least %dx%d",
			m.width, m.height, minTermWidth, minTermHeight))
	}
	// The line under the header is the kind bar while browsing and a plain
	// rule everywhere else. A full-screen view is not one of the kinds, so
	// leaving a tab highlighted over it would be saying something untrue.
	second := m.theme.rule.Render(strings.Repeat("─", m.width))
	var body string
	switch {
	// The credential overlay can appear over any screen, since it answers a
	// background load rather than something the operator opened, so it takes
	// priority ahead of m.mode instead of being one more case inside it.
	case m.credPrompt != nil:
		body = strings.Join(m.viewCredPrompt(), "\n")
	case m.sshPrompt != nil:
		body = strings.Join(m.viewSSHPrompt(), "\n")
	case m.mode == modeDetail:
		body = strings.Join(m.viewDetail(), "\n")
	case m.mode == modeVAppDetail:
		body = strings.Join(m.viewVAppDetail(), "\n")
	case m.mode == modeVAppVMDetail:
		body = strings.Join(m.viewVAppVMDetail(), "\n")
	case m.mode == modeSwitchDetail:
		body = strings.Join(m.viewSwitchDetail(), "\n")
	case m.mode == modeSwitchPGDetail:
		body = strings.Join(m.viewSwitchPGDetail(), "\n")
	case m.mode == modeDoctor:
		body = strings.Join(m.viewDoctor(), "\n")
	case m.mode == modeHelp:
		body = strings.Join(m.viewHelp(), "\n")
	case m.mode == modeForm:
		body = strings.Join(m.viewForm(), "\n")
	case m.mode == modeConfirmDelete:
		body = strings.Join(m.viewConfirmDelete(), "\n")
	case m.mode == modeContexts:
		body = strings.Join(m.viewContexts(), "\n")
	case m.mode == modeSearch:
		body = strings.Join(m.viewSearch(), "\n")
	case m.mode == modeChanges:
		body = strings.Join(m.viewChanges(), "\n")
	case m.mode == modeChangeDetail:
		body = strings.Join(m.viewChangeDetail(), "\n")
	case m.mode == modeHistoryRuns:
		body = strings.Join(m.viewHistoryRuns(), "\n")
	case m.mode == modeHistoryRunEdit:
		body = strings.Join(m.viewHistoryRunEdit(), "\n")
	case m.mode == modeHistoryTimeline:
		body = strings.Join(m.viewHistoryTimeline(), "\n")
	case m.mode == modeDatastoreFiles:
		body = strings.Join(m.viewDatastoreFiles(), "\n")
	case m.mode == modeDatastoreEntry:
		body = strings.Join(m.viewDatastoreEntry(), "\n")
	case m.mode == modeDatastoreFind:
		body = strings.Join(m.viewDatastoreFind(), "\n")
	case m.mode == modeHistoryTimelineDetail:
		body = strings.Join(m.viewHistoryTimelineDetail(), "\n")
	default:
		second = m.viewTabs(m.width)
		body = strings.Join(m.viewBrowse(), "\n")
	}
	// A screen with little to say (an empty history pane, say) must still
	// fill the body, or the message and key lines ride up under it instead of
	// sitting on the bottom row.
	if short := m.bodyHeight() - (strings.Count(body, "\n") + 1); short > 0 {
		body += strings.Repeat("\n", short)
	}
	return strings.Join([]string{
		m.viewHeader(),
		second,
		body,
		m.viewMessage(),
		m.viewKeys(),
	}, "\n")
}

func (m *Model) bodyHeight() int { return max(1, m.height-chromeHeight) }

// tableHeight is how many resource rows fit, after the rule, the headings and
// any lines spent reporting a vCenter or kind that could not be read.
func (m *Model) tableHeight() int {
	chrome, unread := tableChrome, len(m.failuresInScope())
	if m.mode == modeSearch {
		st := m.ensureSearch(m.filter.Value())
		chrome, unread = searchChrome, len(st.missing)+len(st.incomplete)
	}
	return max(1, m.bodyHeight()-chrome-unread)
}

// viewHeader names the scope on the left and the sort order on the right.
// With the sidebar gone this is the only place the current vCenter is
// written, so it says so plainly rather than in a legend.
func (m *Model) viewHeader() string {
	t := m.theme
	if m.mode == modeSearch {
		return m.viewSearchHeader()
	}
	if m.mode == modeHistoryTimeline {
		return m.viewHistoryTimelineHeader()
	}
	if m.mode == modeHistoryTimelineDetail {
		return m.viewHistoryTimelineDetailHeader()
	}
	if m.mode == modeChanges || m.mode == modeChangeDetail || m.mode == modeHistoryRuns || m.mode == modeHistoryRunEdit {
		return m.viewChangesHeader()
	}
	scope := t.accent.Render(m.scopeName())
	if st := m.current(); st != nil && !m.allScope {
		scope = t.statusStyle(st.rowStatus()).Render(glyphScope) + " " + scope
	}
	if m.allScope {
		scope = t.accent.Render(fmt.Sprintf("all %d vCenters", len(m.states)))
	}
	connected, failed := 0, 0
	for _, st := range m.states {
		switch {
		case st.err != nil && !st.credentialsRequired():
			failed++
		case st.inv != nil:
			connected++
		}
	}
	summary := fmt.Sprintf("%d connected", connected)
	if failed > 0 {
		summary += t.bad.Render(fmt.Sprintf(" · %d failed", failed))
	}
	if m.pendingScope() {
		summary += " · " + m.spin.View() + "loading"
	}
	left := t.title.Render("vsfleet")
	// The badge rides on the header rather than a line of its own so that
	// every browse screen — and every screenshot taken of one — says plainly
	// that this estate is invented.
	if m.demo {
		left += "  " + t.warn.Render(demoBadge)
	}
	left += "  " + scope + t.dim.Render("  ·  "+summary)
	// The sort order describes the table, so it is only claimed on the screen
	// that has one.
	right := ""
	if m.mode == modeBrowse {
		right = t.faint.Render("sort: " + m.sortMode.label())
	}
	return joinEnds(left, right, m.width)
}

// joinEnds puts left and right on one line of exactly w columns, dropping the
// right-hand side rather than the left when there is not room for both.
func joinEnds(left, right string, w int) string {
	lw, rw := ansi.StringWidth(left), ansi.StringWidth(right)
	if rw == 0 || lw+rw+2 > w {
		return truncate(left, w)
	}
	return left + strings.Repeat(" ", w-lw-rw) + right
}

func (m *Model) scopeName() string {
	if st := m.current(); st != nil {
		return st.cc.Name
	}
	return "none"
}

func (m *Model) viewMessage() string {
	t := m.theme
	browserPane := m.mode == modeDatastoreEntry || m.mode == modeDatastoreFind
	if !browserPane && (m.filtering || m.filter.Value() != "") {
		return truncate(t.accent.Render(m.filter.View())+t.dim.Render(m.filterHint()), m.width)
	}
	if m.jump != nil && m.jump.kind == m.kind && m.mode == modeBrowse {
		// A jump explains a short table the way a filter explains one: the
		// reader should never be left wondering whether five VMs is really
		// every VM on this host or whether something failed to load.
		return truncate(t.accent.Render("showing: "+m.jump.label)+t.dim.Render("  ·  esc clears"), m.width)
	}
	if m.message == "" {
		return ""
	}
	if m.messageInventory && (m.mode != modeBrowse || m.credPrompt != nil || m.sshPrompt != nil) {
		return ""
	}
	style := t.dim
	if m.messageBad {
		style = t.bad
	}
	return truncate(style.Render(m.message), m.width)
}

// filterHint is what the query line says about its own reach. In the table it
// reports both widths — what matched here and what would match across the
// whole estate — because a filter that found nothing is exactly when it is
// worth knowing the thing exists on another vCenter.
func (m *Model) filterHint() string {
	if m.mode == modeSearch || m.filter.Value() == "" {
		return ""
	}
	if m.mode == modeDatastoreFiles {
		hint := fmt.Sprintf("  %d here", len(m.visibleDSEntries()))
		if !m.filtering {
			hint += " · esc clears"
		}
		return hint
	}
	here := 0
	for _, r := range m.rows() {
		// A switch row is kept for its port groups, not counted as a match.
		if !r.tree.head {
			here++
		}
	}
	hint := fmt.Sprintf("  %d here", here)
	if all := len(m.ensureSearch(m.filter.Value()).rows); all > here {
		hint += fmt.Sprintf(" · %d in the estate — tab to widen", all)
	} else if !m.filtering {
		hint += " · esc clears"
	}
	return hint
}

func (m *Model) viewKeys() string {
	t := m.theme
	parts := make([]string, 0, 10)
	for _, b := range m.keys.footerHints(m) {
		h := b.Help()
		parts = append(parts, t.accent.Render(h.Key)+" "+t.dim.Render(h.Desc))
	}
	return truncate(strings.Join(parts, t.faint.Render("  ")), m.width)
}

// viewBrowse is the main screen: one resource kind, full width. There is no
// sidebar — the estate summary lives in the header and the vCenter list is a
// keystroke away — because at 80 columns the sidebar was costing the table the
// IP address and host columns an operator opened it to read.
func (m *Model) viewBrowse() []string {
	t := m.theme
	w := m.width
	lines := []string{t.rule.Render(strings.Repeat("─", w))}

	cols := m.browseColumns()
	widths := layoutColumns(cols, w-glyphGutter)
	head := make([]string, 0, len(cols))
	for i, c := range cols {
		if widths[i] == 0 {
			continue
		}
		head = append(head, pad(c.title, widths[i], c.right))
	}
	lines = append(lines, t.header.Render(strings.Repeat(" ", glyphGutter)+strings.Join(head, strings.Repeat(" ", cellGap))))

	rows := m.rows()
	h := m.tableHeight()
	if len(rows) == 0 {
		lines = append(lines, t.dim.Render(m.emptyMessage()))
	}
	for i := m.offset; i < len(rows) && i < m.offset+h; i++ {
		lines = append(lines, m.renderRow(rows[i], cols, widths, i == m.cursor))
	}
	// Failures are listed under the table rather than replacing it: the
	// vCenters that did answer are still the answer to the question asked.
	for _, st := range m.failuresInScope() {
		reason, action := "connection failed", "d diagnose"
		if errors.Is(st.err, errPromptCanceled) {
			reason, action = "credential entry canceled", "r retry"
		}
		lines = append(lines, t.bad.Render(truncate(
			fmt.Sprintf("%s %s: %s · %s", glyphFail, st.cc.Name, reason, m.failureHint(action)), w)))
	}
	return scrollLines(lines, 0, m.bodyHeight())
}

// browseColumns are the table's columns: the kind's own, or the grouped
// Networks tab's.
func (m *Model) browseColumns() []column {
	if m.kind == vsphere.KindNetwork && !m.netFlat {
		return networkTreeColumns(m.showContext())
	}
	return columnsFor(m.kind, m.showContext())
}

// failureHint keeps diagnosis reachable even when the selected healthy row
// belongs to another vCenter in the all-contexts table.
func (m *Model) failureHint(action string) string {
	if m.allScope {
		return "c select, " + action
	}
	return action
}

// viewTabs is the numbered kind bar. Numbering it is the point: reaching
// Networks used to be five presses of "l" against a strip that truncated
// before you could see where you were going. Keep the same guarantee for the
// vApp tab at the documented 40-column minimum.
func (m *Model) viewTabs(w int) string {
	t := m.theme
	// Try the widest thing that fits: full names first, and a tighter gap
	// before giving up a name, because "Datastores" earns two columns of
	// whitespace more than "DS" does.
	density, gap := tabCompact, tabGapCompact
	for _, d := range []tabDensity{tabFull, tabShort, tabBare, tabCompact} {
		for _, g := range []int{tabGap, tabGapTight, tabGapCompact} {
			if tabsWidth(m.tabLabels(d), g) <= w {
				density, gap = d, g
				goto found
			}
		}
	}
found:
	labels := m.tabLabels(density)
	parts := make([]string, 0, len(labels))
	for i, label := range labels {
		style := t.tabOff
		if vsphere.AllKinds[i] == m.kind {
			style = t.tabOn
		}
		if strings.HasSuffix(label, "!") {
			// The listing error keeps its own colour: a tab that is merely
			// unselected and a tab you cannot read are different news.
			parts = append(parts, style.Render(label[:len(label)-1])+t.warn.Render("!"))
			continue
		}
		parts = append(parts, style.Render(label))
	}
	return truncate(" "+strings.Join(parts, strings.Repeat(" ", gap)), w)
}

// tabDensity is how much of a kind's label the bar can afford. Something
// always fits, because the number is what you actually press.
type tabDensity int

const (
	tabFull    tabDensity = iota // "1 Datastores 24"
	tabShort                     // "1 DS 24"
	tabBare                      // "1 DS"
	tabCompact                   // "1 D"
)

func (m *Model) tabLabels(d tabDensity) []string {
	labels := make([]string, 0, len(vsphere.AllKinds))
	for i, k := range vsphere.AllKinds {
		title := tabTitle(k)
		if d == tabCompact {
			title = compactTabTitle(k)
		} else if d != tabFull {
			title = shortTabTitle(k)
		}
		label := fmt.Sprintf("%d %s", i+1, title)
		if d == tabFull || d == tabShort {
			label += fmt.Sprintf(" %d", m.count(k))
		}
		if m.kindErrorInScope(k) {
			label += "!"
		}
		labels = append(labels, label)
	}
	return labels
}

func tabsWidth(labels []string, gap int) int {
	w := 1 // the bar is indented one column, like the table's glyph gutter
	for i, l := range labels {
		w += ansi.StringWidth(l)
		if i > 0 {
			w += gap
		}
	}
	return w
}

// viewSearchHeader replaces the scope line while a search is open: a search
// is not scoped to a vCenter, so naming one there would be a lie.
func (m *Model) viewSearchHeader() string {
	t := m.theme
	st := m.ensureSearch(m.filter.Value())
	left := t.title.Render("vsfleet") + "  " + t.accent.Render("search")
	if st.query == "" {
		return truncate(left+t.dim.Render("  ·  every vCenter, every kind"), m.width)
	}
	summary := countWord(len(st.rows), "match", "matches") + " in " + countWord(st.searched, "vCenter", "vCenters")
	if n := len(st.missing); n > 0 {
		summary += t.bad.Render(fmt.Sprintf(" · %d not searched", n))
	}
	if n := len(st.incomplete); n > 0 {
		summary += t.warn.Render(" · " + countWord(n, "kind", "kinds") + " incomplete")
	}
	return truncate(left+t.dim.Render("  ·  ")+t.value.Render(st.query)+t.dim.Render("  ·  "+summary), m.width)
}

// viewSearch is the estate-wide result table: every kind and every vCenter in
// one list. It is the interface catching up with "vsfleet search", which has
// been able to answer this from the command line all along.
//
// A vCenter with no inventory loaded, or a kind that has not finished, is
// named under the results rather than silently left out. Returning fewer
// matches because a proxy or one resource query is down, without saying so,
// is the one way this view could mislead.
func (m *Model) viewSearch() []string {
	t := m.theme
	w := m.width
	st := m.ensureSearch(m.filter.Value())

	cols := searchColumns()
	var lines []string
	widths := layoutColumns(cols, w-glyphGutter)
	head := make([]string, 0, len(cols))
	for i, c := range cols {
		if widths[i] == 0 {
			continue
		}
		head = append(head, pad(c.title, widths[i], c.right))
	}
	lines = append(lines, t.header.Render(strings.Repeat(" ", glyphGutter)+strings.Join(head, strings.Repeat(" ", cellGap))))

	h := m.tableHeight()
	if len(st.rows) == 0 {
		lines = append(lines, t.dim.Render(m.searchEmptyMessage(st)))
	}
	for i := m.offset; i < len(st.rows) && i < m.offset+h; i++ {
		r := st.rows[i]
		r.cells = searchCells(r)
		lines = append(lines, m.renderRow(r, cols, widths, i == m.cursor))
	}
	for _, cs := range st.missing {
		reason := "not connected"
		if cs.showsLoading() {
			reason = "still loading"
		} else if cs.err != nil {
			reason = firstLine(cs.err.Error())
		}
		lines = append(lines, t.bad.Render(truncate(
			fmt.Sprintf("%s %s not searched: %s", glyphFail, cs.cc.Name, reason), w)))
	}
	for _, incomplete := range st.incomplete {
		lines = append(lines, t.warn.Render(truncate(
			fmt.Sprintf("%s %s %s incomplete: %s", glyphPending, incomplete.context.cc.Name, kindWord(incomplete.kind), incomplete.reason), w)))
	}
	return scrollLines(lines, 0, m.bodyHeight())
}

func (m *Model) searchEmptyMessage(st *searchState) string {
	switch {
	case st.query == "":
		return "type a name to search every vCenter and every kind"
	case len(st.incomplete) > 0:
		return fmt.Sprintf("still searching %q", st.query)
	case st.searched == 0:
		return "no vCenter has answered yet"
	default:
		return fmt.Sprintf("nothing named %q in %s", st.query, countWord(st.searched, "vCenter", "vCenters"))
	}
}

// viewContexts is the vCenter list, reached with "c". Everything about a
// context — choosing, adding, editing, removing, diagnosing — happens here,
// which is what lets the browse screen get by on eight keys.
func (m *Model) viewContexts() []string {
	t := m.theme
	scope := "scope: " + m.scopeName()
	if m.allScope {
		scope = fmt.Sprintf("scope: all %d vCenters", len(m.states))
	}
	lines := []string{joinEnds(t.title.Render("Contexts"), t.faint.Render(scope), m.width), ""}
	if len(m.states) == 0 {
		msg := "No vCenters configured yet — n adds the first one."
		if m.demo {
			msg = "No vCenters configured."
		}
		lines = append(lines, "  "+t.dim.Render(msg))
		return scrollLines(lines, 0, m.bodyHeight())
	}

	bodyW := max(minNameWidth, m.width-4)
	epW := clamp((bodyW-ctxNameWidth)/2, 16, 34)
	detW := max(8, bodyW-ctxNameWidth-epW)
	for i, st := range m.states {
		marker := "  "
		if i == m.ctxCursor {
			marker = t.accent.Render(glyphCursor + " ")
		}
		body := pad(st.cc.Name, ctxNameWidth, false) +
			pad(st.cc.Endpoint, epW, false) +
			pad(m.contextDetail(st), detW, false)
		style := t.text
		switch {
		case i == m.ctxCursor:
			style = t.focused
		case !m.allScope && i == m.selected:
			// The context in scope stays marked even while the cursor is
			// elsewhere, so "which one am I looking at" survives browsing.
			style = t.selected
		}
		lines = append(lines, marker+t.statusStyle(st.rowStatus()).Render(st.glyph())+" "+style.Render(body))
	}
	lines = append(lines, "",
		"  "+t.faint.Render("enter narrows the view to one vCenter · a shows them all at once"))
	return scrollLines(lines, 0, m.bodyHeight())
}

// contextDetail is what a context costs to reach, or what went wrong instead.
func (m *Model) contextDetail(st *contextState) string {
	switch {
	case st.showsLoading():
		return contextPhaseDetail(st)
	case st.diagging:
		return "diagnosing…"
	case st.credentialsRequired():
		return "credentials required · press r to connect"
	case st.err != nil:
		return firstLine(st.err.Error())
	case st.inv != nil:
		s := m.status(st.cc.Name)
		route := shortRoute(st.cc.Transport.Describe())
		if s.Latency > 0 {
			return route + " · " + humanize.Duration(s.Latency)
		}
		return route
	default:
		return shortRoute(st.cc.Transport.Describe()) + " · not connected"
	}
}

func contextPhaseDetail(st *contextState) string {
	switch st.phase {
	case phaseCredentials:
		return "credentials required"
	case phaseWaitingCredentials:
		return "waiting for credentials"
	case phaseReadingKeyring:
		return "reading keyring…"
	case phaseAuthenticating:
		return "authenticating…"
	case phaseLoading:
		if st.loadingKind != "" {
			return "loading " + loadingGroupLabel(st.loadingKind) + "…"
		}
		return "loading inventory…"
	default:
		return "authenticating…"
	}
}

func loadingGroupLabel(group vsphere.FetchGroup) string {
	switch group {
	case vsphere.GroupVMs:
		return "VMs and templates"
	case vsphere.GroupHosts:
		return "hosts"
	case vsphere.GroupClusters:
		return "clusters"
	case vsphere.GroupVApps:
		return "vApps"
	case vsphere.GroupDatastores:
		return "datastores"
	case vsphere.GroupNetworks:
		return "networks"
	default:
		return "inventory"
	}
}

func shortRoute(s string) string {
	if i := strings.Index(s, " ->"); i > 0 {
		return s[:i]
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func (m *Model) emptyMessage() string {
	switch {
	case m.pendingScope():
		if st := m.current(); st != nil && st.showsLoading() {
			return contextPhaseDetail(st)
		}
		return m.spin.View() + "loading inventory…"
	case m.current() != nil && m.current().credentialsRequired():
		return "credentials required · press r to connect"
	case m.filter.Value() != "":
		return fmt.Sprintf("no %s matching %q", tabTitle(m.kind), m.filter.Value())
	case len(m.failuresInScope()) > 0:
		return ""
	default:
		return "no " + tabTitle(m.kind)
	}
}

// cursorMark is the first column of every table row: the cursor glyph on the
// selected row, a blank elsewhere. The highlight alone cannot be the cursor.
// Under NO_COLOR, a dumb terminal or a monochrome palette lipgloss strips the
// background and the bold, and the selected row became indistinguishable from
// its neighbours. The mark gets a column of its own, rather than sharing the
// status glyph's, because that glyph (powered on, powered off, failed) is
// information the cursor must not hide.
func (m *Model) cursorMark(selected bool) string {
	if selected {
		return m.theme.accent.Render(glyphCursor)
	}
	return " "
}

// listCursor is the same mark for the plain-text lists whose whole line is
// styled at once, where it has to be unstyled text inside that line.
func listCursor(selected bool) string {
	if selected {
		return glyphCursor
	}
	return " "
}

func (m *Model) renderRow(r row, cols []column, widths []int, selected bool) string {
	t := m.theme
	cells := make([]string, 0, len(cols))
	for i, c := range cols {
		if widths[i] == 0 || i >= len(r.cells) {
			continue
		}
		cells = append(cells, pad(r.cells[i], widths[i], c.right))
	}
	line := strings.Join(cells, strings.Repeat(" ", cellGap))
	if selected {
		line = t.focused.Render(line)
	} else {
		line = t.text.Render(line)
	}
	// The glyph sits in its own gutter, outside the selection highlight, so a
	// powered-off VM still reads as powered off while the cursor is on it.
	return m.cursorMark(selected) + t.statusStyle(r.status).Render(r.glyph) + " " + line
}

// viewDetail is one object, full width, every property the inventory carries.
func (m *Model) viewDetail() []string {
	r, ok := m.currentRow()
	if !ok {
		return []string{m.theme.dim.Render("nothing selected")}
	}
	return m.viewDetailRow(r)
}

func (m *Model) viewDetailRow(r row) []string {
	if r.vm != nil {
		return scrollLines(m.vmDetailLines(r, true), m.detailY, m.bodyHeight())
	}
	t := m.theme
	lines := []string{m.detailHeaderLine(r), ""}
	for i, f := range r.detail {
		lines = append(lines, m.detailFieldLine(2+i, f))
	}
	for _, n := range r.notes {
		lines = append(lines, "", t.label.Render("  "+n.label))
		for _, l := range wrap(n.value, m.width-4) {
			lines = append(lines, "  "+t.value.Render(l))
		}
	}
	if m.actions != nil {
		lines = spliceLines(lines, m.detailCursor, m.actionListLines())
	}
	return scrollLines(lines, m.detailY, m.bodyHeight())
}

// detailHeaderLine renders the object's own name and kind — the field
// cursor's line 0, and the line every object-level action (SSH, open in the
// vSphere Client, jump to related rows) hangs off. Unfocused, it keeps the
// original title/dim styling split; focused, the whole line takes the same
// highlight the browse table gives its own cursor row, the way renderRow
// restyles a whole joined line rather than its individual cells.
func (m *Model) detailHeaderLine(r row) string {
	t := m.theme
	// The kind comes from the row, not from the current tab: a detail pane
	// opened from a search result is showing whatever that result was.
	text := r.name + "   " + kindLabel(r.kind) + " · " + r.context
	if m.detailCursor == 0 {
		return t.focused.Render("▸ " + text)
	}
	return "  " + t.title.Render(r.name) + t.dim.Render("   "+kindLabel(r.kind)+" · "+r.context)
}

// detailFieldLine renders one detail field, keeping the label dimmer than
// its value when the line is not focused — the same distinction the pane
// always drew — and collapsing both into one highlighted line when it is.
func (m *Model) detailFieldLine(idx int, f field) string {
	t := m.theme
	label := pad(f.label, labelColumnPad, false)
	if idx == m.detailCursor {
		return t.focused.Render("▸ " + label + f.value)
	}
	return "  " + t.label.Render(label) + t.value.Render(f.value)
}

// spliceLines inserts insert immediately after position at in base, pushing
// everything below it down — how the action popup opens directly beneath
// the line it belongs to, rather than floating over unrelated content.
func spliceLines(base []string, at int, insert []string) []string {
	if at < 0 || at >= len(base) {
		return append(append([]string{}, base...), insert...)
	}
	out := make([]string, 0, len(base)+len(insert))
	out = append(out, base[:at+1]...)
	out = append(out, insert...)
	out = append(out, base[at+1:]...)
	return out
}

// actionListLines renders the popup opened on the focused line: a bordered
// box listing its actions, its own cursor marking which Enter would run.
// Unavailable actions render dimmed with their reason in place of a value.
func (m *Model) actionListLines() []string {
	al := m.actions
	t := m.theme
	width := 20
	texts := make([]string, len(al.items))
	for i, a := range al.items {
		texts[i] = a.label
		switch {
		case a.disabled != "":
			texts[i] = a.label + "    " + a.disabled
		case a.detail != "":
			texts[i] = a.label + "  " + a.detail
		}
		if w := ansi.StringWidth(texts[i]) + 2; w > width {
			width = w
		}
	}
	if maxWidth := m.width - 8; maxWidth >= 10 && width > maxWidth {
		width = maxWidth
	}
	border := t.rule.Render("┌" + strings.Repeat("─", width) + "┐")
	lines := []string{"  " + border}
	for i, a := range al.items {
		marker := "  "
		if i == al.cursor {
			marker = "▸ "
		}
		body := pad(marker+texts[i], width, false)
		var styled string
		switch {
		case a.disabled != "":
			styled = t.faint.Render(body)
		case i == al.cursor:
			styled = t.focused.Render(body)
		default:
			styled = t.text.Render(body)
		}
		lines = append(lines, "  "+t.rule.Render("│")+styled+t.rule.Render("│"))
	}
	lines = append(lines, "  "+t.rule.Render("└"+strings.Repeat("─", width)+"┘"))
	return lines
}

// viewDoctor renders a connection diagnosis stage by stage, which is the same
// walk "vsfleet doctor" prints.
func (m *Model) viewDoctor() []string {
	t := m.theme
	st := m.doctor
	if st == nil {
		return []string{t.dim.Render("no context selected")}
	}
	lines := []string{t.title.Render("Diagnosing " + st.cc.Name), ""}
	if st.diag == nil {
		return append(lines, "  "+m.spin.View()+t.dim.Render("walking the connection…"))
	}
	d := st.diag
	for _, f := range []field{
		{"Endpoint", d.Endpoint},
		{"Route", d.Route},
		{"TLS", d.TLS},
	} {
		lines = append(lines, "  "+t.label.Render(pad(f.label, 12, false))+t.value.Render(humanize.Dash(f.value)))
	}
	lines = append(lines, "")
	lines = append(lines, m.renderChecks(d.Checks)...)
	lines = append(lines, "")
	if d.OK() {
		lines = append(lines, "  "+t.ok.Render("Connection successful.")+t.dim.Render("  "+humanize.Duration(d.Latency)))
	} else {
		lines = append(lines, "  "+t.bad.Render("Stopped at the first failing stage."))
	}
	if d.Thumbprint != "" {
		lines = append(lines, "", "  "+t.label.Render(pad("Served thumbprint", labelColumnPad, false)))
		lines = append(lines, "  "+t.value.Render(d.Thumbprint))
	}
	return scrollLines(lines, 0, m.bodyHeight())
}

// renderChecks renders a diagnosis's stages, shared by the doctor panel and
// the form's "last test" summary so a connection reads the same way in
// either place.
func (m *Model) renderChecks(checks []vsphere.Check) []string {
	t := m.theme
	lines := make([]string, 0, len(checks))
	for _, c := range checks {
		glyph, style := glyphSkip, t.faint
		switch c.Status {
		case vsphere.CheckPass:
			glyph, style = glyphOK, t.ok
		case vsphere.CheckFail:
			glyph, style = glyphFail, t.bad
		}
		detail := c.Detail
		if c.Err != nil {
			detail = firstLine(c.Err.Error())
		}
		line := fmt.Sprintf("  %s %s  %s", style.Render(glyph), pad(c.Name, 24, false), t.dim.Render(detail))
		lines = append(lines, truncate(line, m.width))
	}
	return lines
}

// viewForm renders the add/edit context form: one row per field, a cursor
// marker on the active one, and the outcome of the last test or discovery
// below it. There is no separate "review" screen — the form is always
// showing exactly what will be saved.
func (m *Model) viewForm() []string {
	t := m.theme
	f := m.form
	if f == nil {
		return []string{t.dim.Render("no form open")}
	}
	title := "Add a vCenter"
	if f.editing {
		title = "Edit " + f.origName
	}
	lines := []string{t.title.Render(title)}
	if len(m.states) == 0 && !f.editing {
		lines = append(lines, t.dim.Render("No contexts are configured yet — this is the first one."))
	}
	lines = append(lines, "")

	const labelWidth = 30
	rows := f.rows()
	for i, r := range rows {
		marker := "  "
		if i == f.cursor {
			marker = t.accent.Render("▸ ")
		}
		switch r.kind {
		case rowButton:
			style := t.dim
			if i == f.cursor {
				style = t.focused
			}
			lines = append(lines, marker+style.Render("[ "+r.static+" ]"))
		case rowStatic:
			lines = append(lines, marker+t.label.Render(pad(r.label, labelWidth, false))+t.value.Render(r.static))
		case rowSelect:
			opts := make([]string, len(r.options))
			for oi, o := range r.options {
				if oi == *r.idx {
					opts[oi] = t.accent.Render("[" + o + "]")
				} else {
					opts[oi] = t.faint.Render(" " + o + " ")
				}
			}
			lines = append(lines, marker+t.label.Render(pad(r.label, labelWidth, false))+strings.Join(opts, " "))
		case rowToggle:
			v, style := "no", t.faint
			if *r.flag {
				v, style = "yes", t.ok
			}
			lines = append(lines, marker+t.label.Render(pad(r.label, labelWidth, false))+style.Render(v))
		default: // rowText, rowSecret
			lines = append(lines, marker+t.label.Render(pad(r.label, labelWidth, false))+r.input.View())
		}
		if r.hint != "" && i == f.cursor {
			lines = append(lines, "    "+t.faint.Render(r.hint))
		}
	}

	lines = append(lines, "")
	switch {
	case f.testing:
		lines = append(lines, "  "+m.spin.View()+t.dim.Render("testing connection…"))
	case f.discovering:
		lines = append(lines, "  "+m.spin.View()+t.dim.Render("fetching certificate…"))
	case f.saving:
		lines = append(lines, "  "+m.spin.View()+t.dim.Render("saving…"))
	}
	if f.err != "" {
		lines = append(lines, "  "+t.bad.Render(f.err))
	} else if f.note != "" {
		lines = append(lines, "  "+t.ok.Render(f.note))
	}
	if f.diag != nil {
		lines = append(lines, "", t.header.Render("Last test"))
		lines = append(lines, m.renderChecks(f.diag.Checks)...)
	}
	return scrollLines(lines, 0, m.bodyHeight())
}

// viewConfirmDelete asks once, plainly, before a context and — by default —
// its stored password are gone.
func (m *Model) viewConfirmDelete() []string {
	t := m.theme
	st := m.confirmDelete
	if st == nil {
		return []string{t.dim.Render("nothing selected")}
	}
	lines := []string{
		t.title.Render("Delete " + st.cc.Name + "?"),
		"",
		"  " + t.label.Render(pad("Endpoint", 12, false)) + t.value.Render(st.cc.Endpoint),
		"  " + t.label.Render(pad("Route", 12, false)) + t.value.Render(st.cc.Transport.Describe()),
		"",
	}
	credLine := "  " + t.label.Render(pad("Stored password", 12, false))
	if m.confirmAlsoCredential {
		credLine += t.bad.Render("deleted with it")
	} else {
		credLine += t.ok.Render("kept in the keyring")
	}
	lines = append(lines, credLine, "  "+t.faint.Render("(c to toggle)"), "")
	lines = append(lines, "  "+t.bad.Render("y")+t.dim.Render(" delete    ")+t.accent.Render("n")+t.dim.Render(" cancel"))
	return scrollLines(lines, 0, m.bodyHeight())
}

// viewCredPrompt renders the credential overlay: a background load is
// waiting on a password, and until this answers, every keystroke belongs to
// it rather than to whatever screen sits underneath.
func (m *Model) viewCredPrompt() []string {
	t := m.theme
	cp := m.credPrompt
	title := "Password required"
	if cp.label != "" {
		title = "Password for " + cp.label
	}
	// Shrink the password field to fit a narrow terminal rather than pushing
	// the overlay past the frame at the minimum supported width.
	cp.input.Width = clamp(m.width-2, 10, 40)
	lines := []string{
		t.title.Render(title),
		"",
		"  " + cp.input.View(),
		"",
	}
	// The overlay owns every keystroke while it is up, so its guidance must
	// stay readable at the minimum supported width. Wrap the prose at word
	// boundaries rather than letting the terminal clip it mid-sentence.
	for _, l := range wrap("A background load is waiting on this credential; nothing else responds until it is answered.", m.width-2) {
		lines = append(lines, "  "+t.dim.Render(l))
	}
	lines = append(lines, "")
	lines = append(lines, m.credPromptKeys()...)
	return scrollLines(lines, 0, m.bodyHeight())
}

// credPromptKeys renders the overlay's key hints inline when they fit and
// stacked one action per line when they do not, so no action is silently
// clipped at 80 columns.
func (m *Model) credPromptKeys() []string {
	return m.overlayKeys([][2]string{
		{"enter", "continue"},
		{"esc", "cancel this load"},
		{"ctrl+c", "quit"},
	})
}

// overlayKeys lays hints out inline when they fit and stacked otherwise; it is
// shared by every overlay that owns the keyboard.
func (m *Model) overlayKeys(hints [][2]string) []string {
	t := m.theme
	single := "  "
	for i, h := range hints {
		if i > 0 {
			single += t.dim.Render("    ")
		}
		single += t.accent.Render(h[0]) + t.dim.Render(" "+h[1])
	}
	if ansi.StringWidth(single) <= m.width {
		return []string{single}
	}
	out := make([]string, 0, len(hints))
	for _, h := range hints {
		out = append(out, "  "+t.accent.Render(h[0])+t.dim.Render(" "+h[1]))
	}
	return out
}

// helpLines is the whole key reference. It is laid out in two columns when
// there is width for them, because a reference you have to scroll to read is
// a reference you look up somewhere else instead.
func (m *Model) helpLines() []string {
	t := m.theme
	sections, title := m.keys.helpSections(m.demo), "Keys"
	if m.helpFrom == modeChanges {
		// Opened from the history hub: show that pane's keys in place of the
		// browse sections, which describe a screen that is not visible.
		sections, title = m.keys.historyHelpSections(m.historyPane, m.canCapture()), "Keys · History"
	}
	blocks := make([][]string, 0, len(sections))
	for _, sec := range sections {
		block := []string{t.header.Render(sec.title)}
		for _, b := range sec.bindings {
			h := b.Help()
			block = append(block, "  "+t.accent.Render(pad(h.Key, 10, false))+t.dim.Render(h.Desc))
		}
		blocks = append(blocks, block)
	}

	var body []string
	if m.width >= helpColumnWidth*2+4 {
		body = pairColumns(blocks, helpColumnWidth)
	} else {
		for _, block := range blocks {
			body = append(body, block...)
			body = append(body, "")
		}
	}

	lines := []string{t.title.Render(title), ""}
	for _, l := range body {
		lines = append(lines, "  "+l)
	}
	return lines
}

func (m *Model) viewHelp() []string {
	return scrollLines(m.helpLines(), m.detailY, m.bodyHeight())
}

// pairColumns splits blocks across two columns, breaking at the block that
// crosses the halfway mark so no section is ever cut in two.
func pairColumns(blocks [][]string, w int) []string {
	total := 0
	for _, b := range blocks {
		total += len(b) + 1
	}
	var left, right []string
	run := 0
	for _, b := range blocks {
		dst := &left
		if run*2 >= total {
			dst = &right
		}
		*dst = append(*dst, b...)
		*dst = append(*dst, "")
		run += len(b) + 1
	}
	out := make([]string, 0, max(len(left), len(right)))
	for i := 0; i < max(len(left), len(right)); i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, pad(l, w, false)+r)
	}
	return out
}

// layoutColumns assigns a width to every column, giving the flexible one what
// is left and dropping columns from the right when even that is not enough.
// A width of zero in the result means the column is not drawn.
func layoutColumns(cols []column, total int) []int {
	widths := make([]int, len(cols))
	drawn := len(cols)
	for {
		fixed, flexIdx := 0, -1
		for i := 0; i < drawn; i++ {
			if cols[i].width == 0 {
				flexIdx = i
				continue
			}
			fixed += cols[i].width
		}
		spare := total - fixed - cellGap*(drawn-1)
		if flexIdx == -1 {
			// No flexible column: shrink by dropping from the right.
			if spare >= 0 || drawn <= 1 {
				for i := 0; i < drawn; i++ {
					widths[i] = cols[i].width
				}
				return widths
			}
			drawn--
			continue
		}
		if spare >= minNameWidth || drawn <= flexIdx+1 {
			for i := 0; i < drawn; i++ {
				widths[i] = cols[i].width
			}
			widths[flexIdx] = max(minNameWidth, spare)
			return widths
		}
		drawn--
	}
}

// pad fits a string to an exact display width, truncating with an ellipsis so
// that a long VM name never pushes the columns to its right out of alignment.
func pad(s string, w int, right bool) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	gap := w - ansi.StringWidth(s)
	if gap <= 0 {
		return s
	}
	if right {
		return strings.Repeat(" ", gap) + s
	}
	return s + strings.Repeat(" ", gap)
}

// truncate cuts a rendered line to the terminal width without padding it.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// scrollLines windows a block of lines and pads it to the available height.
func scrollLines(lines []string, offset, h int) []string {
	if offset > len(lines)-1 {
		offset = max(0, len(lines)-1)
	}
	if offset < 0 {
		offset = 0
	}
	end := min(len(lines), offset+h)
	out := append([]string{}, lines[offset:end]...)
	for len(out) < h {
		out = append(out, "")
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// wrap breaks a paragraph at word boundaries, for annotations that were
// written as prose by whoever created the VM.
func wrap(s string, w int) []string {
	if w <= 0 {
		return []string{s}
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case runewidth.StringWidth(line)+1+runewidth.StringWidth(word) <= w:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}
