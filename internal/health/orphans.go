package health

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/humanize"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Confidence is the strength of an orphan conclusion.
type Confidence string

const (
	ConfidenceVerified     Confidence = "verified-unreferenced"
	ConfidenceSuspected    Confidence = "suspected-unreferenced"
	ConfidenceOtherContext Confidence = "referenced-other-context"
	ConfidenceReferenced   Confidence = "referenced"
	ConfidenceUnknown      Confidence = "unknown-incomplete-coverage"
)

// DatastoreFileAssessment is the point-in-time relationship evidence for one
// interactive browser entry. Unlike OrphanEvidence it also represents a file
// referenced in its own context, because the detail view needs to explain the
// positive relationship as well as orphan candidates.
type DatastoreFileAssessment struct {
	Observed        bool
	Confidence      Confidence
	ReferencedBy    []OrphanReference
	CheckedContexts []string
	Blind           []OrphanBlindness
	Reasons         []string
}

// OrphanReference identifies the VM or template which uses a disk path.
type OrphanReference struct {
	Context   string `json:"context"`
	VCenterID string `json:"vcenter_id,omitempty"`
	VMID      string `json:"vm_id,omitempty"`
	VM        string `json:"vm"`
	Template  bool   `json:"template,omitempty"`
	Path      string `json:"path"`
}

// OrphanBlindness explains why a candidate could not receive a high-confidence
// verdict. It is intentionally structured so the CLI and JSON consumers can
// show the same coverage evidence.
type OrphanBlindness struct {
	Context string `json:"context"`
	Reason  string `json:"reason"`
}

// OrphanEvidence is the complete evidence record for one browsed VMDK.
type OrphanEvidence struct {
	Confidence      Confidence        `json:"confidence"`
	Object          Object            `json:"object"`
	Path            string            `json:"path"`
	SizeBytes       int64             `json:"size_bytes"`
	Modified        time.Time         `json:"modified,omitempty"`
	Identity        []string          `json:"identity,omitempty"`
	CheckedContexts []string          `json:"checked_contexts,omitempty"`
	Blind           []OrphanBlindness `json:"blind,omitempty"`
	ReferencedBy    []OrphanReference `json:"referenced_by,omitempty"`
	Reasons         []string          `json:"reasons,omitempty"`
}

// OrphanScanStatus classifies why a datastore's browse evidence is missing or
// incomplete.
type OrphanScanStatus string

const (
	// OrphanScanNotBrowsed means the assessment was captured without
	// --browse-datastores, so the datastore was never listed.
	OrphanScanNotBrowsed OrphanScanStatus = "not-browsed"
	// OrphanScanFailed means the browse was attempted and errored.
	OrphanScanFailed OrphanScanStatus = "failed"
	// OrphanScanDenied means the datastore was inaccessible or the account
	// lacked Datastore.Browse.
	OrphanScanDenied OrphanScanStatus = "denied"
	// OrphanScanTruncated means the listing succeeded but was cut short at the
	// file cap, so absent files are not evidence of absence.
	OrphanScanTruncated OrphanScanStatus = "truncated"
)

// OrphanScanGap names one datastore whose orphan evidence is missing or partial.
// The CLI and JSON consumers use it so an empty Entries list is never mistaken
// for a fully scanned, genuinely clean estate.
type OrphanScanGap struct {
	Object Object           `json:"object"`
	Status OrphanScanStatus `json:"status"`
	Reason string           `json:"reason,omitempty"`
}

// OrphanCoverage is the top-level scan-coverage state of an orphan report. It is
// populated independently of Entries: a run that browsed nothing still reports
// Browsed == 0 with one Gaps entry per datastore.
type OrphanCoverage struct {
	// Datastores is the number of datastore resources in the assessment.
	Datastores int `json:"datastores"`
	// Browsed is the number of datastores whose browse listing succeeded
	// (a truncated listing still counts, and also appears in Gaps).
	Browsed int `json:"browsed"`
	// Gaps names every datastore that was not browsed, failed to browse, was
	// inaccessible, or whose listing was truncated.
	Gaps []OrphanScanGap `json:"gaps,omitempty"`
}

// Complete reports whether every datastore in the assessment was fully browsed.
// "No candidates" is only a genuine clean result when Complete is true.
func (c OrphanCoverage) Complete() bool {
	return c.Datastores > 0 && len(c.Gaps) == 0
}

