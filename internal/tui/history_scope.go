package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
)

// This file is the Changes pane's "scope": the run axis an operator moves the
// comparison along, the per-vCenter coverage under it, and the change stream
// ranked by what a change means for a migration rather than by which field
// moved. The old pane answered "what differs between two run IDs"; the
// question actually being asked is "did anything happen that I have to deal
// with, and was the whole estate even looked at".

// impact ranks a change by the decision it forces. The order of the constants
// is the order rows are presented in, so a blocker can never sort below churn
// however many of the latter a noisy run produced.
type impact int

const (
	// impactAll is the "no filter" sentinel, not a class any row carries.
	impactAll impact = iota
	impactBlocks
	impactSizing
	impactGrowth
	impactChurn
)

// staleSnapshotAge is when a snapshot stops being an operational detail and
// starts being something that blocks a migration cutover: it grows unbounded
// and has to be consolidated before the VM can move.
const staleSnapshotAge = 30 * 24 * time.Hour

func (i impact) String() string {
	switch i {
	case impactBlocks:
		return "blocks"
	case impactSizing:
		return "sizing"
	case impactGrowth:
		return "growth"
	case impactChurn:
		return "churn"
	default:
		return "all"
	}
}

// why is the one-line explanation of what the class means, shown when the
// stream is filtered to it. A label an operator has to guess at is a label
// that gets ignored.
func (i impact) why() string {
	switch i {
	case impactBlocks:
		return "must be dealt with before a migration cutover"
	case impactSizing:
		return "changes what the destination has to be sized for"
	case impactGrowth:
		return "new objects entering the estate"
	case impactChurn:
		return "renames, relocations and annotations"
	default:
		return "every change in the span"
	}
}

func (i impact) style(t theme) lipgloss.Style {
	switch i {
	case impactBlocks:
		return t.bad
	case impactSizing:
		return t.warn
	case impactGrowth:
		return t.ok
	default:
		return t.dim
	}
}

// scopeRow is one line of the change stream. members holds the underlying
// change rows: exactly one for an individual change, several when identical
// changes were rolled up, and the inspector reads them either way.
type scopeRow struct {
	impact  impact
	object  string
	context string
	detail  string
	members []historyRow
}

func (r scopeRow) grouped() bool { return len(r.members) > 1 }

// lead is the member a grouped row stands for — used when a key needs one
// concrete object, such as opening a timeline.
func (r scopeRow) lead() historyRow {
	if len(r.members) == 0 {
		return historyRow{}
	}
	return r.members[0]
}

// classified is one change with the two things the stream sorts and groups
// by, computed once from the diff's own structs rather than re-parsed out of
// the flattened row text.
type classified struct {
	row       historyRow
	impact    impact
	signature string
}

// classifyDiff turns a diff into classified rows. It reads the typed change
// structs — not the row summaries — because the class of a change depends on
// evidence the summary throws away, such as how old a snapshot actually is.
func classifyDiff(d *assessment.Diff) []classified {
	if d == nil {
		return nil
	}
	ages := snapshotAges(d)
	var out []classified
	for _, v := range d.VMs {
		row := historyRow{kind: "vm", label: v.Name, context: v.Context, change: strings.Join(v.Changes, ", "), detail: changeDetail(v)}
		out = append(out, classified{row: row, impact: vmImpact(v), signature: vmSignature(v)})
	}
	for _, s := range d.Snapshots {
		row := historyRow{kind: "snapshot", label: s.VMName + " / " + s.Name, context: s.Context, change: "snapshot " + s.Kind, detail: nonempty(s.After, s.Before)}
		age, known := ages[s.VMName+"/"+s.Name]
		class, signature := impactChurn, "snapshot "+s.Kind
		if known && age >= staleSnapshotAge {
			class = impactBlocks
			row.detail = fmt.Sprintf("%s · %s old", row.detail, formatInterval(age))
			signature = "snapshot stale"
		}
		out = append(out, classified{row: row, impact: class, signature: signature})
	}
	for _, r := range d.Resources {
		row := historyRow{kind: r.Kind, label: r.Name, context: r.Context, change: strings.Join(r.Changes, ", "), detail: resourceDetail(r)}
		out = append(out, classified{row: row, impact: resourceImpact(r), signature: r.Kind + " " + strings.Join(r.Changes, ",") + " " + fieldSignature(r.Fields)})
	}
	return out
}

