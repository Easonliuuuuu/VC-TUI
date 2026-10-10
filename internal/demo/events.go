package demo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// eventRetention is how far back the demo's vCenters keep events: thirty
// days, vCenter's default. Anything older than that is simply not there,
// which is what makes the timeline's oldest stored changes read as "before
// the oldest event" rather than as unexplained.
const eventRetention = 30 * 24 * time.Hour

// VMEvents implements the TUI's optional vCenter events extension with a
// synthetic event log. Every event is derived from the same seeded estate as
// the stored history, so the two agree: a VM resized since the previous run
// has the reconfiguration that did it, a snapshot has its create task, a VM
// deployed since has its clone, and a decommissioned one its removal. On top
// of that some VMs carry events that leave no stored trace — a migration that
// came back before the next run, a reconfiguration that failed — because
// those are what the live log adds.
func (b *Backend) VMEvents(_ context.Context, cc *config.Context, vmID string, limit int) (vsphere.VMEventListing, error) {
	if limit <= 0 {
		limit = vsphere.DefaultVMEventLimit
	}
	out := vsphere.VMEventListing{Context: cc.Name, VMID: vmID, Limit: limit}
	if err := b.failures[cc.Name]; err != nil {
		return out, err
	}
	e := b.estates[cc.Name]
	if e == nil {
		return out, nil
	}
	if base, ok := strings.CutSuffix(vmID, "-retired"); ok {
		for _, vm := range e.inv.VMs {
			if vm.ID == base {
				out.Events = retiredEvents(cc.Name, vm)
			}
		}
	} else {
		for _, vm := range e.inv.VMs {
			if vm.ID == vmID {
				out.Events = vmEvents(e, cc.Name, vm)
			}
		}
	}
	since := demoNow.Add(-eventRetention)
	kept := out.Events[:0]
	for _, ev := range out.Events {
		if ev.Time.After(since) && !ev.Time.After(demoNow) {
			kept = append(kept, ev)
		}
	}
	vsphere.SortVMEvents(kept)
	if len(kept) > limit {
		kept, out.Truncated = kept[len(kept)-limit:], true
	}
	out.Events = kept
	return out, nil
}

// eventLog builds one VM's events with stable keys.
type eventLog struct {
	ctx, vm string
	key     int32
	events  []vsphere.VMEvent
}

func newEventLog(ctx, vm string) *eventLog {
	return &eventLog{ctx: ctx, vm: vm, key: int32(40000 + hv(ctx, vm, "event-key")%9000*10)}
}

func (l *eventLog) add(ev vsphere.VMEvent) {
	l.key++
	ev.Context, ev.Key, ev.ChainID = l.ctx, l.key, l.key
	if ev.Result == "" && ev.Task == "" {
		ev.Result = vsphere.ResultOK
	}
	l.events = append(l.events, ev)
}

// at is a stable instant some days and hours after start.
func at(start time.Time, ctx, vm, salt string, maxDays int) time.Time {
	d := time.Duration(1+pickN(maxDays, ctx, vm, salt, "d")) * 24 * time.Hour
	h := time.Duration(pickN(14, ctx, vm, salt, "h")+7)*time.Hour + time.Duration(pickN(60, ctx, vm, salt, "m"))*time.Minute
	return start.Add(d + h)
}

