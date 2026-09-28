package rvimport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/health"
	"github.com/easonliuuuuu/vsfleet/internal/topology"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Regression tests for issue #162: an RVTools import must never let the lack
// of a workbook field improve a verdict. Each test names the acceptance
// criterion or checklist item it guards.

// --- helpers ---

func dropSheet(t *testing.T, f *excelize.File, sheet string) {
	t.Helper()
	if err := f.DeleteSheet(sheet); err != nil {
		t.Fatalf("delete %s: %v", sheet, err)
	}
}

// blankHeader removes one column from a worksheet the way a workbook produced
// by a different RVTools version would simply not have it.
func blankHeader(t *testing.T, f *excelize.File, sheet, header string) {
	t.Helper()
	rows, err := f.GetRows(sheet)
	if err != nil || len(rows) == 0 {
		t.Fatalf("read %s: %v", sheet, err)
	}
	for i, h := range rows[0] {
		if h != header {
			continue
		}
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.SetCellValue(sheet, col+"1", ""); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("%s has no %q column to remove", sheet, header)
}

func importFixture(t *testing.T, f *excelize.File, opts Options) (*Result, assessment.Run, *assessment.Store) {
	t.Helper()
	result, err := Parse(f, opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	store, err := assessment.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	run, err := result.Write(context.Background(), store, time.Now())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	return result, run, store
}

func collectionOf(t *testing.T, store *assessment.Store, runID int64, contextName, kind string) assessment.CollectionRun {
	t.Helper()
	contexts, err := store.ContextRuns(context.Background(), runID)
	if err != nil {
		t.Fatalf("ContextRuns: %v", err)
	}
	for _, c := range contexts {
		if c.Name != contextName {
			continue
		}
		for _, col := range c.Collections {
			if col.Kind == kind {
				return col
			}
		}
	}
	t.Fatalf("context %q recorded no %q collection", contextName, kind)
	return assessment.CollectionRun{}
}

func editHost(t *testing.T, data *assessment.ExportData, id string, edit func(*vsphere.Host)) {
	t.Helper()
	for i := range data.Resources {
		r := &data.Resources[i]
		if r.Kind != "host" || r.ID != id {
			continue
		}
		var h vsphere.Host
		if err := json.Unmarshal(r.Payload, &h); err != nil {
			t.Fatal(err)
		}
		edit(&h)
		payload, err := json.Marshal(h)
		if err != nil {
			t.Fatal(err)
		}
		r.Payload = payload
		return
	}
	t.Fatalf("no host %q in fixture", id)
}

func dvsResource(t *testing.T, context, vcenter string, sw vsphere.DVSwitch) assessment.ResourceObservation {
	t.Helper()
	payload, err := json.Marshal(sw)
	if err != nil {
		t.Fatal(err)
	}
	return assessment.ResourceObservation{Context: context, VCenterID: vcenter, Kind: "dvswitch", ID: sw.ID, Name: sw.Name, Payload: payload}
}

func hasGap(r Report, evidence string) bool {
	for _, g := range r.Gaps {
		if g.Evidence == evidence {
			return true
		}
	}
	return false
}

// --- coverage honesty ---

// A missing worksheet must become an explicit coverage gap, never a confirmed
// answer of "this estate has no hosts".
func TestMissingWorksheetIsUnavailableNotEmpty(t *testing.T) {
	f := openFixture(t, writeFixtureWorkbook(t, nil))
	dropSheet(t, f, sheetVHost)
	result, run, store := importFixture(t, f, Options{})

	for _, ctxName := range []string{"alpha", "beta"} {
		col := collectionOf(t, store, run.ID, ctxName, "host")
		if col.Status != "unavailable" {
			t.Errorf("%s host status = %q, want unavailable (a missing worksheet is not an empty estate)", ctxName, col.Status)
		}
		if !strings.Contains(col.Error, sheetVHost) {
			t.Errorf("%s host error = %q, want it to name the missing %s worksheet", ctxName, col.Error, sheetVHost)
		}
	}
	if !hasGap(result.Report, "host") {
		t.Errorf("report gaps = %+v, want a host gap", result.Report.Gaps)
	}
	contexts, err := store.ContextRuns(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blind := assessment.BlindContexts(contexts, []string{"host"}); len(blind) != 2 {
		t.Errorf("blind contexts for host = %v, want both", blind)
	}
	if hosts, _ := store.Resources(context.Background(), run.ID, "host"); len(hosts) != 0 {
		t.Errorf("stored %d hosts from a workbook with no vHost worksheet", len(hosts))
	}
}

// The distinction the whole change rests on: a worksheet that is present and
// rowless IS an answer.
func TestPresentButRowlessWorksheetIsEmpty(t *testing.T) {
	path := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		kept := data.Resources[:0]
		for _, r := range data.Resources {
			if r.Kind != "host" {
				kept = append(kept, r)
			}
		}
		data.Resources = kept
	})
	_, run, store := importFixture(t, openFixture(t, path), Options{})
	if got := collectionOf(t, store, run.ID, "alpha", "host").Status; got != "empty" {
		t.Errorf("host status = %q, want empty for a present, rowless vHost", got)
	}
}

// A capacity column that is absent would import as zero capacity, which reads
// as a less-utilized estate.
func TestMissingCapacityColumnMakesTheKindUnavailable(t *testing.T) {
	for _, tc := range []struct{ sheet, header, kind string }{
		{sheetVDatastore, "Free MiB", "datastore"},
		{sheetVHost, "# Memory", "host"},
		{sheetVCluster, "TotalCpu", "cluster"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			f := openFixture(t, writeFixtureWorkbook(t, nil))
			blankHeader(t, f, tc.sheet, tc.header)
			result, run, store := importFixture(t, f, Options{})

			col := collectionOf(t, store, run.ID, "alpha", tc.kind)
			if col.Status != "unavailable" || !strings.Contains(col.Error, tc.header) {
				t.Errorf("%s = %+v, want unavailable naming %q", tc.kind, col, tc.header)
			}
			if got, _ := store.Resources(context.Background(), run.ID, tc.kind); len(got) != 0 {
				t.Errorf("stored %d %s objects whose capacity column was missing", len(got), tc.kind)
			}
			for _, other := range []string{"host", "cluster", "datastore"} {
				if other == tc.kind {
					continue
				}
				if got := collectionOf(t, store, run.ID, "alpha", other).Status; got != "success" && got != "empty" {
					t.Errorf("%s status = %q, want an unrelated kind to stay answered", other, got)
				}
			}
			if !hasGap(result.Report, tc.kind) {
				t.Errorf("report gaps = %+v, want a %s gap", result.Report.Gaps, tc.kind)
			}
		})
	}
}

