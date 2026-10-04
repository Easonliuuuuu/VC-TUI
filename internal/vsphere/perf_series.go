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

// VMSeriesResult is one VM's share of a VMsPerfSeries read: its series, or
// why it has none.
type VMSeriesResult struct {
	VM  VM
	Set perf.SeriesSet
	Err error
}

// MaxVMsPerfSeries bounds one VMsPerfSeries call, so a very large vApp
// cannot turn one keypress into hundreds of queries.
const MaxVMsPerfSeries = 64

// VMsPerfSeries reads the dashboard counters of several VMs, for a view that
// charts a group of them (a vApp's members). It asks for as many VMs per
// QueryPerf call as vCenter allows: realtime queries are not subject to
// config.vpxd.stats.maxQueryMetrics, historical ones are, so a historical
// batch stays under its default of 64 metrics. A batch that fails is retried
// one VM at a time, so one VM's error does not cost the others their charts.
// Results are in the order of vms. It is read-only.
func (c *Client) VMsPerfSeries(ctx context.Context, vms []VM, window time.Duration, interval int, now time.Time) ([]VMSeriesResult, error) {
	return collectVMsSeries(ctx, perfClient{c}, vms, window, interval, now)
}

func collectVMSeries(ctx context.Context, api perfAPI, vm VM, window time.Duration, interval int, now time.Time) (perf.SeriesSet, error) {
	out, err := collectVMsSeries(ctx, api, []VM{vm}, window, interval, now)
	if err != nil {
		return perf.SeriesSet{IntervalSeconds: interval, WindowStart: now.UTC().Add(-window), WindowEnd: now.UTC()}, err
	}
	return out[0].Set, out[0].Err
}

// seriesBatchVMs is how many VMs one QueryPerf call covers at interval.
func seriesBatchVMs(interval int) int {
	if interval == RealtimePerfInterval {
		return MaxVMsPerfSeries
	}
	return max(1, DefaultMaxQueryMetrics/len(perf.DashboardCounters))
}

// DefaultMaxQueryMetrics is vCenter's default config.vpxd.stats.maxQueryMetrics:
// the most metrics one historical QueryPerf call may ask for.
const DefaultMaxQueryMetrics = 64

func collectVMsSeries(ctx context.Context, api perfAPI, vms []VM, window time.Duration, interval int, now time.Time) ([]VMSeriesResult, error) {
	end := now.UTC()
	template := perf.SeriesSet{IntervalSeconds: interval, WindowStart: end.Add(-window), WindowEnd: end}
	if window <= 0 || interval <= 0 {
		return nil, fmt.Errorf("a performance window and interval must be positive")
	}
	expected := int(window.Seconds()) / interval
	if expected < 1 || expected > DefaultPerfMaxSamples {
		return nil, fmt.Errorf("a %s window at %ds intervals is %d samples per counter, outside 1 to %d", window, interval, expected, DefaultPerfMaxSamples)
	}
	if len(vms) > MaxVMsPerfSeries {
		return nil, fmt.Errorf("%d VMs is more than the %d one chart read covers", len(vms), MaxVMsPerfSeries)
	}
	keys, err := resolveCounterKeys(ctx, api, perf.DashboardCounters)
	if err != nil {
		return nil, err
	}
	out := make([]VMSeriesResult, len(vms))
	for i, vm := range vms {
		out[i] = VMSeriesResult{VM: vm, Set: template}
	}
	q := seriesQuery{api: api, keys: keys, template: template, expected: expected, interval: interval}
	size := seriesBatchVMs(interval)
	for i := 0; i < len(vms); i += size {
		batch := out[i:min(i+size, len(out))]
		err := q.read(ctx, batch)
		switch {
		case err == nil:
			continue
		case isDenied(err):
			// The account cannot read performance data at all; asking again
			// VM by VM would only be refused again.
			return nil, fmt.Errorf("permission denied reading performance data: %w", err)
		case len(batch) == 1:
			batch[0].Err = err
			continue
		}
		for j := range batch {
			if err := q.read(ctx, batch[j:j+1]); err != nil {
				batch[j].Err = err
			}
		}
	}
	return out, nil
}

// seriesQuery is one VMsPerfSeries read's shared request parameters.
type seriesQuery struct {
	api      perfAPI
	keys     map[perf.Metric]int32
	template perf.SeriesSet
	expected int
	interval int
}

// read fills each result in batch from one QueryPerf call.
func (q seriesQuery) read(ctx context.Context, batch []VMSeriesResult) error {
	start, end := q.template.WindowStart, q.template.WindowEnd
	specs := make([]types.PerfQuerySpec, 0, len(batch))
	for _, r := range batch {
		spec := types.PerfQuerySpec{
			Entity:     types.ManagedObjectReference{Type: "VirtualMachine", Value: r.VM.ID},
			StartTime:  &start,
			EndTime:    &end,
			MaxSample:  int32(q.expected),
			IntervalId: int32(q.interval),
			Format:     string(types.PerfFormatNormal),
		}
		for _, c := range perf.DashboardCounters {
			key, ok := q.keys[c.Metric]
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
		specs = append(specs, spec)
	}
	res, err := q.api.query(ctx, specs)
	if err != nil {
		return err
	}
	aggregate := map[string]map[int32][]int64{}
	instances := map[string]map[int32][][]int64{}
	for _, base := range res {
		m, ok := base.(*types.PerfEntityMetric)
		if !ok {
			continue
		}
		id := m.Entity.Value
		if aggregate[id] == nil {
			aggregate[id], instances[id] = map[int32][]int64{}, map[int32][][]int64{}
		}
		for _, v := range m.Value {
			s, ok := v.(*types.PerfMetricIntSeries)
			if !ok {
				continue
			}
			if s.Id.Instance == "" {
				aggregate[id][s.Id.CounterId] = s.Value
			} else {
				instances[id][s.Id.CounterId] = append(instances[id][s.Id.CounterId], s.Value)
			}
		}
	}
	for i := range batch {
		vm := batch[i].VM
		set := q.template
		for _, c := range perf.DashboardCounters {
			key, offered := q.keys[c.Metric]
			raw := aggregate[vm.ID][key]
			if len(raw) == 0 && perf.SumsInstances(c.Metric) {
				raw = sumInstances(instances[vm.ID][key])
			}
			switch {
			case !offered:
				set.Series = append(set.Series, perf.UnavailableSeries(c, q.interval, q.expected, "this server does not offer the "+c.VSphereName()+" counter"))
			case len(raw) == 0:
				set.Series = append(set.Series, perf.UnavailableSeries(c, q.interval, q.expected,
					c.VSphereName()+" returned no samples; it may not be collected at this statistics level, not permitted, or the VM was off"))
			default:
				set.Series = append(set.Series, perf.NewSeries(c, raw, q.expected, q.interval, vm.CPU))
			}
		}
		set.Signal, set.SignalReason = perf.Classify(perf.ClassifyInput{Summaries: set.Summaries(), MemoryMB: vm.MemoryMB})
		batch[i].Set, batch[i].Err = set, nil
	}
	return nil
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