// snapshotAges indexes the target run's snapshot ages by VM and snapshot
// name, which is what separates a snapshot taken this morning from one that
// has been quietly growing for six weeks.
func snapshotAges(d *assessment.Diff) map[string]time.Duration {
	ages := make(map[string]time.Duration, len(d.SnapshotAges))
	for _, a := range d.SnapshotAges {
		ages[a.VMName+"/"+a.Name] = a.Age
	}
	return ages
}

// vmImpact ranks a VM change. A VM that is no longer there is always a
// blocker: either it was decommissioned and nobody recorded it, or the
// capture could not see it — both need an answer before a cutover. A
// migration-configuration change is a blocker for the same reason: firmware,
// secure boot and device changes are exactly what a destination has to match.
func vmImpact(v assessment.VMChange) impact {
	if hasChange(v.Changes, "vanished") {
		return impactBlocks
	}
	for _, f := range v.Fields {
		if strings.HasPrefix(f.Field, "migration_configuration.") {
			return impactBlocks
		}
	}
	if hasChange(v.Changes, "appeared") {
		return impactGrowth
	}
	for _, f := range v.Fields {
		switch f.Field {
		case "cpu", "memory", "storage_gb":
			return impactSizing
		}
	}
	return impactChurn
}

// resourceImpact ranks an infrastructure change: capacity fields size the
// destination, a new cluster or datastore is growth, anything else is churn.
// A disappearing host or datastore is a blocker for the same reason a
// vanished VM is.
func resourceImpact(r assessment.ResourceChange) impact {
	if hasChange(r.Changes, "vanished") {
		return impactBlocks
	}
	if hasChange(r.Changes, "appeared") {
		return impactGrowth
	}
	for _, f := range r.Fields {
		if strings.Contains(f.Field, "capacity") || strings.Contains(f.Field, "used") || strings.Contains(f.Field, "free") {
			return impactSizing
		}
	}
	return impactChurn
}

func hasChange(changes []string, want string) bool {
	for _, c := range changes {
		if c == want {
			return true
		}
	}
	return false
}

// vmSignature is what makes two VM changes "the same change": the same set of
// changed fields moving by the same amounts. Nine VMs each given the same
// 8 GiB share a signature; nine VMs given nine different sizes do not.
func vmSignature(v assessment.VMChange) string {
	return "vm " + strings.Join(v.Changes, ",") + " " + fieldSignature(v.Fields)
}

func fieldSignature(fields []assessment.FieldChange) string {
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		// The name and the annotation are per-object by definition, so they
		// are identity rather than signature — rolling up "renamed" would
		// hide which object was renamed to what.
		if f.Field == "name" || f.Field == "annotation" {
			parts = append(parts, f.Field)
			continue
		}
		parts = append(parts, f.Field+":"+f.Before+"→"+f.After)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// groupThreshold is how many identical changes it takes before the stream
// rolls them into one line. Two is a coincidence worth reading in full;
// three is a pattern, and by nine the individual lines are noise hiding the
// blockers above them.
const groupThreshold = 3

// scopeRows is the change stream: classified, filtered, rolled up, and
// ordered by impact. Blockers are never rolled up — a blocker an operator has
// to expand to see the name of is a blocker they will not act on.
func (m *Model) scopeRows() []scopeRow {
	rows := classifyDiff(m.changeDiff)
	needle := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	kept := rows[:0]
	for _, r := range rows {
		if m.impactFilter != impactAll && r.impact != m.impactFilter {
			continue
		}
		if needle != "" && !matchesRow(r.row, needle) {
			continue
		}
		kept = append(kept, r)
	}
	return groupScopeRows(kept)
}

func matchesRow(r historyRow, needle string) bool {
	return strings.Contains(strings.ToLower(r.label), needle) ||
		strings.Contains(strings.ToLower(r.context), needle) ||
		strings.Contains(strings.ToLower(r.change), needle) ||
		strings.Contains(strings.ToLower(r.kind), needle)
}

// groupScopeRows rolls identical changes in the same vCenter together and
// returns the stream in presentation order.
func groupScopeRows(rows []classified) []scopeRow {
	type bucket struct {
		impact  impact
		context string
		members []historyRow
	}
	var order []string
	buckets := map[string]*bucket{}
	var single []scopeRow
	for _, r := range rows {
		if r.impact == impactBlocks {
			single = append(single, scopeRow{impact: r.impact, object: r.row.label, context: r.row.context, detail: rowDetail(r.row), members: []historyRow{r.row}})
			continue
		}
		key := r.impact.String() + "\x00" + r.row.context + "\x00" + r.signature
		b, ok := buckets[key]
		if !ok {
			b = &bucket{impact: r.impact, context: r.row.context}
			buckets[key] = b
			order = append(order, key)
		}
		b.members = append(b.members, r.row)
	}
	out := single
	for _, key := range order {
		b := buckets[key]
		if len(b.members) < groupThreshold {
			for _, member := range b.members {
				out = append(out, scopeRow{impact: b.impact, object: member.label, context: member.context, detail: rowDetail(member), members: []historyRow{member}})
			}
			continue
		}
		out = append(out, scopeRow{impact: b.impact, object: groupLabel(b.members), context: b.context, detail: groupDetail(b.members), members: b.members})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].impact != out[j].impact {
			return out[i].impact < out[j].impact
		}
		if out[i].context != out[j].context {
			return out[i].context < out[j].context
		}
		return out[i].object < out[j].object
	})
	return out
}

