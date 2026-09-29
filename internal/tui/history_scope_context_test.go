package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

var errUnavailableTrends = errors.New("trend evidence unavailable")

// allKindsCollected records a successful collection for every kind a run
// persists, which is what makes a stored run count as complete.
func allKindsCollected() []assessment.CollectionResult {
	kinds := []string{"vm", "host", "cluster", "datastore", "resourcepool", "dvswitch", "network", "snapshot"}
	out := make([]assessment.CollectionResult, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, assessment.CollectionResult{Kind: kind, Status: "success"})
	}
	return out
}

// scopedVM builds one VM observation belonging to a named vCenter, so a
// fixture can hold evidence for two estates at once.
func scopedVM(contextName, vcenterID, name string) assessment.Observation {
	return assessment.Observation{VCenterID: vcenterID, Context: contextName, VM: vsphere.VM{
		ID: "vm-" + name, Name: name, InstanceUUID: "uuid-" + name,
		PowerState: "poweredOn", Host: "esx-01", CPU: 2, MemoryMB: 4096,
	}}
}

// twoContextStore records two runs over two vCenters. Only customer-a changes
// between them: one of its VMs vanishes. prod is identical in both runs, so
// any change attributed to prod is evidence of contexts being pooled.
func twoContextStore(t *testing.T) *assessment.Store {
	t.Helper()
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	prod := &config.Context{Name: "prod", Endpoint: "https://prod", Username: "user"}
	customer := &config.Context{Name: "customer-a", Endpoint: "https://customer-a", Username: "user"}
	// Trends reads complete assessments only, so every persisted kind has to
	// be recorded for the run to finish as complete rather than partial.
	collected := allKindsCollected()
	customerVMs := [][]assessment.Observation{
		{scopedVM("customer-a", "vc-cust", "cust-keeper"), scopedVM("customer-a", "vc-cust", "cust-doomed")},
		{scopedVM("customer-a", "vc-cust", "cust-keeper")},
	}
	when := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		run, err := store.StartRun(ctx, "test", []*config.Context{prod, customer}, when)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveContext(ctx, run.ID, assessment.ContextResult{
			Name: "prod", VCenterID: "vc-prod", Status: "success",
			VMs:         []assessment.Observation{scopedVM("prod", "vc-prod", "billing")},
			Collections: collected,
		}, when); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveContext(ctx, run.ID, assessment.ContextResult{
			Name: "customer-a", VCenterID: "vc-cust", Status: "success",
			VMs:         customerVMs[i],
			Collections: collected,
		}, when); err != nil {
			t.Fatal(err)
		}
		if _, err := store.FinishRun(ctx, run.ID, when); err != nil {
			t.Fatal(err)
		}
		when = when.Add(time.Hour)
	}
	return store
}

// TestHistoryScopesToTheSelectedVCenter is the whole point of scoping the
// hub: a pane opened under one vCenter must answer for that vCenter. Pooling
// every stored estate into one line meant the operator read another site's
// churn as their own, and a run that failed to reach a site read as a fleet
// -wide drop rather than as the missing evidence it was.
func TestHistoryScopesToTheSelectedVCenter(t *testing.T) {
	store := twoContextStore(t)

	// prod is unchanged across both runs, so a correctly scoped Changes pane
	// has nothing to report and Trends counts only prod's single VM.
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
	press(t, m, "H")
	if rows := m.scopeRows(); len(rows) != 0 {
		t.Fatalf("prod-scoped Changes reported another vCenter's changes: %+v", rows)
	}
	if m.historyChurn == nil || len(m.historyChurn.Points) == 0 {
		t.Fatal("prod-scoped Trends loaded no points")
	}
	for _, p := range m.historyChurn.Points {
		if p.VMCount != 1 {
			t.Fatalf("prod-scoped Trends counted %d VMs, want only prod's 1", p.VMCount)
		}
		if p.Vanished != 0 {
			t.Fatalf("prod-scoped Trends counted %d vanished, want customer-a's loss excluded", p.Vanished)
		}
	}
	if label := strings.Join(m.viewHistoryTrends(), "\n"); !strings.Contains(label, "prod") {
		t.Fatalf("Trends header does not name the scope it answers for:\n%s", label)
	}

	// customer-a is where the change actually happened.
	m = newTestModel(t, twoHealthy(), Options{Current: "customer-a", Assessment: &assessment.Service{Store: store}})
	press(t, m, "H")
	rows := m.scopeRows()
	if len(rows) != 1 || rows[0].object != "cust-doomed" {
		t.Fatalf("customer-a-scoped Changes did not report its own vanished VM: %+v", rows)
	}
	for _, p := range m.historyChurn.Points {
		if p.VMCount > 2 {
			t.Fatalf("customer-a-scoped Trends counted %d VMs, want at most its own 2", p.VMCount)
		}
	}
}

// TestHistoryAllContextsScopePoolsTheEstate pins the other half of the rule:
// the all-vCenters view is exactly where pooling is the right answer.
func TestHistoryAllContextsScopePoolsTheEstate(t *testing.T) {
	store := twoContextStore(t)
	m := newTestModel(t, twoHealthy(), Options{AllContexts: true, Assessment: &assessment.Service{Store: store}})
	press(t, m, "H")

	if rows := m.scopeRows(); len(rows) != 1 || rows[0].object != "cust-doomed" {
		t.Fatalf("estate-wide Changes lost the vanished VM: %+v", rows)
	}
	if m.historyChurn == nil || len(m.historyChurn.Points) == 0 {
		t.Fatal("estate-wide Trends loaded no points")
	}
	if got := m.historyChurn.Points[0].VMCount; got != 3 {
		t.Fatalf("estate-wide Trends counted %d VMs, want both vCenters' 3", got)
	}
	if label := strings.Join(m.viewHistoryTrends(), "\n"); !strings.Contains(label, "all vCenters") {
		t.Fatalf("estate-wide Trends header does not say so:\n%s", label)
	}
}