// PermissionDenied reports whether the gap is a browse the account was not
// privileged to make, as opposed to an inaccessible datastore or another
// failure.
func (g OrphanScanGap) PermissionDenied() bool {
	return g.Status == OrphanScanDenied && g.Reason != vsphere.DatastoreInaccessibleMessage
}

// BrowseRemediation says what is needed to get browse evidence when none of the
// datastores was browsed. It separates a capture that never asked for a browse
// (run with --browse-datastores) from one that asked and was refused (grant
// Datastore.Browse) and from one that failed for another reason (the failure
// itself). It is empty when at least one datastore was browsed.
func (c OrphanCoverage) BrowseRemediation() string {
	if c.Datastores == 0 || c.Browsed > 0 {
		return ""
	}
	var denied, failed, notBrowsed int
	firstFailure := ""
	for _, gap := range c.Gaps {
		switch {
		case gap.PermissionDenied():
			denied++
		case gap.Status == OrphanScanNotBrowsed:
			notBrowsed++
		default:
			failed++
			if firstFailure == "" {
				firstFailure = gap.Reason
			}
		}
	}
	var parts []string
	if denied > 0 {
		parts = append(parts, fmt.Sprintf("browse denied on %d/%d datastores: grant Datastore.Browse", denied, c.Datastores))
	}
	if failed > 0 {
		part := fmt.Sprintf("browse failed on %d/%d datastores", failed, c.Datastores)
		if firstFailure != "" {
			part += ": " + firstFailure
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "a capture run with --browse-datastores"
	}
	if notBrowsed > 0 {
		parts = append(parts, fmt.Sprintf("%d not browsed", notBrowsed))
	}
	return strings.Join(parts, "; ")
}

// OrphanReport contains every browsed VMDK candidate, including unknown ones.
// Unknown entries are retained for drill-down but are not health findings.
// Coverage records whether the datastore scope was actually scanned, so a
// consumer can tell an empty Entries list on a clean estate from one produced
// by missing browse evidence.
type OrphanReport struct {
	RunID    int64            `json:"run_id"`
	Entries  []OrphanEvidence `json:"entries"`
	Coverage OrphanCoverage   `json:"coverage"`
}

// AssessDatastoreFile evaluates one browser path against stored assessment
// evidence. It deliberately returns UNKNOWN when the path was not observed or
// coverage is incomplete; absence from an old capture is not proof that a
// currently visible file is orphaned.
func AssessDatastoreFile(data assessment.ExportData, current vsphere.Datastore, filePath string) DatastoreFileAssessment {
	result := DatastoreFileAssessment{Confidence: ConfidenceUnknown}
	_, relative, ok := vsphere.SplitDatastorePath(filePath)
	if !ok || relative == "" {
		result.Reasons = []string{"file path is not a canonical datastore path"}
		return result
	}
	var stores []orphanDatastore
	currentKeys := assessment.DatastoreIdentity(current)
	for _, resource := range data.Resources {
		if resource.Kind != "datastore" || !strings.EqualFold(resource.Context, current.Context) {
			continue
		}
		var datastore vsphere.Datastore
		if !assessment.DecodeResource(resource, &datastore) {
			continue
		}
		keys := assessment.DatastoreIdentity(datastore)
		idMatch := current.ID != "" && datastore.ID == current.ID
		nameMatch := strings.EqualFold(datastore.Name, current.Name)
		identityMatch := len(currentKeys) > 0 && anyStringIntersection(currentKeys, keys)
		if idMatch || nameMatch || identityMatch {
			stores = append(stores, orphanDatastore{resource: resource, ds: datastore, keys: keys, local: datastore.Backing.Local})
		}
	}
	if len(stores) != 1 {
		if len(stores) == 0 {
			result.Reasons = []string{"datastore was not observed in the selected assessment"}
		} else {
			result.Reasons = []string{"datastore identity is ambiguous in the selected assessment"}
		}
		return result
	}
	store := stores[0]
	if store.ds.BrowseStatus != "success" {
		result.Reasons = []string{nonempty(store.ds.BrowseError, "datastore browse evidence is unavailable")}
		return result
	}
	for _, file := range store.ds.Files {
		_, observedPath, ok := vsphere.SplitDatastorePath(file.Path)
		if ok && vmdkFamily(observedPath) == vmdkFamily(relative) {
			result.Observed = true
			break
		}
	}
	if !result.Observed {
		result.Reasons = []string{"file was not observed in the selected assessment"}
		return result
	}
	result.CheckedContexts = checkedContexts(data, store)
	for _, item := range data.VMs {
		vm := item.Observation.VM
		for _, disk := range vm.Disks {
			name, diskPath, ok := vsphere.SplitDatastorePath(disk.BackingPath)
			if !ok || vmdkFamily(diskPath) != vmdkFamily(relative) {
				continue
			}
			if !sameStoredDatastore(data, store, item.Observation.Context, name) {
				continue
			}
			result.ReferencedBy = append(result.ReferencedBy, OrphanReference{
				Context: item.Observation.Context, VCenterID: item.Observation.VCenterID,
				VMID: vm.ID, VM: vm.Name, Template: vm.IsTemplate, Path: diskPath,
			})
		}
	}
	result.ReferencedBy = uniqueOrphanReferences(result.ReferencedBy)
	for _, ref := range result.ReferencedBy {
		if strings.EqualFold(ref.Context, store.resource.Context) {
			result.Confidence = ConfidenceReferenced
			return result
		}
	}
	if len(result.ReferencedBy) > 0 {
		result.Confidence = ConfidenceOtherContext
		return result
	}
	for _, entry := range Orphans(data).Entries {
		_, entryPath, entryOK := vsphere.SplitDatastorePath(entry.Path)
		if entryOK && strings.EqualFold(entry.Object.Context, store.resource.Context) && entry.Object.ID == store.ds.ID && vmdkFamily(entryPath) == vmdkFamily(relative) {
			result.Confidence, result.Blind, result.Reasons = entry.Confidence, entry.Blind, entry.Reasons
			return result
		}
	}
	result.Reasons = []string{"stored orphan evidence could not establish ownership"}
	return result
}

func vmdkFamily(value string) string {
	value = vsphere.NormalizeRelativePath(value)
	for _, suffix := range []string{"-flat.vmdk", "-delta.vmdk", "-sesparse.vmdk", "-ctk.vmdk", "-rdm.vmdk", "-rdmp.vmdk", "-digest.vmdk"} {
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSuffix(value, suffix) + ".vmdk"
			break
		}
	}
	base := strings.TrimSuffix(value, ".vmdk")
	if len(base) > 7 && base[len(base)-7] == '-' && allDigits(base[len(base)-6:]) {
		value = base[:len(base)-7] + ".vmdk"
	}
	return value
}

