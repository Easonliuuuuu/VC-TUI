package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/sizing"
)

// sizingExitError reports that the scenario did not conclude "fit". It is
// raised only under --fail-unless-fit.
type sizingExitError struct{ verdict string }

func (e *sizingExitError) Error() string {
	return fmt.Sprintf("destination sizing verdict is %s, not fit", e.verdict)
}
func (e *sizingExitError) ExitCode() int { return 2 }

func newAssessmentSizingCommand(a *App) *cobra.Command {
	var (
		hosts, hostCores, failures       int
		hostRAM, cpuRatio, memRatio, gro float64
		datastoreCap, rdmCap             string
		clusters                         []string
		includeOff, failUnlessFit        bool
	)
	cmd := &cobra.Command{
		Use:   "sizing [RUN]",
		Short: "Test whether stored VMs fit a proposed destination (allocation based)",
		Long: strings.TrimSpace(`
Evaluate a read-only, offline destination-capacity scenario against a stored
assessment. The scenario states a target host count, usable cores and RAM per
host, datastore capacity, oversubscription, a growth allowance and how many
host failures to tolerate (N-1 by default). VMs are scoped with --context and
--cluster.

The result is allocation based: it compares configured vCPU, configured memory
and provisioned disk capacity with the target. It uses no performance history
and does not infer rightsizing. Datastore-backed disks, RDM capacity and guest
filesystem use are kept separate and never added together; a datastore reached
through several contexts, and a disk or LUN attached to several VMs, is counted
once.

Each dimension concludes fit, insufficient or unknown. A missing target input,
incomplete source coverage (partial or failed collections) or ambiguous storage
identity gives unknown, never fit. This is a generic planning scenario, not a
claim of compatibility with any named platform. Nothing is changed in vCenter
or in the stored assessment.`),
		Example: `  # Can everything in the latest assessment fit on 4 hosts, tolerating one failure?
  vsfleet assessment sizing --hosts 4 --host-cores 32 --host-ram-gib 512 \
    --datastore-capacity 40TiB --cpu-ratio 4

  # Two clusters, 20% growth, RDM LUN capacity available, gate a pipeline
  vsfleet assessment sizing pre-migration --cluster cluster-a --cluster cluster-b \
    --hosts 3 --host-cores 48 --host-ram-gib 768 --datastore-capacity 60TiB \
    --rdm-capacity 2TiB --growth-pct 20 --fail-unless-fit -o json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			sc := sizing.Scenario{Clusters: clusters, IncludePoweredOff: includeOff}
			if flags.Changed("hosts") {
				sc.Hosts = &hosts
			}
			if flags.Changed("host-cores") {
				sc.HostCores = &hostCores
			}
			if flags.Changed("host-ram-gib") {
				sc.HostRAMGiB = &hostRAM
			}
			if flags.Changed("cpu-ratio") {
				sc.CPURatio = &cpuRatio
			}
			if flags.Changed("memory-ratio") {
				sc.MemoryRatio = &memRatio
			}
			if flags.Changed("growth-pct") {
				sc.GrowthPct = &gro
			}
			if flags.Changed("ha-host-failures") {
				sc.HostFailures = &failures
			}
			for name, v := range map[string]float64{"--hosts": float64(hosts), "--host-cores": float64(hostCores), "--host-ram-gib": hostRAM, "--cpu-ratio": cpuRatio, "--memory-ratio": memRatio, "--growth-pct": gro, "--ha-host-failures": float64(failures)} {
				if v < 0 {
					return fmt.Errorf("%s must be zero or greater", name)
				}
			}
			for _, item := range []struct {
				name, value string
				dst         **float64
			}{{"--datastore-capacity", datastoreCap, &sc.DatastoreCapacityBytes}, {"--rdm-capacity", rdmCap, &sc.RDMCapacityBytes}} {
				if strings.TrimSpace(item.value) == "" {
					continue
				}
				n, err := parseHumanBytes(item.value)
				if err != nil || n < 0 {
					return fmt.Errorf("%s: invalid size %q", item.name, item.value)
				}
				v := n
				*item.dst = &v
			}
			data, err := loadRunExportData(cmd, a, args)
			if err != nil {
				return err
			}
			result := sizing.Evaluate(data, sc, time.Now())
			if a.json() {
				if err := writeJSON(a.out(), result); err != nil {
					return err
				}
			} else {
				printSizing(a, result)
			}
			if failUnlessFit && result.Verdict != sizing.VerdictFit {
				return &sizingExitError{verdict: result.Verdict}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&hosts, "hosts", 0, "target host count")
	f.IntVar(&hostCores, "host-cores", 0, "usable cores per target host")
	f.Float64Var(&hostRAM, "host-ram-gib", 0, "usable RAM per target host, in GiB")
	f.StringVar(&datastoreCap, "datastore-capacity", "", "usable target datastore capacity, e.g. 40TiB")
	f.StringVar(&rdmCap, "rdm-capacity", "", "raw LUN capacity the target can present to RDM disks (needed only when the scope has RDMs)")
	f.Float64Var(&cpuRatio, "cpu-ratio", 1, "vCPU per usable core (1 means no oversubscription)")
	f.Float64Var(&memRatio, "memory-ratio", 1, "allocated memory per usable memory (1 means no overcommit)")
	f.Float64Var(&gro, "growth-pct", 0, "growth allowance applied to every demand, in percent")
	f.IntVar(&failures, "ha-host-failures", 1, "hosts held back as failure headroom (1 is N-1, 0 disables)")
	f.StringSliceVar(&clusters, "cluster", nil, "only VMs in this cluster, as NAME or CONTEXT/NAME (repeatable)")
	f.BoolVar(&includeOff, "include-powered-off", false, "also size powered-off and suspended VMs (templates stay excluded)")
	f.BoolVar(&failUnlessFit, "fail-unless-fit", false, "exit 2 unless every dimension is fit")
	return cmd
}

func sizingNumber(v *float64, unit string) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f %s", *v, unit)
}

func printSizing(a *App, r sizing.Result) {
	out := a.out()
	fmt.Fprintf(out, "Destination sizing: %s (%s)\n", strings.ToUpper(r.Verdict), r.Basis)
	fmt.Fprintf(out, "  %s\n", r.Summary)
	f := newFields(out)
	f.add("run", fmt.Sprintf("%d (%s, captured %s)", r.RunID, r.RunStatus, r.RunFinishedAt.UTC().Format("2006-01-02 15:04 MST")))
	f.add("generated", r.GeneratedAt.Format("2006-01-02 15:04:05 MST"))
	scope := strings.Join(r.Scope.Contexts, ", ")
	if len(r.Scope.Clusters) > 0 {
		scope += "; clusters " + strings.Join(r.Scope.Clusters, ", ")
	}
	f.add("scope", fmt.Sprintf("%s; %d VM(s) sized, %d excluded", scope, r.Scope.VMsInScope, r.Scope.VMsExcluded))
	if r.SourceHosts.Observed {
		f.add("source hosts", fmt.Sprintf("%d host(s), %d cores, %.0f GiB RAM (informational)", r.SourceHosts.Hosts, r.SourceHosts.Cores, r.SourceHosts.RAMGiB))
	}
	f.flush()

	fmt.Fprintln(out, "\nTarget assumptions:")
	t := newTable(out, "INPUT", "VALUE", "UNIT", "SOURCE")
	for _, as := range r.Assumptions {
		t.row(as.Name, as.Value, as.Unit, as.Source)
	}
	t.flush()

	fmt.Fprintln(out, "\nDimensions:")
	t = newTable(out, "DIMENSION", "VERDICT", "REQUIRED", "SUPPLY", "HEADROOM")
	for _, d := range r.Dimensions {
		t.row(d.Name, strings.ToUpper(d.Verdict), sizingNumber(d.Required, d.Unit), sizingNumber(d.Supply, d.Unit), sizingNumber(d.Headroom, d.Unit))
	}
	t.flush()
	for _, d := range r.Dimensions {
		fmt.Fprintf(out, "\n%s: %s\n", d.Name, d.Formula)
		for _, reason := range d.Reasons {
			fmt.Fprintf(out, "  - %s\n", reason)
		}
	}

	fmt.Fprintln(out, "\nSource measures (never added together):")
	t = newTable(out, "MEASURE", "VALUE", "BASIS", "IN VERDICT")
	for _, m := range r.Measures {
		in := "no"
		if m.InVerdict {
			in = "yes"
		}
		t.row(m.Name, fmt.Sprintf("%.1f %s", m.Value, m.Unit), m.Basis, in)
	}
	t.flush()
	if len(r.SourceStorage) > 0 {
		fmt.Fprintln(out, "\nSource datastores (each physical datastore counted once):")
		for _, g := range r.SourceStorage {
			line := fmt.Sprintf("  %s  capacity %.0f GiB, used %.0f GiB", strings.Join(g.Aliases, " = "), float64(g.CapacityBytes)/(1<<30), float64(g.UsedBytes)/(1<<30))
			if g.Ambiguous {
				line += "  [identity ambiguous]"
			}
			fmt.Fprintln(out, line)
		}
	}

	if len(r.Coverage) > 0 {
		fmt.Fprintln(out, "\nCoverage gaps (verdicts above are unknown where these apply):")
		for _, gap := range r.Coverage {
			fmt.Fprintf(out, "  %s %s\n", glyphFail, gap)
		}
	}
	if len(r.Exclusions) > 0 {
		fmt.Fprintf(out, "\nExclusions (%d):\n", len(r.Exclusions))
		byReason := map[string][]string{}
		var order []string
		for _, x := range r.Exclusions {
			if _, ok := byReason[x.Reason]; !ok {
				order = append(order, x.Reason)
			}
			byReason[x.Reason] = append(byReason[x.Reason], x.Context+"/"+x.Name)
		}
		for _, reason := range order {
			names := byReason[reason]
			shown := names
			suffix := ""
			if len(shown) > 8 {
				shown, suffix = shown[:8], fmt.Sprintf(" and %d more (all listed in -o json)", len(names)-8)
			}
			fmt.Fprintf(out, "  %s: %s%s\n", reason, strings.Join(shown, ", "), suffix)
		}
	}
	fmt.Fprintln(out, "\nRelated readiness (existing offline checks):")
	fmt.Fprintf(out, "  migration readiness for this scope: %d blocker(s), %d advisory finding(s); %d rule(s) not evaluated (%s)\n", r.Readiness.Blockers, r.Readiness.Advisories, r.Readiness.UnresolvedRules, r.Readiness.MigrationCommand)
	fmt.Fprintf(out, "  network: %d distinct network(s) used by these VMs; %s (%s)\n", r.Readiness.DistinctNetworks, r.Readiness.NetworkNotEvaluated, r.Readiness.NetworkCommand)
	fmt.Fprintln(out, "\nNotes:")
	for _, n := range r.Notes {
		fmt.Fprintf(out, "  - %s\n", n)
	}
}
