package vsphere_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func TestClassifyEventMapsEventClassesToStoredChanges(t *testing.T) {
	at := time.Date(2026, 9, 16, 2, 40, 0, 0, time.UTC)
	vmEvent := func() types.VmEvent {
		return types.VmEvent{Event: types.Event{
			Key: 7, CreatedTime: at, UserName: "ops@corp.example",
			Host: &types.HostEventArgument{EntityEventArgument: types.EntityEventArgument{Name: "esxi-02"}},
			Ds:   &types.DatastoreEventArgument{EntityEventArgument: types.EntityEventArgument{Name: "ds-a"}},
		}}
	}
	migrated := types.VmMigratedEvent{
		VmEvent:         vmEvent(),
		SourceHost:      types.HostEventArgument{EntityEventArgument: types.EntityEventArgument{Name: "esxi-01"}},
		SourceDatastore: &types.DatastoreEventArgument{EntityEventArgument: types.EntityEventArgument{Name: "ds-a"}},
	}
	drs := types.DrsVmMigratedEvent{VmMigratedEvent: migrated}
	drs.UserName = ""

	for _, tc := range []struct {
		name     string
		event    types.BaseEvent
		label    string
		explains string
		result   string
		minor    bool
		check    func(t *testing.T, ev vsphere.VMEvent)
	}{
		{name: "vmotion", event: &migrated, label: "migrate", explains: vsphere.EventMoved, result: vsphere.ResultOK, check: func(t *testing.T, ev vsphere.VMEvent) {
			if ev.FromHost != "esxi-01" || ev.ToHost != "esxi-02" || ev.Detail != "esxi-01 → esxi-02" {
				t.Fatalf("migration endpoints = %q → %q (%q)", ev.FromHost, ev.ToHost, ev.Detail)
			}
		}},
		{name: "drs", event: &drs, label: "drs migrate", explains: vsphere.EventMoved, result: vsphere.ResultOK, check: func(t *testing.T, ev vsphere.VMEvent) {
			if ev.User != "DRS" {
				t.Fatalf("a DRS migration with no user reads as %q, want DRS", ev.User)
			}
		}},
		{name: "rename", event: &types.VmRenamedEvent{VmEvent: vmEvent(), OldName: "web-01", NewName: "web-01-old"}, label: "rename", explains: vsphere.EventRenamed, result: vsphere.ResultOK, check: func(t *testing.T, ev vsphere.VMEvent) {
			if ev.NewName != "web-01-old" {
				t.Fatalf("new name %q", ev.NewName)
			}
		}},
		{name: "reconfigure", event: &types.VmReconfiguredEvent{VmEvent: vmEvent(), ConfigSpec: types.VirtualMachineConfigSpec{NumCPUs: 4, MemoryMB: 8192}}, label: "reconfigure", explains: vsphere.EventModified, result: vsphere.ResultOK, check: func(t *testing.T, ev vsphere.VMEvent) {
			if strings.Join(ev.Fields, ",") != "cpu,memory" || ev.Detail != "cpu 4 · memory 8192 MB" {
				t.Fatalf("fields %v detail %q", ev.Fields, ev.Detail)
			}
		}},
		{name: "created", event: &types.VmCreatedEvent{VmEvent: vmEvent()}, label: "create", explains: vsphere.EventCreated, result: vsphere.ResultOK},
		{name: "removed", event: &types.VmRemovedEvent{VmEvent: vmEvent()}, label: "remove", explains: vsphere.EventRemoved, result: vsphere.ResultOK},
		{name: "snapshot task", event: &types.TaskEvent{Event: vmEvent().Event, Info: types.TaskInfo{Name: "CreateSnapshot_Task", DescriptionId: "VirtualMachine.createSnapshot", State: types.TaskInfoStateQueued}}, label: "snapshot create", explains: vsphere.EventSnapshotCreated, result: ""},
		{name: "other task", event: &types.TaskEvent{Event: vmEvent().Event, Info: types.TaskInfo{Name: "PowerOnVM_Task", DescriptionId: "VirtualMachine.powerOn"}}, label: "task power on", minor: true, result: ""},
		{name: "power", event: &types.VmPoweredOnEvent{VmEvent: vmEvent()}, label: "powered on", minor: true, result: vsphere.ResultOK},
		{name: "failed migrate", event: &types.VmFailedMigrateEvent{VmEvent: vmEvent()}, label: "failed migrate", result: vsphere.ResultFailed},
		{name: "extended error", event: &types.EventEx{Event: vmEvent().Event, EventTypeId: "com.vmware.vc.vm.VmReconfigureFailedEvent", Severity: "error"}, label: "reconfigure failed", result: vsphere.ResultFailed},
		{name: "extended info", event: &types.EventEx{Event: vmEvent().Event, EventTypeId: "com.vmware.vc.guestOperations.GuestOperation", Severity: "info"}, label: "guest operation", minor: true, result: vsphere.ResultOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := vsphere.ClassifyEvent(tc.event)
			if ev.Label != tc.label || ev.Explains != tc.explains || ev.Result != tc.result || ev.Minor != tc.minor {
				t.Fatalf("got label=%q explains=%q result=%q minor=%v, want %q %q %q %v", ev.Label, ev.Explains, ev.Result, ev.Minor, tc.label, tc.explains, tc.result, tc.minor)
			}
			if !ev.Time.Equal(at) || ev.Key != 7 {
				t.Fatalf("time/key = %v/%d", ev.Time, ev.Key)
			}
			if tc.check != nil {
				tc.check(t, ev)
			}
		})
	}
}