// vCPU carries the CPU column when vInfo lacks it; when neither does, the
// count is a named gap rather than a zero that reads as an idle VM.
func TestVMCPUFallsBackToVCPUAndOtherwiseIsAGap(t *testing.T) {
	f := openFixture(t, writeFixtureWorkbook(t, nil))
	blankHeader(t, f, sheetVInfo, "CPUs")
	result, err := Parse(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if vm := findVM(result, "alpha", "web-01"); vm == nil || vm.CPU != 2 {
		t.Fatalf("alpha VM CPU = %+v, want 2 recovered from vCPU", vm)
	}
	if hasGap(result.Report, "vm.cpu") {
		t.Error("a CPU gap was reported although vCPU carried the column")
	}

	blankHeader(t, f, sheetVCPU, "CPUs")
	result, err = Parse(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasGap(result.Report, "vm.cpu") {
		t.Errorf("gaps = %+v, want vm.cpu when no worksheet carries a CPU count", result.Report.Gaps)
	}
}

func TestVCPUDisagreementWarnsAndKeepsVInfo(t *testing.T) {
	f := openFixture(t, writeFixtureWorkbook(t, nil))
	rows, _ := f.GetRows(sheetVCPU)
	cpuCol := -1
	for i, h := range rows[0] {
		if h == "CPUs" {
			cpuCol = i
		}
	}
	col, _ := excelize.ColumnNumberToName(cpuCol + 1)
	if err := f.SetCellValue(sheetVCPU, col+"2", 64); err != nil {
		t.Fatal(err)
	}
	result, err := Parse(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	warned := false
	for _, w := range result.Report.Warnings {
		if strings.Contains(w, sheetVCPU) && strings.Contains(w, "keeping") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want the vInfo/vCPU disagreement reported", result.Report.Warnings)
	}
	if vm := findVM(result, "alpha", "web-01"); vm.CPU != 2 {
		t.Errorf("CPU = %d, want vInfo's 2 kept over vCPU's 64", vm.CPU)
	}
}

// --- worksheet mapping ---

func TestSnapshotsAreImportedAndAnAbsentWorksheetIsUnavailable(t *testing.T) {
	created := time.Date(2025, 12, 1, 8, 0, 0, 0, time.UTC)
	path := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		data.VMs[0].Snapshots = []vsphere.VMSnapshot{{ID: "snap-1", Name: "pre-patch", CreateTime: created, PowerState: "poweredOn"}}
	})
	f := openFixture(t, path)
	result, run, store := importFixture(t, f, Options{})
	vm := findVM(result, "alpha", "web-01")
	if len(vm.Snapshots) != 1 || !vm.Snapshots[0].CreateTime.Equal(created) {
		t.Fatalf("snapshots = %+v, want the one snapshot with its real creation time", vm.Snapshots)
	}
	if col := collectionOf(t, store, run.ID, "alpha", "snapshot"); col.Status != "success" || col.ItemCount != 1 {
		t.Errorf("snapshot collection = %+v, want success with 1 item", col)
	}

	g := openFixture(t, path)
	dropSheet(t, g, sheetVSnapshot)
	_, run2, store2 := importFixture(t, g, Options{})
	if col := collectionOf(t, store2, run2.ID, "alpha", "snapshot"); col.Status != "unavailable" {
		t.Errorf("snapshot collection = %+v, want unavailable when the workbook has no vSnapshot", col)
	}
}

func TestToolsAndPartitionsAreMappedAndEarnTheirSchemaLevel(t *testing.T) {
	path := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		vm := &data.VMs[0].Observation.VM
		vm.ToolsState, vm.ToolsVersion, vm.ToolsVersionStatus = "guestToolsRunning", "12352", "guestToolsCurrent"
		vm.Partitions = []vsphere.VMPartition{{Path: "/", DiskKeys: []int32{2000}, CapacityBytes: 20 << 30, FreeBytes: 5 << 30, FilesystemType: "ext4"}}
	})
	result, err := Parse(openFixture(t, path), Options{})
	if err != nil {
		t.Fatal(err)
	}
	vm := findVM(result, "alpha", "web-01")
	if vm.ToolsState != "guestToolsRunning" || vm.ToolsVersionStatus != "guestToolsCurrent" {
		t.Errorf("tools = %q/%q, want the vTools values", vm.ToolsState, vm.ToolsVersionStatus)
	}
	if len(vm.Partitions) != 1 || vm.Partitions[0].FilesystemType != "ext4" || vm.Partitions[0].FreeBytes != 5<<30 || len(vm.Partitions[0].DiskKeys) != 1 || vm.Partitions[0].DiskKeys[0] != 2000 {
		t.Errorf("partitions = %+v, want the mapped ext4 filesystem joined to disk key 2000", vm.Partitions)
	}
	if result.Report.SchemaVersion != "5" {
		t.Errorf("schema = %q, want 5 with vTools, vPartition and Disk Key all present", result.Report.SchemaVersion)
	}

	// A workbook earns only the schema level its evidence backs: claiming more
	// would tell schema-gated rules that absent evidence was collected empty.
	for _, tc := range []struct {
		name string
		drop func(*testing.T, *excelize.File)
		want string
	}{
		{"no partition disk key", func(t *testing.T, f *excelize.File) { blankHeader(t, f, sheetVPartition, "Disk Key") }, "4"},
		{"no vPartition", func(t *testing.T, f *excelize.File) { dropSheet(t, f, sheetVPartition) }, "3"},
		{"no vTools", func(t *testing.T, f *excelize.File) { dropSheet(t, f, sheetVTools) }, "2"},
	} {
		f := openFixture(t, path)
		tc.drop(t, f)
		r, err := Parse(f, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if r.Report.SchemaVersion != tc.want {
			t.Errorf("%s: schema = %q, want %q", tc.name, r.Report.SchemaVersion, tc.want)
		}
	}
}

