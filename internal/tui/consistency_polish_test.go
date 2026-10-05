package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// These tests pin the six inconsistencies of issue #289. Each states the
// rule it protects, and the layout ones run at the widths the interface is
// reviewed at (60, 80, 100).

// Item 1: a host's CPU and memory are usage bars like a datastore's, and the
// gigahertz and gigabyte figures behind them share one precision.
func TestHostCPUAndMemoryAreUsageBars(t *testing.T) {
	for _, width := range []int{80, 100} {
		m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
		m.width, m.height = width, 24
		press(t, m, "3")
		view := ansi.Strip(m.View())
		row := lineContaining(t, view, "esxi-01")
		head := lineContaining(t, view, "CPU")
		if strings.Contains(row, "GHz") || strings.Contains(row, "/") {
			t.Errorf("%d columns: host row still prints a used/total figure: %q", width, row)
		}
		for _, want := range []string{"23%", "50%", "█", "·"} {
			if !strings.Contains(row, want) {
				t.Errorf("%d columns: host row has no %q bar: %q", width, want, row)
			}
		}
		// The bar sits under its heading, left aligned like USED.
		if cpuAt := colOf(head, "CPU"); !strings.HasPrefix(ansi.Cut(row, cpuAt, cpuAt+1), "█") && !strings.HasPrefix(ansi.Cut(row, cpuAt, cpuAt+1), "·") {
			t.Errorf("%d columns: the CPU bar is not under its heading:\n%s", width, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Errorf("%d columns: line is %d wide: %q", width, w, line)
			}
		}
	}
}

func TestHostBarColumnsFitWithoutTruncation(t *testing.T) {
	cols := columnsFor(vsphere.KindHost, false)
	for _, total := range []int{60, 80, 100, 140} {
		widths := layoutColumns(cols, total-glyphGutter)
		for i, c := range cols {
			if (c.title == "CPU" || c.title == "MEMORY") && widths[i] != 0 && widths[i] < ansi.StringWidth(capacityBar(1, 2, hostBarWidth)) {
				t.Errorf("%d columns: %s is %d wide, narrower than its bar", total, c.title, widths[i])
			}
		}
		// The point of the change: at 80 columns CPU and MEMORY are both drawn.
		if total >= 80 {
			for i, c := range cols {
				if (c.title == "CPU" || c.title == "MEMORY") && widths[i] == 0 {
					t.Errorf("%d columns: %s was dropped", total, c.title)
				}
			}
		}
	}
}

func TestCapacityBar(t *testing.T) {
	if got := capacityBar(0, 0, hostBarWidth); got != "-" {
		t.Errorf("no capacity should read as not reported, got %q", got)
	}
	if got := capacityBar(0, 1000, hostBarWidth); !strings.HasSuffix(got, "  0%") || strings.Contains(got, "█") {
		t.Errorf("an idle host is an empty bar at 0%%, got %q", got)
	}
	if got, want := capacityBar(75, 100, 10), usageBar(75, 10); got != want {
		t.Errorf("a host bar should be the datastore bar: %q vs %q", got, want)
	}
	if got := capacityBar(500, 100, hostBarWidth); !strings.HasSuffix(got, "100%") {
		t.Errorf("overcommit clamps at 100%%, got %q", got)
	}
}

func TestHostDetailKeepsExactFiguresAtOnePrecision(t *testing.T) {
	b := twoHealthy()
	inv := b.inventories["prod"]
	inv.Hosts[0].CPUUsageMHz, inv.Hosts[0].TotalCPUMHz = 18500, 115000
	inv.Hosts[0].MemoryUsageMB, inv.Hosts[0].MemoryMB = 123392, 262144
	m := newTestModel(t, b, Options{Current: "prod"})
	m.width, m.height = 100, 40
	press(t, m, "3")
	press(t, m, "enter")
	view := ansi.Strip(m.View())
	if _, line := lineWith(view, "CPU used"); !strings.Contains(line, "18.5GHz/115.0GHz") {
		t.Errorf("CPU used = %q, want 18.5GHz/115.0GHz", line)
	}
	if _, line := lineWith(view, "Memory used"); !strings.Contains(line, "120.5G/256.0G") {
		t.Errorf("Memory used = %q, want 120.5G/256.0G", line)
	}
}

// Item 2: the footer only promises "</> range" while charts are on screen.
func TestRangeHintFollowsTheChartsOnScreen(t *testing.T) {
	hasRange := func(m *Model) bool {
		for _, b := range m.keys.footerHints(m) {
			if b.Help().Key == "</>" {
				return true
			}
		}
		return false
	}
	open := func(width, height int) *Model {
		b := perfHealthy()
		m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
		m.backend = b
		m.width, m.height = width, height
		press(t, m, "enter")
		return m
	}

	// Beside the properties (the split layout) the charts are always drawn.
	if m := open(140, 40); !hasRange(m) {
		t.Errorf("140x40 draws the charts beside the properties but the footer omits </> range")
	}
	// Stacked on a short terminal they start below the fold.
	m := open(80, 24)
	if _, line := lineWith(m.View(), "CPU usage"); line != "" {
		t.Fatalf("80x24 should not show the charts yet, but found %q", line)
	}
	if hasRange(m) {
		t.Errorf("80x24 draws no charts but the footer advertises </> range:\n%s", ansi.Strip(m.View()))
	}
	// Paging down brings them into view, and the hint with them.
	for i := 0; i < 3 && !hasRange(m); i++ {
		press(t, m, "pgdown")
	}
	if !hasRange(m) {
		t.Fatalf("the charts never came into view:\n%s", ansi.Strip(m.View()))
	}
	if _, line := lineWith(m.View(), "CPU usage"); line == "" {
		t.Errorf("the footer advertises </> range but no chart is drawn:\n%s", ansi.Strip(m.View()))
	}
	// A tall stacked terminal draws them straight away.
	if m := open(80, 80); !hasRange(m) {
		t.Errorf("80x80 stacks the charts in view but the footer omits </> range")
	}
	// Without a live backend there are no charts at all, only a notice.
	plain := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	plain.width, plain.height = 140, 40
	press(t, plain, "enter")
	if hasRange(plain) {
		t.Errorf("a backend without performance data still advertises </> range")
	}
}

