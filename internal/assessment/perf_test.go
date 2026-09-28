package assessment

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/perf"
)

func f64(v float64) *float64 { return &v }

func testPerfWindow(contextName, vcenter string, end time.Time, status string) perf.Window {
	return perf.Window{
		Context: contextName, VCenterID: vcenter, Endpoint: "https://vc.example", Source: "test",
		StartedAt: end, FinishedAt: end, WindowStart: end.Add(-24 * time.Hour), WindowEnd: end,
		IntervalSeconds: 300, ExpectedSamples: 288, RequestsUsed: 2, VMsRequested: 2, VMsSampled: 2,
		Status: status, Budget: perf.Budget{MaxVMs: 10, MaxRequests: 20, MaxSamples: 400, BatchVMs: 10},
		VMs: []perf.VMResult{
			{MoRef: "vm-1", InstanceUUID: "uuid-1", Name: "web", PowerState: "poweredOn", VCPU: 4, MemoryMB: 4096, Sampled: true,
				Signal: perf.SignalPeaksObserved, SignalReason: "spiky", Summaries: []perf.Summary{
					{Metric: perf.CPUUsage, Unit: perf.UnitPercent, Aggregation: "avg", Interval: 300, Expected: 288, Successful: 288, Average: f64(5), Peak: f64(90), P95: f64(40), Status: perf.StatusOK},
					{Metric: perf.MemActive, Unit: perf.UnitMiB, Aggregation: "avg", Interval: 300, Expected: 288, Missing: 288, Status: perf.StatusUnavailable, Reason: "denied"},
				}},
			{MoRef: "vm-2", Name: "db", VCPU: 2, MemoryMB: 2048, Signal: perf.SignalInsufficient, SignalReason: "not sampled"},
		},
	}
}

func TestPerfWindowRoundTripKeepsUnknownAsNull(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	end := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	saved, err := store.SavePerfWindow(ctx, testPerfWindow("prod", "vc-1", end, perf.WindowPartial))
	if err != nil || saved.ID == 0 {
		t.Fatalf("save: %v id=%d", err, saved.ID)
	}
	got, err := store.LoadPerfWindow(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != perf.WindowPartial || got.IntervalSeconds != 300 || got.Budget.MaxRequests != 20 || !got.WindowEnd.Equal(end) || len(got.VMs) != 2 {
		t.Fatalf("window = %+v", got)
	}
	web := got.VMs[1]
	if web.Name != "web" {
		web = got.VMs[0]
	}
	if len(web.Summaries) != 2 || *web.Summaries[0].Peak != 90 || web.Summaries[0].P95 == nil {
		t.Fatalf("web summaries = %+v", web.Summaries)
	}
	if u := web.Summaries[1]; u.Average != nil || u.Peak != nil || u.P95 != nil || u.Status != perf.StatusUnavailable {
		t.Fatalf("unavailable summary came back with numbers: %+v", u)
	}
	for _, vm := range got.VMs {
		if vm.Name == "db" && (vm.Sampled || len(vm.Summaries) != 0) {
			t.Fatalf("unsampled VM = %+v", vm)
		}
	}
}

func TestLatestPerfWindowSkipsFailedAndMatchesIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	older, _ := store.SavePerfWindow(ctx, testPerfWindow("prod", "vc-1", base, perf.WindowSuccess))
	_, _ = store.SavePerfWindow(ctx, testPerfWindow("prod", "vc-1", base.Add(time.Hour), perf.WindowFailed))
	_, _ = store.SavePerfWindow(ctx, testPerfWindow("other", "vc-2", base.Add(2*time.Hour), perf.WindowSuccess))

	got, found, err := store.LatestPerfWindow(ctx, "renamed-context", "vc-1")
	if err != nil || !found || got.ID != older.ID {
		t.Fatalf("latest for vc-1 = %+v found=%v err=%v; a failed window must be skipped and identity must beat context name", got, found, err)
	}
	if _, found, _ := store.LatestPerfWindow(ctx, "nothing", "vc-9"); found {
		t.Fatal("a window was found for an unknown vCenter")
	}
}

func TestPruneRemovesOldPerfWindowsButSparesNewestPerContext(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	old1, _ := store.SavePerfWindow(ctx, testPerfWindow("prod", "vc-1", now.Add(-200*24*time.Hour), perf.WindowSuccess))
	old2, _ := store.SavePerfWindow(ctx, testPerfWindow("prod", "vc-1", now.Add(-100*24*time.Hour), perf.WindowSuccess))
	lone, _ := store.SavePerfWindow(ctx, testPerfWindow("lab", "vc-2", now.Add(-300*24*time.Hour), perf.WindowSuccess))

	dry, err := store.Prune(ctx, 30*24*time.Hour, 0, false)
	if err != nil || dry.PerfWindows != 1 || dry.PerfDeleted != 0 {
		t.Fatalf("dry run = %+v err=%v", dry, err)
	}
	res, err := store.Prune(ctx, 30*24*time.Hour, 0, true)
	if err != nil || res.PerfDeleted != 1 {
		t.Fatalf("execute = %+v err=%v", res, err)
	}
	ids := map[int64]bool{}
	windows, _ := store.PerfWindows(ctx, "", 10)
	for _, w := range windows {
		ids[w.ID] = true
	}
	if ids[old1.ID] || !ids[old2.ID] || !ids[lone.ID] {
		t.Fatalf("remaining windows = %v, want the newest per context (%d, %d) only", ids, old2.ID, lone.ID)
	}
	var orphans int
	if err := store.db.QueryRow(`SELECT count(*) FROM perf_vms WHERE window_id=?`, old1.ID).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("pruned window left %d VM rows (err=%v): cascade is not applied", orphans, err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM perf_summaries WHERE perf_vm_id NOT IN (SELECT id FROM perf_vms)`).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("orphaned summaries = %d (err=%v)", orphans, err)
	}
}

func TestPerfSchemaIsAdditiveOnAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE perf_summaries; DROP TABLE perf_vms; DROP TABLE perf_windows; PRAGMA user_version = 4`); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = Open(path)
	if err != nil {
		t.Fatalf("reopen at v4: %v", err)
	}
	defer store.Close()
	if _, err := store.SavePerfWindow(context.Background(), testPerfWindow("prod", "vc-1", time.Now(), perf.WindowSuccess)); err != nil {
		t.Fatalf("perf tables not created by migration: %v", err)
	}
}
