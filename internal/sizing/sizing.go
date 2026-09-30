// Package sizing evaluates whether a proposed destination could hold a scoped
// set of stored VMs. It is a pure, offline function over one stored
// assessment: it never contacts vCenter and never mutates the assessment.
//
// The scenario is deliberately allocation based. It compares configured vCPU,
// configured memory, provisioned disk capacity and RDM capacity with the
// stated target; it does not infer rightsizing from point-in-time samples and
// makes no claim of compatibility with any named platform.
//
// Every dimension concludes fit, insufficient or unknown. Missing target
// assumptions, incomplete source coverage and ambiguous storage identity
// yield unknown, never a reassuring fit.
package sizing

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/health"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

const (
	SchemaVersion = 1
	// Basis labels every result: allocation only, no utilization history.
	Basis = "allocation-based"

	VerdictFit          = "fit"
	VerdictInsufficient = "insufficient"
	VerdictUnknown      = "unknown"
	// VerdictInformational marks measures shown for context that never feed
	// the overall verdict.
	VerdictInformational = "informational"

	gib = float64(1 << 30)
)

// Scenario is the proposed destination and the scope of VMs to place on it.
// A nil pointer means "not provided". Required values that are nil make the
// affected dimensions unknown; optional values fall back to a visible default.
type Scenario struct {
	Hosts                  *int
	HostCores              *int
	HostRAMGiB             *float64
	DatastoreCapacityBytes *float64
	// RDMCapacityBytes is the LUN capacity the target can present to RDM
	// style disks. It is required only when the scope contains RDMs.
	RDMCapacityBytes *float64
	// Optional, with defaults: CPURatio 1.0 vCPU per usable core,
	// MemoryRatio 1.0, GrowthPct 0, HostFailures 1 (N-1).
	CPURatio     *float64
	MemoryRatio  *float64
	GrowthPct    *float64
	HostFailures *int

	// Clusters narrows the scope to VMs in these clusters, as "name" or
	// "context/name". Empty means every cluster in the loaded contexts.
	Clusters          []string
	IncludePoweredOff bool
}

// Assumption records one scenario input, its unit and where it came from.
type Assumption struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Unit   string `json:"unit,omitempty"`
	Source string `json:"source"` // provided, default or missing
	Note   string `json:"note,omitempty"`
}

// Exclusion names a resource left out of the sizing and why.
type Exclusion struct {
	Kind    string `json:"kind"`
	Context string `json:"context,omitempty"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
}

// Dimension is one capacity comparison with its formula.
type Dimension struct {
	Name     string   `json:"name"`
	Verdict  string   `json:"verdict"`
	Unit     string   `json:"unit"`
	Required *float64 `json:"required,omitempty"`
	Supply   *float64 `json:"supply,omitempty"`
	Headroom *float64 `json:"headroom,omitempty"`
	Formula  string   `json:"formula"`
	Reasons  []string `json:"reasons,omitempty"`
}

// Measure is one source-side storage or compute total. Unlike measures are
// never added together: each carries its own basis and unit.
type Measure struct {
	Name       string  `json:"name"`
	Value      float64 `json:"value"`
	Unit       string  `json:"unit"`
	Basis      string  `json:"basis"`
	InVerdict  bool    `json:"in_verdict"`
	Note       string  `json:"note,omitempty"`
	Populated  int     `json:"covered_vms,omitempty"`
	OutOfTotal int     `json:"of_vms,omitempty"`
}

// StorageGroup is one physical datastore, counted once however many contexts
// or names it is reached through.
type StorageGroup struct {
	Aliases       []string `json:"aliases"`
	Identity      []string `json:"identity,omitempty"`
	CapacityBytes int64    `json:"capacity_bytes"`
	UsedBytes     int64    `json:"used_bytes"`
	Ambiguous     bool     `json:"ambiguous_identity,omitempty"`
}

// Scope describes what was evaluated.
type Scope struct {
	Contexts          []string `json:"contexts"`
	Clusters          []string `json:"clusters,omitempty"`
	IncludePoweredOff bool     `json:"include_powered_off"`
	VMsInScope        int      `json:"vms_in_scope"`
	VMsExcluded       int      `json:"vms_excluded"`
}

// SourceHosts is the informational source estate for the scoped clusters.
type SourceHosts struct {
	Hosts    int     `json:"hosts"`
	Cores    int64   `json:"cores"`
	RAMGiB   float64 `json:"ram_gib"`
	Observed bool    `json:"observed"`
}

// ReadinessLinks refers to the existing readiness findings for the scope.
type ReadinessLinks struct {
	Verdict             string         `json:"migration_readiness_verdict"`
	Blockers            int            `json:"migration_blockers"`
	Advisories          int            `json:"migration_advisories"`
	UnresolvedRules     int            `json:"migration_rules_not_evaluated"`
	FindingsByRule      map[string]int `json:"findings_by_rule,omitempty"`
	DistinctNetworks    int            `json:"distinct_networks"`
	MigrationCommand    string         `json:"migration_command"`
	NetworkCommand      string         `json:"network_command"`
	RDMsRequireExplicit bool           `json:"rdm_requires_explicit_plan,omitempty"`
	NetworkNotEvaluated string         `json:"network_note"`
}

// Result is the complete, reproducible outcome of one scenario.
type Result struct {
	SchemaVersion int            `json:"schema_version"`
	Basis         string         `json:"basis"`
	Verdict       string         `json:"verdict"`
	Summary       string         `json:"summary"`
	RunID         int64          `json:"run_id"`
	RunStatus     string         `json:"run_status"`
	RunFinishedAt time.Time      `json:"run_finished_at,omitempty"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Scope         Scope          `json:"scope"`
	Assumptions   []Assumption   `json:"assumptions"`
	Dimensions    []Dimension    `json:"dimensions"`
	Measures      []Measure      `json:"measures"`
	SourceStorage []StorageGroup `json:"source_datastores,omitempty"`
	SourceHosts   SourceHosts    `json:"source_hosts"`
	Readiness     ReadinessLinks `json:"readiness"`
	Coverage      []string       `json:"coverage_gaps,omitempty"`
	Exclusions    []Exclusion    `json:"exclusions,omitempty"`
	Notes         []string       `json:"notes"`
	Inputs        map[string]any `json:"inputs"`
}

