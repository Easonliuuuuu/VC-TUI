package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func TestToolsDetailUsesInstallationEvidence(t *testing.T) {
	for _, tc := range []struct{ state, version, want string }{
		{"guestToolsNotRunning", "guestToolsNotInstalled", "not installed"},
		{"guestToolsNotRunning", "guestToolsCurrent", "not running"},
		{"guestToolsRunning", "guestToolsCurrent", "running"},
		{"guestToolsExecutingScripts", "guestToolsCurrent", "executing scripts"},
		{"", "", "-"},
	} {
		vm := vsphere.VM{PowerState: "poweredOn", ToolsState: tc.state, ToolsVersionStatus: tc.version}
		r := vmRow(vm, false)
		m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
		for _, width := range []int{60, 80, 100, 140} {
			m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
			lines, spans := m.vmPropertyLines(r, false)
			for i, f := range r.detail {
				if f.label != "VMware Tools" {
					continue
				}
				if f.value != tc.want {
					t.Fatalf("Tools display=%q, want %q", f.value, tc.want)
				}
				span := spans[i+2]
				text := ansi.Strip(strings.Join(lines[span.start:span.end+1], "\n"))
				if strings.Contains(text, "guestTools") || (tc.want == "not installed" && strings.Contains(text, "not running")) {
					t.Fatalf("contradictory Tools display: %s", text)
				}
				if strings.Count(strings.Join(strings.Fields(text), " "), tc.want) != 1 {
					t.Fatalf("Tools repeats its warning: %s", text)
				}
			}
		}
	}
}

func TestHistoryAndBrowseSharePowerWords(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	m.changeDiff = &assessment.Diff{Target: assessment.Run{ID: 2}}
	v := assessment.VMChange{After: &assessment.Observation{VM: vsphere.VM{PowerState: "poweredOn"}}, Fields: []assessment.FieldChange{
		{Field: "power_state", Before: "poweredOff", After: "poweredOn"},
		{Field: "tools_state", Before: "guestToolsNotRunning", After: "guestToolsRunning"},
	}}
	view := ansi.Strip(strings.Join(m.vmInspectorFields(v), "\n"))
	if !strings.Contains(view, "off → on") || !strings.Contains(view, "not running → running") || strings.Contains(view, "poweredOn") || strings.Contains(view, "guestTools") {
		t.Fatalf("History vocabulary diverged: %s", view)
	}
	if got := changeDetail(v); got != "power_state:off→on tools_state:not running→running" {
		t.Fatalf("change stream: %s", got)
	}
}

func TestOperatorCountsIntervalsAndPicker(t *testing.T) {
	for _, tc := range []struct {
		duration time.Duration
		want     string
	}{
		{28 * 24 * time.Hour, "28d"}, {25 * time.Hour, "1d 1h"}, {2 * time.Hour, "2h"}, {121 * time.Minute, "2h 1m"}, {-28 * 24 * time.Hour, "28d"},
	} {
		if got := formatInterval(tc.duration); got != tc.want {
			t.Fatalf("interval %s=%q want %q", tc.duration, got, tc.want)
		}
	}
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "tab")
	typeText(t, m, "app-01")
	if view := ansi.Strip(m.viewSearchHeader()); !strings.Contains(view, "1 match in 1 vCenter") {
		t.Fatalf("singular search: %s", view)
	}
	press(t, m, "esc")
	press(t, m, "R")
	press(t, m, "tab")
	m.filter.SetValue("app-01")
	if view := ansi.Strip(m.viewSearchHeader()); !strings.Contains(view, "2 matches in 2 vCenters") {
		t.Fatalf("plural search: %s", view)
	}
	m.baseRun, m.targetRun, m.scrubHandle = 1, 2, "t"
	m.runs = []assessment.Run{{ID: 2}, {ID: 1}}
	picker := ansi.Strip(strings.Join(m.viewScrubber(100, false), "\n"))
	if !strings.Contains(picker, "t") || strings.Contains(picker, "T") || m.handleMark(1) != "b" || m.handleMark(2) != "t" {
		t.Fatalf("picker must match b/t keys: %s", picker)
	}
}