// rowDetail is what one change reads as on its own line: the change itself,
// then the before/after preview when there is one.
func rowDetail(r historyRow) string {
	if r.detail == "" {
		return r.change
	}
	return r.change + " · " + r.detail
}

// groupLabel names a rolled-up set. When the members share a real name prefix
// — a k8s worker pool, a numbered app tier — the prefix is more useful than a
// bare count, because it says which part of the estate moved.
func groupLabel(members []historyRow) string {
	count := fmt.Sprintf("%d %s", len(members), plural(members[0].kind, len(members)))
	prefix := commonPrefix(members)
	if len([]rune(prefix)) < 4 {
		return count
	}
	return strings.TrimRight(prefix, "-_. ") + "… " + count
}

func groupDetail(members []historyRow) string {
	return rowDetail(members[0])
}

func plural(kind string, n int) string {
	name := kind
	if kind == "vm" {
		name = "VM"
	}
	if n == 1 {
		return name
	}
	return name + "s"
}

// commonPrefix is the longest name prefix every member shares, in runes so a
// non-ASCII VM name is never cut mid-character.
func commonPrefix(members []historyRow) string {
	if len(members) == 0 {
		return ""
	}
	prefix := []rune(members[0].label)
	for _, member := range members[1:] {
		label := []rune(member.label)
		if len(label) < len(prefix) {
			prefix = prefix[:len(label)]
		}
		for i := range prefix {
			if label[i] != prefix[i] {
				prefix = prefix[:i]
				break
			}
		}
		if len(prefix) == 0 {
			return ""
		}
	}
	return string(prefix)
}

// ---- the run axis -------------------------------------------------------

// scrubCellWidth is one run's slot on the axis. Five columns holds "#124"
// with a space either side, which is the widest run label the store produces
// before an estate has captured ten thousand assessments.
const scrubCellWidth = 5

// scrubLabelWidth is the row-label gutter — "RUNS", "VMs", the vCenter names
// — shared by the axis and the coverage matrix so their columns line up.
const scrubLabelWidth = 8

// scrubNoteWidth is the right-hand margin the axis annotates itself in. Below
// scrubNoteMinWidth there is no margin and the notes move under the axis.
const (
	scrubNoteWidth    = 34
	scrubNoteMinWidth = 100
)

// scrubWindow is the slice of m.runs the axis draws, as indices into the
// newest-first run list. offset is the newest visible run; the returned count
// is however many cells the terminal has room for.
func scrubWindow(runs []assessment.Run, offset, width int) (start, count int) {
	usable := width
	if width >= scrubNoteMinWidth {
		usable -= scrubNoteWidth
	}
	cells := (usable - scrubLabelWidth) / scrubCellWidth
	if cells < 1 {
		cells = 1
	}
	if cells > len(runs) {
		cells = len(runs)
	}
	start = clamp(offset, 0, max(0, len(runs)-cells))
	return start, cells
}

