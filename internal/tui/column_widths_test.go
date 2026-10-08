package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// The three tests below pin issue #287: search sized its columns for the
// shape of the table rather than its contents (PATH truncated while NAME sat
// half empty), the VM detail pane broke inventory paths mid-folder, and the
// Health findings were separated by one space where every other table uses
// cellGap. Each runs at 60, 80 and 100 columns, the widths the interface is
// designed and reviewed at.

var columnWidthSizes = []int{60, 80, 100}

const (
	deepHostPath = "/Taipei/host/Production-Compute/rack-14/compute-a/esxi-01"
	deepVMPath   = "/Taipei/vm/Infrastructure/Production/Windows/DomainControllers/ad-dc-01"
)

func deepPathModel(t *testing.T, width, height int) *Model {
	t.Helper()
	b := twoHealthy()
	inv := inventoryFor("prod")
	inv.Hosts[0].Path = deepHostPath
	inv.VMs[0].Name = "ad-dc-01"
	inv.VMs[0].Path = deepVMPath
	inv.VMs[0].Folder = "/Infrastructure/Production/Windows/DomainControllers"
	b.inventories["prod"] = inv
	m := newTestModel(t, b, Options{Current: "prod"})
	m.width, m.height = width, height
	return m
}

func lineContaining(t *testing.T, view, needle string) string {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no line contains %q:\n%s", needle, view)
	return ""
}

func TestSearchSizesColumnsToContentAndKeepsThePathLeaf(t *testing.T) {
	for _, width := range columnWidthSizes {
		m := deepPathModel(t, width, 24)
		press(t, m, "/")
		typeText(t, m, "esxi-01")
		press(t, m, "tab")
		if m.mode != modeSearch {
			t.Fatalf("%d columns: tab opened mode %v, want search", width, m.mode)
		}
		view := ansi.Strip(m.View())
		for _, line := range strings.Split(view, "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Fatalf("%d columns: line is %d wide: %q", width, w, line)
			}
		}
		head := lineContaining(t, view, "VCENTER")
		row := lineContaining(t, view, " host ")
		if strings.Contains(head, "PATH") {
			// NAME is capped at its widest value (7 columns, "esxi-01"),
			// so PATH starts one gap after it, not twenty columns later.
			nameAt, dcAt := strings.Index(head, "NAME"), strings.Index(head, "DATACENTER")
			if want := len("esxi-01") + cellGap; dcAt-nameAt != want {
				t.Errorf("%d columns: NAME is %d wide, want %d (its widest value plus the gap):\n%s", width, dcAt-nameAt, want, view)
			}
			// The leaf survives truncation; the tail of the path is the
			// part that says which host this is.
			path := strings.TrimSpace(row[strings.Index(head, "PATH"):])
			if !strings.HasSuffix(path, "/compute-a/esxi-01") && !strings.HasSuffix(path, "/esxi-01") {
				t.Errorf("%d columns: PATH lost its leaf: %q", width, path)
			}
			if path != deepHostPath && !strings.HasPrefix(path, "…/") {
				t.Errorf("%d columns: a shortened PATH should start with an ellipsis and a slash, got %q", width, path)
			}
		} else if width >= 80 {
			t.Errorf("%d columns: PATH was dropped although the other columns fit in far less:\n%s", width, view)
		}
		if strings.Contains(row, "esxi-01…") || !strings.Contains(row, "esxi-01") {
			t.Errorf("%d columns: NAME was truncated: %q", width, row)
		}
	}
}

func TestSearchShowsTheWholePathWhenItFits(t *testing.T) {
	m := deepPathModel(t, 140, 24)
	press(t, m, "/")
	typeText(t, m, "esxi-01")
	press(t, m, "tab")
	if view := ansi.Strip(m.View()); !strings.Contains(view, deepHostPath) {
		t.Errorf("a path that fits was shortened:\n%s", view)
	}
}

func TestLayoutSearchStaysInsideTheWidth(t *testing.T) {
	rows := []row{
		{context: "prod", kind: vsphere.KindHost, name: "esxi-01", where: vsphere.Location{Datacenter: "Taipei", Path: deepHostPath}},
		{context: "a-very-long-vcenter-label-indeed", kind: vsphere.KindDatastore, name: strings.Repeat("n", 90), where: vsphere.Location{Datacenter: "Datacenter-With-A-Long-Name", Path: deepVMPath}},
	}
	for total := 41; total <= 200; total++ {
		widths := layoutSearch(rows, total)
		used, drawn := 0, 0
		for _, w := range widths {
			if w > 0 {
				used += w
				drawn++
			}
		}
		if used+cellGap*(drawn-1) > total && drawn > 1 {
			t.Fatalf("total %d: columns %v need %d", total, widths, used+cellGap*(drawn-1))
		}
	}
	if widths := layoutSearch(nil, 77); widths[4] != 0 && widths[2] == 0 {
		t.Errorf("an empty result set must still draw NAME: %v", widths)
	}
}

