//go:build integration

package tests

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
)

// TestVCSIMSharedExportPseudonymizesDuplicateNames exports the two identical
// vc-a and vc-b estates through scoped sharing profiles with pseudonymization
// and proves that the workbook keeps both estates (every row of both contexts
// is present and distinguishable by its pseudonymized context), that none of
// the real names, addresses or the key appear anywhere in the workbook, that
// disks still join to their VMs per context, and that two invocations produce
// byte-identical files.
func TestVCSIMSharedExportPseudonymizesDuplicateNames(t *testing.T) {
	fixture := fixtureDuplicateNames(t)
	r := newRunner(t)
	addFixtureContexts(t, r, fixture)
	historyDB := filepath.Join(t.TempDir(), "history.db")
	if run, _ := captureVCSIM(t, r, historyDB, false); run.Status != assessment.RunComplete {
		t.Fatalf("duplicate-name capture=%+v", run)
	}
	dir := t.TempDir()
	const key = "vsfleet-test-key-0123456789abcdef"
	keyFile := filepath.Join(dir, "share.key")
	if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Real values that must not survive pseudonymization: generated object
	// names shared by both estates, the context names, and the loopback
	// address the endpoints listen on.
	originals := []string{"DC0_C0_RP0_VM", "DC0_C0_H", "DC0_C0", "DC0", "LocalDS_", "vc-a", "vc-b", "127.0.0.1", key}

	export := func(profile, name string) []byte {
		t.Helper()
		file := filepath.Join(dir, name)
		stdout, stderr, err := r.run("", "--history-db", historyDB, "assessment", "export", "--profile", profile,
			"--pseudonymize", "--pseudonymize-key-file", keyFile, "--file", file)
		if err != nil {
			t.Fatalf("export %s: %v\nstdout:\n%s\nstderr:\n%s", profile, err, stdout, stderr)
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}

	for _, profile := range []string{"sizing-summary", "full-inventory"} {
		first, second := export(profile, profile+"-1.xlsx"), export(profile, profile+"-2.xlsx")
		if !bytes.Equal(first, second) {
			t.Fatalf("%s: two exports of the same run and key differ (%d vs %d bytes)", profile, len(first), len(second))
		}

		zr, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range zr.File {
			rc, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			for _, original := range originals {
				if bytes.Contains(body, []byte(original)) {
					t.Fatalf("%s: workbook part %s leaks %q", profile, entry.Name, original)
				}
			}
		}

		book, err := excelize.OpenReader(bytes.NewReader(first))
		if err != nil {
			t.Fatal(err)
		}
		column := func(rows [][]string, header string) int {
			t.Helper()
			for i, name := range rows[0] {
				if name == header {
					return i
				}
			}
			t.Fatalf("%s: no %q column in %v", profile, header, rows[0])
			return -1
		}
		info, err := book.GetRows("vInfo")
		if err != nil {
			t.Fatal(err)
		}
		disks, err := book.GetRows("vDisk")
		if err != nil {
			t.Fatal(err)
		}
		book.Close()

		// Each estate has four VMs (two in the pool, two in the vApp) and one
		// disk per VM, so both contexts together give eight rows plus a header.
		if len(info) != 9 || len(disks) != 9 {
			t.Fatalf("%s: vInfo rows=%d vDisk rows=%d, want a header and eight rows each (4 VMs x 2 contexts): %v", profile, len(info), len(disks), info)
		}
		vmCol, ctxCol := column(info, "VM"), column(info, "vsfleet Context")
		pairs, contexts := map[string]bool{}, map[string]bool{}
		for _, row := range info[1:] {
			pairs[row[vmCol]+"|"+row[ctxCol]] = true
			contexts[row[ctxCol]] = true
		}
		if len(pairs) != 8 || len(contexts) != 2 {
			t.Fatalf("%s: %d distinct (VM, context) rows over %d contexts, want 8 rows over 2 pseudonymized contexts: %v", profile, len(pairs), len(contexts), info)
		}
		for ctxToken := range contexts {
			if !strings.HasPrefix(ctxToken, "ctx-") {
				t.Fatalf("%s: context %q is not pseudonymized", profile, ctxToken)
			}
		}
		diskVMCol, diskCtxCol := column(disks, "VM"), column(disks, "vsfleet Context")
		for _, row := range disks[1:] {
			if !pairs[row[diskVMCol]+"|"+row[diskCtxCol]] {
				t.Fatalf("%s: vDisk row %v does not join to a vInfo (VM, context) row", profile, row)
			}
		}
	}
}
