package vsphere

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/perf"
)

// Defaults for a performance collection. Every one is a bound: the command
// never issues an unbounded number of requests or keeps an unbounded number of
// rows.
const (
	DefaultPerfMaxSamples  = 400
	DefaultPerfMaxVMs      = 500
	DefaultPerfMaxRequests = 1000
	// DefaultPerfBatchVMs keeps metrics-per-request under vCenter's default
	// config.vpxd.stats.maxQueryMetrics of 64 (six counters x ten VMs).
	DefaultPerfBatchVMs = 10
)

// PerfOptions bound one performance collection.
type PerfOptions struct {
	// Window is how far back from Now to read.
	Window time.Duration
	// Interval is the historical roll-up interval in seconds. Zero selects the
	// finest interval whose retention still covers the whole window.
	Interval int
	// MaxSamples caps samples per counter per VM.
	MaxSamples int
	// MaxVMs caps how many VMs are queried; the rest are reported as not sampled.
	MaxVMs int
	// MaxRequests caps QueryPerf calls, including per-VM retries.
	MaxRequests int
	// BatchVMs is how many VMs one QueryPerf call covers.
	BatchVMs int
	// Now supplies the window end; tests inject a fixed clock.
	Now func() time.Time
	// TimeoutSeconds is recorded in the budget; the caller's context enforces it.
	TimeoutSeconds int
}

