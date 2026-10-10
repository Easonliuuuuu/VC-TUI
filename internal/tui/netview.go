package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/humanize"
)

// switchWorkspace is one open switch. Like the vApp workspace it keeps the
// browse cursor where it was, on the switch's row, so Esc lands back there.
type switchWorkspace struct {
	key     string
	context string
	page    int
	// cursor is the port group selected on the Wiring page; offset is the
	// first one drawn. scroll is the Overview page's scroll position.
	cursor int
	offset int
	scroll int
	// from is the screen Esc returns to: the browse table, or the host pane
	// the switch was opened from.
	from mode
}

// switchPages are the workspace's pages, chosen with 0 and 1 the way the VM
// dashboard's chart pages are.
var switchPages = []string{"Overview", "Wiring"}

func (m *Model) openSwitch(r row) tea.Cmd {
	m.sw = &switchWorkspace{key: r.tree.switchKey, context: r.context, from: modeBrowse}
	m.swPG = nil
	m.mode = modeSwitchDetail
	return m.ensureNetTopology(false)
}

// clearSwitchWorkspace drops the open switch and port group. Cross-resource
// jumps leave the workspace entirely, so a later Esc must not reopen it.
func (m *Model) clearSwitchWorkspace() { m.sw, m.swPG = nil, nil }

// leaveSwitchWorkspace closes the workspace and returns to where it was
// opened from.
func (m *Model) leaveSwitchWorkspace() {
	back := modeBrowse
	if m.sw != nil && m.sw.from != modeBrowse {
		back = m.sw.from
	}
	m.clearSwitchWorkspace()
	m.mode = back
}

func (m *Model) activeSwitch() (*netSwitch, *contextState) {
	if m.sw == nil {
		return nil, nil
	}
	return m.findSwitch(m.sw.key)
}

func (m *Model) handleSwitchDetailKey(msg tea.KeyMsg) tea.Cmd {
	sw, _ := m.activeSwitch()
	if sw == nil || m.sw == nil {
		if key.Matches(msg, m.keys.Back) {
			m.leaveSwitchWorkspace()
		}
		return nil
	}
	ws := m.sw
	last := max(0, len(sw.portGroups)-1)
	page := max(1, m.bodyHeight()-8)
	wiring := ws.page == 1
	switch {
	case key.Matches(msg, m.keys.Back):
		m.leaveSwitchWorkspace()
	case key.Matches(msg, m.keys.SwitchPage):
		if i, err := strconv.Atoi(msg.String()); err == nil && i >= 0 && i < len(switchPages) {
			ws.page = i
		}
	case key.Matches(msg, m.keys.Up):
		if wiring {
			ws.cursor = clamp(ws.cursor-1, 0, last)
		} else {
			ws.scroll = max(0, ws.scroll-1)
		}
	case key.Matches(msg, m.keys.Down):
		if wiring {
			ws.cursor = clamp(ws.cursor+1, 0, last)
		} else {
			ws.scroll++
		}
	case key.Matches(msg, m.keys.PageUp):
		if wiring {
			ws.cursor = clamp(ws.cursor-page, 0, last)
		} else {
			ws.scroll = max(0, ws.scroll-page)
		}
	case key.Matches(msg, m.keys.PageDown):
		if wiring {
			ws.cursor = clamp(ws.cursor+page, 0, last)
		} else {
			ws.scroll += page
		}
	case key.Matches(msg, m.keys.Home):
		ws.cursor, ws.scroll = 0, 0
	case key.Matches(msg, m.keys.End):
		ws.cursor = last
		ws.scroll = 1 << 30
	case key.Matches(msg, m.keys.Open):
		if !wiring || len(sw.portGroups) == 0 {
			return nil
		}
		r := portGroupRow(sw, &sw.portGroups[clamp(ws.cursor, 0, last)], false)
		m.swPG = &r
		m.detailCursor, m.detailY = 0, 0
		m.mode = modeSwitchPGDetail
	case key.Matches(msg, m.keys.Reload):
		return m.ensureNetTopology(true)
	}
	return nil
}