func sameStoredDatastore(data assessment.ExportData, target orphanDatastore, context, name string) bool {
	if strings.EqualFold(context, target.resource.Context) && strings.EqualFold(name, target.ds.Name) {
		return true
	}
	if target.local || len(target.keys) == 0 {
		return false
	}
	for _, resource := range data.Resources {
		if resource.Kind != "datastore" || !strings.EqualFold(resource.Context, context) || !strings.EqualFold(resource.Name, name) {
			continue
		}
		var datastore vsphere.Datastore
		if assessment.DecodeResource(resource, &datastore) && anyStringIntersection(target.keys, assessment.DatastoreIdentity(datastore)) {
			return true
		}
	}
	return false
}

func uniqueOrphanReferences(values []OrphanReference) []OrphanReference {
	seen := map[string]bool{}
	out := make([]OrphanReference, 0, len(values))
	for _, value := range values {
		key := value.Context + "\x00" + value.VCenterID + "\x00" + value.VMID + "\x00" + value.VM + "\x00" + value.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Context != out[j].Context {
			return out[i].Context < out[j].Context
		}
		return out[i].VM < out[j].VM
	})
	return out
}

func orphanConfidenceLabel(value Confidence) string {
	switch value {
	case ConfidenceVerified:
		return "Verified-unreferenced"
	case ConfidenceSuspected:
		return "Suspected-unreferenced"
	case ConfidenceOtherContext:
		return "Referenced in another context"
	default:
		return "Unknown"
	}
}

type orphanDatastore struct {
	resource assessment.ResourceObservation
	ds       vsphere.Datastore
	keys     []string
	weakKey  string
	local    bool
}

