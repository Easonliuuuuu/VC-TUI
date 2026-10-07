package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/perf"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func newAssessmentPerfCommand(a *App) *cobra.Command {
	cmd := requireSubcommand(&cobra.Command{
		Use:   "perf",
		Short: "Collect and read bounded VM performance history",
		Long: strings.TrimSpace(`
Read a bounded window of CPU and memory statistics for every VM from vCenter's
historical performance data, and keep summaries of it in the local history
database, apart from inventory captures.

"perf collect" is the only subcommand here that contacts a vCenter. It is
opt-in and read-only, and bounded by a time window, a sample count, a VM
count, a request count and a runtime. "perf list" and "perf show" read back
what was stored.

Values are averages over the vCenter's roll-up interval (typically 5 minutes
to 2 hours), so a reported peak is the highest interval average and can
understate an instantaneous spike. Anything that could not be read, or has
too few samples to support a statistic, is reported as unavailable or
insufficient data, never as zero.`),
		Example: `  # Collect the last week, then read it back
  vsfleet assessment perf collect --window 7d
  vsfleet assessment perf list
  vsfleet assessment perf show latest`,
	})
	cmd.AddCommand(newAssessmentPerfCollectCommand(a), newAssessmentPerfListCommand(a), newAssessmentPerfShowCommand(a))
	return cmd
}

func newAssessmentPerfCollectCommand(a *App) *cobra.Command {
	var window, maxRuntime string
	var interval, maxSamples, maxVMs, maxRequests int
	cmd := &cobra.Command{Use: "collect", Short: "Collect a bounded window of VM performance history", Long: strings.TrimSpace(`
Query vCenter's historical statistics for CPU usage, CPU ready, active and
consumed memory, ballooning, swapping and swap-in rate over --window, and
store per-VM summaries and provenance. Nothing on the vCenter is changed.

The finest historical interval that still retains the whole window is used,
unless --interval names one. vCenter only keeps counters that its statistics
level enables. Active memory and swapped memory need level 2, above the
default of 1; a counter the interval does not keep is not requested and is
recorded as unavailable, and VMs then get a "cpu-only" reading.

Collection stops, and the window is recorded as partial, when any bound is
reached: --max-vms, --max-requests or --max-runtime. VMs that were not
sampled are listed as such rather than dropped.`), Example: `  # The last week for the current context
  vsfleet assessment perf collect --window 7d

  # A month for every context, capped at 300 VMs
  vsfleet assessment perf collect --all-contexts --window 30d --max-vms 300

  # Read the result
  vsfleet assessment perf show latest`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		span, err := parseHumanDuration(window)
		if err != nil || span <= 0 {
			return fmt.Errorf("--window must be a positive duration such as 24h or 7d")
		}
		runtime := assessment.DefaultPerfMaxRuntime
		if maxRuntime != "" {
			if runtime, err = parseHumanDuration(maxRuntime); err != nil || runtime <= 0 {
				return fmt.Errorf("--max-runtime must be a positive duration such as 10m")
			}
		}
		for flag, v := range map[string]int{"--interval": interval, "--max-samples": maxSamples, "--max-vms": maxVMs, "--max-requests": maxRequests} {
			if v < 0 {
				return fmt.Errorf("%s cannot be negative", flag)
			}
		}
		contexts, err := a.Contexts()
		if err != nil {
			return err
		}
		service, err := a.Assessment()
		if err != nil {
			return err
		}
		windows, err := service.Collector.CapturePerf(cmd.Context(), assessment.PerfCaptureOptions{
			Contexts:   contexts,
			MaxRuntime: runtime,
			Perf:       vsphere.PerfOptions{Window: span, Interval: interval, MaxSamples: maxSamples, MaxVMs: maxVMs, MaxRequests: maxRequests},
		})
		if err != nil {
			return err
		}
		failed := 0
		for _, w := range windows {
			if w.Status == perf.WindowFailed {
				failed++
			}
		}
		if a.json() {
			if err := writeJSON(a.out(), windows); err != nil {
				return err
			}
		} else {
			for _, w := range windows {
				printPerfWindowSummary(a, w)
			}
		}
		if failed == len(windows) {
			return fmt.Errorf("performance collection failed for every context")
		}
		return nil
	}}
	cmd.Flags().StringVar(&window, "window", "7d", "how far back to read (e.g. 24h, 7d, 30d); must fit within the vCenter's retained history")
	cmd.Flags().IntVar(&interval, "interval", 0, "historical roll-up interval in seconds; 0 picks the finest one that covers the window")
	cmd.Flags().IntVar(&maxSamples, "max-samples", 0, fmt.Sprintf("refuse a window holding more samples per counter than this (default %d)", vsphere.DefaultPerfMaxSamples))
	cmd.Flags().IntVar(&maxVMs, "max-vms", 0, fmt.Sprintf("query at most this many VMs per context (default %d)", vsphere.DefaultPerfMaxVMs))
	cmd.Flags().IntVar(&maxRequests, "max-requests", 0, fmt.Sprintf("issue at most this many QueryPerf requests per context (default %d)", vsphere.DefaultPerfMaxRequests))
	cmd.Flags().StringVar(&maxRuntime, "max-runtime", "", fmt.Sprintf("stop querying after this long per context (default %s)", assessment.DefaultPerfMaxRuntime))
	return cmd
}

