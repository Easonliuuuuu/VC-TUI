package vsphere

import (
	"strings"
	"testing"
)

func TestClusterDatastoreCoverageTellsGapsFromLocalDisks(t *testing.T) {
	hosts := []Host{
		{Name: "a", Datastores: []string{"shared", "local-a"}},
		{Name: "b", Datastores: []string{"shared", "partial"}},
		{Name: "c", Datastores: []string{"shared", "partial"}},
	}
	cov := ClusterDatastoreCoverage(hosts)
	if cov[0].Name != "partial" || !cov[0].Gap() || strings.Join(cov[0].Missing, ",") != "a" {
		t.Fatalf("gap should sort first and name the host without it: %+v", cov)
	}
	for _, d := range cov {
		if d.Name == "local-a" && (d.Gap() || !d.Local()) {
			t.Fatalf("a datastore on one host is local, not a gap: %+v", d)
		}
		if d.Name == "shared" && (d.Gap() || d.Local()) {
			t.Fatalf("a datastore on every host is neither: %+v", d)
		}
	}
}

func TestHostMountsRead(t *testing.T) {
	if HostMountsRead([]Host{{Name: "a"}, {Name: "b"}}) {
		t.Fatal("hosts without mounts read as mounts read")
	}
	if !HostMountsRead([]Host{{Name: "a"}, {Name: "b", Datastores: []string{"ds"}}}) {
		t.Fatal("a host with mounts did not count")
	}
}