func (m *Model) handleSwitchPGDetailKey(msg tea.KeyMsg) tea.Cmd {
	if m.swPG == nil {
		m.mode = modeSwitchDetail
		return nil
	}
	if m.actions != nil {
		return m.handleActionsKey(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Back):
		m.swPG = nil
		m.mode = modeSwitchDetail
	case key.Matches(msg, m.keys.Open):
		return m.openFieldActions()
	case key.Matches(msg, m.keys.Up):
		m.moveDetailCursor(-1)
	case key.Matches(msg, m.keys.Down):
		m.moveDetailCursor(1)
	case key.Matches(msg, m.keys.PageUp):
		m.scrollDetailPage(-1)
	case key.Matches(msg, m.keys.PageDown):
		m.scrollDetailPage(1)
	}
	return nil
}

func (m *Model) viewSwitchPGDetail() []string {
	if m.swPG == nil {
		return []string{m.theme.dim.Render("nothing selected")}
	}
	return m.viewDetailRow(*m.swPG)
}

// ---- the workspace -------------------------------------------------------

func (m *Model) viewSwitchDetail() []string {
	t := m.theme
	sw, st := m.activeSwitch()
	if sw == nil || m.sw == nil {
		return []string{t.dim.Render("this switch is no longer in the inventory · esc goes back")}
	}
	w := m.width
	title := t.title.Render(sw.name) + t.dim.Render("   "+sw.kindLabel()+" · "+sw.context)
	lines := []string{joinEnds(title, m.switchVerdict(sw), w)}
	lines = append(lines, joinEnds(m.switchPageTabs(m.sw.page), t.dim.Render(m.topoSource(st)), w))
	lines = append(lines, m.topoProblems(st, w)...)
	lines = append(lines, "")
	head := len(lines)
	if m.sw.page == 1 {
		return scrollLines(append(lines, m.wiringLines(sw, w, m.bodyHeight()-head)...), 0, m.bodyHeight())
	}
	body := m.overviewLines(sw, w)
	limit := max(0, len(body)-(m.bodyHeight()-head))
	m.sw.scroll = clamp(m.sw.scroll, 0, limit)
	return scrollLines(append(lines, body[m.sw.scroll:]...), 0, m.bodyHeight())
}

// switchVerdict is the right end of the title: the findings count, or that
// there is nothing to report once the wiring has been read.
func (m *Model) switchVerdict(sw *netSwitch) string {
	t := m.theme
	if !sw.wired {
		return ""
	}
	notes := switchNotes(sw, topoLoaded)
	if notes == "" {
		return t.ok.Render(glyphCheckOK + " no findings")
	}
	return t.statusStyle(sw.status()).Render(notes)
}

func (m *Model) switchPageTabs(page int) string { return m.pageTabs(switchPages, page) }

// pageTabs draws a workspace's page bar, the current page bracketed.
func (m *Model) pageTabs(pages []string, page int) string {
	t := m.theme
	var b strings.Builder
	for i, name := range pages {
		label := fmt.Sprintf("%d %s", i, name)
		if i == page {
			b.WriteString(t.accent.Render("[" + label + "]"))
		} else {
			b.WriteString(t.dim.Render(" " + label + " "))
		}
	}
	return b.String()
}

// topoSource says where the host-level evidence on screen came from.
func (m *Model) topoSource(st *contextState) string {
	switch m.topoStateOf(st) {
	case topoUnsupported:
		return "inventory only"
	case topoLoading:
		return m.spin.View() + "reading wiring…"
	case topoFailed:
		return "wiring not read"
	}
	e := st.netTopo
	s := "wiring as of " + e.asOf.Local().Format("15:04:05")
	switch {
	case e.loading:
		s += " · refreshing"
	case e.err != nil:
		s += " · refresh failed"
	}
	return s
}

