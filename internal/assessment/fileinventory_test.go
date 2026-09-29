package assessment

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func inventoryResource(t *testing.T, ctx, name string, inv *vsphere.DatastoreFileInventory) ResourceObservation {
	t.Helper()
	payload, err := json.Marshal(vsphere.Datastore{ID: "id-" + name, Name: name, Accessible: true, FileInventory: inv})
	if err != nil {
		t.Fatal(err)
	}
	return ResourceObservation{Context: ctx, Kind: "datastore", ID: "id-" + name, Name: name, Payload: payload}
}

func TestFileInventoryCoverageOnlyCallsFullyListedContextsComplete(t *testing.T) {
	complete := &vsphere.DatastoreFileInventory{Status: vsphere.FileInventorySuccess, Files: []vsphere.DatastoreInventoryFile{{Path: "[a] f", Type: "FileInfo", SizeBytes: 1}}}
	data := ExportData{
		Contexts: []ContextRun{{Name: "all-good"}, {Name: "mixed"}, {Name: "none"}, {Name: "empty"}, {Name: "asked-not-answered"}},
		Resources: []ResourceObservation{
			inventoryResource(t, "all-good", "a", complete),
			inventoryResource(t, "mixed", "z-trunc", &vsphere.DatastoreFileInventory{Status: vsphere.FileInventorySuccess, Truncated: true, Files: complete.Files}),
			inventoryResource(t, "mixed", "a-ok", complete),
			inventoryResource(t, "mixed", "m-skip", &vsphere.DatastoreFileInventory{Status: vsphere.FileInventorySkipped, Error: "budget"}),
			inventoryResource(t, "none", "n", nil),
			inventoryResource(t, "empty", "e", &vsphere.DatastoreFileInventory{Status: vsphere.FileInventorySuccess}),
			// A stored inventory with no recorded status is a gap, not a success.
			inventoryResource(t, "asked-not-answered", "q", &vsphere.DatastoreFileInventory{}),
		},
	}
	got := map[string]FileInventoryContext{}
	for _, c := range FileInventoryCoverage(data) {
		got[c.Context] = c
	}
	for ctx, want := range map[string]string{"all-good": "success", "mixed": "partial", "none": "not recorded", "empty": "empty", "asked-not-answered": "failed"} {
		if status, _ := got[ctx].Summary(); status != want {
			t.Errorf("%s summary = %q, want %q", ctx, status, want)
		}
	}
	mixed := got["mixed"]
	if names := []string{mixed.Datastores[0].Datastore, mixed.Datastores[1].Datastore, mixed.Datastores[2].Datastore}; strings.Join(names, ",") != "a-ok,m-skip,z-trunc" {
		t.Errorf("datastores not in name order: %v", names)
	}
	if _, msg := mixed.Summary(); !strings.Contains(msg, "z-trunc truncated") || !strings.Contains(msg, "m-skip skipped") || !strings.Contains(msg, "NOT evidence") {
		t.Errorf("mixed message = %q", msg)
	}
	if got["none"].Requested || !HasFileInventory(data) {
		t.Errorf("requested flags wrong: none=%v", got["none"].Requested)
	}
	if HasFileInventory(ExportData{Contexts: []ContextRun{{Name: "x"}}, Resources: []ResourceObservation{inventoryResource(t, "x", "d", nil)}}) {
		t.Error("a run without inventory records reports the capture as opted in")
	}
}

func TestInventoryDatastoreStatusTruncationIsNotComplete(t *testing.T) {
	status, msg := InventoryDatastoreStatus(&vsphere.DatastoreFileInventory{Status: vsphere.FileInventorySuccess, Truncated: true, TruncatedReason: "datastore file limit", Files: make([]vsphere.DatastoreInventoryFile, 3)})
	if status != FileInventoryTruncated || !strings.Contains(msg, "stopped at 3 files") {
		t.Fatalf("status=%q msg=%q", status, msg)
	}
	if status, _ := InventoryDatastoreStatus(nil); status != FileInventoryNotRecorded {
		t.Fatalf("nil inventory status = %q", status)
	}
}
