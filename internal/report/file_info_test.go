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

// The fixtures below are synthetic: .example endpoints, invented datastore
// names and files, deterministic timestamps.

func inventoryDatastore(t *testing.T, ctx, id, name string, accessible bool, inv *vsphere.DatastoreFileInventory) assessment.ResourceObservation {
	t.Helper()
	payload, err := json.Marshal(vsphere.Datastore{Location: vsphere.Location{Datacenter: "dc-" + ctx}, ID: id, Name: name, Accessible: accessible, FileInventory: inv})
	if err != nil {
		t.Fatal(err)
	}
	return assessment.ResourceObservation{Context: ctx, VCenterID: ctx + "-uuid", Kind: "datastore", ID: id, Name: name, Payload: payload}
}

// fileInfoFixture is a three-vCenter estate exercising every coverage state:
// prod has a complete datastore, a truncated one, a denied one and a skipped
// one; edge has one complete datastore with files and one complete empty one;
// dr's datastore collection failed outright.
func fileInfoFixture(t *testing.T) assessment.ExportData {
	when := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	const tib = int64(1) << 40
	prodComplete := &vsphere.DatastoreFileInventory{Status: vsphere.FileInventorySuccess, Limit: 100, Files: []vsphere.DatastoreInventoryFile{
		// Deliberately stored out of order: the export must sort.
		{Path: "[ds-prod-b] web-01/web-01.vmdk", Type: "VmDiskFileInfo", SizeBytes: 5 * (1 << 30)},
		{Path: "[ds-prod-b] readme.txt", Type: "FileInfo", SizeBytes: 12},
		{Path: "[ds-prod-b] web-01/web-01.vmx", Type: "VmConfigFileInfo", SizeBytes: 2048},
		{Path: "[ds-prod-b] Web-01/vmware.log", Type: "VmLogFileInfo", SizeBytes: tib},
	}}
	prodTruncated := &vsphere.DatastoreFileInventory{Status: vsphere.FileInventorySuccess, Truncated: true, TruncatedReason: "datastore file limit", Limit: 2, Files: []vsphere.DatastoreInventoryFile{
		{Path: "[ds-prod-a] db-01/db-01.vmdk", Type: "VmDiskFileInfo", SizeBytes: 100},
		{Path: "[ds-prod-a] db-01/db-01.vmx", Type: "VmConfigFileInfo", SizeBytes: 200},
	}}
	prodDenied := &vsphere.DatastoreFileInventory{Status: vsphere.FileInventoryDenied, Error: "Permission to perform this operation was denied."}
	prodSkipped := &vsphere.DatastoreFileInventory{Status: vsphere.FileInventorySkipped, Error: "context file inventory row budget was spent before this datastore was reached"}
	edgeFiles := &vsphere.DatastoreFileInventory{Status: vsphere.FileInventorySuccess, Files: []vsphere.DatastoreInventoryFile{
		{Path: "[ds-edge] app/app.vmdk", Type: "VmDiskFileInfo", SizeBytes: 3},
	}}
	edgeEmpty := &vsphere.DatastoreFileInventory{Status: vsphere.FileInventorySuccess}
	return assessment.ExportData{
		Run: assessment.Run{ID: 42, Label: "files", StartedAt: when, FinishedAt: when.Add(time.Minute), Status: assessment.RunPartial, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion},
		Contexts: []assessment.ContextRun{
			{Name: "prod", Endpoint: "https://prod.vc.example", Datacenter: "dc-prod", VCenterID: "prod-uuid", VMStatus: "success", Collections: []assessment.CollectionRun{{Kind: "datastore", Status: "success", ItemCount: 4}}},
			{Name: "edge", Endpoint: "https://edge.vc.example", Datacenter: "dc-edge", VCenterID: "edge-uuid", VMStatus: "success", Collections: []assessment.CollectionRun{{Kind: "datastore", Status: "success", ItemCount: 2}}},
			{Name: "dr", Endpoint: "https://dr.vc.example", VCenterID: "dr-uuid", VMStatus: "failed", Error: "connection refused", Collections: []assessment.CollectionRun{{Kind: "datastore", Status: "failed", Error: "connection refused"}}},
		},
		Resources: []assessment.ResourceObservation{
			inventoryDatastore(t, "prod", "datastore-4", "ds-prod-skipped", true, prodSkipped),
			inventoryDatastore(t, "prod", "datastore-2", "ds-prod-b", true, prodComplete),
			inventoryDatastore(t, "edge", "datastore-9", "ds-edge-empty", true, edgeEmpty),
			inventoryDatastore(t, "prod", "datastore-1", "ds-prod-a", true, prodTruncated),
			inventoryDatastore(t, "prod", "datastore-3", "ds-prod-denied", true, prodDenied),
			inventoryDatastore(t, "edge", "datastore-8", "ds-edge", true, edgeFiles),
		},
	}
}