func TestHostNetworkingJoinsByObjectIDOrUniqueSameWorkbookName(t *testing.T) {
	path := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		data.Run.InventorySchemaVersion = assessment.CurrentInventorySchemaVersion
		for _, hostID := range []string{"host-a1", "host-b1"} {
			switchName := "switch-" + hostID
			portName := "port-" + hostID
			editHost(t, data, hostID, func(h *vsphere.Host) {
				h.Name = "esx-shared"
				yes := true
				h.VSwitches = []vsphere.HostVSwitch{{Name: switchName, NumPorts: 128, MTU: 1500, Uplinks: []string{"vmnic0", "vmnic1"}, Promiscuous: &yes}}
				h.PortGroups = []vsphere.HostPortGroup{{Name: portName, Switch: switchName, VLAN: 120}}
			})
		}
	})
	f := openFixture(t, path)
	result, err := Parse(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var host *vsphere.Host
	for _, c := range result.contexts {
		for _, h := range c.hosts {
			if h.ID == "host-a1" {
				host = h
			}
		}
	}
	if host == nil || len(host.VSwitches) != 1 || len(host.PortGroups) != 1 {
		t.Fatalf("host = %+v, want one vSwitch and one port group attached by Object ID", host)
	}
	if sw := host.VSwitches[0]; sw.Promiscuous == nil || !*sw.Promiscuous || len(sw.Uplinks) != 2 {
		t.Errorf("vSwitch = %+v, want the promiscuous flag and both uplinks", sw)
	}

	// RVTools host-subsheet rows can omit Object ID. They may join by name only
	// when that name resolves uniquely inside the same vCenter in this workbook.
	g := openFixture(t, path)
	blankHeader(t, g, sheetVSwitch, "Object ID")
	blankHeader(t, g, sheetVPort, "Object ID")
	r2, err := Parse(g, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r2.contexts {
		for _, h := range c.hosts {
			if len(h.VSwitches) != 1 || len(h.PortGroups) != 1 {
				t.Errorf("host %s did not join its unique same-context networking rows: %+v", h.ID, h)
			}
			wantSwitch := "switch-" + h.ID
			if len(h.VSwitches) == 1 && h.VSwitches[0].Name != wantSwitch {
				t.Errorf("host %s received switch %q, want its same-vCenter switch %q", h.ID, h.VSwitches[0].Name, wantSwitch)
			}
		}
	}

	// A duplicate host name within that same vCenter makes the join ambiguous;
	// neither host receives the rows.
	ambiguousPath := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		data.Run.InventorySchemaVersion = assessment.CurrentInventorySchemaVersion
		for _, hostID := range []string{"host-a1", "host-b1"} {
			switchName := "switch-" + hostID
			editHost(t, data, hostID, func(h *vsphere.Host) {
				h.Name = "esx-shared"
				h.VSwitches = []vsphere.HostVSwitch{{Name: switchName}}
				h.PortGroups = []vsphere.HostPortGroup{{Name: "port-" + hostID, Switch: switchName}}
			})
		}
		data.Resources = append(data.Resources, hostResource(t, "alpha", "vc-alpha-uuid", vsphere.Host{
			Location: vsphere.Location{Context: "alpha", Datacenter: "dc-a"}, ID: "host-a2", Name: "esx-shared", Cluster: "cluster-a", CPUCores: 8, CPUMHz: 2000, MemoryMB: 65536,
		}))
	})
	ambiguousWorkbook := openFixture(t, ambiguousPath)
	blankHeader(t, ambiguousWorkbook, sheetVSwitch, "Object ID")
	blankHeader(t, ambiguousWorkbook, sheetVPort, "Object ID")
	r3, err := Parse(ambiguousWorkbook, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r3.contexts {
		for _, h := range c.hosts {
			if c.name == "alpha" && (len(h.VSwitches) != 0 || len(h.PortGroups) != 0) {
				t.Errorf("ambiguous host name attached networking rows to %s: %+v", h.ID, h)
			}
		}
	}
	if len(r3.Report.Ambiguities) == 0 {
		t.Errorf("duplicate same-context host name was not reported as an ambiguity: contexts=%+v warnings=%v", r3.contexts, r3.Report.Warnings)
	}
}