// Evaluate computes the scenario. now is stamped into the result and is the
// only non-deterministic input.
func Evaluate(data assessment.ExportData, sc Scenario, now time.Time) Result {
	e := &evaluator{data: data, sc: sc}
	return e.run(now)
}

type evaluator struct {
	data assessment.ExportData
	sc   Scenario

	hosts, cores int
	ramGiB       float64
	cpuRatio     float64
	memRatio     float64
	growth       float64
	failures     int
}

type inScopeVM struct {
	ctx string
	vm  vsphere.VM
}

func fp(v float64) *float64 { return &v }

func (e *evaluator) run(now time.Time) Result {
	sc, data := e.sc, e.data
	res := Result{
		SchemaVersion: SchemaVersion, Basis: Basis, RunID: data.Run.ID, RunStatus: string(data.Run.Status),
		RunFinishedAt: data.Run.FinishedAt, GeneratedAt: now.UTC(),
	}
	res.Assumptions = e.assumptions()
	res.Inputs = e.inputs()

	// Scope.
	var vms []inScopeVM
	contexts := make([]string, 0, len(data.Contexts))
	for _, c := range data.Contexts {
		contexts = append(contexts, c.Name)
	}
	sort.Strings(contexts)
	clusterSeen := map[string]bool{}
	for _, item := range data.VMs {
		vm := item.Observation.VM
		ctx := item.Observation.Context
		switch {
		case vm.IsTemplate:
			res.Exclusions = append(res.Exclusions, Exclusion{"vm", ctx, vm.Name, "template: not a running workload"})
		case len(sc.Clusters) > 0 && !clusterSelected(sc.Clusters, ctx, vm.Cluster):
			res.Exclusions = append(res.Exclusions, Exclusion{"vm", ctx, vm.Name, fmt.Sprintf("outside the selected clusters (cluster %q)", vm.Cluster)})
		case vm.PowerState != "poweredOn" && !sc.IncludePoweredOff:
			res.Exclusions = append(res.Exclusions, Exclusion{"vm", ctx, vm.Name, fmt.Sprintf("power state %q; powered-off VMs are excluded unless included explicitly", nonempty(vm.PowerState, "unknown"))})
		default:
			vms = append(vms, inScopeVM{ctx, vm})
			clusterSeen[ctx+"/"+vm.Cluster] = true
		}
	}
	sort.SliceStable(res.Exclusions, func(i, j int) bool {
		a, b := res.Exclusions[i], res.Exclusions[j]
		if a.Context != b.Context {
			return a.Context < b.Context
		}
		return a.Name < b.Name
	})
	res.Scope = Scope{Contexts: contexts, Clusters: append([]string(nil), sc.Clusters...), IncludePoweredOff: sc.IncludePoweredOff, VMsInScope: len(vms), VMsExcluded: len(res.Exclusions)}

	// Coverage.
	var vmGaps, storageGaps []string
	if data.Run.Status != assessment.RunComplete {
		note := fmt.Sprintf("source run status is %q, not complete", data.Run.Status)
		vmGaps = append(vmGaps, note)
	}
	if len(data.Contexts) == 0 {
		vmGaps = append(vmGaps, "the assessment has no contexts in scope")
	}
	for _, name := range assessment.BlindContexts(data.Contexts, []string{"vm"}) {
		vmGaps = append(vmGaps, fmt.Sprintf("context %s: %s", name, assessment.CoverageReason(data.Contexts, name, []string{"vm"})))
	}
	for _, name := range assessment.BlindContexts(data.Contexts, []string{"datastore"}) {
		storageGaps = append(storageGaps, fmt.Sprintf("context %s: %s", name, assessment.CoverageReason(data.Contexts, name, []string{"datastore"})))
	}
	if len(vms) == 0 {
		vmGaps = append(vmGaps, "no VMs are in scope, so nothing was sized")
	}
	var zeroed []string
	for _, v := range vms {
		if v.vm.CPU <= 0 || v.vm.MemoryMB <= 0 {
			zeroed = append(zeroed, v.ctx+"/"+v.vm.Name)
		}
	}
	if len(zeroed) > 0 {
		sort.Strings(zeroed)
		vmGaps = append(vmGaps, fmt.Sprintf("%d VM(s) have no recorded vCPU or memory allocation (%s)", len(zeroed), strings.Join(head(zeroed, 5), ", ")))
	}

	// Compute totals.
	var vcpu int64
	var memMB, memReservedMB, cpuReservedMHz int64
	var missingResv []string
	for _, v := range vms {
		vcpu += int64(v.vm.CPU)
		memMB += v.vm.MemoryMB
		if !v.vm.ConfigurationAvailable {
			missingResv = append(missingResv, v.ctx+"/"+v.vm.Name)
			continue
		}
		if a := v.vm.MemoryAllocation; a != nil && a.Reservation != nil && *a.Reservation > 0 {
			memReservedMB += *a.Reservation
		}
		if a := v.vm.CPUAllocation; a != nil && a.Reservation != nil && *a.Reservation > 0 {
			cpuReservedMHz += *a.Reservation
		}
	}
	sort.Strings(missingResv)

	res.Dimensions = append(res.Dimensions, e.cpuDimension(vcpu, vmGaps, cpuReservedMHz))
	res.Dimensions = append(res.Dimensions, e.memoryDimension(memMB, vmGaps))
	res.Dimensions = append(res.Dimensions, e.reservationDimension(memReservedMB, missingResv, vmGaps))

	st := e.storage(vms, vmGaps, storageGaps)
	res.Dimensions = append(res.Dimensions, st.dimensions...)
	res.Measures = append(res.Measures, Measure{Name: "vcpu-allocated", Value: float64(vcpu), Unit: "vCPU", Basis: "configured allocation", InVerdict: true},
		Measure{Name: "memory-allocated", Value: float64(memMB) / 1024, Unit: "GiB", Basis: "configured allocation", InVerdict: true},
		Measure{Name: "memory-reserved", Value: float64(memReservedMB) / 1024, Unit: "GiB", Basis: "VM-level reservation", InVerdict: true},
		Measure{Name: "cpu-reserved", Value: float64(cpuReservedMHz), Unit: "MHz", Basis: "VM-level reservation", Note: "not compared: the target core frequency is not an input"})
	res.Measures = append(res.Measures, st.measures...)
	res.SourceStorage = st.groups
	res.Exclusions = append(res.Exclusions, st.exclusions...)
	res.Coverage = dedupe(append(append(append([]string{}, vmGaps...), storageGaps...), st.gaps...))
	res.SourceHosts = e.sourceHosts(clusterSeen)
	res.Readiness = e.readiness(vms)
	if res.Readiness.Blockers > 0 || res.Readiness.UnresolvedRules > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("Migration readiness for this scope: %d blocker(s), %d rule(s) not evaluated. Capacity fit does not imply the VMs are ready to move.", res.Readiness.Blockers, res.Readiness.UnresolvedRules))
	}

	res.Verdict = VerdictFit
	for _, d := range res.Dimensions {
		switch d.Verdict {
		case VerdictInsufficient:
			res.Verdict = VerdictInsufficient
		case VerdictUnknown:
			if res.Verdict != VerdictInsufficient {
				res.Verdict = VerdictUnknown
			}
		}
	}
	res.Summary = summarize(res)
	res.Notes = append([]string{
		"Allocation based: compares configured vCPU, memory and provisioned disk with the stated target. It does not use performance history and does not infer rightsizing from point-in-time samples.",
		"Generic planning scenario: it makes no claim of compatibility with any named hypervisor or cloud platform.",
		"Read-only and offline: computed from the stored assessment; vCenter and the assessment are never modified.",
	}, res.Notes...)
	return res
}