// topoProblems reports a failed read, and the parts of a read that failed,
// so missing wiring is never mistaken for a switch with nothing on it.
func (m *Model) topoProblems(st *contextState, w int) []string {
	t := m.theme
	e := st.netTopo
	if e == nil {
		return nil
	}
	var msgs []string
	if e.err != nil {
		if e.topo == nil {
			msgs = append(msgs, "Wiring could not be read: "+firstLine(e.err.Error()))
		} else {
			msgs = append(msgs, "Refresh failed, showing the read from "+e.asOf.Local().Format("15:04:05")+": "+firstLine(e.err.Error()))
		}
	}
	if e.topo != nil {
		for _, ie := range e.topo.Errors {
			msgs = append(msgs, fmt.Sprintf("%s not read: %s", ie.Kind, firstLine(ie.Message)))
		}
	}
	var out []string
	for _, msg := range msgs {
		for _, l := range wrap(msg, w-2) {
			out = append(out, t.warn.Render(l))
		}
	}
	return out
}

func (m *Model) overviewLines(sw *netSwitch, w int) []string {
	t := m.theme
	var lines []string
	lines = append(lines, t.header.Render("Properties"))
	for _, f := range switchFields(sw) {
		if f.label == "vCenter" || f.label == "Kind" {
			continue
		}
		lines = append(lines, truncate("  "+t.label.Render(pad(f.label, labelColumnPad, false))+t.value.Render(f.value), w))
	}
	if sw.wired && (len(sw.hosts) > 0 || len(sw.notJoined) > 0) {
		lines = append(lines, "", t.header.Render("Uplinks by host"))
		lines = append(lines, m.uplinkGrid(sw, w)...)
	}
	lines = append(lines, "", t.header.Render("Findings"))
	switch {
	case len(sw.findings) > 0:
		for _, f := range sw.findings {
			glyph := glyphCheckWarn
			if f.status == statusBad {
				glyph = glyphFail
			}
			for i, l := range wrap(f.text, w-6) {
				prefix := "    "
				if i == 0 {
					prefix = "  " + glyph + " "
				}
				lines = append(lines, t.statusStyle(f.status).Render(prefix+l))
			}
		}
	case sw.wired:
		lines = append(lines, t.ok.Render("  "+glyphCheckOK+" nothing to report"))
	default:
		lines = append(lines, t.dim.Render("  host-level checks need the wiring, which has not been read"))
	}
	lines = append(lines, "", t.header.Render("Port groups"))
	lines = append(lines, m.portGroupTable(sw, w)...)
	return lines
}

// uplinkGrid is a host × uplink table. Hosts whose rows are identical are
// drawn as one line ("esxi-01 … esxi-12 ×12"), so a large cluster still fits
// and only the host that differs stands out.
func (m *Model) uplinkGrid(sw *netSwitch, w int) []string {
	t := m.theme
	const cellW = 15
	type gridRow struct {
		hosts []string
		cells []*netNIC
		has   []bool
		note  string
	}
	var rows []gridRow
	index := map[string]int{}
	for _, h := range sw.hosts {
		r := gridRow{hosts: []string{h.name}}
		var sig strings.Builder
		for _, u := range sw.uplinks {
			nic, ok := h.nics[u]
			r.cells, r.has = append(r.cells, nic), append(r.has, ok)
			if ok {
				sig.WriteString(nic.label())
			} else {
				sig.WriteString("?")
			}
			sig.WriteByte('|')
		}
		if !h.connected {
			r.note = "not connected"
		}
		s := sig.String() + r.note
		if i, ok := index[s]; ok {
			rows[i].hosts = append(rows[i].hosts, h.name)
			continue
		}
		index[s] = len(rows)
		rows = append(rows, r)
	}
	labelW := 10
	labels := make([]string, len(rows))
	for i, r := range rows {
		labels[i] = hostRange(r.hosts)
		labelW = max(labelW, ansi.StringWidth(labels[i]))
	}
	for _, h := range sw.notJoined {
		labelW = max(labelW, ansi.StringWidth(h.name))
	}
	labelW = min(labelW, 34)
	fit := max(1, (w-2-labelW-2)/(cellW+1))
	shown := sw.uplinks
	more := 0
	if len(shown) > fit {
		more = len(shown) - (fit - 1)
		shown = shown[:fit-1]
	}
	head := "  " + pad("", labelW, false) + "  "
	for _, u := range shown {
		head += pad(u, cellW, false) + " "
	}
	if more > 0 {
		head += fmt.Sprintf("+%d more", more)
	}
	out := []string{t.dim.Render(truncate(head, w))}
	// The speed most hosts run each uplink at, so the odd one out is marked.
	common := make([]int32, len(sw.uplinks))
	for j, u := range sw.uplinks {
		speeds := map[int32]int{}
		for _, h := range sw.hosts {
			if nic := h.nics[u]; nic != nil && nic.up {
				speeds[nic.speedMB]++
			}
		}
		if len(speeds) > 1 {
			common[j], _ = strictMajority(speeds)
		}
	}
	for i, r := range rows {
		line := "  " + t.value.Render(pad(labels[i], labelW, false)) + "  "
		for j := range shown {
			nic, has := r.cells[j], r.has[j]
			var cell string
			var style lipgloss.Style
			switch {
			case !has:
				cell, style = "", t.dim
			case nic == nil:
				cell, style = "–", t.dim
			case !nic.up:
				cell, style = nic.device+" down "+glyphFail, t.bad
			case common[j] != 0 && nic.speedMB != common[j]:
				cell, style = nic.label()+" "+glyphCheckWarn, t.warn
			default:
				cell, style = nic.label(), t.text
			}
			line += style.Render(pad(cell, cellW, false)) + " "
		}
		if r.note != "" {
			line += t.dim.Render(r.note)
		}
		out = append(out, truncate(line, w))
	}
	for _, h := range sw.notJoined {
		out = append(out, truncate("  "+t.value.Render(pad(h.name, labelW, false))+"  "+t.warn.Render(glyphCheckWarn+" not on this switch ("+h.cluster+")"), w))
	}
	return out
}

