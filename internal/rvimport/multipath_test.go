package rvimport

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Built independently of vsfleet's exporter with the vMultiPath headers
// observed in the RVTools 4.8.1.4 workbook reported in #268. All inventory
// values are synthetic; the real lab workbook is not shipped as a fixture.
func TestSyntheticRVTools48Multipath(t *testing.T) {
	for _, tc := range []struct {
		name, host, uuid, objectID, dc, cluster, display, warning string
		duplicate, ambiguity                                      bool
	}{
		{name: "datastore object ID", host: "esx-shared", uuid: "synthetic-alpha", objectID: "datastore-14", display: "Synthetic disk"},
		{name: "object ID cannot choose another host", host: "esx-shared", uuid: "synthetic-beta", objectID: "host-1", display: "Synthetic disk"},
		{name: "blank object ID", host: "esx-shared", uuid: "synthetic-alpha", display: "Synthetic disk"},
		{name: "disk fallback", host: "esx-shared", uuid: "synthetic-alpha", objectID: "datastore-14"},
		{name: "duplicate host name", host: "esx-shared", uuid: "synthetic-alpha", objectID: "host-1", duplicate: true, ambiguity: true, warning: "ambiguous"},
		{name: "datacenter narrows duplicate", host: "esx-shared", uuid: "synthetic-alpha", objectID: "datastore-14", duplicate: true, dc: "dc-a"},
		{name: "cluster narrows duplicate", host: "esx-shared", uuid: "synthetic-alpha", objectID: "datastore-14", duplicate: true, cluster: "cluster-a"},
		{name: "missing host", host: "esx-missing", uuid: "synthetic-alpha", objectID: "host-1", warning: "no matching host"},
		{name: "blank host", uuid: "synthetic-alpha", objectID: "host-1", warning: "no matching host"},
		{name: "different vCenter UUID", host: "esx-shared", uuid: "synthetic-missing", objectID: "datastore-14", warning: "no matching host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := excelize.NewFile()
			t.Cleanup(func() { _ = f.Close() })
			hosts := [][]any{
				{"Host", "Object ID", "Datacenter", "Cluster", "# Cores", "# Memory", "Speed", "VI SDK Server", "VI SDK UUID"},
				{"esx-shared", "host-1", "dc-a", "cluster-a", 8, 65536, 2400, "vc-alpha.example", "synthetic-alpha"},
				{"esx-shared", "host-2", "dc-b", "cluster-b", 8, 65536, 2400, "vc-beta.example", "synthetic-beta"},
			}
			if tc.duplicate {
				hosts = append(hosts, []any{"esx-shared", "host-3", "dc-other", "cluster-other", 8, 65536, 2400, "vc-alpha.example", "synthetic-alpha"})
			}
			headers := []any{"Host", "Cluster", "Datacenter", "Datastore", "Disk", "Display name", "Policy", "Oper. State"}
			// A different endpoint spelling proves that the UUID scopes the join.
			row := []any{tc.host, tc.cluster, tc.dc, "Synthetic datastore", "naa.synthetic", tc.display, "FIXED", "ok"}
			for i := 1; i <= 8; i++ {
				headers = append(headers, fmt.Sprintf("Path %d", i), fmt.Sprintf("Path %d state", i))
				path, state := "", ""
				switch i {
				case 1, 2, 3, 4, 8:
					path = fmt.Sprintf("vmhba0:C0:T0:L%d", i)
					state = map[int]string{1: "active", 2: "standby", 3: "dead", 4: "disabled", 8: "ACTIVE"}[i]
				}
				row = append(row, path, state)
			}
			for _, h := range []string{"vStorage", "Queue depth", "Vendor", "Model", "Revision", "Level", "Serial #", "UUID", "Object ID", "VI SDK Server", "VI SDK UUID"} {
				headers = append(headers, h)
				row = append(row, map[string]string{"Object ID": tc.objectID, "VI SDK Server": "vc-alias.example", "VI SDK UUID": tc.uuid}[h])
			}
			for _, sheet := range []struct {
				name string
				rows [][]any
			}{
				{sheetVInfo, [][]any{
					{"VM", "VM ID", "CPUs", "Memory", "VI SDK Server", "VI SDK UUID"},
					{"Synthetic VM", "vm-1", 2, 4096, "vc-alpha.example", "synthetic-alpha"},
				}},
				{sheetVHost, hosts},
				{sheetVMultiPath, [][]any{headers, row}},
			} {
				if _, err := f.NewSheet(sheet.name); err != nil {
					t.Fatal(err)
				}
				for i, values := range sheet.rows {
					if err := f.SetSheetRow(sheet.name, fmt.Sprintf("A%d", i+1), &values); err != nil {
						t.Fatal(err)
					}
				}
			}
			path := filepath.Join(t.TempDir(), "synthetic-rvtools-4.8.1.4.xlsx")
			if err := f.SaveAs(path); err != nil {
				t.Fatal(err)
			}
			result, run, store := importFixture(t, openFixture(t, path), Options{CapturedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)})
			resources, err := store.Resources(context.Background(), run.ID, kindHost)
			if err != nil {
				t.Fatal(err)
			}
			if len(resources) != len(hosts)-1 {
				t.Fatalf("persisted %d hosts, want %d", len(resources), len(hosts)-1)
			}
			wantID := "host-1"
			if tc.uuid == "synthetic-beta" {
				wantID = "host-2"
			}
			for _, resource := range resources {
				var host vsphere.Host
				if err := json.Unmarshal(resource.Payload, &host); err != nil {
					t.Fatal(err)
				}
				if tc.warning != "" || host.ID != wantID {
					if len(host.Multipaths) != 0 {
						t.Errorf("multipath attached to host %s: %+v", host.ID, host.Multipaths)
					}
					continue
				}
				lun := tc.display
				if lun == "" {
					lun = "naa.synthetic"
				}
				want := []vsphere.HostMultipath{{LUN: lun, DevicePath: "naa.synthetic", Policy: "FIXED", PathCount: 5, Active: 2, Standby: 1, Dead: 1, Disabled: 1}}
				if !reflect.DeepEqual(host.Multipaths, want) {
					t.Errorf("host %s multipaths = %+v, want %+v", host.ID, host.Multipaths, want)
				}
			}
			if tc.warning != "" && !containsSubstring(result.Report.Warnings, tc.warning) {
				t.Errorf("warnings = %v, want %q", result.Report.Warnings, tc.warning)
			}
			if (len(result.Report.Ambiguities) != 0) != tc.ambiguity {
				t.Errorf("ambiguities = %+v, want ambiguity %v", result.Report.Ambiguities, tc.ambiguity)
			}
			for _, sheet := range result.Report.Sheets {
				if sheet.Name != sheetVMultiPath {
					continue
				}
				if contains(sheet.RecognizedColumns, "Object ID") || !contains(sheet.IgnoredColumns, "Object ID") {
					t.Errorf("datastore Object ID must not be read as a host identity: %+v", sheet)
				}
				for _, h := range []string{"Host", "Disk", "Display name", "Path 8", "Path 8 state"} {
					if !contains(sheet.RecognizedColumns, h) {
						t.Errorf("%s not recognized: %+v", h, sheet)
					}
				}
				if contains(sheet.MissingColumns, "LUN") || contains(sheet.MissingColumns, "Object ID") || contains(sheet.MissingColumns, "Path count") {
					t.Errorf("RVTools layout reports vsfleet columns missing: %+v", sheet)
				}
			}
		})
	}
}

func TestMultipathAggregateExportRoundTrip(t *testing.T) {
	local := true
	want := []vsphere.HostMultipath{{LUN: "Synthetic LUN", DevicePath: "/vmfs/devices/disks/naa.synthetic", Policy: "VMW_PSP_RR", LocalDisk: &local, PathCount: 12, Active: 8, Standby: 2, Dead: 1, Disabled: 1, WorkingPaths: 7}}
	path := writeFixtureWorkbook(t, func(data *assessment.ExportData) {
		editHost(t, data, "host-a1", func(host *vsphere.Host) { host.Multipaths = want })
	})
	_, run, store := importFixture(t, openFixture(t, path), Options{})
	resources, err := store.Resources(context.Background(), run.ID, kindHost)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, resource := range resources {
		var host vsphere.Host
		if err := json.Unmarshal(resource.Payload, &host); err != nil {
			t.Fatal(err)
		}
		if host.ID == "host-a1" {
			found = true
			if !reflect.DeepEqual(host.Multipaths, want) {
				t.Errorf("aggregate multipaths = %+v, want %+v", host.Multipaths, want)
			}
		}
	}
	if !found {
		t.Fatal("aggregate host was not persisted")
	}
}
