package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/perf"
)

func syntheticPerfCounter(t *testing.T, m perf.Metric) perf.Counter {
	t.Helper()
	c, ok := perf.CounterFor(m)
	if !ok {
		t.Fatalf("no counter %s", m)
	}
	return c
}

// seedPerformance stores a synthetic window for "prod": one VM that is idle
// in its latest sample but peaks briefly and periodically, one whose memory
// counter was denied, and one with too little history. "edge" gets none.
func seedPerformance(t *testing.T, dbPath string) {
	t.Helper()
	store, err := assessment.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const n = 288
	cpu, mem := make([]int64, n), make([]int64, n)
	for i := range cpu {
		cpu[i], mem[i] = 500, 400*1024
		if i%96 < 2 {
			cpu[i] = 9000
		}
	}
	summarize := func(m perf.Metric, raw []int64) perf.Summary {
		return perf.Summarize(perf.SummaryInput{Counter: syntheticPerfCounter(t, m), Raw: raw, Expected: n, IntervalSeconds: 300, VCPU: 4})
	}
	spiky := []perf.Summary{summarize(perf.CPUUsage, cpu), summarize(perf.MemActive, mem)}
	denied := []perf.Summary{summarize(perf.CPUUsage, cpu), perf.Unavailable(syntheticPerfCounter(t, perf.MemActive), 300, n, "permission denied")}
	sparse := []perf.Summary{summarize(perf.CPUUsage, cpu[:3]), summarize(perf.MemActive, mem[:3])}
	end := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	w := perf.Window{
		Context: "prod", VCenterID: "vc-prod", Endpoint: "https://prod.example", Source: "synthetic test data",
		StartedAt: end, FinishedAt: end, WindowStart: end.Add(-24 * time.Hour), WindowEnd: end, IntervalSeconds: 300, ExpectedSamples: n,
		RequestsUsed: 2, VMsRequested: 3, VMsSampled: 3, Status: perf.WindowSuccess,
	}
	for _, vm := range []struct {
		name string
		sums []perf.Summary
	}{{"spiky", spiky}, {"denied", denied}, {"sparse", sparse}} {
		sig, reason := perf.Classify(perf.ClassifyInput{Summaries: vm.sums, MemoryMB: 4096})
		w.VMs = append(w.VMs, perf.VMResult{MoRef: "vm-" + vm.name, Name: vm.name, VCPU: 4, MemoryMB: 4096, Sampled: true, Signal: sig, SignalReason: reason, Summaries: vm.sums})
	}
	if _, err := store.SavePerfWindow(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}

func mustAssessment(t *testing.T, dbPath string, args ...string) string {
	t.Helper()
	out, errOut, err := runAssessment(t, dbPath, args...)
	if err != nil {
		t.Fatalf("assessment %v: %v\nstderr: %s", args, err, errOut)
	}
	return out
}

func TestPerfShowDoesNotInferSizingFromOneSampleOrMissingData(t *testing.T) {
	dbPath := newTopologyTestHistoryDB(t)
	seedPerformance(t, dbPath)
	out := mustAssessment(t, dbPath, "perf", "show", "latest")
	lines := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 1 {
			lines[f[0]] = line
		}
	}
	if !strings.Contains(lines["spiky"], "peaks-observed") {
		t.Errorf("spiky VM must not read as idle from its last sample: %q", lines["spiky"])
	}
	if !strings.Contains(lines["denied"], "unavailable") || strings.Contains(strings.Fields(lines["denied"])[5], "0.0") {
		t.Errorf("denied counter must be unavailable, not zero: %q", lines["denied"])
	}
	if !strings.Contains(lines["sparse"], "insufficient-data") {
		t.Errorf("three samples must not support a signal: %q", lines["sparse"])
	}
	for _, name := range []string{"spiky", "denied", "sparse"} {
		if strings.Contains(lines[name], "sustained-low") {
			t.Errorf("%s was called sustained-low: %q", name, lines[name])
		}
	}
}

func TestAssessmentReportStatesWhichContextsHavePerformanceHistory(t *testing.T) {
	dbPath := newTopologyTestHistoryDB(t)
	seedPerformance(t, dbPath)
	out := mustAssessment(t, dbPath, "report")
	if !strings.Contains(out, "prod: window #1 success") || !strings.Contains(out, "edge: not collected") {
		t.Fatalf("report:\n%s", out)
	}
	var report struct {
		Performance []assessment.PerfSummary `json:"performance"`
	}
	if err := json.Unmarshal([]byte(mustAssessment(t, dbPath, "report", "-o", "json")), &report); err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, p := range report.Performance {
		status[p.Context] = p.Status
	}
	if status["prod"] != "success" || status["edge"] != "not collected" {
		t.Fatalf("performance = %+v", report.Performance)
	}
}