// hostRange names a set of hosts in one cell: the host itself, or the first
// and last of an alike group with its size.
func hostRange(hosts []string) string {
	sorted := append([]string(nil), hosts...)
	sort.Strings(sorted)
	switch len(sorted) {
	case 1:
		return sorted[0]
	case 2:
		return sorted[0] + ", " + sorted[1]
	}
	return fmt.Sprintf("%s … %s ×%d", sorted[0], sorted[len(sorted)-1], len(sorted))
}

func (m *Model) portGroupTable(sw *netSwitch, w int) []string {
	t := m.theme
	if len(sw.portGroups) == 0 {
		return []string{t.dim.Render("  no port groups")}
	}
	cols := []column{
		{title: "NAME"},
		{title: "VLAN", width: 12},
		{title: "VMS", width: 5, right: true},
		{title: "VMK", width: 4, right: true},
		{title: "ACTIVE", width: 8},
		{title: "STANDBY", width: 8},
		{title: "NOTES", width: 22},
	}
	widths := layoutColumns(cols, w-glyphGutter)
	head := make([]string, 0, len(cols))
	for i, c := range cols {
		if widths[i] > 0 {
			head = append(head, pad(c.title, widths[i], c.right))
		}
	}
	out := []string{t.header.Render(strings.Repeat(" ", glyphGutter) + strings.Join(head, strings.Repeat(" ", cellGap)))}
	for i := range sw.portGroups {
		pg := &sw.portGroups[i]
		r := portGroupRow(sw, pg, false)
		vlan, vms, vmk := r.cells[1], r.cells[2], r.cells[4]
		cells := []string{pg.name, vlan, vms, vmk, shortUplinks(sw, pg.active), shortUplinks(sw, pg.standby), r.cells[3]}
		if sw.kind != netDistributed {
			cells[4], cells[5] = "", ""
		}
		r.cells = cells
		out = append(out, m.renderRow(r, cols, widths, false))
	}
	return out
}

// ---- the wiring diagram ----------------------------------------------------

// wireLeft is one line on the physical side of the diagram: one kind of NIC
// found behind an uplink, and on how many hosts.
type wireLeft struct {
	text   string
	status rowStatus
	uplink string
	// first is the uplink's first line, which carries its label and its
	// connection to the switch; join is how this line's link meets the
	// uplink's other NIC lines.
	first bool
	join  string
	// linked is false for "no NIC" lines, which draw no link.
	linked bool
}

