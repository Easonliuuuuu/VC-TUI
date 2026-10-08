package network

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func boolPtr(value bool) *bool { return &value }

func comparisonFixture(t *testing.T, sourcePGs, targetPGs []vsphere.DVPortGroup) assessment.ExportData {
	t.Helper()
	contexts := []assessment.ContextRun{
		{Name: "prod", VCenterID: "vc-prod", VMStatus: "success", Collections: completeCollections()},
		{Name: "dr", VCenterID: "vc-dr", VMStatus: "success", Collections: completeCollections()},
	}
	resources := make([]assessment.ResourceObservation, 0)
	add := func(context, vcenter, kind, id, name string, value any) {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		resources = append(resources, assessment.ResourceObservation{Context: context, VCenterID: vcenter, Kind: kind, ID: id, Name: name, Payload: payload})
	}
	add("prod", "vc-prod", "cluster", "cluster-prod", "source", vsphere.Cluster{Location: vsphere.Location{Context: "prod", Datacenter: "dc"}, ID: "cluster-prod", Name: "source"})
	add("dr", "vc-dr", "cluster", "cluster-dr", "target", vsphere.Cluster{Location: vsphere.Location{Context: "dr", Datacenter: "dc"}, ID: "cluster-dr", Name: "target"})
	add("prod", "vc-prod", "host", "host-prod", "esx-prod", vsphere.Host{Location: vsphere.Location{Context: "prod", Datacenter: "dc"}, ID: "host-prod", Name: "esx-prod", Cluster: "source"})
	add("dr", "vc-dr", "host", "host-dr", "esx-dr", vsphere.Host{Location: vsphere.Location{Context: "dr", Datacenter: "dc"}, ID: "host-dr", Name: "esx-dr", Cluster: "target"})
	add("prod", "vc-prod", "dvswitch", "dvs-prod", "dvs-prod", vsphere.DVSwitch{Location: vsphere.Location{Context: "prod", Datacenter: "dc"}, ID: "dvs-prod", Name: "dvs-prod", UUID: "uuid-prod", MaxMTU: 1500, Hosts: []string{"host-prod"}, PortGroups: sourcePGs})
	add("dr", "vc-dr", "dvswitch", "dvs-dr", "dvs-dr", vsphere.DVSwitch{Location: vsphere.Location{Context: "dr", Datacenter: "dc"}, ID: "dvs-dr", Name: "dvs-dr", UUID: "uuid-dr", MaxMTU: 1500, Hosts: []string{"host-dr"}, PortGroups: targetPGs})
	return assessment.ExportData{Run: assessment.Run{ID: 42, InventorySchemaVersion: "12"}, Contexts: contexts, Resources: resources}
}

func completeCollections() []assessment.CollectionRun {
	return []assessment.CollectionRun{
		{Kind: "vm", Status: "success"},
		{Kind: "host", Status: "success"},
		{Kind: "cluster", Status: "success"},
		{Kind: "resourcepool", Status: "success"},
		{Kind: "dvswitch", Status: "success"},
		{Kind: "datastore", Status: "success"},
		{Kind: "network", Status: "success"},
	}
}

func portGroup(name, key, vlan string) vsphere.DVPortGroup {
	return vsphere.DVPortGroup{
		ID: "dvport-" + key, Key: key, Name: name, VLAN: vlan,
		TeamingPolicy: "loadbalance_srcid", Promiscuous: boolPtr(false), MACChanges: boolPtr(false), ForgedTransmits: boolPtr(false),
		ActiveUplinks: []string{"uplink1"},
	}
}

func TestCompareCleanMatchByVLAN(t *testing.T) {
	source := portGroup("app-prod", "pg-prod", "100")
	target := portGroup("app-dr", "pg-dr", "100")
	data := comparisonFixture(t, []vsphere.DVPortGroup{source}, []vsphere.DVPortGroup{target})
	result := Compare(data, "source", "target", nil)
	if result.Confidence != "complete" || len(result.Matched) != 1 || result.Matched[0].MatchBasis != "vlan" {
		t.Fatalf("unexpected comparison: %+v", result)
	}
	if len(result.MappingGaps) != 0 || len(result.TargetOnly) != 0 || len(result.Differences) != 0 {
		t.Fatalf("expected clean match: %+v", result)
	}
	if result.Source.HostCount != 1 || result.Target.HostCount != 1 {
		t.Fatalf("host counts were not resolved: %+v", result)
	}
}

func TestCompareReportsVLANGapAndAttachedVM(t *testing.T) {
	missing := portGroup("isolated", "pg-missing", "200")
	data := comparisonFixture(t, []vsphere.DVPortGroup{missing}, nil)
	vm := vsphere.VM{Location: vsphere.Location{Context: "prod"}, ID: "vm-1", Name: "app01", Cluster: "source", NICs: []vsphere.VMNIC{{Network: "isolated", NetworkID: "pg-missing", SwitchID: "uuid-prod"}}}
	data.VMs = []assessment.ExportVM{{Observation: assessment.Observation{Context: "prod", VCenterID: "vc-prod", VM: vm}}}
	result := Compare(data, "source", "target", nil)
	if result.Confidence != "complete" || len(result.MappingGaps) != 1 {
		t.Fatalf("unexpected gap comparison: %+v", result)
	}
	gap := result.MappingGaps[0]
	if len(gap.AttachedVMs) != 1 || gap.AttachedVMs[0].Name != "app01" || gap.Severity != severityBlocker {
		t.Fatalf("attached VM was not resolved: %+v", gap)
	}
	readiness := Verdict(result)
	if readiness.Verdict != "blocked" || len(readiness.Blockers) != 1 {
		t.Fatalf("unexpected readiness: %+v", readiness)
	}
}

