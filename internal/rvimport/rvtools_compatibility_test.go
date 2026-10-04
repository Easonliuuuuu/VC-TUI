package rvimport

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"regexp"
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
		"A1": "RVTools major version", "B1": "RVTools version", "C1": "xlsx creation datetime", "D1": "Server",
		"A2": "4.8", "B2": "4.8.1.4", "D2": "192.0.2.10",
	} {
		if err := f.SetCellValue(sheetVMetaData, cell, value); err != nil {
			t.Fatal(err)
		}
	}
	// Reproduce the 4.8.1.4 metadata layout reported in #269. The Excel
	// display format hides seconds and uses a two-digit year.
	if err := f.SetCellValue(sheetVMetaData, "C2", time.Date(2026, 9, 29, 17, 19, 20, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if got, err := f.GetCellValue(sheetVMetaData, "C2"); err != nil || got != "9/29/26 17:19" {
		t.Fatalf("formatted metadata = %q (%v), want 9/29/26 17:19", got, err)
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
	if !result.Report.CapturedAt.Equal(time.Date(2026, 9, 30, 0, 19, 20, 0, time.UTC)) || result.Report.CapturedAtSource != "vMetaData worksheet" {
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

// These are synthetic XLSX cells matching the metadata encodings reported
// in #269, plus Excel's alternate date system and ISO date storage.
func TestMetadataCaptureTimePreservesRawDates(t *testing.T) {
	zone, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		date1904  bool
		keyValue  bool
		dateTyped bool
		iso       bool
		offset    bool
	}{
		{name: "numeric 1900"},
		{name: "numeric 1904", date1904: true},
		{name: "RVTools date-typed serial", dateTyped: true},
		{name: "date-typed serial 1904", dateTyped: true, date1904: true},
		{name: "key-value serial", keyValue: true, dateTyped: true},
		{name: "ISO date", dateTyped: true, iso: true},
		{name: "key-value ISO date", keyValue: true, dateTyped: true, iso: true},
		{name: "ISO date with offset", dateTyped: true, iso: true, offset: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := excelize.NewFile()
			defer f.Close()
			if err := f.SetSheetName("Sheet1", sheetVInfo); err != nil {
				t.Fatal(err)
			}
			if err := f.SetSheetRow(sheetVInfo, "A1", &[]any{"VM", "VM ID", "VI SDK Server"}); err != nil {
				t.Fatal(err)
			}
			if err := f.SetSheetRow(sheetVInfo, "A2", &[]any{"Synthetic VM", "vm-synthetic", "192.0.2.10"}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.NewSheet(sheetVMetaData); err != nil {
				t.Fatal(err)
			}
			if err := f.SetWorkbookProps(&excelize.WorkbookPropsOptions{Date1904: &tc.date1904}); err != nil {
				t.Fatal(err)
			}
			cell := "C2"
			if tc.keyValue {
				if err := f.SetSheetRow(sheetVMetaData, "A1", &[]any{"Property", "Value"}); err != nil {
					t.Fatal(err)
				}
				if err := f.SetCellValue(sheetVMetaData, "A2", "xlsx creation datetime"); err != nil {
					t.Fatal(err)
				}
				cell = "B2"
			} else {
				if err := f.SetSheetRow(sheetVMetaData, "A1", &[]any{"RVTools major version", "RVTools version", "xlsx creation datetime", "Server"}); err != nil {
					t.Fatal(err)
				}
				if err := f.SetSheetRow(sheetVMetaData, "A2", &[]any{"4.8", "4.8.1.4", "", "192.0.2.10"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.SetCellValue(sheetVMetaData, cell, time.Date(2026, 9, 29, 17, 19, 20, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			raw, err := f.GetCellValue(sheetVMetaData, cell, excelize.Options{RawCellValue: true})
			if err != nil {
				t.Fatal(err)
			}
			if tc.iso {
				raw = "2026-09-29T17:19:20"
			}
			if tc.offset {
				raw += "-07:00"
			}
			if tc.dateTyped {
				style, err := f.GetCellStyle(sheetVMetaData, cell)
				if err != nil {
					t.Fatal(err)
				}
				f = withMetadataDateCell(t, f, cell, style, raw)
				if typ, err := f.GetCellType(sheetVMetaData, cell); err != nil || typ != excelize.CellTypeDate {
					t.Fatalf("metadata cell type = %v (%v), want date", typ, err)
				}
			}
			result, err := Parse(f, Options{Timezone: zone})
			if err != nil {
				t.Fatal(err)
			}
			want := time.Date(2026, 9, 30, 0, 19, 20, 0, time.UTC)
			if !result.Report.CapturedAt.Equal(want) || result.Report.CapturedAtSource != "vMetaData worksheet" {
				t.Fatalf("captured at = %v (%s), want %v from vMetaData", result.Report.CapturedAt, result.Report.CapturedAtSource, want)
			}
			withoutZone, err := Parse(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.offset {
				if !withoutZone.Report.CapturedAt.Equal(want) || withoutZone.Report.CapturedAtSource != "vMetaData worksheet" {
					t.Fatalf("explicit timestamp offset was not honored: %+v", withoutZone.Report)
				}
			} else if withoutZone.Report.CapturedAtSource == "vMetaData worksheet" || !containsSubstring(withoutZone.Report.Warnings, "pass --timezone") {
				t.Fatalf("timezone-free metadata was guessed or not explained: %+v", withoutZone.Report)
			}
		})
	}
}

// excelize writes time.Time as a numeric cell. Rewrite the serialized cell
// to t="d" to exercise the exact date cell type reported by RVTools.
func withMetadataDateCell(t *testing.T, f *excelize.File, cell string, style int, raw string) *excelize.File {
	t.Helper()
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for _, part := range reader.File {
		r, err := part.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if part.Name == "xl/worksheets/sheet2.xml" {
			pattern := regexp.MustCompile(`<c r="` + cell + `"[^>]*>.*?</c>`)
			if !pattern.Match(body) {
				t.Fatalf("metadata XML has no %s cell", cell)
			}
			body = pattern.ReplaceAll(body, []byte(fmt.Sprintf(`<c r="%s" s="%d" t="d"><v>%s</v></c>`, cell, style, raw)))
		}
		w, err := writer.Create(part.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := excelize.OpenReader(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Close() })
	return result
}

func TestParseVMKernelPortGroupHeaderSpellings(t *testing.T) {
	for _, sheet := range []string{"vSC_VMK", sheetVSCVMK} {
		for _, header := range []string{"Port Group", "Port group"} {
			t.Run(sheet+"/"+header, func(t *testing.T) {
				path := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
					editHost(t, data, "host-a1", func(h *vsphere.Host) {
						h.VMKs = []vsphere.HostVMKernel{{Device: "vmk0", PortGroup: "Synthetic Management Network"}}
					})
				})
				f := openFixture(t, path)
				setHeader(t, f, "vSC_VMK", []string{"Port Group"}, header)
				if sheet != "vSC_VMK" {
					if err := f.SetSheetName("vSC_VMK", sheet); err != nil {
						t.Fatal(err)
					}
				}
				result, err := Parse(f, Options{})
				if err != nil {
					t.Fatal(err)
				}
				for _, c := range result.contexts {
					for _, h := range c.hosts {
						if c.name == "alpha" && h.ID == "host-a1" {
							if len(h.VMKs) != 1 || h.VMKs[0].Device != "vmk0" || h.VMKs[0].PortGroup != "Synthetic Management Network" {
								t.Fatalf("VMkernel adapter was not preserved: %+v", h.VMKs)
							}
							return
						}
					}
				}
				t.Fatal("synthetic host was not imported")
			})
		}
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