// wiringLines draws physical NICs → uplinks → switch → port groups, with the
// selected port group's teaming drawn into the links: heavy for its active
// uplinks, dotted for its standby ones. Physical NICs are counted across
// hosts rather than drawn per host, so the drawing is as tall for sixty hosts
// as for two, and the exception has a line of its own.
func (m *Model) wiringLines(sw *netSwitch, w, h int) []string {
	t := m.theme
	ws := m.sw
	if len(sw.portGroups) == 0 {
		return []string{t.dim.Render("  no port groups on this switch")}
	}
	ws.cursor = clamp(ws.cursor, 0, len(sw.portGroups)-1)
	sel := &sw.portGroups[ws.cursor]
	role := map[string]string{}
	for _, u := range sel.active {
		role[u] = "active"
	}
	for _, u := range sel.standby {
		if role[u] == "" {
			role[u] = "standby"
		}
	}

	left := wiringLeft(sw)
	uplinkCarries := map[string]bool{}
	for _, l := range left {
		if l.linked && l.status != statusBad {
			uplinkCarries[l.uplink] = true
		}
	}
	detail := m.wiringDetail(sw, sel, w)
	diagramH := max(3, h-2-len(detail)-2)
	rows := diagramH
	if len(sw.portGroups) < rows {
		rows = len(sw.portGroups)
	}
	if ws.cursor < ws.offset {
		ws.offset = ws.cursor
	}
	if ws.cursor >= ws.offset+rows {
		ws.offset = ws.cursor - rows + 1
	}
	ws.offset = clamp(ws.offset, 0, max(0, len(sw.portGroups)-rows))
	right := sw.portGroups[ws.offset : ws.offset+rows]
	spine := ws.cursor - ws.offset
	if len(left) > diagramH {
		left = left[:diagramH]
	}
	n := max(max(len(left), len(right)), 1)

	// Column widths. The physical column goes first when the terminal is too
	// narrow for everything.
	textW, upW := 0, 0
	for _, l := range left {
		textW = max(textW, ansi.StringWidth(l.text))
		upW = max(upW, ansi.StringWidth(l.uplink))
	}
	textW, upW = min(textW, 22), min(upW, 14)
	const linkW, vlanW, countW = 7, 9, 8
	centerW := min(max(ansi.StringWidth(sw.name)+6, 12), 26)
	nameW := 18
	for _, pg := range right {
		nameW = max(nameW, min(ansi.StringWidth(pg.name)+2, 28))
	}
	leftW := func() int {
		if upW == 0 {
			return 0
		}
		lw := 1 + upW + 3 // " uplink ──"
		if textW > 0 {
			lw += textW + linkW
		}
		return lw
	}
	total := func() int { return 1 + leftW() + 1 + centerW + 1 + 3 + nameW + vlanW + countW }
	// Give way in order of what the title already says: the switch's name
	// on the spine first, then port group name width, then the physical
	// column, which the Overview's uplink grid also shows.
	showName := true
	if total() > w {
		centerW, showName = 6, false
	}
	if total() > w {
		nameW = max(16, nameW-(total()-w))
	}
	if total() > w {
		textW = 0
	}
	if total() > w {
		nameW = max(10, nameW-(total()-w))
	}

	busTop, busBot := spine, spine
	for i, l := range left {
		if l.first && i < n {
			busTop, busBot = min(busTop, i), max(busBot, i)
		}
	}
	rTop, rBot := 0, max(len(right)-1, spine)

	heavy := func(s string, r string) string {
		switch r {
		case "active":
			return strings.ReplaceAll(s, "─", "━")
		case "standby":
			return strings.ReplaceAll(s, "─", "┄")
		}
		return s
	}
	styleFor := func(r string) lipgloss.Style {
		switch r {
		case "active":
			return t.accent.Bold(true)
		case "standby":
			return t.accent
		}
		return t.faint
	}

	var head strings.Builder
	head.WriteString(" ")
	if upW > 0 {
		if textW > 0 {
			head.WriteString(pad("PHYSICAL", textW+linkW, false))
		}
		head.WriteString(pad(" UPLINK", 1+upW+3, false))
		head.WriteString(" ")
	}
	head.WriteString(pad("SWITCH", centerW+1+3, false))
	head.WriteString(pad("PORT GROUP", nameW, false) + pad("VLAN", vlanW, true) + pad("VMS", countW, true))
	out := []string{t.header.Render(truncate(head.String(), w))}

	for i := 0; i < n; i++ {
		var b strings.Builder
		b.WriteString(" ")
		if upW > 0 {
			var l wireLeft
			hasLeft := i < len(left)
			if hasLeft {
				l = left[i]
			}
			r := role[l.uplink]
			if textW > 0 {
				textStyle := t.text
				switch l.status {
				case statusBad:
					textStyle = t.bad
				case statusWarn:
					textStyle = t.warn
				}
				if r != "" && l.status == statusGood {
					textStyle = styleFor(r)
				}
				b.WriteString(textStyle.Render(pad(l.text, textW, false)))
				link := strings.Repeat(" ", linkW)
				linkRole := r
				if l.status == statusBad {
					// A NIC with no link carries nothing, whatever the teaming.
					linkRole = ""
				}
				if hasLeft && l.linked {
					link = " " + heavy(l.join, linkRole)
				}
				b.WriteString(styleFor(linkRole).Render(link))
			}
			label, stub := "", "   "
			stubRole := r
			if !uplinkCarries[l.uplink] {
				// Nothing is behind it, so there is no path to draw.
				stubRole = ""
			}
			if hasLeft && l.first {
				label, stub = l.uplink, heavy(" ──", stubRole)
			}
			b.WriteString(" " + styleFor(stubRole).Render(pad(label, upW, false)) + styleFor(stubRole).Render(stub))
			up, down := i > busTop && i <= busBot, i >= busTop && i < busBot
			b.WriteString(t.faint.Render(junction(up, down, hasLeft && l.first, i == spine)))
		}
		// The switch sits on the selected port group's line, so the path from
		// its uplinks to it reads straight across.
		if i == spine {
			label := "━ " + sw.name + " "
			if !showName {
				label = ""
			}
			b.WriteString(t.accent.Bold(true).Render(pad(label+strings.Repeat("━", max(0, centerW-ansi.StringWidth(label))), centerW, false)))
		} else {
			b.WriteString(strings.Repeat(" ", centerW))
		}
		up, down := i > rTop && i <= rBot, i >= rTop && i < rBot
		b.WriteString(t.faint.Render(junction(up, down, i == spine, i < len(right))))
		if i < len(right) {
			pg := &right[i]
			stub, nameStyle := t.faint.Render("── "), t.text
			if i == spine {
				stub, nameStyle = t.accent.Bold(true).Render("━━ "), t.focused
			}
			name := pg.name
			if len(pg.notes) > 0 && pg.status != statusIdle && pg.status != statusGood {
				mark := glyphCheckWarn
				if pg.status == statusBad {
					mark = glyphFail
				}
				name += " " + mark
			}
			count := "-"
			switch {
			case pg.vms > 0 || (pg.vms == 0 && len(pg.vmks) == 0):
				count = strconv.Itoa(pg.vms)
			case len(pg.vmks) > 0:
				count = "vmk " + strconv.Itoa(len(pg.vmks))
			}
			b.WriteString(stub + nameStyle.Render(pad(name, nameW, false)) + t.text.Render(pad(pg.vlan, vlanW, true)+pad(count, countW, true)))
		}
		out = append(out, truncate(b.String(), w))
	}
	if more := len(sw.portGroups) - len(right); more > 0 {
		out = append(out, t.dim.Render(fmt.Sprintf("  %d of %d port groups shown · j/k scrolls", len(right), len(sw.portGroups))))
	}
	out = append(out, "")
	if sw.kind == netDistributed {
		out = append(out, t.dim.Render(truncate("  ━ active uplink   ┄ standby   ─ not used by the selected port group   "+glyphCheckWarn+" differs   "+glyphFail+" no link", w)))
	}
	return append(out, detail...)
}

