package vsphere

import (
	"testing"

	"github.com/vmware/govmomi/vim25/types"
)

func TestApplyComputeSummaryReadsClusterHealthAndFailover(t *testing.T) {
	summary := &types.ClusterComputeResourceSummary{
		ComputeResourceSummary: types.ComputeResourceSummary{
			NumCpuCores: 64, TotalCpu: 112000, TotalMemory: 1536 << 30,
			EffectiveCpu: 89600, EffectiveMemory: 1024 << 10,
			NumHosts: 4, NumEffectiveHosts: 3,
		},
		CurrentEVCModeKey:    "intel-icelake",
		DrsScore:             92,
		CurrentFailoverLevel: 3,
		AdmissionControlInfo: &types.ClusterFailoverLevelAdmissionControlInfo{CurrentFailoverLevel: 0},
	}
	cl := Cluster{HA: &ClusterHA{Policy: HAPolicyHostFailures, FailoverLevel: 1}}
	applyComputeSummary(&cl, summary)
	if cl.EffectiveCPUMHz != 89600 || cl.EffectiveMemoryMB != 1024<<10 {
		t.Fatalf("effective capacity not read: %+v", cl)
	}
	if cl.EVCMode != "intel-icelake" || cl.DRSScore != 92 {
		t.Fatalf("EVC or DRS score not read: %+v", cl)
	}
	// The admission control info is the policy's own answer; the summary's
	// deprecated level is only a fallback when the info is missing.
	if cl.HA.CurrentFailoverLevel != 3 {
		t.Fatalf("zero info level should fall back to the summary's: %+v", cl.HA)
	}

	cl = Cluster{HA: &ClusterHA{Policy: HAPolicyResources, CPUReservePct: 25, MemReservePct: 25}}
	summary.AdmissionControlInfo = &types.ClusterFailoverResourcesAdmissionControlInfo{
		CurrentCpuFailoverResourcesPercent: 38, CurrentMemoryFailoverResourcesPercent: 12,
	}
	applyComputeSummary(&cl, summary)
	if cl.HA.CPUFailoverPct != 38 || cl.HA.MemFailoverPct != 12 || cl.HA.CurrentFailoverLevel != 0 {
		t.Fatalf("resource failover percentages not read: %+v", cl.HA)
	}
}

func TestApplyComputeSummaryLeavesStandaloneWithoutHA(t *testing.T) {
	cl := Cluster{Standalone: true}
	applyComputeSummary(&cl, &types.ComputeResourceSummary{EffectiveCpu: 1000, EffectiveMemory: 2048})
	if cl.HA != nil || cl.EVCMode != "" || cl.EffectiveCPUMHz != 1000 || cl.EffectiveMemoryMB != 2048 {
		t.Fatalf("standalone summary read wrong: %+v", cl)
	}
}

func TestHAConfigReadsEachAdmissionPolicy(t *testing.T) {
	off := false
	idx := &index{byRef: map[types.ManagedObjectReference]entity{
		{Type: "HostSystem", Value: "host-9"}: {name: "esx-spare"},
	}}
	cases := []struct {
		name string
		das  types.ClusterDasConfigInfo
		want ClusterHA
	}{
		{"host failures", types.ClusterDasConfigInfo{
			HostMonitoring: "enabled", VmMonitoring: "vmMonitoringDisabled",
			AdmissionControlPolicy: &types.ClusterFailoverLevelAdmissionControlPolicy{FailoverLevel: 1},
		}, ClusterHA{HostMonitoring: "enabled", VMMonitoring: "vmMonitoringDisabled", AdmissionControl: true, Policy: HAPolicyHostFailures, FailoverLevel: 1}},
		{"resource percent", types.ClusterDasConfigInfo{
			AdmissionControlPolicy: &types.ClusterFailoverResourcesAdmissionControlPolicy{CpuFailoverResourcesPercent: 25, MemoryFailoverResourcesPercent: 30, FailoverLevel: 1},
		}, ClusterHA{AdmissionControl: true, Policy: HAPolicyResources, CPUReservePct: 25, MemReservePct: 30, FailoverLevel: 1}},
		{"failover hosts", types.ClusterDasConfigInfo{
			AdmissionControlEnabled: &off,
			AdmissionControlPolicy:  &types.ClusterFailoverHostAdmissionControlPolicy{FailoverHosts: []types.ManagedObjectReference{{Type: "HostSystem", Value: "host-9"}}},
		}, ClusterHA{AdmissionControl: false, Policy: HAPolicyFailoverHosts, FailoverHosts: []string{"esx-spare"}}},
		{"legacy level", types.ClusterDasConfigInfo{FailoverLevel: 2},
			ClusterHA{AdmissionControl: true, Policy: HAPolicyHostFailures, FailoverLevel: 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := haConfig(idx, &tc.das)
			if got.HostMonitoring != tc.want.HostMonitoring || got.VMMonitoring != tc.want.VMMonitoring ||
				got.AdmissionControl != tc.want.AdmissionControl || got.Policy != tc.want.Policy ||
				got.FailoverLevel != tc.want.FailoverLevel || got.CPUReservePct != tc.want.CPUReservePct ||
				got.MemReservePct != tc.want.MemReservePct || len(got.FailoverHosts) != len(tc.want.FailoverHosts) {
				t.Fatalf("haConfig = %+v, want %+v", *got, tc.want)
			}
			for i := range got.FailoverHosts {
				if got.FailoverHosts[i] != tc.want.FailoverHosts[i] {
					t.Fatalf("failover hosts = %v, want %v", got.FailoverHosts, tc.want.FailoverHosts)
				}
			}
		})
	}
}

func TestIssueMessagesKeepsReadableMessages(t *testing.T) {
	events := []types.BaseEvent{
		&types.Event{FullFormattedMessage: "Insufficient vSphere HA failover resources"},
		nil,
		&types.Event{FullFormattedMessage: "   "},
		&types.Event{FullFormattedMessage: " vSphere HA agent is unreachable "},
	}
	got := issueMessages(events)
	if len(got) != 2 || got[0] != "Insufficient vSphere HA failover resources" || got[1] != "vSphere HA agent is unreachable" {
		t.Fatalf("issueMessages = %q", got)
	}
}

func TestHidesHostsCatchesMembersTheAccountCannotSee(t *testing.T) {
	seen := types.ManagedObjectReference{Type: "HostSystem", Value: "host-1"}
	hidden := types.ManagedObjectReference{Type: "HostSystem", Value: "host-2"}
	idx := &index{byRef: map[types.ManagedObjectReference]entity{seen: {name: "esx-a"}}}
	if hidesHosts(idx, []types.ManagedObjectReference{seen}, 1) {
		t.Fatal("every member is visible")
	}
	if !hidesHosts(idx, []types.ManagedObjectReference{seen, hidden}, 2) {
		t.Fatal("a member outside the index went unnoticed")
	}
	// The member list can leave a host out that the summary still counts.
	if !hidesHosts(idx, []types.ManagedObjectReference{seen}, 2) {
		t.Fatal("a host counted but not listed went unnoticed")
	}
}
