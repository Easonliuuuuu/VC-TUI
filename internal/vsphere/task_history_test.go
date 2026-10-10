package vsphere_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// queuedTaskEvent is what vCenter logs for a task: written when the task is
// queued, so its state never says how the task ended.
func queuedTaskEvent(key int32, taskKey, id string, at time.Time) *types.TaskEvent {
	return &types.TaskEvent{
		Event: types.Event{Key: key, ChainId: key, CreatedTime: at, UserName: "ops@corp.example"},
		Info:  types.TaskInfo{Key: taskKey, Name: id + "_Task", DescriptionId: id, State: types.TaskInfoStateQueued},
	}
}

func finishedTask(key string, chain int32, id string, at time.Time, state types.TaskInfoState, message string) types.TaskInfo {
	info := types.TaskInfo{
		Key: key, EventChainId: chain, DescriptionId: id, Name: id + "_Task", QueueTime: at, State: state,
		Reason: &types.TaskReasonUser{UserName: "ops@corp.example"},
	}
	if message != "" {
		info.Error = &types.LocalizedMethodFault{LocalizedMessage: message, Fault: &types.InvalidArgument{}}
	}
	return info
}

func TestApplyTaskHistoryJoinsOutcomesOntoTaskEvents(t *testing.T) {
	at := time.Date(2026, 10, 10, 13, 34, 45, 0, time.UTC)
	events := []vsphere.VMEvent{
		vsphere.ClassifyEvent(queuedTaskEvent(10, "task-failed", "VirtualMachine.reconfigure", at)),
		vsphere.ClassifyEvent(queuedTaskEvent(11, "task-ok", "VirtualMachine.powerOn", at.Add(time.Minute))),
		vsphere.ClassifyEvent(queuedTaskEvent(12, "task-running", "VirtualMachine.powerOff", at.Add(2*time.Minute))),
		vsphere.ClassifyEvent(queuedTaskEvent(13, "task-unknown", "VirtualMachine.suspend", at.Add(3*time.Minute))),
		// A task event is joined by chain ID when its key is not in the history.
		vsphere.ClassifyEvent(queuedTaskEvent(14, "task-renamed-key", "VirtualMachine.reset", at.Add(4*time.Minute))),
	}
	tasks := []types.TaskInfo{
		finishedTask("task-failed", 10, "VirtualMachine.reconfigure", at, types.TaskInfoStateError, "A specified parameter was not correct: configSpec.memoryMB"),
		finishedTask("task-ok", 11, "VirtualMachine.powerOn", at.Add(time.Minute), types.TaskInfoStateSuccess, ""),
		finishedTask("task-running", 12, "VirtualMachine.powerOff", at.Add(2*time.Minute), types.TaskInfoStateRunning, ""),
		finishedTask("other-key", 14, "VirtualMachine.reset", at.Add(4*time.Minute), types.TaskInfoStateSuccess, ""),
	}

	got := vsphere.ApplyTaskHistory(events, tasks, time.Time{})
	if len(got) != len(events) {
		t.Fatalf("got %d events, want %d: a task with an event adds no row", len(got), len(events))
	}
	byTask := map[string]vsphere.VMEvent{}
	for _, ev := range got {
		byTask[ev.Task] = ev
	}

	failed := byTask["VirtualMachine.reconfigure_Task"]
	if failed.Result != vsphere.ResultFailed || failed.Minor || failed.Explains != "" {
		t.Fatalf("failed task: result=%q minor=%v explains=%q", failed.Result, failed.Minor, failed.Explains)
	}
	if failed.Detail != "A specified parameter was not correct: configSpec.memoryMB" {
		t.Fatalf("failed task detail = %q", failed.Detail)
	}
	ok := byTask["VirtualMachine.powerOn_Task"]
	if ok.Result != vsphere.ResultOK || !ok.Minor {
		t.Fatalf("successful task: result=%q minor=%v, want ok and still routine", ok.Result, ok.Minor)
	}
	if ev := byTask["VirtualMachine.powerOff_Task"]; ev.Result != "" {
		t.Fatalf("a running task has result %q, want unknown", ev.Result)
	}
	if ev := byTask["VirtualMachine.suspend_Task"]; ev.Result != "" || !ev.Minor {
		t.Fatalf("a task with no history has result %q minor=%v, want unchanged", ev.Result, ev.Minor)
	}
	if ev := byTask["VirtualMachine.reset_Task"]; ev.Result != vsphere.ResultOK {
		t.Fatalf("a task joined by chain ID has result %q, want ok", ev.Result)
	}
}

