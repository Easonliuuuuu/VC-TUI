package assessment

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

type AttributionBasis string

const (
	BasisExact        AttributionBasis = "exact"
	BasisInferred     AttributionBasis = "inferred"
	BasisSplit        AttributionBasis = "split"
	BasisUnattributed AttributionBasis = "unattributed"
)

type CapacityConfidence string

const (
	CapacityAttributed CapacityConfidence = "attributed"
	CapacityPartial    CapacityConfidence = "partial"
	CapacityUnknown    CapacityConfidence = "unknown-incomplete-coverage"
)

type ProjectionConfidence string

const (
	ProjectionProjected ProjectionConfidence = "projected"
	ProjectionLow       ProjectionConfidence = "low-confidence"
	ProjectionUnknown   ProjectionConfidence = "unknown"
)

type GrowthContributor struct {
	Name       string           `json:"name"`
	Context    string           `json:"context,omitempty"`
	VCenterID  string           `json:"vcenter_id,omitempty"`
	Kind       string           `json:"kind"`
	Basis      AttributionBasis `json:"basis"`
	DeltaBytes float64          `json:"delta_bytes"`
	Paths      []string         `json:"paths,omitempty"`
}

type CapacityBlindness struct {
	Context string `json:"context"`
	Reason  string `json:"reason"`
}

type GrowthAnomaly struct {
	FromRunID         int64   `json:"from_run_id"`
	ToRunID           int64   `json:"to_run_id"`
	BytesPerDay       float64 `json:"bytes_per_day"`
	MedianBytesPerDay float64 `json:"median_bytes_per_day"`
	DeltaBytes        float64 `json:"delta_bytes"`
}

type CapacityProjection struct {
	Confidence       ProjectionConfidence `json:"confidence"`
	Method           string               `json:"method"`
	TargetFreeBytes  float64              `json:"target_free_bytes"`
	TargetBasis      string               `json:"target_basis"`
	SlopeBytesPerDay float64              `json:"slope_bytes_per_day"`
	RSquared         float64              `json:"r_squared"`
	Points           int                  `json:"points"`
	SpanDays         float64              `json:"span_days"`
	CrossesAt        *time.Time           `json:"crosses_at,omitempty"`
	DaysRemaining    *float64             `json:"days_remaining,omitempty"`
	Reasons          []string             `json:"reasons,omitempty"`
}

type Object struct {
	Name       string `json:"name"`
	ID         string `json:"id"`
	Context    string `json:"context"`
	VCenterID  string `json:"vcenter_id"`
	Datacenter string `json:"datacenter,omitempty"`
}

type DatastoreCapacity struct {
	Object               Object              `json:"object"`
	Identity             []string            `json:"identity,omitempty"`
	CapacityBytes        *float64            `json:"capacity_bytes,omitempty"`
	FreeBytes            *float64            `json:"free_bytes,omitempty"`
	FreePercent          *float64            `json:"free_percent,omitempty"`
	FirstRun             Run                 `json:"first_run"`
	LastRun              Run                 `json:"last_run"`
	SpanDays             float64             `json:"span_days"`
	UsedGrowthBytes      *float64            `json:"used_growth_bytes,omitempty"`
	CapacityChangedBytes float64             `json:"capacity_changed_bytes"`
	Confidence           CapacityConfidence  `json:"confidence"`
	Contributors         []GrowthContributor `json:"contributors,omitempty"`
	UnattributedBytes    float64             `json:"unattributed_bytes"`
	Anomalies            []GrowthAnomaly     `json:"anomalies,omitempty"`
	Projection           *CapacityProjection `json:"projection,omitempty"`
	Blind                []CapacityBlindness `json:"blind,omitempty"`
	Reasons              []string            `json:"reasons,omitempty"`
}

type CapacityThresholds struct {
	FreePercent float64 `json:"min_free_percent"`
	FreeBytes   float64 `json:"min_free_bytes"`
}

type CapacityReport struct {
	SchemaVersion int                 `json:"schema_version"`
	Window        TrendWindow         `json:"window"`
	Thresholds    CapacityThresholds  `json:"thresholds"`
	Units         map[string]string   `json:"units"`
	Coverage      []CoverageIssue     `json:"coverage"`
	Datastores    []DatastoreCapacity `json:"datastores"`
}

type capacityDS struct {
	resource ResourceObservation
	ds       vsphere.Datastore
	run      Run
	key      string
	identity []string
}

// capacityContext is the display identity of a context: a human-readable name
// plus the vCenter it was observed in. It is carried alongside the internal
// composite key so blindness diagnostics never surface the NUL-delimited key.
type capacityContext struct {
	Name      string
	VCenterID string
}

