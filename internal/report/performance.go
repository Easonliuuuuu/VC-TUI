package report

import (
	"sort"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/perf"
)

// performanceSheetName is vsfleet's own worksheet for VM performance history.
// It is deliberately separate from vCPU and vMemory, which keep their
// RVTools-compatible configured-size columns.
const performanceSheetName = "vsfleetPerformance"

var performanceHeaders = []string{
	"Context", "vCenter ID", "VM", "VM ID", "VM UUID", "Inventory match", "Counter", "Unit", "Aggregation",
	"Window start", "Window end", "Interval s", "Expected samples", "Successful samples", "Missing samples",
	"Average", "Peak", "P95", "Status", "Reason", "Sizing signal", "Signal reason", "Collection status", "Source",
}

// performanceDateCols are the Window start and Window end columns.
var performanceDateCols = []int{9, 10}

func floatCell(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// performanceRows renders one row per VM per counter. Unknown statistics are
// empty cells, never zero. A context with no collection gets a single
// "not collected" row so an absent window cannot be read as an idle estate.
func performanceRows(data assessment.ExportData) [][]any {
	type key struct{ context, moref string }
	inventory := make(map[key]string, len(data.VMs))
	for _, item := range data.VMs {
		inventory[key{item.Observation.Context, item.Observation.VM.ID}] = item.Observation.VM.InstanceUUID
	}
	match := func(context, moref, uuid string) string {
		have, ok := inventory[key{context, moref}]
		switch {
		case !ok:
			return "not in this run"
		case uuid != "" && have != "" && uuid != have:
			return "instance UUID differs"
		}
		return "matched"
	}

	windows := append([]perf.Window(nil), data.Performance...)
	sort.SliceStable(windows, func(i, j int) bool { return windows[i].Context < windows[j].Context })
	byVCenter := map[string]bool{}
	byContext := map[string]bool{}
	rows := make([][]any, 0)
	for _, w := range windows {
		byVCenter[w.VCenterID], byContext[w.Context] = true, true
		vms := append([]perf.VMResult(nil), w.VMs...)
		sort.SliceStable(vms, func(i, j int) bool {
			if vms[i].Name != vms[j].Name {
				return vms[i].Name < vms[j].Name
			}
			return vms[i].MoRef < vms[j].MoRef
		})
		if len(vms) == 0 {
			rows = append(rows, []any{w.Context, w.VCenterID, nil, nil, nil, nil, nil, nil, nil,
				w.WindowStart.UTC(), w.WindowEnd.UTC(), w.IntervalSeconds, w.ExpectedSamples, nil, nil,
				nil, nil, nil, "no VMs", w.Error, nil, nil, w.Status, w.Source})
		}
		for _, vm := range vms {
			identity := []any{w.Context, w.VCenterID, vm.Name, vm.MoRef, vm.InstanceUUID, match(w.Context, vm.MoRef, vm.InstanceUUID)}
			window := []any{w.WindowStart.UTC(), w.WindowEnd.UTC(), w.IntervalSeconds, w.ExpectedSamples}
			if len(vm.Summaries) == 0 {
				row := append(append(append([]any(nil), identity...), nil, nil, nil), window...)
				rows = append(rows, append(row, nil, nil, nil, nil, nil, "not sampled", vm.SignalReason, string(vm.Signal), vm.SignalReason, w.Status, w.Source))
				continue
			}
			for _, s := range vm.Summaries {
				row := append(append([]any(nil), identity...), string(s.Metric), string(s.Unit), s.Aggregation)
				row = append(row, w.WindowStart.UTC(), w.WindowEnd.UTC(), s.Interval, s.Expected, s.Successful, s.Missing,
					floatCell(s.Average), floatCell(s.Peak), floatCell(s.P95), string(s.Status), s.Reason,
					string(vm.Signal), vm.SignalReason, w.Status, w.Source)
				rows = append(rows, row)
			}
		}
	}
	for _, c := range data.Contexts {
		if byVCenter[c.VCenterID] && c.VCenterID != "" || byContext[c.Name] {
			continue
		}
		rows = append(rows, []any{c.Name, c.VCenterID, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			nil, nil, nil, "not collected", "no performance collection is recorded for this vCenter; run `vsfleet assessment perf collect`", nil, nil, nil, nil})
	}
	return rows
}
