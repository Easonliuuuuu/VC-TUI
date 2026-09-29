package tests

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
)

// Distinctive, visibly synthetic stand-ins for license keys. If either string
// appears anywhere a user or a downstream consumer can look, a key leaked.
const (
	licenseKeyStandard = "SYNTH-LICEN-SEKEY-MUST-NEVERLEAK"
	licenseKeyTrial    = "SYNTH-TRIAL-KEYSH-OULDN-OTLEAK"
)

var licenseKeys = []string{licenseKeyStandard, licenseKeyTrial}

// startLicensedVCenter starts a vcsim whose license state is fully synthetic.
// tune runs after the license state is seeded so a test can degrade it.
func startLicensedVCenter(t *testing.T, tune func(model *simulator.Model)) *vcenter {
	t.Helper()
	var model *simulator.Model
	vc := startVCenter(t, func(m *simulator.Model) {
		model = m
		m.Datacenter = 1
		m.Cluster = 1
		m.ClusterHost = 2
		m.Machine = 2
	})
	m := model.Map()
	lm := m.Get(types.ManagedObjectReference{Type: "LicenseManager", Value: "LicenseManager"}).(*simulator.LicenseManager)
	standard := types.LicenseManagerLicenseInfo{
		LicenseKey: licenseKeyStandard, EditionKey: "esx.standard.cpuPackage", Name: "Synthetic vSphere Standard (example)",
		Total: 16, Used: 2, CostUnit: "cpuPackage",
		Properties: []types.KeyAnyValue{
			{Key: "expirationDate", Value: time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)},
			{Key: "feature", Value: types.KeyValue{Key: "vmotion", Value: "vMotion"}},
		},
		Labels: []types.KeyValue{{Key: "owner", Value: "synthetic-team"}},
	}
	trial := types.LicenseManagerLicenseInfo{LicenseKey: licenseKeyTrial, EditionKey: "eval", Name: "Synthetic Evaluation Mode (example)"}
	lm.Licenses = []types.LicenseManagerLicenseInfo{standard, trial}
	lam := m.Get(*lm.LicenseAssignmentManager).(*simulator.LicenseAssignmentManager)
	lam.QueryAssignedLicensesResponse.Returnval = nil
	for _, ref := range m.AllReference("HostSystem") {
		lam.QueryAssignedLicensesResponse.Returnval = append(lam.QueryAssignedLicensesResponse.Returnval, types.LicenseAssignmentManagerLicenseAssignment{
			EntityId: ref.Reference().Value, EntityDisplayName: "synthetic-" + ref.Reference().Value, Scope: "synthetic-vcenter-uuid", AssignedLicense: standard,
		})
	}
	if tune != nil {
		tune(model)
	}
	return vc
}

