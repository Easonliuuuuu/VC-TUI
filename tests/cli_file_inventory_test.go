package tests

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vmware/govmomi/simulator"
)

// fileInventoryEstate starts a one-datastore simulator seeded with synthetic
// files and returns a runner whose capture and export share a history DB.
func fileInventoryEstate(t *testing.T) (*runner, *vcenter, string, string) {
	t.Helper()
	t.Setenv("VSFLEET_E2E_PASSWORD", testPassword)
	vc := startVCenter(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Cluster = 1
		m.ClusterHost = 1
		m.Machine = 1
		m.Datastore = 1
	})
	probe := datastoreTestClient(t, vc)
	base := datastoreDirectory(t, probe)
	for path, content := range map[string]string{
		"synthetic-vm/synthetic-vm.vmx":  "0123456789",
		"synthetic-vm/synthetic-vm.vmdk": "01234567890123456789",
		"synthetic-vm/vmware.log":        "abcde",
		"notes.txt":                      "1234567",
	} {
		full := filepath.Join(base, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := newRunner(t)
	r.addNonInteractiveContext("lab", vc, "env:VSFLEET_E2E_PASSWORD")
	return r, vc, filepath.Join(t.TempDir(), "history.db"), base
}

func coverageRow(t *testing.T, dir, sheet string) []string {
	t.Helper()
	for _, row := range readCSVFile(t, filepath.Join(dir, "vsfleetCoverage.csv"))[1:] {
		if row[9] == sheet {
			return row
		}
	}
	t.Fatalf("no vsfleetCoverage row for %s", sheet)
	return nil
}

func TestDefaultAssessmentNeverBrowsesDatastoresAndSaysVFileInfoIsNotCaptured(t *testing.T) {
	r, vc, db, _ := fileInventoryEstate(t)
	// A default capture must not issue a recursive datastore search at all.
	vc.Service.FaultInjector().AddRule(&simulator.FaultInjectionRule{MethodName: "SearchDatastoreSubFolders_Task", ObjectType: "*", ObjectName: "*", Probability: 1, FaultType: simulator.FaultTypeNoPermission, Message: "browsing must be opt-in", Enabled: true})
	stdout, stderr, err := r.run("", "--history-db", db, "assessment", "run", "--all-contexts")
	if err != nil {
		t.Fatalf("assessment run: %v\n%s\n%s", err, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, "file inventory") || strings.Contains(stderr, "browsing must be opt-in") {
		t.Fatalf("a default capture touched the datastore browser:\n%s\n%s", stdout, stderr)
	}
	dir := filepath.Join(t.TempDir(), "csv")
	r.mustRun("", "--history-db", db, "assessment", "export", "latest", "--format", "csv", "--file", dir)
	rows := readCSVFile(t, filepath.Join(dir, "vFileInfo.csv"))
	if len(rows) != 2 || !strings.Contains(rows[1][0], "does not mean the datastores hold no files") {
		t.Fatalf("vFileInfo of a default run = %v, want one explanatory row", rows)
	}
	cov := coverageRow(t, dir, "vFileInfo")
	if cov[10] != "not recorded" || !strings.Contains(cov[12], "--datastore-file-inventory") {
		t.Fatalf("coverage = %v", cov)
	}
}

func TestFileInventoryLimitFlagsDoNotSilentlyEnableBrowsing(t *testing.T) {
	r, _, db, _ := fileInventoryEstate(t)
	_, _, err := r.run("", "--history-db", db, "assessment", "run", "--all-contexts", "--file-inventory-max-files", "5")
	if err == nil || !strings.Contains(err.Error(), "--datastore-file-inventory") {
		t.Fatalf("limit flag without opt-in: err = %v, want a refusal naming --datastore-file-inventory", err)
	}
	_, _, err = r.run("", "--history-db", db, "assessment", "run", "--all-contexts", "--datastore-file-inventory", "--file-inventory-max-files", "0")
	if err == nil || !strings.Contains(err.Error(), "--file-inventory-max-files") {
		t.Fatalf("zero limit: err = %v", err)
	}
	// Browsing VMDK evidence must not switch the file inventory on either.
	r.mustRun("", "--history-db", db, "assessment", "run", "--all-contexts", "--browse-datastores")
	dir := filepath.Join(t.TempDir(), "csv")
	r.mustRun("", "--history-db", db, "assessment", "export", "latest", "--format", "csv", "--file", dir)
	if cov := coverageRow(t, dir, "vFileInfo"); cov[10] != "not recorded" {
		t.Fatalf("--browse-datastores enabled the file inventory: %v", cov)
	}
}

func TestFileInventoryCaptureExportsFilesAndIsOfflineAndByteIdentical(t *testing.T) {
	r, _, db, _ := fileInventoryEstate(t)
	stdout := r.mustRun("", "--history-db", db, "assessment", "run", "--all-contexts", "--datastore-file-inventory", "-o", "json")
	var run struct {
		ID            int64 `json:"id"`
		FileInventory []struct {
			Context    string `json:"context"`
			Requested  bool   `json:"requested"`
			Datastores []struct {
				Status string `json:"status"`
				Files  int    `json:"files"`
			} `json:"datastores"`
		} `json:"file_inventory"`
	}
	if err := json.Unmarshal([]byte(stdout), &run); err != nil {
		t.Fatalf("run JSON: %v\n%s", err, stdout)
	}
	if run.ID == 0 || len(run.FileInventory) != 1 || !run.FileInventory[0].Requested || run.FileInventory[0].Datastores[0].Status != "complete" || run.FileInventory[0].Datastores[0].Files < 4 {
		t.Fatalf("run JSON file_inventory = %+v", run.FileInventory)
	}

	// Export from an empty configuration: it must need no vCenter, no context,
	// no credentials.
	offline := newRunner(t)
	first := filepath.Join(t.TempDir(), "a")
	second := filepath.Join(t.TempDir(), "b")
	receiptJSON := offline.mustRun("", "--history-db", db, "assessment", "export", "latest", "--format", "csv", "--file", first, "-o", "json")
	offline.mustRun("", "--history-db", db, "assessment", "export", "latest", "--format", "csv", "--file", second)
	var receipt struct {
		FileInventory []struct {
			Datastores []struct{ Status string } `json:"datastores"`
		} `json:"file_inventory"`
	}
	if err := json.Unmarshal([]byte(receiptJSON), &receipt); err != nil || len(receipt.FileInventory) != 1 || receipt.FileInventory[0].Datastores[0].Status != "complete" {
		t.Fatalf("export receipt = %s (%v)", receiptJSON, err)
	}
	a, _ := os.ReadFile(filepath.Join(first, "vFileInfo.csv"))
	b, _ := os.ReadFile(filepath.Join(second, "vFileInfo.csv"))
	if len(a) == 0 || !bytes.Equal(a, b) {
		t.Fatal("re-exporting the stored run changed vFileInfo.csv")
	}

	rows := readCSVFile(t, filepath.Join(first, "vFileInfo.csv"))
	sizes := map[string]string{}
	for _, row := range rows[1:] {
		sizes[row[5]] = row[3] + "/" + row[2] // Internal Sort Column -> size/type
		if row[0] != row[4] || row[11] != "lab" {
			t.Errorf("row %v: folder columns disagree or context missing", row)
		}
	}
	var ds string
	for _, row := range rows[1:] {
		ds = row[8]
	}
	for suffix, want := range map[string]string{
		"synthetic-vm/synthetic-vm.vmx":  "10/VmConfigFileInfo",
		"synthetic-vm/synthetic-vm.vmdk": "20/VmDiskFileInfo",
		"synthetic-vm/vmware.log":        "5/VmLogFileInfo",
		"notes.txt":                      "7/FileInfo",
	} {
		// Internal Sort Column: "[ds] folder/name", or "[ds]name" at the
		// datastore root, as RVTools writes it.
		key := "[" + ds + "] " + suffix
		if !strings.Contains(suffix, "/") {
			key = "[" + ds + "]" + suffix
		}
		if got := sizes[key]; got != want {
			t.Errorf("%s = %q, want %q (all: %v)", key, got, want, sizes)
		}
	}
	if cov := coverageRow(t, first, "vFileInfo"); cov[10] != "success" {
		t.Fatalf("coverage = %v", cov)
	}
}

func TestFileInventoryTruncationAndDenialAreVisibleInHumanAndMachineOutput(t *testing.T) {
	r, vc, db, _ := fileInventoryEstate(t)
	stdout, stderr, err := r.run("", "--history-db", db, "assessment", "run", "--all-contexts", "--datastore-file-inventory", "--file-inventory-max-files", "2")
	if err != nil {
		t.Fatalf("run: %v\n%s\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "TRUNCATED") || !strings.Contains(stderr, "truncated") || !strings.Contains(stderr, "not evidence") {
		t.Fatalf("human output hides the truncation:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	dir := filepath.Join(t.TempDir(), "csv")
	_, expErr, err := r.run("", "--history-db", db, "assessment", "export", "latest", "--format", "csv", "--file", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expErr, "truncated") || !strings.Contains(expErr, "sensitive") {
		t.Fatalf("export stderr = %q, want the truncation warning and the privacy note", expErr)
	}
	if rows := readCSVFile(t, filepath.Join(dir, "vFileInfo.csv")); len(rows) != 3 {
		t.Fatalf("truncated inventory kept %d rows, want exactly 2", len(rows)-1)
	}
	summary := coverageRow(t, dir, "vFileInfo")
	if summary[10] == "success" || summary[10] == "empty" || !strings.Contains(summary[12], "truncated") {
		t.Fatalf("summary coverage labels a truncated scan complete: %v", summary)
	}
	for _, row := range readCSVFile(t, filepath.Join(dir, "vsfleetCoverage.csv"))[1:] {
		if strings.HasPrefix(row[9], "vFileInfo/") && (row[10] != "truncated" || row[11] != "2") {
			t.Fatalf("datastore coverage = %v, want truncated with 2 files", row)
		}
	}

	// A denied browse: nothing listed, and the tab says why instead of
	// looking like an empty datastore.
	vc.Service.FaultInjector().AddRule(&simulator.FaultInjectionRule{MethodName: "SearchDatastoreSubFolders_Task", ObjectType: "*", ObjectName: "*", Probability: 1, FaultType: simulator.FaultTypeNoPermission, Message: "Permission to perform this operation was denied.", Enabled: true})
	stdout, stderr, err = r.run("", "--history-db", db, "assessment", "run", "--all-contexts", "--datastore-file-inventory")
	if err != nil {
		t.Fatalf("run: %v\n%s\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "DENIED") || !strings.Contains(stderr, "denied") {
		t.Fatalf("human output hides the denial:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	denied := filepath.Join(t.TempDir(), "csv")
	r.mustRun("", "--history-db", db, "assessment", "export", "latest", "--format", "csv", "--file", denied)
	rows := readCSVFile(t, filepath.Join(denied, "vFileInfo.csv"))
	if len(rows) != 2 || !strings.Contains(rows[1][0], "denied, failed, skipped, unavailable") {
		t.Fatalf("vFileInfo of a denied run = %v", rows)
	}
	if cov := coverageRow(t, denied, "vFileInfo"); cov[10] != "failed" || cov[11] != "0" {
		t.Fatalf("denied coverage = %v", cov)
	}
	for _, row := range readCSVFile(t, filepath.Join(denied, "vsfleetCoverage.csv"))[1:] {
		if strings.HasPrefix(row[9], "vFileInfo/") && row[10] != "denied" {
			t.Fatalf("datastore coverage = %v, want denied", row)
		}
	}
}