func summarize(r Result) string {
	var bad, unk []string
	for _, d := range r.Dimensions {
		switch d.Verdict {
		case VerdictInsufficient:
			bad = append(bad, d.Name)
		case VerdictUnknown:
			unk = append(unk, d.Name)
		}
	}
	switch r.Verdict {
	case VerdictInsufficient:
		s := "insufficient capacity: " + strings.Join(bad, ", ")
		if len(unk) > 0 {
			s += "; unknown: " + strings.Join(unk, ", ")
		}
		return s
	case VerdictUnknown:
		return "incomplete: cannot conclude for " + strings.Join(unk, ", ")
	}
	return "fits on an allocation basis for every dimension evaluated"
}

func (e *evaluator) assumptions() []Assumption {
	sc := e.sc
	var out []Assumption
	req := func(name, unit, note string, set bool, val string) {
		a := Assumption{Name: name, Unit: unit, Note: note, Source: "provided", Value: val}
		if !set {
			a.Source, a.Value = "missing", "-"
		}
		out = append(out, a)
	}
	if sc.Hosts != nil {
		e.hosts = *sc.Hosts
	}
	if sc.HostCores != nil {
		e.cores = *sc.HostCores
	}
	if sc.HostRAMGiB != nil {
		e.ramGiB = *sc.HostRAMGiB
	}
	req("target-hosts", "hosts", "", sc.Hosts != nil, fmt.Sprint(e.hosts))
	req("host-cores", "usable cores per host", "usable, not physical, cores", sc.HostCores != nil, fmt.Sprint(e.cores))
	req("host-ram", "GiB per host", "usable RAM", sc.HostRAMGiB != nil, fmt.Sprintf("%.6g", e.ramGiB))
	dsv := "-"
	if sc.DatastoreCapacityBytes != nil {
		dsv = fmt.Sprintf("%.6g", *sc.DatastoreCapacityBytes/gib)
	}
	req("target-datastore-capacity", "GiB", "usable datastore capacity; assumes full provisioned disk size is consumed (no thin-provisioning or deduplication credit)", sc.DatastoreCapacityBytes != nil, dsv)
	rdm := "-"
	if sc.RDMCapacityBytes != nil {
		rdm = fmt.Sprintf("%.6g", *sc.RDMCapacityBytes/gib)
	}
	a := Assumption{Name: "target-rdm-capacity", Unit: "GiB", Note: "capacity of raw LUNs the target can present; kept apart from datastore capacity", Source: "provided", Value: rdm}
	if sc.RDMCapacityBytes == nil {
		a.Source = "missing"
		a.Note += " (required only when the scope contains RDMs)"
	}
	out = append(out, a)
	def := func(name, unit, note string, v *float64, d float64) float64 {
		as := Assumption{Name: name, Unit: unit, Note: note, Source: "provided"}
		val := d
		if v != nil {
			val = *v
		} else {
			as.Source = "default"
		}
		as.Value = fmt.Sprintf("%.6g", val)
		out = append(out, as)
		return val
	}
	e.cpuRatio = def("cpu-oversubscription", "vCPU per usable core", "1 means no oversubscription", sc.CPURatio, 1)
	e.memRatio = def("memory-oversubscription", "allocated GiB per usable GiB", "1 means no memory overcommit", sc.MemoryRatio, 1)
	e.growth = def("growth-allowance", "percent", "applied to every demand", sc.GrowthPct, 0)
	fail := Assumption{Name: "ha-host-failures", Unit: "hosts", Note: "hosts held back as failure headroom (N-1 means 1)", Source: "provided", Value: "1"}
	e.failures = 1
	if sc.HostFailures != nil {
		e.failures = *sc.HostFailures
	} else {
		fail.Source = "default"
	}
	fail.Value = fmt.Sprint(e.failures)
	out = append(out, fail)
	return out
}