func TestDistributedSwitchesJoinPortGroupsByContextDatacenterAndName(t *testing.T) {
	sw := func(id, dc string) vsphere.DVSwitch {
		return vsphere.DVSwitch{
			Location: vsphere.Location{Datacenter: dc}, ID: id, Name: "DVS-Prod",
			PortGroups: []vsphere.DVPortGroup{{ID: "dvpg-" + id, Key: "key-" + id, Name: "pg-" + dc, Switch: "DVS-Prod", VLAN: "120"}},
		}
	}
	// Same switch name in two datacenters of one vCenter: distinct switches, and
	// each port group must land on its own.
	path := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		data.Resources = append(data.Resources,
			dvsResource(t, "alpha", "vc-alpha-uuid", sw("dvs-1", "dc-a")),
			dvsResource(t, "alpha", "vc-alpha-uuid", sw("dvs-2", "dc-b")))
	})
	result, run, store := importFixture(t, openFixture(t, path), Options{})
	for _, c := range result.contexts {
		if c.name != "alpha" {
			continue
		}
		if len(c.dvswitches) != 2 {
			t.Fatalf("dvswitches = %d, want the two same-named switches kept distinct", len(c.dvswitches))
		}
		for _, d := range c.dvswitches {
			if len(d.PortGroups) != 1 || !strings.HasSuffix(d.PortGroups[0].Name, strings.TrimPrefix(d.Datacenter, "dc-")) {
				t.Errorf("switch %s (%s) port groups = %+v, want only its own datacenter's", d.ID, d.Datacenter, d.PortGroups)
			}
		}
	}
	if col := collectionOf(t, store, run.ID, "alpha", "dvswitch"); col.Status != "success" {
		t.Errorf("dvswitch = %+v, want success", col)
	}

	// The same name in the SAME datacenter cannot be told apart by dvPort, so
	// the importer must refuse to guess: ambiguity recorded, kind unavailable.
	ambiguous := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		data.Resources = append(data.Resources,
			dvsResource(t, "alpha", "vc-alpha-uuid", sw("dvs-1", "dc-a")),
			dvsResource(t, "alpha", "vc-alpha-uuid", sw("dvs-2", "dc-a")))
	})
	r2, run2, store2 := importFixture(t, openFixture(t, ambiguous), Options{})
	if len(r2.Report.Ambiguities) == 0 {
		t.Error("no ambiguity recorded for two same-named switches in one datacenter")
	}
	if col := collectionOf(t, store2, run2.ID, "alpha", "dvswitch"); col.Status != "unavailable" {
		t.Errorf("dvswitch = %+v, want unavailable rather than a port group attached to a guess", col)
	}
}

