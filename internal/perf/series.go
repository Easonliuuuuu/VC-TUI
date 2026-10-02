package perf

import (
	"math"
	"time"
)

// Series is one counter's samples over a window, oldest first, in the
// counter's normalised unit. It is what a chart draws, where Summary is what
// a report keeps. A sample vSphere had no reading for is NaN, never zero, for
// the same reason Normalize refuses NoData: a gap is missing evidence, not a
// measurement of idleness.
type Series struct {
	Metric Metric
	Unit   Unit
	Values []float64
	// Summary is the same bounded description a stored window would carry,
	// so a chart's average and peak are gated exactly as a report's are.
	Summary Summary
}

// SeriesSet is one VM's counters over one window.
type SeriesSet struct {
	IntervalSeconds int
	WindowStart     time.Time
	WindowEnd       time.Time
	// Series holds one entry per counter, in Counters order. A counter that
	// could not be read has no Values and an unavailable Summary.
	Series       []Series
	Signal       Signal
	SignalReason string
}

// Get returns the series for one metric.
func (s SeriesSet) Get(m Metric) (Series, bool) {
	for _, series := range s.Series {
		if series.Metric == m {
			return series, true
		}
	}
	return Series{}, false
}

// Summaries returns each series' summary, in order, for Classify.
func (s SeriesSet) Summaries() []Summary {
	out := make([]Summary, len(s.Series))
	for i, series := range s.Series {
		out[i] = series.Summary
	}
	return out
}

// NewSeries normalises raw samples into a Series and summarises them.
// expected and intervalSeconds have SummaryInput's meaning.
func NewSeries(c Counter, raw []int64, expected, intervalSeconds int, vcpu int32) Series {
	values := make([]float64, len(raw))
	for i, r := range raw {
		v, ok := Normalize(c, r, intervalSeconds, vcpu)
		if !ok {
			v = math.NaN()
		}
		values[i] = v
	}
	return Series{
		Metric: c.Metric,
		Unit:   c.Unit,
		Values: values,
		Summary: Summarize(SummaryInput{
			Counter: c, Raw: raw, Expected: expected, IntervalSeconds: intervalSeconds, VCPU: vcpu,
		}),
	}
}

// UnavailableSeries is the Series for a counter that could not be read.
func UnavailableSeries(c Counter, interval, expected int, reason string) Series {
	return Series{Metric: c.Metric, Unit: c.Unit, Summary: Unavailable(c, interval, expected, reason)}
}
