package report

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/health"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// rvtoolsTabOrder is the tab order both WriteRVTools and RVToolsCSV must
// produce.
var rvtoolsTabOrder = []string{"vInfo", "vCPU", "vMemory", "vDisk", "vPartition", "vNetwork", "vCD", "vUSB", "vSnapshot", "vTools", "vRP", "vCluster", "vHost", "vHBA", "vNIC", "vSwitch", "vPort", "dvSwitch", "dvPort", "vSC_VMK", "vDatastore", "vMultiPath", "vHealth", "vsfleetCoverage"}

func healthReport(data assessment.ExportData) health.Report {
	return health.Evaluate(data, health.Options{Thresholds: health.DefaultThresholds()})
}

// sampleExportData builds one persisted run with a VM, its disk, NIC,
// guest partition, and snapshot, plus a host, resource pool, and datastore resource observation. Shared by the
// XLSX and CSV tests so both exercise identical evidence.
func sampleExportData(when time.Time) assessment.ExportData {
	linkSpeed, duplex, enabled, localDisk := int32(10000), true, false, false
	hostPayload, _ := json.Marshal(vsphere.Host{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "host-1", Name: "esx-1", CPUCores: 8, CPUMHz: 2400, CPUUsageMHz: 1200, MemoryMB: 32768, MemoryUsageMB: 8192, VMCount: 4,
		HBAs:       []vsphere.HostHBA{{Key: "hba-1", Device: "vmhba0", Bus: 3, Status: "online", Model: "Fibre Channel", StorageProtocol: "fc", Type: "HostFibreChannelHba", WWNN: int64Ptr(10), WWPN: int64Ptr(11)}},
		NICs:       []vsphere.HostNIC{{Key: "nic-1", Device: "vmnic0", PCI: "0000:01:00.0", Driver: "ixgben", MAC: "00:50:56:00:00:01", LinkSpeedMB: &linkSpeed, Duplex: &duplex, WakeOnLAN: true, Switch: "vSwitch0"}},
		VSwitches:  []vsphere.HostVSwitch{{Key: "switch-1", Name: "vSwitch0", NumPorts: 128, FreePorts: 120, MTU: 1500, Uplinks: []string{"vmnic0"}, Promiscuous: &enabled, MACChanges: &duplex, ForgedTransmits: &duplex, TrafficShaping: &enabled}},
		PortGroups: []vsphere.HostPortGroup{{Key: "port-1", Name: "Management Network", Switch: "vSwitch0", VLAN: 120, Promiscuous: &enabled, MACChanges: &duplex, ForgedTransmits: &duplex}},
		VMKs:       []vsphere.HostVMKernel{{Key: "vmk-1", Device: "vmk0", PortGroup: "Management Network", MAC: "00:50:56:00:00:02", MTU: 1500, TSO: &duplex, Netstack: "defaultTcpipStack", DHCP: &enabled, IP: "192.0.2.10", SubnetMask: "255.255.255.0"}},
		Multipaths: []vsphere.HostMultipath{{Key: "lun-1", LUN: "naa.123", DevicePath: "/vmfs/devices/disks/naa.123", Policy: "VMW_PSP_RR", LocalDisk: &localDisk, PathCount: 2, Active: 1, Standby: 1, WorkingPaths: 1}},
	})
	datastorePayload, _ := json.Marshal(vsphere.Datastore{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "ds-1", Name: "datastore-1", CapacityBytes: 8 << 30, FreeBytes: 2 << 30, Accessible: true})
	poolPayload, _ := json.Marshal(vsphere.ResourcePool{Location: vsphere.Location{Datacenter: "dc-a", Path: "/dc-a/host/cluster-1/Resources/app-pool"}, ID: "pool-1", Name: "app-pool", Status: "green", VMRefs: []string{"vm-1"}, CPULimitMHz: int64Ptr(12000), CPUReservationMHz: int64Ptr(1000), MemConfiguredMB: 4096})
	dvSwitchPayload, _ := json.Marshal(vsphere.DVSwitch{Location: vsphere.Location{Datacenter: "dc-a", Path: "/dc-a/network/dvs-1"}, ID: "dvs-1", Name: "DVS-1", UUID: "dvs-uuid", Vendor: "VMware", Version: "8.0.3", NumPorts: 128, MaxPorts: 4096, MaxMTU: 9000, Hosts: []string{"esx-1"}, PortGroups: []vsphere.DVPortGroup{{ID: "dvpg-1", Key: "dvportgroup-1", Name: "frontend", Switch: "DVS-1", Type: "earlyBinding", NumPorts: 64, VLAN: "120", Promiscuous: &enabled, MACChanges: &duplex, ForgedTransmits: &duplex, NotifySwitches: &duplex, Failback: &enabled, ActiveUplinks: []string{"uplink-1"}, StandbyUplinks: []string{"uplink-2"}}}})
	thin := true
	connected, uptCompatible := true, true
	cdConnected, cdStarts, usbConnected, autoDetect := true, true, false, false
	return assessment.ExportData{
		Run:       assessment.Run{ID: 7, Label: "nightly", StartedAt: when, FinishedAt: when.Add(time.Minute), Status: assessment.RunComplete, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion},
		Contexts:  []assessment.ContextRun{{Name: "prod", Endpoint: "https://vc.example", Datacenter: "dc-a", VCenterID: "vc-uuid", VMStatus: "success", Collections: []assessment.CollectionRun{{Kind: "host", Status: "success", ItemCount: 1}, {Kind: "cluster", Status: "empty"}, {Kind: "resourcepool", Status: "success", ItemCount: 1}, {Kind: "dvswitch", Status: "success", ItemCount: 1}, {Kind: "datastore", Status: "success", ItemCount: 1}}}},
		VMs:       []assessment.ExportVM{{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-uuid", VM: vsphere.VM{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "vm-1", Name: "app", PowerState: "poweredOn", CPU: 2, MemoryMB: 4096, StorageGB: 10, GuestOS: "Ubuntu", InstanceUUID: "instance", BIOSUUID: "bios", Host: "esx-1", ToolsState: "guestToolsRunning", ToolsVersion: "12352", ToolsVersionStatus: "guestToolsCurrent", Disks: []vsphere.VMDisk{{Key: 101, Label: "Hard disk 1", CapacityBytes: 8 << 30, UUID: "disk-uuid", SharedBus: "noSharing", ThinProvisioned: &thin, BackingPath: "[ds] app/app.vmdk"}}, NICs: []vsphere.VMNIC{{Key: 201, Label: "Network adapter 1", Network: "VM Network", Connected: &connected, UPTCompatible: &uptCompatible, IPv4: []string{"192.0.2.20"}}}, CDROMs: []vsphere.VMCDROM{{Key: 301, Label: "CD/DVD drive 1", Connected: &cdConnected, StartsConnected: &cdStarts, BackingType: "iso", BackingPath: "[datastore-1] app/install.iso", BackingDatastore: "datastore-1", BackingObjectID: "backing-1", UseAutoDetect: &autoDetect, Controller: "IDE", ControllerLabel: "IDE controller 0", UnitNumber: int32Ptr(0)}}, USBs: []vsphere.VMUSB{{Key: 401, Label: "USB device 1", Connected: &usbConnected, Vendor: 4660, Product: 22136, Family: []string{"storage", "hid"}, Speed: []string{"high", "full"}, BackingType: "remoteHost", BackingDevice: "vid:1234 pid:5678", BackingHost: "esx-1", UseAutoDetect: &autoDetect, Controller: "USB", ControllerLabel: "USB controller 0", UnitNumber: int32Ptr(1)}}, Partitions: []vsphere.VMPartition{{Path: "/", CapacityBytes: 8 << 30, FreeBytes: 2 << 30, FilesystemType: "ext4"}}}}, Snapshots: []vsphere.VMSnapshot{{ID: "snap-1", Name: "base", CreateTime: when, PowerState: "poweredOn", Quiesced: true}}}},
		Resources: []assessment.ResourceObservation{{Context: "prod", VCenterID: "vc-uuid", Kind: "host", ID: "host-1", Name: "esx-1", Payload: hostPayload}, {Context: "prod", VCenterID: "vc-uuid", Kind: "resourcepool", ID: "pool-1", Name: "app-pool", Payload: poolPayload}, {Context: "prod", VCenterID: "vc-uuid", Kind: "dvswitch", ID: "dvs-1", Name: "DVS-1", Payload: dvSwitchPayload}, {Context: "prod", VCenterID: "vc-uuid", Kind: "datastore", ID: "ds-1", Name: "datastore-1", Payload: datastorePayload}},
	}
}

func int64Ptr(value int64) *int64 { return &value }

func int32Ptr(value int32) *int32 { return &value }

func TestWriteRVToolsIsDeterministicAndComplete(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	data := sampleExportData(when)
	var first, second bytes.Buffer
	if err := WriteRVTools(&first, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	if err := WriteRVTools(&second, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("repeated exports differ")
	}
	f, err := excelize.OpenReader(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gotSheets := f.GetSheetList()
	if len(gotSheets) != len(rvtoolsTabOrder) {
		t.Fatalf("sheets=%v", gotSheets)
	}
	for i := range rvtoolsTabOrder {
		if gotSheets[i] != rvtoolsTabOrder[i] {
			t.Fatalf("sheets=%v", gotSheets)
		}
	}
	if got, _ := f.GetCellValue("vInfo", "A2"); got != "app" {
		t.Fatalf("vInfo A2=%q", got)
	}
	if got, _ := f.GetCellValue("vCPU", "D2"); got != "2" {
		t.Fatalf("vCPU CPUs=%q", got)
	}
	if got, _ := f.GetCellValue("vMemory", "D2"); got != "4096" {
		t.Fatalf("vMemory size=%q", got)
	}
	if got, _ := f.GetCellValue("dvSwitch", "A2"); got != "DVS-1" {
		t.Fatalf("dvSwitch name=%q", got)
	}
	if got, _ := f.GetCellValue("dvPort", "A2"); got != "frontend" {
		t.Fatalf("dvPort name=%q", got)
	}
	for sheet, cells := range map[string]map[string]string{
		"vDisk":    {"V1": "Shared Bus"},
		"vHBA":     {"F1": "Pci"},
		"vSwitch":  {"C1": "Free Ports", "F1": "Promiscuous Mode", "G1": "Mac Changes", "H1": "Forged Transmits", "I1": "Traffic Shaping"},
		"vPort":    {"A1": "Port Group", "D1": "Promiscuous Mode", "E1": "Mac Changes", "F1": "Forged Transmits"},
		"dvSwitch": {"A1": "Switch", "C1": "Max Ports"},
		"dvPort":   {"A1": "Port", "B1": "Switch", "I1": "Allow Promiscuous", "J1": "Mac Changes", "K1": "Forged Transmits", "L1": "Policy", "M1": "Notify Switch", "N1": "Rolling Order", "O1": "In Traffic Shaping", "P1": "Out Traffic Shaping", "S1": "Active Uplink", "T1": "Standby Uplink"},
		"vCluster": {"C1": "numEffectiveHosts"},
		"vRP":      {"G1": "CPU overheadLimit", "K1": "CPU expandableReservation", "L1": "Mem Configured", "N1": "Mem overheadLimit", "R1": "Mem expandableReservation"},
	} {
		for cell, want := range cells {
			if got, _ := f.GetCellValue(sheet, cell); got != want {
				t.Errorf("%s %s=%q, want %q", sheet, cell, got, want)
			}
		}
	}
	if got, _ := f.GetCellValue("vDisk", "V2"); got != "noSharing" {
		t.Fatalf("vDisk shared bus=%q, want noSharing", got)
	}
	if got, _ := f.GetCellValue("dvPort", "N2"); got != "TRUE" {
		t.Fatalf("dvPort rolling order=%q, want raw value TRUE", got)
	}
	if got, _ := f.GetCellValue("vDisk", "A2"); got != "app" {
		t.Fatalf("vDisk A2=%q", got)
	}
	if got, _ := f.GetCellValue("vDisk", "G2"); got != "8192" {
		t.Fatalf("vDisk capacity=%q", got)
	}
	if got, _ := f.GetCellValue("vNetwork", "F2"); got != "VM Network" {
		t.Fatalf("vNetwork network=%q", got)
	}
	if got, _ := f.GetCellValue("vNetwork", "K2"); got != "192.0.2.20" {
		t.Fatalf("vNetwork ipv4=%q", got)
	}
	if got, _ := f.GetCellValue("vNetwork", "M2"); got != "TRUE" {
		t.Fatalf("vNetwork UPT compatibility=%q", got)
	}
	if got, _ := f.GetCellValue("vCD", "D2"); got != "CD/DVD drive 1" {
		t.Fatalf("vCD device=%q", got)
	}
	if got, _ := f.GetCellValue("vCD", "F2"); got != "TRUE" {
		t.Fatalf("vCD connected=%q", got)
	}
	if got, _ := f.GetCellValue("vCD", "I2"); got != "[datastore-1] app/install.iso" {
		t.Fatalf("vCD backing path=%q", got)
	}
	if got, _ := f.GetCellValue("vUSB", "F2"); got != "FALSE" {
		t.Fatalf("vUSB connected=%q", got)
	}
	if got, _ := f.GetCellValue("vUSB", "I2"); got != "hid, storage" {
		t.Fatalf("vUSB family=%q", got)
	}
	if got, _ := f.GetCellValue("vUSB", "J2"); got != "full, high" {
		t.Fatalf("vUSB speed=%q", got)
	}
	if got, _ := f.GetCellValue("vTools", "D2"); got != "guestToolsRunning" {
		t.Fatalf("vTools state=%q", got)
	}
	if got, _ := f.GetCellValue("vTools", "E2"); got != "12352" {
		t.Fatalf("vTools version=%q", got)
	}
	if got, _ := f.GetCellValue("vTools", "F2"); got != "guestToolsCurrent" {
		t.Fatalf("vTools version status=%q", got)
	}
	for sheet, cells := range map[string][2]string{
		"vHBA":       {"A2", "vmhba0"},
		"vNIC":       {"A2", "vmnic0"},
		"vSwitch":    {"A2", "vSwitch0"},
		"vPort":      {"A2", "Management Network"},
		"vSC_VMK":    {"A2", "vmk0"},
		"vMultiPath": {"A2", "naa.123"},
	} {
		if got, _ := f.GetCellValue(sheet, cells[0]); got != cells[1] {
			t.Fatalf("%s %s=%q", sheet, cells[0], got)
		}
	}
	if got, _ := f.GetCellValue("vMultiPath", "D2"); got != "FALSE" {
		t.Fatalf("vMultiPath local disk=%q, want FALSE", got)
	}
	if got, _ := f.GetCellValue("vSnapshot", "E2"); got != "2026/01/02 03:04:05" {
		t.Fatalf("snapshot time=%q", got)
	}
	if got, _ := f.GetCellValue("vRP", "E2"); got != "2" {
		t.Fatalf("vRP vCPUs=%q", got)
	}
	if got, _ := f.GetCellValue("vsfleetCoverage", "J2"); got != "vInfo" {
		t.Fatalf("coverage sheet=%q", got)
	}
	for i, name := range rvtoolsTabOrder[:len(rvtoolsTabOrder)-1] {
		cell := fmt.Sprintf("J%d", i+2)
		if got, _ := f.GetCellValue("vsfleetCoverage", cell); got != name {
			t.Errorf("coverage sheet %s=%q, want %q", cell, got, name)
		}
	}
	if got, _ := f.GetCellValue("vHealth", "A1"); got != "Name" {
		t.Fatalf("vHealth header=%q", got)
	}
}

func TestWriteRVToolsSetsWorksheetDimensions(t *testing.T) {
	data := sampleExportData(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	var output bytes.Buffer
	if err := WriteRVTools(&output, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatalf("opening workbook ZIP: %v", err)
	}
	sheets, err := rvtoolsSheets(data, healthReport(data))
	if err != nil {
		t.Fatalf("building worksheet expectations: %v", err)
	}
	files := make(map[string]*zip.File, len(archive.File))
	for _, file := range archive.File {
		files[file.Name] = file
	}
	for i, sheet := range sheets {
		path := fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)
		file := files[path]
		if file == nil {
			t.Fatalf("workbook is missing %s for %s", path, sheet.name)
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", path, err)
		}
		contents, readErr := io.ReadAll(rc)
		closeErr := rc.Close()
		if readErr != nil {
			t.Fatalf("reading %s: %v", path, readErr)
		}
		if closeErr != nil {
			t.Fatalf("closing %s: %v", path, closeErr)
		}
		var worksheet struct {
			Dimension struct {
				Ref string `xml:"ref,attr"`
			} `xml:"dimension"`
		}
		if err := xml.Unmarshal(contents, &worksheet); err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		lastCol, err := excelize.ColumnNumberToName(len(sheet.headers))
		if err != nil {
			t.Fatalf("converting %s column count: %v", sheet.name, err)
		}
		want := fmt.Sprintf("A1:%s%d", lastCol, len(sheet.rows)+1)
		if worksheet.Dimension.Ref != want {
			t.Errorf("%s dimension=%q, want %q", sheet.name, worksheet.Dimension.Ref, want)
		}
	}
}

func TestRVToolsCSVMatchesXLSXAndIsDeterministic(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	data := sampleExportData(when)

	first, err := RVToolsCSV(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	second, err := RVToolsCSV(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(rvtoolsTabOrder) {
		t.Fatalf("files=%d, want %d", len(first), len(rvtoolsTabOrder))
	}
	for i, name := range rvtoolsTabOrder {
		if first[i].Name != name+".csv" {
			t.Fatalf("file[%d]=%q, want %q", i, first[i].Name, name+".csv")
		}
		if !bytes.Equal(first[i].Data, second[i].Data) {
			t.Fatalf("%s differs between renders", first[i].Name)
		}
	}

	byName := make(map[string][]byte, len(first))
	for _, file := range first {
		byName[file.Name] = file.Data
	}

	vInfo := readCSV(t, byName["vInfo.csv"])
	if vInfo[0][0] != "VM" || vInfo[1][0] != "app" {
		t.Fatalf("vInfo.csv rows=%v", vInfo)
	}

	vTools := readCSV(t, byName["vTools.csv"])
	if got := vTools[1]; got[0] != "app" || got[3] != "guestToolsRunning" || got[4] != "12352" || got[5] != "guestToolsCurrent" {
		t.Fatalf("vTools.csv row=%v", got)
	}
	vCD := readCSV(t, byName["vCD.csv"])
	if got := vCD[1]; got[0] != "app" || got[3] != "CD/DVD drive 1" || got[5] != "true" || got[8] != "[datastore-1] app/install.iso" {
		t.Fatalf("vCD.csv row=%v", got)
	}
	vUSB := readCSV(t, byName["vUSB.csv"])
	if got := vUSB[1]; got[0] != "app" || got[5] != "false" || got[8] != "hid, storage" || got[9] != "full, high" {
		t.Fatalf("vUSB.csv row=%v", got)
	}

	vSnapshot := readCSV(t, byName["vSnapshot.csv"])
	if got := vSnapshot[1][4]; got != "2026-01-02T03:04:05Z" {
		t.Fatalf("vSnapshot.csv timestamp=%q, want RFC3339", got)
	}
	if got := vSnapshot[1][5]; got != "true" {
		t.Fatalf("vSnapshot.csv quiesced=%q, want lowercase true", got)
	}

	vDisk := readCSV(t, byName["vDisk.csv"])
	if got := vDisk[1][6]; got != "8192" {
		t.Fatalf("vDisk.csv capacity=%q", got)
	}
	vRP := readCSV(t, byName["vRP.csv"])
	if got := vRP[0][4]; got != "vCPUs" || vRP[1][4] != "2" {
		t.Fatalf("vRP.csv vCPUs header/row=%q/%q", got, vRP[1][4])
	}

	coverage := readCSV(t, byName["vsfleetCoverage.csv"])
	if got := coverage[1][9]; got != "vInfo" {
		t.Fatalf("vsfleetCoverage.csv sheet=%q", got)
	}
	if got := coverage[1][2]; got != "2026-01-02T03:04:05Z" {
		t.Fatalf("vsfleetCoverage.csv run started=%q, want RFC3339", got)
	}
}

func TestRVToolsPreservesTemplateRowsAndHealthCoverageGaps(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	data := sampleExportData(when)
	data.VMs = append(data.VMs, assessment.ExportVM{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-uuid", VM: vsphere.VM{ID: "tpl-1", Name: "golden", IsTemplate: true}}})
	var output bytes.Buffer
	if err := WriteRVTools(&output, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got, _ := f.GetCellValue("vInfo", "A2"); got != "golden" {
		t.Fatalf("template vInfo name=%q", got)
	}
	if got, _ := f.GetCellValue("vInfo", "C2"); !strings.EqualFold(got, "true") {
		t.Fatalf("template vInfo flag=%q", got)
	}
	if got, _ := f.GetCellValue("vsfleetCoverage", "J24"); got != "vHealth" {
		t.Fatalf("health coverage sheet=%q", got)
	}
	if got, _ := f.GetCellValue("vsfleetCoverage", "K24"); got != "partial" {
		t.Fatalf("health coverage status=%q", got)
	}
	if got, _ := f.GetCellValue("vsfleetCoverage", "M24"); !strings.Contains(got, "datastore-zombie-vmdk") {
		t.Fatalf("health coverage message=%q", got)
	}
}

func readCSV(t *testing.T, data []byte) [][]string {
	t.Helper()
	records, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	return records
}

func TestWriteRVToolsMarksDeviceTabsNotRecordedForOldRuns(t *testing.T) {
	data := assessment.ExportData{
		Run:      assessment.Run{ID: 8, StartedAt: time.Unix(0, 0).UTC(), FinishedAt: time.Unix(1, 0).UTC(), Status: assessment.RunComplete, InventorySchemaVersion: "1"},
		Contexts: []assessment.ContextRun{{Name: "prod", VMStatus: "success"}},
		VMs:      []assessment.ExportVM{{Observation: assessment.Observation{Context: "prod", VM: vsphere.VM{ID: "vm-1", Name: "app", Disks: []vsphere.VMDisk{{Key: 1}}, NICs: []vsphere.VMNIC{{Key: 2}}}}}},
	}
	var output bytes.Buffer
	if err := WriteRVTools(&output, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Coverage rows follow the tab order after the header: vInfo(2), vCPU(3),
	// vMemory(4), vDisk(5), vPartition(6), vNetwork(7), vCD(8), vUSB(9),
	// vSnapshot(10), vTools(11), vRP(12), vCluster(13), vHost(14), vHBA(15),
	// vNIC(16), vSwitch(17), vPort(18), dvSwitch(19), dvPort(20),
	// vSC_VMK(21), vDatastore(22), vMultiPath(23), vHealth(24).
	for row, sheet := range map[string]string{"5": "vDisk", "7": "vNetwork", "8": "vCD", "9": "vUSB"} {
		if got, _ := f.GetCellValue("vsfleetCoverage", "J"+row); got != sheet {
			t.Fatalf("coverage sheet row %s=%q, want %q", row, got, sheet)
		}
		if got, _ := f.GetCellValue("vsfleetCoverage", "K"+row); got != "not recorded" {
			t.Fatalf("coverage status row %s=%q", row, got)
		}
		if got, _ := f.GetCellValue("vsfleetCoverage", "L"+row); got != "0" {
			t.Fatalf("coverage count row %s=%q", row, got)
		}
	}
	for row, sheet := range map[string]string{"8": "vCD", "9": "vUSB"} {
		if got, _ := f.GetCellValue("vsfleetCoverage", "M"+row); got != "capture predates CD-ROM and USB device inventory" {
			t.Fatalf("%s coverage message=%q", sheet, got)
		}
	}
	// vTools still has one row per VM on a pre-schema-3 run (the running
	// status predates the version columns), so its coverage row carries the
	// VM collection's own status with an explanatory message instead of
	// "not recorded".
	if got, _ := f.GetCellValue("vsfleetCoverage", "J11"); got != "vTools" {
		t.Fatalf("coverage sheet row 11=%q, want vTools", got)
	}
	if got, _ := f.GetCellValue("vsfleetCoverage", "K11"); got != "success" {
		t.Fatalf("vTools coverage status=%q", got)
	}
	if got, _ := f.GetCellValue("vsfleetCoverage", "L11"); got != "1" {
		t.Fatalf("vTools coverage count=%q", got)
	}
	if got, _ := f.GetCellValue("vsfleetCoverage", "M11"); got != "capture predates VMware Tools version inventory" {
		t.Fatalf("vTools coverage message=%q", got)
	}
	if got, _ := f.GetCellValue("vsfleetCoverage", "J12"); got != "vRP" {
		t.Fatalf("coverage sheet row 12=%q, want vRP", got)
	}
	if got, _ := f.GetCellValue("vsfleetCoverage", "K12"); got != "not recorded" {
		t.Fatalf("vRP coverage status=%q", got)
	}
	if got, _ := f.GetCellValue("vsfleetCoverage", "M12"); got != "capture predates resource pool inventory" {
		t.Fatalf("vRP coverage message=%q", got)
	}
	for row, sheet := range map[string]string{"15": "vHBA", "16": "vNIC", "17": "vSwitch", "18": "vPort", "21": "vSC_VMK", "23": "vMultiPath"} {
		if got, _ := f.GetCellValue("vsfleetCoverage", "J"+row); got != sheet {
			t.Fatalf("coverage sheet row %s=%q, want %q", row, got, sheet)
		}
		if got, _ := f.GetCellValue("vsfleetCoverage", "K"+row); got != "not recorded" {
			t.Fatalf("%s coverage status=%q", sheet, got)
		}
		if got, _ := f.GetCellValue("vsfleetCoverage", "M"+row); got != "capture predates host storage and network inventory" {
			t.Fatalf("%s coverage message=%q", sheet, got)
		}
	}
}

func TestRVToolsDeviceWorksheetsAreCanonicalAndContextAware(t *testing.T) {
	connected, starts, autoDetect := true, false, true
	data := assessment.ExportData{
		Run: assessment.Run{ID: 12, Status: assessment.RunComplete, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion},
		Contexts: []assessment.ContextRun{
			{Name: "a", Endpoint: "https://a.example", VMStatus: "success"},
			{Name: "b", Endpoint: "https://b.example", VMStatus: "success"},
		},
		VMs: []assessment.ExportVM{
			{Observation: assessment.Observation{Context: "b", VCenterID: "vc-b", VM: vsphere.VM{Name: "same", ID: "vm-b", IsTemplate: true, CDROMs: []vsphere.VMCDROM{{Key: 30, Label: "later"}}, USBs: []vsphere.VMUSB{{Key: 4, Label: "usb-b", Connected: &connected}}}}},
			{Observation: assessment.Observation{Context: "a", VCenterID: "vc-a", VM: vsphere.VM{Name: "same", ID: "vm-a", CDROMs: []vsphere.VMCDROM{{Key: 20, Label: "unknown", Connected: nil}, {Key: 10, Label: "disconnected", Connected: &starts, StartsConnected: &starts, BackingType: "iso"}}, USBs: []vsphere.VMUSB{{Key: 2, Label: "usb-a", Connected: &connected, Vendor: 0, Product: 0, Family: []string{"storage", "hid"}, Speed: []string{"high", "full"}, UseAutoDetect: &autoDetect}}}}},
		},
	}
	files, err := RVToolsCSV(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string][]byte, len(files))
	for _, file := range files {
		byName[file.Name] = file.Data
	}
	cd := readCSV(t, byName["vCD.csv"])
	if len(cd) != 4 || cd[1][4] != "10" || cd[2][4] != "20" || cd[1][23] != "vm-a" || cd[3][23] != "vm-b" || cd[3][27] != "b" {
		t.Fatalf("vCD canonical rows=%v", cd)
	}
	if cd[2][5] != "" {
		t.Fatalf("vCD nil Connected=%q, want empty", cd[2][5])
	}
	usb := readCSV(t, byName["vUSB.csv"])
	if len(usb) != 3 || usb[1][4] != "2" || usb[2][4] != "4" || usb[1][8] != "hid, storage" || usb[1][9] != "full, high" {
		t.Fatalf("vUSB canonical rows=%v", usb)
	}
	if usb[2][2] != "true" {
		t.Fatalf("vUSB template flag=%q, want true", usb[2][2])
	}
	if usb[1][6] != "" || usb[1][7] != "" {
		t.Fatalf("vUSB unknown IDs=%q/%q, want empty", usb[1][6], usb[1][7])
	}
	if usb[1][30] != "a" || usb[2][30] != "b" {
		t.Fatalf("vUSB context provenance=%q/%q", usb[1][30], usb[2][30])
	}
}

func TestRVToolsDeviceWorksheetsHaveNoRowsForVMsWithoutDevices(t *testing.T) {
	data := assessment.ExportData{
		Run:      assessment.Run{ID: 13, Status: assessment.RunComplete, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion},
		Contexts: []assessment.ContextRun{{Name: "prod", VMStatus: "success"}},
		VMs:      []assessment.ExportVM{{Observation: assessment.Observation{Context: "prod", VM: vsphere.VM{ID: "vm-1", Name: "empty"}}}},
	}
	files, err := RVToolsCSV(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string][]byte, len(files))
	for _, file := range files {
		byName[file.Name] = file.Data
	}
	if got := len(readCSV(t, byName["vCD.csv"])); got != 1 {
		t.Fatalf("vCD rows=%d, want header only", got)
	}
	if got := len(readCSV(t, byName["vUSB.csv"])); got != 1 {
		t.Fatalf("vUSB rows=%d, want header only", got)
	}
}

func TestWriteRVToolsMarksToolsVersionGapForSchemaTwoRuns(t *testing.T) {
	data := assessment.ExportData{
		Run:      assessment.Run{ID: 9, StartedAt: time.Unix(0, 0).UTC(), FinishedAt: time.Unix(1, 0).UTC(), Status: assessment.RunComplete, InventorySchemaVersion: "2"},
		Contexts: []assessment.ContextRun{{Name: "prod", VMStatus: "success"}},
		VMs:      []assessment.ExportVM{{Observation: assessment.Observation{Context: "prod", VM: vsphere.VM{ID: "vm-1", Name: "app", ToolsState: "guestToolsRunning", Disks: []vsphere.VMDisk{{Key: 1}}, NICs: []vsphere.VMNIC{{Key: 2}}}}}},
	}
	var output bytes.Buffer
	if err := WriteRVTools(&output, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Schema 2 already records devices, so vDisk has its real item count here
	// (unlike the "not recorded"+0 pairing schema 1 gets)...
	if got, _ := f.GetCellValue("vsfleetCoverage", "L5"); got != "1" {
		t.Fatalf("vDisk coverage count=%q, want 1 at schema 2", got)
	}
	// ...but the Tools version columns are still schema-3-only.
	if got, _ := f.GetCellValue("vTools", "D2"); got != "guestToolsRunning" {
		t.Fatalf("vTools state=%q", got)
	}
	if got, _ := f.GetCellValue("vTools", "E2"); got != "" {
		t.Fatalf("vTools version=%q, want empty at schema 2", got)
	}
	if got, _ := f.GetCellValue("vsfleetCoverage", "K11"); got != "success" {
		t.Fatalf("vTools coverage status=%q", got)
	}
	if got, _ := f.GetCellValue("vsfleetCoverage", "M11"); got != "capture predates VMware Tools version inventory" {
		t.Fatalf("vTools coverage message=%q", got)
	}
}

func TestWriteRVToolsDeviceCoverageMirrorsVMCollectionStatus(t *testing.T) {
	// vdisk/vnetwork are never persisted as their own collection kind, so the
	// coverage sheet has to read their status off the VM capture they ride on.
	connected := true
	for _, tc := range []struct {
		name           string
		context        assessment.ContextRun
		vms            []assessment.ExportVM
		status, detail string
		count          string
	}{
		{
			name:    "success",
			context: assessment.ContextRun{Name: "prod", VMStatus: "success"},
			vms:     []assessment.ExportVM{{Observation: assessment.Observation{Context: "prod", VM: vsphere.VM{ID: "vm-1", Name: "app", Disks: []vsphere.VMDisk{{Key: 1}}, NICs: []vsphere.VMNIC{{Key: 2}}, CDROMs: []vsphere.VMCDROM{{Key: 3, Connected: &connected}}, USBs: []vsphere.VMUSB{{Key: 4, Connected: &connected}}}}}},
			status:  "success",
			count:   "1",
		},
		{
			name:    "failure carries the collection error",
			context: assessment.ContextRun{Name: "prod", VMStatus: "error", Error: "collect vms: permission denied"},
			status:  "error",
			detail:  "collect vms: permission denied",
			count:   "0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := assessment.ExportData{
				Run:      assessment.Run{ID: 11, StartedAt: time.Unix(0, 0).UTC(), Status: assessment.RunComplete, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion},
				Contexts: []assessment.ContextRun{tc.context},
				VMs:      tc.vms,
			}
			var output bytes.Buffer
			if err := WriteRVTools(&output, data, healthReport(data)); err != nil {
				t.Fatal(err)
			}
			f, err := excelize.OpenReader(bytes.NewReader(output.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			// Coverage rows follow tab order: vDisk is row 5, vNetwork row 7
			// (vPartition sits between them).
			for row, sheet := range map[string]string{"5": "vDisk", "7": "vNetwork"} {
				if got, _ := f.GetCellValue("vsfleetCoverage", "J"+row); got != sheet {
					t.Fatalf("coverage sheet row %s=%q, want %q", row, got, sheet)
				}
				if got, _ := f.GetCellValue("vsfleetCoverage", "K"+row); got != tc.status {
					t.Fatalf("%s coverage status=%q, want %q", sheet, got, tc.status)
				}
				if got, _ := f.GetCellValue("vsfleetCoverage", "L"+row); got != tc.count {
					t.Fatalf("%s coverage count=%q, want %q", sheet, got, tc.count)
				}
				if got, _ := f.GetCellValue("vsfleetCoverage", "M"+row); got != tc.detail {
					t.Fatalf("%s coverage message=%q, want %q", sheet, got, tc.detail)
				}
			}
			for row, sheet := range map[string]string{"8": "vCD", "9": "vUSB"} {
				if got, _ := f.GetCellValue("vsfleetCoverage", "J"+row); got != sheet {
					t.Fatalf("coverage sheet row %s=%q, want %q", row, got, sheet)
				}
				if got, _ := f.GetCellValue("vsfleetCoverage", "K"+row); got != tc.status {
					t.Fatalf("%s coverage status=%q, want %q", sheet, got, tc.status)
				}
				wantCount := "0"
				if tc.status == "success" {
					wantCount = "1"
				}
				if got, _ := f.GetCellValue("vsfleetCoverage", "L"+row); got != wantCount {
					t.Fatalf("%s coverage count=%q, want %q", sheet, got, wantCount)
				}
				if got, _ := f.GetCellValue("vsfleetCoverage", "M"+row); got != tc.detail {
					t.Fatalf("%s coverage message=%q, want %q", sheet, got, tc.detail)
				}
			}
		})
	}
}

// partitionRun builds a one-context run whose VMs are given verbatim, so a
// test can say exactly which of them reported guest filesystems.
func partitionRun(schema string, vms []assessment.ExportVM) assessment.ExportData {
	return assessment.ExportData{
		Run:      assessment.Run{ID: 9, Status: assessment.RunComplete, InventorySchemaVersion: schema},
		Contexts: []assessment.ContextRun{{Name: "prod", Endpoint: "https://vc.example", VMStatus: "success"}},
		VMs:      vms,
	}
}

func partitionVM(name string, parts ...vsphere.VMPartition) assessment.ExportVM {
	return assessment.ExportVM{Observation: assessment.Observation{Context: "prod", VM: vsphere.VM{ID: name, Name: name, Partitions: parts}}}
}

func TestWriteRVToolsRendersGuestPartitions(t *testing.T) {
	data := partitionRun(assessment.CurrentInventorySchemaVersion, []assessment.ExportVM{
		partitionVM("app", vsphere.VMPartition{Path: "/", DiskKeys: []int32{2000}, CapacityBytes: 8 << 30, FreeBytes: 2 << 30, FilesystemType: "ext4"}),
	})
	var out bytes.Buffer
	if err := WriteRVTools(&out, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// 8 GiB capacity with 2 GiB free is 6 GiB consumed and 25% free.
	for cell, want := range map[string]string{
		"A2": "app", "D2": "2000", "E2": "/", "F2": "8192", "G2": "6144", "H2": "2048", "I2": "25", "J2": "ext4",
	} {
		if got, _ := f.GetCellValue("vPartition", cell); got != want {
			t.Errorf("vPartition!%s=%q, want %q", cell, got, want)
		}
	}
}

// Disk Key is the column that makes vPartition joinable to vDisk, so what it
// renders for each of the three cases Tools can produce is the whole point of
// having it: nothing before vSphere 7.0, one key normally, and several for a
// volume spanning disks.
func TestWriteRVToolsRendersPartitionDiskKeys(t *testing.T) {
	data := partitionRun(assessment.CurrentInventorySchemaVersion, []assessment.ExportVM{
		partitionVM("mapped", vsphere.VMPartition{Path: "/", DiskKeys: []int32{2000}, CapacityBytes: 1 << 30}),
		partitionVM("spanned", vsphere.VMPartition{Path: "/data", DiskKeys: []int32{2000, 2001}, CapacityBytes: 1 << 30}),
		partitionVM("unmapped", vsphere.VMPartition{Path: "/", CapacityBytes: 1 << 30}),
	})
	var out bytes.Buffer
	if err := WriteRVTools(&out, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// A lone key stays a number so a spreadsheet matches it against vDisk's
	// own numeric Disk Key; an estate too old to report the mapping leaves the
	// cell empty rather than claiming disk key zero.
	for cell, want := range map[string]string{"D2": "2000", "D3": "2000, 2001", "D4": ""} {
		if got, _ := f.GetCellValue("vPartition", cell); got != want {
			t.Errorf("vPartition!%s=%q, want %q", cell, got, want)
		}
	}
}

// A capacity vSphere could not size must not be reported as a full disk: the
// column is summed downstream, and 0% free reads as an alarm.
func TestWriteRVToolsHandlesUnsizedPartitions(t *testing.T) {
	data := partitionRun(assessment.CurrentInventorySchemaVersion, []assessment.ExportVM{
		partitionVM("app", vsphere.VMPartition{Path: "/mnt/unsized"}),
		// Tools occasionally reports free space exceeding capacity; consumed
		// must not go negative.
		partitionVM("web", vsphere.VMPartition{Path: "C:\\", CapacityBytes: 1 << 30, FreeBytes: 2 << 30}),
	})
	var out bytes.Buffer
	if err := WriteRVTools(&out, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for cell, want := range map[string]string{"G2": "0", "I2": "0", "G3": "0"} {
		if got, _ := f.GetCellValue("vPartition", cell); got != want {
			t.Errorf("vPartition!%s=%q, want %q", cell, got, want)
		}
	}
}

// The point of the coverage sheet: a short vPartition tab must be
// distinguishable from a small estate. Partitions come from VMware Tools, so
// a fully successful capture can still cover only part of the estate.
func TestWriteRVToolsReportsPartialGuestPartitionCoverage(t *testing.T) {
	withParts := partitionVM("app", vsphere.VMPartition{Path: "/", CapacityBytes: 1 << 30, FreeBytes: 1 << 29})
	noParts := partitionVM("no-tools")

	for _, tc := range []struct {
		name           string
		schema         string
		vms            []assessment.ExportVM
		status, detail string
	}{
		{
			name: "every VM answered", schema: assessment.CurrentInventorySchemaVersion,
			vms: []assessment.ExportVM{withParts}, status: "success", detail: "",
		},
		{
			name: "some VMs had no running Tools", schema: assessment.CurrentInventorySchemaVersion,
			vms: []assessment.ExportVM{withParts, noParts}, status: "partial",
			detail: "1 of 2 VMs reported guest filesystems; the rest had no running VMware Tools",
		},
		{
			name: "no VM answered", schema: assessment.CurrentInventorySchemaVersion,
			vms: []assessment.ExportVM{noParts}, status: "partial",
			detail: "no VM reported guest filesystems; VMware Tools must be running",
		},
		{
			name: "capture predates the tab", schema: "3",
			vms: []assessment.ExportVM{withParts}, status: "not recorded",
			detail: "capture predates guest partition inventory",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			data := partitionRun(tc.schema, tc.vms)
			if err := WriteRVTools(&out, data, healthReport(data)); err != nil {
				t.Fatal(err)
			}
			f, err := excelize.OpenReader(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			// vPartition is the sixth tab, so row 6 of the coverage sheet.
			if got, _ := f.GetCellValue("vsfleetCoverage", "J6"); got != "vPartition" {
				t.Fatalf("coverage row 6=%q, want vPartition", got)
			}
			if got, _ := f.GetCellValue("vsfleetCoverage", "K6"); got != tc.status {
				t.Errorf("status=%q, want %q", got, tc.status)
			}
			if got, _ := f.GetCellValue("vsfleetCoverage", "M6"); got != tc.detail {
				t.Errorf("detail=%q, want %q", got, tc.detail)
			}
		})
	}
}

func TestWriteRVToolsHealthCoverageStates(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	base := sampleExportData(when)
	for _, tc := range []struct {
		name, schema, vmStatus, wantStatus, wantMessage string
	}{
		{name: "successful capture without browse", schema: assessment.CurrentInventorySchemaVersion, vmStatus: "success", wantStatus: "partial", wantMessage: "datastore-zombie-vmdk"},
		{name: "schema gated", schema: "3", vmStatus: "success", wantStatus: "partial", wantMessage: "guest-disk-space-low"},
		{name: "VM collection failure", schema: assessment.CurrentInventorySchemaVersion, vmStatus: "failed", wantStatus: "failed", wantMessage: "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := base
			data.Run.InventorySchemaVersion = tc.schema
			data.Contexts = append([]assessment.ContextRun(nil), base.Contexts...)
			data.Contexts[0].VMStatus = tc.vmStatus
			data.Contexts[0].Error = "permission denied"
			var output bytes.Buffer
			report := healthReport(data)
			if err := WriteRVTools(&output, data, report); err != nil {
				t.Fatal(err)
			}
			f, err := excelize.OpenReader(bytes.NewReader(output.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if got, _ := f.GetCellValue("vsfleetCoverage", "J24"); got != "vHealth" {
				t.Fatalf("health coverage sheet=%q", got)
			}
			if got, _ := f.GetCellValue("vsfleetCoverage", "K24"); got != tc.wantStatus {
				t.Fatalf("health coverage status=%q, want %q", got, tc.wantStatus)
			}
			if got, _ := f.GetCellValue("vsfleetCoverage", "M24"); !strings.Contains(got, tc.wantMessage) {
				t.Fatalf("health coverage message=%q, want substring %q", got, tc.wantMessage)
			}
		})
	}
}

// TestEveryExportedColumnIsDocumented is the guard that keeps the
// compatibility report honest. Profile walks the real exporter output, so a
// column added to a worksheet without a description in compatibility.go fails
// here rather than shipping a report that quietly omits it.
func TestEveryExportedColumnIsDocumented(t *testing.T) {
	profile, err := Profile()
	if err != nil {
		t.Fatalf("building the compatibility profile: %v", err)
	}
	if len(profile) != len(rvtoolsTabOrder) {
		t.Fatalf("profile describes %d worksheets, export renders %d", len(profile), len(rvtoolsTabOrder))
	}
	for i, spec := range profile {
		if spec.Name != rvtoolsTabOrder[i] {
			t.Errorf("worksheet %d is %q, want %q", i, spec.Name, rvtoolsTabOrder[i])
		}
		if spec.DerivesFrom == "" {
			t.Errorf("%s: no collection named as its source", spec.Name)
		}
		if len(spec.Columns) == 0 {
			t.Errorf("%s: no columns described", spec.Name)
		}
		for _, col := range spec.Columns {
			if col.Kind == "" {
				t.Errorf("%s column %q: no cell kind", spec.Name, col.Name)
			}
		}
	}
}

// The profile must describe the columns the exporter actually writes, in the
// order it writes them — that is what lets a reader map a description to a
// spreadsheet column.
func TestProfileMatchesRenderedHeaderRow(t *testing.T) {
	when := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	data := sampleExportData(when)
	var buf bytes.Buffer
	if err := WriteRVTools(&buf, data, healthReport(data)); err != nil {
		t.Fatalf("writing the workbook: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("reopening the workbook: %v", err)
	}
	defer func() { _ = f.Close() }()
	profile, err := Profile()
	if err != nil {
		t.Fatalf("building the compatibility profile: %v", err)
	}
	for _, spec := range profile {
		rows, err := f.GetRows(spec.Name)
		if err != nil {
			t.Fatalf("%s: reading rows: %v", spec.Name, err)
		}
		if len(rows) == 0 {
			t.Fatalf("%s: no header row", spec.Name)
		}
		header := rows[0]
		if len(header) != len(spec.Columns) {
			t.Errorf("%s: workbook has %d columns, profile describes %d", spec.Name, len(header), len(spec.Columns))
			continue
		}
		for i, cell := range header {
			if cell != spec.Columns[i].Name {
				t.Errorf("%s column %d: workbook says %q, profile says %q", spec.Name, i, cell, spec.Columns[i].Name)
			}
		}
	}
}