type orphanReference struct {
	ref     OrphanReference
	context string
	key     string
	path    string
	stem    string
	weakKey string
	blind   bool
}

type orphanIndex struct {
	byKey  map[string][]orphanReference
	byWeak map[string][]orphanReference
}

// Orphans weighs every browsed datastore file in a stored assessment against
// every VM and template disk reference in the whole estate. It is a pure
// function of the run and never reads wall clock time; recent is relative to
// data.Run.FinishedAt.
func Orphans(data assessment.ExportData) OrphanReport {
	stores := make([]orphanDatastore, 0)
	byContextName := make(map[string][]orphanDatastore)
	for _, resource := range data.Resources {
		if resource.Kind != "datastore" {
			continue
		}
		var datastore vsphere.Datastore
		if !assessment.DecodeResource(resource, &datastore) {
			continue
		}
		store := orphanDatastore{resource: resource, ds: datastore, keys: assessment.DatastoreIdentity(datastore), local: datastore.Backing.Local}
		store.weakKey = weakDatastoreKey(resource.Context, datastore.Name)
		stores = append(stores, store)
		byContextName[contextNameKey(resource.Context, datastore.Name)] = append(byContextName[contextNameKey(resource.Context, datastore.Name)], store)
	}

	index := orphanIndex{byKey: make(map[string][]orphanReference), byWeak: make(map[string][]orphanReference)}
	for _, item := range data.VMs {
		vm := item.Observation.VM
		for _, disk := range vm.Disks {
			name, relative, ok := vsphere.SplitDatastorePath(disk.BackingPath)
			if !ok || relative == "" {
				continue
			}
			refContext := item.Observation.Context
			if refContext == "" {
				refContext = vm.Context
			}
			matching := byContextName[contextNameKey(refContext, name)]
			if refContext == "" {
				matching = storesNamed(byContextName, name)
				if len(matching) == 1 {
					refContext = matching[0].resource.Context
				}
			}
			candidate := orphanReference{
				context: refContext,
				path:    vsphere.NormalizeRelativePath(relative),
				stem:    snapshotStem(relative),
				weakKey: weakDatastoreKey(refContext, name),
				ref: OrphanReference{
					Context:   refContext,
					VCenterID: item.Observation.VCenterID,
					VMID:      vm.ID,
					VM:        vm.Name,
					Template:  vm.IsTemplate,
					Path:      vsphere.NormalizeRelativePath(relative),
				},
			}
			if len(matching) == 0 {
				candidate.blind = true
				index.byWeak[candidate.weakKey] = append(index.byWeak[candidate.weakKey], candidate)
				continue
			}
			registered := false
			for _, store := range matching {
				if len(store.keys) == 0 {
					continue
				}
				registered = true
				for _, key := range store.keys {
					candidate.key = key
					index.byKey[key] = append(index.byKey[key], candidate)
				}
			}
			if !registered {
				candidate.blind = true
			}
			index.byWeak[candidate.weakKey] = append(index.byWeak[candidate.weakKey], candidate)
		}
	}
	entries := make([]OrphanEvidence, 0)
	for _, store := range stores {
		if store.ds.BrowseStatus != "success" {
			continue
		}
		for _, file := range store.ds.Files {
			name, relative, ok := vsphere.SplitDatastorePath(file.Path)
			if !ok || !strings.HasSuffix(strings.ToLower(relative), ".vmdk") || sidecarVMDK(relative) {
				continue
			}
			// A malformed file path is not evidence of an orphan. Preserve it as
			// unknown so the drill-down exposes the incomplete observation.
			if name == "" {
				continue
			}
			entry := OrphanEvidence{
				Object: resourceObject(data, store.resource, "datastore", store.ds.Name, store.ds.ID, store.ds.Datacenter),
				Path:   file.Path, SizeBytes: file.SizeBytes, Modified: file.Modified,
				Identity: append([]string(nil), store.keys...),
			}
			if store.local {
				entry.Identity = append(entry.Identity, "local datastore (context-scoped)")
			}
			entry.CheckedContexts = checkedContexts(data, store)
			refs := matchingReferences(index, store, relative)
			entry.ReferencedBy = uniqueReferences(refs)
			for _, ref := range entry.ReferencedBy {
				if ref.Context == store.resource.Context {
					// A live reference in the candidate's own context is not an
					// orphan, regardless of how much other coverage is missing.
					entry = OrphanEvidence{}
					break
				}
			}
			if entry.Path == "" {
				continue
			}
			entry.Blind = blindnessFor(data, store, byContextName, index, relative)
			entry.Reasons = blindnessReasons(entry.Blind)

			schema := inventorySchema(data.Run.InventorySchemaVersion)
			if len(entry.Blind) > 0 || (!store.local && schema >= 11 && len(store.keys) == 0) {
				entry.Confidence = ConfidenceUnknown
				if !store.local && schema >= 11 && len(store.keys) == 0 {
					entry.Reasons = appendUnique(entry.Reasons, "datastore backing identity is unavailable")
				}
			} else if len(entry.ReferencedBy) > 0 {
				sameContext := false
				for _, ref := range entry.ReferencedBy {
					if ref.Context == store.resource.Context {
						sameContext = true
						break
					}
				}
				if sameContext {
					// Referenced candidates are deliberately omitted from the
					// report; this entry is retained only for the matching logic.
					continue
				}
				entry.Confidence = ConfidenceOtherContext
			} else {
				entry.Confidence = ConfidenceVerified
				if schema < 11 {
					entry.Confidence = ConfidenceSuspected
					entry.Reasons = appendUnique(entry.Reasons, "inventory schema predates datastore backing identity")
				}
				if folderHasReference(index, store, relative) {
					entry.Confidence = ConfidenceSuspected
					entry.Reasons = appendUnique(entry.Reasons, "the folder also contains a referenced disk")
				}
				if recentDisk(file.Modified, data.Run.FinishedAt) {
					entry.Confidence = ConfidenceSuspected
					entry.Reasons = appendUnique(entry.Reasons, "file was modified within 24 hours of the assessment")
				}
			}
			entries = append(entries, entry)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		for _, pair := range [][2]string{{entries[i].Object.Context, entries[j].Object.Context}, {entries[i].Object.Name, entries[j].Object.Name}, {entries[i].Path, entries[j].Path}} {
			if pair[0] != pair[1] {
				return strings.ToLower(pair[0]) < strings.ToLower(pair[1])
			}
		}
		return entries[i].Confidence < entries[j].Confidence
	})
	return OrphanReport{RunID: data.Run.ID, Entries: entries, Coverage: orphanCoverage(data, stores)}
}