func csvByName(t *testing.T, data assessment.ExportData) map[string][][]string {
	t.Helper()
	files, err := RVToolsCSV(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string][][]string)
	for _, f := range files {
		out[strings.TrimSuffix(f.Name, ".csv")] = readCSV(t, f.Data)
	}
	return out
}

func TestVFileInfoRowsCarrySizesPathsAndStableOrder(t *testing.T) {
	sheets := csvByName(t, fileInfoFixture(t))
	rows := sheets["vFileInfo"]
	if got := strings.Join(rows[0], "|"); got != "Friendly Path Name|File Name|File Type|File Size in bytes|Path|Internal Sort Column|VI SDK Server|VI SDK UUID|Datastore|Datastore ID|Datacenter|vsfleet Context" {
		t.Fatalf("headers = %q", got)
	}
	type row struct{ folder, name, typ, size, path, server, uuid, datastore, id, dc, ctx string }
	var got []row
	for _, r := range rows[1:] {
		if r[0] != r[4] || r[5] != r[4]+r[1] {
			t.Errorf("Friendly Path Name %q, Path %q and Internal Sort Column %q must follow RVTools 4.8 (folder form, folder form, path+name)", r[0], r[4], r[5])
		}
		got = append(got, row{r[0], r[1], r[2], r[3], r[4], r[6], r[7], r[8], r[9], r[10], r[11]})
	}
	want := []row{
		// Contexts sort by name (edge, prod), datastores by name, files by
		// case-insensitive path.
		{"[ds-edge] app/", "app.vmdk", "VmDiskFileInfo", "3", "[ds-edge] app/", "https://edge.vc.example", "edge-uuid", "ds-edge", "datastore-8", "dc-edge", "edge"},
		{"[ds-prod-a] db-01/", "db-01.vmdk", "VmDiskFileInfo", "100", "[ds-prod-a] db-01/", "https://prod.vc.example", "prod-uuid", "ds-prod-a", "datastore-1", "dc-prod", "prod"},
		{"[ds-prod-a] db-01/", "db-01.vmx", "VmConfigFileInfo", "200", "[ds-prod-a] db-01/", "https://prod.vc.example", "prod-uuid", "ds-prod-a", "datastore-1", "dc-prod", "prod"},
		{"[ds-prod-b]", "readme.txt", "FileInfo", "12", "[ds-prod-b]", "https://prod.vc.example", "prod-uuid", "ds-prod-b", "datastore-2", "dc-prod", "prod"},
		{"[ds-prod-b] Web-01/", "vmware.log", "VmLogFileInfo", "1099511627776", "[ds-prod-b] Web-01/", "https://prod.vc.example", "prod-uuid", "ds-prod-b", "datastore-2", "dc-prod", "prod"},
		{"[ds-prod-b] web-01/", "web-01.vmdk", "VmDiskFileInfo", "5368709120", "[ds-prod-b] web-01/", "https://prod.vc.example", "prod-uuid", "ds-prod-b", "datastore-2", "dc-prod", "prod"},
		{"[ds-prod-b] web-01/", "web-01.vmx", "VmConfigFileInfo", "2048", "[ds-prod-b] web-01/", "https://prod.vc.example", "prod-uuid", "ds-prod-b", "datastore-2", "dc-prod", "prod"},
	}
	if len(got) != len(want) {
		t.Fatalf("vFileInfo has %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestVFileInfoSizeIsAnIntegerCellInXLSX(t *testing.T) {
	data := fileInfoFixture(t)
	var out bytes.Buffer
	if err := WriteRVTools(&out, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw := excelize.Options{RawCellValue: true}
	// Row 7 is ds-prod-b's web-01.vmdk (5 GiB, past int32).
	if got, _ := f.GetCellValue("vFileInfo", "D7", raw); got != "5368709120" {
		t.Fatalf("size cell = %q, want an exact integer byte count", got)
	}
	if got, _ := f.GetCellType("vFileInfo", "D7"); got != excelize.CellTypeNumber && got != excelize.CellTypeUnset {
		t.Fatalf("size cell type = %v, want a numeric cell", got)
	}
	if got, _ := f.GetCellValue("vFileInfo", "D7"); got != "5,368,709,120" {
		t.Fatalf("size cell displays %q, want the RVTools #,##0 format", got)
	}
	if got, _ := f.GetCellValue("vFileInfo", "E7"); got != "[ds-prod-b] web-01/" {
		t.Fatalf("path cell = %q", got)
	}
	if got, _ := f.GetCellValue("vFileInfo", "F7"); got != "[ds-prod-b] web-01/web-01.vmdk" {
		t.Fatalf("internal sort column = %q", got)
	}
	for _, ref := range []string{"A7", "B7", "C7", "E7", "F7", "G7"} {
		style, err := f.GetCellStyle("vFileInfo", ref)
		if err != nil {
			t.Fatal(err)
		}
		st, err := f.GetStyle(style)
		if err != nil || st.NumFmt != 49 {
			t.Fatalf("%s number format = %+v (err %v), want text (@)", ref, st, err)
		}
	}
}

func TestVFileInfoReExportIsByteIdenticalRegardlessOfStoredOrder(t *testing.T) {
	data := fileInfoFixture(t)
	shuffled := data
	shuffled.Resources = append([]assessment.ResourceObservation(nil), data.Resources...)
	for i, j := 0, len(shuffled.Resources)-1; i < j; i, j = i+1, j-1 {
		shuffled.Resources[i], shuffled.Resources[j] = shuffled.Resources[j], shuffled.Resources[i]
	}
	var a, b, c bytes.Buffer
	for buf, d := range map[*bytes.Buffer]assessment.ExportData{&a: data, &b: data, &c: shuffled} {
		if err := WriteRVTools(buf, d, healthReport(d)); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("re-exporting the same run changed the workbook bytes")
	}
	if !bytes.Equal(a.Bytes(), c.Bytes()) {
		t.Fatal("stored resource order changed the workbook bytes")
	}
	first, err := RVToolsCSV(data, healthReport(data))
	if err != nil {
		t.Fatal(err)
	}
	second, err := RVToolsCSV(shuffled, healthReport(shuffled))
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		if !bytes.Equal(first[i].Data, second[i].Data) {
			t.Fatalf("%s differs between orderings", first[i].Name)
		}
	}
}

func fileInfoCoverageFor(sheets map[string][][]string, sheet, ctx string) []string {
	for _, row := range sheets["vsfleetCoverage"][1:] {
		// Columns: Context=5, Sheet=9.
		if row[9] == sheet && row[5] == ctx {
			return row
		}
	}
	return nil
}

func TestVFileInfoCoverageNamesEveryIncompleteDatastore(t *testing.T) {
	sheets := csvByName(t, fileInfoFixture(t))
	for _, tc := range []struct {
		sheet, ctx, status, count, contains string
	}{
		{"vFileInfo", "prod", "partial", "6", "ds-prod-a truncated"},
		{"vFileInfo/ds-prod-a", "prod", "truncated", "2", "stopped at 2 files"},
		{"vFileInfo/ds-prod-b", "prod", "complete", "4", ""},
		{"vFileInfo/ds-prod-denied", "prod", "denied", "0", "denied"},
		{"vFileInfo/ds-prod-skipped", "prod", "skipped", "0", "budget"},
		{"vFileInfo", "edge", "success", "1", ""},
		{"vFileInfo/ds-edge-empty", "edge", "complete", "0", ""},
		// The datastore list never arrived: nothing could be attempted.
		{"vFileInfo", "dr", "failed", "0", "no file inventory could be attempted"},
	} {
		row := fileInfoCoverageFor(sheets, tc.sheet, tc.ctx)
		if row == nil {
			t.Errorf("no vsfleetCoverage row for %s/%s", tc.ctx, tc.sheet)
			continue
		}
		if row[10] != tc.status || row[11] != tc.count || !strings.Contains(row[12], tc.contains) {
			t.Errorf("%s/%s coverage = status %q count %q error %q; want status %q count %q containing %q", tc.ctx, tc.sheet, row[10], row[11], row[12], tc.status, tc.count, tc.contains)
		}
	}
	if row := fileInfoCoverageFor(sheets, "vFileInfo", "prod"); row != nil && !strings.Contains(row[12], "NOT evidence") {
		t.Errorf("a partial context must say an absent file is not evidence: %q", row[12])
	}
	// A truncated listing must never be labelled complete anywhere.
	for _, row := range sheets["vsfleetCoverage"][1:] {
		if row[9] == "vFileInfo/ds-prod-a" && (row[10] == "complete" || row[10] == "success") {
			t.Fatalf("truncated datastore labelled %q", row[10])
		}
	}
}

func TestVFileInfoWithoutCaptureIsAnExplainedPlaceholderNotAnEmptyTab(t *testing.T) {
	data := sampleExportData(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	sheets := csvByName(t, data)
	rows := sheets["vFileInfo"]
	if len(rows) != 2 {
		t.Fatalf("vFileInfo has %d rows, want the header and one explanatory row", len(rows))
	}
	if !strings.Contains(rows[1][0], "does not mean the datastores hold no files") || !strings.Contains(rows[1][0], "not captured") {
		t.Fatalf("placeholder = %q", rows[1][0])
	}
	for _, cell := range rows[1][1:] {
		if cell != "" {
			t.Fatalf("placeholder row carries data cells: %q", rows[1])
		}
	}
	row := fileInfoCoverageFor(sheets, "vFileInfo", "prod")
	if row == nil || row[10] != "not recorded" || row[11] != "0" || !strings.Contains(row[12], "--datastore-file-inventory") {
		t.Fatalf("coverage for an unrequested inventory = %v", row)
	}
	if strings.Count(strings.Join(row, ","), "vFileInfo/") != 0 {
		t.Fatalf("an unrequested inventory must not invent per-datastore rows: %v", row)
	}
}

func TestVFileInfoWhenEveryDatastoreFailedSaysSoInsteadOfLookingEmpty(t *testing.T) {
	data := fileInfoFixture(t)
	data.Resources = []assessment.ResourceObservation{
		inventoryDatastore(t, "prod", "datastore-3", "ds-prod-denied", true, &vsphere.DatastoreFileInventory{Status: vsphere.FileInventoryDenied, Error: "Permission to perform this operation was denied."}),
		inventoryDatastore(t, "prod", "datastore-5", "ds-prod-offline", false, &vsphere.DatastoreFileInventory{Status: vsphere.FileInventoryUnavailable, Error: "datastore is inaccessible"}),
	}
	sheets := csvByName(t, data)
	rows := sheets["vFileInfo"]
	if len(rows) != 2 || !strings.Contains(rows[1][0], "denied, failed, skipped, unavailable") {
		t.Fatalf("vFileInfo = %v, want one explanatory row naming why there are no files", rows)
	}
	if row := fileInfoCoverageFor(sheets, "vFileInfo", "prod"); row == nil || row[10] != "failed" || row[11] != "0" {
		t.Fatalf("context coverage = %v, want failed with 0 files", row)
	}
	if row := fileInfoCoverageFor(sheets, "vFileInfo/ds-prod-offline", "prod"); row == nil || row[10] != "unavailable" {
		t.Fatalf("offline datastore coverage = %v", row)
	}
}

func TestVFileInfoPredatingSchemaSaysSo(t *testing.T) {
	data := sampleExportData(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	data.Run.InventorySchemaVersion = "16"
	row := fileInfoCoverageFor(csvByName(t, data), "vFileInfo", "prod")
	if row == nil || row[10] != "not recorded" || !strings.Contains(row[12], "predates") {
		t.Fatalf("coverage for a pre-inventory schema = %v", row)
	}
}

func TestXLSXRefusesAWorksheetBeyondTheRowLimit(t *testing.T) {
	over := sheet{name: "vFileInfo", rows: make([][]any, maxXLSXRows)}
	if err := checkXLSXRows([]sheet{over}); err == nil || !strings.Contains(err.Error(), "--format csv") {
		t.Fatalf("err = %v, want a refusal that points at CSV", err)
	}
	if err := checkXLSXRows([]sheet{{name: "vFileInfo", rows: make([][]any, maxXLSXRows-1)}}); err != nil {
		t.Fatalf("a full-but-legal sheet was refused: %v", err)
	}
}