func printPerfWindowSummary(a *App, w perf.Window) {
	glyph := "ok"
	if w.Status == perf.WindowFailed {
		glyph = glyphFail
	}
	fmt.Fprintf(a.out(), "%s %s: window #%d %s\n", glyph, w.Context, w.ID, w.Status)
	if w.Status == perf.WindowFailed && w.IntervalSeconds == 0 {
		fmt.Fprintf(a.out(), "  error: %s\n", dash(w.Error))
		return
	}
	fmt.Fprintf(a.out(), "  window:    %s to %s (%d s interval, %d samples per counter)\n",
		w.WindowStart.Local().Format("2006-01-02 15:04"), w.WindowEnd.Local().Format("2006-01-02 15:04"), w.IntervalSeconds, w.ExpectedSamples)
	fmt.Fprintf(a.out(), "  coverage:  %d of %d VMs sampled\n", w.VMsSampled, w.VMsRequested)
	fmt.Fprintf(a.out(), "  cost:      %d of %d requests, %s\n", w.RequestsUsed, w.Budget.MaxRequests, w.FinishedAt.Sub(w.StartedAt).Round(time.Second))
	counts := map[perf.Signal]int{}
	unavailable := 0
	var belowLevel []string
	seen := map[perf.Metric]bool{}
	for _, vm := range w.VMs {
		counts[vm.Signal]++
		for _, s := range vm.Summaries {
			switch {
			case perf.BelowLevel(s):
				if !seen[s.Metric] {
					seen[s.Metric] = true
					belowLevel = append(belowLevel, string(s.Metric))
				}
			case s.Status == perf.StatusUnavailable:
				unavailable++
			}
		}
	}
	fmt.Fprintf(a.out(), "  signals:   %s\n", signalCounts(counts))
	if len(belowLevel) > 0 {
		fmt.Fprintf(a.out(), "  not kept:  %s, above this interval's statistics level (see \"assessment perf show %d\")\n", strings.Join(belowLevel, ", "), w.ID)
	}
	if unavailable > 0 {
		fmt.Fprintf(a.out(), "  unavailable counters: %d (see \"assessment perf show %d\")\n", unavailable, w.ID)
	}
	if w.Error != "" {
		fmt.Fprintf(a.out(), "  notes:     %s\n", w.Error)
	}
}

