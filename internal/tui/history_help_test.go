package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
)

func captureCapableModel(t *testing.T) *Model {
	t.Helper()
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store, Collector: &assessment.Collector{Store: store}}})
}

// TestChangesFooterFitsEightyColumnsAndKeepsHelpAndBack pins the key line the
// Changes pane used to lose its tail from: it ran past 100 columns, so the
// ellipsis swallowed "esc back" and "? help" first. Both variants are
// checked, because "n capture" is only offered when a capture can run.
func TestChangesFooterFitsEightyColumnsAndKeepsHelpAndBack(t *testing.T) {
	withCapture := captureCapableModel(t)
	storeOnly := newTestModel(t, twoHealthy(), Options{})
	for name, m := range map[string]*Model{"capture-capable": withCapture, "store-only": storeOnly} {
		m.width, m.height = 80, 24
		m.mode, m.historyPane = modeChanges, historyPaneChanges
		got := footerText(m)
		if strings.Contains(got, "…") {
			t.Errorf("%s: Changes footer is truncated at 80 columns: %q", name, got)
		}
		if w := ansi.StringWidth(got); w > 80 {
			t.Errorf("%s: Changes footer is %d columns wide: %q", name, w, got)
		}
		for _, want := range []string{"esc back", "? help", "c clip", "1-4 impact"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: Changes footer %q is missing %q", name, got, want)
			}
		}
	}
	if got := footerText(withCapture); !strings.Contains(got, "n capture") {
		t.Errorf("capture-capable footer %q lost the capture hint", got)
	}
}

// TestHelpFromTheHistoryHubDocumentsTheChangesKeys is the other half: the keys
// the footer no longer has room to name (b, t, s, R, e, N, p...) have to be
// readable somewhere, and "?" is where an operator looks.
func TestHelpFromTheHistoryHubDocumentsTheChangesKeys(t *testing.T) {
	m := captureCapableModel(t)
	m.width, m.height = 80, 24
	press(t, m, "H")
	if m.mode != modeChanges || m.historyPane != historyPaneChanges {
		t.Fatalf("H opened mode=%v pane=%d, want the Changes pane", m.mode, m.historyPane)
	}
	press(t, m, "?")
	if m.mode != modeHelp {
		t.Fatalf("? opened mode=%v, want help", m.mode)
	}

	view := ansi.Strip(m.View())
	for _, want := range []string{
		"Changes pane", "History hub", "move active end", "baseline", "target", "swap", "pick from all runs",
		"clip to same vCenters", "impact", "VM timeline", "capture", "back to browse",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("history help is missing %q:\n%s", want, view)
		}
	}
	for _, browseOnly := range []string{"Resource kinds", "Contexts screen", "diagnose", "all vCenters"} {
		if strings.Contains(view, browseOnly) {
			t.Errorf("history help still carries the browse section %q:\n%s", browseOnly, view)
		}
	}

	// It must fit the smallest terminal the help is designed for without
	// scrolling, and without clipping a description to an ellipsis.
	lines := strings.Split(view, "\n")
	if len(lines) != 24 {
		t.Fatalf("frame is %d lines, want 24", len(lines))
	}
	if limit := len(m.helpLines()) - m.bodyHeight(); limit > 0 {
		t.Errorf("history help overflows an 80x24 body by %d lines:\n%s", limit, view)
	}
	for _, line := range lines[2 : len(lines)-2] {
		if strings.Contains(line, "…") {
			t.Errorf("history help clips a description: %q", line)
		}
		if w := ansi.StringWidth(line); w > 80 {
			t.Errorf("help line is %d columns wide: %q", w, line)
		}
	}

	// Escape goes back to the pane it came from, not to the browse table.
	press(t, m, "esc")
	if m.mode != modeChanges || m.historyPane != historyPaneChanges {
		t.Fatalf("esc from help left mode=%v pane=%d, want the Changes pane", m.mode, m.historyPane)
	}
}

func TestHelpFromEachHistoryPaneListsThatPanesKeys(t *testing.T) {
	cases := []struct {
		pane int
		want []string
	}{
		{historyPaneTrends, []string{"Trends pane", "scroll up"}},
		{historyPaneRuns, []string{"Runs pane", "edit label", "edit note", "pin or unpin"}},
		{historyPaneHealth, []string{"Health pane", "scroll down"}},
	}
	for _, tc := range cases {
		m := captureCapableModel(t)
		m.width, m.height = 80, 24
		m.mode, m.historyPane = modeChanges, tc.pane
		press(t, m, "?")
		view := ansi.Strip(m.View())
		for _, want := range tc.want {
			if !strings.Contains(view, want) {
				t.Errorf("pane %d help is missing %q:\n%s", tc.pane, want, view)
			}
		}
		press(t, m, "esc")
		if m.mode != modeChanges || m.historyPane != tc.pane {
			t.Errorf("pane %d: esc from help left mode=%v pane=%d", tc.pane, m.mode, m.historyPane)
		}
	}
}

// TestHelpHidesCaptureWhenItCannotRun mirrors the footer: advertising a key
// that always fails is worse than leaving it out.
func TestHelpHidesCaptureWhenItCannotRun(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{})
	m.width, m.height = 80, 24
	m.mode = modeChanges
	press(t, m, "?")
	if view := ansi.Strip(m.View()); strings.Contains(view, "capture") {
		t.Errorf("a store-only session's help advertises capture:\n%s", view)
	}
}

// TestHelpFromBrowseIsUnchangedAndEscReturnsToTheScreen keeps the original
// help for every other screen, and checks esc no longer dumps a detail pane
// back onto the table.
func TestHelpFromBrowseIsUnchangedAndEscReturnsToTheScreen(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "?")
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Resource kinds") || strings.Contains(view, "Changes pane") {
		t.Errorf("browse help changed:\n%s", view)
	}
	press(t, m, "esc")
	if m.mode != modeBrowse {
		t.Fatalf("esc from browse help left mode=%v", m.mode)
	}

	press(t, m, "enter")
	if m.mode != modeDetail {
		t.Fatalf("enter opened mode=%v, want detail", m.mode)
	}
	press(t, m, "?", "esc")
	if m.mode != modeDetail {
		t.Fatalf("esc from help opened over a detail pane left mode=%v, want detail", m.mode)
	}
}
