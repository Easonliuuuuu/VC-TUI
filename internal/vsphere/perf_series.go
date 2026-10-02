package vsphere

import (
	"context"
	"fmt"
	"time"

	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/perf"
)

// RealtimePerfInterval is the 20-second sampling ESXi keeps for about an
// hour. It is not one of the server's historical intervals, so it is never
// offered by chooseInterval; a caller asks for it explicitly.
const RealtimePerfInterval = 20

// VMPerfSeries reads one VM's dashboard counters (perf.DashboardCounters) as
// time series, for the detail pane's charts. Unlike CollectPerf it reads
// exactly one VM in exactly one QueryPerf call and keeps every sample,
// because what it feeds is a chart rather than a stored summary. interval is
// explicit — RealtimePerfInterval or one of the historical roll-ups — and
// the window must fit in DefaultPerfMaxSamples samples at that interval. It
// is read-only.
func (c *Client) VMPerfSeries(ctx context.Context, vm VM, window time.Duration, interval int, now time.Time) (perf.SeriesSet, error) {
	return collectVMSeries(ctx, perfClient{c}, vm, window, interval, now)
}

func collectVMSeries(ctx context.Context, api perfAPI, vm VM, window time.Duration, interval int, now time.Time) (perf.SeriesSet, error) {
	end := now.UTC()
	set := perf.SeriesSet{IntervalSeconds: interval, WindowStart: end.Add(-window), WindowEnd: end}
	if window <= 0 || interval <= 0 {
		return set, fmt.Errorf("a performance window and interval must be positive")
	}
	expected := int(window.Seconds()) / interval
	if expected < 1 || expected > DefaultPerfMaxSamples {
		return set, fmt.Errorf("a %s window at %ds intervals is %d samples per counter, outside 1 to %d", window, interval, expected, DefaultPerfMaxSamples)
	}
	keys, err := resolveCounterKeys(ctx, api, perf.DashboardCounters)
	if err != nil {
		return set, err
	}
	start := set.WindowStart
	spec := types.PerfQuerySpec{
		Entity:     types.ManagedObjectReference{Type: "VirtualMachine", Value: vm.ID},
		StartTime:  &start,
		EndTime:    &end,
		MaxSample:  int32(expected),
		IntervalId: int32(interval),
		Format:     string(types.PerfFormatNormal),
	}
	for _, c := range perf.DashboardCounters {
		key, ok := keys[c.Metric]
		if !ok {
			continue
		}
		// Instance "" is the VM-level aggregate; "*" asks for every
		// instance as well, which an additive counter falls back on.
		instance := ""
		if perf.SumsInstances(c.Metric) {
			instance = "*"
		}
		spec.MetricId = append(spec.MetricId, types.PerfMetricId{CounterId: key, Instance: instance})
	}
	out, err := api.query(ctx, []types.PerfQuerySpec{spec})
	if err != nil {
		if isDenied(err) {
			return set, fmt.Errorf("permission denied reading performance data: %w", err)
		}
		return set, err
	}
	aggregate := map[int32][]int64{}
	instances := map[int32][][]int64{}
	for _, base := range out {
		m, ok := base.(*types.PerfEntityMetric)
		if !ok || m.Entity.Value != vm.ID {
			continue
		}
		for _, v := range m.Value {
			s, ok := v.(*types.PerfMetricIntSeries)
			if !ok {
				continue
			}
			if s.Id.Instance == "" {
				aggregate[s.Id.CounterId] = s.Value
			} else {
				instances[s.Id.CounterId] = append(instances[s.Id.CounterId], s.Value)
			}
		}
	}
	for _, c := range perf.DashboardCounters {
		key, offered := keys[c.Metric]
		raw := aggregate[key]
		if len(raw) == 0 && perf.SumsInstances(c.Metric) {
			raw = sumInstances(instances[key])
		}
		switch {
		case !offered:
			set.Series = append(set.Series, perf.UnavailableSeries(c, interval, expected, "this server does not offer the "+c.VSphereName()+" counter"))
		case len(raw) == 0:
			set.Series = append(set.Series, perf.UnavailableSeries(c, interval, expected,
				c.VSphereName()+" returned no samples; it may not be collected at this statistics level, not permitted, or the VM was off"))
		default:
			set.Series = append(set.Series, perf.NewSeries(c, raw, expected, interval, vm.CPU))
		}
	}
	set.Signal, set.SignalReason = perf.Classify(perf.ClassifyInput{Summaries: set.Summaries(), MemoryMB: vm.MemoryMB})
	return set, nil
}

// sumInstances adds per-device series sample by sample. A sample is
// missing (NoData) only when every instance is missing it; otherwise the
// missing instances simply contribute nothing.
func sumInstances(series [][]int64) []int64 {
	n := 0
	for _, s := range series {
		n = max(n, len(s))
	}
	if n == 0 {
		return nil
	}
	out := make([]int64, n)
	for i := range out {
		sum, any := int64(0), false
		for _, s := range series {
			if i < len(s) && s[i] >= 0 {
				sum += s[i]
				any = true
			}
		}
		if !any {
			sum = perf.NoData
		}
		out[i] = sum
	}
	return out
}
