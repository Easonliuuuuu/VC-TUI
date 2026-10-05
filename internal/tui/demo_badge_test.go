package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestDemoBadgeOnEveryScreenHeader pins the rule from AGENTS.md that
// presentation fixtures stay visibly synthetic: every screen's header, not
// just browse and detail, carries the demo badge, and it still fits on one
// line at the widths the testbed renders.
func TestDemoBadgeOnEveryScreenHeader(t *testing.T) {
	type variant struct {
		name  string
		setup func(m *Model)
	}
	var variants []variant
	for md := modeBrowse; md <= modeSwitchPGDetail; md++ {
		variants = append(variants, variant{name: "mode", setup: func(m *Model) { m.mode = md }})
	}
	for _, pane := range []int{historyPaneChanges, historyPaneTrends, historyPaneRuns, historyPaneHealth} {
		variants = append(variants, variant{name: "history pane", setup: func(m *Model) {
			m.mode, m.historyPane = modeChanges, pane
		}})
	}
	variants = append(variants,
		variant{name: "search with query", setup: func(m *Model) {
			m.mode = modeSearch
			m.filter.SetValue("web")
		}},
		variant{name: "timeline with entity", setup: func(m *Model) {
			m.mode, m.timelineQuery = modeHistoryTimeline, "billing"
		}},
	)

	for _, width := range []int{60, 80, 100} {
		for i, v := range variants {
			for _, demo := range []bool{true, false} {
				m := newTestModel(t, twoHealthy(), Options{Current: "prod", Demo: demo})
				m.width, m.height = width, 30
				v.setup(m)
				got := m.viewHeader()
				if strings.Contains(got, "\n") {
					t.Fatalf("%s #%d at %d: header spans lines: %q", v.name, i, width, got)
				}
				if w := ansi.StringWidth(got); w > width {
					t.Fatalf("%s #%d at %d: header is %d columns wide: %q", v.name, i, width, w, got)
				}
				if has := strings.Contains(ansi.Strip(got), demoBadge); has != demo {
					t.Fatalf("%s #%d (mode %v) at %d, demo=%v: badge present=%v in %q", v.name, i, m.mode, width, demo, has, ansi.Strip(got))
				}
			}
		}
	}
}

// TestDemoBadgeKeepsHistoryAndSearchLabels checks the badge does not cost the
// header its own name: the screen label must survive beside it.
func TestDemoBadgeKeepsHistoryAndSearchLabels(t *testing.T) {
	for _, width := range []int{60, 80, 100} {
		m := newTestModel(t, twoHealthy(), Options{Current: "prod", Demo: true})
		m.width, m.height = width, 30
		for _, c := range []struct {
			setup func()
			label string
		}{
			{func() { m.mode, m.historyPane = modeChanges, historyPaneChanges }, "history  ·  Changes"},
			{func() { m.mode = modeSearch; m.filter.SetValue("") }, "search"},
		} {
			c.setup()
			first, _, _ := strings.Cut(m.View(), "\n")
			first = ansi.Strip(first)
			if !strings.Contains(first, demoBadge) || !strings.Contains(first, c.label) {
				t.Fatalf("width %d: want badge and %q on the header line, got %q", width, c.label, first)
			}
		}
	}
}