type capacityGroup struct {
	items    []capacityDS
	contexts map[string]capacityContext
	identity map[string]bool
}

type capacityPoint struct {
	run Run
	ds  capacityDS
}

type capacityVM struct {
	key string
	vm  vsphere.VM
	obs Observation
}

const capacityGiB = float64(1 << 30)

// CapacityReport attributes datastore used-space growth using only evidence
// persisted by the assessment ledger. It deliberately keeps coverage and
// attribution uncertainty visible instead of turning missing collections into
// zero-valued observations.
func (s *Store) CapacityReport(ctx context.Context, opts TrendOptions, thresholds CapacityThresholds) (CapacityReport, error) {
	if thresholds.FreePercent < 0 || thresholds.FreePercent > 100 {
		return CapacityReport{}, fmt.Errorf("capacity free-percent threshold must be between 0 and 100")
	}
	if thresholds.FreeBytes < 0 {
		return CapacityReport{}, fmt.Errorf("capacity free-bytes threshold must be zero or greater")
	}
	runs, err := s.contributingTrendRuns(ctx, opts)
	if err != nil {
		return CapacityReport{}, err
	}
	report := CapacityReport{
		SchemaVersion: 1,
		Window:        trendWindow(opts, runs),
		Thresholds:    thresholds,
		Units: map[string]string{
			"capacity_bytes": "bytes", "free_bytes": "bytes", "used_growth_bytes": "bytes",
			"slope_bytes_per_day": "bytes/day", "free_percent": "%",
		},
		Coverage:   make([]CoverageIssue, 0),
		Datastores: make([]DatastoreCapacity, 0),
	}
	if len(runs) == 0 {
		return report, nil
	}

	resourcesByRun := make(map[int64]resourceRunData, len(runs))
	vmsByRun := make(map[int64][]capacityVM, len(runs))
	contextsByRun := make(map[int64]map[string]ContextRun, len(runs))
	for _, run := range runs {
		resources, err := s.loadResources(ctx, run.ID)
		if err != nil {
			return CapacityReport{}, err
		}
		resourcesByRun[run.ID] = resources
		vms, contexts, err := s.loadVMs(ctx, run.ID)
		if err != nil {
			return CapacityReport{}, err
		}
		contextsByRun[run.ID] = make(map[string]ContextRun)
		for contextID, contextRun := range contexts {
			if !trendContextAllowed(contextRun.Name, opts.Contexts) {
				continue
			}
			contextsByRun[run.ID][capacityContextKey(contextRun.Name, contextRun.VCenterID)] = contextRun
			for _, item := range vms[contextID] {
				if trendContextAllowed(item.observation.Context, opts.Contexts) {
					vmsByRun[run.ID] = append(vmsByRun[run.ID], capacityVM{key: capacityVMKey(item.observation), vm: item.observation.VM, obs: item.observation})
				}
			}
		}
		appendCapacityCoverage(&report, run, resources.Coverage, contextsByRun[run.ID])
	}

	var observations []capacityDS
	for _, run := range runs {
		data := resourcesByRun[run.ID]
		for _, stored := range data.ByKind["datastore"] {
			if !trendContextAllowed(stored.observation.Context, opts.Contexts) {
				continue
			}
			var datastore vsphere.Datastore
			if !DecodeResource(stored.observation, &datastore) {
				continue
			}
			observations = append(observations, capacityDS{
				resource: stored.observation, ds: datastore, run: run,
				key:      stored.observation.VCenterID + "\x00" + stored.observation.ID,
				identity: DatastoreIdentity(datastore),
			})
		}
	}
	groups := groupCapacityDatastores(observations)
	for _, group := range groups {
		result := buildDatastoreCapacity(group, runs, vmsByRun, contextsByRun, thresholds, opts.IncludePartial)
		report.Datastores = append(report.Datastores, result)
	}
	sort.SliceStable(report.Datastores, func(i, j int) bool {
		a, b := report.Datastores[i].Object, report.Datastores[j].Object
		for _, pair := range [][2]string{{a.Context, b.Context}, {a.Name, b.Name}, {a.ID, b.ID}} {
			if !strings.EqualFold(pair[0], pair[1]) {
				return strings.ToLower(pair[0]) < strings.ToLower(pair[1])
			}
		}
		return a.VCenterID < b.VCenterID
	})
	return report, nil
}