// Item 3: help puts datastore keys in a datastore section and says "tab" once.
func TestHelpSectionsAreCoherent(t *testing.T) {
	k := defaultKeys()
	var tabs int
	for _, sec := range k.helpSections(false) {
		for _, b := range sec.bindings {
			h := b.Help()
			if h.Key == "tab" {
				tabs++
			}
			if h.Key == "f" || h.Key == "y" {
				if sec.title != "Datastores" {
					t.Errorf("%q is listed under %q, want the Datastores section", h.Key, sec.title)
				}
			}
		}
	}
	if tabs != 1 {
		t.Errorf("tab is listed %d times in the browse help, want once", tabs)
	}
	// The hub's own help still carries its meaning of tab.
	var hubTab string
	for _, sec := range k.historyHelpSections(historyPaneChanges, false) {
		for _, b := range sec.bindings {
			if h := b.Help(); h.Key == "tab" {
				hubTab = h.Desc
			}
		}
	}
	if hubTab != "next pane" {
		t.Errorf("history help lists tab as %q, want next pane", hubTab)
	}
}

// Item 4: the filter count names what it counted.
func TestFilterCountSaysWhatItCounted(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	m.width, m.height = 100, 24
	press(t, m, "/")
	typeText(t, m, "app")
	hint := m.filterHint()
	if !strings.Contains(hint, "1 VM in prod") || strings.Contains(hint, "here") {
		t.Errorf("hint = %q, want it to name the kind and the vCenter", hint)
	}
	m.allScope = true
	if hint := m.filterHint(); !strings.Contains(hint, "VM in all vCenters") {
		t.Errorf("all-scope hint = %q, want it to name every vCenter", hint)
	}
	for _, tc := range []struct {
		kind vsphere.Kind
		n    int
		want string
	}{
		{vsphere.KindVM, 1, "1 VM"}, {vsphere.KindVM, 10, "10 VMs"},
		{vsphere.KindHost, 2, "2 hosts"}, {vsphere.KindDatastore, 1, "1 datastore"},
		{vsphere.KindVApp, 3, "3 vApps"}, {vsphere.KindTemplate, 0, "0 templates"},
	} {
		if got := kindCount(tc.kind, tc.n); got != tc.want {
			t.Errorf("kindCount(%s, %d) = %q, want %q", tc.kind, tc.n, got, tc.want)
		}
	}
}

// Item 5: the Changes stream's IMPACT heading starts where its rows' impact
// words do, whatever width the stream is drawn at.
func TestChangeStreamHeadingAlignsWithRows(t *testing.T) {
	base := []assessment.Observation{vmObservation("billing-primary-database", 2, 4096), vmObservation("doomed", 2, 4096)}
	target := []assessment.Observation{vmObservation("billing-primary-database", 8, 65536)}
	store := twoRunStore(t, base, target)
	for _, width := range columnWidthSizes {
		m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
		m.width, m.height = width, 24
		press(t, m, "H")
		rows := m.scopeRows()
		if len(rows) == 0 {
			t.Fatal("no change rows")
		}
		lines := m.renderScopeStream(rows, width, 10)
		head := ansi.Strip(lines[0])
		first := ansi.Strip(lines[1])
		if got, want := colOf(head, "IMPACT"), colOf(first, rows[0].impact.String()); got != want {
			t.Errorf("%d columns: IMPACT starts at column %d, the rows' impact at %d:\n%s\n%s", width, got, want, head, first)
		}
		if got, want := colOf(head, "OBJECT"), colOf(first, rows[0].object); got != want {
			t.Errorf("%d columns: OBJECT starts at column %d, the rows' object at %d:\n%s\n%s", width, got, want, head, first)
		}
		for _, line := range lines {
			if w := ansi.StringWidth(line); w > width {
				t.Errorf("%d columns: stream line is %d wide: %q", width, w, line)
			}
		}
	}
}

// Item 6: the kind bar begins over the table's first column.
func TestKindBarStartsOverTheNameColumn(t *testing.T) {
	for _, width := range columnWidthSizes {
		m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
		m.width, m.height = width, 24
		lines := strings.Split(ansi.Strip(m.View()), "\n")
		tabs, head := lines[1], lineContaining(t, strings.Join(lines, "\n"), "NAME")
		if got, want := colOf(tabs, "1 "), colOf(head, "NAME"); got != want {
			t.Errorf("%d columns: kind bar starts at column %d, NAME at %d:\n%s\n%s", width, got, want, tabs, head)
		}
		if w := ansi.StringWidth(tabs); w > width {
			t.Errorf("%d columns: kind bar is %d wide", width, w)
		}
		if !strings.Contains(tabs, "7 ") {
			t.Errorf("%d columns: the last kind fell off the bar: %q", width, tabs)
		}
	}
}

// colOf is the display column at which sub first appears in line, -1 if it
// does not. strings.Index counts bytes, which the cursor and bar glyphs skew.
func colOf(line, sub string) int {
	i := strings.Index(line, sub)
	if i < 0 {
		return -1
	}
	return ansi.StringWidth(line[:i])
}