func signalCounts(counts map[perf.Signal]int) string {
	order := []perf.Signal{perf.SignalContention, perf.SignalPeaksObserved, perf.SignalInUse, perf.SignalSustainedLow, perf.SignalCPUOnly, perf.SignalInsufficient, perf.SignalUnavailable}
	var parts []string
	for _, s := range order {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	return dash(strings.Join(parts, ", "))
}

func newAssessmentPerfListCommand(a *App) *cobra.Command {
	return &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List stored performance collections", Long: strings.TrimSpace(`
List stored performance collections, newest first. The ID is what "perf show"
accepts.`), Example: `  vsfleet assessment perf list
  vsfleet assessment perf list -o json`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := rejectStoredContextSelection(cmd, "perf list"); err != nil {
			return err
		}
		s, err := a.History()
		if err != nil {
			return err
		}
		windows, err := s.PerfWindows(cmd.Context(), "", 100)
		if err != nil {
			return err
		}
		if a.json() {
			return writeJSON(a.out(), windows)
		}
		t := newTable(a.out(), "ID", "CONTEXT", "STATUS", "WINDOW END", "SPAN", "INTERVAL", "VMS", "REQUESTS")
		for _, w := range windows {
			t.row(strconv.FormatInt(w.ID, 10), w.Context, w.Status, w.WindowEnd.Local().Format("2006-01-02 15:04"),
				w.WindowEnd.Sub(w.WindowStart).Round(time.Hour).String(), fmt.Sprintf("%ds", w.IntervalSeconds),
				fmt.Sprintf("%d/%d", w.VMsSampled, w.VMsRequested), itoa(w.RequestsUsed))
		}
		t.flush()
		return nil
	}}
}

func newAssessmentPerfShowCommand(a *App) *cobra.Command {
	return &cobra.Command{Use: "show [ID|latest]", Short: "Show one performance collection", Long: strings.TrimSpace(`
Show per-VM summaries from a stored collection: average, peak and 95th
percentile of interval averages, the sample count behind them, and a
conservative sizing signal.

A dash means unknown, never zero. The 95th percentile appears only when
enough samples support it. Signals other than "in-use" and "sustained-low"
explain themselves below the table.`), Example: `  vsfleet assessment perf show latest
  vsfleet assessment perf show 3 -o json`, Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := a.History()
		if err != nil {
			return err
		}
		var id int64
		if len(args) == 0 || args[0] == "latest" {
			windows, err := s.PerfWindows(cmd.Context(), "", 1)
			if err != nil {
				return err
			}
			if len(windows) == 0 {
				return fmt.Errorf("no performance collections are stored; run \"assessment perf collect\"")
			}
			id = windows[0].ID
		} else if id, err = strconv.ParseInt(args[0], 10, 64); err != nil {
			return fmt.Errorf("invalid performance window %q", args[0])
		}
		w, err := s.LoadPerfWindow(cmd.Context(), id)
		if err != nil {
			return err
		}
		if a.json() {
			return writeJSON(a.out(), w)
		}
		printPerfWindowSummary(a, w)
		fmt.Fprintln(a.out())
		t := newTable(a.out(), "VM", "SIGNAL", "CPU% AVG", "PEAK", "P95", "ACTIVE MiB AVG", "PEAK", "READY% PEAK", "BALLOON MiB", "SWAP MiB", "SWAP-IN KBps", "SAMPLES")
		for _, vm := range w.VMs {
			cpu, mem := perfSummary(vm, perf.CPUUsage), perfSummary(vm, perf.MemActive)
			t.row(vm.Name, string(vm.Signal), perfNum(cpu.Average), perfNum(cpu.Peak), perfNum(cpu.P95),
				perfNum(mem.Average), perfNum(mem.Peak), perfNum(perfSummary(vm, perf.CPUReady).Peak),
				perfNum(perfSummary(vm, perf.MemBalloon).Peak), perfNum(perfSummary(vm, perf.MemSwapped).Peak),
				perfNum(perfSummary(vm, perf.MemSwapinRate).Peak), perfSamples(cpu))
		}
		t.flush()
		notes := false
		for _, vm := range w.VMs {
			switch vm.Signal {
			case perf.SignalInUse, perf.SignalSustainedLow:
				continue
			}
			if !notes {
				fmt.Fprintln(a.out(), "\nNotes:")
				notes = true
			}
			fmt.Fprintf(a.out(), "  %s: %s\n", vm.Name, dash(vm.SignalReason))
		}
		return nil
	}}
}

func perfSummary(vm perf.VMResult, m perf.Metric) perf.Summary {
	for _, s := range vm.Summaries {
		if s.Metric == m {
			return s
		}
	}
	return perf.Summary{}
}

func perfNum(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'f', 1, 64)
}

func perfSamples(s perf.Summary) string {
	if s.Expected == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", s.Successful, s.Expected)
}

// printPerformanceSummary adds the per-context performance lines to an
// assessment report. Each line carries the collection's own window, because
// performance history is collected separately from the capture it sits beside.
func printPerformanceSummary(a *App, rows []assessment.PerfSummary) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintln(a.out(), "\nPerformance history (collected separately from the capture):")
	for _, r := range rows {
		if r.WindowID == 0 {
			fmt.Fprintf(a.out(), "  %s: not collected; run \"vsfleet assessment perf collect\"\n", r.Context)
			continue
		}
		counts := make(map[perf.Signal]int, len(r.Signals))
		for k, v := range r.Signals {
			counts[perf.Signal(k)] = v
		}
		fmt.Fprintf(a.out(), "  %s: window #%d %s, %s to %s, %d of %d VMs sampled; %s\n", r.Context, r.WindowID, r.Status,
			r.WindowStart.Local().Format("2006-01-02"), r.WindowEnd.Local().Format("2006-01-02"), r.VMsSampled, r.VMsRequested, signalCounts(counts))
	}
}
