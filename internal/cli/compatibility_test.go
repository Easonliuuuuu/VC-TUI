package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func runCompatibilityReport(t *testing.T, args ...string) (*App, string, string, error) {
	t.Helper()
	var in, out, errOut bytes.Buffer
	a := &App{In: &in, Out: &out, Err: &errOut}
	root := NewRootCommand(a)
	root.SetArgs(append([]string{"compatibility", "report"}, args...))
	err := root.ExecuteContext(context.Background())
	return a, out.String(), errOut.String(), err
}

func TestCompatibilityReportIsRegistered(t *testing.T) {
	root := NewRootCommand(&App{})
	cmd, _, err := root.Find([]string{"compatibility", "report"})
	if err != nil {
		t.Fatalf("finding the compatibility report command: %v", err)
	}
	if cmd.Name() != "report" {
		t.Fatalf("expected the report command, got %q", cmd.Name())
	}
}

// The report describes the export profile itself, so it must answer without a
// vCenter, without configuration and without a keyring — the same promise the
// demo makes, measured the same way: every side-effecting dependency on App is
// a lazy accessor, so each one still being nil proves none of it was touched.
func TestCompatibilityReportLoadsNothingFromTheMachine(t *testing.T) {
	scratchPaths(t)
	keyring.MockInit()

	a, out, _, err := runCompatibilityReport(t)
	if err != nil {
		t.Fatalf("compatibility report: %v", err)
	}
	if out == "" {
		t.Fatal("compatibility report produced no output")
	}
	if a.cfg != nil {
		t.Error("the report loaded the configuration file")
	}
	if a.resolver != nil {
		t.Error("the report built the credential resolver")
	}
	if a.mgr != nil {
		t.Error("the report built the session manager")
	}
	if a.history != nil {
		t.Error("the report opened the history database")
	}
}

func TestCompatibilityReportCoversEveryWorksheet(t *testing.T) {
	_, out, _, err := runCompatibilityReport(t)
	if err != nil {
		t.Fatalf("compatibility report: %v", err)
	}
	for _, sheet := range []string{"vInfo", "vCPU", "vMemory", "vDisk", "vPartition", "vNetwork", "vCD", "vUSB", "vSnapshot", "vTools", "vRP", "vCluster", "vHost", "vHBA", "vNIC", "vSwitch", "vPort", "dvSwitch", "dvPort", "vSC_VMK", "vDatastore", "vMultiPath", "vFileInfo", "vHealth", "vsfleetCoverage"} {
		if !strings.Contains(out, sheet) {
			t.Errorf("report does not mention the %s worksheet", sheet)
		}
	}
}

func TestCompatibilityReportJSONIsMachineReadable(t *testing.T) {
	var in, out, errOut bytes.Buffer
	a := &App{In: &in, Out: &out, Err: &errOut}
	root := NewRootCommand(a)
	root.SetArgs([]string{"-o", "json", "compatibility", "report", "--sheet", "vDatastore"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("compatibility report: %v", err)
	}
	var sheets []struct {
		Sheet       string `json:"sheet"`
		RVToolsName bool   `json:"rvtools_named"`
		DerivesFrom string `json:"derives_from"`
		Columns     []struct {
			Column string `json:"column"`
			Kind   string `json:"kind"`
			Unit   string `json:"unit"`
		} `json:"columns"`
	}
	if err := json.Unmarshal(out.Bytes(), &sheets); err != nil {
		t.Fatalf("decoding the report: %v\n%s", err, out.String())
	}
	if len(sheets) != 1 || sheets[0].Sheet != "vDatastore" {
		t.Fatalf("--sheet did not narrow the report: %+v", sheets)
	}
	if !sheets[0].RVToolsName || sheets[0].DerivesFrom == "" {
		t.Errorf("vDatastore is missing its provenance: %+v", sheets[0])
	}
	var capacity bool
	for _, col := range sheets[0].Columns {
		if col.Column == "Capacity MiB" {
			capacity = true
			if col.Kind == "" || col.Unit != "MiB" {
				t.Errorf("Capacity MiB is described as %+v", col)
			}
		}
	}
	if !capacity {
		t.Error("vDatastore does not describe its Capacity MiB column")
	}
}

// vsfleetCoverage is vsfleet's own worksheet. The report must say so rather
// than letting a reader assume every tab in the workbook is an RVTools layout.
func TestCompatibilityReportMarksTheVsfleetExtension(t *testing.T) {
	_, out, _, err := runCompatibilityReport(t, "--sheet", "vsfleetCoverage")
	if err != nil {
		t.Fatalf("compatibility report: %v", err)
	}
	if !strings.Contains(out, "vsfleet extension") {
		t.Errorf("vsfleetCoverage is not marked as a vsfleet extension:\n%s", out)
	}
}

func TestCompatibilityReportRejectsAnUnknownWorksheet(t *testing.T) {
	_, _, _, err := runCompatibilityReport(t, "--sheet", "vMetaData")
	if err == nil {
		t.Fatal("expected an error for a worksheet this profile does not write")
	}
	// The message must name what is available, so the answer to "is vMetaData
	// in here?" is settled by the error itself.
	if !strings.Contains(err.Error(), "vInfo") {
		t.Errorf("the error does not list the worksheets that exist: %v", err)
	}
}
