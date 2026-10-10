package demo

import (
	"context"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/health"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func TestNewBackendIncludesOrphanAndZombieFixtures(t *testing.T) {
	backend := NewBackend()
	inv := backend.inventories["prod-vc"]
	if inv == nil {
		t.Fatal("prod-vc demo inventory is missing")
	}
	foundOrphan := false
	for _, vm := range inv.VMs {
		foundOrphan = foundOrphan || vm.ConnectionState == "orphaned"
	}
	if !foundOrphan {
		t.Fatal("demo inventory has no orphaned VM")
	}
	var nvme *vsphere.Datastore
	for i := range inv.Datastores {
		if inv.Datastores[i].Name == "nvme-01" {
			nvme = &inv.Datastores[i]
		}
	}
	if nvme == nil || nvme.BrowseStatus != "success" {
		t.Fatalf("demo datastore browse provenance=%+v", nvme)
	}
	foundZombie := false
	for _, file := range nvme.Files {
		if file.Path == "[nvme-01] lost+found/orphan.vmdk" {
			foundZombie = true
		}
	}
	if !foundZombie {
		t.Fatal("demo inventory has no zombie VMDK fixture")
	}
}

func TestAssessmentServiceSeedsNewHealthFindingsInMemory(t *testing.T) {
	service, closeStore, err := NewBackend().AssessmentService()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	runID, err := service.Store.ResolveRun(context.Background(), "latest")
	if err != nil {
		t.Fatal(err)
	}
	data, err := service.Store.LoadExportData(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	report := health.Evaluate(data, health.Options{Thresholds: health.DefaultThresholds()})
	found := map[string]bool{}
	for _, finding := range report.Findings {
		found[finding.Rule] = true
	}
	if !found["vm-orphaned"] || !found["datastore-zombie-vmdk"] {
		t.Fatalf("demo health findings=%v", found)
	}
	orphans := health.Orphans(data)
	confidence := map[health.Confidence]bool{}
	for _, entry := range orphans.Entries {
		confidence[entry.Confidence] = true
	}
	if !confidence[health.ConfidenceVerified] || !confidence[health.ConfidenceOtherContext] {
		t.Fatalf("demo orphan confidence=%v entries=%+v", confidence, orphans.Entries)
	}
}

// TestDemoVMEventsAgreeWithStoredHistory keeps the synthetic event log honest:
// every event sits inside the thirty days the demo's vCenters keep, oldest
// first, and every snapshot inside that window has the task that took it, so
// the timeline's Combined tab has real matches to show.
func TestDemoVMEventsAgreeWithStoredHistory(t *testing.T) {
	b := NewBackend()
	cc := b.contexts[0]
	matched := 0
	for _, vm := range b.estates[cc.Name].inv.VMs {
		listing, err := b.VMEvents(context.Background(), cc, vm.ID, 0)
		if err != nil {
			t.Fatalf("%s: %v", vm.Name, err)
		}
		for i, ev := range listing.Events {
			if ev.Context != cc.Name || !ev.Time.After(demoNow.Add(-eventRetention)) || ev.Time.After(demoNow) {
				t.Fatalf("%s event %+v is outside retention or misattributed", vm.Name, ev)
			}
			if i > 0 && ev.Time.Before(listing.Events[i-1].Time) {
				t.Fatalf("%s events are not oldest first", vm.Name)
			}
		}
		for _, s := range vm.Snapshots {
			if !s.CreateTime.After(demoNow.Add(-eventRetention)) {
				continue
			}
			found := false
			for _, ev := range listing.Events {
				found = found || (ev.Explains == vsphere.EventSnapshotCreated && ev.Time.Equal(s.CreateTime))
			}
			if !found {
				t.Fatalf("%s snapshot %q has no create task in its event log", vm.Name, s.Name)
			}
			matched++
		}
	}
	if matched == 0 {
		t.Fatal("no demo snapshot falls inside event retention; the Combined tab would have nothing to match")
	}
	if _, err := b.VMEvents(context.Background(), b.contexts[2], "vm-1", 0); err == nil {
		t.Fatal("the unreachable demo vCenter answered an events read")
	}
}