// TestHistoryExplainsAVCenterWithNothingStored pins the empty case. The store
// rejects a selector it never recorded, which is right for a typed CLI flag
// but is not a mistake here: the operator simply has not captured the vCenter
// they are looking at, and the pane has to say that rather than show a raw
// "unknown assessment context" error.
func TestHistoryExplainsAVCenterWithNothingStored(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	prod := &config.Context{Name: "prod", Endpoint: "https://prod", Username: "user"}
	when := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		run, err := store.StartRun(ctx, "test", []*config.Context{prod}, when)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveContext(ctx, run.ID, assessment.ContextResult{
			Name: "prod", VCenterID: "vc-prod", Status: "success",
			VMs:         []assessment.Observation{scopedVM("prod", "vc-prod", "billing")},
			Collections: allKindsCollected(),
		}, when); err != nil {
			t.Fatal(err)
		}
		if _, err := store.FinishRun(ctx, run.ID, when); err != nil {
			t.Fatal(err)
		}
		when = when.Add(time.Hour)
	}

	// customer-a is configured but has never been captured.
	m := newTestModel(t, twoHealthy(), Options{Current: "customer-a", Assessment: &assessment.Service{Store: store}})
	press(t, m, "H")

	if m.historyErr == nil || !strings.Contains(m.historyErr.Error(), "customer-a") {
		t.Fatalf("Changes did not explain the empty scope by name: %v", m.historyErr)
	}
	if strings.Contains(m.historyErr.Error(), "unknown assessment context") {
		t.Fatalf("Changes leaked the store's selector error: %v", m.historyErr)
	}
	trends := strings.Join(m.viewHistoryTrends(), "\n")
	if !strings.Contains(trends, "customer-a") {
		t.Fatalf("Trends did not explain the empty scope:\n%s", trends)
	}
}

// TestTrendsFailureLeavesTheChangesPaneAlone pins the pane boundary. Trends
// and Changes read different evidence under one scope, so a scope with no
// complete assessments must empty Trends without putting an error banner over
// the comparison Changes can still make from partial runs.
func TestTrendsFailureLeavesTheChangesPaneAlone(t *testing.T) {
	store := twoContextStore(t)
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
	press(t, m, "H")

	m.historyTrendsErr = errUnavailableTrends
	if m.historyErr != nil {
		t.Fatalf("a Trends failure set the Changes pane's error: %v", m.historyErr)
	}
	if view := strings.Join(m.viewChanges(), "\n"); strings.Contains(view, errUnavailableTrends.Error()) {
		t.Fatalf("Changes rendered the Trends error:\n%s", view)
	}
}

// TestScopedChangesDefaultsToRunsThatReachedTheContext: the newest runs cover
// only customer-a, so a prod-scoped pane must open on prod's own newest pair
// rather than on the newest runs overall (which prod was never part of), and
// must never draw an unset run as "#0" or the zero time.
func TestScopedChangesDefaultsToRunsThatReachedTheContext(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	prod := &config.Context{Name: "prod", Endpoint: "https://prod", Username: "user"}
	customer := &config.Context{Name: "customer-a", Endpoint: "https://customer-a", Username: "user"}
	collected := allKindsCollected()
	when := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	var ids []int64
	for i, withProd := range []bool{true, true, false, false} {
		requested := []*config.Context{customer}
		if withProd {
			requested = []*config.Context{prod, customer}
		}
		run, err := store.StartRun(ctx, "test", requested, when)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
		if withProd {
			if err := store.SaveContext(ctx, run.ID, assessment.ContextResult{
				Name: "prod", VCenterID: "vc-prod", Status: "success",
				VMs:         []assessment.Observation{scopedVM("prod", "vc-prod", "billing")},
				Collections: collected,
			}, when); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.SaveContext(ctx, run.ID, assessment.ContextResult{
			Name: "customer-a", VCenterID: "vc-cust", Status: "success",
			VMs:         []assessment.Observation{scopedVM("customer-a", "vc-cust", "cust-"+string(rune('a'+i)))},
			Collections: collected,
		}, when); err != nil {
			t.Fatal(err)
		}
		if _, err := store.FinishRun(ctx, run.ID, when); err != nil {
			t.Fatal(err)
		}
		when = when.Add(time.Hour)
	}

	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
	press(t, m, "H")
	if m.historyErr != nil {
		t.Fatalf("scoped Changes errored instead of using prod's own runs: %v", m.historyErr)
	}
	if m.baseRun != ids[0] || m.targetRun != ids[1] {
		t.Fatalf("span = %d→%d, want prod's newest pair %d→%d", m.baseRun, m.targetRun, ids[0], ids[1])
	}
	if got := historyRunLabel(0); got != "—" {
		t.Fatalf("unset run label = %q, want —", got)
	}
	if out := strings.Join(m.viewChanges(), "\n"); strings.Contains(out, "#0") || strings.Contains(out, "01 Jan 08:06") {
		t.Fatalf("Changes rendered a zero-value end:\n%s", out)
	}
}
