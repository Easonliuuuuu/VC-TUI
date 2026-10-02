package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/metareport"
	"github.com/easonliuuuuu/vsfleet/internal/metareport/metareporttest"
)

func newMetadataTestHistory(t *testing.T) (dir, dbPath string) {
	t.Helper()
	dir = t.TempDir()
	dbPath = filepath.Join(dir, "history.db")
	s, err := assessment.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	metareporttest.Seed(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, dbPath
}

func runMetadataCLI(t *testing.T, dir, dbPath string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &App{HistoryPath: dbPath, Out: &out, Err: &errOut}
	root := NewRootCommand(a)
	root.SetArgs(append([]string{"--history-db", dbPath, "--config", filepath.Join(dir, "config.toml")}, args...))
	defer func() { _ = a.Close(context.Background()) }()
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestAssessmentMetadataCSVHasFixedHeaderAndWarnsAboutGaps(t *testing.T) {
	dir, db := newMetadataTestHistory(t)
	out, stderr, err := runMetadataCLI(t, dir, db, "assessment", "metadata", metareporttest.BaseLabel, "--format", "csv")
	if err != nil {
		t.Fatalf("%v (stderr=%s)", err, stderr)
	}
	if first := strings.SplitN(out, "\n", 2)[0]; first != strings.Join(metareport.RowColumns, ",") {
		t.Fatalf("header=%q", first)
	}
	if !strings.Contains(out, ",tag,,,,,denied,403 Forbidden: synthetic tagging denial") {
		t.Fatalf("denied source has no status row:\n%s", out)
	}
	if !strings.Contains(stderr, "warning: synthetic-west vm: tags partial") || !strings.Contains(stderr, "1 denied") {
		t.Fatalf("stderr=%s", stderr)
	}
	_, _, err = runMetadataCLI(t, dir, db, "assessment", "metadata", metareporttest.BaseLabel, "--format", "csv", "--fail-on-incomplete")
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) || coded.ExitCode() != 3 {
		t.Fatalf("--fail-on-incomplete err=%v", err)
	}
}

func TestAssessmentMetadataReportRunsSavedDefinitionByName(t *testing.T) {
	dir, db := newMetadataTestHistory(t)
	if err := os.MkdirAll(filepath.Join(dir, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reports", "pci.toml"), []byte(metareporttest.PCIReport), 0o600); err != nil {
		t.Fatal(err)
	}
	first, _, err := runMetadataCLI(t, dir, db, "-o", "json", "assessment", "metadata-report", "pci", metareporttest.BaseLabel)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _ := runMetadataCLI(t, dir, db, "-o", "json", "assessment", "metadata-report", "pci", metareporttest.BaseLabel)
	if first != second {
		t.Fatal("repeated report output differs")
	}
	var result metareport.Result
	if err := json.Unmarshal([]byte(first), &result); err != nil {
		t.Fatal(err)
	}
	if result.RunLabel != metareporttest.BaseLabel || result.CapturedAt.IsZero() || len(result.Members) != 1 || len(result.Undetermined) != 1 || result.Complete {
		t.Fatalf("result=%+v", result)
	}

	table, _, err := runMetadataCLI(t, dir, db, "assessment", "metadata-report", "pci", "--base", metareporttest.BaseLabel)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"added", "removed", "unknown", "comparison INCOMPLETE"} {
		if !strings.Contains(table, want) {
			t.Fatalf("comparison table lacks %q:\n%s", want, table)
		}
	}
}

func TestAssessmentExportMetadataSheetIsOptInAndShareAware(t *testing.T) {
	dir, db := newMetadataTestHistory(t)
	sheets := func(path string) []string {
		f, err := excelize.OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		return f.GetSheetList()
	}
	plain := filepath.Join(dir, "plain.xlsx")
	if _, stderr, err := runMetadataCLI(t, dir, db, "assessment", "export", "--file", plain); err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	if strings.Contains(strings.Join(sheets(plain), ","), "vsfleetMetadata") {
		t.Fatal("default export gained the metadata sheet")
	}
	with := filepath.Join(dir, "with.xlsx")
	if _, stderr, err := runMetadataCLI(t, dir, db, "assessment", "export", "--include-metadata", "--file", with); err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	if got := sheets(with); got[len(got)-1] != "vsfleetMetadata" {
		t.Fatalf("sheets=%v", got)
	}

	out, stderr, err := runMetadataCLI(t, dir, db, "-o", "json", "assessment", "export", "--include-metadata", "--profile", "sizing-summary", "--preview")
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	if !strings.Contains(out, `"vsfleetMetadata"`) || strings.Contains(out, `"name": "vsfleetMetadata"`) {
		t.Fatalf("sizing-summary must list vsfleetMetadata as omitted, not included:\n%s", out)
	}
	keyFile := filepath.Join(dir, "share.key")
	if err := os.WriteFile(keyFile, []byte("synthetic-share-key-0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(dir, "shared.xlsx")
	if _, stderr, err := runMetadataCLI(t, dir, db, "assessment", "export", "--include-metadata", "--profile", "full-inventory", "--pseudonymize", "--pseudonymize-key-file", keyFile, "--file", shared); err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	f, err := excelize.OpenFile(shared)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := f.GetRows("vsfleetMetadata")
	if err != nil || len(rows) < 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	for _, row := range rows[1:] {
		for _, cell := range row {
			if strings.Contains(cell, "synthetic-alice") || strings.Contains(cell, "PCI") || cell == "web" {
				t.Fatalf("pseudonymized metadata sheet leaked %q in %v", cell, row)
			}
		}
	}
}