// TestVMEventsReadsOneVMsLogFromTheSimulator exercises the hand-rolled
// QueryEvents shim end to end: the simulator logs lifecycle events for every
// VM it creates and powers on, and the read must return only the asked-for
// VM's, oldest first, attributed to the context.
func TestVMEventsReadsOneVMsLogFromTheSimulator(t *testing.T) {
	model := simulator.VPX()
	model.Datacenter, model.Cluster, model.ClusterHost, model.Machine = 1, 1, 2, 2
	if err := model.Create(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Remove)
	gc, endpoint := dialSimulator(t, model)
	client := clientFor(gc, endpoint, "")

	vms, err := client.ListVMs(context.Background())
	if err != nil || len(vms) < 2 {
		t.Fatalf("list VMs: %v (%d)", err, len(vms))
	}
	target := vms[0]
	listing, err := client.VMEvents(context.Background(), target.ID, 0)
	if err != nil {
		t.Fatalf("VMEvents: %v", err)
	}
	if listing.Context != "sim" || listing.VMID != target.ID || listing.Limit != vsphere.DefaultVMEventLimit {
		t.Fatalf("listing header = %+v", listing)
	}
	if len(listing.Events) == 0 {
		t.Fatal("the simulator logged no events for a VM it created")
	}
	for i, ev := range listing.Events {
		if ev.Context != "sim" {
			t.Fatalf("event %d has context %q", i, ev.Context)
		}
		if i > 0 && ev.Time.Before(listing.Events[i-1].Time) {
			t.Fatalf("events are not oldest first: %v before %v", ev.Time, listing.Events[i-1].Time)
		}
		if strings.Contains(ev.Message, vms[1].Name) && !strings.Contains(ev.Message, target.Name) {
			t.Fatalf("event %q belongs to another VM", ev.Message)
		}
	}
	if !listing.Oldest().Equal(listing.Events[0].Time) {
		t.Fatalf("Oldest = %v, want %v", listing.Oldest(), listing.Events[0].Time)
	}

	limited, err := client.VMEvents(context.Background(), target.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Events) != 1 || !limited.Truncated {
		t.Fatalf("limit 1 returned %d events, truncated=%v", len(limited.Events), limited.Truncated)
	}

	if _, err := client.VMEvents(context.Background(), "", 0); err == nil {
		t.Fatal("an empty managed object reference must be refused, not read as every event")
	}
}