// wiringLeft builds the physical side: for each uplink, each kind of NIC
// behind it (device, link and speed) with how many hosts have it, most
// common first, then how many hosts have nothing assigned.
func wiringLeft(sw *netSwitch) []wireLeft {
	var out []wireLeft
	for _, u := range sw.uplinks {
		type variant struct {
			nic   netNIC
			count int
		}
		var vs []variant
		missing := 0
		for _, h := range sw.hosts {
			nic, ok := h.nics[u]
			if !ok {
				continue
			}
			if nic == nil {
				missing++
				continue
			}
			found := false
			for i := range vs {
				if vs[i].nic == *nic {
					vs[i].count++
					found = true
					break
				}
			}
			if !found {
				vs = append(vs, variant{nic: *nic, count: 1})
			}
		}
		sort.SliceStable(vs, func(i, j int) bool { return vs[i].count > vs[j].count })
		var lines []wireLeft
		for i, v := range vs {
			text := v.nic.label()
			if sw.kind == netStandard {
				// A standard switch's uplink is the NIC itself.
				text = strings.TrimPrefix(text, v.nic.device+" ")
			}
			l := wireLeft{text: fmt.Sprintf("%s ×%d", text, v.count), status: statusGood, uplink: u, linked: true, join: "──────"}
			switch {
			case !v.nic.up:
				l.status, l.text = statusBad, l.text+" "+glyphFail
			case i > 0:
				l.status, l.text = statusWarn, l.text+" "+glyphCheckWarn
			}
			if len(vs) > 1 {
				switch i {
				case 0:
					l.join = "──┬───"
				case len(vs) - 1:
					l.join = "──┘   "
				default:
					l.join = "──┤   "
				}
			}
			lines = append(lines, l)
		}
		if missing > 0 {
			st := statusWarn
			if len(vs) == 0 {
				st = statusIdle
			}
			lines = append(lines, wireLeft{text: fmt.Sprintf("no NIC ×%d", missing), status: st, uplink: u})
		}
		if len(lines) == 0 {
			lines = append(lines, wireLeft{text: "", status: statusIdle, uplink: u})
		}
		lines[0].first = true
		out = append(out, lines...)
	}
	return out
}

