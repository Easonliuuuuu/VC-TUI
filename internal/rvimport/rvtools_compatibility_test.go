package rvimport

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// This fixture is synthesized from header spellings observed in the #206
// report. It is not a real RVTools workbook and does not claim real-workbook
// validation.
func TestSyntheticRVTools48HeadersAndMetadata(t *testing.T) {
	path := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		data.VMs[0].Observation.VM.Disks[0].SharedBus = "physicalSharing"
		data.VMs[0].Observation.VM.CDROMs = []vsphere.VMCDROM{{
			Key: 300, Label: "CD/DVD drive 1", BackingType: "iso", BackingPath: "[nvme-01] install.iso",
		}}
		data.VMs[0].Observation.VM.USBs = []vsphere.VMUSB{{
			Key: 400, Label: "USB device 1", Vendor: 0x1234, Product: 0x5678, Family: []string{"storage"}, Speed: []string{"high"},
		}}
		data.VMs[0].Observation.VM.Snapshots = []vsphere.VMSnapshot{{
			Name: "synthetic snapshot", CreateTime: time.Date(2026, 9, 28, 16, 29, 35, 0, time.UTC),
		}}
		data.VMs[0].Snapshots = []vsphere.VMSnapshot{{
			Name: "synthetic snapshot", CreateTime: time.Date(2026, 9, 28, 16, 29, 35, 0, time.UTC),
		}}
		enabled, disabled := true, false
		editHost(t, data, "host-a1", func(h *vsphere.Host) {
			h.Name = "esx-shared"
			h.HBAs = []vsphere.HostHBA{{Device: "vmhba2", Bus: 1, Status: "online", Model: "Synthetic Fibre Channel", Type: "Block SCSI"}}
			h.NICs = []vsphere.HostNIC{{Device: "vmnic2", MAC: "00:50:56:aa:bb:02", Switch: "vSwitch0"}}
			h.VSwitches = []vsphere.HostVSwitch{{Name: "vSwitch0", FreePorts: 12, MTU: 1500, Promiscuous: &enabled, MACChanges: &disabled, ForgedTransmits: &enabled, TrafficShaping: &disabled}}
			h.PortGroups = []vsphere.HostPortGroup{{Name: "Management Network", Switch: "vSwitch0", VLAN: 20, Promiscuous: &enabled, MACChanges: &disabled, ForgedTransmits: &enabled}}
			h.VMKs = []vsphere.HostVMKernel{{Device: "vmk0", PortGroup: "Management Network", IP: "192.0.2.10", MTU: 1500}}
			h.Multipaths = []vsphere.HostMultipath{{LUN: "naa.synthetic", PathCount: 2, Active: 1, Standby: 1, WorkingPaths: 2}}
		})
		editHost(t, data, "host-b1", func(h *vsphere.Host) { h.Name = "esx-shared" })
		failback := true
		data.Resources = append(data.Resources, dvsResource(t, "alpha", "vc-alpha-uuid", vsphere.DVSwitch{
			Location: vsphere.Location{Context: "alpha", Datacenter: "dc-a"},
			ID:       "dvs-alpha", Name: "DVS-1", MaxPorts: 4096,
			PortGroups: []vsphere.DVPortGroup{{ID: "dvpg-alpha", Key: "dvportgroup-1", Name: "frontend", Switch: "DVS-1", Promiscuous: &enabled, MACChanges: &disabled, ForgedTransmits: &enabled, TeamingPolicy: "loadbalance_srcid", NotifySwitches: &enabled, Failback: &failback, IngressShaping: &disabled, EgressShaping: &enabled, ActiveUplinks: []string{"vmnic0"}, StandbyUplinks: []string{"vmnic1"}}},
		}))
	})
	f := openFixture(t, path)
	scVMKSheet := firstExistingSheet(f, sheetVSCVMK, "vSC_VMK")
	if scVMKSheet == "" {
		t.Fatal("synthetic workbook has neither vSC+VMK nor vSC_VMK")
	}
	setHeader(t, f, sheetVDisk, []string{"SharedBus", "Shared Bus"}, "Shared Bus")
	setHeader(t, f, sheetDVSwitch, []string{"DVS", "Switch"}, "Switch")
	setHeader(t, f, sheetDVSwitch, []string{"# Max ports", "Max Ports"}, "Max Ports")
	setHeader(t, f, sheetDVPort, []string{"Port group", "Port"}, "Port")
	setHeader(t, f, sheetDVPort, []string{"DVS", "Switch"}, "Switch")
	setHeader(t, f, sheetDVPort, []string{"Failback", "Rolling Order"}, "Rolling Order")
	// RVTools stores the opposite of the internal Failback value. Keep the
	// source row coherent with its new header and preserve the round-trip value.
	setColumnValue(t, f, sheetDVPort, "Rolling Order", 2, "false")
	setHeader(t, f, sheetDVPort, []string{"Active uplinks", "Active Uplink"}, "Active Uplink")
	setHeader(t, f, sheetDVPort, []string{"Standby uplinks", "Standby Uplink"}, "Standby Uplink")
	setHeader(t, f, sheetDVPort, []string{"Notify switches", "Notify Switch"}, "Notify Switch")
	setHeader(t, f, sheetDVPort, []string{"Promiscuous mode", "Allow Promiscuous"}, "Allow Promiscuous")
	setHeader(t, f, sheetDVPort, []string{"MAC changes", "Mac Changes"}, "Mac Changes")
	setHeader(t, f, sheetDVPort, []string{"Forged transmits", "Forged Transmits"}, "Forged Transmits")
	setHeader(t, f, sheetDVPort, []string{"Teaming policy", "Policy"}, "Policy")
	setHeader(t, f, sheetDVPort, []string{"Ingress shaping", "In Traffic Shaping"}, "In Traffic Shaping")
	setHeader(t, f, sheetDVPort, []string{"Egress shaping", "Out Traffic Shaping"}, "Out Traffic Shaping")
	setHeader(t, f, sheetVSwitch, []string{"Free ports", "Free Ports"}, "Free Ports")
	setHeader(t, f, sheetVSwitch, []string{"Promiscuous mode", "Promiscuous Mode"}, "Promiscuous Mode")
	setHeader(t, f, sheetVSwitch, []string{"MAC changes", "Mac Changes"}, "Mac Changes")
	setHeader(t, f, sheetVSwitch, []string{"Forged transmits", "Forged Transmits"}, "Forged Transmits")
	setHeader(t, f, sheetVSwitch, []string{"Traffic shaping", "Traffic Shaping"}, "Traffic Shaping")
	setHeader(t, f, sheetVPort, []string{"Port group", "Port Group"}, "Port Group")
	setHeader(t, f, sheetVCluster, []string{"NumEffectiveHosts", "numeffectivehosts"}, "numeffectivehosts")
	setColumnValue(t, f, sheetVSnapshot, "Date / time", 2, "9/28/2026 9:29:35 AM")
	for _, sheet := range []string{sheetVHBA, sheetVNIC, sheetVSwitch, sheetVPort, scVMKSheet, sheetVMultiPath} {
		clearColumn(t, f, sheet, "Object ID") // observed RVTools host sheets identify the host by name
	}
	if scVMKSheet == sheetVSCVMK {
		if err := f.SetSheetName(sheetVSCVMK, "vSC_VMK"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.NewSheet(sheetVMetaData); err != nil {
		t.Fatal(err)
	}
	for cell, value := range map[string]string{
		"A1": "RVTools Version", "B1": "Creation Date/Time", "C1": "Server",
		"A2": "4.8.1.4", "B2": "9/28/2026 11:12:23 AM", "C2": "vc-alpha.example",
	} {
		if err := f.SetCellValue(sheetVMetaData, cell, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.SetDocProps(&excelize.DocProperties{Created: "2020-01-02T03:04:05Z"}); err != nil {
		t.Fatal(err)
	}

	zone, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Parse(f, Options{Timezone: zone})
	if err != nil {
		t.Fatalf("Parse synthetic RVTools 4.8 headers: %v", err)
	}
	if !result.Report.CapturedAt.Equal(time.Date(2026, 9, 28, 18, 12, 23, 0, time.UTC)) || result.Report.CapturedAtSource != "vMetaData worksheet" {
		t.Errorf("captured at = %v (%s), want the zoned vMetaData time", result.Report.CapturedAt, result.Report.CapturedAtSource)
	}
	withoutZone, err := Parse(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if withoutZone.Report.CapturedAtSource != "workbook document properties" || !containsSubstring(withoutZone.Report.Warnings, "pass --timezone") {
		t.Errorf("timezone-free metadata was guessed or not explained: source=%q warnings=%v", withoutZone.Report.CapturedAtSource, withoutZone.Report.Warnings)
	}
	if !contains(result.Report.RecognizedSheets, "vSC_VMK") || !contains(result.Report.RecognizedSheets, sheetVMetaData) {
		t.Errorf("recognized sheets = %v, want vSC_VMK and vMetaData", result.Report.RecognizedSheets)
	}
	vm := findVM(result, "alpha", "web-01")
	if vm == nil {
		t.Fatal("synthetic vInfo VM was not imported")
	}
	if len(vm.Disks) != 1 || vm.Disks[0].SharedBus != "physicalSharing" {
		t.Errorf("vDisk Shared Bus alias was not read: %+v", vm.Disks)
	}
	if len(vm.CDROMs) != 1 || vm.CDROMs[0].BackingPath != "[nvme-01] install.iso" {
		t.Errorf("vCD row was not imported: %+v", vm.CDROMs)
	}
	if len(vm.USBs) != 1 || vm.USBs[0].Vendor != 0x1234 || vm.USBs[0].Product != 0x5678 {
		t.Errorf("vUSB row was not imported: %+v", vm.USBs)
	}
	if len(vm.Snapshots) != 1 || !vm.Snapshots[0].CreateTime.Equal(time.Date(2026, 9, 28, 16, 29, 35, 0, time.UTC)) {
		t.Errorf("vSnapshot was not interpreted in the selected timezone: %+v", vm.Snapshots)
	}
	var host *vsphere.Host
	for _, c := range result.contexts {
		if c.name != "alpha" {
			continue
		}
		for _, candidate := range c.hosts {
			if candidate.ID == "host-a1" {
				host = candidate
			}
		}
	}
	if host == nil || len(host.HBAs) != 1 || len(host.NICs) != 1 || len(host.VSwitches) != 1 || len(host.PortGroups) != 1 || len(host.VMKs) != 1 || len(host.Multipaths) != 1 {
		t.Fatalf("host configuration sheets were not joined by same-workbook name: %+v", host)
	}
	if host.VSwitches[0].FreePorts != 12 || host.VSwitches[0].Promiscuous == nil || !*host.VSwitches[0].Promiscuous || host.PortGroups[0].ForgedTransmits == nil || !*host.PortGroups[0].ForgedTransmits {
		t.Errorf("RVTools standard-switch headers were not mapped: switches=%+v ports=%+v", host.VSwitches, host.PortGroups)
	}
	if len(result.contexts[1].hosts) == 0 || len(result.contexts[1].hosts[0].HBAs) != 0 {
		t.Error("same-named host from another vCenter received alpha's HBA row")
	}
	if len(result.contexts[0].dvswitches) != 1 || result.contexts[0].dvswitches[0].Name != "DVS-1" || result.contexts[0].dvswitches[0].MaxPorts != 4096 || len(result.contexts[0].dvswitches[0].PortGroups) != 1 {
		t.Fatalf("Switch/Port aliases did not join the distributed port group: contexts=%+v report=%+v", result.contexts, result.Report)
	}
	port := result.contexts[0].dvswitches[0].PortGroups[0]
	if port.Failback == nil || !*port.Failback {
		t.Errorf("Rolling Order was not inverted into Failback: %+v", result.contexts[0].dvswitches[0].PortGroups[0])
	}
	if port.Promiscuous == nil || !*port.Promiscuous || port.MACChanges == nil || *port.MACChanges || port.ForgedTransmits == nil || !*port.ForgedTransmits || port.TeamingPolicy != "loadbalance_srcid" || port.IngressShaping == nil || *port.IngressShaping || port.EgressShaping == nil || !*port.EgressShaping || port.NotifySwitches == nil || !*port.NotifySwitches || len(port.ActiveUplinks) != 1 || len(port.StandbyUplinks) != 1 {
		t.Errorf("RVTools dvPort aliases were not mapped: %+v", port)
	}

	explicit := time.Date(2025, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3600))
	withOverride, err := Parse(f, Options{CapturedAt: explicit, Timezone: zone})
	if err != nil {
		t.Fatal(err)
	}
	if !withOverride.Report.CapturedAt.Equal(explicit.UTC()) || withOverride.Report.CapturedAtSource != "explicit --captured-at" {
		t.Errorf("explicit capture override = %v (%s), want %v", withOverride.Report.CapturedAt, withOverride.Report.CapturedAtSource, explicit.UTC())
	}

	// Keep the historical vsfleet worksheet spelling importable too.
	if err := f.SetSheetName("vSC_VMK", sheetVSCVMK); err != nil {
		t.Fatal(err)
	}
	legacy, err := Parse(f, Options{Timezone: zone})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(legacy.Report.RecognizedSheets, sheetVSCVMK) {
		t.Errorf("legacy %s sheet not recognized: %v", sheetVSCVMK, legacy.Report.RecognizedSheets)
	}
}

func setHeader(t *testing.T, f *excelize.File, sheet string, candidates []string, replacement string) {
	t.Helper()
	rows, err := f.GetRows(sheet)
	if err != nil || len(rows) == 0 {
		t.Fatalf("read %s: %v", sheet, err)
	}
	for i, header := range rows[0] {
		for _, candidate := range candidates {
			if strings.EqualFold(header, candidate) {
				column, err := excelize.ColumnNumberToName(i + 1)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.SetCellValue(sheet, column+"1", replacement); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
	}
	t.Fatalf("%s has none of the expected headers %v", sheet, candidates)
}

func setColumnValue(t *testing.T, f *excelize.File, sheet, header string, row int, value string) {
	t.Helper()
	rows, err := f.GetRows(sheet)
	if err != nil || len(rows) == 0 {
		t.Fatalf("read %s: %v", sheet, err)
	}
	for columnIndex, current := range rows[0] {
		if !strings.EqualFold(current, header) {
			continue
		}
		column, err := excelize.ColumnNumberToName(columnIndex + 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.SetCellValue(sheet, column+strconv.Itoa(row), value); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("%s has no %q column", sheet, header)
}

func clearColumn(t *testing.T, f *excelize.File, sheet, header string) {
	t.Helper()
	rows, err := f.GetRows(sheet)
	if err != nil || len(rows) == 0 {
		t.Fatalf("read %s: %v", sheet, err)
	}
	for columnIndex, value := range rows[0] {
		if value != header {
			continue
		}
		column, err := excelize.ColumnNumberToName(columnIndex + 1)
		if err != nil {
			t.Fatal(err)
		}
		for rowIndex := 1; rowIndex < len(rows); rowIndex++ {
			if err := f.SetCellValue(sheet, column+strconv.Itoa(rowIndex+1), ""); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	// RVTools can omit Object ID entirely. That already represents the
	// host-name-only case this synthetic fixture is intended to exercise.
}

func firstExistingSheet(f *excelize.File, candidates ...string) string {
	available := make(map[string]bool)
	for _, name := range f.GetSheetList() {
		available[name] = true
	}
	for _, candidate := range candidates {
		if available[candidate] {
			return candidate
		}
	}
	return ""
}

func containsSubstring(values []string, substring string) bool {
	for _, value := range values {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
}
