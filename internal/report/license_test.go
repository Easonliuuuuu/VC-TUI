package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// licensedTabOrder is the tab order of an export whose run collected licenses.
// RVTools 4.8 places vLicense directly after vMultiPath.
var licensedTabOrder = []string{"vInfo", "vCPU", "vMemory", "vDisk", "vPartition", "vNetwork", "vCD", "vUSB", "vSnapshot", "vTools", "vSource", "vRP", "vCluster", "vHost", "vHBA", "vNIC", "vSwitch", "vPort", "dvSwitch", "dvPort", "vSC_VMK", "vDatastore", "vMultiPath", "vLicense", "vsfleetLicenseAssignment", "vHealth", "vsfleetCoverage", "vsfleetPerformance"}

// syntheticLicenses are visibly synthetic records. They have no key field, by
// construction: vsphere.License cannot carry one.
func syntheticLicenses(when time.Time) []vsphere.License {
	expires := when.AddDate(1, 0, 0)
	return []vsphere.License{
		{ID: "license-001", Name: "Synthetic vSphere Standard (example)", EditionKey: "esx.standard.cpuPackage", CostUnit: "cpuPackage", Total: 16, Used: 4, Expiration: &expires, Features: []string{"Distributed Switch", "vMotion"},
			Assignments: []vsphere.LicenseAssignment{
				{EntityID: "host-1", EntityName: "esx-1", EntityType: "host", Scope: "vc-uuid"},
				{EntityID: "host-2", EntityName: "esx-2", EntityType: "host", Scope: "vc-uuid"},
			}},
		{ID: "license-002", Name: "Synthetic Evaluation Mode (example)", EditionKey: "eval", Total: 0, Used: 0, Features: []string{"vMotion"}},
	}
}

func withSyntheticLicenses(data assessment.ExportData) assessment.ExportData {
	when := data.Run.StartedAt
	data.Contexts = append([]assessment.ContextRun(nil), data.Contexts...)
	data.Contexts[0].Collections = append(append([]assessment.CollectionRun(nil), data.Contexts[0].Collections...), assessment.CollectionRun{Kind: assessment.LicenseKind, Status: "success", ItemCount: 2})
	data.Resources = append([]assessment.ResourceObservation(nil), data.Resources...)
	for _, l := range syntheticLicenses(when) {
		payload, _ := json.Marshal(l)
		data.Resources = append(data.Resources, assessment.ResourceObservation{Context: data.Contexts[0].Name, VCenterID: data.Contexts[0].VCenterID, Kind: assessment.LicenseKind, ID: l.ID, Name: l.Name, Payload: payload})
	}
	return data
}

func withLicenseCollection(data assessment.ExportData, status, message string) assessment.ExportData {
	data.Contexts = append([]assessment.ContextRun(nil), data.Contexts...)
	data.Contexts[0].Collections = append(append([]assessment.CollectionRun(nil), data.Contexts[0].Collections...), assessment.CollectionRun{Kind: assessment.LicenseKind, Status: status, Error: message})
	return data
}

func sheetNames(t *testing.T, data assessment.ExportData) []string {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteRVTools(&buf, data, healthReport(data)); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	return f.GetSheetList()
}

func coverageFor(t *testing.T, data assessment.ExportData, sheet string) (status, message string, count string, found bool) {
	t.Helper()
	for _, row := range coverageRows(data, healthReport(data)) {
		if row[9] == sheet {
			return csvCell(row[10]), csvCell(row[12]), csvCell(row[11]), true
		}
	}
	return "", "", "", false
}

func TestDefaultExportHasNoLicenseSheetOrCoverageRow(t *testing.T) {
	data := sampleExportData(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	got := sheetNames(t, data)
	if strings.Join(got, ",") != strings.Join(rvtoolsTabOrder, ",") {
		t.Fatalf("default sheets=%v, want %v", got, rvtoolsTabOrder)
	}
	for _, sheet := range []string{licenseSheetName, licenseAssignmentSheetName} {
		if _, _, _, found := coverageFor(t, data, sheet); found {
			t.Fatalf("default export has a coverage row for %s; a run that did not collect licenses must be silent, not a zero-license claim", sheet)
		}
	}
	files, err := RVToolsCSV(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name, "vLicense") {
			t.Fatalf("default CSV export contains %s", f.Name)
		}
	}
}

