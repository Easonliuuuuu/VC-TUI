package rvimport

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// FuzzParseWorkbook starts from a real workbook written by vsfleet's own
// RVTools writer and overwrites a block of one worksheet with arbitrary
// cells, header row included. Parse is the boundary for files an operator
// imports from elsewhere: it must return an error or a result, never panic,
// and given the same workbook it must report the same thing twice.
func FuzzParseWorkbook(f *testing.F) {
	base, err := os.ReadFile(writeFixtureWorkbook(f, nil))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(uint8(0), uint8(1), "web-01\tpoweredOn\t2\t4096")
	f.Add(uint8(0), uint8(0), "VM\tPowerstate\nweb-02\tpoweredOff")
	f.Add(uint8(1), uint8(1), "\t\t\t\t\t\t\t\t")
	f.Add(uint8(2), uint8(2), "-1\t1e400\tNaN\t2026-13-45 99:99")
	f.Add(uint8(3), uint8(0), "")
	f.Add(uint8(4), uint8(1), "dup\tdup\ndup\tdup")
	f.Fuzz(func(t *testing.T, sheetIndex, startRow uint8, cells string) {
		if len(cells) > 4096 {
			cells = cells[:4096]
		}
		book, err := excelize.OpenReader(bytes.NewReader(base))
		if err != nil {
			t.Fatal(err)
		}
		defer book.Close()
		sheets := book.GetSheetList()
		sheet := sheets[int(sheetIndex)%len(sheets)]
		// Row 1 is the header, so start rows 0 and 1 both rewrite it; larger
		// values leave the header intact and replace data rows.
		row := 1 + int(startRow)%4
		for _, line := range strings.Split(cells, "\n") {
			for col, value := range strings.Split(line, "\t") {
				cell, err := excelize.CoordinatesToCellName(col+1, row)
				if err != nil {
					t.Fatal(err)
				}
				if err := book.SetCellStr(sheet, cell, value); err != nil {
					t.Skip("excelize rejects the cell value")
				}
			}
			row++
		}
		var mutated bytes.Buffer
		if err := book.Write(&mutated); err != nil {
			t.Fatal(err)
		}

		opts := Options{SourceLabel: "fuzz.xlsx", CapturedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Timezone: time.UTC}
		parse := func() (*Result, error) {
			wb, err := excelize.OpenReader(bytes.NewReader(mutated.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			defer wb.Close()
			return Parse(wb, opts)
		}
		first, err := parse()
		if err != nil {
			return
		}
		second, err := parse()
		if err != nil {
			t.Fatalf("second parse of an accepted workbook failed: %v", err)
		}
		if !reflect.DeepEqual(first.Report, second.Report) {
			t.Fatalf("parse is not deterministic:\n%+v\n%+v", first.Report, second.Report)
		}
	})
}
