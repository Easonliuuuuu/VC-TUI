package perf

import (
	"fmt"
	"math"
	"sort"
)

// Status says whether a summary carries numbers.
type Status string

const (
	// StatusOK means the samples support an average and peak.
	StatusOK Status = "ok"
	// StatusInsufficient means samples were returned but too few, or too
	// sparse across the window, to summarise. No numbers are reported.
	StatusInsufficient Status = "insufficient-data"
	// StatusUnavailable means the counter could not be read at all: it was
	// denied, not collected at this statistics level, or the query failed.
	StatusUnavailable Status = "unavailable"
)

// Thresholds that decide whether samples support a statistic. They are
// deliberately conservative: a sizing signal built on too little history is
// worse than none.
const (
	// MinSummarySamples is the fewest successful samples for an average/peak.
	MinSummarySamples = 12
	// MinCoverage is the fewest successful/expected samples for an
	// average/peak. Powered-off periods count as missing, so a VM that ran
	// for under half the window is reported as insufficient rather than
	// summarised as if it had run throughout.
	MinCoverage = 0.5
	// MinPercentileSamples is the fewest successful samples for a 95th
	// percentile; with fewer, the percentile is just the maximum in disguise.
	MinPercentileSamples = 50
)

// Summary is the stored, bounded description of one counter over one window
// for one VM. Average, Peak and P95 are nil, never zero, when unknown.
type Summary struct {
	Metric      Metric   `json:"metric"`
	Unit        Unit     `json:"unit"`
	Aggregation string   `json:"aggregation"`
	Interval    int      `json:"interval_seconds"`
	Expected    int      `json:"expected_samples"`
	Successful  int      `json:"successful_samples"`
	Missing     int      `json:"missing_samples"`
	Average     *float64 `json:"average,omitempty"`
	Peak        *float64 `json:"peak,omitempty"`
	P95         *float64 `json:"p95,omitempty"`
	Status      Status   `json:"status"`
	Reason      string   `json:"reason,omitempty"`
}

// SummaryInput carries what Summarize needs to interpret raw samples.
type SummaryInput struct {
	Counter         Counter
	Raw             []int64
	Expected        int
	IntervalSeconds int
	VCPU            int32
}

// Summarize reduces raw samples to a Summary. Samples equal to NoData, or
// otherwise negative, are counted as missing and never enter a statistic.
// Peak is the largest interval average, not an instantaneous maximum.
func Summarize(in SummaryInput) Summary {
	s := Summary{
		Metric:      in.Counter.Metric,
		Unit:        in.Counter.Unit,
		Aggregation: in.Counter.Aggregation,
		Interval:    in.IntervalSeconds,
		Expected:    in.Expected,
	}
	values := make([]float64, 0, len(in.Raw))
	for _, raw := range in.Raw {
		if v, ok := Normalize(in.Counter, raw, in.IntervalSeconds, in.VCPU); ok {
			values = append(values, v)
		}
	}
	s.Successful = len(values)
	s.Missing = in.Expected - s.Successful
	if s.Missing < 0 {
		// More samples than the window predicted: the expectation, not the
		// data, was wrong, so do not report a negative gap.
		s.Missing = 0
		s.Expected = s.Successful
	}
	if s.Successful == 0 {
		s.Status = StatusInsufficient
		s.Reason = "vSphere returned no samples; the VM may have been powered off or history is not retained for this window"
		return s
	}
	coverage := float64(s.Successful) / float64(s.Expected)
	if s.Successful < MinSummarySamples || coverage < MinCoverage {
		s.Status = StatusInsufficient
		s.Reason = fmt.Sprintf("%d of %d expected samples returned; at least %d samples covering %.0f%% of the window are required",
			s.Successful, s.Expected, MinSummarySamples, MinCoverage*100)
		return s
	}
	sum, peak := 0.0, values[0]
	for _, v := range values {
		sum += v
		peak = math.Max(peak, v)
	}
	avg := sum / float64(len(values))
	s.Average, s.Peak = &avg, &peak
	s.Status = StatusOK
	if s.Successful >= MinPercentileSamples {
		p := percentile(values, 0.95)
		s.P95 = &p
	} else {
		s.Reason = fmt.Sprintf("95th percentile omitted: %d samples, %d required", s.Successful, MinPercentileSamples)
	}
	return s
}

// Unavailable builds the summary for a counter that could not be read.
func Unavailable(c Counter, interval, expected int, reason string) Summary {
	return Summary{
		Metric:      c.Metric,
		Unit:        c.Unit,
		Aggregation: c.Aggregation,
		Interval:    interval,
		Expected:    expected,
		Missing:     expected,
		Status:      StatusUnavailable,
		Reason:      reason,
	}
}

// percentile is the nearest-rank percentile of values (0 < p <= 1). It does
// not modify its input.
func percentile(values []float64, p float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	return sorted[rank]
}