// junction is the box-drawing character joining a bus to its branches.
func junction(up, down, left, right bool) string {
	switch {
	case up && down && left && right:
		return "┼"
	case up && down && left:
		return "┤"
	case up && down && right:
		return "├"
	case up && down:
		return "│"
	case down && left && right:
		return "┬"
	case up && left && right:
		return "┴"
	case down && left:
		return "┐"
	case down && right:
		return "┌"
	case up && left:
		return "┘"
	case up && right:
		return "└"
	case left || right:
		return "─"
	case up || down:
		return "│"
	}
	return " "
}

// wiringDetail is the selected port group's line-up under the diagram.
func (m *Model) wiringDetail(sw *netSwitch, pg *netPortGroup, w int) []string {
	t := m.theme
	var facts []string
	facts = append(facts, "VLAN "+pg.vlan)
	if pg.vms >= 0 {
		facts = append(facts, nounCount(pg.vms, "VM"))
	}
	if sw.wired {
		facts = append(facts, nounCount(len(pg.vmks), "VMkernel adapter"))
	}
	lines := []string{"", truncate(t.accent.Render(glyphCursor+" ")+t.title.Render(pg.name)+t.dim.Render("   "+strings.Join(facts, " · ")), w)}
	if pg.dv != nil {
		lines = append(lines, truncate("  "+t.label.Render("teaming ")+t.value.Render(humanize.Dash(pg.teaming))+t.label.Render(" · active ")+t.value.Render(listOrDash(pg.active))+t.label.Render(" · standby ")+t.value.Render(listOrDash(pg.standby)), w))
	}
	if sec := securityText(pg); sec != "" {
		lines = append(lines, truncate("  "+t.label.Render("security ")+t.value.Render(sec), w))
	}
	for _, f := range sw.findings {
		if !strings.Contains(f.text, pg.name+" ") {
			continue
		}
		glyph := glyphCheckWarn
		if f.status == statusBad {
			glyph = glyphFail
		}
		lines = append(lines, truncate(t.statusStyle(f.status).Render("  "+glyph+" "+f.text), w))
	}
	lines = append(lines, truncate(t.dim.Render("  enter opens the port group"), w))
	return lines
}
