package tui

import (
	"strings"
	"testing"
)

// The key hints belong on the last row of the frame on every history pane,
// including the empty ones whose body is only a line or two.
func TestHistoryPanesKeepFooterOnBottomRow(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{})
	m.width, m.height = 100, 30
	m.mode = modeChanges
	for pane := 0; pane < historyPaneCount; pane++ {
		m.historyPane = pane
		if got := len(strings.Split(m.View(), "\n")); got != m.height {
			t.Errorf("history pane %d renders %d lines, want %d", pane, got, m.height)
		}
	}
}
