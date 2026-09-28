package report

import (
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/perf"
)

func fp(v float64) *float64 { return &v }

func performanceWindow(when time.Time) perf.Window {
	return perf.Window{
		Context: "prod", VCenterID: "vc-uuid", Endpoint: "https://vc.example", Source: "PerformanceManager.QueryPerf, historical interval 300s; synthetic",
		StartedAt: when, FinishedAt: when, WindowStart: when.Add(-24 * time.Hour), WindowEnd: when,
		IntervalSeconds: 300, ExpectedSamples: 288, RequestsUsed: 2, VMsRequested: 3, VMsSampled: 2, Status: perf.WindowPartial,
		VMs: []perf.VMResult{
			{MoRef: "vm-1", InstanceUUID: "instance", Name: "app", VCPU: 2, MemoryMB: 4096, Sampled: true, Signal: perf.SignalPeaksObserved, SignalReason: "low typical usage but peaks",
				Summaries: []perf.Summary{
					{Metric: perf.CPUUsage, Unit: perf.UnitPercent, Aggregation: "avg", Interval: 300, Expected: 288, Successful: 288, Average: fp(5), Peak: fp(90), P95: fp(20), Status: perf.StatusOK},
					{Metric: perf.MemActive, Unit: perf.UnitMiB, Aggregation: "avg", Interval: 300, Expected: 288, Missing: 288, Status: perf.StatusUnavailable, Reason: "permission denied"},
				}},
			{MoRef: "vm-9", InstanceUUID: "other", Name: "gone", VCPU: 1, MemoryMB: 1024, Signal: perf.SignalInsufficient, SignalReason: "not sampled: VM limit of 1 reached"},
		},
	}
}

func TestPerformanceRowsKeepUnknownEmptyAndExplainIt(t *testing.T) {
	when := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	data := sampleExportData(when)
	data.Performance = []perf.Window{performanceWindow(when)}
	rows := performanceRows(data)
	for _, row := range rows {
		if len(row) != len(performanceHeaders) {
			t.Fatalf("row has %d cells for %d headers: %v", len(row), len(performanceHeaders), row)
		}
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want cpu, unavailable memory, one unsampled VM", len(rows))
	}
	col := func(name string) int {
		for i, h := range performanceHeaders {
			if h == name {
				return i
			}
		}
		t.Fatalf("no column %q", name)
		return -1
	}
	cpu, mem, unsampled := rows[0], rows[1], rows[2]
	if cpu[col("Peak")] != 90.0 || cpu[col("Inventory match")] != "matched" || cpu[col("Sizing signal")] != "peaks-observed" {
		t.Fatalf("cpu row = %v", cpu)
	}
	for _, name := range []string{"Average", "Peak", "P95"} {
		if mem[col(name)] != nil {
			t.Fatalf("unavailable counter has %s = %v, want an empty cell, not zero", name, mem[col(name)])
		}
	}
	if mem[col("Status")] != "unavailable" || mem[col("Reason")] != "permission denied" {
		t.Fatalf("memory row = %v", mem)
	}
	if unsampled[col("Status")] != "not sampled" || unsampled[col("Inventory match")] != "not in this run" || unsampled[col("Average")] != nil {
		t.Fatalf("unsampled row = %v", unsampled)
	}
}

func TestPerformanceRowsSayNotCollected(t *testing.T) {
	data := sampleExportData(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	rows := performanceRows(data)
	for _, row := range rows {
		if len(row) != len(performanceHeaders) {
			t.Fatalf("row has %d cells for %d headers: %v", len(row), len(performanceHeaders), row)
		}
	}
	empty := performanceWindow(data.Run.StartedAt)
	empty.VMs = nil
	data.Performance = []perf.Window{empty}
	for _, row := range performanceRows(data) {
		if len(row) != len(performanceHeaders) {
			t.Fatalf("no-VMs row has %d cells for %d headers: %v", len(row), len(performanceHeaders), row)
		}
	}
	data.Performance = nil
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want a single not-collected row for the one context", len(rows))
	}
	if rows[0][0] != "prod" || rows[0][18] != "not collected" || rows[0][15] != nil || rows[0][16] != nil {
		t.Fatalf("row = %v", rows[0])
	}
}

func TestPerformanceMatchedByVCenterIdentityNotContextName(t *testing.T) {
	when := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	data := sampleExportData(when)
	w := performanceWindow(when)
	w.Context = "prod-old-name"
	data.Performance = []perf.Window{w}
	for _, row := range performanceRows(data) {
		if row[18] == "not collected" {
			t.Fatalf("a window for the same vCenter under another context name was reported as not collected: %v", row)
		}
	}
}

func TestPerformanceSheetIsDeterministicAndLeavesVCPUVMemoryUntouched(t *testing.T) {
	when := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	plain := sampleExportData(when)
	withPerf := sampleExportData(when)
	withPerf.Performance = []perf.Window{performanceWindow(when)}
	a, err := rvtoolsSheets(plain, healthReport(plain))
	if err != nil {
		t.Fatal(err)
	}
	b, err := rvtoolsSheets(withPerf, healthReport(withPerf))
	if err != nil {
		t.Fatal(err)
	}
	for i := range a {
		if a[i].name == performanceSheetName {
			continue
		}
		if len(a[i].rows) != len(b[i].rows) {
			t.Fatalf("collecting performance changed the row count of %s", a[i].name)
		}
		for r := range a[i].rows {
			for c := range a[i].rows[r] {
				if a[i].rows[r][c] != b[i].rows[r][c] {
					t.Fatalf("collecting performance changed %s row %d column %s", a[i].name, r, a[i].headers[c])
				}
			}
		}
	}
	first, err := RVToolsCSV(withPerf, healthReport(withPerf))
	if err != nil {
		t.Fatal(err)
	}
	second, _ := RVToolsCSV(withPerf, healthReport(withPerf))
	for i := range first {
		if string(first[i].Data) != string(second[i].Data) {
			t.Fatalf("%s is not deterministic", first[i].Name)
		}
	}
}

var _ = assessment.ExportData{}
