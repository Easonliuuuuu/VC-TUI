package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
)

// TestTrendsLeadsWithDatastoresThatHaveEvidence pins the Trends layout: the
// datastore that is running out ranks first with a truncated name, datastores
// with no growth data fold into one line, and the per-run table keeps its own
// header rather than sitting above unrelated rows.
func TestTrendsLeadsWithDatastoresThatHaveEvidence(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	m.width, m.height = 100, 40
	growth, days := 15.5e12, 3.0
	run := assessment.Run{ID: 7, StartedAt: time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC)}
	m.historyChurn = &assessment.ChurnTrend{Points: []assessment.ChurnPoint{{Run: run, VMCount: 12}}}
	m.historySnapshots = &assessment.SnapshotTrend{Points: []assessment.SnapshotTrendPoint{{Run: run}}}
	m.historyCapacityReport = &assessment.CapacityReport{Datastores: []assessment.DatastoreCapacity{
		{Object: assessment.Object{Name: "quiet-1"}},
		{Object: assessment.Object{Name: "HCI-Virtual-SAN-Datastore-1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"},
			UsedGrowthBytes: &growth, Projection: &assessment.CapacityProjection{DaysRemaining: &days}},
		{Object: assessment.Object{Name: "quiet-2"}},
	}}

	view := strings.Join(m.viewHistoryTrends(), "\n")
	for _, want := range []string{"DATASTORE", "in 3d", "HCI-Virtual-SAN-Datasto…", "+ 2 datastores without enough history", "#7"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Trends missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "quiet-1") {
		t.Fatalf("datastores without evidence should be folded, not listed:\n%s", view)
	}
}