func TestApplyTaskHistoryKeepsAFailedSnapshotFromExplainingAChange(t *testing.T) {
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	events := []vsphere.VMEvent{vsphere.ClassifyEvent(queuedTaskEvent(5, "snap", "VirtualMachine.createSnapshot", at))}
	if events[0].Explains != vsphere.EventSnapshotCreated {
		t.Fatalf("queued snapshot task explains %q", events[0].Explains)
	}
	got := vsphere.ApplyTaskHistory(events, []types.TaskInfo{
		finishedTask("snap", 5, "VirtualMachine.createSnapshot", at, types.TaskInfoStateError, "Insufficient disk space"),
	}, time.Time{})
	if got[0].Result != vsphere.ResultFailed || got[0].Explains != "" {
		t.Fatalf("failed snapshot: result=%q explains=%q", got[0].Result, got[0].Explains)
	}
}

func TestApplyTaskHistoryAddsFailedTasksThatHaveNoEvent(t *testing.T) {
	at := time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC)
	events := []vsphere.VMEvent{vsphere.ClassifyEvent(queuedTaskEvent(20, "ok", "VirtualMachine.powerOn", at))}
	tasks := []types.TaskInfo{
		finishedTask("ok", 20, "VirtualMachine.powerOn", at, types.TaskInfoStateSuccess, ""),
		finishedTask("lost-fail", 3, "VirtualMachine.reconfigure", at.Add(-time.Hour), types.TaskInfoStateError, "boom"),
		finishedTask("lost-ok", 4, "VirtualMachine.reset", at.Add(-time.Hour), types.TaskInfoStateSuccess, ""),
	}

	got := vsphere.ApplyTaskHistory(append([]vsphere.VMEvent(nil), events...), tasks, time.Time{})
	if len(got) != 2 {
		t.Fatalf("got %d events, want the logged one plus the lost failure: %+v", len(got), got)
	}
	row := got[0]
	if row.Result != vsphere.ResultFailed || row.Minor || row.User != "ops@corp.example" || row.Detail != "boom" || !row.Time.Equal(at.Add(-time.Hour)) {
		t.Fatalf("failed task row = %+v", row)
	}
	if row.Label != "task reconfigure" {
		t.Fatalf("failed task row label = %q", row.Label)
	}

	// Outside the window a truncated read claims to cover, the failure is not
	// shown: its neighbors were cut off, so it would appear out of nowhere.
	got = vsphere.ApplyTaskHistory(append([]vsphere.VMEvent(nil), events...), tasks, at)
	if len(got) != 1 {
		t.Fatalf("a failed task older than the event window added a row: %+v", got)
	}
}

// failedAndSucceededPowerTasks runs one failing and one succeeding task on a
// simulated VM.
func failedAndSucceededPowerTasks(t *testing.T, vm *object.VirtualMachine) {
	t.Helper()
	ctx := context.Background()
	// The simulator powers its VMs on, so a second power on fails.
	task, err := vm.PowerOn(ctx)
	if err != nil {
		t.Fatalf("queue power on: %v", err)
	}
	if err := task.Wait(ctx); err == nil {
		t.Fatal("powering on a powered-on VM succeeded; the test needs a failed task")
	}
	task, err = vm.PowerOff(ctx)
	if err != nil {
		t.Fatalf("queue power off: %v", err)
	}
	if err := task.Wait(ctx); err != nil {
		t.Fatalf("power off: %v", err)
	}
}