func vmEvents(e *estate, ctx string, vm vsphere.VM) []vsphere.VMEvent {
	l := newEventLog(ctx, vm.Name)
	prev := runDates[len(runDates)-2]
	span := int(demoNow.Sub(prev)/(24*time.Hour)) - 2

	// Deployed since the previous run: the clone that created it.
	if bornRun(ctx, vm.Name) == len(runDates)-1 {
		when := at(prev, ctx, vm.Name, "born", span)
		label, typ := "clone", "VmClonedEvent"
		if pickN(2, ctx, vm.Name, "born-how") == 1 {
			label, typ = "deploy", "VmDeployedEvent"
		}
		l.add(vsphere.VMEvent{Time: when, Type: typ, Label: label, Explains: vsphere.EventCreated, User: "terraform@corp.example",
			Message: fmt.Sprintf("Virtual machine %s %sed on %s in %s", vm.Name, strings.TrimSuffix(label, "e"), vm.Host, vm.Cluster)})
		l.add(vsphere.VMEvent{Time: when.Add(4 * time.Minute), Type: "VmPoweredOnEvent", Label: "powered on", Minor: true, User: "terraform@corp.example",
			Message: fmt.Sprintf("%s on %s is powered on", vm.Name, vm.Host)})
	}

	// Resized since the previous run: the reconfiguration that did it.
	if !pinnedVM(vm.Name) && hv(vm.Context, vm.Name, "resize")%40 == 0 && vm.CPU > 1 && 1+int(hv(vm.Context, vm.Name, "resize-run")%4) == len(runDates)-1 {
		when := at(prev, ctx, vm.Name, "resize", span)
		l.add(vsphere.VMEvent{Time: when, Type: "VmReconfiguredEvent", Label: "reconfigure", Explains: vsphere.EventModified, User: "admin@vsphere.local",
			Fields: []string{"cpu", "memory"}, Detail: fmt.Sprintf("cpu %d · memory %d MB", vm.CPU, vm.MemoryMB),
			Message: fmt.Sprintf("Reconfigured %s on %s in %s", vm.Name, vm.Host, vm.Cluster)})
	}

	// Every snapshot inside retention has the task that took it.
	for _, s := range vm.Snapshots {
		l.add(vsphere.VMEvent{Time: s.CreateTime, Type: "TaskEvent", Label: "snapshot create", Explains: vsphere.EventSnapshotCreated, User: "backup@corp.example",
			Task: "CreateSnapshot_Task", Message: "Task: Create virtual machine snapshot"})
	}

	// A migration that went out and came back between runs: the live log
	// has it, no stored run does.
	if vm.Host != "" && pickN(4, ctx, vm.Name, "bounce") == 0 {
		var peers []string
		for _, h := range e.inv.Hosts {
			if h.Cluster == vm.Cluster && h.Name != vm.Host && h.Name != e.lateHost {
				peers = append(peers, h.Name)
			}
		}
		if len(peers) > 0 {
			other := peers[pickN(len(peers), ctx, vm.Name, "peer")]
			when := at(prev, ctx, vm.Name, "bounce", span)
			ds := firstOf(vm.Datastores)
			l.add(vsphere.VMEvent{Time: when, Type: "VmMigratedEvent", Label: "migrate", Explains: vsphere.EventMoved, User: "ops@corp.example",
				FromHost: vm.Host, ToHost: other, FromDatastore: ds, ToDatastore: ds, Detail: vm.Host + " → " + other,
				Message: fmt.Sprintf("Migration of virtual machine %s from %s, %s to %s, %s completed", vm.Name, vm.Host, ds, other, ds)})
			back := when.Add(time.Duration(90+pickN(120, ctx, vm.Name, "back")) * time.Minute)
			l.add(vsphere.VMEvent{Time: back, Type: "DrsVmMigratedEvent", Label: "drs migrate", Explains: vsphere.EventMoved, User: "DRS",
				FromHost: other, ToHost: vm.Host, FromDatastore: ds, ToDatastore: ds, Detail: other + " → " + vm.Host,
				Message: fmt.Sprintf("Migrating %s from %s, %s to %s, %s in %s", vm.Name, other, ds, vm.Host, ds, vm.Cluster)})
		}
	}

	// A reconfiguration that failed changed nothing a run could record.
	if vm.CPU > 0 && pickN(6, ctx, vm.Name, "failed") == 0 {
		when := at(prev, ctx, vm.Name, "failed", span)
		l.add(vsphere.VMEvent{Time: when, Type: "com.vmware.vc.vm.VmReconfigureFailedEvent", Label: "reconfigure failed", Result: vsphere.ResultFailed, User: "ops@corp.example",
			Detail:  fmt.Sprintf("cpu %d → %d · not enough CPU on %s", vm.CPU, vm.CPU*2, vm.Host),
			Message: fmt.Sprintf("Reconfiguration of %s failed: the host %s does not have enough CPU capacity", vm.Name, vm.Host)})
	}

	// Routine events, hidden until asked for.
	if vm.PowerState == "poweredOn" && pickN(3, ctx, vm.Name, "reboot") == 0 {
		when := at(prev, ctx, vm.Name, "reboot", span)
		l.add(vsphere.VMEvent{Time: when, Type: "VmGuestRebootEvent", Label: "guest reboot", Minor: true, User: "patch@corp.example",
			Message: fmt.Sprintf("Guest OS reboot for %s on %s", vm.Name, vm.Host)})
	}
	return l.events
}

// retiredEvents is the log of a decommissioned predecessor (see viewAt): its
// removal, when that falls inside retention.
func retiredEvents(ctx string, current vsphere.VM) []vsphere.VMEvent {
	name := "old-" + current.Name
	l := newEventLog(ctx, name)
	die := 1 + int(hv(ctx, current.Name, "die")%4)
	if die != len(runDates)-1 {
		return nil
	}
	prev := runDates[len(runDates)-2]
	span := int(demoNow.Sub(prev)/(24*time.Hour)) - 2
	when := at(prev, ctx, name, "remove", span)
	l.add(vsphere.VMEvent{Time: when.Add(-6 * time.Minute), Type: "VmPoweredOffEvent", Label: "powered off", Minor: true, User: "ops@corp.example",
		Message: fmt.Sprintf("%s on %s is powered off", name, current.Host)})
	l.add(vsphere.VMEvent{Time: when, Type: "VmRemovedEvent", Label: "remove", Explains: vsphere.EventRemoved, User: "ops@corp.example",
		Message: fmt.Sprintf("Removed %s on %s from %s", name, current.Host, current.Datacenter)})
	return l.events
}

func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