func (o PerfOptions) withDefaults() PerfOptions {
	if o.MaxSamples <= 0 {
		o.MaxSamples = DefaultPerfMaxSamples
	}
	if o.MaxVMs <= 0 {
		o.MaxVMs = DefaultPerfMaxVMs
	}
	if o.MaxRequests <= 0 {
		o.MaxRequests = DefaultPerfMaxRequests
	}
	if o.BatchVMs <= 0 {
		o.BatchVMs = DefaultPerfBatchVMs
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// CollectPerf reads performance history for vms from this vCenter. It is
// read-only and bounded by opts. The returned window carries no context or
// vCenter identity; the caller owns those.
func (c *Client) CollectPerf(ctx context.Context, vms []VM, opts PerfOptions) perf.Window {
	return collectPerf(ctx, perfClient{c}, c.About.FullVersion(), vms, opts)
}

func collectPerf(ctx context.Context, api perfAPI, about string, vms []VM, opts PerfOptions) perf.Window {
	opts = opts.withDefaults()
	end := opts.Now().UTC()
	w := perf.Window{
		StartedAt:   end,
		WindowStart: end.Add(-opts.Window),
		WindowEnd:   end,
		Budget: perf.Budget{
			MaxVMs: opts.MaxVMs, MaxRequests: opts.MaxRequests, MaxSamples: opts.MaxSamples,
			BatchVMs: opts.BatchVMs, TimeoutSeconds: opts.TimeoutSeconds,
		},
	}
	finish := func(status, msg string) perf.Window {
		w.Status, w.Error = status, msg
		w.FinishedAt = opts.Now().UTC()
		return w
	}
	if opts.Window <= 0 {
		return finish(perf.WindowFailed, "the collection window must be positive")
	}

	targets := make([]VM, 0, len(vms))
	for _, vm := range vms {
		if !vm.IsTemplate {
			targets = append(targets, vm)
		}
	}
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	w.VMsRequested = len(targets)

	counterKeys, err := resolveCounters(ctx, api)
	if err != nil {
		return finish(perf.WindowFailed, err.Error())
	}
	interval, err := chooseInterval(ctx, api, opts)
	if err != nil {
		return finish(perf.WindowFailed, err.Error())
	}
	w.IntervalSeconds = interval
	w.ExpectedSamples = int(opts.Window.Seconds()) / interval
	w.Source = fmt.Sprintf("PerformanceManager.QueryPerf, historical interval %ds; %s", interval, about)
	if w.ExpectedSamples < 1 {
		return finish(perf.WindowFailed, fmt.Sprintf("a %s window holds no %ds samples", opts.Window, interval))
	}
	if w.ExpectedSamples > opts.MaxSamples {
		return finish(perf.WindowFailed, fmt.Sprintf("a %s window at %ds intervals is %d samples per counter, over the limit of %d; shorten the window or raise the limit",
			opts.Window, interval, w.ExpectedSamples, opts.MaxSamples))
	}

	run := &perfRun{
		api: api, opts: opts, window: &w, keys: counterKeys, start: w.WindowStart, end: end,
		results: map[string]*perf.VMResult{},
	}
	for _, vm := range targets {
		run.results[vm.ID] = &perf.VMResult{
			MoRef: vm.ID, InstanceUUID: vm.InstanceUUID, Name: vm.Name, PowerState: vm.PowerState,
			VCPU: vm.CPU, MemoryMB: vm.MemoryMB,
		}
	}

	queryable := targets
	skipped := map[string]string{}
	if len(queryable) > opts.MaxVMs {
		for _, vm := range queryable[opts.MaxVMs:] {
			skipped[vm.ID] = fmt.Sprintf("not sampled: VM limit of %d reached", opts.MaxVMs)
		}
		queryable = queryable[:opts.MaxVMs]
	}
	run.collect(ctx, queryable, skipped)

	for _, vm := range targets {
		res := run.results[vm.ID]
		if reason, ok := skipped[vm.ID]; ok {
			res.Signal, res.SignalReason = perf.SignalInsufficient, reason
		} else {
			res.Sampled = true
			w.VMsSampled++
			res.Signal, res.SignalReason = perf.Classify(perf.ClassifyInput{Summaries: res.Summaries, MemoryMB: vm.MemoryMB})
		}
		w.VMs = append(w.VMs, *res)
	}

	anyData := false
	for _, res := range run.results {
		for _, s := range res.Summaries {
			anyData = anyData || s.Successful > 0
		}
	}
	switch {
	case len(targets) > 0 && (w.VMsSampled == 0 || (!anyData && len(run.errs) > 0)):
		return finish(perf.WindowFailed, run.errorSummary(len(skipped)))
	case len(skipped) > 0 || len(run.errs) > 0:
		return finish(perf.WindowPartial, run.errorSummary(len(skipped)))
	}
	return finish(perf.WindowSuccess, "")
}

type perfRun struct {
	api      perfAPI
	opts     PerfOptions
	window   *perf.Window
	keys     map[perf.Metric]int32
	start    time.Time
	end      time.Time
	results  map[string]*perf.VMResult
	requests int
	errs     []string
}

func (r *perfRun) errorSummary(skipped int) string {
	parts := append([]string(nil), r.errs...)
	if len(parts) > 5 {
		parts = append(parts[:5], fmt.Sprintf("and %d more errors", len(parts)-5))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d VMs were not sampled", skipped))
	}
	return strings.Join(parts, "; ")
}

// collect queries vms in batches, retrying a failed batch one VM at a time and
// stopping early on a permission denial or an exhausted budget. VMs it cannot
// reach are added to skipped with the reason.
func (r *perfRun) collect(ctx context.Context, vms []VM, skipped map[string]string) {
	for i := 0; i < len(vms); i += r.opts.BatchVMs {
		batch := vms[i:min(i+r.opts.BatchVMs, len(vms))]
		if reason := r.stopReason(ctx); reason != "" {
			r.skip(vms[i:], skipped, reason)
			return
		}
		out, err := r.query(ctx, batch)
		if err == nil {
			r.record(batch, out)
			continue
		}
		if isDenied(err) {
			r.fail(batch, err)
			r.errs = append(r.errs, "permission denied reading performance data: "+err.Error())
			if rest := vms[i+len(batch):]; len(rest) > 0 {
				for _, vm := range rest {
					skipped[vm.ID] = "not sampled: permission denied earlier in this collection"
				}
			}
			return
		}
		if len(batch) == 1 {
			r.fail(batch, err)
			r.errs = append(r.errs, fmt.Sprintf("%s: %v", batch[0].Name, err))
			continue
		}
		// One VM can poison a whole batch (for instance a counter it does not
		// offer), so isolate it rather than losing the others.
		for j, vm := range batch {
			if reason := r.stopReason(ctx); reason != "" {
				r.skip(append(append([]VM(nil), batch[j:]...), vms[i+len(batch):]...), skipped, reason)
				return
			}
			one := []VM{vm}
			out, err := r.query(ctx, one)
			if err != nil {
				r.fail(one, err)
				r.errs = append(r.errs, fmt.Sprintf("%s: %v", vm.Name, err))
				continue
			}
			r.record(one, out)
		}
	}
}

func (r *perfRun) stopReason(ctx context.Context) string {
	if ctx.Err() != nil {
		return "not sampled: collection timed out or was cancelled"
	}
	if r.requests >= r.opts.MaxRequests {
		return fmt.Sprintf("not sampled: request limit of %d reached", r.opts.MaxRequests)
	}
	return ""
}

func (r *perfRun) skip(vms []VM, skipped map[string]string, reason string) {
	for _, vm := range vms {
		skipped[vm.ID] = reason
	}
	r.errs = append(r.errs, reason)
}

func (r *perfRun) query(ctx context.Context, vms []VM) ([]types.BasePerfEntityMetricBase, error) {
	r.requests++
	r.window.RequestsUsed = r.requests
	specs := make([]types.PerfQuerySpec, 0, len(vms))
	start, end := r.start, r.end
	for _, vm := range vms {
		spec := types.PerfQuerySpec{
			Entity:     types.ManagedObjectReference{Type: "VirtualMachine", Value: vm.ID},
			StartTime:  &start,
			EndTime:    &end,
			MaxSample:  int32(r.window.ExpectedSamples),
			IntervalId: int32(r.window.IntervalSeconds),
			Format:     string(types.PerfFormatNormal),
		}
		for _, c := range perf.Counters {
			if key, ok := r.keys[c.Metric]; ok {
				// Instance "" is the VM-level aggregate across vCPUs.
				spec.MetricId = append(spec.MetricId, types.PerfMetricId{CounterId: key, Instance: ""})
			}
		}
		specs = append(specs, spec)
	}
	return r.api.query(ctx, specs)
}

func (r *perfRun) record(vms []VM, out []types.BasePerfEntityMetricBase) {
	byEntity := map[string]map[int32][]int64{}
	for _, base := range out {
		m, ok := base.(*types.PerfEntityMetric)
		if !ok {
			continue
		}
		series := map[int32][]int64{}
		for _, v := range m.Value {
			if s, ok := v.(*types.PerfMetricIntSeries); ok && s.Id.Instance == "" {
				series[s.Id.CounterId] = s.Value
			}
		}
		byEntity[m.Entity.Value] = series
	}
	for _, vm := range vms {
		res := r.results[vm.ID]
		series, entityReturned := byEntity[vm.ID]
		for _, c := range perf.Counters {
			key, offered := r.keys[c.Metric]
			var raw []int64
			if offered && entityReturned {
				raw = series[key]
			}
			switch {
			case !offered:
				res.Summaries = append(res.Summaries, perf.Unavailable(c, r.window.IntervalSeconds, r.window.ExpectedSamples,
					"this server does not offer the "+c.VSphereName()+" counter"))
			case len(raw) == 0:
				res.Summaries = append(res.Summaries, perf.Unavailable(c, r.window.IntervalSeconds, r.window.ExpectedSamples,
					c.VSphereName()+" returned no samples; it may not be collected at this statistics level or not permitted"))
			default:
				res.Summaries = append(res.Summaries, perf.Summarize(perf.SummaryInput{
					Counter: c, Raw: raw, Expected: r.window.ExpectedSamples,
					IntervalSeconds: r.window.IntervalSeconds, VCPU: vm.CPU,
				}))
			}
		}
	}
}

// fail records every counter of vms as unavailable with the error text.
func (r *perfRun) fail(vms []VM, err error) {
	for _, vm := range vms {
		res := r.results[vm.ID]
		res.Summaries = res.Summaries[:0]
		for _, c := range perf.Counters {
			res.Summaries = append(res.Summaries, perf.Unavailable(c, r.window.IntervalSeconds, r.window.ExpectedSamples, err.Error()))
		}
	}
}

func resolveCounters(ctx context.Context, api perfAPI) (map[perf.Metric]int32, error) {
	return resolveCounterKeys(ctx, api, perf.Counters)
}

// resolveCounterKeys maps each of counters to the numeric id this server
// gives it, leaving out any the server does not offer.
func resolveCounterKeys(ctx context.Context, api perfAPI, counters []perf.Counter) (map[perf.Metric]int32, error) {
	infos, err := api.counters(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]int32{}
	for _, info := range infos {
		if info.NameInfo == nil || info.GroupInfo == nil {
			continue
		}
		name := info.GroupInfo.GetElementDescription().Key + "." + info.NameInfo.GetElementDescription().Key + "." + string(info.RollupType)
		byName[name] = info.Key
	}
	keys := map[perf.Metric]int32{}
	for _, c := range counters {
		if key, ok := byName[c.VSphereName()]; ok {
			keys[c.Metric] = key
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("this server offers none of the requested performance counters")
	}
	return keys, nil
}

func chooseInterval(ctx context.Context, api perfAPI, opts PerfOptions) (int, error) {
	intervals, err := api.intervals(ctx)
	if err != nil {
		return 0, err
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].SamplingPeriod < intervals[j].SamplingPeriod })
	window := int32(opts.Window.Seconds())
	for _, iv := range intervals {
		if iv.SamplingPeriod <= 0 {
			continue
		}
		if opts.Interval > 0 && int(iv.SamplingPeriod) != opts.Interval {
			continue
		}
		if iv.Length >= window {
			return int(iv.SamplingPeriod), nil
		}
		if opts.Interval > 0 {
			return 0, fmt.Errorf("the %ds interval retains only %s of history, less than the requested %s window",
				iv.SamplingPeriod, time.Duration(iv.Length)*time.Second, opts.Window)
		}
	}
	if opts.Interval > 0 {
		return 0, fmt.Errorf("this server has no %ds historical interval", opts.Interval)
	}
	return 0, fmt.Errorf("no historical interval on this server retains a %s window", opts.Window)
}

func isDenied(err error) bool {
	var fault types.AnyType
	switch {
	case soap.IsSoapFault(err):
		fault = soap.ToSoapFault(err).VimFault()
	case soap.IsVimFault(err):
		fault = soap.ToVimFault(err)
	default:
		return false
	}
	switch fault.(type) {
	case types.NoPermission, *types.NoPermission:
		return true
	}
	return false
}