func TestCompareReportsMTUMismatch(t *testing.T) {
	source := portGroup("app", "pg-prod", "100")
	target := portGroup("app", "pg-dr", "100")
	data := comparisonFixture(t, []vsphere.DVPortGroup{source}, []vsphere.DVPortGroup{target})
	for index := range data.Resources {
		if data.Resources[index].Kind != "dvswitch" || data.Resources[index].Context != "dr" {
			continue
		}
		var sw vsphere.DVSwitch
		if !assessment.DecodeResource(data.Resources[index], &sw) {
			t.Fatal("could not decode test switch")
		}
		sw.MaxMTU = 9000
		data.Resources[index].Payload, _ = json.Marshal(sw)
	}
	result := Compare(data, "source", "target", nil)
	if len(result.Differences) != 1 || result.Differences[0].Field != "mtu" || result.Differences[0].Severity != severityBlocker {
		t.Fatalf("unexpected MTU differences: %+v", result.Differences)
	}
	if Verdict(result).Verdict != "blocked" {
		t.Fatalf("MTU mismatch should block: %+v", Verdict(result))
	}
}

func TestCompareBlindDistributedSwitchIsUnknown(t *testing.T) {
	data := comparisonFixture(t, []vsphere.DVPortGroup{portGroup("app", "pg-prod", "100")}, []vsphere.DVPortGroup{portGroup("app", "pg-dr", "100")})
	for index := range data.Contexts {
		if data.Contexts[index].Name == "dr" {
			for collection := range data.Contexts[index].Collections {
				if data.Contexts[index].Collections[collection].Kind == "dvswitch" {
					data.Contexts[index].Collections[collection].Status = "failed"
				}
			}
		}
	}
	result := Compare(data, "source", "target", nil)
	if result.Confidence != "unknown" || len(result.Blind) != 1 || result.Blind[0].Context != "dr" {
		t.Fatalf("blind switch should be unknown: %+v", result)
	}
	if len(result.Differences) != 0 {
		t.Fatalf("clean evidence should not produce differences: %+v", result.Differences)
	}
}

func TestCompareAmbiguousClusterName(t *testing.T) {
	data := comparisonFixture(t, nil, nil)
	data.Resources = append(data.Resources,
		assessment.ResourceObservation{Context: "dr", VCenterID: "vc-dr", Kind: "cluster", ID: "cluster-source-2", Name: "source", Payload: mustJSON(t, vsphere.Cluster{Location: vsphere.Location{Context: "dr"}, ID: "cluster-source-2", Name: "source"})},
	)
	result := Compare(data, "source", "target", nil)
	if !result.Ambiguous || len(result.Candidates) != 2 || result.Confidence != "unknown" {
		t.Fatalf("expected ambiguity: %+v", result)
	}
}

func TestCompareIsDeterministic(t *testing.T) {
	source := portGroup("app", "pg-prod", "100")
	target := portGroup("app", "pg-dr", "100")
	left := comparisonFixture(t, []vsphere.DVPortGroup{source}, []vsphere.DVPortGroup{target})
	right := comparisonFixture(t, []vsphere.DVPortGroup{source}, []vsphere.DVPortGroup{target})
	reverseResources := make([]assessment.ResourceObservation, len(right.Resources))
	for index := range right.Resources {
		reverseResources[len(right.Resources)-index-1] = right.Resources[index]
	}
	right.Resources = reverseResources
	reverseContexts := make([]assessment.ContextRun, len(right.Contexts))
	for index := range right.Contexts {
		reverseContexts[len(right.Contexts)-index-1] = right.Contexts[index]
	}
	right.Contexts = reverseContexts
	if !reflect.DeepEqual(Compare(left, "source", "target", nil), Compare(right, "source", "target", nil)) {
		t.Fatal("comparison changed with input ordering")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestFullCoverageOnClustersOfDifferentSizesIsNoDifference(t *testing.T) {
	source := NetworkSummary{Name: "web", VLAN: "110", CoveredHosts: 16, TotalHosts: 16}
	target := NetworkSummary{Name: "web", VLAN: "110", CoveredHosts: 6, TotalHosts: 6}
	if got := Differences(NetworkMatch{Source: source, Target: target, MatchBasis: "vlan"}); len(got) != 0 {
		t.Fatalf("full coverage on both sides reported %+v", got)
	}
	target.CoveredHosts = 5
	got := Differences(NetworkMatch{Source: source, Target: target, MatchBasis: "vlan"})
	if len(got) != 1 || got[0].Field != "host_coverage" || !IsBlocker(got[0].Severity) {
		t.Fatalf("a target host without the network is not a host_coverage blocker: %+v", got)
	}
}
