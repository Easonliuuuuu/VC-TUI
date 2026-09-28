package assessment

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Diff compares two immutable runs. Only vCenters successfully collected in
// both runs participate in lifecycle claims; this is the guard that prevents
// an outage from looking like mass deletion.
func (s *Store) Diff(ctx context.Context, baseID, targetID int64, includeRuntime bool) (Diff, error) {
	return s.DiffForContexts(ctx, baseID, targetID, includeRuntime, nil)
}

// DiffForContexts compares only evidence belonging to the requested stored
// contexts. A context present on one side but not the other remains a
// coverage warning, never an inferred lifecycle change.
func (s *Store) DiffForContexts(ctx context.Context, baseID, targetID int64, includeRuntime bool, selectors []string) (Diff, error) {
	base, err := s.GetRun(ctx, baseID)
	if err != nil {
		return Diff{}, err
	}
	target, err := s.GetRun(ctx, targetID)
	if err != nil {
		return Diff{}, err
	}
	bv, bc, err := s.loadVMs(ctx, baseID)
	if err != nil {
		return Diff{}, err
	}
	tv, tc, err := s.loadVMs(ctx, targetID)
	if err != nil {
		return Diff{}, err
	}
	baseContexts := contextRunValues(bc)
	targetContexts := contextRunValues(tc)
	selected, err := ValidateStoredContexts(selectors, baseContexts, targetContexts)
	if err != nil {
		return Diff{}, err
	}
	filterContextVMs(bv, bc, selected)
	filterContextVMs(tv, tc, selected)
	d := Diff{SchemaVersion: 1, Base: base, Target: target}
	baseByVC := make(map[string][]storedVM)
	targetByVC := make(map[string][]storedVM)
	// vcNames resolves a VCenterID back to the context name an operator
	// configured. Both sides feed it so a vCenter present in only one run
	// still resolves by name rather than falling back to its raw ID.
	vcNames := make(map[string]string)
	// A vCenter with no snapshot evidence on EITHER side cannot have snapshot
	// lifecycle compared on either: the covered side's snapshots would
	// otherwise read as removed (or created) by the other side's silence.
	snapshotBlind := make(map[string]bool)
	for _, side := range []map[int64]ContextRun{bc, tc} {
		for _, c := range side {
			if c.VCenterID != "" && Successful(c.VMStatus) && !ContextComplete(c, []string{"snapshot"}) {
				snapshotBlind[c.VCenterID] = true
			}
		}
	}
	for id, c := range bc {
		if c.VCenterID != "" && Successful(c.VMStatus) {
			baseByVC[c.VCenterID] = append(baseByVC[c.VCenterID], withSnapshotCoverage(bv[id], c, "baseline", &d, snapshotBlind[c.VCenterID])...)
		} else if c.VMStatus != "" && !Successful(c.VMStatus) {
			msg := fmt.Sprintf("%s was not fully collected in baseline: %s", c.Name, nonempty(c.Error, c.VMStatus))
			d.Warnings = append(d.Warnings, msg)
			d.Coverage = append(d.Coverage, CoverageIssue{Scope: "baseline", Context: c.Name, Message: msg})
		}
		if c.VCenterID != "" && c.Name != "" {
			vcNames[c.VCenterID] = c.Name
		}
	}
	for id, c := range tc {
		if c.VCenterID != "" && Successful(c.VMStatus) {
			targetByVC[c.VCenterID] = append(targetByVC[c.VCenterID], withSnapshotCoverage(tv[id], c, "target", &d, snapshotBlind[c.VCenterID])...)
		} else if c.VMStatus != "" && !Successful(c.VMStatus) {
			msg := fmt.Sprintf("%s was not fully collected in target: %s", c.Name, nonempty(c.Error, c.VMStatus))
			d.Warnings = append(d.Warnings, msg)
			d.Coverage = append(d.Coverage, CoverageIssue{Scope: "target", Context: c.Name, Message: msg})
		}
		if c.VCenterID != "" && c.Name != "" {
			vcNames[c.VCenterID] = c.Name
		}
	}
	var allBase, allTarget []storedVM
	for vc, values := range baseByVC {
		if _, ok := targetByVC[vc]; ok {
			allBase = append(allBase, values...)
			allTarget = append(allTarget, targetByVC[vc]...)
		}
	}
	changes, snapshots := compareVMs(allBase, allTarget, includeRuntime)
	d.VMs = append(d.VMs, changes...)
	d.Snapshots = append(d.Snapshots, snapshots...)
	for vc := range baseByVC {
		if _, ok := targetByVC[vc]; !ok {
			name := vcLabel(vcNames, vc)
			msg := "vCenter " + name + " is not comparable: it was not successfully collected in target"
			d.Warnings = append(d.Warnings, msg)
			d.Coverage = append(d.Coverage, CoverageIssue{Scope: "target", Context: name, Message: msg})
		}
	}
	for vc := range targetByVC {
		if _, ok := baseByVC[vc]; !ok {
			name := vcLabel(vcNames, vc)
			msg := "vCenter " + name + " is not comparable: it was not successfully collected in baseline"
			d.Warnings = append(d.Warnings, msg)
			d.Coverage = append(d.Coverage, CoverageIssue{Scope: "baseline", Context: name, Message: msg})
		}
	}
	d.Counts = countChanges(d.VMs, d.Snapshots)
	if err := s.infrastructureDiff(ctx, baseID, targetID, includeRuntime, selected, &d); err != nil {
		return Diff{}, err
	}
	sort.Slice(d.VMs, func(i, j int) bool { return strings.ToLower(d.VMs[i].Name) < strings.ToLower(d.VMs[j].Name) })
	sort.Slice(d.Snapshots, func(i, j int) bool {
		return strings.ToLower(d.Snapshots[i].VMName) < strings.ToLower(d.Snapshots[j].VMName)
	})
	d.SnapshotAges, err = s.SnapshotAgesForContexts(ctx, targetID, 0, selected)
	if err != nil {
		return Diff{}, err
	}
	return d, nil
}