// allBytes returns every byte a directory holds, expanding XLSX archives so a
// compressed key cannot hide from the search.
func allBytes(t *testing.T, root string) []byte {
	t.Helper()
	var out bytes.Buffer
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out.Write(data)
		if strings.HasSuffix(path, ".xlsx") {
			zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				return err
			}
			for _, f := range zr.File {
				rc, err := f.Open()
				if err != nil {
					return err
				}
				b, err := io.ReadAll(rc)
				_ = rc.Close()
				if err != nil {
					return err
				}
				out.Write(b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	return out.Bytes()
}

func assertNoKeys(t *testing.T, where string, data []byte) {
	t.Helper()
	for _, key := range licenseKeys {
		if bytes.Contains(data, []byte(key)) {
			t.Fatalf("license key %q leaked into %s", key, where)
		}
	}
}

func csvRows(t *testing.T, dir, name string) [][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	rows, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return rows
}

func coverageStatus(t *testing.T, dir, sheet string) (status, message string, found bool) {
	t.Helper()
	for _, row := range csvRows(t, dir, "vsfleetCoverage.csv")[1:] {
		if row[9] == sheet {
			return row[10], row[12], true
		}
	}
	return "", "", false
}

func TestDefaultAssessmentDoesNotCollectLicenses(t *testing.T) {
	t.Setenv("VSFLEET_E2E_PASSWORD", testPassword)
	vc := startLicensedVCenter(t, nil)
	r := newRunner(t)
	r.addNonInteractiveContext("lab", vc, "env:VSFLEET_E2E_PASSWORD")
	historyDir := t.TempDir()
	historyDB := filepath.Join(historyDir, "history.db")

	stdout, stderr, err := r.run("", "--history-db", historyDB, "assessment", "run", "--all-contexts")
	if err != nil {
		t.Fatalf("assessment run: %v\n%s", err, stderr)
	}
	assertNoKeys(t, "default run output", []byte(stdout+stderr))
	dir := filepath.Join(t.TempDir(), "csv")
	r.mustRun("", "--history-db", historyDB, "assessment", "export", "latest", "--format", "csv", "--file", dir)
	if _, err := os.Stat(filepath.Join(dir, "vLicense.csv")); err == nil {
		t.Fatal("a default assessment exported vLicense.csv")
	}
	if _, _, found := coverageStatus(t, dir, "vLicense"); found {
		t.Fatal("a default assessment has vLicense coverage; it must not claim to have looked")
	}
	report := r.mustRun("", "--history-db", historyDB, "assessment", "report", "latest", "-o", "json")
	if strings.Contains(report, `"license"`) {
		t.Fatalf("default report mentions a license collection:\n%s", report)
	}
	// Nothing of the license state may be in the ledger at all.
	assertNoKeys(t, "history database", allBytes(t, historyDir))
	if bytes.Contains(allBytes(t, historyDir), []byte("Synthetic vSphere Standard")) {
		t.Fatal("license product names were persisted by a default assessment")
	}
}

func TestLicenseAssessmentReportsMetadataAndNeverAKey(t *testing.T) {
	t.Setenv("VSFLEET_E2E_PASSWORD", testPassword)
	vc := startLicensedVCenter(t, nil)
	r := newRunner(t)
	r.addNonInteractiveContext("lab", vc, "env:VSFLEET_E2E_PASSWORD")
	historyDir := t.TempDir()
	historyDB := filepath.Join(historyDir, "history.db")

	var transcript strings.Builder
	run := func(args ...string) string {
		stdout, stderr, err := r.run("", append([]string{"--history-db", historyDB}, args...)...)
		if err != nil {
			t.Fatalf("vsfleet %s: %v\n%s", strings.Join(args, " "), err, stderr)
		}
		transcript.WriteString(stdout + stderr)
		return stdout
	}
	runJSON := run("assessment", "run", "--all-contexts", "--include-licenses", "-o", "json")
	if !strings.Contains(runJSON, `"status": "complete"`) && !strings.Contains(runJSON, `"status":"complete"`) {
		t.Fatalf("a fully answered licensed run should be complete:\n%s", runJSON)
	}
	reportJSON := run("assessment", "report", "latest", "-o", "json")
	if !strings.Contains(reportJSON, `"license"`) {
		t.Fatalf("report does not show the license collection:\n%s", reportJSON)
	}

	csvA := filepath.Join(t.TempDir(), "csv-a")
	xlsx := filepath.Join(t.TempDir(), "out", "estate.xlsx")
	run("assessment", "export", "latest", "--format", "csv", "--file", csvA)
	run("assessment", "export", "latest", "--format", "rvtools", "--file", xlsx)

	rows := csvRows(t, csvA, "vLicense.csv")
	if len(rows) != 3 {
		t.Fatalf("vLicense rows=%v", rows)
	}
	var std []string
	for _, row := range rows[1:] {
		if row[1] != "[redacted]" {
			t.Fatalf("Key cell=%q", row[1])
		}
		if strings.Contains(row[0], "Standard") {
			std = row
		}
	}
	if std == nil || std[3] != "cpuPackage" || std[4] != "16" || std[5] != "2" || std[6] != "2030-06-01T00:00:00Z" || std[7] != "vMotion" {
		t.Fatalf("standard license row=%v", std)
	}
	assign := csvRows(t, csvA, "vsfleetLicenseAssignment.csv")
	if len(assign) != 4 { // header + three hosts
		t.Fatalf("assignments=%v", assign)
	}
	for _, sheet := range []string{"vLicense", "vsfleetLicenseAssignment"} {
		if status, message, found := coverageStatus(t, csvA, sheet); !found || status != "success" || message != "" {
			t.Fatalf("%s coverage=%q %q found=%v", sheet, status, message, found)
		}
	}

	// Re-export from the unchanged run is deterministic and needs neither
	// configuration nor credentials nor a network: a fresh runner with an empty
	// config produces byte-identical output.
	csvB := filepath.Join(t.TempDir(), "csv-b")
	offline := newRunner(t)
	if _, stderr, err := offline.run("", "--history-db", historyDB, "assessment", "export", "latest", "--format", "csv", "--file", csvB); err != nil {
		t.Fatalf("offline re-export: %v\n%s", err, stderr)
	}
	for _, name := range []string{"vLicense.csv", "vsfleetLicenseAssignment.csv", "vsfleetCoverage.csv"} {
		a, _ := os.ReadFile(filepath.Join(csvA, name))
		b, _ := os.ReadFile(filepath.Join(csvB, name))
		if !bytes.Equal(a, b) {
			t.Fatalf("%s differs between exports of an unchanged run", name)
		}
	}

	// The proof: no key in any output, log, export or byte of the ledger.
	assertNoKeys(t, "command output", []byte(transcript.String()))
	assertNoKeys(t, "CSV export", allBytes(t, csvA))
	assertNoKeys(t, "XLSX export", allBytes(t, filepath.Dir(xlsx)))
	assertNoKeys(t, "history database", allBytes(t, historyDir))
	// Positive control: the searches above do read the metadata that was
	// legitimately stored, so an absent key is a real absence.
	if !bytes.Contains(allBytes(t, historyDir), []byte("Synthetic vSphere Standard")) {
		t.Fatal("positive control failed: license metadata is not in the ledger being searched")
	}
	if !bytes.Contains(allBytes(t, filepath.Dir(xlsx)), []byte("Synthetic vSphere Standard")) {
		t.Fatal("positive control failed: license metadata is not in the XLSX being searched")
	}
	// Labels are free text and are not collected.
	if bytes.Contains(allBytes(t, historyDir), []byte("synthetic-team")) {
		t.Fatal("license labels were persisted")
	}
}

func TestLicenseAssessmentWithDeniedAssignmentsIsExplicit(t *testing.T) {
	t.Setenv("VSFLEET_E2E_PASSWORD", testPassword)
	vc := startLicensedVCenter(t, func(model *simulator.Model) {
		model.Service.FaultInjector().AddRule(&simulator.FaultInjectionRule{
			MethodName: "QueryAssignedLicenses", ObjectType: "*", ObjectName: "*", Probability: 1, Enabled: true,
			FaultType: simulator.FaultTypeCustom, Fault: &types.NoPermission{PrivilegeId: "Global.Licenses"},
		})
	})
	r := newRunner(t)
	r.addNonInteractiveContext("lab", vc, "env:VSFLEET_E2E_PASSWORD")
	historyDir := t.TempDir()
	historyDB := filepath.Join(historyDir, "history.db")

	stdout, stderr, err := r.run("", "--history-db", historyDB, "assessment", "run", "--all-contexts", "--include-licenses")
	if err != nil {
		t.Fatalf("assessment run: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "partial") || !strings.Contains(stderr, "license collection partial") || !strings.Contains(stderr, "Global.Licenses") {
		t.Fatalf("denied license assignments must surface as a partial run.\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	// --fail-on-partial gates automation on it.
	if _, _, err := r.run("", "--history-db", historyDB, "assessment", "run", "--all-contexts", "--include-licenses", "--fail-on-partial"); err == nil {
		t.Fatal("--fail-on-partial did not fail for a denied license collection")
	}
	dir := filepath.Join(t.TempDir(), "csv")
	r.mustRun("", "--history-db", historyDB, "assessment", "export", "latest", "--format", "csv", "--file", dir)
	if status, _, _ := coverageStatus(t, dir, "vsfleetLicenseAssignment"); status != "unavailable" {
		t.Fatalf("assignment coverage=%q, want unavailable", status)
	}
	if _, message, _ := coverageStatus(t, dir, "vsfleetLicenseAssignment"); !strings.Contains(message, "permission denied") {
		t.Fatalf("assignment coverage message=%q", message)
	}
	assertNoKeys(t, "history database", allBytes(t, historyDir))
	assertNoKeys(t, "CSV export", allBytes(t, dir))
	assertNoKeys(t, "output", []byte(stdout+stderr))
}

// A read-only account can be given an empty list instead of an error. That
// must never render as an apparently complete, license-free estate.
func TestLicenseAssessmentWithEmptyListIsUnavailable(t *testing.T) {
	t.Setenv("VSFLEET_E2E_PASSWORD", testPassword)
	vc := startLicensedVCenter(t, func(model *simulator.Model) {
		m := model.Map()
		lm := m.Get(types.ManagedObjectReference{Type: "LicenseManager", Value: "LicenseManager"}).(*simulator.LicenseManager)
		lm.Licenses = nil
		lam := m.Get(*lm.LicenseAssignmentManager).(*simulator.LicenseAssignmentManager)
		lam.QueryAssignedLicensesResponse.Returnval = nil
	})
	r := newRunner(t)
	r.addNonInteractiveContext("lab", vc, "env:VSFLEET_E2E_PASSWORD")
	historyDB := filepath.Join(t.TempDir(), "history.db")

	stdout, stderr, err := r.run("", "--history-db", historyDB, "assessment", "run", "--all-contexts", "--include-licenses")
	if err != nil {
		t.Fatalf("assessment run: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "partial") {
		t.Fatalf("run with no readable licenses must not be reported complete: %s", stdout)
	}
	dir := filepath.Join(t.TempDir(), "csv")
	r.mustRun("", "--history-db", historyDB, "assessment", "export", "latest", "--format", "csv", "--file", dir)
	if rows := csvRows(t, dir, "vLicense.csv"); len(rows) != 1 {
		t.Fatalf("vLicense.csv has %d rows, want header only", len(rows))
	}
	for _, sheet := range []string{"vLicense", "vsfleetLicenseAssignment"} {
		status, message, found := coverageStatus(t, dir, sheet)
		if !found || status != "unavailable" || !strings.Contains(message, "Global.Licenses") {
			t.Fatalf("%s coverage=%q %q found=%v", sheet, status, message, found)
		}
	}
}
