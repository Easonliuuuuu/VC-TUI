package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/health"
)

// healthFixture is shaped like the demo estate that exposed the problem: a
// long run of informational findings stored ahead of the few critical ones,
// no finding carrying a confidence, and rule and object names as long as the
// real rules produce.
func healthFixture() *health.Report {
	var findings []health.Finding
	for i := 0; i < 30; i++ {
		findings = append(findings, health.Finding{
			Rule: "custom-resource-allocation", Category: health.CategoryMigration, Severity: health.SeverityInfo,
			Object:  health.Object{Kind: "vm", Name: fmt.Sprintf("app-%02d", i)},
			Message: "VM carries a custom resource allocation",
		})
	}
	for i := 0; i < 3; i++ {
		findings = append(findings, health.Finding{
			Rule: "cdrom-connected", Category: health.CategoryMigration, Severity: health.SeverityWarning,
			Object:  health.Object{Kind: "vm", Name: fmt.Sprintf("db-%02d", i)},
			Message: "CD-ROM is connected",
		})
	}
	findings = append(findings, health.Finding{
		Rule: "datastore-inaccessible", Category: health.CategoryCapacity, Severity: health.SeverityCritical,
		Object:  health.Object{Kind: "datastore", Name: "vsan-archive-01"},
		Message: "datastore is not accessible from any host",
	})
	return &health.Report{
		RunID:    12,
		Findings: findings,
		Counts:   health.Counts{Info: 30, Warning: 3, Critical: 1, Total: 34},
	}
}

func healthPaneModel(t *testing.T, width, height int) *Model {
	t.Helper()
	m := newTestModel(t, twoHealthy(), Options{})
	m.width, m.height = width, height
	m.mode, m.historyPane = modeChanges, historyPaneHealth
	m.historyHealth = healthFixture()
	return m
}

// TestHealthPaneListsCriticalFindingsFirst pins the sort: the stored report
// puts informational findings first, and the pane scrolls, so before this the
// one critical finding was a screen and a half below the fold.
func TestHealthPaneListsCriticalFindingsFirst(t *testing.T) {
	m := healthPaneModel(t, 100, 30)
	var severities []string
	for _, line := range m.viewHistoryHealth() {
		fields := strings.Fields(ansi.Strip(line))
		if len(fields) > 0 && (fields[0] == "critical" || fields[0] == "warning" || fields[0] == "info") {
			severities = append(severities, fields[0])
		}
	}
	if len(severities) < 5 {
		t.Fatalf("expected findings in the first screen, got %v", severities)
	}
	want := []string{"critical", "warning", "warning", "warning", "info"}
	for i, w := range want {
		if severities[i] != w {
			t.Fatalf("finding rows start %v, want %v", severities[:len(want)], want)
		}
	}
}

// TestHealthFindingsKeepReportOrderWithinASeverity is the stable half of the
// sort: ties keep the order the report was stored in.
func TestHealthFindingsKeepReportOrderWithinASeverity(t *testing.T) {
	r := healthFixture()
	got := sortFindingsBySeverity(r.Findings)
	if r.Findings[0].Severity != health.SeverityInfo {
		t.Fatal("sorting reordered the model's own report")
	}
	var infos []string
	for _, f := range got {
		if f.Severity == health.SeverityInfo {
			infos = append(infos, f.Object.Name)
		}
	}
	for i, name := range infos {
		if want := fmt.Sprintf("app-%02d", i); name != want {
			t.Fatalf("info finding %d is %q, want %q (order within a severity must be stable)", i, name, want)
		}
	}
}

// TestHealthPaneKeepsObjectAndCriticalCountAt80Columns pins the layout at the
// smallest terminal the interface is designed for: OBJECT must stay, the
// empty CONFIDENCE column must not be drawn, and the summary must not be cut
// before the critical count.
func TestHealthPaneKeepsObjectAndCriticalCountAt80Columns(t *testing.T) {
	m := healthPaneModel(t, 80, 24)
	view := ansi.Strip(m.View())
	for _, line := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(line); w > 80 {
			t.Fatalf("line is %d columns wide: %q", w, line)
		}
	}
	for _, want := range []string{"OBJECT", "datastore/vsan-archive-0", "1 critical", "3 warning"} {
		if !strings.Contains(view, want) {
			t.Errorf("80x24 health pane is missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "CONFIDENCE") {
		t.Errorf("a column with no values is still drawn:\n%s", view)
	}
}

// TestHealthColumnsShrinkBeforeObjectGoes walks the column ladder directly.
func TestHealthColumnsShrinkBeforeObjectGoes(t *testing.T) {
	findings := healthFixture().Findings
	findings[0].Confidence = health.ConfidenceVerified
	titles := func(width int) string {
		var out []string
		for _, c := range healthColumns(findings, width) {
			out = append(out, c.title)
		}
		return strings.Join(out, " ")
	}
	if got := titles(160); !strings.Contains(got, "CONFIDENCE") || !strings.Contains(got, "CATEGORY") {
		t.Errorf("a wide terminal should draw every column, got %q", got)
	}
	for _, width := range []int{60, 70, 80, 100} {
		got := titles(width)
		if !strings.Contains(got, "OBJECT") || !strings.Contains(got, "MESSAGE") {
			t.Errorf("width %d lost OBJECT or MESSAGE: %q", width, got)
		}
	}
	if got := titles(80); strings.Contains(got, "CONFIDENCE") || strings.Contains(got, "CATEGORY") {
		t.Errorf("80 columns should drop CONFIDENCE and CATEGORY first, got %q", got)
	}
}
