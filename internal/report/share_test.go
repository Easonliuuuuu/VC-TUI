package report

import (
	"archive/zip"
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

var testShareKey = []byte("synthetic-share-key-0123456789ab")

// shareEstate is a synthetic estate with a duplicate VM name across two
// vCenters, a disk backing shared by both, IPs, paths and identifying free
// text.
func shareEstate() assessment.ExportData {
	data := sampleExportData(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	first := data.VMs[0]
	first.Observation.VM.Name = "payroll-db01"
	first.Observation.VM.Annotation = "owner alice@corp.example"
	first.Observation.VM.InstanceUUID, first.Observation.VM.BIOSUUID = "inst-7c1f0a", "bios-9d2e4b"
	first.Observation.VM.Folder = "/dc-a/vm/finance-team"
	first.Observation.VM.Disks[0].BackingPath = "[datastore-1] shared/shared-backing.vmdk"
	first.Observation.VM.Partitions[0].Path = "/srv/payroll"
	second := first
	second.Observation.Context, second.Observation.VCenterID = "dr-site", "vc-uuid-2"
	second.Observation.VM.ID = "vm-1"
	second.Observation.VM.Disks = append([]vsphere.VMDisk(nil), first.Observation.VM.Disks...)
	data.VMs = []assessment.ExportVM{first, second}
	data.Contexts = append(data.Contexts, assessment.ContextRun{Name: "dr-site", Endpoint: "https://vc-dr.corp.example", Datacenter: "dc-a", VCenterID: "vc-uuid-2", VMStatus: "success",
		Collections: []assessment.CollectionRun{{Kind: "host", Status: "denied", Error: "permission denied reading host payroll-db01 at vc-dr.corp.example"}, {Kind: "cluster", Status: "empty"}, {Kind: "datastore", Status: "empty"}}})
	data.Contexts[0].Endpoint = "https://vc.corp.example"
	return data
}

var shareOriginals = []string{"payroll-db01", "alice@corp.example", "finance-team", "shared-backing", "corp.example", "192.0.2.20", "192.0.2.10",
	"esx-1", "datastore-1", "dc-a", "app-pool", "DVS-1", "Management Network", "nightly", "vc-uuid", "dr-site", "disk-uuid", "inst-7c1f0a", "bios-9d2e4b",
	"00:50:56:00:00:01", "naa.123", "/srv/payroll"}

func writeShareBytes(t *testing.T, data assessment.ExportData, opts ShareOptions) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteShared(&buf, data, healthReport(data), opts); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipEntries(t *testing.T, b []byte) map[string]string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range r.File {
		rc, _ := f.Open()
		body, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(body)
	}
	return out
}

func sheetRowsOf(t *testing.T, b []byte, name string) [][]string {
	t.Helper()
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := f.GetRows(name)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestSharedProfilesDoNotLeakOriginalValues(t *testing.T) {
	data := shareEstate()
	for _, profile := range ShareProfileNames() {
		out := writeShareBytes(t, data, ShareOptions{Profile: profile, Pseudonymize: true, Key: testShareKey})
		for name, body := range zipEntries(t, out) {
			for _, original := range shareOriginals {
				if strings.Contains(body, original) {
					t.Errorf("%s: %s leaks %q", profile, name, original)
				}
			}
			if strings.Contains(body, string(testShareKey)) {
				t.Errorf("%s: %s contains the key", profile, name)
			}
		}
	}
}

func TestSharedIsDeterministicAndKeyed(t *testing.T) {
	data := shareEstate()
	opts := ShareOptions{Profile: ProfileFullInventory, Pseudonymize: true, Key: testShareKey}
	a, b := writeShareBytes(t, data, opts), writeShareBytes(t, data, opts)
	if !bytes.Equal(a, b) {
		t.Fatal("same input, profile and key must be byte-identical")
	}
	other := opts
	other.Key = []byte("a-different-key-0123456789abcdef")
	if bytes.Equal(a, writeShareBytes(t, data, other)) {
		t.Fatal("different key must give different pseudonyms")
	}
	linked := opts
	linked.LinkExports = true
	if bytes.Equal(a, writeShareBytes(t, data, linked)) {
		t.Fatal("run-bound and linked pseudonyms must differ")
	}
	// A linked token is stable across runs of the same estate; a run-bound one is not.
	later := shareEstate()
	later.Run.ID = 99
	vm := func(d assessment.ExportData, o ShareOptions) string {
		return sheetRowsOf(t, writeShareBytes(t, d, o), "vInfo")[1][0]
	}
	if vm(data, linked) != vm(later, linked) {
		t.Fatal("linked pseudonyms must survive a new run")
	}
	if vm(data, opts) == vm(later, opts) {
		t.Fatal("run-bound pseudonyms must differ between runs")
	}
}

func TestSharedPreservesJoinsAndDuplicateNames(t *testing.T) {
	out := writeShareBytes(t, shareEstate(), ShareOptions{Profile: ProfileSizingSummary, Pseudonymize: true, Key: testShareKey})
	info, disks, stores := sheetRowsOf(t, out, "vInfo"), sheetRowsOf(t, out, "vDisk"), sheetRowsOf(t, out, "vDatastore")
	col := func(rows [][]string, h string) int {
		for i, c := range rows[0] {
			if c == h {
				return i
			}
		}
		t.Fatalf("no column %s", h)
		return -1
	}
	if len(info) != 3 || len(disks) != 3 {
		t.Fatalf("rows: vInfo %d vDisk %d", len(info), len(disks))
	}
	vmI, ctxI := col(info, "VM"), col(info, "vsfleet Context")
	if info[1][vmI] != info[2][vmI] || info[1][ctxI] == info[2][ctxI] {
		t.Fatalf("duplicate VM names must share a token and stay distinguishable by context: %v %v", info[1], info[2])
	}
	vmD, ctxD, pathD := col(disks, "VM"), col(disks, "vsfleet Context"), col(disks, "Path")
	for i := 1; i <= 2; i++ {
		if disks[i][vmD] != info[i][vmI] || disks[i][ctxD] != info[i][ctxI] {
			t.Fatalf("vDisk row %d does not join to vInfo", i)
		}
	}
	if disks[1][pathD] != disks[2][pathD] || strings.Contains(disks[1][pathD], "shared") {
		t.Fatalf("shared backing must stay equal and hidden: %q %q", disks[1][pathD], disks[2][pathD])
	}
	if !strings.HasSuffix(disks[1][pathD], ".vmdk") {
		t.Fatalf("file extension should survive: %q", disks[1][pathD])
	}
	dsToken := stores[1][col(stores, "Name")]
	if !strings.HasPrefix(disks[1][pathD], "["+dsToken+"] ") {
		t.Fatalf("path datastore %q does not join to vDatastore %q", disks[1][pathD], dsToken)
	}
}

func TestSharedVMKernelPortGroupPreservesNetworkJoin(t *testing.T) {
	out := writeShareBytes(t, shareEstate(), ShareOptions{Profile: ProfileFullInventory, Pseudonymize: true, Key: testShareKey})
	vmk, ports := sheetRowsOf(t, out, "vSC_VMK"), sheetRowsOf(t, out, "vPort")
	if vmk[0][1] != "Port Group" {
		t.Fatalf("VMkernel port group header=%q, want Port Group", vmk[0][1])
	}
	if got := vmk[1][1]; !strings.HasPrefix(got, "net-") || got != ports[1][0] {
		t.Fatalf("VMkernel port group %q does not join to pseudonymized vPort %q", got, ports[1][0])
	}
}

func TestSharedFailsClosed(t *testing.T) {
	data := shareEstate()
	var buf bytes.Buffer
	cases := map[string]ShareOptions{
		"no key":        {Profile: ProfileSizingSummary, Pseudonymize: true},
		"short key":     {Profile: ProfileSizingSummary, Pseudonymize: true, Key: []byte("short")},
		"unknown":       {Profile: "everything", Key: testShareKey},
		"link no pseud": {Profile: ProfileSizingSummary, LinkExports: true},
	}
	for name, opts := range cases {
		if err := WriteShared(&buf, data, healthReport(data), opts); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if buf.Len() != 0 {
		t.Fatal("nothing may be written on failure")
	}
}

func TestShareRulesCoverEveryColumn(t *testing.T) {
	data := shareEstate()
	sheets, err := rvtoolsSheetsFor(data, healthReport(data), sheetOptions{describeAll: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sheets {
		for _, h := range s.headers {
			if _, ok := ruleFor(s.name, h); !ok {
				t.Errorf("%s/%s has no sensitivity rule", s.name, h)
			}
		}
	}
	if _, err := PlanShare(data, healthReport(data), ShareOptions{Profile: ProfileFullInventory}); err != nil {
		t.Fatal(err)
	}
	// An unlisted column fails closed rather than passing through.
	if _, _, _, err := project(sheet{name: "vInfo", headers: []string{"Brand new column"}}, []int{0}, ShareOptions{}); err == nil {
		t.Fatal("an unclassified column must be an error")
	}
	if _, err := columnIndexes(sheet{name: "vInfo", headers: []string{"VM"}}, profileSheet{name: "vInfo", columns: []string{"Nope"}}); err == nil {
		t.Fatal("an allowlisted column that does not exist must be an error")
	}
}

func TestSharePreviewMatchesExport(t *testing.T) {
	data := shareEstate()
	for _, profile := range ShareProfileNames() {
		opts := ShareOptions{Profile: profile, Pseudonymize: true, Key: testShareKey}
		plan, err := PlanShare(data, healthReport(data), ShareOptions{Profile: profile, Pseudonymize: true})
		if err != nil {
			t.Fatal(err)
		}
		out := writeShareBytes(t, data, opts)
		f, err := excelize.OpenReader(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		if got := f.GetSheetList(); len(got) != len(plan.Sheets) {
			t.Fatalf("%s: workbook has %v, plan has %d sheets", profile, got, len(plan.Sheets))
		}
		for i, ps := range plan.Sheets {
			if f.GetSheetList()[i] != ps.Name {
				t.Fatalf("%s: sheet %d is %s, plan says %s", profile, i, f.GetSheetList()[i], ps.Name)
			}
			rows, _ := f.GetRows(ps.Name)
			if len(rows)-1 != ps.Rows {
				t.Errorf("%s/%s: %d rows, plan says %d", profile, ps.Name, len(rows)-1, ps.Rows)
			}
			var headers []string
			for _, c := range ps.Columns {
				headers = append(headers, c.Name)
			}
			if !reflect.DeepEqual(rows[0], headers) {
				t.Errorf("%s/%s: headers %v, plan says %v", profile, ps.Name, rows[0], headers)
			}
		}
		f.Close()
	}
}

func TestSharePartialAssessmentStaysVisiblyPartial(t *testing.T) {
	data := shareEstate()
	plan, err := PlanShare(data, healthReport(data), ShareOptions{Profile: ProfileSizingSummary, Pseudonymize: true})
	if err != nil || !plan.Partial || len(plan.Gaps) == 0 {
		t.Fatalf("plan %+v err %v", plan.Gaps, err)
	}
	rows := sheetRowsOf(t, writeShareBytes(t, data, ShareOptions{Profile: ProfileSizingSummary, Pseudonymize: true, Key: testShareKey}), shareSheetName)
	text := ""
	for _, r := range rows {
		text += strings.Join(r, " | ") + "\n"
	}
	for _, want := range []string{"Partial assessment | yes", "Coverage gap", "denied", "not anonymous", "not guaranteed to be accepted by RVTools"} {
		if !strings.Contains(text, want) {
			t.Errorf("manifest lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "dr-site") || strings.Contains(text, "payroll-db01") {
		t.Errorf("manifest leaks original names:\n%s", text)
	}
}

func TestOrdinaryExportIsUnchangedByShareCode(t *testing.T) {
	data := shareEstate()
	var buf bytes.Buffer
	if err := WriteRVTools(&buf, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	entries := zipEntries(t, buf.Bytes())
	if !strings.Contains(entries["docProps/core.xml"], "vsfleet RVTools assessment export") || strings.Contains(entries["xl/workbook.xml"], shareSheetName) {
		t.Fatal("the ordinary export must not gain share metadata or sheets")
	}
}

func TestSharedWithoutPseudonymizationKeepsValues(t *testing.T) {
	out := writeShareBytes(t, shareEstate(), ShareOptions{Profile: ProfileSizingSummary})
	if !strings.Contains(zipEntries(t, out)["xl/sharedStrings.xml"], "payroll-db01") {
		t.Fatal("scoping alone must not rewrite values; the scan above would be vacuous")
	}
	if strings.Contains(zipEntries(t, out)["xl/sharedStrings.xml"], "192.0.2.20") {
		t.Fatal("the sizing profile must not carry IPs")
	}
}
