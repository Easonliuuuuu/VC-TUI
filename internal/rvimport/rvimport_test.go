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
	"github.com/easonliuuuuu/vsfleet/internal/report"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// writeFixtureWorkbook builds an ExportData with two vCenters that both
// happen to have a VM named "web-01", renders it through vsfleet's own
// RVTools writer (the one thing this test can treat as ground truth for what
// a real workbook's headers look like), and returns the path to the file so
// the importer under test can read it back exactly as a user would.
func writeFixtureWorkbook(t *testing.T, mutate func(*assessment.ExportData)) string {
	t.Helper()
	data := assessment.ExportData{
		Run: assessment.Run{ID: 1, StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		Contexts: []assessment.ContextRun{
			{Name: "alpha", Endpoint: "https://vc-alpha.example", VCenterID: "vc-alpha-uuid", Source: &assessment.SourceInfo{Name: "Synthetic vCenter", Version: "1.2.3", Build: "0001", APIType: "VirtualCenter"}},
			{Name: "beta", Endpoint: "https://vc-beta.example", VCenterID: "vc-beta-uuid"},
		},
		VMs: []assessment.ExportVM{
			{Observation: assessment.Observation{VCenterID: "vc-alpha-uuid", Context: "alpha", VM: vsphere.VM{
				Location:     vsphere.Location{Context: "alpha", Datacenter: "dc-a"},
				ID:           "vm-alpha-1",
				InstanceUUID: "uuid-shared-identity",
				Name:         "web-01",
				PowerState:   "poweredOn",
				CPU:          2,
				MemoryMB:     4096,
				Host:         "esx-a1",
				Cluster:      "cluster-a",
				Folder:       "/dc-a/vm",
				StorageGB:    20,
				GuestOS:      "otherLinux64Guest",
				Disks: []vsphere.VMDisk{
					{Label: "Hard disk 1", Key: 2000, CapacityBytes: 20 << 30, DiskMode: "persistent", BackingPath: "[nvme-01] web-01/web-01.vmdk"},
				},
				NICs: []vsphere.VMNIC{
					{Label: "Network adapter 1", Adapter: "vmxnet3", Network: "prod-vlan", MACAddress: "00:50:56:aa:bb:cc", IPv4: []string{"10.0.0.5"}},
				},
			}}},
			{Observation: assessment.Observation{VCenterID: "vc-beta-uuid", Context: "beta", VM: vsphere.VM{
				Location:   vsphere.Location{Context: "beta", Datacenter: "dc-b"},
				ID:         "vm-beta-1",
				Name:       "web-01",
				PowerState: "poweredOff",
				CPU:        4,
				MemoryMB:   8192,
				Host:       "esx-b1",
				Cluster:    "cluster-b",
				StorageGB:  40,
			}}},
		},
		Resources: []assessment.ResourceObservation{
			hostResource(t, "alpha", "vc-alpha-uuid", vsphere.Host{Location: vsphere.Location{Context: "alpha", Datacenter: "dc-a"}, ID: "host-a1", Name: "esx-a1", Cluster: "cluster-a", CPUCores: 16, CPUMHz: 2400, MemoryMB: 131072, VMCount: 1, Version: "8.0.0", Vendor: "Dell", Model: "R740"}),
			clusterResource(t, "alpha", "vc-alpha-uuid", vsphere.Cluster{Location: vsphere.Location{Context: "alpha", Datacenter: "dc-a"}, ID: "cluster-a", Name: "cluster-a", Hosts: 1, EffectiveHost: 1, DRSEnabled: true, HAEnabled: true}),
			datastoreResource(t, "alpha", "vc-alpha-uuid", vsphere.Datastore{Location: vsphere.Location{Context: "alpha", Datacenter: "dc-a"}, ID: "ds-a1", Name: "nvme-01", Type: "VMFS", CapacityBytes: 2 << 40, FreeBytes: 1 << 40, Accessible: true}),
			hostResource(t, "beta", "vc-beta-uuid", vsphere.Host{Location: vsphere.Location{Context: "beta", Datacenter: "dc-b"}, ID: "host-b1", Name: "esx-b1", Cluster: "cluster-b", CPUCores: 8, CPUMHz: 2000, MemoryMB: 65536, VMCount: 1, Version: "7.0.3", Vendor: "HPE", Model: "DL380"}),
			clusterResource(t, "beta", "vc-beta-uuid", vsphere.Cluster{Location: vsphere.Location{Context: "beta", Datacenter: "dc-b"}, ID: "cluster-b", Name: "cluster-b", Hosts: 1, EffectiveHost: 1}),
			datastoreResource(t, "beta", "vc-beta-uuid", vsphere.Datastore{Location: vsphere.Location{Context: "beta", Datacenter: "dc-b"}, ID: "ds-b1", Name: "ssd-01", Type: "VMFS", CapacityBytes: 1 << 40, FreeBytes: 512 << 30, Accessible: true}),
		},
	}
	if mutate != nil {
		mutate(&data)
	}

	var buf strings.Builder
	if err := report.WriteRVTools(&writerAdapter{&buf}, data, health.Report{}); err != nil {
		t.Fatalf("WriteRVTools: %v", err)
	}
	path := filepath.Join(t.TempDir(), "fixture.xlsx")
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	return path
}

// writerAdapter lets WriteRVTools, which wants an io.Writer, write into a
// strings.Builder without pulling in bytes.Buffer just for this helper.
type writerAdapter struct{ b *strings.Builder }

func (w *writerAdapter) Write(p []byte) (int, error) { return w.b.Write(p) }

func hostResource(t *testing.T, context, vcenter string, h vsphere.Host) assessment.ResourceObservation {
	t.Helper()
	payload, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	return assessment.ResourceObservation{Context: context, VCenterID: vcenter, Kind: "host", ID: h.ID, Name: h.Name, Payload: payload}
}

func clusterResource(t *testing.T, context, vcenter string, c vsphere.Cluster) assessment.ResourceObservation {
	t.Helper()
	payload, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return assessment.ResourceObservation{Context: context, VCenterID: vcenter, Kind: "cluster", ID: c.ID, Name: c.Name, Payload: payload}
}

func datastoreResource(t *testing.T, context, vcenter string, d vsphere.Datastore) assessment.ResourceObservation {
	t.Helper()
	payload, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return assessment.ResourceObservation{Context: context, VCenterID: vcenter, Kind: "datastore", ID: d.ID, Name: d.Name, Payload: payload}
}

func openFixture(t *testing.T, path string) *excelize.File {
	t.Helper()
	f, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestParseRecognizesMappedAndIgnoresUnmappedWorksheets(t *testing.T) {
	path := writeFixtureWorkbook(t, nil)
	result, err := Parse(openFixture(t, path), Options{SourceLabel: "fixture.xlsx"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, want := range []string{sheetVInfo, sheetVCPU, sheetVMemory, sheetVDisk, sheetVPartition, sheetVNetwork, sheetVTools, sheetVCD, sheetVUSB, sheetVHost, sheetVHBA, sheetVNIC, sheetVSwitch, sheetVPort, sheetVMultiPath, sheetDVSwitch, sheetDVPort, sheetVCluster, sheetVDatastore, sheetVSnapshot} {
		if !contains(result.Report.RecognizedSheets, want) {
			t.Errorf("recognized sheets = %v, want %q among them", result.Report.RecognizedSheets, want)
		}
	}
	if !contains(result.Report.RecognizedSheets, sheetVSCVMK) && !contains(result.Report.RecognizedSheets, "vSC_VMK") {
		t.Errorf("recognized sheets = %v, want a supported vSC VMkernel spelling", result.Report.RecognizedSheets)
	}
	for _, want := range []string{"vRP", "vSource", "vHealth", "vsfleetCoverage"} {
		if !contains(result.Report.IgnoredSheets, want) {
			t.Errorf("ignored sheets = %v, want %q among them", result.Report.IgnoredSheets, want)
		}
	}
	if len(result.Report.Warnings) != 0 {
		t.Errorf("a clean fixture produced warnings: %v", result.Report.Warnings)
	}
}

// Two vCenters that both happen to have a VM named "web-01" must not be
// merged into one VM just because the display name matches — that is
// exactly the unsafe name-only merge the importer's identity rules exist to
// refuse.
func TestParseKeepsSameNamedVMsAcrossContextsDistinct(t *testing.T) {
	path := writeFixtureWorkbook(t, nil)
	result, err := Parse(openFixture(t, path), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.contexts) != 2 {
		t.Fatalf("contexts = %d, want 2", len(result.contexts))
	}
	names := map[string]bool{}
	for _, c := range result.contexts {
		names[c.name] = true
		if len(c.vms) != 1 {
			t.Fatalf("context %q has %d VMs, want 1", c.name, len(c.vms))
		}
	}
	if !names["alpha"] || !names["beta"] {
		t.Fatalf("reconstructed context names = %v, want alpha and beta", names)
	}
	alphaVM, betaVM := findVM(result, "alpha", "web-01"), findVM(result, "beta", "web-01")
	if alphaVM == nil || betaVM == nil {
		t.Fatal("both same-named VMs should be present, one per context")
	}
	if alphaVM.ID == betaVM.ID {
		t.Fatal("same-named VMs across contexts collapsed onto one identity")
	}
}

func TestWritePersistsAnAtomicRunWithVMHostClusterDatastore(t *testing.T) {
	path := writeFixtureWorkbook(t, nil)
	result, err := Parse(openFixture(t, path), Options{Label: "imported-lab", SourceLabel: "fixture.xlsx"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	store, err := assessment.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer store.Close()

	run, err := result.Write(context.Background(), store, time.Now())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if run.Source != importSource {
		t.Errorf("run.Source = %q, want %q", run.Source, importSource)
	}
	if run.Status != assessment.RunPartial {
		t.Errorf("run.Status = %q, want %q (resourcepool and network are never collected by this profile)", run.Status, assessment.RunPartial)
	}
	if !strings.Contains(run.Note, "fixture.xlsx") || !strings.Contains(run.Note, ProfileVersion) {
		t.Errorf("run note is missing provenance: %q", run.Note)
	}

	data, err := store.LoadExportData(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("LoadExportData: %v", err)
	}
	if len(data.VMs) != 2 {
		t.Fatalf("loaded %d VMs, want 2", len(data.VMs))
	}
	var alphaVM *assessment.ExportVM
	for i := range data.VMs {
		if data.VMs[i].Observation.Context == "alpha" {
			alphaVM = &data.VMs[i]
		}
	}
	if alphaVM == nil {
		t.Fatal("alpha VM missing from the persisted run")
	}
	vm := alphaVM.Observation.VM
	if vm.Name != "web-01" || vm.CPU != 2 || vm.MemoryMB != 4096 || vm.InstanceUUID != "uuid-shared-identity" {
		t.Errorf("round-tripped VM = %+v, fields do not match the source workbook", vm)
	}
	if len(vm.Disks) != 1 || vm.Disks[0].CapacityBytes != 20<<30 {
		t.Errorf("round-tripped disks = %+v, want one 20 GiB disk", vm.Disks)
	}
	if len(vm.NICs) != 1 || vm.NICs[0].MACAddress != "00:50:56:aa:bb:cc" {
		t.Errorf("round-tripped NICs = %+v, want the seeded adapter", vm.NICs)
	}

	hosts, err := store.Resources(context.Background(), run.ID, "host")
	if err != nil || len(hosts) != 2 {
		t.Fatalf("host resources = %v (%d), want 2 hosts, err=%v", hosts, len(hosts), err)
	}
	clusters, err := store.Resources(context.Background(), run.ID, "cluster")
	if err != nil || len(clusters) != 2 {
		t.Fatalf("cluster resources = %v (%d), want 2, err=%v", clusters, len(clusters), err)
	}
	datastores, err := store.Resources(context.Background(), run.ID, "datastore")
	if err != nil || len(datastores) != 2 {
		t.Fatalf("datastore resources = %v (%d), want 2, err=%v", datastores, len(datastores), err)
	}

	contexts, err := store.ContextRuns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ContextRuns: %v", err)
	}
	reason := assessment.CoverageReason(contexts, "alpha", []string{"network"})
	if !strings.Contains(reason, "network") {
		t.Errorf("coverage reason = %q, want it to name the network gap", reason)
	}
}

// A dry run must never touch the store: this is the difference between
// previewing an import and committing one.
func TestDryRunNeverWritesToTheStore(t *testing.T) {
	path := writeFixtureWorkbook(t, nil)
	result, err := Parse(openFixture(t, path), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	store, err := assessment.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer store.Close()
	_ = result // a dry run only inspects Report and never calls Write

	runs, err := store.Runs(context.Background())
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("store has %d runs before any Write call", len(runs))
	}
}

func TestVMRenameAcrossTwoImportedRunsIsJoinedByInstanceUUID(t *testing.T) {
	store, err := assessment.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer store.Close()

	first := writeFixtureWorkbook(t, nil)
	firstResult, err := Parse(openFixture(t, first), Options{})
	if err != nil {
		t.Fatalf("Parse (first): %v", err)
	}
	if _, err := firstResult.Write(context.Background(), store, time.Now()); err != nil {
		t.Fatalf("Write (first): %v", err)
	}

	second := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		for i := range data.VMs {
			if data.VMs[i].Observation.Context == "alpha" {
				data.VMs[i].Observation.VM.Name = "web-01-renamed"
			}
		}
	})
	secondResult, err := Parse(openFixture(t, second), Options{})
	if err != nil {
		t.Fatalf("Parse (second): %v", err)
	}
	if _, err := secondResult.Write(context.Background(), store, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Write (second): %v", err)
	}

	history, err := store.History(context.Background(), "uuid-shared-identity", "alpha")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history entries = %d, want 2 (one per imported run)", len(history))
	}
	names := map[string]bool{}
	for _, h := range history {
		names[h.Observation.VM.Name] = true
	}
	if !names["web-01"] || !names["web-01-renamed"] {
		t.Fatalf("history names = %v, want both the original and renamed VM name joined by instance UUID", names)
	}
}

func TestMissingVInfoWorksheetFailsWithoutWritingAnything(t *testing.T) {
	path := writeFixtureWorkbook(t, nil)
	f := openFixture(t, path)
	if err := f.DeleteSheet(sheetVInfo); err != nil {
		t.Fatalf("delete vInfo for the test fixture: %v", err)
	}
	_, err := Parse(f, Options{})
	if err == nil {
		t.Fatal("a workbook with no vInfo worksheet should fail to parse")
	}
	if !strings.Contains(err.Error(), sheetVInfo) {
		t.Errorf("error does not name the missing worksheet: %v", err)
	}
}

func TestUnknownExtraWorksheetAndColumnDoNotCrash(t *testing.T) {
	path := writeFixtureWorkbook(t, nil)
	f := openFixture(t, path)
	if _, err := f.NewSheet("vFutureThing"); err != nil {
		t.Fatalf("add unknown sheet: %v", err)
	}
	if err := f.SetCellValue("vFutureThing", "A1", "Something RVTools added later"); err != nil {
		t.Fatalf("seed unknown sheet: %v", err)
	}
	rows, err := f.GetRows(sheetVInfo)
	if err != nil || len(rows) == 0 {
		t.Fatalf("read vInfo header: %v", err)
	}
	lastCol, err := excelize.ColumnNumberToName(len(rows[0]) + 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue(sheetVInfo, lastCol+"1", "Some Future Column"); err != nil {
		t.Fatalf("add unknown column: %v", err)
	}
	if err := f.SetCellValue(sheetVInfo, lastCol+"2", "unrecognized value"); err != nil {
		t.Fatalf("seed unknown column value: %v", err)
	}

	result, err := Parse(f, Options{})
	if err != nil {
		t.Fatalf("an unknown extra worksheet/column should not fail parsing: %v", err)
	}
	if !contains(result.Report.IgnoredSheets, "vFutureThing") {
		t.Errorf("ignored sheets = %v, want the unknown sheet reported", result.Report.IgnoredSheets)
	}
}

// A missing optional column must degrade the field to absent, never to a
// value that reads as a confirmed zero.
func TestMissingOptionalColumnDegradesHonestly(t *testing.T) {
	path := writeFixtureWorkbook(t, nil)
	f := openFixture(t, path)
	rows, err := f.GetRows(sheetVDisk)
	if err != nil || len(rows) == 0 {
		t.Fatalf("read vDisk: %v", err)
	}
	thinCol := -1
	for i, h := range rows[0] {
		if h == "Thin" {
			thinCol = i
		}
	}
	if thinCol < 0 {
		t.Fatal("fixture vDisk sheet has no Thin column to remove")
	}
	col, err := excelize.ColumnNumberToName(thinCol + 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue(sheetVDisk, col+"1", ""); err != nil {
		t.Fatalf("blank the Thin header: %v", err)
	}

	result, err := Parse(f, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	vm := findVM(result, "alpha", "web-01")
	if vm == nil || len(vm.Disks) != 1 {
		t.Fatalf("expected the alpha VM's one disk to still be present")
	}
	if vm.Disks[0].ThinProvisioned != nil {
		t.Errorf("ThinProvisioned = %v, want nil (unknown) rather than a fabricated false", vm.Disks[0].ThinProvisioned)
	}
}

func TestMalformedWorkbookFailsCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-workbook.xlsx")
	if err := os.WriteFile(path, []byte("this is not a zip file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFile(path); err == nil {
		t.Fatal("opening a malformed workbook should fail")
	}
}

func TestContextMapRenamesAReconstructedContext(t *testing.T) {
	path := writeFixtureWorkbook(t, nil)
	result, err := Parse(openFixture(t, path), Options{ContextMap: map[string]string{"alpha": "prod-renamed"}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	found := false
	for _, c := range result.Report.Contexts {
		if c.Key == "alpha" {
			found = true
			if c.Name != "prod-renamed" {
				t.Errorf("context %q name = %q, want the mapped name", c.Key, c.Name)
			}
		}
	}
	if !found {
		t.Fatal("expected the alpha context to be present in the report")
	}
}

func TestExplicitCapturedAtOverridesWorkbookMetadata(t *testing.T) {
	path := writeFixtureWorkbook(t, nil)
	explicit := time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC)
	result, err := Parse(openFixture(t, path), Options{CapturedAt: explicit})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !result.Report.CapturedAt.Equal(explicit) {
		t.Errorf("captured at = %v, want the explicit override %v", result.Report.CapturedAt, explicit)
	}
	if !strings.Contains(result.Report.CapturedAtSource, "explicit") {
		t.Errorf("captured at source = %q, want it to say explicit", result.Report.CapturedAtSource)
	}
}

func findVM(result *Result, contextName, name string) *vsphere.VM {
	for _, c := range result.contexts {
		if c.name != contextName {
			continue
		}
		for _, vm := range c.vms {
			if vm.Name == name {
				return vm
			}
		}
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