// scrubBars renders one axis cell per visible run, scaled across the VM
// counts the churn trend recorded. A run the trend has no point for — a
// failed capture, or one older than the trend window — draws as a gap rather
// than as a zero, because "we did not measure" and "there were none" are
// different facts.
func scrubBars(counts map[int64]int, runs []assessment.Run) []string {
	glyphs := []rune("▁▂▃▄▅▆▇█")
	lo, hi, seen := 0, 0, false
	for _, r := range runs {
		v, ok := counts[r.ID]
		if !ok {
			continue
		}
		if !seen {
			lo, hi, seen = v, v, true
			continue
		}
		lo, hi = min(lo, v), max(hi, v)
	}
	out := make([]string, len(runs))
	for i, r := range runs {
		v, ok := counts[r.ID]
		switch {
		case !ok:
			out[i] = "·"
		case !seen || hi == lo:
			out[i] = string(glyphs[len(glyphs)-1])
		default:
			out[i] = string(glyphs[(v-lo)*(len(glyphs)-1)/(hi-lo)])
		}
	}
	return out
}

// viewScrubber draws the run axis: IDs, capture size, and the two handles.
// Runs read left to right oldest to newest, the direction time is drawn in
// everywhere else, even though the run list itself is newest-first.
func (m *Model) viewScrubber(width int, bars bool) []string {
	t := m.theme
	if len(m.runs) == 0 {
		return []string{t.dim.Render("  no assessments stored — press n to capture")}
	}
	start, count := scrubWindow(m.runs, m.scrubOffset, width)
	visible := make([]assessment.Run, 0, count)
	for i := start + count - 1; i >= start; i-- {
		visible = append(visible, m.runs[i])
	}
	counts := map[int64]int{}
	if m.historyChurn != nil {
		for _, p := range m.historyChurn.Points {
			counts[p.Run.ID] = p.VMCount
		}
	}
	ids := pad("RUNS", scrubLabelWidth, false)
	sizes := pad("VMs", scrubLabelWidth, false)
	handles := pad("pick", scrubLabelWidth, false)
	glyphs := scrubBars(counts, visible)
	for i, r := range visible {
		style := t.dim
		switch r.ID {
		case m.baseRun, m.targetRun:
			style = t.accent
		}
		if r.Status == assessment.RunFailed || r.Status == assessment.RunPartial {
			style = t.warn
		}
		ids += style.Render(centre(historyRunLabel(r.ID), scrubCellWidth))
		sizes += t.dim.Render(centre(glyphs[i], scrubCellWidth))
		handleStyle := t.accent
		if m.handleMark(r.ID) == m.scrubHandle {
			handleStyle = t.tabOn
		}
		handles += handleStyle.Render(centre(m.handleMark(r.ID), scrubCellWidth))
	}
	notes := m.scrubNotes(width)
	lines := []string{ids}
	if bars {
		lines = append(lines, sizes)
	}
	lines = append(lines, handles)
	for i := range lines {
		if width >= scrubNoteMinWidth && i < len(notes) {
			lines[i] += "  " + notes[i]
		}
		lines[i] = truncate("  "+lines[i], m.width)
	}
	if width < scrubNoteMinWidth {
		for _, note := range notes {
			lines = append(lines, truncate("  "+note, m.width))
		}
	}
	return lines
}

// handleMark is the letter drawn under a run: which end of the comparison it
// is. The active end is styled without changing the lowercase key label.
func (m *Model) handleMark(id int64) string {
	mark := ""
	switch id {
	case m.baseRun:
		mark = "b"
	case m.targetRun:
		mark = "t"
	default:
		return ""
	}
	return mark
}

// scrubNotes is the axis's right margin: what the span is, how big the estate
// is, and how to move. Three lines, matching the three axis rows.
func (m *Model) scrubNotes(width int) []string {
	t := m.theme
	span := t.dim.Render("no span")
	if m.changeDiff != nil {
		d := m.changeDiff
		span = t.accent.Render(historyRunLabel(d.Base.ID)+" → "+historyRunLabel(d.Target.ID)) +
			t.dim.Render(" · "+formatInterval(d.Target.StartedAt.Sub(d.Base.StartedAt))+" · "+runsInSpan(m.runs, d))
	}
	size := t.dim.Render("—")
	if m.historyChurn != nil && len(m.historyChurn.Points) > 0 {
		latest := m.historyChurn.Points[len(m.historyChurn.Points)-1]
		size = t.dim.Render(fmt.Sprintf("%d VMs in %s", latest.VMCount, historyRunLabel(latest.Run.ID)))
	}
	return []string{span, size, t.faint.Render("←/→ move " + m.handleName() + " · b/t pick end")}
}

func (m *Model) handleName() string {
	if m.scrubHandle == "b" {
		return "baseline"
	}
	return "target"
}