func appendCapacityCoverage(report *CapacityReport, run Run, coverage map[string]CollectionRun, contexts map[string]ContextRun) {
	for _, contextRun := range contexts {
		contextName := contextRun.Name
		for _, kind := range []string{"vm", "datastore"} {
			status := ""
			message := ""
			if kind == "vm" {
				status, message = contextRun.VMStatus, contextRun.Error
				seen := false
				for _, collection := range contextRun.Collections {
					if collection.Kind == "vm" {
						seen = true
						status, message = collection.Status, collection.Error
						break
					}
				}
				if !seen {
					status = ""
				}
			} else if collection, ok := coverage[contextName+"\x00"+kind]; ok {
				status, message = collection.Status, collection.Error
			}
			if status == "" {
				report.Coverage = append(report.Coverage, CoverageIssue{Scope: "capacity", Context: contextName, Message: fmt.Sprintf("run %d %s collection was not recorded", run.ID, kind)})
			} else if !Successful(status) {
				report.Coverage = append(report.Coverage, CoverageIssue{Scope: "capacity", Context: contextName, Message: fmt.Sprintf("run %d %s collection: %s", run.ID, kind, capacityNonempty(message, status))})
			}
		}
	}
}

func groupCapacityDatastores(values []capacityDS) []capacityGroup {
	parent := make([]int, len(values))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(a, b int) {
		a, b = find(a), find(b)
		if a != b {
			parent[b] = a
		}
	}
	for i := range values {
		for j := i + 1; j < len(values); j++ {
			if values[i].key == values[j].key || identityOverlap(values[i].identity, values[j].identity) {
				union(i, j)
			}
		}
	}
	byRoot := make(map[int]*capacityGroup)
	for i, value := range values {
		root := find(i)
		group := byRoot[root]
		if group == nil {
			group = &capacityGroup{contexts: make(map[string]capacityContext), identity: make(map[string]bool)}
			byRoot[root] = group
		}
		group.items = append(group.items, value)
		group.contexts[capacityContextKey(value.resource.Context, value.resource.VCenterID)] = capacityContext{Name: value.resource.Context, VCenterID: value.resource.VCenterID}
		for _, key := range value.identity {
			group.identity[key] = true
		}
	}
	groups := make([]capacityGroup, 0, len(byRoot))
	for _, group := range byRoot {
		sort.SliceStable(group.items, func(i, j int) bool {
			if !group.items[i].run.StartedAt.Equal(group.items[j].run.StartedAt) {
				return group.items[i].run.StartedAt.Before(group.items[j].run.StartedAt)
			}
			return group.items[i].key < group.items[j].key
		})
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool { return capacityGroupKey(groups[i]) < capacityGroupKey(groups[j]) })
	return groups
}

func capacityGroupKey(group capacityGroup) string {
	if len(group.items) == 0 {
		return ""
	}
	return group.items[0].key
}

func identityOverlap(a, b []string) bool {
	seen := make(map[string]bool, len(a))
	for _, value := range a {
		seen[value] = true
	}
	for _, value := range b {
		if seen[value] {
			return true
		}
	}
	return false
}