// orphanCoverage records the browse state of every datastore in the assessment
// so an empty candidate list carries its own evidence of completeness.
func orphanCoverage(data assessment.ExportData, stores []orphanDatastore) OrphanCoverage {
	coverage := OrphanCoverage{Datastores: len(stores)}
	for _, store := range stores {
		object := resourceObject(data, store.resource, "datastore", store.ds.Name, store.ds.ID, store.ds.Datacenter)
		switch store.ds.BrowseStatus {
		case "success":
			coverage.Browsed++
			if store.ds.BrowseTruncated {
				coverage.Gaps = append(coverage.Gaps, OrphanScanGap{Object: object, Status: OrphanScanTruncated, Reason: "datastore browse listing was truncated at the file cap"})
			}
		case "denied":
			coverage.Gaps = append(coverage.Gaps, OrphanScanGap{Object: object, Status: OrphanScanDenied, Reason: nonempty(store.ds.BrowseError, vsphere.DatastoreInaccessibleMessage)})
		case "failed":
			// Runs captured before a permission fault was classified recorded
			// it as a plain failure; the server's text still says which it was.
			if vsphere.BrowsePermissionDenied(store.ds.BrowseError) {
				coverage.Gaps = append(coverage.Gaps, OrphanScanGap{Object: object, Status: OrphanScanDenied, Reason: store.ds.BrowseError})
				continue
			}
			coverage.Gaps = append(coverage.Gaps, OrphanScanGap{Object: object, Status: OrphanScanFailed, Reason: nonempty(store.ds.BrowseError, "datastore browse failed")})
		default:
			coverage.Gaps = append(coverage.Gaps, OrphanScanGap{Object: object, Status: OrphanScanNotBrowsed, Reason: "assessment was captured without --browse-datastores"})
		}
	}
	sort.SliceStable(coverage.Gaps, func(i, j int) bool {
		a, b := coverage.Gaps[i].Object, coverage.Gaps[j].Object
		if !strings.EqualFold(a.Context, b.Context) {
			return strings.ToLower(a.Context) < strings.ToLower(b.Context)
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return coverage
}

func storesNamed(byContextName map[string][]orphanDatastore, name string) []orphanDatastore {
	var out []orphanDatastore
	suffix := "\x00" + strings.ToLower(strings.TrimSpace(name))
	for key, stores := range byContextName {
		if strings.HasSuffix(key, suffix) {
			out = append(out, stores...)
		}
	}
	return out
}

func snapshotStem(value string) string {
	value = vsphere.NormalizeRelativePath(value)
	directory, filename := path.Split(value)
	ext := strings.TrimSuffix(filename, ".vmdk")
	if len(ext) > 7 {
		suffix := ext[len(ext)-7:]
		if suffix[0] == '-' && allDigits(suffix[1:]) {
			ext = ext[:len(ext)-7]
		}
	}
	return directory + ext
}

func sidecarVMDK(value string) bool {
	name := strings.ToLower(path.Base(vsphere.NormalizeRelativePath(value)))
	for _, suffix := range []string{"-flat.vmdk", "-delta.vmdk", "-sesparse.vmdk", "-ctk.vmdk", "-rdm.vmdk", "-rdmp.vmdk", "-digest.vmdk"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func weakDatastoreKey(context, name string) string {
	return strings.ToLower(strings.TrimSpace(context)) + "\x00name:" + strings.ToLower(strings.TrimSpace(name))
}

func contextNameKey(context, name string) string {
	return strings.ToLower(strings.TrimSpace(context)) + "\x00" + strings.ToLower(strings.TrimSpace(name))
}

func matchingReferences(index orphanIndex, store orphanDatastore, relative string) []orphanReference {
	pathValue := vsphere.NormalizeRelativePath(relative)
	stem := snapshotStem(relative)
	var candidates []orphanReference
	if len(store.keys) > 0 {
		for _, key := range store.keys {
			candidates = append(candidates, index.byKey[key]...)
		}
	} else {
		candidates = append(candidates, index.byWeak[store.weakKey]...)
	}
	out := make([]orphanReference, 0)
	for _, ref := range candidates {
		if ref.path == pathValue || ref.stem == stem {
			out = append(out, ref)
		}
	}
	return out
}

func uniqueReferences(values []orphanReference) []OrphanReference {
	seen := make(map[string]bool)
	out := make([]OrphanReference, 0, len(values))
	for _, value := range values {
		key := value.ref.Context + "\x00" + value.ref.VCenterID + "\x00" + value.ref.VM + "\x00" + value.ref.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value.ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Context != out[j].Context {
			return out[i].Context < out[j].Context
		}
		return out[i].VM < out[j].VM
	})
	return out
}

func checkedContexts(data assessment.ExportData, store orphanDatastore) []string {
	seen := make(map[string]bool)
	for _, context := range data.Contexts {
		if context.Name == store.resource.Context || contextHasDatastoreName(data, context.Name, store.ds.Name) {
			seen[context.Name] = true
		}
	}
	seen[store.resource.Context] = true
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func blindnessFor(data assessment.ExportData, store orphanDatastore, byContextName map[string][]orphanDatastore, index orphanIndex, relative string) []OrphanBlindness {
	blind := make([]OrphanBlindness, 0)
	schema := inventorySchema(data.Run.InventorySchemaVersion)
	if schema < 11 {
		return blind
	}
	if store.ds.BrowseTruncated {
		blind = append(blind, OrphanBlindness{Context: store.resource.Context, Reason: "datastore browse listing was truncated"})
	}
	for _, context := range data.Contexts {
		if context.Name == store.resource.Context || contextSharesStore(context.Name, store, byContextName) {
			if reason, ok := contextCoverageFailure(context); ok {
				blind = append(blind, OrphanBlindness{Context: context.Name, Reason: reason})
			}
			if context.Name != store.resource.Context && contextHasDatastoreName(data, context.Name, store.ds.Name) && !contextHasStrongStore(byContextName, context.Name, store.keys) {
				blind = append(blind, OrphanBlindness{Context: context.Name, Reason: "datastore backing identity is unavailable"})
			}
		}
	}
	if len(data.Contexts) == 0 {
		return blind
	}
	if !store.local && len(store.keys) == 0 {
		blind = append(blind, OrphanBlindness{Context: store.resource.Context, Reason: "datastore backing identity is unavailable"})
	}
	for _, ref := range index.byWeak[store.weakKey] {
		if ref.blind && ref.context == store.resource.Context && ref.path == vsphere.NormalizeRelativePath(relative) {
			blind = append(blind, OrphanBlindness{Context: ref.context, Reason: "VM references a datastore name without an observed datastore identity"})
		}
	}
	return uniqueBlindness(blind)
}

func contextSharesStore(context string, store orphanDatastore, byContextName map[string][]orphanDatastore) bool {
	for key, stores := range byContextName {
		if !strings.HasPrefix(key, strings.ToLower(context)+"\x00") {
			continue
		}
		for _, other := range stores {
			if len(store.keys) == 0 || len(other.keys) == 0 {
				continue
			}
			if anyStringIntersection(store.keys, other.keys) {
				return true
			}
		}
	}
	return false
}

func contextHasStrongStore(byContextName map[string][]orphanDatastore, context string, keys []string) bool {
	for _, store := range byContextName[contextNameKey(context, "")] {
		if anyStringIntersection(store.keys, keys) {
			return true
		}
	}
	for key, stores := range byContextName {
		if !strings.HasPrefix(key, strings.ToLower(context)+"\x00") {
			continue
		}
		for _, store := range stores {
			if anyStringIntersection(store.keys, keys) {
				return true
			}
		}
	}
	return false
}

func contextHasDatastoreName(data assessment.ExportData, context, name string) bool {
	for _, resource := range data.Resources {
		if resource.Kind == "datastore" && resource.Context == context && strings.EqualFold(resource.Name, name) {
			return true
		}
	}
	return false
}

func contextCoverageFailure(context assessment.ContextRun) (string, bool) {
	if !assessment.Successful(context.VMStatus) {
		return "vm collection: " + nonempty(context.Error, context.VMStatus), true
	}
	for _, collection := range context.Collections {
		if collection.Kind == "datastore" && !assessment.Successful(collection.Status) {
			return "datastore collection: " + nonempty(collection.Error, collection.Status), true
		}
	}
	for _, collection := range context.Collections {
		if collection.Kind == "vm" && !assessment.Successful(collection.Status) {
			return "vm collection: " + nonempty(collection.Error, collection.Status), true
		}
	}
	return "", false
}

func folderHasReference(index orphanIndex, store orphanDatastore, relative string) bool {
	directory := path.Dir(vsphere.NormalizeRelativePath(relative))
	for _, ref := range uniqueReferences(referencesForStore(index, store)) {
		if path.Dir(ref.Path) == directory {
			return true
		}
	}
	return false
}

func referencesForStore(index orphanIndex, store orphanDatastore) []orphanReference {
	var candidates []orphanReference
	if len(store.keys) > 0 {
		for _, key := range store.keys {
			candidates = append(candidates, index.byKey[key]...)
		}
	} else {
		candidates = append(candidates, index.byWeak[store.weakKey]...)
	}
	return candidates
}

func recentDisk(modified, finished time.Time) bool {
	return !modified.IsZero() && !finished.IsZero() && modified.After(finished.Add(-24*time.Hour)) && !modified.After(finished.Add(time.Minute))
}

func blindnessReasons(values []OrphanBlindness) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = appendUnique(out, value.Context+": "+value.Reason)
	}
	return out
}

func uniqueBlindness(values []OrphanBlindness) []OrphanBlindness {
	seen := make(map[string]bool)
	out := make([]OrphanBlindness, 0, len(values))
	for _, value := range values {
		key := value.Context + "\x00" + value.Reason
		if !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Context < out[j].Context })
	return out
}

func anyStringIntersection(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, value := range a {
		set[value] = true
	}
	for _, value := range b {
		if set[value] {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// OrphanSizeLabel renders an orphan's size, saying so explicitly when none was
// reported: a zero is a server that did not tell us, never an empty disk.
func OrphanSizeLabel(bytes int64) string {
	if bytes <= 0 {
		return "size unknown"
	}
	return humanize.Bytes(bytes)
}