func TestShortenPathKeepsTheLeaf(t *testing.T) {
	for _, tc := range []struct {
		in   string
		w    int
		want string
	}{
		{"/Taipei/host/compute-a/esxi-01", 30, "/Taipei/host/compute-a/esxi-01"},
		{"/Taipei/host/compute-a/esxi-01", 20, "…/compute-a/esxi-01"},
		{"/Taipei/host/compute-a/esxi-01", 12, "…/esxi-01"},
		{"/Taipei/host/compute-a/esxi-01", 8, "…esxi-01"[:len("…esxi-01")]},
		{"/Taipei/vm/a-leaf-wider-than-the-column", 10, "…the-column"},
	} {
		got := shortenPath(tc.in, tc.w)
		if ansi.StringWidth(got) > tc.w {
			t.Errorf("shortenPath(%q, %d) = %q is %d wide", tc.in, tc.w, got, ansi.StringWidth(got))
		}
		if tc.w >= 12 && got != tc.want {
			t.Errorf("shortenPath(%q, %d) = %q, want %q", tc.in, tc.w, got, tc.want)
		}
		if !strings.HasSuffix(got, "esxi-01"[len("esxi-01")-min(len("esxi-01"), tc.w-2):]) && strings.Contains(tc.in, "esxi-01") && tc.w >= 9 {
			t.Errorf("shortenPath(%q, %d) = %q lost the leaf", tc.in, tc.w, got)
		}
	}
}

func TestWrapPathBreaksOnSlashes(t *testing.T) {
	const path = "/Taipei/vm/Infrastructure/Core/ad-dc-01"
	for _, tc := range []struct {
		w    int
		want []string
	}{
		{100, []string{path}},
		{38, []string{"/Taipei/vm/Infrastructure/Core/", "ad-dc-01"}},
		{28, []string{"/Taipei/vm/Infrastructure/", "Core/ad-dc-01"}},
		{12, []string{"/Taipei/vm/", "Infrastructu", "re/Core/", "ad-dc-01"}},
	} {
		got := wrapPath(path, tc.w)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("wrapPath(%q, %d) = %q, want %q", path, tc.w, got, tc.want)
		}
		if strings.Join(got, "") != path {
			t.Errorf("wrapPath(%q, %d) lost characters: %q", path, tc.w, got)
		}
		for _, line := range got {
			if ansi.StringWidth(line) > tc.w {
				t.Errorf("wrapPath(%q, %d) line %q is wider than the width", path, tc.w, line)
			}
		}
	}
}

func TestVMDetailWrapsInventoryPathOnSlashes(t *testing.T) {
	for _, width := range columnWidthSizes {
		m := deepPathModel(t, width, 40)
		var vm row
		for _, r := range m.rows() {
			if r.name == "ad-dc-01" {
				vm = r
			}
		}
		if vm.vm == nil {
			t.Fatalf("%d columns: fixture VM not found", width)
		}
		lines, _ := m.vmPropertyLines(vm, false)
		labelW := min(labelColumnPad, max(0, m.vmPropertyWidth()-3))
		for _, label := range []string{"Inventory path", "Folder"} {
			want := map[string]string{"Inventory path": deepVMPath, "Folder": "/Infrastructure/Production/Windows/DomainControllers"}[label]
			var got []string
			collecting := false
			for _, line := range lines {
				line = ansi.Strip(line)
				switch {
				case strings.HasPrefix(line, "  "+label):
					collecting = true
					got = append(got, strings.TrimRight(line[2+labelW:], " "))
				case collecting && strings.TrimSpace(line[:min(len(line), 2+labelW)]) == "" && strings.TrimSpace(line) != "":
					got = append(got, strings.TrimRight(line[2+labelW:], " "))
				default:
					collecting = false
				}
			}
			if strings.Join(got, "") != want {
				t.Fatalf("%d columns: %s renders as %q, which is not %q", width, label, got, want)
			}
			if label == "Inventory path" && len(got) < 2 {
				t.Fatalf("%d columns: fixture path fits on one line, so this does not test wrapping: %q", width, got)
			}
			for i, line := range got {
				if i < len(got)-1 && !strings.HasSuffix(line, "/") {
					t.Errorf("%d columns: %s line %d breaks inside a folder name: %q (all lines %q)", width, label, i, line, got)
				}
			}
		}
	}
}