func buildDatastoreCapacity(group capacityGroup, runs []Run, vmsByRun map[int64][]capacityVM, contextsByRun map[int64]map[string]ContextRun, thresholds CapacityThresholds, includePartial bool) DatastoreCapacity {
	points := make([]capacityPoint, 0, len(runs))
	for _, run := range runs {
		candidates := make([]capacityDS, 0)
		for _, item := range group.items {
			if item.run.ID == run.ID && item.ds.CapacityBytes >= 0 && item.ds.FreeBytes >= 0 {
				candidates = append(candidates, item)
			}
		}
		if len(candidates) > 0 {
			sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].key < candidates[j].key })
			points = append(points, capacityPoint{run: run, ds: candidates[0]})
		}
	}
	result := DatastoreCapacity{Confidence: CapacityUnknown, Contributors: make([]GrowthContributor, 0), Anomalies: make([]GrowthAnomaly, 0), Blind: make([]CapacityBlindness, 0), Reasons: make([]string, 0)}
	if len(points) == 0 {
		result.Reasons = append(result.Reasons, "datastore capacity and free space were not reported")
		result.Projection = unknownProjection("no usable capacity points")
		return result
	}
	first, last := points[0], points[len(points)-1]
	result.Object = capacityObject(last.ds)
	result.Identity = sortedKeys(group.identity)
	result.FirstRun, result.LastRun = first.run, last.run
	result.SpanDays = last.run.StartedAt.Sub(first.run.StartedAt).Hours() / 24
	capacity := float64(last.ds.ds.CapacityBytes)
	free := float64(last.ds.ds.FreeBytes)
	capacityPtr, freePtr, freePercent := capacity, free, 0.0
	if capacity > 0 {
		freePercent = free / capacity * 100
	}
	result.CapacityBytes, result.FreeBytes, result.FreePercent = &capacityPtr, &freePtr, &freePercent
	if len(points) >= 2 {
		usedFirst := float64(first.ds.ds.UsedBytes())
		usedLast := float64(last.ds.ds.UsedBytes())
		growth := usedLast - usedFirst
		result.UsedGrowthBytes = &growth
		result.CapacityChangedBytes = capacity - float64(first.ds.ds.CapacityBytes)
		if result.CapacityChangedBytes != 0 {
			result.Reasons = append(result.Reasons, fmt.Sprintf("datastore capacity changed by %.0f bytes during the window", result.CapacityChangedBytes))
		}
		if !strings.EqualFold(first.ds.ds.Name, last.ds.ds.Name) {
			result.Reasons = append(result.Reasons, fmt.Sprintf("datastore was renamed from %q to %q", first.ds.ds.Name, last.ds.ds.Name))
		}
		result.Contributors = attributeGrowth(group, first, last, vmsByRun, growth)
		var total float64
		for _, contributor := range result.Contributors {
			total += contributor.DeltaBytes
		}
		result.UnattributedBytes = growth - total
		if math.Abs(result.UnattributedBytes) > 0.5 || len(result.Contributors) == 0 {
			result.Contributors = append(result.Contributors, GrowthContributor{Name: "unattributed", Kind: "file", Basis: BasisUnattributed, DeltaBytes: result.UnattributedBytes})
		}
		result.Anomalies = capacityAnomalies(points)
	} else {
		result.Reasons = append(result.Reasons, "fewer than two usable runs")
		result.UnattributedBytes = 0
	}

	result.Blind = capacityBlindness(group, runs, contextsByRun)
	if len(result.Blind) > 0 || len(points) < 2 {
		result.Confidence = CapacityUnknown
	} else if result.UsedGrowthBytes != nil && math.Abs(result.UnattributedBytes) <= math.Abs(*result.UsedGrowthBytes)*0.05 {
		result.Confidence = CapacityAttributed
	} else if len(result.Contributors) > 0 {
		result.Confidence = CapacityPartial
	}
	result.Projection = capacityProjection(points, thresholds, includePartial, capacityChangedDuringWindow(points))
	return result
}

func capacityObject(item capacityDS) Object {
	return Object{Name: item.ds.Name, ID: item.ds.ID, Context: item.resource.Context, VCenterID: item.resource.VCenterID, Datacenter: item.ds.Datacenter}
}

