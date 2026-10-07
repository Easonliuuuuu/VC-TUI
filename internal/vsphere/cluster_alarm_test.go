package vsphere_test

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
)

func TestListClustersReadsTriggeredAlarms(t *testing.T) {
	model := simulator.VPX()
	model.ClusterHost = 2
	if err := model.Create(); err != nil {
		t.Fatalf("create simulator model: %v", err)
	}
	t.Cleanup(model.Remove)

	var hostAlarm, vmAlarm types.ManagedObjectReference
	for _, ref := range model.Map().AllReference("Alarm") {
		switch model.Map().Get(ref.Reference()).(*simulator.Alarm).Info.Name {
		case "Host error":
			hostAlarm = ref.Reference()
		case "Virtual machine error":
			vmAlarm = ref.Reference()
		}
	}
	if hostAlarm.Value == "" || vmAlarm.Value == "" {
		t.Fatal("simulator has no default host and VM error alarms")
	}
	cluster := model.Map().Any("ClusterComputeResource").(*simulator.ClusterComputeResource)
	host := model.Map().Get(cluster.Host[0]).(*simulator.HostSystem)
	// vSphere lists a descendant's alarm on the cluster too; set the
	// cluster's state as the server would have propagated it.
	cluster.TriggeredAlarmState = []types.AlarmState{
		{Key: "a1", Alarm: vmAlarm, Entity: types.ManagedObjectReference{Type: "VirtualMachine", Value: "vm-missing"}, OverallStatus: types.ManagedEntityStatusYellow},
		{Key: "a2", Alarm: hostAlarm, Entity: host.Self, OverallStatus: types.ManagedEntityStatusRed, Acknowledged: types.NewBool(true)},
		{Key: "a3", Alarm: hostAlarm, Entity: host.Self, OverallStatus: types.ManagedEntityStatusGray},
	}

	gc, endpoint := dialSimulator(t, model)
	c := clientFor(gc, endpoint, "")
	clusters, err := c.ListClusters(context.Background())
	if err != nil {
		t.Fatalf("ListClusters: %v", err)
	}
	found := false
	for _, cl := range clusters {
		if !cl.AlarmsRead {
			t.Errorf("%s: alarms not marked read", cl.Name)
		}
		if cl.ID != cluster.Self.Value {
			if len(cl.Alarms) != 0 {
				t.Errorf("%s: alarms = %+v, want none", cl.Name, cl.Alarms)
			}
			continue
		}
		found = true
		if len(cl.Alarms) != 2 {
			t.Fatalf("alarms = %+v, want the red and yellow ones", cl.Alarms)
		}
		first, second := cl.Alarms[0], cl.Alarms[1]
		if first.Name != "Host error" || first.Status != "red" || first.Entity != host.Name || first.EntityType != "HostSystem" || !first.Acknowledged {
			t.Errorf("critical alarm = %+v", first)
		}
		// An entity outside the index still says where the alarm fired.
		if second.Name != "Virtual machine error" || second.Status != "yellow" || second.Entity != "vm-missing" || second.Acknowledged {
			t.Errorf("warning alarm = %+v", second)
		}
	}
	if !found {
		t.Fatalf("cluster %s not listed", cluster.Self.Value)
	}
}