// --- identity ---

func TestDuplicateVMIdentityIsAmbiguousAndNeverMergedOrAttached(t *testing.T) {
	path := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		twin := data.VMs[0]
		twin.Observation.VM.Name = "web-01-twin"
		twin.Observation.VM.InstanceUUID = "uuid-twin"
		data.VMs = append(data.VMs, twin) // same managed-object ID, different name
	})
	result, err := Parse(openFixture(t, path), Options{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range result.Report.Ambiguities {
		if a.Sheet == sheetVInfo && a.Context == "alpha" && strings.HasPrefix(a.Identity, "id:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("ambiguities = %+v, want the duplicated VM ID recorded", result.Report.Ambiguities)
	}
	alpha := 0
	for _, c := range result.contexts {
		if c.name != "alpha" {
			continue
		}
		alpha = len(c.vms)
		for _, vm := range c.vms {
			if len(vm.Disks) != 0 {
				t.Errorf("VM %q had %d disks attached through an ambiguous ID", vm.Name, len(vm.Disks))
			}
		}
	}
	if alpha != 2 {
		t.Errorf("alpha VMs = %d, want both candidates preserved", alpha)
	}
}

func TestDuplicateHostObjectIDMakesHostsUnavailable(t *testing.T) {
	path := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		for _, r := range append([]assessment.ResourceObservation(nil), data.Resources...) {
			if r.Kind == "host" && r.Context == "alpha" {
				dup := r
				dup.Name = "esx-a1-twin"
				data.Resources = append(data.Resources, dup)
			}
		}
	})
	result, run, store := importFixture(t, openFixture(t, path), Options{})
	if len(result.Report.Ambiguities) == 0 {
		t.Error("no ambiguity recorded for a repeated host Object ID")
	}
	if col := collectionOf(t, store, run.ID, "alpha", "host"); col.Status != "unavailable" {
		t.Errorf("alpha host = %+v, want unavailable: a repeated ID would overwrite its twin in the store", col)
	}
	if col := collectionOf(t, store, run.ID, "beta", "host"); col.Status != "success" {
		t.Errorf("beta host = %+v, want the unaffected context left answered", col)
	}
}

// --- atomicity, dry run, repeat import ---