func attributeGrowth(group capacityGroup, first, last capacityPoint, vmsByRun map[int64][]capacityVM, growth float64) []GrowthContributor {
	firstVMs := groupVMs(group, vmsByRun[first.run.ID])
	lastVMs := groupVMs(group, vmsByRun[last.run.ID])
	firstByKey := make(map[string]capacityVM)
	lastByKey := make(map[string]capacityVM)
	for _, item := range firstVMs {
		firstByKey[item.key] = item
	}
	for _, item := range lastVMs {
		lastByKey[item.key] = item
	}
	keys := make(map[string]bool)
	for key := range firstByKey {
		keys[key] = true
	}
	for key := range lastByKey {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	contributors := make([]GrowthContributor, 0)
	common := make(map[string]bool)
	for _, key := range ordered {
		_, beforeOK := firstByKey[key]
		_, afterOK := lastByKey[key]
		if beforeOK && afterOK {
			common[key] = true
		}
	}

	exactDeltas, exactPaths, exactNames := exactFileDeltas(group, first, last, common, firstByKey, lastByKey)
	for _, key := range ordered {
		before, beforeOK := firstByKey[key]
		after, afterOK := lastByKey[key]
		if beforeOK && afterOK {
			if delta, ok := exactDeltas[key]; ok {
				item := after
				contributors = append(contributors, vmContributor(item, exactNames[key], BasisExact, delta, exactPaths[key]))
				continue
			}
			if delta, basis, ok := inferredVMDelta(before, after, group); ok {
				contributors = append(contributors, vmContributor(after, after.vm.Name, basis, delta, nil))
			}
			continue
		}
		item := after
		sign := 1.0
		if !afterOK {
			item, sign = before, -1
		}
		if delta, basis, ok := appearedVMDelta(item, group); ok {
			contributors = append(contributors, vmContributor(item, item.vm.Name, basis, sign*delta, nil))
		}
	}
	contributors = append(contributors, exactFileContributors(group, first, last, common, firstByKey, lastByKey)...)
	if len(contributors) == 0 && growth == 0 {
		return contributors
	}
	return mergeContributors(contributors)
}

func groupVMs(group capacityGroup, values []capacityVM) []capacityVM {
	allowed := make(map[string]bool, len(group.contexts))
	for key := range group.contexts {
		allowed[key] = true
	}
	out := make([]capacityVM, 0)
	for _, value := range values {
		if allowed[capacityContextKey(value.obs.Context, value.obs.VCenterID)] {
			out = append(out, value)
		}
	}
	return out
}

func exactFileDeltas(group capacityGroup, first, last capacityPoint, common map[string]bool, firstByKey, lastByKey map[string]capacityVM) (map[string]float64, map[string][]string, map[string]string) {
	deltas := make(map[string]float64)
	paths := make(map[string][]string)
	names := make(map[string]string)
	if first.ds.ds.BrowseStatus != "success" || last.ds.ds.BrowseStatus != "success" || first.ds.ds.BrowseTruncated || last.ds.ds.BrowseTruncated {
		return deltas, paths, names
	}
	beforeFiles := datastoreFilesForGroup(group, first)
	afterFiles := datastoreFilesForGroup(group, last)
	filePaths := make(map[string]bool)
	for path := range beforeFiles {
		filePaths[path] = true
	}
	for path := range afterFiles {
		filePaths[path] = true
	}
	for path := range filePaths {
		delta := float64(afterFiles[path] - beforeFiles[path])
		owner := fileOwner(path, group, firstByKey, lastByKey)
		if owner != "" && common[owner] {
			deltas[owner] += delta
			paths[owner] = append(paths[owner], path)
			if names[owner] == "" {
				if vm, ok := lastByKey[owner]; ok {
					names[owner] = vm.vm.Name
				} else {
					names[owner] = firstByKey[owner].vm.Name
				}
			}
		}
	}
	return deltas, paths, names
}

func datastoreFilesForGroup(group capacityGroup, point capacityPoint) map[string]int64 {
	out := make(map[string]int64)
	for _, item := range group.items {
		if item.run.ID != point.run.ID || item.ds.BrowseStatus != "success" || item.ds.BrowseTruncated {
			continue
		}
		for _, file := range item.ds.Files {
			_, relative, ok := vsphere.SplitDatastorePath(file.Path)
			if ok {
				out[vsphere.NormalizeRelativePath(relative)] = file.SizeBytes
			}
		}
	}
	return out
}

func fileOwner(pathValue string, group capacityGroup, before, after map[string]capacityVM) string {
	for key, item := range before {
		if vmHasRelativeDisk(item.vm, pathValue, group) {
			return key
		}
	}
	for key, item := range after {
		if vmHasRelativeDisk(item.vm, pathValue, group) {
			return key
		}
	}
	return ""
}

func vmHasRelativeDisk(vm vsphere.VM, pathValue string, group capacityGroup) bool {
	for _, disk := range vm.Disks {
		name, relative, ok := vsphere.SplitDatastorePath(disk.BackingPath)
		if !ok || vsphere.NormalizeRelativePath(relative) != pathValue {
			continue
		}
		for _, item := range group.items {
			if strings.EqualFold(name, item.ds.Name) {
				return true
			}
		}
	}
	return false
}

func exactFileContributors(group capacityGroup, first, last capacityPoint, common map[string]bool, firstByKey, lastByKey map[string]capacityVM) []GrowthContributor {
	if first.ds.ds.BrowseStatus != "success" || last.ds.ds.BrowseStatus != "success" || first.ds.ds.BrowseTruncated || last.ds.ds.BrowseTruncated {
		return nil
	}
	before, after := datastoreFilesForGroup(group, first), datastoreFilesForGroup(group, last)
	paths := make(map[string]bool)
	for path := range before {
		paths[path] = true
	}
	for path := range after {
		paths[path] = true
	}
	var out []GrowthContributor
	for path := range paths {
		owner := fileOwner(path, group, firstByKey, lastByKey)
		if owner != "" && common[owner] {
			continue
		}
		if owner != "" {
			continue
		}
		delta := float64(after[path] - before[path])
		out = append(out, GrowthContributor{Name: path, Context: last.ds.resource.Context, VCenterID: last.ds.resource.VCenterID, Kind: "file", Basis: BasisExact, DeltaBytes: delta, Paths: []string{path}})
	}
	return out
}

func inferredVMDelta(before, after capacityVM, group capacityGroup) (float64, AttributionBasis, bool) {
	if len(after.vm.Datastores) == 1 && datastoreNameInGroup(after.vm.Datastores[0], group) {
		return (after.vm.StorageGB - before.vm.StorageGB) * capacityGiB, BasisInferred, true
	}
	if len(after.vm.Datastores) > 1 && vmOnGroup(after.vm, group) {
		return diskCapacityDelta(before.vm, after.vm, group), BasisSplit, true
	}
	return 0, "", false
}

func appearedVMDelta(item capacityVM, group capacityGroup) (float64, AttributionBasis, bool) {
	if len(item.vm.Datastores) == 1 && datastoreNameInGroup(item.vm.Datastores[0], group) {
		return vmSize(item.vm), BasisInferred, true
	}
	if len(item.vm.Datastores) > 1 && vmOnGroup(item.vm, group) {
		return diskCapacityForGroup(item.vm, group), BasisSplit, true
	}
	return 0, "", false
}

// vmOnGroup reports whether any datastore the VM lives on belongs to the group,
// so a multi-datastore VM is never listed against datastores it has no files on.
func vmOnGroup(vm vsphere.VM, group capacityGroup) bool {
	for _, name := range vm.Datastores {
		if datastoreNameInGroup(name, group) {
			return true
		}
	}
	return false
}

func datastoreNameInGroup(name string, group capacityGroup) bool {
	for _, item := range group.items {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(item.ds.Name)) {
			return true
		}
	}
	return false
}