func TestVMTasksReadsOutcomesFromTheSimulator(t *testing.T) {
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
	target, other := vms[0], vms[1]
	failedAndSucceededPowerTasks(t, object.NewVirtualMachine(gc.Client, types.ManagedObjectReference{Type: "VirtualMachine", Value: target.ID}))
	// A task on another VM must not leak into this VM's history.
	failedAndSucceededPowerTasks(t, object.NewVirtualMachine(gc.Client, types.ManagedObjectReference{Type: "VirtualMachine", Value: other.ID}))

	tasks, err := client.VMTasks(context.Background(), target.ID, time.Time{})
	if err != nil {
		t.Fatalf("VMTasks: %v", err)
	}
	var failed, succeeded int
	for _, task := range tasks {
		if task.Entity == nil || task.Entity.Value != target.ID {
			t.Fatalf("task %q is for %v, not %s", task.Key, task.Entity, target.ID)
		}
		switch task.State {
		case types.TaskInfoStateError:
			failed++
		case types.TaskInfoStateSuccess:
			succeeded++
		}
	}
	if failed != 1 || succeeded < 1 {
		t.Fatalf("read %d failed and %d successful tasks, want 1 failed and at least 1 successful", failed, succeeded)
	}
	if oldest := tasks[0]; oldest.QueueTime.After(tasks[len(tasks)-1].QueueTime) {
		t.Fatalf("tasks are not oldest first: %v after %v", oldest.QueueTime, tasks[len(tasks)-1].QueueTime)
	}

	// Only tasks queued since a time are read.
	since, err := client.VMTasks(context.Background(), target.ID, time.Now().Add(-time.Hour))
	if err != nil || len(since) != len(tasks) {
		t.Fatalf("tasks since an hour ago: %d (err %v), want all %d", len(since), err, len(tasks))
	}
	since, err = client.VMTasks(context.Background(), target.ID, time.Now().Add(time.Hour))
	if err != nil || len(since) != 0 {
		t.Fatalf("tasks since an hour from now: %d (err %v), want none", len(since), err)
	}
	if _, err := client.VMTasks(context.Background(), "", time.Time{}); err == nil {
		t.Fatal("an empty managed object reference must be refused")
	}

	// VMEvents surfaces the failed task, which the simulator logs no
	// TaskEvent for, as its own failed row.
	listing, err := client.VMEvents(context.Background(), target.ID, 0)
	if err != nil {
		t.Fatalf("VMEvents: %v", err)
	}
	if listing.TaskHistoryError != "" {
		t.Fatalf("task history error: %s", listing.TaskHistoryError)
	}
	var rows int
	for _, ev := range listing.Events {
		if ev.Result == vsphere.ResultFailed && ev.Label == "task power on" {
			rows++
			if ev.Minor || ev.Detail == "" {
				t.Fatalf("failed power on row = %+v", ev)
			}
		}
	}
	if rows != 1 {
		t.Fatalf("found %d failed power on rows in %+v", rows, listing.Events)
	}
}

// TestVMTasksDestroysItsCollector reads far more often than the server allows
// collectors. vCenter and the simulator both cap them (32 by default), so a
// read that left its collector behind would start failing partway through.
func TestVMTasksDestroysItsCollector(t *testing.T) {
	model := simulator.VPX()
	model.Datacenter, model.Cluster, model.ClusterHost, model.Machine = 1, 1, 2, 1
	if err := model.Create(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Remove)
	gc, endpoint := dialSimulator(t, model)
	client := clientFor(gc, endpoint, "")
	vms, err := client.ListVMs(context.Background())
	if err != nil || len(vms) == 0 {
		t.Fatalf("list VMs: %v", err)
	}
	for i := 0; i < 80; i++ {
		if _, err := client.VMTasks(context.Background(), vms[0].ID, time.Time{}); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	// A cancelled read must not leave one either.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 80; i++ {
		if _, err := client.VMTasks(cancelled, vms[0].ID, time.Time{}); err == nil {
			t.Fatal("a cancelled context read succeeded")
		}
	}
	if _, err := client.VMTasks(context.Background(), vms[0].ID, time.Time{}); err != nil {
		t.Fatalf("read after cancelled reads: %v", err)
	}
}

// TestVMEventsKeepsEventsWhenTaskHistoryCannotBeRead covers a read-only role
// that may read events but not task history: the events still come back, with
// the reason, rather than the whole read failing.
func TestVMEventsKeepsEventsWhenTaskHistoryCannotBeRead(t *testing.T) {
	model := simulator.VPX()
	model.Datacenter, model.Cluster, model.ClusterHost, model.Machine = 1, 1, 2, 1
	if err := model.Create(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Remove)
	gc, endpoint := dialSimulator(t, model)
	client := clientFor(gc, endpoint, "")
	vms, err := client.ListVMs(context.Background())
	if err != nil || len(vms) == 0 {
		t.Fatalf("list VMs: %v", err)
	}

	// Pointing the task manager at nothing makes CreateCollectorForTasks fault.
	gc.ServiceContent.TaskManager = &types.ManagedObjectReference{Type: "TaskManager", Value: "no-such-task-manager"}
	listing, err := client.VMEvents(context.Background(), vms[0].ID, 0)
	if err != nil {
		t.Fatalf("VMEvents failed outright instead of degrading: %v", err)
	}
	if len(listing.Events) == 0 {
		t.Fatal("no events returned")
	}
	if listing.TaskHistoryError == "" {
		t.Fatal("TaskHistoryError is empty, want the reason the task history was not read")
	}
	if strings.Contains(listing.TaskHistoryError, "\n") {
		t.Fatalf("TaskHistoryError is not one line: %q", listing.TaskHistoryError)
	}

	gc.ServiceContent.TaskManager = nil
	listing, err = client.VMEvents(context.Background(), vms[0].ID, 0)
	if err != nil || len(listing.Events) == 0 || listing.TaskHistoryError == "" {
		t.Fatalf("a server with no TaskManager: err=%v events=%d note=%q", err, len(listing.Events), listing.TaskHistoryError)
	}
}
