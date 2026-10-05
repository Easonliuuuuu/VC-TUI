package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
)

func TestPartialTrendsExplainsStoredCoverage(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	when := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		run, err := store.StartRun(ctx, "synthetic", []*config.Context{{Name: "prod"}, {Name: "dr-site"}}, when)
		if err != nil {
			t.Fatal(err)
		}
		err = store.SaveContext(ctx, run.ID, assessment.ContextResult{Name: "prod", VCenterID: "vc-prod", Status: "success", VMs: []assessment.Observation{scopedVM("prod", "vc-prod", "billing")}, Collections: allKindsCollected()}, when)
		if err != nil {
			t.Fatal(err)
		}
		err = store.SaveContext(ctx, run.ID, assessment.ContextResult{Name: "dr-site", Status: "error", Error: "synthetic unreachable site"}, when)
		if err != nil {
			t.Fatal(err)
		}
		finished, err := store.FinishRun(ctx, run.ID, when)
		if err != nil || finished.Status != assessment.RunPartial {
			t.Fatalf("run not partial: %+v %v", finished, err)
		}
		when = when.Add(time.Hour)
	}
	service := &assessment.Service{Store: store}
	var last *Model
	for _, all := range []bool{false, true} {
		m := newTestModel(t, twoHealthy(), Options{Current: "prod", AllContexts: all, Assessment: service})
		press(t, m, "H")
		last = m
		m.historyPane = historyPaneTrends
		for _, width := range []int{60, 80, 100, 140} {
			m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
			view := ansi.Strip(strings.Join(m.viewHistoryTrends(), "\n"))
			for _, want := range []string{"All 5 stored assessments are partial.", "Incomplete VM coverage: dr-site", "capture a complete"} {
				if !strings.Contains(view, want) {
					t.Fatalf("all=%v width=%d missing %q:\n%s", all, width, want, view)
				}
			}
			for _, line := range strings.Split(view, "\n") {
				if ansi.StringWidth(line) > width {
					t.Fatalf("overflow: %q", line)
				}
			}
		}
		if m.historyChurn != nil && len(m.historyChurn.Points) > 0 {
			t.Fatal("partial runs became complete trend points")
		}
	}
	if lines := trendEmptyExplanation(ctx, service, []string{"never-recorded"}); len(lines) != 0 {
		t.Fatalf("unrelated partial runs explained an unknown scope: %v", lines)
	}
	last.demo = true
	if view := strings.Join(last.viewHistoryTrends(), "\n"); !strings.Contains(view, "Demo history is fixed") || strings.Contains(view, "press n") {
		t.Fatalf("demo advertised an unavailable capture: %s", view)
	}
	last.demo = false
	service.Collector = &assessment.Collector{Store: store}
	if view := strings.Join(last.viewHistoryTrends(), "\n"); !strings.Contains(view, "press n") {
		t.Fatalf("capture-capable session omitted recovery key: %s", view)
	}
	service.Collector = nil
	// A complete capture makes the pane useful and clears the old empty reason.
	run, err := store.StartRun(ctx, "synthetic", []*config.Context{{Name: "prod"}}, when)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(ctx, run.ID, assessment.ContextResult{Name: "prod", VCenterID: "vc-prod", Status: "success", VMs: []assessment.Observation{scopedVM("prod", "vc-prod", "billing")}, Collections: allKindsCollected()}, when); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, run.ID, when); err != nil {
		t.Fatal(err)
	}
	m := last
	drive(t, m, loadHistoryTrendsCmd(ctx, service, m.historyScope()))
	if len(m.historyTrendsEmpty) > 0 || m.historyChurn == nil || len(m.historyChurn.Points) != 1 {
		t.Fatal("complete capture did not replace the empty state")
	}
}
