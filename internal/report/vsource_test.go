package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
)

var rvtools482SourceHeaders = []string{"Name", "OS type", "API type", "API version", "Version", "Patch level", "Build", "Fullname", "Product name", "Product version", "Product line", "Vendor", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}

// vsourceData is a synthetic multi-vCenter run: one vCenter, one standalone
// ESXi host, and one context that never connected.
func vsourceData() assessment.ExportData {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	esxi := &assessment.SourceInfo{Name: "VMware ESXi", FullName: "VMware ESXi 7.0.3 build-2", Vendor: "VMware, Inc.", Version: "7.0.3", Build: "2", OSType: "vmnix-x86", ProductLineID: "embeddedEsx", APIType: "HostAgent", APIVersion: "7.0.3.0", InstanceUUID: "esxi-uuid", LicenseProductName: "VMware ESX Server", LicenseProductVersion: "7.0"}
	return assessment.ExportData{
		Run: assessment.Run{ID: 9, StartedAt: when, FinishedAt: when.Add(time.Minute), Status: assessment.RunPartial, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion},
		Contexts: []assessment.ContextRun{
			{Name: "vc-b", Endpoint: "https://vc-b.example", VCenterID: "vc-b-uuid", VMStatus: "success", Source: sampleSource("vpx", "VMware vCenter Server", "8.0.3")},
			{Name: "esxi-c", Endpoint: "https://esxi-c.example", VCenterID: "esxi-uuid", VMStatus: "empty", Source: esxi},
			{Name: "down", Endpoint: "https://down.example", VMStatus: "failed", Error: "dial tcp: connection refused"},
		},
	}
}

func TestVSourceRowsAreAttributedPerContextAndOmitFailedContexts(t *testing.T) {
	data := vsourceData()
	sheets, err := rvtoolsSheets(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	var source *sheet
	for i := range sheets {
		if sheets[i].name == "vSource" {
			source = &sheets[i]
		}
	}
	if source == nil {
		t.Fatal("no vSource sheet")
	}
	if strings.Join(source.headers, "|") != strings.Join(rvtools482SourceHeaders, "|") {
		t.Fatalf("vSource headers=%v, want the RVTools 4.8.2 columns plus vsfleet Context", source.headers)
	}
	if len(source.rows) != 2 {
		t.Fatalf("vSource rows=%v, want one per successfully captured context", source.rows)
	}
	// Contexts render in name order: esxi-c, then vc-b.
	want := [][]any{
		{"VMware ESXi", "vmnix-x86", "HostAgent", "7.0.3.0", "7.0.3", "", "2", "VMware ESXi 7.0.3 build-2", "VMware ESX Server", "7.0", "embeddedEsx", "VMware, Inc.", "https://esxi-c.example", "esxi-uuid", "esxi-c"},
		{"VMware vCenter Server", "linux-x64", "VirtualCenter", "8.0.3.0", "8.0.3", "00400", "1000001", "VMware vCenter Server 8.0.3 build-1000001", "VMware VirtualCenter Server", "8.0", "vpx", "Example Vendor", "https://vc-b.example", "vc-b-uuid", "vc-b"},
	}
	for i := range want {
		for j := range want[i] {
			if source.rows[i][j] != want[i][j] {
				t.Errorf("row %d column %q=%v, want %v", i, source.headers[j], source.rows[i][j], want[i][j])
			}
		}
	}
}

func TestVSourceCoverageExplainsGaps(t *testing.T) {
	data := vsourceData()
	rows := coverageRows(canonicalData(data), healthReport(data))
	got := map[string][]any{}
	for _, row := range rows {
		if row[9] == "vSource" {
			got[row[5].(string)] = row
		}
	}
	for name, want := range map[string]struct {
		status  string
		count   int
		message string
	}{
		"vc-b":   {"success", 1, ""},
		"esxi-c": {"success", 1, ""},
		"down":   {"failed", 0, "no ServiceInstance About record was stored: dial tcp: connection refused"},
	} {
		row := got[name]
		if row == nil || row[10] != want.status || row[11] != want.count || row[12] != want.message {
			t.Errorf("%s coverage=%v, want %+v", name, row, want)
		}
	}

	// A run captured before schema 17 cannot have a source, however healthy the
	// context was, and a schema 17 context that stored none says so.
	old := vsourceData()
	old.Run.InventorySchemaVersion = "16"
	for i := range old.Contexts {
		old.Contexts[i].Source = nil
	}
	for _, row := range coverageRows(canonicalData(old), healthReport(old)) {
		if row[9] != "vSource" {
			continue
		}
		if row[10] != "not recorded" || row[11] != 0 || row[12] != "capture predates source identity inventory; no ServiceInstance About record was stored" {
			t.Errorf("old-run vSource coverage=%v", row)
		}
	}
	missing := vsourceData()
	missing.Contexts[0].Source = nil
	for _, row := range coverageRows(canonicalData(missing), healthReport(missing)) {
		if row[9] == "vSource" && row[5] == "vc-b" && (row[10] != "not recorded" || row[11] != 0 || row[12] != "no ServiceInstance About record was stored for this context") {
			t.Errorf("missing-source coverage=%v", row)
		}
	}
}

func TestVSourceOldRunHasNoRowsAndNoFabricatedValues(t *testing.T) {
	data := vsourceData()
	data.Run.InventorySchemaVersion = "16"
	for i := range data.Contexts {
		data.Contexts[i].Source = nil
	}
	files, err := RVToolsCSV(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name == "vSource.csv" {
			if lines := strings.Count(strings.TrimSpace(string(f.Data)), "\n"); lines != 0 {
				t.Fatalf("old run vSource.csv has data rows:\n%s", f.Data)
			}
			return
		}
	}
	t.Fatal("vSource.csv not written")
}

func TestVSourceExportsAreByteIdentical(t *testing.T) {
	data := vsourceData()
	var first, second bytes.Buffer
	if err := WriteRVTools(&first, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	if err := WriteRVTools(&second, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("repeated XLSX exports differ")
	}
	f, err := excelize.OpenReader(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got, _ := f.GetCellValue("vSource", "O3"); got != "vc-b" {
		t.Fatalf("vSource O3=%q, want vc-b", got)
	}
	// Patch level and Build are text in RVTools (a patch level of 00400 keeps
	// its leading zeros), so they must be string cells, never numbers.
	for cell, want := range map[string]string{"F3": "00400", "G3": "1000001"} {
		if got, _ := f.GetCellValue("vSource", cell); got != want {
			t.Fatalf("vSource %s=%q, want %q", cell, got, want)
		}
		if typ, _ := f.GetCellType("vSource", cell); typ != excelize.CellTypeSharedString && typ != excelize.CellTypeInlineString {
			t.Fatalf("vSource %s is cell type %v, want a string", cell, typ)
		}
	}
	a, err := RVToolsCSV(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	b, err := RVToolsCSV(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	for i := range a {
		if !bytes.Equal(a[i].Data, b[i].Data) {
			t.Fatalf("%s differs between renders", a[i].Name)
		}
	}
}
