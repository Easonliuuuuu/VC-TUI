package cli

import (
	"bytes"
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

// Import workflow regressions: `vsfleet import rvtools` must work offline,
// write nothing on --dry-run, warn on a repeated import, and leave imported
// runs usable by the stored-evidence commands.

// writeImportWorkbook renders a small two-vCenter estate through vsfleet's own
// RVTools writer, so the file has exactly the headers a real export has.
func writeImportWorkbook(t *testing.T, mutate func(*assessment.ExportData)) string {
	t.Helper()
	data := assessment.ExportData{
		Run: assessment.Run{ID: 1, StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		Contexts: []assessment.ContextRun{
			{Name: "alpha", Endpoint: "https://vc-alpha.example", VCenterID: "vc-alpha-uuid"},
			{Name: "beta", Endpoint: "https://vc-beta.example", VCenterID: "vc-beta-uuid"},
		},
		VMs: []assessment.ExportVM{
			{Observation: assessment.Observation{VCenterID: "vc-alpha-uuid", Context: "alpha", VM: vsphere.VM{Location: vsphere.Location{Context: "alpha", Datacenter: "dc-a"}, ID: "vm-1", InstanceUUID: "uuid-web", Name: "web-01", PowerState: "poweredOn", CPU: 2, MemoryMB: 4096, StorageGB: 20}}},
			{Observation: assessment.Observation{VCenterID: "vc-beta-uuid", Context: "beta", VM: vsphere.VM{Location: vsphere.Location{Context: "beta", Datacenter: "dc-b"}, ID: "vm-9", Name: "web-01", PowerState: "poweredOff", CPU: 4, MemoryMB: 8192, StorageGB: 40}}},
		},
	}
	if mutate != nil {
		mutate(&data)
	}
	var buf bytes.Buffer
	if err := report.WriteRVTools(&buf, data, health.Report{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "estate.xlsx")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runImport(t *testing.T, dbPath string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &App{HistoryPath: dbPath, Out: &out, Err: &errOut}
	root := NewRootCommand(a)
	root.SetArgs(append([]string{"--history-db", dbPath, "import", "rvtools"}, args...))
	defer func() { _ = a.Close(context.Background()) }()
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestImportDryRunWritesNothingAndReportsColumnsAndGaps(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	out, _, err := runImport(t, dbPath, writeImportWorkbook(t, nil), "--dry-run", "--captured-at", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	for _, want := range []string{"Dry run", "WORKSHEET", "vInfo", "MISSING", "Coverage gaps", "resourcepool", "network", "2026-01-01", "explicit --captured-at"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output is missing %q:\n%s", want, out)
		}
	}
	// A dry run must not even open the history database.
	if _, err := os.Stat(dbPath); err == nil {
		t.Fatal("a dry run created the history database")
	}
}

func TestImportTimezoneFlagInterpretsMetadataCaptureTime(t *testing.T) {
	path := writeImportWorkbook(t, nil)
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewSheet("vMetaData"); err != nil {
		t.Fatal(err)
	}
	for cell, value := range map[string]string{"A1": "Creation Date/Time", "A2": "9/28/2026 11:12:23 AM"} {
		if err := f.SetCellValue("vMetaData", cell, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	zone, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	wantCapture := time.Date(2026, 9, 28, 11, 12, 23, 0, zone).Local().Format("2006-01-02 15:04:05")
	out, _, err := runImport(t, filepath.Join(t.TempDir(), "history.db"), path, "--dry-run", "--timezone", "America/Los_Angeles")
	if err != nil {
		t.Fatalf("dry-run with --timezone: %v", err)
	}
	for _, want := range []string{wantCapture, "vMetaData worksheet"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output is missing %q:\n%s", want, out)
		}
	}
}

func TestImportedRunIsMarkedAndWorksWithStoredEvidenceCommands(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	first := writeImportWorkbook(t, nil)
	second := writeImportWorkbook(t, func(d *assessment.ExportData) { d.VMs[0].Observation.VM.Name = "web-01-renamed" })
	for i, path := range []string{first, second} {
		if _, _, err := runImport(t, dbPath, path, "--label", []string{"pre-migration", "wave-1"}[i], "--captured-at", []string{"2026-01-01T12:00:00Z", "2026-06-01T12:00:00Z"}[i]); err != nil {
			t.Fatalf("import %d: %v", i+1, err)
		}
	}
	list, _, err := runAssessment(t, dbPath, "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(list, "rvtools-import") != 2 || !strings.Contains(list, "pre-migration") || !strings.Contains(list, "wave-1") {
		t.Errorf("assessment list does not identify both imports by source and label:\n%s", list)
	}
	// Runs are ordered by capture time, so the list must show when the data
	// was captured, not the day the workbook was imported.
	if !strings.Contains(list, "2026-01-01") || !strings.Contains(list, "2026-06-01") {
		t.Errorf("assessment list does not show the imports' capture dates:\n%s", list)
	}
	for _, args := range [][]string{
		{"diff", "pre-migration", "wave-1"},
		{"report", "wave-1"},
		{"findings", "wave-1"},
		{"trends", "snapshots"},
		{"trends", "capacity"},
	} {
		if _, _, err := runAssessment(t, dbPath, args...); err != nil {
			t.Errorf("assessment %v on an imported run: %v", args, err)
		}
	}
}

func TestRepeatImportWarnsButStillImportsAndCanBeSilenced(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	path := writeImportWorkbook(t, nil)
	if _, errOut, err := runImport(t, dbPath, path); err != nil || strings.Contains(errOut, "already imported") {
		t.Fatalf("first import: err=%v stderr=%q, want a clean first import", err, errOut)
	}
	_, errOut, err := runImport(t, dbPath, path)
	if err != nil {
		t.Fatalf("second import must not be blocked: %v", err)
	}
	if !strings.Contains(errOut, "already imported as assessment 1") {
		t.Errorf("stderr = %q, want a warning naming the earlier run", errOut)
	}
	if _, errOut, err := runImport(t, dbPath, path, "--allow-duplicate"); err != nil || strings.Contains(errOut, "already imported") {
		t.Errorf("--allow-duplicate: err=%v stderr=%q, want it silenced", err, errOut)
	}
	list, _, _ := runAssessment(t, dbPath, "list")
	if strings.Count(list, "rvtools-import") != 3 {
		t.Errorf("want three runs after three imports:\n%s", list)
	}
}

func TestMalformedWorkbookImportFailsWithoutCreatingARun(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	bad := filepath.Join(t.TempDir(), "bad.xlsx")
	if err := os.WriteFile(bad, []byte("this is not a zip archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runImport(t, dbPath, bad); err == nil {
		t.Fatal("importing garbage succeeded")
	}
	s, err := assessment.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if runs, _ := s.Runs(context.Background()); len(runs) != 0 {
		t.Fatalf("a failed import left %d run(s) behind", len(runs))
	}
}

func TestImportRequiresNoConfigurationOrNetwork(t *testing.T) {
	// The App below has no config path, no credential store and no session
	// factory: if import touched any of them it would fail or panic.
	dbPath := filepath.Join(t.TempDir(), "history.db")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	out, _, err := runImport(t, dbPath, writeImportWorkbook(t, nil))
	if err != nil {
		t.Fatalf("import with no configuration: %v", err)
	}
	if !strings.Contains(out, "Imported assessment") {
		t.Errorf("output = %q", out)
	}
}

func TestImportJSONReportCarriesGapsAndProvenance(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	var out, errOut bytes.Buffer
	a := &App{HistoryPath: dbPath, Out: &out, Err: &errOut}
	root := NewRootCommand(a)
	root.SetArgs([]string{"--history-db", dbPath, "-o", "json", "import", "rvtools", writeImportWorkbook(t, nil), "--dry-run"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Report struct {
			ProfileVersion string `json:"profile_version"`
			SourceSHA256   string `json:"source_sha256"`
			Gaps           []struct {
				Evidence string `json:"evidence"`
			} `json:"gaps"`
			Sheets []struct {
				Name string `json:"name"`
			} `json:"sheets"`
		} `json:"report"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if payload.Report.ProfileVersion == "" || len(payload.Report.SourceSHA256) != 64 || len(payload.Report.Gaps) == 0 || len(payload.Report.Sheets) == 0 {
		t.Errorf("report = %+v, want profile version, a 64-hex fingerprint, gaps and sheets", payload.Report)
	}
}
