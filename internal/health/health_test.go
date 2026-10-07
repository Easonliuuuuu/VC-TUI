package health

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func healthFixture(schema string, finish time.Time) assessment.ExportData {
	hostDisconnected, _ := json.Marshal(vsphere.Host{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "host-2", Name: "esx-2", ConnectionState: "disconnected"})
	hostMaintenance, _ := json.Marshal(vsphere.Host{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "host-1", Name: "esx-1", ConnectionState: "connected", InMaintenance: true})
	dsLow, _ := json.Marshal(vsphere.Datastore{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "ds-1", Name: "slow", Accessible: true, CapacityBytes: 100, FreeBytes: 9})
	dsBad, _ := json.Marshal(vsphere.Datastore{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "ds-2", Name: "broken", Accessible: false, CapacityBytes: 100, FreeBytes: 50})
	created := finish.Add(-31 * 24 * time.Hour)
	return assessment.ExportData{
		Run:      assessment.Run{ID: 42, InventorySchemaVersion: schema, FinishedAt: finish},
		Contexts: []assessment.ContextRun{{Name: "prod", Datacenter: "dc-a", FinishedAt: finish}},
		VMs: []assessment.ExportVM{
			{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-1", VM: vsphere.VM{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "vm-2", Name: "not-installed", PowerState: "poweredOn", ToolsState: "guestToolsNotRunning", ToolsVersionStatus: "guestToolsNotInstalled", Partitions: []vsphere.VMPartition{{Path: "/", CapacityBytes: 100, FreeBytes: 9}}}}, Snapshots: []vsphere.VMSnapshot{{ID: "snap-2", Name: "pre-patch", CreateTime: created}}},
			{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-1", VM: vsphere.VM{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "vm-1", Name: "outdated", PowerState: "poweredOn", ToolsState: "guestToolsRunning", ToolsVersionStatus: "guestToolsNeedUpgrade"}}},
		},
		Resources: []assessment.ResourceObservation{
			{Context: "prod", VCenterID: "vc-1", Kind: "host", ID: "host-1", Name: "esx-1", Payload: hostMaintenance},
			{Context: "prod", VCenterID: "vc-1", Kind: "datastore", ID: "ds-2", Name: "broken", Payload: dsBad},
			{Context: "prod", VCenterID: "vc-1", Kind: "host", ID: "host-2", Name: "esx-2", Payload: hostDisconnected},
			{Context: "prod", VCenterID: "vc-1", Kind: "datastore", ID: "ds-1", Name: "slow", Payload: dsLow},
		},
	}
}

func TestEvaluateInitialRules(t *testing.T) {
	data := healthFixture("5", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	report := Evaluate(data, Options{Thresholds: Thresholds{SnapshotAge: 30 * 24 * time.Hour, DatastoreFreePct: 10, GuestDiskFreePct: 10}})
	got := make(map[string]int)
	for _, finding := range report.Findings {
		got[finding.Rule]++
	}
	want := map[string]int{
		"datastore-inaccessible": 1,
		"datastore-space-low":    1,
		"guest-disk-space-low":   1,
		"host-disconnected":      1,
		"host-in-maintenance":    1,
		"snapshot-age":           1,
		"tools-not-installed":    1,
		"tools-not-running":      1,
		"tools-outdated":         1,
	}
	for id, count := range want {
		if got[id] != count {
			t.Errorf("%s findings=%d, want %d", id, got[id], count)
		}
	}
	if report.Counts.Total != 9 || report.Counts.Info != 1 || report.Counts.Warning != 6 || report.Counts.Critical != 2 {
		t.Fatalf("counts=%+v", report.Counts)
	}
}

// An empty ConnectionState or ToolsState is missing evidence — a source
// that never collects the field, such as an imported RVTools workbook —
// not a reported bad state. Neither rule has a MinSchema gate to fall back
// on, so each has to make this distinction itself.
func TestHostDisconnectedAndToolsNotRunningTreatEmptyStateAsUnknown(t *testing.T) {
	hostUnknown, _ := json.Marshal(vsphere.Host{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "host-3", Name: "esx-3"})
	data := assessment.ExportData{
		Run:      assessment.Run{ID: 1, InventorySchemaVersion: "2"},
		Contexts: []assessment.ContextRun{{Name: "prod", Datacenter: "dc-a"}},
		VMs: []assessment.ExportVM{
			{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-1", VM: vsphere.VM{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "vm-1", Name: "imported-vm", PowerState: "poweredOn"}}},
		},
		Resources: []assessment.ResourceObservation{
			{Context: "prod", VCenterID: "vc-1", Kind: "host", ID: "host-3", Name: "esx-3", Payload: hostUnknown},
		},
	}
	report := Evaluate(data, Options{Thresholds: DefaultThresholds()})
	for _, finding := range report.Findings {
		if finding.Rule == "host-disconnected" || finding.Rule == "tools-not-running" {
			t.Errorf("rule %q fired on missing evidence alone: %+v", finding.Rule, finding)
		}
	}
}

func TestEvaluateMarksFailedCollectionUnknown(t *testing.T) {
	data := assessment.ExportData{
		Run:      assessment.Run{ID: 50, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion},
		Contexts: []assessment.ContextRun{{Name: "unreachable", VMStatus: "success", Collections: []assessment.CollectionRun{{Kind: "vm", Status: "success"}, {Kind: "datastore", Status: "failed", Error: "permission denied"}}}},
	}
	report := Evaluate(data, Options{Thresholds: DefaultThresholds()})
	for _, status := range report.Rules {
		if status.Rule == "datastore-space-low" {
			if status.Result != "unknown" || len(status.Blind) != 1 || status.Blind[0] != "unreachable" {
				t.Fatalf("datastore coverage status=%+v", status)
			}
			return
		}
	}
	t.Fatal("datastore-space-low status was not reported")
}

func TestEvaluateMigrationReadinessRules(t *testing.T) {
	trueValue := true
	sharedStorage := false
	hostOne, _ := json.Marshal(vsphere.Host{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "host-1", Name: "esx-1", Cluster: "cluster-a", VSwitches: []vsphere.HostVSwitch{{Name: "vSwitch0", MTU: 1500, Uplinks: []string{"vmnic0"}}}, Multipaths: []vsphere.HostMultipath{{LUN: "naa.1", LocalDisk: &sharedStorage, PathCount: 1, Active: 1}}})
	hostTwo, _ := json.Marshal(vsphere.Host{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "host-2", Name: "esx-2", Cluster: "cluster-a", VSwitches: []vsphere.HostVSwitch{{Name: "vSwitch0", MTU: 9000, Uplinks: []string{"vmnic0", "vmnic1"}}}, PortGroups: []vsphere.HostPortGroup{{Name: "migration", Switch: "vSwitch0", Promiscuous: &trueValue}}})
	dvs, _ := json.Marshal(vsphere.DVSwitch{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "dvs-1", Name: "dvSwitch0", Hosts: []string{"esx-1"}, PortGroups: []vsphere.DVPortGroup{{Name: "migration", Promiscuous: &trueValue}}})
	data := assessment.ExportData{Run: assessment.Run{ID: 51, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion}, Contexts: []assessment.ContextRun{{Name: "prod", VMStatus: "empty", Collections: []assessment.CollectionRun{{Kind: "vm", Status: "empty"}, {Kind: "host", Status: "success"}, {Kind: "dvswitch", Status: "success"}}}}, Resources: []assessment.ResourceObservation{{Context: "prod", VCenterID: "vc-1", Kind: "host", ID: "host-1", Name: "esx-1", Payload: hostOne}, {Context: "prod", VCenterID: "vc-1", Kind: "host", ID: "host-2", Name: "esx-2", Payload: hostTwo}, {Context: "prod", VCenterID: "vc-1", Kind: "dvswitch", ID: "dvs-1", Name: "dvSwitch0", Payload: dvs}}}
	report := Evaluate(data, Options{})
	want := map[string]bool{"cluster-network-inconsistent": false, "dvswitch-host-coverage": false, "host-path-redundancy": false, "portgroup-promiscuous": false, "dvportgroup-promiscuous": false}
	for _, finding := range report.Findings {
		if _, ok := want[finding.Rule]; ok {
			want[finding.Rule] = true
			if finding.Recommendation == "" || len(finding.Evidence) == 0 {
				t.Errorf("%s missing recommendation/evidence: %+v", finding.Rule, finding)
			}
		}
	}
	for rule, found := range want {
		if !found {
			t.Errorf("%s did not fire", rule)
		}
	}
}

func TestDatastoreHostCoverage(t *testing.T) {
	host := func(id, name, cluster string, datastores ...string) assessment.ResourceObservation {
		payload, _ := json.Marshal(vsphere.Host{Location: vsphere.Location{Datacenter: "dc-a"}, ID: id, Name: name, Cluster: cluster, Datastores: datastores})
		return assessment.ResourceObservation{Context: "prod", VCenterID: "vc-1", Kind: "host", ID: id, Name: name, Payload: payload}
	}
	data := func(schema string, hosts ...assessment.ResourceObservation) assessment.ExportData {
		return assessment.ExportData{Run: assessment.Run{ID: 52, InventorySchemaVersion: schema}, Contexts: []assessment.ContextRun{{Name: "prod", VMStatus: "empty", Collections: []assessment.CollectionRun{{Kind: "vm", Status: "empty"}, {Kind: "host", Status: "success"}}}}, Resources: hosts}
	}
	findings := func(report Report) []Finding {
		var out []Finding
		for _, f := range report.Findings {
			if f.Rule == "datastore-host-coverage" {
				out = append(out, f)
			}
		}
		return out
	}
	status := func(report Report) RuleStatus {
		for _, s := range report.Rules {
			if s.Rule == "datastore-host-coverage" {
				return s
			}
		}
		t.Fatal("datastore-host-coverage status was not reported")
		return RuleStatus{}
	}

	hosts := []assessment.ResourceObservation{
		host("host-1", "esx-1", "cluster-a", "shared", "partial", "local-1"),
		host("host-2", "esx-2", "cluster-a", "shared", "partial"),
		host("host-3", "esx-3", "cluster-a", "shared"),
		// A different cluster that does not mount "partial" is not a gap in cluster-a.
		host("host-4", "esx-4", "cluster-b", "shared"),
	}
	report := Evaluate(data(assessment.CurrentInventorySchemaVersion, hosts...), Options{})
	got := findings(report)
	if len(got) != 1 {
		t.Fatalf("findings=%+v, want one for the partially mounted datastore", got)
	}
	f := got[0]
	if f.Object.Kind != "cluster" || f.Object.Name != "cluster-a" || f.Severity != SeverityWarning || f.Category != CategoryAvailability {
		t.Errorf("finding object/severity = %+v", f)
	}
	if !strings.Contains(f.Message, `"partial"`) || !strings.Contains(f.Message, "not on esx-3") || f.Recommendation == "" {
		t.Errorf("finding should name the datastore and the host without it: %+v", f)
	}
	if s := status(report); s.Status != "evaluated" || s.Result != "fail" {
		t.Errorf("status=%+v, want evaluated fail", s)
	}

	// A cluster whose hosts carry no mounts was not read: no finding.
	unread := Evaluate(data(assessment.CurrentInventorySchemaVersion, host("host-1", "esx-1", "cluster-a"), host("host-2", "esx-2", "cluster-a")), Options{})
	if got := findings(unread); len(got) != 0 {
		t.Errorf("unread mounts produced findings: %+v", got)
	}

	// Runs from before host mounts were captured cannot pass the rule.
	old := Evaluate(data("20", hosts...), Options{})
	if got := findings(old); len(got) != 0 {
		t.Errorf("schema 20 produced findings: %+v", got)
	}
	if s := status(old); s.Status != "not-evaluated" || s.Result != "unknown" {
		t.Errorf("schema 20 status=%+v, want not-evaluated unknown", s)
	}
}

func TestDVPortGroupPromiscuousSkipsUplinkPortGroups(t *testing.T) {
	trueValue := true
	dvs, _ := json.Marshal(vsphere.DVSwitch{Location: vsphere.Location{Datacenter: "dc-a"}, ID: "dvs-1", Name: "dvSwitch0", PortGroups: []vsphere.DVPortGroup{
		{Name: "dvSwitch0-DVUplinks-1", Uplink: true, ForgedTransmits: &trueValue},
		{Name: "migration", Promiscuous: &trueValue},
	}})
	data := assessment.ExportData{Run: assessment.Run{ID: 52, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion}, Contexts: []assessment.ContextRun{{Name: "prod", VMStatus: "empty", Collections: []assessment.CollectionRun{{Kind: "vm", Status: "empty"}, {Kind: "dvswitch", Status: "success"}}}}, Resources: []assessment.ResourceObservation{{Context: "prod", VCenterID: "vc-1", Kind: "dvswitch", ID: "dvs-1", Name: "dvSwitch0", Payload: dvs}}}
	var flagged []string
	for _, finding := range Evaluate(data, Options{}).Findings {
		if finding.Rule != "dvportgroup-promiscuous" {
			continue
		}
		for _, ev := range finding.Evidence {
			if ev.Field == "port_group" {
				flagged = append(flagged, ev.Observed)
			}
		}
	}
	if len(flagged) != 1 || flagged[0] != "migration" {
		t.Fatalf("flagged port groups = %v, want [migration]", flagged)
	}
}

func TestEvaluateHostPathRedundancyLocality(t *testing.T) {
	local, shared := true, false
	cases := []struct {
		name                      string
		localDisk                 *bool
		pathCount, dead, disabled int
		wantResult                string
		findings                  int
	}{
		{name: "local single path", localDisk: &local, pathCount: 1, wantResult: "pass"},
		{name: "shared single path", localDisk: &shared, pathCount: 1, wantResult: "fail", findings: 1},
		{name: "healthy shared multipath", localDisk: &shared, pathCount: 2, wantResult: "pass"},
		{name: "shared dead path", localDisk: &shared, pathCount: 2, dead: 1, wantResult: "fail", findings: 1},
		{name: "shared disabled path", localDisk: &shared, pathCount: 2, disabled: 1, wantResult: "fail", findings: 1},
		{name: "unknown locality", pathCount: 1, wantResult: "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hostPayload, _ := json.Marshal(vsphere.Host{ID: "host-1", Name: "esx-1", Multipaths: []vsphere.HostMultipath{{LUN: "naa.1", LocalDisk: tc.localDisk, PathCount: tc.pathCount, Dead: tc.dead, Disabled: tc.disabled}}})
			data := assessment.ExportData{Run: assessment.Run{ID: 52, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion}, Contexts: []assessment.ContextRun{{Name: "prod", Collections: []assessment.CollectionRun{{Kind: "host", Status: "success"}}}}, Resources: []assessment.ResourceObservation{{Context: "prod", Kind: "host", ID: "host-1", Name: "esx-1", Payload: hostPayload}}}
			report := Evaluate(data, Options{})
			var status RuleStatus
			for _, candidate := range report.Rules {
				if candidate.Rule == "host-path-redundancy" {
					status = candidate
					break
				}
			}
			if status.Result != tc.wantResult || status.Findings != tc.findings {
				t.Fatalf("status=%+v, want result=%q findings=%d", status, tc.wantResult, tc.findings)
			}
		})
	}
}

func TestEvaluateHostPathRedundancyUnknownDoesNotMaskSharedFailure(t *testing.T) {
	shared := false
	hostPayload, _ := json.Marshal(vsphere.Host{ID: "host-1", Name: "esx-1", Multipaths: []vsphere.HostMultipath{{LUN: "naa-unknown", PathCount: 1}, {LUN: "naa-shared", LocalDisk: &shared, PathCount: 1}}})
	data := assessment.ExportData{Run: assessment.Run{ID: 53, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion}, Contexts: []assessment.ContextRun{{Name: "prod", Collections: []assessment.CollectionRun{{Kind: "host", Status: "success"}}}}, Resources: []assessment.ResourceObservation{{Context: "prod", Kind: "host", ID: "host-1", Name: "esx-1", Payload: hostPayload}}}
	report := Evaluate(data, Options{})
	for _, status := range report.Rules {
		if status.Rule == "host-path-redundancy" {
			if status.Result != "fail" || status.Findings != 1 {
				t.Fatalf("status=%+v, want confirmed shared failure", status)
			}
			return
		}
	}
	t.Fatal("host-path-redundancy status missing")
}

func TestEvaluateHostPathRedundancySchemaGate(t *testing.T) {
	shared := false
	hostPayload, _ := json.Marshal(vsphere.Host{ID: "host-1", Name: "esx-1", Multipaths: []vsphere.HostMultipath{{LUN: "naa.1", LocalDisk: &shared, PathCount: 1}}})
	data := assessment.ExportData{Run: assessment.Run{ID: 54, InventorySchemaVersion: "13"}, Contexts: []assessment.ContextRun{{Name: "prod", Collections: []assessment.CollectionRun{{Kind: "host", Status: "success"}}}}, Resources: []assessment.ResourceObservation{{Context: "prod", Kind: "host", ID: "host-1", Name: "esx-1", Payload: hostPayload}}}
	report := Evaluate(data, Options{})
	for _, status := range report.Rules {
		if status.Rule == "host-path-redundancy" {
			if status.Status != "not-evaluated" || status.Result != "unknown" {
				t.Fatalf("status=%+v, want schema-gated unknown", status)
			}
			return
		}
	}
	t.Fatal("host-path-redundancy status missing")
}

func TestEvaluateExpandedMigrationReadiness(t *testing.T) {
	secure, locked, manualTopology := true, true, false
	reservation, limit := int64(128), int64(500)
	unlimited := int64(-1)
	problem := vsphere.VM{
		Location: vsphere.Location{Context: "prod", Datacenter: "dc-a"},
		ID:       "vm-problem", Name: "problem", ConfigurationAvailable: true, CPU: 4, Firmware: "bios", CoresPerSocket: 2,
		AutoCoresPerSocket: &manualTopology, SecureBootEnabled: &secure,
		CPUAllocation:                &vsphere.VMResourceAllocation{Reservation: &reservation, Limit: &limit},
		MemoryAllocation:             &vsphere.VMResourceAllocation{Reservation: &reservation, Limit: &unlimited},
		MemoryReservationLockedToMax: &locked,
		ManagedBy:                    &vsphere.VMManagedBy{ExtensionKey: "com.example", Type: "appliance"},
		Disks: []vsphere.VMDisk{
			{Key: 1, Label: "RDM", Raw: true, BackingType: "rdm", RawLUNID: "naa.1", RawCompatibilityMode: "physicalMode"},
			{Key: 2, Label: "shared", UUID: "shared-uuid", Sharing: "sharingMultiWriter", SharedBus: "physicalSharing"},
		},
		NICs:       []vsphere.VMNIC{{Key: 3, Label: "manual", MACAddress: "00:50:56:aa:bb:cc", MACAddressType: "manual"}, {Key: 4, Label: "sriov", Adapter: "SR-IOV"}},
		TPMs:       []vsphere.VMTPM{{Key: 5, Label: "TPM"}},
		PCIDevices: []vsphere.VMPCIDevice{{Key: 6, Label: "GPU", BackingType: "device"}},
		Floppies:   []vsphere.VMFloppy{{Key: 7, Label: "floppy", BackingType: "image", BackingPath: "[ds] problem/boot.img"}},
	}
	peerSecure := false
	peer := vsphere.VM{Location: vsphere.Location{Context: "prod", Datacenter: "dc-a"}, ID: "vm-peer", Name: "peer", ConfigurationAvailable: true, Firmware: "efi", SecureBootEnabled: &peerSecure, CPU: 1, CoresPerSocket: 1,
		CPUAllocation: &vsphere.VMResourceAllocation{Limit: &unlimited}, MemoryAllocation: &vsphere.VMResourceAllocation{Limit: &unlimited},
		Disks: []vsphere.VMDisk{{Key: 2, Label: "shared", UUID: "shared-uuid", Sharing: "sharingMultiWriter"}}}
	data := assessment.ExportData{Run: assessment.Run{ID: 60, InventorySchemaVersion: "13"}, Contexts: []assessment.ContextRun{{Name: "prod", VMStatus: "success", Collections: []assessment.CollectionRun{{Kind: "vm", Status: "success"}, {Kind: "host", Status: "success"}, {Kind: "datastore", Status: "success"}, {Kind: "dvswitch", Status: "success"}}}}, VMs: []assessment.ExportVM{
		{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-1", VM: problem}},
		{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-1", VM: peer}},
	}}
	report := Evaluate(data, Options{})
	got := make(map[string][]Finding)
	for _, finding := range report.Findings {
		got[finding.Rule] = append(got[finding.Rule], finding)
	}
	for _, rule := range []string{"bios-firmware", "custom-cpu-topology", "custom-resource-allocation", "extension-managed-vm", "floppy-present", "manual-mac-address", "secure-boot-enabled", "vtpm-present", "rdm-present", "shared-disk", "host-device-passthrough"} {
		if len(got[rule]) == 0 {
			t.Errorf("expanded rule %s did not fire", rule)
		}
	}
	if len(got["host-device-passthrough"]) != 2 || len(got["shared-disk"]) != 2 {
		t.Errorf("passthrough/shared findings = host %d shared %d", len(got["host-device-passthrough"]), len(got["shared-disk"]))
	}
	readiness := Readiness(report)
	if readiness.Verdict != "blocked" || len(readiness.Blockers) == 0 || len(readiness.Advisories) == 0 || len(readiness.Unresolved) != 0 {
		t.Fatalf("expanded readiness = verdict %q blockers %d advisories %d unresolved %+v", readiness.Verdict, len(readiness.Blockers), len(readiness.Advisories), readiness.Unresolved)
	}
	for _, status := range report.Rules {
		if status.Rule == "bios-firmware" || status.Rule == "custom-cpu-topology" || status.Rule == "custom-resource-allocation" || status.Rule == "extension-managed-vm" || status.Rule == "floppy-present" || status.Rule == "manual-mac-address" || status.Rule == "secure-boot-enabled" {
			if status.Result != "fail" {
				t.Errorf("advisory %s result=%q", status.Rule, status.Result)
			}
		}
	}
}

func TestEvaluateCustomCPUTopology(t *testing.T) {
	auto, manual := true, false
	tests := []struct {
		name           string
		coresPerSocket int32
		autoCores      *bool
		isTemplate     bool
		wantFinding    bool
	}{
		{name: "automatic topology with multiple cores", coresPerSocket: 4, autoCores: &auto},
		{name: "manual topology with multiple cores", coresPerSocket: 4, autoCores: &manual, wantFinding: true},
		{name: "unset automatic flag with multiple cores", coresPerSocket: 4, wantFinding: true},
		{name: "manual topology with one core", coresPerSocket: 1, autoCores: &manual},
		{name: "unset flag with one core", coresPerSocket: 1},
		{name: "template with manual topology", coresPerSocket: 4, autoCores: &manual, isTemplate: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := vsphere.VM{ID: "vm-test", Name: tt.name, CPU: 4, CoresPerSocket: tt.coresPerSocket, AutoCoresPerSocket: tt.autoCores, IsTemplate: tt.isTemplate}
			var findings []Finding
			evaluateCustomCPUTopology(Input{Data: assessment.ExportData{VMs: []assessment.ExportVM{{Observation: assessment.Observation{VM: vm}}}}}, func(f Finding) {
				findings = append(findings, f)
			})
			if got := len(findings) > 0; got != tt.wantFinding {
				t.Fatalf("finding=%t, want %t (findings=%+v)", got, tt.wantFinding, findings)
			}
		})
	}
}

func TestEvaluateHostDevicePassthroughUsesBindingEvidence(t *testing.T) {
	trueValue := true
	falseValue := false
	baseVM := func(id string) vsphere.VM {
		return vsphere.VM{
			Location:               vsphere.Location{Context: "prod", Datacenter: "dc-a"},
			ID:                     id,
			Name:                   id,
			ConfigurationAvailable: true,
			CPU:                    1,
			CoresPerSocket:         1,
			CPUSockets:             1,
			Firmware:               "efi",
			SecureBootEnabled:      &falseValue,
			CPUAllocation:          &vsphere.VMResourceAllocation{},
			MemoryAllocation:       &vsphere.VMResourceAllocation{},
		}
	}
	dataFor := func(vms ...vsphere.VM) assessment.ExportData {
		exportVMs := make([]assessment.ExportVM, 0, len(vms))
		for _, vm := range vms {
			exportVMs = append(exportVMs, assessment.ExportVM{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-1", VM: vm}})
		}
		return assessment.ExportData{
			Run: assessment.Run{ID: 62, InventorySchemaVersion: "15"},
			Contexts: []assessment.ContextRun{{Name: "prod", Datacenter: "dc-a", VMStatus: "success", Collections: []assessment.CollectionRun{
				{Kind: "vm", Status: "success"}, {Kind: "host", Status: "empty"}, {Kind: "cluster", Status: "empty"},
				{Kind: "resourcepool", Status: "empty"}, {Kind: "dvswitch", Status: "empty"}, {Kind: "datastore", Status: "empty"},
			}}},
			VMs: exportVMs,
		}
	}

	uptVM := baseVM("vm-upt")
	uptVM.NICs = []vsphere.VMNIC{{Key: 1, Adapter: "Vmxnet3", UPTCompatible: &trueValue}}
	sriovVM := baseVM("vm-sriov")
	sriovVM.NICs = []vsphere.VMNIC{{Key: 2, Adapter: "SR-IOV"}}
	pciVM := baseVM("vm-pci")
	pciVM.PCIDevices = []vsphere.VMPCIDevice{{Key: 3, Label: "GPU", BackingType: "device"}}
	vgpuVM := baseVM("vm-vgpu")
	vgpuVM.PCIDevices = []vsphere.VMPCIDevice{{Key: 4, Label: "vGPU", BackingType: "vmiop", VGPU: "grid_v100-4q"}}

	report := Evaluate(dataFor(uptVM, sriovVM, pciVM, vgpuVM), Options{})
	var findings []Finding
	for _, finding := range report.Findings {
		if finding.Rule == "host-device-passthrough" {
			findings = append(findings, finding)
		}
	}
	if len(findings) != 3 {
		t.Fatalf("host-device findings = %+v, want SR-IOV, PCI, and vGPU only", findings)
	}
	seen := make(map[string]bool, len(findings))
	for _, finding := range findings {
		seen[finding.Object.ID] = true
	}
	for _, id := range []string{"vm-sriov", "vm-pci", "vm-vgpu"} {
		if !seen[id] {
			t.Errorf("missing host-device finding for %s", id)
		}
	}
	if seen["vm-upt"] {
		t.Errorf("VMXNET3 UPT capability produced a host-device finding")
	}
	vgpuEvidence := false
	for _, finding := range findings {
		if finding.Object.ID != "vm-vgpu" {
			continue
		}
		for _, evidence := range finding.Evidence {
			if evidence.Field == "vgpu" && evidence.Observed == "grid_v100-4q" {
				vgpuEvidence = true
			}
		}
	}
	if !vgpuEvidence {
		t.Errorf("vGPU finding lacks vgpu evidence: %+v", findings)
	}

	readiness := Readiness(Evaluate(dataFor(uptVM), Options{}))
	if readiness.Verdict == "blocked" {
		t.Fatalf("UPT-only VM readiness = %+v, want no blocker", readiness)
	}
}

func TestMigrationReadinessDoesNotTreatAdvisoriesOrMissingConfigurationAsReady(t *testing.T) {
	unlimited := int64(-1)
	advisoryVM := vsphere.VM{Location: vsphere.Location{Context: "prod"}, ID: "vm-1", Name: "legacy", ConfigurationAvailable: true, Firmware: "bios", CPU: 1, CoresPerSocket: 1,
		CPUAllocation: &vsphere.VMResourceAllocation{Limit: &unlimited}, MemoryAllocation: &vsphere.VMResourceAllocation{Limit: &unlimited}}
	base := assessment.ExportData{Run: assessment.Run{ID: 61, InventorySchemaVersion: "13"}, Contexts: []assessment.ContextRun{{Name: "prod", VMStatus: "success", Collections: []assessment.CollectionRun{{Kind: "vm", Status: "success"}, {Kind: "host", Status: "success"}, {Kind: "datastore", Status: "success"}, {Kind: "dvswitch", Status: "success"}, {Kind: "snapshot", Status: "empty"}}}}, VMs: []assessment.ExportVM{{Observation: assessment.Observation{Context: "prod", VM: advisoryVM}}}}
	if got := Readiness(Evaluate(base, Options{})); got.Verdict != "ready" || len(got.Advisories) != 1 {
		t.Fatalf("advisory-only readiness = %+v", got)
	}
	base.VMs[0].Observation.VM.ConfigurationAvailable = false
	unknown := Readiness(Evaluate(base, Options{}))
	if unknown.Verdict != "unknown" || len(unknown.Unresolved) == 0 {
		t.Fatalf("incomplete migration evidence readiness = %+v", unknown)
	}
}

func TestRulesUseStableAlphabeticalOrder(t *testing.T) {
	want := []string{
		"bios-firmware", "cdrom-connected", "cluster-network-inconsistent", "custom-cpu-topology", "custom-resource-allocation", "datastore-host-coverage", "datastore-inaccessible", "datastore-space-low", "datastore-zombie-vmdk",
		"dvportgroup-promiscuous", "dvswitch-host-coverage", "extension-managed-vm", "floppy-present", "guest-disk-space-low", "host-device-passthrough", "host-disconnected", "host-in-maintenance", "host-path-redundancy", "manual-mac-address", "portgroup-promiscuous",
		"rdm-present", "secure-boot-enabled", "shared-disk", "snapshot-age", "tools-not-installed", "tools-not-running", "tools-outdated", "usb-connected", "vm-inaccessible", "vm-orphaned", "vtpm-present",
	}
	rules := Rules()
	if len(rules) != len(want) {
		t.Fatalf("rule count=%d, want %d", len(rules), len(want))
	}
	for i, rule := range rules {
		if rule.ID != want[i] {
			t.Errorf("rule %d=%q, want %q", i, rule.ID, want[i])
		}
	}
}

func TestEvaluateVMConnectionRules(t *testing.T) {
	data := assessment.ExportData{
		Run:      assessment.Run{ID: 45, InventorySchemaVersion: "7"},
		Contexts: []assessment.ContextRun{{Name: "prod", Datacenter: "dc-a"}},
		VMs: []assessment.ExportVM{
			{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-1", VM: vsphere.VM{ID: "vm-orphan", Name: "orphan", ConnectionState: "orphaned"}}},
			{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-1", VM: vsphere.VM{ID: "vm-broken", Name: "broken", ConnectionState: "notResponding"}}},
			{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-1", VM: vsphere.VM{ID: "vm-ok", Name: "ok", ConnectionState: "connected"}}},
			{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-1", VM: vsphere.VM{ID: "tpl", Name: "template", IsTemplate: true, ConnectionState: "orphaned"}}},
		},
	}
	report := Evaluate(data, Options{})
	got := map[string]Finding{}
	for _, finding := range report.Findings {
		if finding.Rule == "vm-inaccessible" || finding.Rule == "vm-orphaned" {
			got[finding.Rule] = finding
		}
	}
	if len(got) != 2 || got["vm-orphaned"].Object.ID != "vm-orphan" || !strings.Contains(got["vm-inaccessible"].Message, "notResponding") {
		t.Fatalf("VM connection findings=%+v", got)
	}
	for _, status := range report.Rules {
		if (status.Rule == "vm-inaccessible" || status.Rule == "vm-orphaned") && status.Status != "evaluated" {
			t.Errorf("%s status=%q, want evaluated", status.Rule, status.Status)
		}
	}
}

func TestEvaluateZombieVMDKConservativelyMatchesReferencesAndDeltas(t *testing.T) {
	datastorePayload, _ := json.Marshal(vsphere.Datastore{
		Location:     vsphere.Location{Datacenter: "dc-a"},
		ID:           "ds-1",
		Name:         "datastore-1",
		BrowseStatus: "success",
		Files: []vsphere.DatastoreFile{
			{Path: "[datastore-1] app/disk.vmdk", SizeBytes: 8 << 30},
			{Path: "[datastore-1] app/disk-000001.vmdk", SizeBytes: 2 << 30},
			{Path: "[datastore-1] orphan/orphan.vmdk", SizeBytes: 4 << 30},
			{Path: "[datastore-1] templates/golden.vmdk", SizeBytes: 1 << 30},
		},
	})
	data := assessment.ExportData{
		Run: assessment.Run{ID: 46, InventorySchemaVersion: "7"},
		VMs: []assessment.ExportVM{
			{Observation: assessment.Observation{VM: vsphere.VM{Disks: []vsphere.VMDisk{{BackingPath: "[DATASTORE-1] app/disk.vmdk"}}}}},
			{Observation: assessment.Observation{VM: vsphere.VM{IsTemplate: true, Disks: []vsphere.VMDisk{{BackingPath: "[datastore-1] templates/golden.vmdk"}}}}},
		},
		Resources: []assessment.ResourceObservation{{Context: "prod", VCenterID: "vc-1", Kind: "datastore", ID: "ds-1", Name: "datastore-1", Payload: datastorePayload}},
	}
	report := Evaluate(data, Options{})
	var zombies []Finding
	for _, finding := range report.Findings {
		if finding.Rule == "datastore-zombie-vmdk" {
			zombies = append(zombies, finding)
		}
	}
	if len(zombies) != 1 || !strings.Contains(zombies[0].Message, "orphan/orphan.vmdk") {
		t.Fatalf("zombie findings=%+v", zombies)
	}

	data.Resources[0].Payload, _ = json.Marshal(vsphere.Datastore{ID: "ds-1", Name: "datastore-1"})
	report = Evaluate(data, Options{})
	for _, status := range report.Rules {
		if status.Rule == "datastore-zombie-vmdk" {
			if status.Status != "not-evaluated" || status.Reason != "a capture run with --browse-datastores" {
				t.Fatalf("zombie status=%+v", status)
			}
			return
		}
	}
	t.Fatal("datastore-zombie-vmdk status was not reported")
}

func TestEvaluateZombieVMDKUnknownWhenBrowseCoverageIsPartial(t *testing.T) {
	browsed, _ := json.Marshal(vsphere.Datastore{Location: vsphere.Location{Context: "prod"}, ID: "ds-1", Name: "browsed", Backing: vsphere.DatastoreBacking{VMFSUUID: "uuid-1"}, BrowseStatus: "success"})
	unbrowsed, _ := json.Marshal(vsphere.Datastore{Location: vsphere.Location{Context: "edge"}, ID: "ds-2", Name: "unbrowsed", Backing: vsphere.DatastoreBacking{VMFSUUID: "uuid-2"}})
	data := assessment.ExportData{
		Run: assessment.Run{ID: 47, InventorySchemaVersion: "11"},
		Contexts: []assessment.ContextRun{
			{Name: "prod", VMStatus: "success", Collections: []assessment.CollectionRun{{Kind: "vm", Status: "success"}, {Kind: "datastore", Status: "success"}}},
			{Name: "edge", VMStatus: "success", Collections: []assessment.CollectionRun{{Kind: "vm", Status: "success"}, {Kind: "datastore", Status: "success"}}},
		},
		Resources: []assessment.ResourceObservation{
			{Context: "prod", VCenterID: "vc-1", Kind: "datastore", ID: "ds-1", Name: "browsed", Payload: browsed},
			{Context: "edge", VCenterID: "vc-2", Kind: "datastore", ID: "ds-2", Name: "unbrowsed", Payload: unbrowsed},
		},
	}
	report := Evaluate(data, Options{})
	for _, status := range report.Rules {
		if status.Rule != "datastore-zombie-vmdk" {
			continue
		}
		if status.Status != "evaluated" || status.Result != "unknown" || status.Reason != "orphan coverage is incomplete" {
			t.Fatalf("zombie status=%+v", status)
		}
		if !containsString(status.Blind, "edge") {
			t.Fatalf("zombie blind contexts=%v, want edge", status.Blind)
		}
		return
	}
	t.Fatal("datastore-zombie-vmdk status was not reported")
}

func TestEvaluateThresholdBoundaries(t *testing.T) {
	finish := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	data := healthFixture("5", finish)
	data.VMs[0].Observation.VM.Partitions[0].FreeBytes = 10
	data.VMs[0].Snapshots[0].CreateTime = finish.Add(-30 * 24 * time.Hour)
	data.Resources[1].Payload, _ = json.Marshal(vsphere.Datastore{ID: "ds-2", Name: "broken", Accessible: false, CapacityBytes: 100, FreeBytes: 10})
	data.Resources[3].Payload, _ = json.Marshal(vsphere.Datastore{ID: "ds-1", Name: "slow", Accessible: true, CapacityBytes: 100, FreeBytes: 10})
	report := Evaluate(data, Options{Thresholds: Thresholds{SnapshotAge: 30 * 24 * time.Hour, DatastoreFreePct: 10, GuestDiskFreePct: 10}})
	for _, finding := range report.Findings {
		if finding.Rule == "guest-disk-space-low" || finding.Rule == "datastore-space-low" {
			t.Fatalf("free-space rule fired at threshold: %+v", finding)
		}
	}
	foundSnapshot := false
	for _, finding := range report.Findings {
		foundSnapshot = foundSnapshot || finding.Rule == "snapshot-age"
	}
	if !foundSnapshot {
		t.Fatal("snapshot-age did not fire at threshold")
	}
}

func TestEvaluateDatastoreAbsoluteFreeFloor(t *testing.T) {
	data := healthFixture("13", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	findingsFor := func(thresholds Thresholds) []Finding {
		report := Evaluate(data, Options{Thresholds: thresholds})
		var findings []Finding
		for _, finding := range report.Findings {
			if finding.Rule == "datastore-space-low" {
				findings = append(findings, finding)
			}
		}
		return findings
	}
	absolute := findingsFor(Thresholds{DatastoreFreeBytes: 10})
	if len(absolute) != 1 || len(absolute[0].Evidence) != 1 || absolute[0].Evidence[0].Field != "free_bytes" {
		t.Fatalf("absolute findings=%+v", absolute)
	}
	percentOnly := findingsFor(Thresholds{DatastoreFreePct: 10})
	if len(percentOnly) != 1 || len(percentOnly[0].Evidence) != 1 || percentOnly[0].Evidence[0].Field != "free_percent" {
		t.Fatalf("percent findings=%+v", percentOnly)
	}
	both := findingsFor(Thresholds{DatastoreFreePct: 10, DatastoreFreeBytes: 10})
	if len(both) != 1 || len(both[0].Evidence) != 2 {
		t.Fatalf("combined findings=%+v", both)
	}
	if findings := findingsFor(Thresholds{}); len(findings) != 0 {
		t.Fatalf("disabled findings=%+v", findings)
	}
}

func TestEvaluateIsDeterministicAndRunRelative(t *testing.T) {
	finish := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	data := healthFixture("5", finish)
	first := Evaluate(data, Options{Thresholds: DefaultThresholds()})
	second := Evaluate(data, Options{Thresholds: DefaultThresholds()})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated evaluation differs:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if first.Findings[0].Object.Context != "prod" {
		t.Fatalf("finding context=%q", first.Findings[0].Object.Context)
	}
	data.Run.FinishedAt = finish.Add(365 * 24 * time.Hour)
	third := Evaluate(data, Options{Thresholds: DefaultThresholds()})
	if !reflect.DeepEqual(first, third) {
		t.Fatal("evaluation changed when only the run finish time changed; context finish should be authoritative")
	}
}

func TestEvaluateSchemaGatesVersionAndPartitionRules(t *testing.T) {
	data := healthFixture("2", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	report := Evaluate(data, Options{Thresholds: DefaultThresholds()})
	statuses := make(map[string]string)
	for _, status := range report.Rules {
		statuses[status.Rule] = status.Status
	}
	for _, id := range []string{"cdrom-connected", "usb-connected", "guest-disk-space-low", "tools-not-installed", "tools-outdated"} {
		if statuses[id] != "not-evaluated" {
			t.Errorf("%s status=%q, want not-evaluated", id, statuses[id])
		}
	}
}

func TestEvaluateConnectedDeviceRulesOnlyReportConnectedDevices(t *testing.T) {
	cdConnected, cdDisconnected := true, false
	usbConnected, usbDisconnected := true, false
	data := assessment.ExportData{
		Run:      assessment.Run{ID: 43, InventorySchemaVersion: "6"},
		Contexts: []assessment.ContextRun{{Name: "prod", Datacenter: "dc-a"}},
		VMs: []assessment.ExportVM{{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-1", VM: vsphere.VM{
			Location: vsphere.Location{Datacenter: "dc-a"}, ID: "vm-1", Name: "app",
			CDROMs: []vsphere.VMCDROM{
				{Key: 102, Label: "CD/DVD drive 2", Connected: &cdDisconnected},
				{Key: 101, Label: "CD/DVD drive 1", Connected: &cdConnected, BackingType: "iso", BackingPath: "[ds] app/install.iso"},
			},
			USBs: []vsphere.VMUSB{
				{Key: 202, Label: "USB device 2", Connected: &usbDisconnected},
				{Key: 201, Label: "USB device 1", Connected: &usbConnected, BackingType: "remoteHost", BackingHost: "esx-1"},
			},
		}}}},
	}
	report := Evaluate(data, Options{})
	var findings []Finding
	for _, finding := range report.Findings {
		if finding.Rule == "cdrom-connected" || finding.Rule == "usb-connected" {
			findings = append(findings, finding)
		}
	}
	if len(findings) != 2 {
		t.Fatalf("connected-device findings = %d, want 2: %+v", len(findings), findings)
	}
	if findings[0].Rule != "cdrom-connected" || !strings.Contains(findings[0].Message, "install.iso") {
		t.Errorf("CD-ROM finding = %+v", findings[0])
	}
	if findings[1].Rule != "usb-connected" || !strings.Contains(findings[1].Message, "esx-1") {
		t.Errorf("USB finding = %+v", findings[1])
	}
	for _, finding := range findings {
		if finding.Severity != SeverityWarning || finding.Object.ID != "vm-1" {
			t.Errorf("finding metadata = %+v", finding)
		}
	}
	first := Evaluate(data, Options{})
	data.VMs[0].Observation.VM.CDROMs[0], data.VMs[0].Observation.VM.CDROMs[1] = data.VMs[0].Observation.VM.CDROMs[1], data.VMs[0].Observation.VM.CDROMs[0]
	data.VMs[0].Observation.VM.USBs[0], data.VMs[0].Observation.VM.USBs[1] = data.VMs[0].Observation.VM.USBs[1], data.VMs[0].Observation.VM.USBs[0]
	second := Evaluate(data, Options{})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("connected-device evaluation is not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
	}
}

func TestEvaluateConnectedDeviceRulesAreSchemaGated(t *testing.T) {
	connected := true
	data := assessment.ExportData{
		Run: assessment.Run{ID: 44, InventorySchemaVersion: "5"},
		VMs: []assessment.ExportVM{{Observation: assessment.Observation{VM: vsphere.VM{
			ID: "vm-1", Name: "app", CDROMs: []vsphere.VMCDROM{{Connected: &connected}}, USBs: []vsphere.VMUSB{{Connected: &connected}},
		}}}},
	}
	report := Evaluate(data, Options{})
	for _, status := range report.Rules {
		if (status.Rule == "cdrom-connected" || status.Rule == "usb-connected") && status.Status != "not-evaluated" {
			t.Errorf("%s status=%q, want not-evaluated", status.Rule, status.Status)
		}
	}
	for _, finding := range report.Findings {
		if finding.Rule == "cdrom-connected" || finding.Rule == "usb-connected" {
			t.Fatalf("schema-gated rule emitted finding: %+v", finding)
		}
	}
}
