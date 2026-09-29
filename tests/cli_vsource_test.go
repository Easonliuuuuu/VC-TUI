package tests

import (
	"bytes"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vmware/govmomi/simulator"
)

func readCSVFile(t *testing.T, path string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return rows
}

func columnIndex(t *testing.T, header []string, name string) int {
	t.Helper()
	for i, h := range header {
		if h == name {
			return i
		}
	}
	t.Fatalf("column %q not in %v", name, header)
	return -1
}

// TestAssessmentCaptureExportsAttributedVSource captures two independent
// simulated vCenters with distinct ServiceInstance identities plus one
// unreachable context, then proves the vSource worksheet attributes each
// stored About record to its own context, states the failed context's gap in
// vsfleetCoverage, and re-exports byte-identically through a runner that has no
// configured contexts at all, so the export can only have used the stored run.
func TestAssessmentCaptureExportsAttributedVSource(t *testing.T) {
	t.Setenv("VSFLEET_E2E_PASSWORD", testPassword)
	tune := func(version, build, uuid string) func(*simulator.Model) {
		return func(m *simulator.Model) {
			m.Datacenter, m.Cluster, m.ClusterHost, m.Machine = 1, 1, 1, 1
			m.ServiceContent.About.Version = version
			m.ServiceContent.About.Build = build
			m.ServiceContent.About.InstanceUuid = uuid
		}
	}
	alpha := startVCenter(t, tune("8.0.3", "24022515", "aaaaaaaa-0000-4000-8000-000000000001"))
	beta := startVCenter(t, tune("7.0.3", "20150588", "bbbbbbbb-0000-4000-8000-000000000002"))
	r := newRunner(t)
	r.addNonInteractiveContext("alpha", alpha, "env:VSFLEET_E2E_PASSWORD")
	r.addNonInteractiveContext("beta", beta, "env:VSFLEET_E2E_PASSWORD")
	addUnreachableContext(r, "down")
	historyDB := filepath.Join(t.TempDir(), "history.db")
	if _, _, err := r.run("", "--history-db", historyDB, "assessment", "run", "--all-contexts"); err != nil {
		t.Fatalf("assessment run: %v", err)
	}

	offline := newRunner(t)
	firstDir := filepath.Join(t.TempDir(), "csv-1")
	secondDir := filepath.Join(t.TempDir(), "csv-2")
	offline.mustRun("", "--history-db", historyDB, "assessment", "export", "latest", "--format", "csv", "--file", firstDir)
	offline.mustRun("", "--history-db", historyDB, "assessment", "export", "latest", "--format", "csv", "--file", secondDir)
	first, _ := os.ReadFile(filepath.Join(firstDir, "vSource.csv"))
	second, _ := os.ReadFile(filepath.Join(secondDir, "vSource.csv"))
	if len(first) == 0 || !bytes.Equal(first, second) {
		t.Fatal("vSource.csv is missing or differs between exports of the same run")
	}

	rows := readCSVFile(t, filepath.Join(firstDir, "vSource.csv"))
	if len(rows) != 3 {
		t.Fatalf("vSource has %d rows, want header plus one per successful context: %v", len(rows), rows)
	}
	header := rows[0]
	byContext := map[string][]string{}
	for _, row := range rows[1:] {
		byContext[row[columnIndex(t, header, "vsfleet Context")]] = row
	}
	for name, want := range map[string]struct{ version, build, uuid string }{
		"alpha": {"8.0.3", "24022515", "aaaaaaaa-0000-4000-8000-000000000001"},
		"beta":  {"7.0.3", "20150588", "bbbbbbbb-0000-4000-8000-000000000002"},
	} {
		row, ok := byContext[name]
		if !ok {
			t.Fatalf("no vSource row for context %q: %v", name, rows)
		}
		if got := row[columnIndex(t, header, "Version")]; got != want.version {
			t.Errorf("%s Version=%q, want %q", name, got, want.version)
		}
		if got := row[columnIndex(t, header, "Build")]; got != want.build {
			t.Errorf("%s Build=%q, want %q", name, got, want.build)
		}
		if got := row[columnIndex(t, header, "VI SDK UUID")]; got != want.uuid {
			t.Errorf("%s VI SDK UUID=%q, want %q", name, got, want.uuid)
		}
		if got := row[columnIndex(t, header, "API type")]; got != "VirtualCenter" {
			t.Errorf("%s API type=%q, want VirtualCenter", name, got)
		}
	}
	if _, ok := byContext["down"]; ok {
		t.Fatal("the unreachable context has a vSource row; no version may be invented")
	}
	if got := byContext["alpha"][columnIndex(t, header, "VI SDK Server")]; got != alpha.URL {
		t.Errorf("alpha VI SDK Server=%q, want its endpoint %q", got, alpha.URL)
	}

	coverage := readCSVFile(t, filepath.Join(firstDir, "vsfleetCoverage.csv"))
	ch := coverage[0]
	seen := map[string]bool{}
	for _, row := range coverage[1:] {
		if row[columnIndex(t, ch, "Sheet")] != "vSource" {
			continue
		}
		name, status := row[columnIndex(t, ch, "Context")], row[columnIndex(t, ch, "Collection status")]
		seen[name] = true
		switch name {
		case "alpha", "beta":
			if status != "success" || row[columnIndex(t, ch, "Item count")] != "1" {
				t.Errorf("%s vSource coverage=%v", name, row)
			}
		case "down":
			if status != "failed" || !strings.Contains(row[columnIndex(t, ch, "Error")], "no ServiceInstance About record") {
				t.Errorf("down vSource coverage=%v", row)
			}
		}
	}
	if len(seen) != 3 {
		t.Errorf("vSource coverage rows for %v, want all three contexts", seen)
	}
}
