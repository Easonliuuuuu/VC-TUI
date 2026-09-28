package perf

import (
	"math"
	"strings"
	"testing"
)

func counter(t *testing.T, m Metric) Counter {
	t.Helper()
	c, ok := CounterFor(m)
	if !ok {
		t.Fatalf("no counter for %s", m)
	}
	return c
}

func series(n int, v int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestNormalizeUnits(t *testing.T) {
	cases := []struct {
		name     string
		metric   Metric
		raw      int64
		interval int
		vcpu     int32
		want     float64
		ok       bool
	}{
		{"cpu usage is hundredths of a percent", CPUUsage, 2550, 300, 4, 25.5, true},
		{"cpu ready is per-vCPU percent of the interval", CPUReady, 30000, 300, 2, 5, true},
		{"memory KB to MiB", MemActive, 2048, 300, 1, 2, true},
		{"no data is never zero", CPUUsage, NoData, 300, 4, 0, false},
		{"any negative is no data", MemActive, -5, 300, 4, 0, false},
		{"ready needs a vCPU count", CPUReady, 1000, 300, 0, 0, false},
		{"ready needs an interval", CPUReady, 1000, 0, 2, 0, false},
		{"a real zero is a measurement", CPUUsage, 0, 300, 4, 0, true},
	}
	for _, tc := range cases {
		got, ok := Normalize(counter(t, tc.metric), tc.raw, tc.interval, tc.vcpu)
		if ok != tc.ok || (ok && !near(got, tc.want)) {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSummarizeMissingSamplesAreNotZero(t *testing.T) {
	raw := append(series(20, 1000), series(20, NoData)...)
	s := Summarize(SummaryInput{Counter: counter(t, CPUUsage), Raw: raw, Expected: 40, IntervalSeconds: 300})
	if s.Status != StatusOK || s.Successful != 20 || s.Missing != 20 {
		t.Fatalf("summary = %+v", s)
	}
	if !near(*s.Average, 10) {
		t.Fatalf("average = %v, want 10: missing samples must not dilute the mean", *s.Average)
	}
}

func TestSummarizeInsufficientReportsNoNumbers(t *testing.T) {
	cases := map[string]SummaryInput{
		"no samples":      {Counter: counter(t, CPUUsage), Raw: series(50, NoData), Expected: 50, IntervalSeconds: 300},
		"too few samples": {Counter: counter(t, CPUUsage), Raw: series(5, 1000), Expected: 5, IntervalSeconds: 300},
		"sparse coverage": {Counter: counter(t, CPUUsage), Raw: append(series(20, 1000), series(80, NoData)...), Expected: 100, IntervalSeconds: 300},
	}
	for name, in := range cases {
		s := Summarize(in)
		if s.Status != StatusInsufficient {
			t.Errorf("%s: status = %s, want insufficient", name, s.Status)
		}
		if s.Average != nil || s.Peak != nil || s.P95 != nil {
			t.Errorf("%s: numbers reported for insufficient data: %+v", name, s)
		}
		if s.Reason == "" {
			t.Errorf("%s: no reason recorded", name)
		}
	}
}

func TestSummarizePercentileGatedOnSampleCount(t *testing.T) {
	few := Summarize(SummaryInput{Counter: counter(t, CPUUsage), Raw: series(20, 1000), Expected: 20, IntervalSeconds: 300})
	if few.Status != StatusOK || few.P95 != nil || !strings.Contains(few.Reason, "95th percentile omitted") {
		t.Fatalf("20 samples: %+v", few)
	}
	raw := make([]int64, 100)
	for i := range raw {
		raw[i] = int64(i+1) * 100
	}
	many := Summarize(SummaryInput{Counter: counter(t, CPUUsage), Raw: raw, Expected: 100, IntervalSeconds: 300})
	if many.P95 == nil || !near(*many.P95, 95) {
		t.Fatalf("p95 = %v, want nearest-rank 95", many.P95)
	}
	if !near(*many.Peak, 100) {
		t.Fatalf("peak = %v", *many.Peak)
	}
}

func TestUnavailableCarriesNoNumbers(t *testing.T) {
	s := Unavailable(counter(t, MemActive), 300, 288, "NoPermission")
	if s.Status != StatusUnavailable || s.Average != nil || s.Peak != nil || s.Missing != 288 {
		t.Fatalf("summary = %+v", s)
	}
}

func summaries(t *testing.T, cpu, mem []int64, extra ...Summary) []Summary {
	t.Helper()
	out := []Summary{
		Summarize(SummaryInput{Counter: counter(t, CPUUsage), Raw: cpu, Expected: len(cpu), IntervalSeconds: 300, VCPU: 4}),
		Summarize(SummaryInput{Counter: counter(t, MemActive), Raw: mem, Expected: len(mem), IntervalSeconds: 300, VCPU: 4}),
	}
	return append(out, extra...)
}

// A VM that is idle now but spikes briefly and periodically must not be read
// as oversized from its latest sample.
func TestClassifyPeriodicPeakIsNotSustainedLow(t *testing.T) {
	const n = 288 // one day of 5-minute samples
	cpu, mem := series(n, 500), series(n, 400*1024)
	for i := 40; i < n; i += 96 { // brief peak, three times
		cpu[i], cpu[i+1] = 9000, 9000
		mem[i], mem[i+1] = 3500*1024, 3500*1024
	}
	if cpu[n-1] != 500 {
		t.Fatalf("fixture: the current sample must look idle, got %d", cpu[n-1])
	}
	sig, reason := Classify(ClassifyInput{Summaries: summaries(t, cpu, mem), MemoryMB: 4096})
	if sig != SignalPeaksObserved {
		t.Fatalf("signal = %s (%s), want %s", sig, reason, SignalPeaksObserved)
	}
}

func TestClassifySustainedLowNeedsSupportedSamples(t *testing.T) {
	sig, _ := Classify(ClassifyInput{Summaries: summaries(t, series(288, 500), series(288, 400*1024)), MemoryMB: 4096})
	if sig != SignalSustainedLow {
		t.Fatalf("signal = %s, want %s", sig, SignalSustainedLow)
	}
}

func TestClassifyNeverLowOnMissingEvidence(t *testing.T) {
	memOK := series(288, 400*1024)
	cases := map[string]struct {
		in   ClassifyInput
		want Signal
	}{
		"too few samples":        {ClassifyInput{Summaries: summaries(t, series(4, 100), memOK), MemoryMB: 4096}, SignalInsufficient},
		"powered off all window": {ClassifyInput{Summaries: summaries(t, series(288, NoData), memOK), MemoryMB: 4096}, SignalInsufficient},
		"denied counter": {ClassifyInput{Summaries: []Summary{
			Summarize(SummaryInput{Counter: counter(t, CPUUsage), Raw: series(288, 500), Expected: 288, IntervalSeconds: 300}),
			Unavailable(counter(t, MemActive), 300, 288, "permission denied"),
		}, MemoryMB: 4096}, SignalUnavailable},
		"counter never collected":   {ClassifyInput{Summaries: summaries(t, series(288, 500), memOK)[:1], MemoryMB: 4096}, SignalUnavailable},
		"unknown configured memory": {ClassifyInput{Summaries: summaries(t, series(288, 500), memOK)}, SignalInsufficient},
	}
	for name, tc := range cases {
		if sig, reason := Classify(tc.in); sig != tc.want {
			t.Errorf("%s: signal = %s (%s), want %s", name, sig, reason, tc.want)
		}
	}
}

func TestClassifyContentionOutranksLowUtilisation(t *testing.T) {
	ready := Summarize(SummaryInput{Counter: counter(t, CPUReady), Raw: append(series(287, 3000), 90000), Expected: 288, IntervalSeconds: 300, VCPU: 4})
	sig, reason := Classify(ClassifyInput{Summaries: summaries(t, series(288, 500), series(288, 400*1024), ready), MemoryMB: 4096})
	if sig != SignalContention {
		t.Fatalf("signal = %s (%s), want %s", sig, reason, SignalContention)
	}
	balloon := Summarize(SummaryInput{Counter: counter(t, MemBalloon), Raw: series(288, 512*1024), Expected: 288, IntervalSeconds: 300})
	if sig, _ := Classify(ClassifyInput{Summaries: summaries(t, series(288, 500), series(288, 400*1024), balloon), MemoryMB: 4096}); sig != SignalContention {
		t.Fatalf("ballooning signal = %s, want %s", sig, SignalContention)
	}
}

func TestClassifyInUse(t *testing.T) {
	sig, _ := Classify(ClassifyInput{Summaries: summaries(t, series(288, 5000), series(288, 3000*1024)), MemoryMB: 4096})
	if sig != SignalInUse {
		t.Fatalf("signal = %s, want %s", sig, SignalInUse)
	}
}