func contextRunValues(values map[int64]ContextRun) []ContextRun {
	out := make([]ContextRun, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func filterContextVMs(vms map[int64][]storedVM, contexts map[int64]ContextRun, selected []string) {
	if len(selected) == 0 {
		return
	}
	for id, contextRun := range contexts {
		if !contextSelected(contextRun.Name, selected) {
			delete(contexts, id)
			delete(vms, id)
		}
	}
}

// vcLabel resolves a VCenterID to the context name an operator recognizes,
// falling back to the raw ID only when neither run's context rows named it
// (a v1/v2 ledger row predating the column, in practice).
func vcLabel(names map[string]string, vc string) string {
	if name, ok := names[vc]; ok {
		return name
	}
	return vc
}

// withSnapshotCoverage strips snapshot evidence from a context's VMs when that
// vCenter's "snapshot" collection was never recorded as successful in either
// compared run — an imported run whose workbook carried no vSnapshot
// worksheet, or a run captured before this collection kind existed. Without
// this, an absent worksheet would read as "confirmed no snapshots" to every
// snapshot lifecycle and age check. blind is true when either side lacks the
// evidence; the warning is raised on the side that actually lacks it.
func withSnapshotCoverage(vms []storedVM, c ContextRun, scope string, d *Diff, blind bool) []storedVM {
	if !blind {
		return vms
	}
	if !ContextComplete(c, []string{"snapshot"}) {
		msg := fmt.Sprintf("%s: snapshot evidence was not collected in %s (%s); snapshot changes are not compared", c.Name, scope, CoverageReason([]ContextRun{c}, c.Name, []string{"snapshot"}))
		d.Warnings = append(d.Warnings, msg)
		d.Coverage = append(d.Coverage, CoverageIssue{Scope: scope, Context: c.Name, Message: msg})
	}
	out := make([]storedVM, len(vms))
	for i, v := range vms {
		v.snapshots = nil
		out[i] = v
	}
	return out
}

// Successful reports whether a collection answered the question, including
// the valid empty-estate answer.
func Successful(s string) bool { return s == "success" || s == "empty" }
func nonempty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func countChanges(vms []VMChange, snaps []SnapshotChange) DiffCounts {
	var c DiffCounts
	for _, v := range vms {
		for _, k := range v.Changes {
			switch k {
			case "appeared":
				c.Appeared++
			case "vanished":
				c.Vanished++
			case "moved":
				c.Moved++
			case "modified":
				c.Modified++
			}
		}
	}
	c.Snapshots = len(snaps)
	return c
}

func compareVMs(base, target []storedVM, includeRuntime bool) ([]VMChange, []SnapshotChange) {
	used := make([]bool, len(base))
	targetIdentityCounts := make(map[string]int, len(target)*2)
	for _, vm := range target {
		if value := vm.observation.VM.InstanceUUID; value != "" {
			targetIdentityCounts[identityKey("instance_uuid", value)]++
		}
		if value := vm.observation.VM.BIOSUUID; value != "" {
			targetIdentityCounts[identityKey("bios_uuid", value)]++
		}
	}
	var out []VMChange
	var snaps []SnapshotChange
	for i := range target {
		j, basis := findMatch(base, used, target[i].observation, targetIdentityCounts)
		if j < 0 {
			out = append(out, VMChange{Kind: "vm", Changes: []string{"appeared"}, Name: target[i].observation.VM.Name, Context: target[i].observation.Context, After: &target[i].observation})
			for _, snap := range target[i].snapshots {
				snaps = append(snaps, SnapshotChange{Kind: "created", VMName: target[i].observation.VM.Name, Context: target[i].observation.Context, Name: snap.Name, After: snap.CreateTime.UTC().Format(time.RFC3339), CreateTime: snap.CreateTime.UTC().Format(time.RFC3339)})
			}
			continue
		}
		used[j] = true
		before, after := base[j], target[i]
		fields := changedFields(before.observation.VM, after.observation.VM, includeRuntime)
		changes := make([]string, 0, 2)
		if moved(before.observation.VM, after.observation.VM, before.observation.VCenterID, after.observation.VCenterID) {
			changes = append(changes, "moved")
		}
		if len(fields) > 0 {
			changes = append(changes, "modified")
		}
		if len(changes) > 0 {
			out = append(out, VMChange{Kind: "vm", Changes: changes, Name: after.observation.VM.Name, Context: after.observation.Context, MatchBasis: basis, Fields: fields, Before: &before.observation, After: &after.observation})
		}
		if before.observation.VCenterID == after.observation.VCenterID {
			snaps = append(snaps, compareSnapshots(before, after)...)
		}
	}
	for i := range base {
		if used[i] {
			continue
		}
		b := base[i]
		out = append(out, VMChange{Kind: "vm", Changes: []string{"vanished"}, Name: b.observation.VM.Name, Context: b.observation.Context, Before: &b.observation})
	}
	return out, snaps
}

func findMatch(base []storedVM, used []bool, target Observation, targetIdentityCounts map[string]int) (int, string) {
	for i := range base {
		if used[i] {
			continue
		}
		if base[i].observation.VCenterID == target.VCenterID && base[i].observation.VM.ID == target.VM.ID {
			return i, "moref"
		}
	}
	for _, kind := range []string{"instance_uuid", "bios_uuid"} {
		value := target.VM.InstanceUUID
		if kind == "bios_uuid" {
			value = target.VM.BIOSUUID
		}
		if value == "" {
			continue
		}
		// A duplicated identity is not strong enough evidence to infer a move.
		// Treat every ambiguous target identity as appeared/vanished instead of
		// silently pairing the wrong VM.
		if targetIdentityCounts[identityKey(kind, value)] != 1 {
			continue
		}
		candidate, count := -1, 0
		for i := range base {
			if used[i] {
				continue
			}
			v := base[i].observation.VM
			other := v.InstanceUUID
			if kind == "bios_uuid" {
				other = v.BIOSUUID
			}
			if other == value {
				candidate = i
				count++
			}
		}
		if count == 1 {
			return candidate, kind
		}
	}
	return -1, ""
}

func identityKey(kind, value string) string { return kind + "\x00" + value }

func moved(a, b vsphere.VM, avc, bvc string) bool {
	if avc != bvc || a.Host != b.Host || a.Cluster != b.Cluster || a.Folder != b.Folder {
		return true
	}
	return !equalStrings(a.Datastores, b.Datastores)
}

func equalStrings(a, b []string) bool {
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func changedFields(a, b vsphere.VM, runtime bool) []FieldChange {
	var out []FieldChange
	add := func(name, before, after string) {
		if before != after {
			out = append(out, FieldChange{Field: name, Before: before, After: after})
		}
	}
	add("name", a.Name, b.Name)
	add("cpu", strconv.FormatInt(int64(a.CPU), 10), strconv.FormatInt(int64(b.CPU), 10))
	add("memory", strconv.FormatInt(a.MemoryMB, 10), strconv.FormatInt(b.MemoryMB, 10))
	add("guest_os", a.GuestOS, b.GuestOS)
	add("annotation", a.Annotation, b.Annotation)
	out = append(out, migrationFieldChanges(a, b)...)
	if before, err := json.Marshal(a); err == nil {
		if after, err := json.Marshal(b); err == nil {
			out = append(out, metadataFieldChanges(before, after)...)
		}
	}
	if runtime {
		add("power_state", a.PowerState, b.PowerState)
		add("guest_state", a.GuestState, b.GuestState)
		add("tools_state", a.ToolsState, b.ToolsState)
		add("ip_address", a.IPAddress, b.IPAddress)
		add("storage_gb", fmt.Sprintf("%.3f", a.StorageGB), fmt.Sprintf("%.3f", b.StorageGB))
	}
	return out
}

// metadataFieldChanges reports identity-aware metadata changes while treating
// an unavailable source as unknown. The full before/after payload still keeps
// the source status and values for callers that need to inspect it.
func metadataFieldChanges(before, after json.RawMessage) []FieldChange {
	var bm, am struct {
		Metadata vsphere.Metadata `json:"metadata"`
	}
	if json.Unmarshal(before, &bm) != nil || json.Unmarshal(after, &am) != nil {
		return nil
	}
	var out []FieldChange
	if bm.Metadata.TagsStatus == "available" && am.Metadata.TagsStatus == "available" {
		bt := make(map[string]vsphere.Tag, len(bm.Metadata.Tags))
		at := make(map[string]vsphere.Tag, len(am.Metadata.Tags))
		for _, tag := range bm.Metadata.Tags {
			bt[tag.CategoryID+"/"+tag.ID] = tag
		}
		for _, tag := range am.Metadata.Tags {
			at[tag.CategoryID+"/"+tag.ID] = tag
		}
		for key, tag := range at {
			if old, ok := bt[key]; !ok {
				out = append(out, FieldChange{Field: "metadata.tags[" + key + "]", After: tagLabel(tag)})
			} else if tagLabel(old) != tagLabel(tag) {
				out = append(out, FieldChange{Field: "metadata.tags[" + key + "]", Before: tagLabel(old), After: tagLabel(tag)})
			}
		}
		for key, tag := range bt {
			if _, ok := at[key]; !ok {
				out = append(out, FieldChange{Field: "metadata.tags[" + key + "]", Before: tagLabel(tag)})
			}
		}
	}
	if bm.Metadata.CustomAttributesStatus == "available" && am.Metadata.CustomAttributesStatus == "available" {
		bt := make(map[int32]vsphere.CustomAttribute, len(bm.Metadata.CustomAttributes))
		at := make(map[int32]vsphere.CustomAttribute, len(am.Metadata.CustomAttributes))
		for _, attr := range bm.Metadata.CustomAttributes {
			bt[attr.Key] = attr
		}
		for _, attr := range am.Metadata.CustomAttributes {
			at[attr.Key] = attr
		}
		for key, attr := range at {
			if old, ok := bt[key]; !ok || old.Name != attr.Name || old.Value != attr.Value {
				before := ""
				if ok {
					before = old.Name + "=" + old.Value
				}
				out = append(out, FieldChange{Field: fmt.Sprintf("metadata.custom_attributes[%d]", key), Before: before, After: attr.Name + "=" + attr.Value})
			}
		}
		for key, attr := range bt {
			if _, ok := at[key]; !ok {
				out = append(out, FieldChange{Field: fmt.Sprintf("metadata.custom_attributes[%d]", key), Before: attr.Name + "=" + attr.Value})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

func tagLabel(tag vsphere.Tag) string {
	if tag.Category == "" {
		return tag.Name
	}
	return tag.Category + "/" + tag.Name
}

// migrationFingerprint fingerprints the migration-relevant configuration of vm.
// other is the VM from the opposite run: disk fields that the older collector
// did not record (empty) are unknown rather than changed, so they are left out
// of both sides instead of turning every VM into a migration-blocking change
// after a tool upgrade.
func migrationFingerprint(vm, other vsphere.VM) string {
	raw, _ := json.Marshal(migrationConfig(vm, other))
	return string(raw)
}

// migrationFieldPrefix namespaces per-field migration configuration changes,
// e.g. "migration_configuration.disks[2000].shared_bus".
const migrationFieldPrefix = "migration_configuration"

// migrationFieldChanges reports each differing migration-relevant sub-field
// so the inspector can show what changed instead of two truncated blobs.
func migrationFieldChanges(a, b vsphere.VM) []FieldChange {
	before, after := flattenJSON(migrationConfig(a, b)), flattenJSON(migrationConfig(b, a))
	var out []FieldChange
	for path, old := range before {
		if now := after[path]; now != old {
			out = append(out, FieldChange{Field: migrationFieldPrefix + "." + path, Before: old, After: now})
		}
	}
	for path, now := range after {
		if _, ok := before[path]; !ok && now != "" {
			out = append(out, FieldChange{Field: migrationFieldPrefix + "." + path, After: now})
		}
	}
	return out
}

// flattenJSON renders v as leaf path -> compact JSON value. Array elements
// that carry a "key" are addressed by it, so device order does not matter.
func flattenJSON(v any) map[string]string {
	raw, _ := json.Marshal(v)
	var tree any
	_ = json.Unmarshal(raw, &tree)
	out := map[string]string{}
	var walk func(path string, node any)
	walk = func(path string, node any) {
		switch n := node.(type) {
		case map[string]any:
			for k, child := range n {
				next := k
				if path != "" {
					next = path + "." + k
				}
				walk(next, child)
			}
		case []any:
			for i, child := range n {
				id := strconv.Itoa(i)
				if m, ok := child.(map[string]any); ok {
					if key, ok := m["key"].(float64); ok {
						id = strconv.FormatInt(int64(key), 10)
					}
				}
				walk(fmt.Sprintf("%s[%s]", path, id), child)
			}
		case nil:
		default:
			b, _ := json.Marshal(n)
			out[path] = string(b)
		}
	}
	walk("", tree)
	return out
}

func migrationConfig(vm, other vsphere.VM) any {
	value := struct {
		ConfigurationAvailable       bool                        `json:"configuration_available"`
		GuestID                      string                      `json:"guest_id"`
		Firmware                     string                      `json:"firmware"`
		SecureBootEnabled            *bool                       `json:"secure_boot_enabled"`
		CoresPerSocket               int32                       `json:"cores_per_socket"`
		CPUSockets                   int32                       `json:"cpu_sockets"`
		AutoCoresPerSocket           *bool                       `json:"auto_cores_per_socket"`
		CPUAllocation                *vmResourceAllocationCompat `json:"cpu_allocation"`
		MemoryAllocation             *vmResourceAllocationCompat `json:"memory_allocation"`
		MemoryReservationLockedToMax *bool                       `json:"memory_reservation_locked_to_max"`
		ManagedBy                    *vsphere.VMManagedBy        `json:"managed_by"`
		Disks                        []migrationDisk             `json:"disks"`
		NICs                         []migrationNIC              `json:"nics"`
		TPMs                         []vsphere.VMTPM             `json:"tpms"`
		PCIDevices                   []vsphere.VMPCIDevice       `json:"pci_devices"`
		Floppies                     []vsphere.VMFloppy          `json:"floppies"`
	}{
		ConfigurationAvailable: vm.ConfigurationAvailable, GuestID: vm.GuestID, Firmware: vm.Firmware,
		SecureBootEnabled: vm.SecureBootEnabled, CoresPerSocket: vm.CoresPerSocket, CPUSockets: vm.CPUSockets,
		AutoCoresPerSocket: vm.AutoCoresPerSocket, CPUAllocation: allocationCompat(vm.CPUAllocation), MemoryAllocation: allocationCompat(vm.MemoryAllocation),
		MemoryReservationLockedToMax: vm.MemoryReservationLockedToMax, ManagedBy: vm.ManagedBy, Disks: migrationDisks(vm.Disks, other.Disks), NICs: migrationNICs(vm.NICs), TPMs: vm.TPMs, PCIDevices: vm.PCIDevices, Floppies: vm.Floppies,
	}
	return value
}

// migrationDisk is an explicit allow-list of migration-relevant disk fields.
// Display-only fields (label, backing path) and fields added by later
// collectors are excluded or compared only when both runs recorded them.
type migrationDisk struct {
	Key                  int32  `json:"key"`
	CapacityBytes        int64  `json:"capacity_bytes"`
	UUID                 string `json:"uuid,omitempty"`
	BackingType          string `json:"backing_type,omitempty"`
	Raw                  bool   `json:"raw"`
	DiskMode             string `json:"disk_mode,omitempty"`
	Sharing              string `json:"sharing,omitempty"`
	ThinProvisioned      *bool  `json:"thin_provisioned,omitempty"`
	EagerlyScrub         *bool  `json:"eagerly_scrub,omitempty"`
	SharedBus            string `json:"shared_bus,omitempty"`
	RawLUNID             string `json:"raw_lun_id,omitempty"`
	RawCompatibilityMode string `json:"raw_compatibility_mode,omitempty"`
}

func migrationDisks(values, other []vsphere.VMDisk) []migrationDisk {
	peers := make(map[int32]vsphere.VMDisk, len(other))
	for _, d := range other {
		peers[d.Key] = d
	}
	out := make([]migrationDisk, 0, len(values))
	for _, d := range values {
		m := migrationDisk{Key: d.Key, CapacityBytes: d.CapacityBytes, UUID: d.UUID, BackingType: d.BackingType, Raw: d.Raw,
			DiskMode: d.DiskMode, Sharing: d.Sharing, ThinProvisioned: d.ThinProvisioned, EagerlyScrub: d.EagerlyScrub,
			SharedBus: d.SharedBus, RawLUNID: d.RawLUNID, RawCompatibilityMode: d.RawCompatibilityMode}
		if peer, ok := peers[d.Key]; ok {
			// Empty on either side means "not recorded" for these late-added fields.
			if peer.SharedBus == "" || d.SharedBus == "" {
				m.SharedBus = ""
			}
			if peer.RawLUNID == "" || d.RawLUNID == "" {
				m.RawLUNID = ""
			}
			if peer.RawCompatibilityMode == "" || d.RawCompatibilityMode == "" {
				m.RawCompatibilityMode = ""
			}
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

type migrationNIC struct {
	Key            int32  `json:"key"`
	Adapter        string `json:"adapter,omitempty"`
	MACAddress     string `json:"mac_address,omitempty"`
	MACAddressType string `json:"mac_address_type,omitempty"`
	UPTCompatible  *bool  `json:"upt_compatibility_enabled,omitempty"`
}

func migrationNICs(values []vsphere.VMNIC) []migrationNIC {
	out := make([]migrationNIC, 0, len(values))
	for _, value := range values {
		out = append(out, migrationNIC{Key: value.Key, Adapter: value.Adapter, MACAddress: value.MACAddress, MACAddressType: value.MACAddressType, UPTCompatible: value.UPTCompatible})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// vmResourceAllocationCompat keeps the diff package independent of the
// pointer-bearing implementation details while preserving nil/default state.
type vmResourceAllocationCompat struct {
	Reservation *int64 `json:"reservation,omitempty"`
	Limit       *int64 `json:"limit,omitempty"`
}

func allocationCompat(value *vsphere.VMResourceAllocation) *vmResourceAllocationCompat {
	if value == nil {
		return nil
	}
	return &vmResourceAllocationCompat{Reservation: value.Reservation, Limit: value.Limit}
}

func compareSnapshots(a, b storedVM) []SnapshotChange {
	byID := make(map[string]vsphere.VMSnapshot, len(a.snapshots))
	for _, s := range a.snapshots {
		byID[s.ID] = s
	}
	var out []SnapshotChange
	for _, s := range b.snapshots {
		if old, ok := byID[s.ID]; ok {
			delete(byID, s.ID)
			if old.Name != s.Name || old.Description != s.Description || old.Current != s.Current {
				out = append(out, SnapshotChange{Kind: "changed", VMName: b.observation.VM.Name, Context: b.observation.Context, Name: s.Name, Before: old.Name, After: s.Name, CreateTime: s.CreateTime.UTC().Format(time.RFC3339)})
			}
		} else {
			out = append(out, SnapshotChange{Kind: "created", VMName: b.observation.VM.Name, Context: b.observation.Context, Name: s.Name, After: s.CreateTime.UTC().Format(time.RFC3339), CreateTime: s.CreateTime.UTC().Format(time.RFC3339)})
		}
	}
	for _, s := range byID {
		out = append(out, SnapshotChange{Kind: "removed", VMName: a.observation.VM.Name, Context: a.observation.Context, Name: s.Name, Before: s.CreateTime.UTC().Format(time.RFC3339), CreateTime: s.CreateTime.UTC().Format(time.RFC3339)})
	}
	return out
}

func (s *Store) snapshotAges(ctx context.Context, runID int64, olderThan time.Duration) ([]SnapshotAge, error) {
	targetVMs, targetContexts, err := s.loadVMs(ctx, runID)
	if err != nil {
		return nil, err
	}
	type key struct{ vc, vm, snap string }
	wanted := make(map[key]SnapshotAge)
	for id, c := range targetContexts {
		if !Successful(c.VMStatus) || !ContextComplete(c, []string{"snapshot"}) {
			continue
		}
		for _, v := range targetVMs[id] {
			for _, snap := range v.snapshots {
				if olderThan > 0 && c.FinishedAt.Sub(snap.CreateTime) < olderThan {
					continue
				}
				wanted[key{c.VCenterID, v.observation.VM.ID, snap.ID}] = SnapshotAge{VMName: v.observation.VM.Name, Context: v.observation.Context, Name: snap.Name, CreateTime: snap.CreateTime, Age: c.FinishedAt.Sub(snap.CreateTime), FirstSeen: c.FinishedAt, LastSeen: c.FinishedAt}
			}
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT cr.vcenter_id,v.moref,so.moref,cr.finished_at FROM snapshot_observations so JOIN vm_observations v ON v.id=so.vm_observation_id JOIN context_runs cr ON cr.id=v.context_run_id WHERE cr.vm_status IN ('success','empty')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var vc, vm, snap string
		var seen sql.NullInt64
		if err := rows.Scan(&vc, &vm, &snap, &seen); err != nil {
			return nil, err
		}
		k := key{vc, vm, snap}
		a, ok := wanted[k]
		if !ok {
			continue
		}
		t := fromMillis(seen)
		if t.Before(a.FirstSeen) {
			a.FirstSeen = t
		}
		if t.After(a.LastSeen) {
			a.LastSeen = t
		}
		wanted[k] = a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SnapshotAge, 0, len(wanted))
	for _, a := range wanted {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Age > out[j].Age })
	return out, nil
}