func TestHealthFindingsUseTheSharedColumnGap(t *testing.T) {
	for _, width := range columnWidthSizes {
		m := healthPaneModel(t, width, 24)
		view := ansi.Strip(m.View())
		for _, line := range strings.Split(view, "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Fatalf("%d columns: line is %d wide: %q", width, w, line)
			}
		}
		head := lineContaining(t, view, "SEVERITY")
		if got, want := strings.Index(head, "RULE")-strings.Index(head, "SEVERITY"), len("SEVERITY")+cellGap; got != want {
			t.Errorf("%d columns: RULE starts %d columns after SEVERITY, want %d:\n%s", width, got, want, head)
		}
		crit := lineContaining(t, view, "  critical ")
		if want := "critical" + strings.Repeat(" ", cellGap) + "datastore-inaccessible"; !strings.Contains(crit, want) {
			t.Errorf("%d columns: critical row %q does not separate its columns by %d spaces", width, crit, cellGap)
		}
		if got, want := strings.Index(head, "OBJECT")-strings.Index(head, "RULE"), strings.Index(crit, "datastore/")-strings.Index(crit, "datastore-inaccessible"); got != want {
			t.Errorf("%d columns: OBJECT is not aligned under its heading", width)
		}
		if after := head[strings.Index(head, "OBJECT")+1:]; !strings.Contains(after, "MESSAGE") {
			t.Errorf("%d columns: MESSAGE heading missing:\n%s", width, head)
		}
	}
}

// Issue #303: the Health subtitle is 69 columns wide and was drawn untruncated.
func TestHealthSubtitleFitsTheWidth(t *testing.T) {
	for _, width := range columnWidthSizes {
		m := healthPaneModel(t, width, 24)
		line := lineContaining(t, ansi.Strip(m.View()), "all vCenters")
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("%d columns: subtitle is %d wide: %q", width, w, line)
		}
	}
}

// Issue #304: datastore search results show the datastore path, and a path
// wider than the NAME column lost its file name to a right-hand cut.
func TestDatastoreFindKeepsTheFileNameOfALongPath(t *testing.T) {
	const deep = "[nvme-01] backups/2026/finance/sql-prod-01/snapshots/sql-prod-01.vmdk"
	const deepDir = "[nvme-01] backups/2026/finance/sql-prod-01/snapshots/archive"
	for _, width := range columnWidthSizes {
		b := browsing()
		b.results = []vsphere.DatastoreEntry{
			file("sql-prod-01.vmdk", deep, 40<<30),
			dir("archive", deepDir),
		}
		m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
		m.backend = b
		m.width, m.height = width, 24
		openBrowser(t, m)
		press(t, m, "f")
		typeText(t, m, "sql-prod-01*")
		press(t, m, "enter")

		view := ansi.Strip(m.View())
		for _, line := range strings.Split(view, "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Fatalf("%d columns: line is %d wide: %q", width, w, line)
			}
		}
		row := lineContaining(t, view, "FILE")
		if !strings.Contains(row, "sql-prod-01.vmdk") {
			t.Errorf("%d columns: the file name was cut off: %q", width, row)
		}
		if !strings.Contains(row, deep[len("[nvme-01] "):]) && !strings.Contains(row, "…/") {
			t.Errorf("%d columns: a shortened path should start with an ellipsis and a slash: %q", width, row)
		}
		drow := lineContaining(t, view, "DIR")
		if !strings.Contains(drow, "archive/") {
			t.Errorf("%d columns: the folder name was cut off: %q", width, drow)
		}
	}
}

func TestPadDSPathKeepsAFolderName(t *testing.T) {
	for _, w := range []int{20, 30, 40} {
		got := padDSPath("backups/2026/finance/sql-prod-01/", w)
		if ansi.StringWidth(got) != w {
			t.Errorf("width %d: got %q (%d wide)", w, got, ansi.StringWidth(got))
		}
		if !strings.Contains(got, "sql-prod-01/") {
			t.Errorf("width %d: folder name lost: %q", w, got)
		}
	}
}

// TestTemplateNameFitsAndHostNameWidens keeps GUEST OS beside the template
// names instead of at the far edge of a wide terminal, and gives a host's
// FQDN room before STATE.
func TestTemplateNameFitsAndHostNameWidens(t *testing.T) {
	for _, width := range columnWidthSizes {
		monochrome(t)
		m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
		m.width, m.height = width, 24
		press(t, m, "2")
		view := ansi.Strip(m.View())
		head := lineContaining(t, view, "GUEST OS")
		name := "ubuntu-24.04-golden"
		if got, want := strings.Index(head, "GUEST OS"), strings.Index(head, "NAME")+len(name)+cellGap; got != want {
			t.Errorf("%d columns: GUEST OS at %d, want %d right after the widest name:\n%s", width, got, want, head)
		}

		press(t, m, "3")
		head = lineContaining(t, ansi.Strip(m.View()), "STATE")
		if gap := strings.Index(head, "STATE") - strings.Index(head, "NAME"); gap < len("esxi01.lab.local")+cellGap {
			t.Errorf("%d columns: host NAME is %d wide, too narrow for a lab FQDN:\n%s", width, gap-cellGap, head)
		}
	}
}