func TestFailedImportLeavesNoRunBehind(t *testing.T) {
	// Mapping both contexts onto one name makes the write fail after the run
	// has begun to be created.
	f := openFixture(t, writeFixtureWorkbook(t, nil))
	result, err := Parse(f, Options{ContextMap: map[string]string{"alpha": "same", "beta": "same"}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := assessment.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := result.Write(context.Background(), store, time.Now()); err == nil {
		t.Fatal("Write succeeded with two contexts mapped onto one name")
	}
	runs, err := store.Runs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs after a failed import = %+v, want none: a failure must leave no partial run", runs)
	}
}

// Rollback deletes the run, and DeleteRun refuses a pinned one. That is safe
// only while an import never pins, so this is pinned down as a test.
func TestImportedRunsAreNeverPinned(t *testing.T) {
	_, run, _ := importFixture(t, openFixture(t, writeFixtureWorkbook(t, nil)), Options{Label: "keep-me"})
	if run.Pinned {
		t.Fatal("an imported run was pinned, which would make a failed import's rollback impossible")
	}
}

func TestRepeatImportIsDetectedByFingerprintAndStillImports(t *testing.T) {
	path := writeFixtureWorkbook(t, nil)
	sha, err := FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	other := writeFixtureWorkbook(t, func(data *assessment.ExportData) { data.VMs[0].Observation.VM.Name = "renamed" })
	otherSHA, _ := FileSHA256(other)
	if sha == otherSHA {
		t.Fatal("two different workbooks share a fingerprint")
	}

	store, err := assessment.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, found, err := FindDuplicate(ctx, store, sha); err != nil || found {
		t.Fatalf("FindDuplicate on an empty history = %v, %v", found, err)
	}
	for i := 0; i < 2; i++ {
		result, err := Parse(openFixture(t, path), Options{SourceSHA256: sha})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := result.Write(ctx, store, time.Now().Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("import %d: %v", i+1, err)
		}
	}
	if runs, _ := store.Runs(ctx); len(runs) != 2 {
		t.Errorf("runs = %d, want the repeat to import as a second run rather than be dropped", len(runs))
	}
	if prior, found, err := FindDuplicate(ctx, store, sha); err != nil || !found || prior.Source != importSource {
		t.Errorf("FindDuplicate = %+v, %v, %v, want the earlier import", prior, found, err)
	}
	if _, found, _ := FindDuplicate(ctx, store, otherSHA); found {
		t.Error("a different workbook was reported as a duplicate")
	}
}

func TestDryRunReportsColumnsGapsAndTimestampWithoutWriting(t *testing.T) {
	f := openFixture(t, writeFixtureWorkbook(t, nil))
	blankHeader(t, f, sheetVDatastore, "Free MiB")
	dropSheet(t, f, sheetVSnapshot)
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	result, err := Parse(f, Options{CapturedAt: stamp})
	if err != nil {
		t.Fatal(err)
	}
	r := result.Report
	if !r.CapturedAt.Equal(stamp) || r.CapturedAtSource == "" {
		t.Errorf("captured at = %v (%s), want the explicit time and its source", r.CapturedAt, r.CapturedAtSource)
	}
	var ds *SheetReport
	for i := range r.Sheets {
		if r.Sheets[i].Name == sheetVDatastore {
			ds = &r.Sheets[i]
		}
	}
	if ds == nil || len(ds.RecognizedColumns) == 0 {
		t.Fatalf("sheets = %+v, want per-column detail for vDatastore", r.Sheets)
	}
	if !contains(ds.MissingColumns, "Free MiB") {
		t.Errorf("vDatastore missing columns = %v, want Free MiB named", ds.MissingColumns)
	}
	if !hasGap(r, "datastore") || !hasGap(r, "snapshot") {
		t.Errorf("gaps = %+v, want datastore and snapshot gaps", r.Gaps)
	}
	for _, c := range r.Contexts {
		if c.DatastoreCount != 0 {
			t.Errorf("context %s reports %d datastores that would not be stored", c.Name, c.DatastoreCount)
		}
	}
}

// --- downstream commands ---

func twoImportedRuns(t *testing.T, mutateSecond func(*assessment.ExportData)) (*assessment.Store, assessment.Run, assessment.Run) {
	t.Helper()
	store, err := assessment.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var runs [2]assessment.Run
	for i, m := range []func(*assessment.ExportData){nil, mutateSecond} {
		result, err := Parse(openFixture(t, writeFixtureWorkbook(t, m)), Options{CapturedAt: base.Add(time.Duration(i) * 24 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if runs[i], err = result.Write(ctx, store, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return store, runs[0], runs[1]
}

func TestImportedRunsParticipateInDiffAndTimeline(t *testing.T) {
	store, first, second := twoImportedRuns(t, func(data *assessment.ExportData) {
		data.VMs[0].Observation.VM.Name = "web-01-renamed"
		data.VMs[0].Observation.VM.PowerState = "poweredOff"
	})
	ctx := context.Background()
	d, err := store.DiffForContexts(ctx, first.ID, second.ID, false, nil)
	if err != nil {
		t.Fatalf("DiffForContexts: %v", err)
	}
	if d.Counts.Appeared != 0 || d.Counts.Vanished != 0 || d.Counts.Modified == 0 {
		t.Errorf("diff counts = %+v, want the renamed VM matched by UUID and reported modified, not vanished+appeared", d.Counts)
	}
	events, err := store.TimelineForContexts(ctx, "uuid-shared-identity", []string{"alpha"}, false, true)
	if err != nil {
		t.Fatalf("TimelineForContexts: %v", err)
	}
	renamed := false
	for _, e := range events {
		if e.Kind == "renamed" {
			renamed = true
		}
	}
	if !renamed {
		t.Errorf("timeline = %+v, want a renamed event across the two imported runs", events)
	}
}

// A snapshot the workbook could not have told us about must not be reported as
// a lifecycle change, and the diff must say why.
func TestDiffDoesNotInventSnapshotsWhenTheWorksheetIsAbsent(t *testing.T) {
	store, err := assessment.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	withSnap := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		data.VMs[0].Snapshots = []vsphere.VMSnapshot{{ID: "s", Name: "old", CreateTime: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}}
	})
	first, _ := Parse(openFixture(t, withSnap), Options{CapturedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	r1, err := first.Write(ctx, store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	g := openFixture(t, withSnap)
	dropSheet(t, g, sheetVSnapshot)
	second, _ := Parse(g, Options{CapturedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)})
	r2, err := second.Write(ctx, store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.DiffForContexts(ctx, r1.ID, r2.ID, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Counts.Snapshots != 0 {
		t.Errorf("diff reports %d snapshot changes, want none: the snapshot vanishing is an absent worksheet, not a removal", d.Counts.Snapshots)
	}
	said := false
	for _, w := range d.Warnings {
		if strings.Contains(w, "snapshot evidence was not collected") {
			said = true
		}
	}
	if !said {
		t.Errorf("warnings = %v, want the snapshot coverage gap named", d.Warnings)
	}
}

func TestHealthReportsUnknownWhenImportedEvidenceIsAbsent(t *testing.T) {
	// Without vSnapshot the snapshot-age rule cannot answer.
	f := openFixture(t, writeFixtureWorkbook(t, nil))
	dropSheet(t, f, sheetVSnapshot)
	_, run, store := importFixture(t, f, Options{})
	data, err := store.LoadExportData(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	report := health.Evaluate(data, health.Options{})
	statuses := map[string]health.RuleStatus{}
	for _, r := range report.Rules {
		statuses[r.Rule] = r
	}
	if s := statuses["snapshot-age"]; s.Result != "unknown" || len(s.Blind) == 0 {
		t.Errorf("snapshot-age = %+v, want unknown and blind when vSnapshot is absent", s)
	}
	// Rules gated on evidence the profile never claims stay not-evaluated.
	for _, id := range []string{"host-path-redundancy", "bios-firmware"} {
		if s, ok := statuses[id]; ok && s.Status != "not-evaluated" {
			t.Errorf("%s = %+v, want not-evaluated for an imported run", id, s)
		}
	}
	for _, f := range report.Findings {
		if f.Severity == health.SeverityCritical {
			t.Errorf("an imported run produced a critical finding from absent evidence: %+v", f)
		}
	}
}

func TestTopologyNeverMergesSameNamedImportedVMsAcrossVCenters(t *testing.T) {
	_, run, store := importFixture(t, openFixture(t, writeFixtureWorkbook(t, nil)), Options{})
	data, err := store.LoadExportData(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	graph := topology.Build(data)
	alpha := graph.Topology(topology.Subject{Kind: "vm", Name: "web-01", Members: nil})
	// Both vCenters carry a VM called web-01; a name-only join would fuse them
	// into one subject with two members.
	if len(alpha.Subject.Members) > 1 {
		t.Errorf("web-01 resolved to %d members in one subject: %+v", len(alpha.Subject.Members), alpha.Subject.Members)
	}
	for _, m := range alpha.Subject.Members {
		if m.Context == "" {
			t.Errorf("member %+v has no context", m)
		}
	}
	// An imported run has no recorded network or resource-pool evidence, so a
	// query must report reduced confidence rather than a clean answer.
	if alpha.Confidence == topology.ConfidenceComplete {
		t.Errorf("confidence = %q for a run with unavailable collections, want reduced", alpha.Confidence)
	}
}

// --- safety ---

func TestOversizedWorkbookIsRefusedBeforeItIsOpened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.xlsx")
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fh.Truncate(maxWorkbookBytes + 1); err != nil { // sparse: no real disk use
		t.Fatal(err)
	}
	fh.Close()
	if _, err := OpenFile(path); err == nil || !strings.Contains(err.Error(), "import limit") {
		t.Fatalf("OpenFile = %v, want a size-limit refusal", err)
	}
}

func TestImportNeverReachesAMutationCapableClient(t *testing.T) {
	// The package must not import the vSphere client, session or transport:
	// import is local file processing only.
	src, err := os.ReadFile("rvimport.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"internal/session", "internal/transport", "internal/credentials", "govmomi", "net/http"} {
		if strings.Contains(string(src), `"`+banned) || strings.Contains(string(src), banned+`"`) {
			t.Errorf("rvimport imports %q; import must stay offline", banned)
		}
	}
}

// A workbook with no VI SDK UUID column would otherwise import with an empty
// vCenter ID, which diff and history treat as no vCenter at all. Live capture
// falls back to the endpoint; so must an import, and it must say so.
func TestMissingVCenterUUIDFallsBackToTheEndpointAndIsReported(t *testing.T) {
	f := openFixture(t, writeFixtureWorkbook(t, nil))
	for _, sheet := range []string{sheetVInfo, sheetVDisk, sheetVNetwork, sheetVHost, sheetVCluster, sheetVDatastore, sheetVSnapshot, sheetVCPU, sheetVMemory, sheetVTools, sheetVPartition, sheetVSwitch, sheetVPort, sheetDVSwitch, sheetDVPort} {
		blankHeader(t, f, sheet, "VI SDK UUID")
	}
	result, run, store := importFixture(t, f, Options{})
	if !hasGap(result.Report, "vcenter.id") {
		t.Errorf("gaps = %+v, want the missing vCenter UUID reported", result.Report.Gaps)
	}
	contexts, err := store.ContextRuns(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range contexts {
		if c.VCenterID == "" {
			t.Errorf("context %s has no vCenter ID, so diff and history would ignore it", c.Name)
		}
	}
}

// A worksheet with no data rows must still report which columns the importer
// reads; otherwise an empty vDisk looks like a sheet the importer ignores.
func TestColumnReportDoesNotDependOnRowCount(t *testing.T) {
	result, err := Parse(openFixture(t, writeFixtureWorkbook(t, nil)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sh := range result.Report.Sheets {
		if sh.Name == sheetVDisk && sh.Rows == 0 && len(sh.RecognizedColumns) == 0 {
			t.Errorf("rowless %s reports no recognized columns: %+v", sh.Name, sh)
		}
		for _, col := range sh.MissingColumns {
			if strings.EqualFold(col, "vsfleet Context") || strings.EqualFold(col, "VM SMBIOS UUID") {
				t.Errorf("%s reports optional column %q as missing", sh.Name, col)
			}
		}
		// A clean export has every column the importer reads, so nothing may be
		// reported missing — in particular the VM identity columns must not be
		// asked of host-level worksheets.
		if len(sh.MissingColumns) != 0 {
			t.Errorf("%s reports missing columns %v on a complete export", sh.Name, sh.MissingColumns)
		}
		if (sh.Name == sheetVSwitch || sh.Name == sheetVPort) && contains(sh.IgnoredColumns, "Object ID") {
			t.Errorf("%s ignores its Object ID join column", sh.Name)
		}
	}
}

// History, diff and trends order by run time, so an imported run must carry the
// time the data was captured — not the day it happened to be imported.
func TestRunIsStampedWithTheCaptureTimeAndRecordsTheImportTime(t *testing.T) {
	captured := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	imported := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	result, err := Parse(openFixture(t, writeFixtureWorkbook(t, nil)), Options{CapturedAt: captured})
	if err != nil {
		t.Fatal(err)
	}
	store, err := assessment.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run, err := result.Write(context.Background(), store, imported)
	if err != nil {
		t.Fatal(err)
	}
	if !run.StartedAt.Equal(captured) {
		t.Errorf("run started at %v, want the capture time %v", run.StartedAt, captured)
	}
	if !strings.Contains(run.Note, "Imported at: 2026-09-20T09:00:00Z") || !strings.Contains(run.Note, "Captured at: 2025-06-01T12:00:00Z") {
		t.Errorf("note does not record both times:\n%s", run.Note)
	}
	contexts, _ := store.ContextRuns(context.Background(), run.ID)
	for _, c := range contexts {
		if c.FinishedAt.After(captured.Add(time.Minute)) {
			t.Errorf("context %s finished at %v, want it stamped with the capture time", c.Name, c.FinishedAt)
		}
	}
}
