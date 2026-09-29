package assessment

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func hiddenCollection(t *testing.T, kind string, values ...any) CollectionResult {
	t.Helper()
	c := CollectionResult{Kind: kind, Status: "success", ItemCount: len(values)}
	if len(values) == 0 {
		c.Status = "empty"
	}
	for _, v := range values {
		payload, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		c.Resources = append(c.Resources, ResourceObservation{Kind: kind, Payload: payload})
	}
	return c
}

func hiddenStatus(r ContextResult, kind string) (string, string) {
	for _, c := range r.Collections {
		if c.Kind == kind {
			return c.Status, c.Error
		}
	}
	return "", ""
}

func TestMarkHiddenInventoryDemotesContradictedCollections(t *testing.T) {
	nic := vsphere.VMNIC{Network: "dvpg-vlan200", NetworkID: "dvportgroup-1005", SwitchID: "50 aa"}
	vm := vsphere.VM{Name: "app", PowerState: "poweredOn", Host: "esx-02", NICs: []vsphere.VMNIC{nic}}
	r := ContextResult{
		VMs: []Observation{{VM: vm}},
		Collections: []CollectionResult{
			hiddenCollection(t, "host"),
			hiddenCollection(t, "cluster", vsphere.Cluster{Name: "Cluster-DR", Hosts: 1}),
			hiddenCollection(t, "network"),
			hiddenCollection(t, "dvswitch"),
		},
	}
	markHiddenInventory(&r)
	for _, kind := range []string{"host", "network", "dvswitch"} {
		status, reason := hiddenStatus(r, kind)
		if status != CollectionPartial || !strings.Contains(reason, "permission-hidden") {
			t.Errorf("%s = %q (%q), want partial with a reason", kind, status, reason)
		}
	}
	if status, _ := hiddenStatus(r, "cluster"); status != "success" {
		t.Errorf("cluster = %q, want untouched", status)
	}
	var runs []CollectionRun
	for _, c := range r.Collections {
		runs = append(runs, CollectionRun{Kind: c.Kind, Status: c.Status})
	}
	if ContextComplete(ContextRun{Collections: runs}, []string{"host", "dvswitch"}) {
		t.Error("a demoted collection still counts as complete evidence")
	}
}

func TestMarkHiddenInventoryLeavesConsistentInventoryAlone(t *testing.T) {
	vm := vsphere.VM{Name: "app", PowerState: "poweredOn", Host: "esx-01", NICs: []vsphere.VMNIC{
		{Network: "dvpg", NetworkID: "dvportgroup-1", SwitchID: "50-AA"},
		{Network: "nsx-seg", NetworkID: "opaque-segment-uuid"},
	}}
	r := ContextResult{
		VMs: []Observation{{VM: vm}},
		Collections: []CollectionResult{
			hiddenCollection(t, "host", vsphere.Host{Name: "esx-01", Cluster: "C1"}),
			hiddenCollection(t, "cluster", vsphere.Cluster{Name: "C1", Hosts: 1}),
			hiddenCollection(t, "network", vsphere.Network{ID: "dvportgroup-1", Name: "dvpg"}),
			hiddenCollection(t, "dvswitch", vsphere.DVSwitch{UUID: "50-aa"}),
		},
	}
	markHiddenInventory(&r)
	for _, c := range r.Collections {
		if c.Status != "success" {
			t.Errorf("%s = %q (%s), want success", c.Kind, c.Status, c.Error)
		}
	}
}