// runsInSpan counts the assessments the comparison jumps over, which is what
// makes a diff across a week read differently from one across two runs.
func runsInSpan(runs []assessment.Run, d *assessment.Diff) string {
	n := 0
	for _, r := range runs {
		if !r.StartedAt.Before(d.Base.StartedAt) && !r.StartedAt.After(d.Target.StartedAt) {
			n++
		}
	}
	if n == 1 {
		return "1 run"
	}
	return fmt.Sprintf("%d runs", n)
}

// centre pads a short cell value to an exact width with the value in the
// middle, so the axis rows sit under their run IDs however the cell is drawn.
func centre(s string, w int) string {
	gap := w - len([]rune(s))
	if gap <= 0 {
		return pad(s, w, false)
	}
	left := gap / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", gap-left)
}

// ---- coverage -----------------------------------------------------------

// viewCoverage draws which vCenters each visible run actually reached. It is
// a matrix only when there is something to see: when every visible run
// covered the same vCenters it collapses to a single line, so the rows are
// spent on the case that misleads — a narrower capture reading as mass
// deletion because nothing on screen said the comparison was partial.
func (m *Model) viewCoverage(width int) []string {
	t := m.theme
	if len(m.runs) == 0 {
		return nil
	}
	start, count := scrubWindow(m.runs, m.scrubOffset, width)
	visible := make([]assessment.Run, 0, count)
	for i := start + count - 1; i >= start; i-- {
		visible = append(visible, m.runs[i])
	}
	names := m.coverageNames(visible)
	if len(names) == 0 {
		if m.historyCoverage == nil {
			return []string{truncate("  "+t.dim.Render("coverage loading…"), m.width)}
		}
		return nil
	}
	if uniform(m.historyCoverage, visible, names) {
		return []string{truncate("  "+t.ok.Render("full coverage")+t.dim.Render("  "+strings.Join(names, ", ")), m.width)}
	}
	lines := make([]string, 0, len(names)+2)
	lines = append(lines, truncate("  "+pad("COVER", scrubLabelWidth, false)+t.dim.Render(" ● reached   ✕ not compared   · no record"), m.width))
	for _, name := range names {
		line := pad(name, scrubLabelWidth, false)
		for _, r := range visible {
			line += m.coverageGlyph(t, r.ID, name)
		}
		lines = append(lines, truncate("  "+line, m.width))
	}
	if hint := m.coverageHint(); hint != "" {
		lines = append(lines, truncate("  "+t.warn.Render(hint), m.width))
	}
	return lines
}

// coverageGlyph is deliberately binary: was this vCenter in that run's
// comparison or not. A site the run never attempted and a site whose
// collection failed are the same fact to a diff — its objects are missing
// from one side — so both draw as a miss rather than as two glyphs an
// operator has to decode. A run with no context records at all is unknown.
func (m *Model) coverageGlyph(t theme, runID int64, name string) string {
	contexts, ok := m.historyCoverage[runID]
	if !ok || len(contexts) == 0 {
		return t.faint.Render(centre("·", scrubCellWidth))
	}
	if contexts[name] == "success" {
		return t.ok.Render(centre("●", scrubCellWidth))
	}
	return t.bad.Render(centre("✕", scrubCellWidth))
}

