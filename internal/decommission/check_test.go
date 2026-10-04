package decommission

import (
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func boolPtr(value bool) *bool { return &value }

func baseData(vm vsphere.VM) assessment.ExportData {
	collections := make([]assessment.CollectionRun, 0, 7)
	for _, kind := range []string{"vm", "host", "cluster", "resourcepool", "vapp", "dvswitch", "datastore", "network", "snapshot"} {
		collections = append(collections, assessment.CollectionRun{Kind: kind, Status: "empty"})
	}
	return assessment.ExportData{
		Run:      assessment.Run{ID: 42, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion},
		Contexts: []assessment.ContextRun{{Name: "prod", VMStatus: "success", Collections: collections}},
		VMs:      []assessment.ExportVM{{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-prod", VM: vm}}},
	}
}

func findCheck(report SubjectReport, id string) Check {
	for _, check := range report.Checks {
		if check.ID == id {
			return check
		}
	}
	return Check{}
}

func TestEvaluateNoBlockersAndMetadataAdvisories(t *testing.T) {
	report := Evaluate(baseData(vsphere.VM{ID: "vm-1", Name: "app", PowerState: "poweredOff", ConnectionState: "connected", ConfigurationAvailable: true}), "app", nil)
	if report.Verdict != VerdictNoBlockers || len(report.Subjects) != 1 {
		t.Fatalf("report = %#v", report)
	}
	for _, id := range []string{"ownership", "backup-policy", "application-dependencies"} {
		if check := findCheck(report.Subjects[0], id); check.Status != StatusNotAssessed || check.Impact != ImpactAdvisory {
			t.Fatalf("%s check = %#v", id, check)
		}
	}
	if report.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %d", report.SchemaVersion)
	}
}

func TestEvaluateStrictBlockers(t *testing.T) {
	cases := []struct {
		name string
		vm   vsphere.VM
		id   string
	}{
		{name: "powered on", vm: vsphere.VM{PowerState: "poweredOn", ConnectionState: "connected", ConfigurationAvailable: true}, id: "power-state"},
		{name: "snapshot", vm: vsphere.VM{PowerState: "poweredOff", ConnectionState: "connected", ConfigurationAvailable: true, Snapshots: []vsphere.VMSnapshot{{ID: "snap-1", Name: "before-change"}}}, id: "snapshots"},
		{name: "cdrom", vm: vsphere.VM{PowerState: "poweredOff", ConnectionState: "connected", ConfigurationAvailable: true, CDROMs: []vsphere.VMCDROM{{Label: "installer", Connected: boolPtr(true)}}}, id: "mounted-media"},
		{name: "usb", vm: vsphere.VM{PowerState: "poweredOff", ConnectionState: "connected", ConfigurationAvailable: true, USBs: []vsphere.VMUSB{{Label: "token", Connected: boolPtr(true)}}}, id: "mounted-media"},
		{name: "inaccessible", vm: vsphere.VM{PowerState: "poweredOff", ConnectionState: "inaccessible", ConfigurationAvailable: true}, id: "connection-state"},
		{name: "orphaned", vm: vsphere.VM{PowerState: "poweredOff", ConnectionState: "orphaned", ConfigurationAvailable: true}, id: "connection-state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.vm.ID = "vm-1"
			tc.vm.Name = "app"
			report := Evaluate(baseData(tc.vm), "app", nil)
			if report.Verdict != VerdictBlocked {
				t.Fatalf("verdict = %s, report = %#v", report.Verdict, report)
			}
			check := findCheck(report.Subjects[0], tc.id)
			if check.Status != StatusFail || check.Impact != ImpactBlocker {
				t.Fatalf("check = %#v", check)
			}
		})
	}
}

func TestEvaluateUnknownCoverageAndIdentity(t *testing.T) {
	vm := vsphere.VM{ID: "vm-1", Name: "app", ConnectionState: "connected", ConfigurationAvailable: true}
	report := Evaluate(baseData(vm), "app", nil)
	if report.Verdict != VerdictUnknown || findCheck(report.Subjects[0], "power-state").Status != StatusUnknown {
		t.Fatalf("missing power evidence report = %#v", report)
	}
	missing := Evaluate(baseData(vm), "does-not-exist", nil)
	if missing.Verdict != VerdictUnknown || findCheck(missing.Subjects[0], "identity").Status != StatusUnknown {
		t.Fatalf("missing identity report = %#v", missing)
	}
}

func TestEvaluateAmbiguityAndStrongCrossContextJoin(t *testing.T) {
	collections := make([]assessment.CollectionRun, 0, 7)
	for _, kind := range []string{"vm", "host", "cluster", "resourcepool", "vapp", "dvswitch", "datastore", "network", "snapshot"} {
		collections = append(collections, assessment.CollectionRun{Kind: kind, Status: "empty"})
	}
	data := assessment.ExportData{
		Run:      assessment.Run{ID: 7, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion},
		Contexts: []assessment.ContextRun{{Name: "edge", VMStatus: "success", Collections: collections}, {Name: "prod", VMStatus: "success", Collections: collections}},
		VMs: []assessment.ExportVM{
			{Observation: assessment.Observation{Context: "edge", VCenterID: "vc-edge", VM: vsphere.VM{ID: "vm-edge", Name: "app", PowerState: "poweredOff", ConnectionState: "connected", ConfigurationAvailable: true}}},
			{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-prod", VM: vsphere.VM{ID: "vm-prod", Name: "app", PowerState: "poweredOff", ConnectionState: "connected", ConfigurationAvailable: true}}},
		},
	}
	ambiguous := Evaluate(data, "app", nil)
	if !ambiguous.Ambiguous || ambiguous.Verdict != VerdictUnknown || len(ambiguous.Subjects) != 2 {
		t.Fatalf("ambiguous report = %#v", ambiguous)
	}
	data.VMs[0].Observation.VM.InstanceUUID = "same-instance"
	data.VMs[1].Observation.VM.InstanceUUID = "same-instance"
	joined := Evaluate(data, "app", nil)
	if joined.Ambiguous || joined.Verdict != VerdictNoBlockers || len(joined.Subjects) != 1 || len(joined.Subjects[0].Subject.Members) != 2 {
		t.Fatalf("joined report = %#v", joined)
	}
	byUUID := Evaluate(data, "same-instance", nil)
	if byUUID.Verdict != VerdictNoBlockers || len(byUUID.Subjects) != 1 {
		t.Fatalf("UUID query report = %#v", byUUID)
	}
}

func TestEvaluateUnresolvedDependenciesAreUnknown(t *testing.T) {
	connected := false
	data := baseData(vsphere.VM{
		ID: "vm-1", Name: "app", PowerState: "poweredOff", ConnectionState: "connected", ConfigurationAvailable: true,
		Disks: []vsphere.VMDisk{{Key: 1, Label: "disk", BackingPath: "[missing-ds] app/app.vmdk"}},
		NICs:  []vsphere.VMNIC{{Key: 2, Label: "nic", Network: "missing-network", Connected: &connected}},
	})
	report := Evaluate(data, "app", nil)
	if report.Verdict != VerdictUnknown {
		t.Fatalf("verdict = %s, report = %#v", report.Verdict, report)
	}
	if check := findCheck(report.Subjects[0], "datastore-dependencies"); check.Status != StatusUnknown {
		t.Fatalf("datastore dependency check = %#v", check)
	}
	if check := findCheck(report.Subjects[0], "network-relationships"); check.Status != StatusUnknown {
		t.Fatalf("network relationship check = %#v", check)
	}
}
