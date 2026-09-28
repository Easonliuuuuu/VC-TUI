package vsphere

import (
	"testing"

	"github.com/vmware/govmomi/vim25/types"
)

func TestApplyComputeSummaryReadsClusterAndStandaloneSummaries(t *testing.T) {
	base := types.ComputeResourceSummary{
		NumCpuCores: 4, TotalCpu: 9984, TotalMemory: 20412 << 20,
		NumEffectiveHosts: 1, NumHosts: 1,
	}
	cases := map[string]types.BaseComputeResourceSummary{
		"standalone": &base,
		"cluster":    &types.ClusterComputeResourceSummary{ComputeResourceSummary: base},
	}
	for name, summary := range cases {
		t.Run(name, func(t *testing.T) {
			cl := Cluster{Hosts: 1}
			applyComputeSummary(&cl, summary)
			if cl.CPUCores != 4 || cl.TotalCPUMHz != 9984 || cl.TotalMemoryMB != 20412 || cl.EffectiveHost != 1 {
				t.Fatalf("capacity not read: %+v", cl)
			}
		})
	}
}

func TestApplyComputeSummaryToleratesMissingSummaries(t *testing.T) {
	var nilCluster *types.ClusterComputeResourceSummary
	var nilBase *types.ComputeResourceSummary
	for _, summary := range []types.BaseComputeResourceSummary{nil, nilCluster, nilBase} {
		cl := Cluster{Hosts: 2}
		applyComputeSummary(&cl, summary)
		if cl.Hosts != 2 || cl.CPUCores != 0 {
			t.Fatalf("missing summary changed the cluster: %+v", cl)
		}
	}
}