func vmSize(vm vsphere.VM) float64 {
	if vm.StorageGB != 0 {
		return vm.StorageGB * capacityGiB
	}
	var total float64
	for _, disk := range vm.Disks {
		total += float64(disk.CapacityBytes)
	}
	return total
}

func diskCapacityDelta(before, after vsphere.VM, group capacityGroup) float64 {
	return diskCapacityForGroup(after, group) - diskCapacityForGroup(before, group)
}

func diskCapacityForGroup(vm vsphere.VM, group capacityGroup) float64 {
	var total float64
	for _, disk := range vm.Disks {
		name, _, ok := vsphere.SplitDatastorePath(disk.BackingPath)
		if ok && datastoreNameInGroup(name, group) {
			total += float64(disk.CapacityBytes)
		}
	}
	return total
}

func vmContributor(item capacityVM, name string, basis AttributionBasis, delta float64, paths []string) GrowthContributor {
	kind := "vm"
	if item.vm.IsTemplate {
		kind = "template"
	}
	sort.Strings(paths)
	return GrowthContributor{Name: name, Context: item.obs.Context, VCenterID: item.obs.VCenterID, Kind: kind, Basis: basis, DeltaBytes: delta, Paths: paths}
}

func mergeContributors(values []GrowthContributor) []GrowthContributor {
	byKey := make(map[string]GrowthContributor)
	for _, value := range values {
		key := value.Kind + "\x00" + value.Context + "\x00" + value.VCenterID + "\x00" + value.Name + "\x00" + string(value.Basis)
		current := byKey[key]
		current.Name, current.Context, current.VCenterID, current.Kind, current.Basis = value.Name, value.Context, value.VCenterID, value.Kind, value.Basis
		current.DeltaBytes += value.DeltaBytes
		current.Paths = append(current.Paths, value.Paths...)
		byKey[key] = current
	}
	out := make([]GrowthContributor, 0, len(byKey))
	for _, value := range byKey {
		sort.Strings(value.Paths)
		value.Paths = uniqueStrings(value.Paths)
		out = append(out, value)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if math.Abs(out[i].DeltaBytes-out[j].DeltaBytes) > 0.5 {
			return out[i].DeltaBytes > out[j].DeltaBytes
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// groupContextsByRun resolves which context identity actually applies to the
// group in each run. Runs where the group reported a datastore use that run's
// own context/vCenter; runs with no datastore observation carry the identity
// from the nearest run that has one, so a legitimate vCenter-ID transition is
// attributed to the runs it spans rather than compared against every run.
func groupContextsByRun(group capacityGroup, runs []Run) map[int64]map[string]capacityContext {
	observed := make(map[int64]map[string]capacityContext)
	for _, item := range group.items {
		key := capacityContextKey(item.resource.Context, item.resource.VCenterID)
		if observed[item.run.ID] == nil {
			observed[item.run.ID] = make(map[string]capacityContext)
		}
		observed[item.run.ID][key] = capacityContext{Name: item.resource.Context, VCenterID: item.resource.VCenterID}
	}
	out := make(map[int64]map[string]capacityContext, len(runs))
	for i, run := range runs {
		if len(observed[run.ID]) > 0 {
			out[run.ID] = observed[run.ID]
			continue
		}
		for offset := 1; offset < len(runs); offset++ {
			if j := i - offset; j >= 0 && len(observed[runs[j].ID]) > 0 {
				out[run.ID] = observed[runs[j].ID]
				break
			}
			if j := i + offset; j < len(runs) && len(observed[runs[j].ID]) > 0 {
				out[run.ID] = observed[runs[j].ID]
				break
			}
		}
	}
	return out
}

func capacityBlindness(group capacityGroup, runs []Run, contextsByRun map[int64]map[string]ContextRun) []CapacityBlindness {
	var out []CapacityBlindness
	perRun := groupContextsByRun(group, runs)
	for _, run := range runs {
		for contextKey, identity := range perRun[run.ID] {
			contextRun, ok := contextsByRun[run.ID][contextKey]
			if !ok {
				out = appendUniqueBlindness(out, CapacityBlindness{Context: identity.Name, Reason: fmt.Sprintf("run %d context was not recorded", run.ID)})
				continue
			}
			vmSeen := false
			if !Successful(contextRun.VMStatus) {
				out = appendUniqueBlindness(out, CapacityBlindness{Context: contextRun.Name, Reason: "vm collection: " + capacityNonempty(contextRun.Error, contextRun.VMStatus)})
			}
			for _, collection := range contextRun.Collections {
				if collection.Kind == "vm" {
					vmSeen = true
					if !Successful(collection.Status) {
						out = appendUniqueBlindness(out, CapacityBlindness{Context: contextRun.Name, Reason: "vm collection: " + capacityNonempty(collection.Error, collection.Status)})
					}
				}
			}
			if !vmSeen {
				out = appendUniqueBlindness(out, CapacityBlindness{Context: contextRun.Name, Reason: "vm collection was not recorded"})
			}
			status := ""
			for _, collection := range contextRun.Collections {
				if collection.Kind == "datastore" {
					status = collection.Status
					if !Successful(status) {
						out = appendUniqueBlindness(out, CapacityBlindness{Context: contextRun.Name, Reason: "datastore collection: " + capacityNonempty(collection.Error, collection.Status)})
					}
					break
				}
			}
			if status == "" {
				out = appendUniqueBlindness(out, CapacityBlindness{Context: contextRun.Name, Reason: "datastore collection was not recorded"})
			}
		}
	}
	return out
}

func capacityAnomalies(points []capacityPoint) []GrowthAnomaly {
	if len(points) < 5 {
		return nil
	}
	rates := make([]float64, 0, len(points)-1)
	intervals := make([]GrowthAnomaly, 0, len(points)-1)
	for i := 1; i < len(points); i++ {
		days := points[i].run.StartedAt.Sub(points[i-1].run.StartedAt).Hours() / 24
		if days <= 0 {
			continue
		}
		delta := float64(points[i].ds.ds.UsedBytes() - points[i-1].ds.ds.UsedBytes())
		rate := delta / days
		rates = append(rates, rate)
		intervals = append(intervals, GrowthAnomaly{FromRunID: points[i-1].run.ID, ToRunID: points[i].run.ID, BytesPerDay: rate, DeltaBytes: delta})
	}
	if len(rates) < 4 {
		return nil
	}
	med := median(rates)
	deviations := make([]float64, len(rates))
	for i, value := range rates {
		deviations[i] = math.Abs(value - med)
	}
	mad := median(deviations)
	threshold := med + 3*mad
	var out []GrowthAnomaly
	for i, interval := range intervals {
		if interval.BytesPerDay > 0 && interval.BytesPerDay > threshold {
			interval.MedianBytesPerDay = med
			out = append(out, interval)
		}
		_ = i
	}
	return out
}

func capacityProjection(points []capacityPoint, thresholds CapacityThresholds, includePartial, capacityChanged bool) *CapacityProjection {
	projection := &CapacityProjection{Confidence: ProjectionUnknown, Method: "linear-least-squares", TargetBasis: "percent", Points: len(points), Reasons: make([]string, 0)}
	if len(points) > 0 {
		latest := points[len(points)-1].ds.ds
		percentTarget := float64(latest.CapacityBytes) * thresholds.FreePercent / 100
		target := percentTarget
		if thresholds.FreeBytes > target {
			target, projection.TargetBasis = thresholds.FreeBytes, "bytes"
		}
		projection.TargetFreeBytes = target
		if float64(latest.FreeBytes) <= target {
			when := points[len(points)-1].run.StartedAt
			projection.CrossesAt = &when
			remaining := 0.0
			projection.DaysRemaining = &remaining
			projection.Reasons = append(projection.Reasons, "already below floor")
		}
	}
	if len(points) >= 2 {
		projection.SpanDays = points[len(points)-1].run.StartedAt.Sub(points[0].run.StartedAt).Hours() / 24
	}
	if len(points) < 3 {
		projection.Reasons = append(projection.Reasons, "fewer than 3 usable points")
		return projection
	}
	span := projection.SpanDays
	if span < 1 {
		projection.Reasons = append(projection.Reasons, "history spans less than 1 day")
		return projection
	}
	x := make([]float64, len(points))
	y := make([]float64, len(points))
	base := points[0].run.StartedAt
	for i, point := range points {
		x[i] = point.run.StartedAt.Sub(base).Hours() / 24
		y[i] = float64(point.ds.ds.UsedBytes())
	}
	slope, intercept, r2 := linearFit(x, y)
	projection.SlopeBytesPerDay, projection.RSquared = slope, r2
	if slope <= 0 {
		projection.Reasons = append(projection.Reasons, "free space is not shrinking")
		return projection
	}
	thresholdUsed := float64(points[len(points)-1].ds.ds.CapacityBytes) - projection.TargetFreeBytes
	latestUsed := y[len(y)-1]
	days := (thresholdUsed - latestUsed) / slope
	if days < 0 {
		days = 0
	}
	remaining := days
	when := points[len(points)-1].run.StartedAt.Add(time.Duration(days * float64(24*time.Hour)))
	projection.CrossesAt, projection.DaysRemaining = &when, &remaining
	low := len(points) < 5 || span < 7 || r2 < 0.5 || capacityChanged || includePartial || hasLargeCapacityGap(points)
	if low {
		projection.Confidence = ProjectionLow
		if len(points) < 5 {
			projection.Reasons = append(projection.Reasons, "fewer than 5 points")
		}
		if span < 7 {
			projection.Reasons = append(projection.Reasons, "history spans less than 7 days")
		}
		if r2 < 0.5 {
			projection.Reasons = append(projection.Reasons, "linear fit R² is below 0.5")
		}
		if capacityChanged {
			projection.Reasons = append(projection.Reasons, "capacity changed during the window")
		}
		if includePartial {
			projection.Reasons = append(projection.Reasons, "partial runs were included")
		}
		if hasLargeCapacityGap(points) {
			projection.Reasons = append(projection.Reasons, "run gap suggests history pruning")
		}
	} else {
		projection.Confidence = ProjectionProjected
	}
	_ = intercept
	return projection
}

func linearFit(x, y []float64) (slope, intercept, r2 float64) {
	var sx, sy float64
	for i := range x {
		sx += x[i]
		sy += y[i]
	}
	mx, my := sx/float64(len(x)), sy/float64(len(y))
	var xx, xy, yy float64
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		xx += dx * dx
		xy += dx * dy
		yy += dy * dy
	}
	if xx == 0 {
		return 0, my, 0
	}
	slope, intercept = xy/xx, my-(xy/xx)*mx
	if yy > 0 {
		r2 = (xy * xy) / (xx * yy)
	}
	return slope, intercept, r2
}

func hasLargeCapacityGap(points []capacityPoint) bool {
	if len(points) < 3 {
		return false
	}
	var total float64
	for i := 1; i < len(points); i++ {
		total += points[i].run.StartedAt.Sub(points[i-1].run.StartedAt).Hours() / 24
	}
	average := total / float64(len(points)-1)
	for i := 1; i < len(points); i++ {
		if points[i].run.StartedAt.Sub(points[i-1].run.StartedAt).Hours()/24 > 30 && points[i].run.StartedAt.Sub(points[i-1].run.StartedAt).Hours()/24 > average*2 {
			return true
		}
	}
	return false
}

func capacityChangedDuringWindow(points []capacityPoint) bool {
	for i := 1; i < len(points); i++ {
		if points[i].ds.ds.CapacityBytes != points[i-1].ds.ds.CapacityBytes {
			return true
		}
	}
	return false
}

func unknownProjection(reason string) *CapacityProjection {
	return &CapacityProjection{Confidence: ProjectionUnknown, Method: "linear-least-squares", TargetBasis: "percent", Reasons: []string{reason}}
}

func median(values []float64) float64 {
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	if len(copyValues)%2 == 1 {
		return copyValues[len(copyValues)/2]
	}
	return (copyValues[len(copyValues)/2-1] + copyValues[len(copyValues)/2]) / 2
}

func capacityContextKey(context, vcenter string) string { return context + "\x00" + vcenter }
func capacityVMKey(obs Observation) string {
	key := obs.VCenterID + "\x00" + obs.VM.ID
	if obs.VM.ID == "" {
		key = obs.VCenterID + "\x00" + obs.Context + "\x00" + obs.VM.Name
	}
	return key
}
func capacityNonempty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
func appendUniqueBlindness(values []CapacityBlindness, value CapacityBlindness) []CapacityBlindness {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}