func (e *evaluator) inputs() map[string]any {
	m := map[string]any{"include_powered_off": e.sc.IncludePoweredOff}
	put := func(k string, v any, ok bool) {
		if ok {
			m[k] = v
		}
	}
	sc := e.sc
	put("hosts", derefInt(sc.Hosts), sc.Hosts != nil)
	put("host_cores", derefInt(sc.HostCores), sc.HostCores != nil)
	put("host_ram_gib", derefF(sc.HostRAMGiB), sc.HostRAMGiB != nil)
	put("datastore_capacity_bytes", derefF(sc.DatastoreCapacityBytes), sc.DatastoreCapacityBytes != nil)
	put("rdm_capacity_bytes", derefF(sc.RDMCapacityBytes), sc.RDMCapacityBytes != nil)
	put("cpu_ratio", e.cpuRatio, true)
	put("memory_ratio", e.memRatio, true)
	put("growth_pct", e.growth, true)
	put("host_failures", e.failures, true)
	put("clusters", sc.Clusters, len(sc.Clusters) > 0)
	return m
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
func derefF(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// usableHosts is the host count left after the failure allowance.
func (e *evaluator) usableHosts() int {
	n := e.hosts - e.failures
	if n < 0 {
		return 0
	}
	return n
}

func (e *evaluator) haNote() string {
	if e.sc.Hosts == nil {
		return ""
	}
	return fmt.Sprintf("usable hosts = target-hosts - ha-host-failures = %d - %d = %d", e.hosts, e.failures, e.usableHosts())
}

// verdict returns the dimension verdict from the fit test, downgraded to
// unknown when any input or coverage is missing. insufficient is kept only when
// demand already exceeds supply on the evidence that is present, because
// missing evidence can only add demand.
func finish(d *Dimension, missing []string, coverage []string, requiredOK, fits bool) {
	d.Reasons = append(d.Reasons, missing...)
	if len(missing) > 0 {
		d.Verdict = VerdictUnknown
		return
	}
	if requiredOK && !fits {
		d.Verdict = VerdictInsufficient
		if len(coverage) > 0 {
			d.Reasons = append(d.Reasons, "source coverage is incomplete; real demand may be higher")
		}
		return
	}
	if len(coverage) > 0 {
		d.Verdict = VerdictUnknown
		d.Reasons = append(d.Reasons, coverage...)
		return
	}
	d.Verdict = VerdictFit
}

func (e *evaluator) cpuDimension(vcpu int64, gaps []string, resvMHz int64) Dimension {
	d := Dimension{Name: "cpu", Unit: "vCPU"}
	var missing []string
	if e.sc.Hosts == nil {
		missing = append(missing, "target host count not provided")
	}
	if e.sc.HostCores == nil {
		missing = append(missing, "target usable cores per host not provided")
	}
	if e.cpuRatio <= 0 {
		missing = append(missing, "cpu-oversubscription must be greater than zero")
	}
	req := float64(vcpu) * (1 + e.growth/100)
	d.Required = fp(req)
	d.Formula = fmt.Sprintf("required vCPU = sum(vCPU of in-scope VMs) x (1 + growth%%/100) = %d x %.4g = %.6g", vcpu, 1+e.growth/100, req)
	ok := len(missing) == 0
	fits := false
	if ok {
		supply := float64(e.usableHosts()*e.cores) * e.cpuRatio
		d.Supply = fp(supply)
		d.Headroom = fp(supply - req)
		fits = req <= supply
		d.Formula += fmt.Sprintf("; supply = usable hosts x cores/host x vCPU:core ratio = %d x %d x %.4g = %.6g", e.usableHosts(), e.cores, e.cpuRatio, supply)
		d.Reasons = append(d.Reasons, e.haNote())
		if pc := float64(e.usableHosts() * e.cores); pc > 0 {
			d.Reasons = append(d.Reasons, fmt.Sprintf("effective oversubscription at target after failure allowance = %.3g vCPU per usable core", req/pc))
		} else {
			d.Reasons = append(d.Reasons, "no usable cores remain after the failure allowance")
		}
	}
	if resvMHz > 0 {
		d.Reasons = append(d.Reasons, fmt.Sprintf("in-scope VMs hold %d MHz of CPU reservations; not compared because target core frequency is not an input", resvMHz))
	}
	finish(&d, missing, gaps, ok, fits)
	return d
}

func (e *evaluator) memoryDimension(memMB int64, gaps []string) Dimension {
	d := Dimension{Name: "memory", Unit: "GiB"}
	var missing []string
	if e.sc.Hosts == nil {
		missing = append(missing, "target host count not provided")
	}
	if e.sc.HostRAMGiB == nil {
		missing = append(missing, "target RAM per host not provided")
	}
	if e.memRatio <= 0 {
		missing = append(missing, "memory-oversubscription must be greater than zero")
	}
	alloc := float64(memMB) / 1024
	req := alloc * (1 + e.growth/100) / max(e.memRatio, 1e-9)
	d.Required = fp(req)
	d.Formula = fmt.Sprintf("required GiB = sum(configured memory) x (1 + growth%%/100) / memory ratio = %.6g x %.4g / %.4g = %.6g", alloc, 1+e.growth/100, e.memRatio, req)
	ok := len(missing) == 0
	fits := false
	if ok {
		supply := float64(e.usableHosts()) * e.ramGiB
		d.Supply = fp(supply)
		d.Headroom = fp(supply - req)
		fits = req <= supply
		d.Formula += fmt.Sprintf("; supply = usable hosts x RAM/host = %d x %.6g = %.6g", e.usableHosts(), e.ramGiB, supply)
		d.Reasons = append(d.Reasons, e.haNote())
	}
	finish(&d, missing, gaps, ok, fits)
	return d
}

func (e *evaluator) reservationDimension(resvMB int64, missingCfg []string, gaps []string) Dimension {
	d := Dimension{Name: "memory-reservation", Unit: "GiB"}
	var missing []string
	if e.sc.Hosts == nil {
		missing = append(missing, "target host count not provided")
	}
	if e.sc.HostRAMGiB == nil {
		missing = append(missing, "target RAM per host not provided")
	}
	req := float64(resvMB) / 1024
	d.Required = fp(req)
	d.Formula = fmt.Sprintf("reserved GiB = sum(VM memory reservations) = %.6g; must not exceed usable RAM = usable hosts x RAM/host (growth and oversubscription do not apply to reservations)", req)
	ok := len(missing) == 0
	fits := false
	if ok {
		supply := float64(e.usableHosts()) * e.ramGiB
		d.Supply = fp(supply)
		d.Headroom = fp(supply - req)
		fits = req <= supply
		d.Reasons = append(d.Reasons, e.haNote())
	}
	cov := append([]string(nil), gaps...)
	if len(missingCfg) > 0 {
		cov = append(cov, fmt.Sprintf("%d VM(s) have no full configuration so their reservations are unknown (%s)", len(missingCfg), strings.Join(head(missingCfg, 5), ", ")))
	}
	finish(&d, missing, cov, ok, fits)
	return d
}

type storageResult struct {
	dimensions []Dimension
	measures   []Measure
	groups     []StorageGroup
	exclusions []Exclusion
	gaps       []string
}

type dsInfo struct {
	ds    vsphere.Datastore
	group int
}

func (e *evaluator) storage(vms []inScopeVM, vmGaps, dsGaps []string) storageResult {
	var out storageResult
	// Index datastores and union them by strong backing identity.
	var infos []*dsInfo
	byName := map[string]*dsInfo{}
	for _, r := range e.data.Resources {
		if r.Kind != "datastore" {
			continue
		}
		var ds vsphere.Datastore
		if err := json.Unmarshal(r.Payload, &ds); err != nil {
			out.gaps = append(out.gaps, fmt.Sprintf("datastore %s/%s payload could not be read", r.Context, r.Name))
			continue
		}
		if ds.Context == "" {
			ds.Context = r.Context
		}
		if ds.Name == "" {
			ds.Name = r.Name
		}
		info := &dsInfo{ds: ds, group: len(infos)}
		infos = append(infos, info)
		byName[strings.ToLower(ds.Context)+"\x00"+strings.ToLower(ds.Name)] = info
	}
	parent := make([]int, len(infos))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	keyOwner := map[string]int{}
	for i, info := range infos {
		for _, k := range info.ds.IdentityKeys() {
			if j, ok := keyOwner[k]; ok {
				parent[find(i)] = find(j)
			} else {
				keyOwner[k] = i
			}
		}
	}
	// Ambiguity: the same display name in several contexts where identity is
	// not proven by matching keys (or is absent) cannot be called shared or
	// distinct. Local datastores are never shared and are unambiguous.
	nameCtx := map[string]map[string]bool{}
	for _, info := range infos {
		n := strings.ToLower(info.ds.Name)
		if nameCtx[n] == nil {
			nameCtx[n] = map[string]bool{}
		}
		nameCtx[n][info.ds.Context] = true
	}
	ambiguous := map[int]bool{}
	for i, info := range infos {
		if info.ds.Backing.Local || len(nameCtx[strings.ToLower(info.ds.Name)]) < 2 {
			continue
		}
		if len(info.ds.IdentityKeys()) == 0 {
			ambiguous[find(i)] = true
			continue
		}
		// Keyed, but a same-named datastore in another context may be the
		// same storage that simply failed to report a key.
		for j, other := range infos {
			if i != j && strings.EqualFold(other.ds.Name, info.ds.Name) && other.ds.Context != info.ds.Context && find(i) != find(j) && len(other.ds.IdentityKeys()) == 0 && !other.ds.Backing.Local {
				ambiguous[find(i)] = true
			}
		}
	}

	touched := map[int]bool{}
	seenDisk := map[string]bool{}
	seenLUN := map[string]bool{}
	var provisioned, rdmBytes, observed float64
	var sharedDisks, sharedRDMs, unresolved, noDisks int
	var rdmGaps, dsDiskGaps []string
	rdmKeys := map[string]map[int32]bool{} // vm -> raw disk keys
	dsKeys := map[string]map[int32]bool{}  // vm -> datastore disk keys
	rdmCount := 0
	for _, v := range vms {
		vmKey := v.ctx + "/" + v.vm.Name + "/" + v.vm.ID
		if len(v.vm.Disks) == 0 && !v.vm.ConfigurationAvailable {
			noDisks++
			continue
		}
		observed += v.vm.StorageGB * gib
		for _, disk := range v.vm.Disks {
			if isRDM(disk) {
				rdmCount++
				if rdmKeys[vmKey] == nil {
					rdmKeys[vmKey] = map[int32]bool{}
				}
				rdmKeys[vmKey][disk.Key] = true
				id := strings.ToLower(strings.TrimSpace(disk.RawLUNID))
				shared := (disk.SharedBus != "" && !strings.EqualFold(disk.SharedBus, "noSharing")) || (disk.Sharing != "" && !strings.EqualFold(disk.Sharing, "sharingNone") && !strings.EqualFold(disk.Sharing, "none"))
				switch {
				case id == "" && shared:
					rdmGaps = append(rdmGaps, fmt.Sprintf("%s/%s disk %q is a shared-bus RDM with no LUN identity, so it cannot be told apart from the same LUN attached elsewhere", v.ctx, v.vm.Name, nonempty(disk.Label, fmt.Sprint(disk.Key))))
					rdmBytes += float64(disk.CapacityBytes)
				case id == "":
					rdmBytes += float64(disk.CapacityBytes)
				case seenLUN[id]:
					sharedRDMs++
				default:
					seenLUN[id] = true
					rdmBytes += float64(disk.CapacityBytes)
				}
				continue
			}
			if dsKeys[vmKey] == nil {
				dsKeys[vmKey] = map[int32]bool{}
			}
			dsKeys[vmKey][disk.Key] = true
			name, rel, ok := vsphere.SplitDatastorePath(disk.BackingPath)
			if !ok {
				unresolved++
				dsDiskGaps = append(dsDiskGaps, fmt.Sprintf("%s/%s disk %q has no datastore-qualified backing path", v.ctx, v.vm.Name, nonempty(disk.Label, fmt.Sprint(disk.Key))))
				provisioned += float64(disk.CapacityBytes)
				continue
			}
			info := byName[strings.ToLower(v.ctx)+"\x00"+name]
			if info == nil {
				unresolved++
				dsDiskGaps = append(dsDiskGaps, fmt.Sprintf("%s/%s disk %q is on datastore %q which is not in the stored datastore inventory", v.ctx, v.vm.Name, nonempty(disk.Label, fmt.Sprint(disk.Key)), name))
				provisioned += float64(disk.CapacityBytes)
				continue
			}
			g := find(info.group)
			touched[g] = true
			key := fmt.Sprintf("%d|%s", g, rel)
			if ambiguous[g] {
				dsDiskGaps = append(dsDiskGaps, fmt.Sprintf("datastore %q is reachable from more than one context without matching backing identity; whether it is one shared datastore or several is unknown", info.ds.Name))
			}
			if rel != "" && seenDisk[key] {
				sharedDisks++
				continue
			}
			seenDisk[key] = true
			provisioned += float64(disk.CapacityBytes)
		}
	}

	// Source datastore rollup, once per identity group.
	groupSet := map[int]*StorageGroup{}
	var gorder []int
	for i, info := range infos {
		g := find(i)
		if !touched[g] {
			continue
		}
		sg := groupSet[g]
		if sg == nil {
			sg = &StorageGroup{Ambiguous: ambiguous[g]}
			groupSet[g] = sg
			gorder = append(gorder, g)
			sg.CapacityBytes, sg.UsedBytes = info.ds.CapacityBytes, max(info.ds.CapacityBytes-info.ds.FreeBytes, 0)
		}
		sg.Aliases = append(sg.Aliases, info.ds.Context+"/"+info.ds.Name)
		sg.Identity = uniqueStrings(append(sg.Identity, info.ds.IdentityKeys()...))
	}
	sort.Ints(gorder)
	for _, g := range gorder {
		sort.Strings(groupSet[g].Aliases)
		out.groups = append(out.groups, *groupSet[g])
	}
	sort.SliceStable(out.groups, func(i, j int) bool { return out.groups[i].Aliases[0] < out.groups[j].Aliases[0] })

	// Storage coverage.
	dsCov := append([]string(nil), vmGaps...)
	dsCov = append(dsCov, dsGaps...)
	if noDisks > 0 {
		dsCov = append(dsCov, fmt.Sprintf("%d in-scope VM(s) have no disk configuration recorded, so their storage is unknown", noDisks))
	}
	dsCov = append(dsCov, dedupe(dsDiskGaps)...)
	rdmCov := append([]string(nil), vmGaps...)
	if noDisks > 0 {
		rdmCov = append(rdmCov, fmt.Sprintf("%d in-scope VM(s) have no disk configuration recorded, so RDMs may be missing", noDisks))
	}
	rdmCov = append(rdmCov, dedupe(rdmGaps)...)
	out.gaps = append(out.gaps, dsCov[len(vmGaps):]...)
	out.gaps = append(out.gaps, rdmCov[len(vmGaps):]...)

	// Datastore dimension.
	ds := Dimension{Name: "datastore-capacity", Unit: "GiB"}
	var missing []string
	if e.sc.DatastoreCapacityBytes == nil {
		missing = append(missing, "target datastore capacity not provided")
	}
	req := provisioned * (1 + e.growth/100)
	ds.Required = fp(req / gib)
	ds.Formula = fmt.Sprintf("required GiB = sum(provisioned capacity of datastore-backed disks, shared disks and shared datastores counted once) x (1 + growth%%/100) = %.6g x %.4g = %.6g", provisioned/gib, 1+e.growth/100, req/gib)
	ok := len(missing) == 0
	fits := false
	if ok {
		supply := *e.sc.DatastoreCapacityBytes
		ds.Supply = fp(supply / gib)
		ds.Headroom = fp((supply - req) / gib)
		fits = req <= supply
		ds.Formula += fmt.Sprintf("; supply = target datastore capacity = %.6g", supply/gib)
		if observed*(1+e.growth/100) <= supply {
			ds.Reasons = append(ds.Reasons, "observed datastore use alone would fit; the verdict is on provisioned size because the target thin-provisioning behaviour is not an input")
		}
	}
	if sharedDisks > 0 {
		ds.Reasons = append(ds.Reasons, fmt.Sprintf("%d disk(s) attached to more than one VM were counted once", sharedDisks))
	}
	finish(&ds, missing, dsCov, ok, fits)
	out.dimensions = append(out.dimensions, ds)

	// RDM dimension.
	rd := Dimension{Name: "rdm-capacity", Unit: "GiB"}
	rreq := rdmBytes * (1 + e.growth/100)
	rd.Required = fp(rreq / gib)
	rd.Formula = fmt.Sprintf("required GiB = sum(RDM LUN capacity, one per LUN identity) x (1 + growth%%/100) = %.6g x %.4g = %.6g; RDM capacity is never added to datastore capacity (only its small mapping file lives on a datastore)", rdmBytes/gib, 1+e.growth/100, rreq/gib)
	var rmissing []string
	rok := true
	rfits := true
	if rdmCount > 0 {
		if e.sc.RDMCapacityBytes == nil {
			rmissing = append(rmissing, fmt.Sprintf("%d RDM disk(s) in scope but no target RDM/LUN capacity provided", rdmCount))
			rok = false
		} else {
			rd.Supply = fp(*e.sc.RDMCapacityBytes / gib)
			rd.Headroom = fp((*e.sc.RDMCapacityBytes - rreq) / gib)
			rfits = rreq <= *e.sc.RDMCapacityBytes
			rd.Formula += fmt.Sprintf("; supply = target RDM capacity = %.6g", *e.sc.RDMCapacityBytes/gib)
		}
	} else {
		rd.Reasons = append(rd.Reasons, "no RDMs in scope")
	}
	if sharedRDMs > 0 {
		rd.Reasons = append(rd.Reasons, fmt.Sprintf("%d RDM attachment(s) to an already counted LUN were not counted again", sharedRDMs))
	}
	finish(&rd, rmissing, rdmCov, rok, rfits)
	out.dimensions = append(out.dimensions, rd)

	// Guest filesystem use, split by backing class, informational only.
	var guestDS, guestRDM, guestUnattr float64
	withParts, powered := 0, 0
	for _, v := range vms {
		if v.vm.PowerState == "poweredOn" {
			powered++
		}
		if len(v.vm.Partitions) == 0 {
			continue
		}
		withParts++
		vmKey := v.ctx + "/" + v.vm.Name + "/" + v.vm.ID
		for _, p := range v.vm.Partitions {
			used := float64(p.UsedBytes())
			var nds, nrdm int
			for _, k := range p.DiskKeys {
				if rdmKeys[vmKey][k] {
					nrdm++
				} else if dsKeys[vmKey][k] {
					nds++
				}
			}
			switch {
			case len(p.DiskKeys) > 0 && nrdm == len(p.DiskKeys):
				guestRDM += used
			case len(p.DiskKeys) > 0 && nds == len(p.DiskKeys):
				guestDS += used
			default:
				guestUnattr += used
			}
		}
	}
	guestNote := "guest view from VMware Tools; not additive with provisioned or observed datastore use"
	out.measures = append(out.measures,
		Measure{Name: "datastore-provisioned", Value: provisioned / gib, Unit: "GiB", Basis: "provisioned disk capacity, datastore-backed, deduplicated", InVerdict: true},
		Measure{Name: "datastore-observed-use", Value: observed / gib, Unit: "GiB", Basis: "vSphere committed storage per VM", Note: "includes snapshots and swap and counts a shared disk once per VM; informational, not the verdict basis"},
		Measure{Name: "rdm-capacity", Value: rdmBytes / gib, Unit: "GiB", Basis: "raw LUN capacity, one per LUN identity", InVerdict: true},
		Measure{Name: "guest-fs-used-datastore-backed", Value: guestDS / gib, Unit: "GiB", Basis: "guest filesystem used", Note: guestNote, Populated: withParts, OutOfTotal: powered},
		Measure{Name: "guest-fs-used-rdm-backed", Value: guestRDM / gib, Unit: "GiB", Basis: "guest filesystem used", Note: guestNote},
		Measure{Name: "guest-fs-used-unattributed", Value: guestUnattr / gib, Unit: "GiB", Basis: "guest filesystem used", Note: "disk mapping unavailable (needs VMware Tools on vSphere 7.0 or later) or spans classes"},
	)
	return out
}

func isRDM(d vsphere.VMDisk) bool {
	return d.Raw || strings.EqualFold(d.BackingType, "rdm") || d.RawLUNID != ""
}

func (e *evaluator) sourceHosts(clusters map[string]bool) SourceHosts {
	var sh SourceHosts
	for _, r := range e.data.Resources {
		if r.Kind != "host" {
			continue
		}
		var h vsphere.Host
		if json.Unmarshal(r.Payload, &h) != nil {
			continue
		}
		if len(e.sc.Clusters) > 0 && !clusters[r.Context+"/"+h.Cluster] {
			continue
		}
		sh.Observed = true
		sh.Hosts++
		sh.Cores += int64(h.CPUCores)
		sh.RAMGiB += float64(h.MemoryMB) / 1024
	}
	return sh
}

func (e *evaluator) readiness(vms []inScopeVM) ReadinessLinks {
	links := ReadinessLinks{
		MigrationCommand:    "vsfleet assessment readiness " + fmt.Sprint(e.data.Run.ID),
		NetworkCommand:      "vsfleet assessment network-readiness --source <cluster> --target <cluster> " + fmt.Sprint(e.data.Run.ID),
		NetworkNotEvaluated: "network readiness needs a source and target cluster mapping and is not evaluated here",
	}
	scoped := map[string]bool{}
	networks := map[string]bool{}
	for _, v := range vms {
		scoped[v.ctx+"\x00"+v.vm.ID] = true
		for _, nic := range v.vm.NICs {
			if n := nonempty(nic.Network, nic.NetworkID); n != "" {
				networks[v.ctx+"/"+n] = true
			}
		}
	}
	links.DistinctNetworks = len(networks)
	report := health.Evaluate(e.data, health.Options{Thresholds: health.DefaultThresholds()})
	ready := health.Readiness(report)
	links.Verdict = ready.Verdict
	links.UnresolvedRules = len(ready.Unresolved)
	links.FindingsByRule = map[string]int{}
	count := func(fs []health.Finding, blockers bool) {
		for _, f := range fs {
			if f.Object.Kind != "vm" || !scoped[f.Object.Context+"\x00"+f.Object.ID] {
				continue
			}
			if blockers {
				links.Blockers++
			} else {
				links.Advisories++
			}
			links.FindingsByRule[f.Rule]++
			if f.Rule == "rdm-present" {
				links.RDMsRequireExplicit = true
			}
		}
	}
	count(ready.Blockers, true)
	count(ready.Advisories, false)
	if len(links.FindingsByRule) == 0 {
		links.FindingsByRule = nil
	}
	return links
}

func clusterSelected(filters []string, ctx, cluster string) bool {
	for _, f := range filters {
		f = strings.TrimSpace(f)
		if strings.EqualFold(f, cluster) || strings.EqualFold(f, ctx+"/"+cluster) {
			return true
		}
	}
	return false
}

func nonempty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func head(v []string, n int) []string {
	if len(v) <= n {
		return v
	}
	return append(append([]string(nil), v[:n]...), fmt.Sprintf("and %d more", len(v)-n))
}

func dedupe(v []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range v {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func uniqueStrings(v []string) []string {
	out := dedupe(v)
	sort.Strings(out)
	return out
}
