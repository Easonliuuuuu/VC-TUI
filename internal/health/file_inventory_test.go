package health

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// The opt-in file inventory (vFileInfo) is a separate capture from the VMDK
// browse evidence. It must never feed orphan or health conclusions: a
// datastore whose inventory listed nothing, or was denied, skipped or
// truncated, is exactly as unknown as before, and a fully listed inventory
// must not promote an unbrowsed datastore to "evaluated".
func TestFileInventoryNeverChangesOrphanOrHealthConclusions(t *testing.T) {
	finish := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	files := []vsphere.DatastoreInventoryFile{{Path: "[datastore1] lost/orphan.vmdk", Type: "VmDiskFileInfo", SizeBytes: 100}}
	for name, inv := range map[string]*vsphere.DatastoreFileInventory{
		"complete":  {Status: vsphere.FileInventorySuccess, Files: files},
		"empty":     {Status: vsphere.FileInventorySuccess},
		"denied":    {Status: vsphere.FileInventoryDenied, Error: "denied"},
		"skipped":   {Status: vsphere.FileInventorySkipped},
		"truncated": {Status: vsphere.FileInventorySuccess, Truncated: true, Files: files},
	} {
		for _, browse := range []string{"", "denied", "success"} {
			build := func(withInventory bool) assessment.ExportData {
				ds := vsphere.Datastore{Location: vsphere.Location{Context: "prod", Datacenter: "dc-a"}, ID: "ds-1", Name: "datastore1", Backing: vsphere.DatastoreBacking{VMFSUUID: "uuid-1"}, BrowseStatus: browse}
				if withInventory {
					ds.FileInventory = inv
				}
				return assessment.ExportData{
					Run:       assessment.Run{ID: 3, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion, FinishedAt: finish},
					Contexts:  []assessment.ContextRun{completeOrphanContext("prod")},
					Resources: []assessment.ResourceObservation{orphanResource(t, "prod", "vc-prod", ds)},
				}
			}
			with, without := build(true), build(false)
			a, _ := json.Marshal(Orphans(with))
			b, _ := json.Marshal(Orphans(without))
			if string(a) != string(b) {
				t.Errorf("inventory %s (browse %q) changed the orphan report:\n with:    %s\n without: %s", name, browse, a, b)
			}
			ha, _ := json.Marshal(Evaluate(with, Options{Thresholds: DefaultThresholds()}))
			hb, _ := json.Marshal(Evaluate(without, Options{Thresholds: DefaultThresholds()}))
			if string(ha) != string(hb) {
				t.Errorf("inventory %s (browse %q) changed the health report", name, browse)
			}
			if browse == "" {
				cov := Orphans(with).Coverage
				if cov.Complete() || cov.Browsed != 0 || len(cov.Gaps) != 1 || cov.Gaps[0].Status != OrphanScanNotBrowsed {
					t.Errorf("inventory %s made an unbrowsed datastore look evaluated: %+v", name, cov)
				}
			}
		}
	}
}
