package health

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func orphanResource(t *testing.T, context, vc string, datastore vsphere.Datastore) assessment.ResourceObservation {
	t.Helper()
	payload, err := json.Marshal(datastore)
	if err != nil {
		t.Fatal(err)
	}
	return assessment.ResourceObservation{Context: context, VCenterID: vc, Kind: "datastore", ID: datastore.ID, Name: datastore.Name, Payload: payload}
}

func completeOrphanContext(name string) assessment.ContextRun {
	return assessment.ContextRun{Name: name, VMStatus: "success", Collections: []assessment.CollectionRun{{Kind: "vm", Status: "success"}, {Kind: "datastore", Status: "success"}}}
}

func TestOrphansClassifiesSharedStorageAndBothSnapshotDirections(t *testing.T) {
	finish := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	prod := vsphere.Datastore{Location: vsphere.Location{Context: "prod", Datacenter: "dc-a"}, ID: "ds-prod", Name: "datastore1", Backing: vsphere.DatastoreBacking{VMFSUUID: "uuid-1", Extents: []string{"naa.6000"}}, BrowseStatus: "success", Files: []vsphere.DatastoreFile{
		{Path: "[datastore1] app/disk.vmdk"},
		{Path: "[datastore1] app/disk-000002.vmdk"},
		{Path: "[datastore1] lost/orphan.vmdk"},
		{Path: "[datastore1] lost/orphan-flat.vmdk"},
	}}
	edge := vsphere.Datastore{Location: vsphere.Location{Context: "edge", Datacenter: "dc-b"}, ID: "ds-edge", Name: "other-name", Backing: vsphere.DatastoreBacking{Extents: []string{"naa.6000"}}, BrowseStatus: "success"}
	data := assessment.ExportData{
		Run:      assessment.Run{ID: 1, InventorySchemaVersion: "11", FinishedAt: finish},
		Contexts: []assessment.ContextRun{completeOrphanContext("prod"), completeOrphanContext("edge")},
		VMs: []assessment.ExportVM{
			{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-prod", VM: vsphere.VM{Name: "snapshot-vm", Disks: []vsphere.VMDisk{{BackingPath: "[datastore1] app/disk-000002.vmdk"}}}}},
			{Observation: assessment.Observation{Context: "edge", VCenterID: "vc-edge", VM: vsphere.VM{Name: "finance", Disks: []vsphere.VMDisk{{BackingPath: "[other-name] lost/orphan.vmdk"}}}}},
		},
		Resources: []assessment.ResourceObservation{orphanResource(t, "prod", "vc-prod", prod), orphanResource(t, "edge", "vc-edge", edge)},
	}
	report := Orphans(data)
	byPath := make(map[string]OrphanEvidence, len(report.Entries))
	for _, entry := range report.Entries {
		byPath[entry.Path] = entry
	}
	if _, ok := byPath["[datastore1] app/disk.vmdk"]; ok {
		t.Fatal("snapshot base was incorrectly classified as an orphan")
	}
	if _, ok := byPath["[datastore1] app/disk-000002.vmdk"]; ok {
		t.Fatal("snapshot delta was incorrectly classified as an orphan")
	}
	orphan, ok := byPath["[datastore1] lost/orphan.vmdk"]
	if !ok || orphan.Confidence != ConfidenceOtherContext {
		t.Fatalf("shared orphan=%+v, want referenced-other-context", orphan)
	}
}

func TestAssessDatastoreFilePreservesReferenceAndUnknownCoverage(t *testing.T) {
	ds := vsphere.Datastore{Location: vsphere.Location{Context: "prod"}, ID: "ds-1", Name: "datastore1", Backing: vsphere.DatastoreBacking{VMFSUUID: "uuid-1"}, BrowseStatus: "success", Files: []vsphere.DatastoreFile{{Path: "[datastore1] app/app.vmdk"}}}
	data := assessment.ExportData{
		Run:       assessment.Run{ID: 9, InventorySchemaVersion: "11"},
		Contexts:  []assessment.ContextRun{completeOrphanContext("prod")},
		VMs:       []assessment.ExportVM{{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-prod", VM: vsphere.VM{ID: "vm-1", Name: "app", Disks: []vsphere.VMDisk{{BackingPath: "[datastore1] app/app.vmdk"}}}}}},
		Resources: []assessment.ResourceObservation{orphanResource(t, "prod", "vc-prod", ds)},
	}
	got := AssessDatastoreFile(data, ds, "[datastore1] app/app.vmdk")
	if !got.Observed || got.Confidence != ConfidenceReferenced || len(got.ReferencedBy) != 1 || got.ReferencedBy[0].VMID != "vm-1" {
		t.Fatalf("assessment=%+v, want referenced VM evidence", got)
	}
	data.VMs = nil
	got = AssessDatastoreFile(data, ds, "[datastore1] app/app.vmdk")
	if got.Confidence != ConfidenceVerified {
		t.Fatalf("unreferenced assessment=%+v, want verified", got)
	}
	ds.BrowseTruncated = true
	data.Resources[0] = orphanResource(t, "prod", "vc-prod", ds)
	got = AssessDatastoreFile(data, ds, "[datastore1] app/app.vmdk")
	if got.Confidence != ConfidenceUnknown || len(got.Reasons) == 0 {
		t.Fatalf("truncated assessment=%+v, want unknown coverage", got)
	}
}

func TestOrphansDowngradesTruncatedAndBlindCoverage(t *testing.T) {
	finish := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	truncated := vsphere.Datastore{Location: vsphere.Location{Context: "prod"}, ID: "ds-1", Name: "prod", Backing: vsphere.DatastoreBacking{VMFSUUID: "uuid-1"}, BrowseStatus: "success", BrowseTruncated: true, Files: []vsphere.DatastoreFile{{Path: "[prod] orphan.vmdk"}}}
	blind := vsphere.Datastore{Location: vsphere.Location{Context: "blind"}, ID: "ds-2", Name: "blind", Backing: vsphere.DatastoreBacking{VMFSUUID: "uuid-2"}, BrowseStatus: "success", Files: []vsphere.DatastoreFile{{Path: "[blind] orphan.vmdk"}}}
	data := assessment.ExportData{Run: assessment.Run{ID: 2, InventorySchemaVersion: "11", FinishedAt: finish}, Contexts: []assessment.ContextRun{completeOrphanContext("prod"), {Name: "blind", VMStatus: "failed", Collections: []assessment.CollectionRun{{Kind: "vm", Status: "failed"}, {Kind: "datastore", Status: "success"}}}}, Resources: []assessment.ResourceObservation{orphanResource(t, "prod", "vc-prod", truncated), orphanResource(t, "blind", "vc-blind", blind)}}
	report := Orphans(data)
	for _, entry := range report.Entries {
		if entry.Confidence != ConfidenceUnknown {
			t.Errorf("%s confidence=%s, want unknown", entry.Path, entry.Confidence)
		}
	}
	if len(report.Entries) != 2 {
		t.Fatalf("entries=%d, want 2", len(report.Entries))
	}
}

func TestOrphansCoverageReportsBrowseState(t *testing.T) {
	finish := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	base := func(status string) assessment.ExportData {
		return assessment.ExportData{
			Run:      assessment.Run{ID: 7, InventorySchemaVersion: "11", FinishedAt: finish},
			Contexts: []assessment.ContextRun{completeOrphanContext("prod")},
			Resources: []assessment.ResourceObservation{orphanResource(t, "prod", "vc-prod", vsphere.Datastore{
				Location: vsphere.Location{Context: "prod"}, ID: "ds-1", Name: "datastore1",
				Backing: vsphere.DatastoreBacking{VMFSUUID: "uuid-1"}, BrowseStatus: status,
			})},
		}
	}

	t.Run("no browse", func(t *testing.T) {
		cov := Orphans(base("")).Coverage
		if cov.Complete() || cov.Browsed != 0 || len(cov.Gaps) != 1 || cov.Gaps[0].Status != OrphanScanNotBrowsed {
			t.Fatalf("coverage=%+v", cov)
		}
	})
	t.Run("failed browse", func(t *testing.T) {
		data := base("failed")
		var ds vsphere.Datastore
		_ = json.Unmarshal(data.Resources[0].Payload, &ds)
		ds.BrowseError = "permission denied"
		data.Resources[0].Payload, _ = json.Marshal(ds)
		cov := Orphans(data).Coverage
		if cov.Complete() || len(cov.Gaps) != 1 || cov.Gaps[0].Status != OrphanScanFailed || cov.Gaps[0].Reason != "permission denied" {
			t.Fatalf("coverage=%+v", cov)
		}
	})
	t.Run("denied browse", func(t *testing.T) {
		cov := Orphans(base("denied")).Coverage
		if cov.Complete() || len(cov.Gaps) != 1 || cov.Gaps[0].Status != OrphanScanDenied {
			t.Fatalf("coverage=%+v", cov)
		}
	})
	t.Run("complete clean browse", func(t *testing.T) {
		cov := Orphans(base("success")).Coverage
		if !cov.Complete() || cov.Browsed != 1 || len(cov.Gaps) != 0 {
			t.Fatalf("coverage=%+v", cov)
		}
	})
}

func TestOrphansCoveragePartialAndTruncatedBrowse(t *testing.T) {
	finish := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	browsed := vsphere.Datastore{Location: vsphere.Location{Context: "prod"}, ID: "ds-1", Name: "browsed", Backing: vsphere.DatastoreBacking{VMFSUUID: "uuid-1"}, BrowseStatus: "success"}
	truncated := vsphere.Datastore{Location: vsphere.Location{Context: "prod"}, ID: "ds-2", Name: "truncated", Backing: vsphere.DatastoreBacking{VMFSUUID: "uuid-2"}, BrowseStatus: "success", BrowseTruncated: true, Files: []vsphere.DatastoreFile{{Path: "[truncated] lost/orphan.vmdk"}}}
	unbrowsed := vsphere.Datastore{Location: vsphere.Location{Context: "edge"}, ID: "ds-3", Name: "unbrowsed", Backing: vsphere.DatastoreBacking{VMFSUUID: "uuid-3"}}
	data := assessment.ExportData{
		Run:       assessment.Run{ID: 8, InventorySchemaVersion: "11", FinishedAt: finish},
		Contexts:  []assessment.ContextRun{completeOrphanContext("prod"), completeOrphanContext("edge")},
		Resources: []assessment.ResourceObservation{orphanResource(t, "prod", "vc-prod", browsed), orphanResource(t, "prod", "vc-prod", truncated), orphanResource(t, "edge", "vc-edge", unbrowsed)},
	}
	cov := Orphans(data).Coverage
	if cov.Complete() {
		t.Fatalf("partial estate reported complete: %+v", cov)
	}
	if cov.Datastores != 3 || cov.Browsed != 2 {
		t.Fatalf("counts=%+v", cov)
	}
	byName := make(map[string]OrphanScanStatus, len(cov.Gaps))
	for _, gap := range cov.Gaps {
		byName[gap.Object.Name] = gap.Status
	}
	if byName["truncated"] != OrphanScanTruncated || byName["unbrowsed"] != OrphanScanNotBrowsed || len(cov.Gaps) != 2 {
		t.Fatalf("gaps=%+v", cov.Gaps)
	}
}

func TestOrphansDoesNotCrossSuppressSameNameDatastores(t *testing.T) {
	left := vsphere.Datastore{Location: vsphere.Location{Context: "left"}, ID: "ds-left", Name: "datastore1", Backing: vsphere.DatastoreBacking{Extents: []string{"naa.left"}}, BrowseStatus: "success", Files: []vsphere.DatastoreFile{{Path: "[datastore1] orphan.vmdk"}}}
	right := vsphere.Datastore{Location: vsphere.Location{Context: "right"}, ID: "ds-right", Name: "datastore1", Backing: vsphere.DatastoreBacking{Extents: []string{"naa.right"}}, BrowseStatus: "success", Files: []vsphere.DatastoreFile{{Path: "[datastore1] orphan.vmdk"}}}
	data := assessment.ExportData{Run: assessment.Run{InventorySchemaVersion: "11"}, Contexts: []assessment.ContextRun{completeOrphanContext("left"), completeOrphanContext("right")}, Resources: []assessment.ResourceObservation{orphanResource(t, "left", "vc-left", left), orphanResource(t, "right", "vc-right", right)}}
	if got := len(Orphans(data).Entries); got != 2 {
		t.Fatalf("same-name independent datastores produced %d entries, want 2", got)
	}
}

func TestOrphanSizeLabelDistinguishesUnknownFromSize(t *testing.T) {
	if got := OrphanSizeLabel(0); got != "size unknown" {
		t.Fatalf("zero size label=%q", got)
	}
	if got := OrphanSizeLabel(16 << 20); got == "size unknown" || got == "-" {
		t.Fatalf("real size label=%q", got)
	}
}
