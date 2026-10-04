package report

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// vappExport adds one vApp, holding the sample's VM, to the sample run.
func vappExport(t *testing.T, schema string, collection *assessment.CollectionRun) assessment.ExportData {
	t.Helper()
	data := sampleExportData(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	data.Run.InventorySchemaVersion = schema
	collections := data.Contexts[0].Collections[:0]
	for _, c := range data.Contexts[0].Collections {
		if c.Kind != "vapp" {
			collections = append(collections, c)
		}
	}
	if collection != nil {
		collections = append(collections, *collection)
	}
	data.Contexts[0].Collections = collections
	if collection == nil || collection.Status != "success" {
		return data
	}
	payload, _ := json.Marshal(vsphere.VApp{
		Location: vsphere.Location{Datacenter: "dc-a", Path: "/dc-a/host/cluster-1/Resources/shop"},
		ID:       "resgroup-v1", Name: "shop", Status: "started", OverallStatus: "green", ConfigStatus: "green",
		DirectVMCount: 1, DirectVMs: []string{"app"}, DirectVMRefs: []string{"VirtualMachine:vm-1"},
		Allocation: &vsphere.ResourceAllocation{CPULimitMHz: int64Ptr(1000), CPUReservationMHz: int64Ptr(0), CPUExpandable: true, CPULevel: "normal", CPUShares: 4000,
			MemConfiguredMB: 512, MemLimitMB: int64Ptr(-1), MemReservationMB: int64Ptr(128), MemExpandable: true, MemLevel: "normal", MemShares: 163840},
	})
	data.Resources = append(data.Resources, assessment.ResourceObservation{Context: "prod", VCenterID: "vc-uuid", Kind: "vapp", ID: "resgroup-v1", Name: "shop", Payload: payload})
	return data
}

func exportWorkbook(t *testing.T, data assessment.ExportData) *excelize.File {
	t.Helper()
	var out bytes.Buffer
	if err := WriteRVTools(&out, data, healthReport(data)); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// vRPCoverage returns the status, count and message of the vRP coverage row.
func vRPCoverage(t *testing.T, f *excelize.File) (string, string, string) {
	t.Helper()
	rows, err := f.GetRows("vsfleetCoverage")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows[1:] {
		if len(r) > 9 && r[9] == "vRP" {
			for len(r) < 13 {
				r = append(r, "")
			}
			return r[10], r[11], r[12]
		}
	}
	t.Fatal("no vRP coverage row")
	return "", "", ""
}

func TestVRPListsVAppsAfterResourcePools(t *testing.T) {
	f := exportWorkbook(t, vappExport(t, assessment.CurrentInventorySchemaVersion, &assessment.CollectionRun{Kind: "vapp", Status: "success", ItemCount: 1}))
	rows, err := f.GetRows("vRP")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("vRP rows=%d, want header, pool and vApp:\n%v", len(rows), rows)
	}
	header, vapp := rows[0], rows[2]
	got := map[string]string{}
	for i, h := range header {
		if i < len(vapp) {
			got[h] = vapp[i]
		}
	}
	want := map[string]string{
		"Resource pool": "/dc-a/host/cluster-1/Resources/shop", "Name": "shop", "Status": "green", "VMs": "1", "vCPUs": "2",
		"CPU limit": "1000", "CPU reservation": "0", "CPU level": "normal", "CPU shares": "4000", "CPU expandableReservation": "TRUE",
		"Mem Configured": "512", "Mem limit": "-1", "Mem reservation": "128", "Mem shares": "163840", "Config status": "green", "Object ID": "resgroup-v1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("vRP vApp %s=%q, want %q", k, got[k], v)
		}
	}
	if status, count, message := vRPCoverage(t, f); status != "success" || count != "2" || message != "" {
		t.Errorf("vRP coverage=%q/%q/%q, want success/2 with no message", status, count, message)
	}
}

func TestVRPCoverageSaysWhenVAppsAreNotListed(t *testing.T) {
	cases := []struct {
		name, schema       string
		collection         *assessment.CollectionRun
		status, wantPrefix string
	}{
		{"capture predates vApps", "19", nil, "success", "capture predates vApp inventory"},
		{"vApp collection failed", assessment.CurrentInventorySchemaVersion, &assessment.CollectionRun{Kind: "vapp", Status: "failed", Error: "permission denied"}, "partial", "vApps are not listed: permission denied"},
		{"vApp collection missing", assessment.CurrentInventorySchemaVersion, nil, "partial", "vApps were not collected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := exportWorkbook(t, vappExport(t, tc.schema, tc.collection))
			status, count, message := vRPCoverage(t, f)
			if status != tc.status || count != "1" || len(message) < len(tc.wantPrefix) || message[:len(tc.wantPrefix)] != tc.wantPrefix {
				t.Errorf("vRP coverage=%q/%q/%q, want %q/1/%q...", status, count, message, tc.status, tc.wantPrefix)
			}
		})
	}
}