func TestLicenseExportReportsProductUsageAndExpiration(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	data := withSyntheticLicenses(sampleExportData(when))
	var buf bytes.Buffer
	if err := WriteRVTools(&buf, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := f.GetSheetList(); strings.Join(got, ",") != strings.Join(licensedTabOrder, ",") {
		t.Fatalf("sheets=%v", got)
	}
	rows, err := f.GetRows("vLicense")
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := []string{"Name", "Key", "Labels", "Cost Unit", "Total", "Used", "Expiration Date", "Features", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	if strings.Join(rows[0], "|") != strings.Join(wantHeader, "|") {
		t.Fatalf("vLicense header=%v, want the RVTools 4.8 layout %v", rows[0], wantHeader)
	}
	if len(rows) != 3 {
		t.Fatalf("vLicense rows=%d, want header plus 2 licenses", len(rows))
	}
	// Rows sort by name: Evaluation before Standard.
	eval, std := rows[1], rows[2]
	if eval[0] != "Synthetic Evaluation Mode (example)" || std[0] != "Synthetic vSphere Standard (example)" {
		t.Fatalf("license order: %q, %q", eval[0], std[0])
	}
	if std[3] != "cpuPackage" || std[4] != "16" || std[5] != "4" || std[7] != "Distributed Switch; vMotion" {
		t.Fatalf("standard row=%v", std)
	}
	if std[8] != "https://vc.example" || std[9] != "vc-uuid" || std[10] != "prod" {
		t.Fatalf("provenance columns=%v", std[8:])
	}
	for _, row := range rows[1:] {
		if row[1] != vsphere.LicenseRedacted {
			t.Fatalf("Key cell=%q, want the fixed redaction marker", row[1])
		}
		if row[2] != "" {
			t.Fatalf("Labels cell=%q, want empty (not collected)", row[2])
		}
	}
	// Cell types: Total and Used are numbers, Expiration Date is a date, and a
	// license with no expiration property leaves the cell empty.
	for _, cell := range []string{"E3", "F3"} {
		if typ, _ := f.GetCellType("vLicense", cell); typ != excelize.CellTypeNumber && typ != excelize.CellTypeUnset {
			t.Errorf("%s type=%v, want numeric", cell, typ)
		}
	}
	raw, _ := f.GetCellValue("vLicense", "G3", excelize.Options{RawCellValue: true})
	if raw == "" {
		t.Fatalf("expiration cell empty for a license with an expiration")
	}
	if got, _ := f.GetCellValue("vLicense", "G2"); got != "" {
		t.Fatalf("license with no expiration has Expiration Date %q, want empty", got)
	}
	// Assignment detail.
	assign, err := f.GetRows("vsfleetLicenseAssignment")
	if err != nil {
		t.Fatal(err)
	}
	if len(assign) != 3 || assign[1][2] != "esx-1" || assign[2][2] != "esx-2" || assign[1][3] != "host" || assign[1][0] != "Synthetic vSphere Standard (example)" {
		t.Fatalf("assignments=%v", assign)
	}
	// Coverage.
	status, _, count, found := coverageFor(t, data, licenseSheetName)
	if !found || status != "success" || count != "2" {
		t.Fatalf("vLicense coverage status=%q count=%q found=%v", status, count, found)
	}
	status, _, count, found = coverageFor(t, data, licenseAssignmentSheetName)
	if !found || status != "success" || count != "2" {
		t.Fatalf("assignment coverage status=%q count=%q found=%v", status, count, found)
	}
}

// The central acceptance case: an account without the license privilege must
// never yield an export that looks like a complete, license-free estate.
func TestUnavailableLicensesAreExplicitInCoverage(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	denied := "permission denied reading license list: the account lacks the Global.Licenses privilege"
	data := withLicenseCollection(sampleExportData(when), vsphere.LicenseStatusUnavailable, denied)
	got := sheetNames(t, data)
	if strings.Join(got, ",") != strings.Join(licensedTabOrder, ",") {
		t.Fatalf("a run that asked for licenses must still have the license sheets, got %v", got)
	}
	if rows := licenseRows(data); len(rows) != 0 {
		t.Fatalf("unavailable collection produced %d rows", len(rows))
	}
	for _, sheet := range []string{licenseSheetName, licenseAssignmentSheetName} {
		status, message, count, found := coverageFor(t, data, sheet)
		if !found || status != "unavailable" || count != "0" || !strings.Contains(message, "Global.Licenses") {
			t.Fatalf("%s coverage status=%q count=%q message=%q found=%v", sheet, status, count, message, found)
		}
	}
}

func TestPartialLicenseCollectionMarksAssignmentsUnavailable(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	data := withSyntheticLicenses(sampleExportData(when))
	for i := range data.Contexts[0].Collections {
		if data.Contexts[0].Collections[i].Kind == assessment.LicenseKind {
			data.Contexts[0].Collections[i].Status = vsphere.LicenseStatusPartial
			data.Contexts[0].Collections[i].Error = "unsupported: this vSphere version does not provide license assignments"
		}
	}
	status, message, _, _ := coverageFor(t, data, licenseSheetName)
	if status != "success" || !strings.Contains(message, "assignments were not readable") {
		t.Fatalf("vLicense status=%q message=%q", status, message)
	}
	status, message, _, _ = coverageFor(t, data, licenseAssignmentSheetName)
	if status != "unavailable" || !strings.Contains(message, "unsupported") {
		t.Fatalf("assignment status=%q message=%q", status, message)
	}
}

func TestLicenseExportIsDeterministicAndKeyFree(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	data := withSyntheticLicenses(sampleExportData(when))
	var first, second bytes.Buffer
	if err := WriteRVTools(&first, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	if err := WriteRVTools(&second, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("re-exporting an unchanged licensed run is not byte-identical")
	}
	csvA, err := RVToolsCSV(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	csvB, err := RVToolsCSV(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	var vLicenseCSV string
	for i := range csvA {
		if !bytes.Equal(csvA[i].Data, csvB[i].Data) {
			t.Fatalf("%s CSV differs between renders", csvA[i].Name)
		}
		if csvA[i].Name == "vLicense.csv" {
			vLicenseCSV = string(csvA[i].Data)
		}
	}
	if !strings.Contains(vLicenseCSV, "2027-01-02T03:04:05Z") || !strings.Contains(vLicenseCSV, vsphere.LicenseRedacted) {
		t.Fatalf("vLicense.csv=%q", vLicenseCSV)
	}
}

func TestLicenseCoverageAppearsForEveryContext(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	data := withSyntheticLicenses(sampleExportData(when))
	// A second context whose capture never reached the license collection.
	data.Contexts = append(data.Contexts, assessment.ContextRun{Name: "dr", Endpoint: "https://dr.example", VCenterID: "dr-uuid", VMStatus: "failed", Error: "dial tcp: refused"})
	seen := map[string]string{}
	for _, row := range coverageRows(data, healthReport(data)) {
		if row[9] == licenseSheetName {
			seen[csvCell(row[5])] = csvCell(row[10])
		}
	}
	if seen["prod"] != "success" || seen["dr"] != "not recorded" {
		t.Fatalf("license coverage by context=%v", seen)
	}
}
