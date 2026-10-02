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

// VMPerfSeries reads one VM's counters as time series, for the detail pane's
// charts. Unlike CollectPerf it reads exactly one VM in exactly one QueryPerf
// call and keeps every sample, because what it feeds is a chart rather than
// a stored summary. interval is explicit — RealtimePerfInterval or one of
// the historical roll-ups — and the window must fit in DefaultPerfMaxSamples
// samples at that interval. It is read-only.
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
	keys, err := resolveCounters(ctx, api)
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
	for _, c := range perf.Counters {
		if key, ok := keys[c.Metric]; ok {
			spec.MetricId = append(spec.MetricId, types.PerfMetricId{CounterId: key, Instance: ""})
		}
	}
	out, err := api.query(ctx, []types.PerfQuerySpec{spec})
	if err != nil {
		if isDenied(err) {
			return set, fmt.Errorf("permission denied reading performance data: %w", err)
		}
		return set, err
	}
	series := map[int32][]int64{}
	for _, base := range out {
		m, ok := base.(*types.PerfEntityMetric)
		if !ok || m.Entity.Value != vm.ID {
			continue
		}
		for _, v := range m.Value {
			if s, ok := v.(*types.PerfMetricIntSeries); ok && s.Id.Instance == "" {
				series[s.Id.CounterId] = s.Value
			}
		}
	}
	for _, c := range perf.Counters {
		key, offered := keys[c.Metric]
		raw := series[key]
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