// coverageNames is every vCenter any visible run recorded, named the way the
// operator named it rather than by its vCenter UUID.
func (m *Model) coverageNames(visible []assessment.Run) []string {
	seen := map[string]bool{}
	var names []string
	for _, r := range visible {
		for name := range m.historyCoverage[r.ID] {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// uniform reports whether every visible run reached exactly the same
// vCenters, which is the case that needs one line rather than a matrix.
func uniform(coverage map[int64]map[string]string, visible []assessment.Run, names []string) bool {
	for _, r := range visible {
		contexts, ok := coverage[r.ID]
		if !ok {
			return false
		}
		for _, name := range names {
			if contexts[name] != "success" {
				return false
			}
		}
	}
	return true
}

// coverageHint names the gap between the two selected runs in plain words and
// says which key fixes it. The matrix shows that a gap exists; this says what
// it does to the counts below.
func (m *Model) coverageHint() string {
	if m.changeDiff == nil {
		return ""
	}
	missingTarget, missingBaseline := coverageGaps(m.changeDiff)
	switch {
	case len(missingTarget) > 0:
		return fmt.Sprintf("! %s not in the target — its objects read as vanished; c clips the span to shared coverage", strings.Join(missingTarget, ", "))
	case len(missingBaseline) > 0:
		return fmt.Sprintf("! %s not in the baseline — its objects read as appeared; c clips the span to shared coverage", strings.Join(missingBaseline, ", "))
	default:
		return ""
	}
}

// clipTarget finds the newest run older than the target that reached exactly
// the vCenters the target reached. It is what "c" moves the baseline to: the
// nearest comparison that is a like-for-like one.
func clipTarget(runs []assessment.Run, coverage map[int64]map[string]string, targetID int64) (int64, bool) {
	want, ok := coverage[targetID]
	if !ok {
		return 0, false
	}
	seen := false
	for _, r := range runs {
		if r.ID == targetID {
			seen = true
			continue
		}
		if !seen {
			// Newer than the target; a baseline is always older.
			continue
		}
		if sameCoverage(want, coverage[r.ID]) {
			return r.ID, true
		}
	}
	return 0, false
}

func sameCoverage(a, b map[string]string) bool {
	if len(a) != len(b) || b == nil {
		return false
	}
	for name, status := range a {
		if b[name] != status {
			return false
		}
	}
	return true
}

// ---- the stream ---------------------------------------------------------

// scopeColumnWidths sizes the stream. IMPACT and vCENTER are fixed and never
// truncate — both are short, and a truncated vCenter name is a lie about
// which site a change happened in. OBJECT and DETAIL absorb whatever the
// terminal gives, and DETAIL is dropped entirely before OBJECT is squeezed.
func scopeColumnWidths(width int) (impactW, objectW, contextW, detailW int) {
	impactW, contextW = 7, 9
	if width < 70 {
		contextW = 7
	}
	avail := width - cursorGutter - impactW - 1 - contextW - 1
	objectW = avail
	if objectW > 28 {
		detailW = objectW - 28 - 1
		objectW = 28
	}
	if objectW < 8 {
		objectW = 8
	}
	if detailW < 12 {
		detailW = 0
	}
	return impactW, objectW, contextW, detailW
}

// renderScopeStream draws the heading and as many rows as fit, starting from
// the list offset. A grouped row carries its member count in the object
// column, so a rolled-up line never pretends to be a single object.
func (m *Model) renderScopeStream(rows []scopeRow, width, height int) []string {
	t := m.theme
	impactW, objectW, contextW, detailW := scopeColumnWidths(width)
	heading := strings.Repeat(" ", cursorGutter) + pad("IMPACT", impactW, false) + " " + pad("OBJECT", objectW, false) + " " + pad("vCENTER", contextW, false)
	if detailW > 0 {
		heading += " " + pad("WHAT MOVED", detailW, false)
	}
	lines := []string{t.header.Render(truncate(heading, width))}
	for i := m.changeOffset; i < len(rows) && len(lines) < height; i++ {
		r := rows[i]
		body := " " + pad(r.object, objectW, false) + " " + pad(r.context, contextW, false)
		if detailW > 0 {
			body += " " + pad(r.detail, detailW, false)
		}
		plain := "▎" + pad(r.impact.String(), impactW, false) + body
		line := truncate("▎"+r.impact.style(t).Render(pad(r.impact.String(), impactW, false))+body, width)
		if i == m.changeCursor {
			// The cursor row takes one background across the whole line, so
			// the impact colour gives way to the highlight rather than
			// fighting it. Its leading bar becomes the cursor glyph, since a
			// background alone is gone under NO_COLOR (see cursorMark).
			line = t.focused.Render(truncate(glyphCursor+strings.TrimPrefix(plain, "▎"), width))
		}
		lines = append(lines, line)
	}
	return lines
}

// scopeInspector is the detail beside the stream. An individual row reuses
// the change inspector; a grouped row lists what it rolled up, because "9
// VMs" is only actionable once you can see which nine.
func (m *Model) scopeInspector(r scopeRow) []string {
	t := m.theme
	if !r.grouped() {
		return m.historyInspector(r.lead())
	}
	lines := []string{
		t.title.Render(r.object),
		"  " + t.label.Render("context") + "  " + r.context,
		"  " + t.label.Render("impact") + "   " + r.impact.style(t).Render(r.impact.String()) + t.dim.Render(" — "+r.impact.why()),
		"  " + t.label.Render("change") + "   " + r.detail,
		"",
		t.header.Render(fmt.Sprintf("  %d objects", len(r.members))),
	}
	for _, member := range r.members {
		lines = append(lines, "  "+t.value.Render(member.label))
	}
	return lines
}
