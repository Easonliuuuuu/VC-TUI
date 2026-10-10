package vsphere

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// The hand-rolled operations used to read one VM's task history. A TaskEvent
// in the event log is written when a task is queued, so only the task history
// records how it ended. The govmomi wrappers live in the task and history
// packages, which reach the server through the methods package that vsfleet
// must not import. As with the event query, the bodies are defined here and
// TestOnlyPerfAndBrowserShimsDefineFault keeps every hand-rolled Fault in the
// reviewed files and names the only requests this file may send.
//
// The three operations are CreateCollectorForTasks, ReadNextTasks and
// DestroyCollector. They change no inventory: the collector is a private,
// session-scoped cursor over task records, and DestroyCollector releases the
// cursor this file created, nothing else.
type createCollectorForTasksBody struct {
	Req    *types.CreateCollectorForTasks         `xml:"urn:vim25 CreateCollectorForTasks,omitempty"`
	Res    *types.CreateCollectorForTasksResponse `xml:"CreateCollectorForTasksResponse,omitempty"`
	Fault_ *soap.Fault                            `xml:"http://schemas.xmlsoap.org/soap/envelope/ Fault,omitempty"`
}

func (b *createCollectorForTasksBody) Fault() *soap.Fault { return b.Fault_ }

type readNextTasksBody struct {
	Req    *types.ReadNextTasks         `xml:"urn:vim25 ReadNextTasks,omitempty"`
	Res    *types.ReadNextTasksResponse `xml:"ReadNextTasksResponse,omitempty"`
	Fault_ *soap.Fault                  `xml:"http://schemas.xmlsoap.org/soap/envelope/ Fault,omitempty"`
}

func (b *readNextTasksBody) Fault() *soap.Fault { return b.Fault_ }

type destroyCollectorBody struct {
	Req    *types.DestroyCollector         `xml:"urn:vim25 DestroyCollector,omitempty"`
	Res    *types.DestroyCollectorResponse `xml:"DestroyCollectorResponse,omitempty"`
	Fault_ *soap.Fault                     `xml:"http://schemas.xmlsoap.org/soap/envelope/ Fault,omitempty"`
}

func (b *destroyCollectorBody) Fault() *soap.Fault { return b.Fault_ }

const (
	// taskPageSize is how many task records one ReadNextTasks call asks for.
	taskPageSize = 100
	// maxVMTasks bounds one task history read.
	maxVMTasks = 5000
	// taskWindowSlack widens the time bound on a read so a task queued just
	// before the oldest event read is not cut by clock rounding.
	taskWindowSlack = time.Minute
	// destroyCollectorTimeout bounds the cleanup call, which must still run
	// after the caller's context is cancelled so no collector is left on the
	// server.
	destroyCollectorTimeout = 10 * time.Second
)

// readEntityTasks reads the task records for one entity, optionally only
// those queued at or after since, in no promised order: vCenter pages newest
// first where the simulator pages oldest first. It creates a collector and
// always destroys it, including when the read fails or ctx is cancelled:
// vCenter caps collectors per user, and an abandoned one would sit there
// until the session ended.
func readEntityTasks(ctx context.Context, client *vim25.Client, manager, entity types.ManagedObjectReference, since time.Time) (tasks []types.TaskInfo, err error) {
	filter := types.TaskFilterSpec{Entity: &types.TaskFilterSpecByEntity{
		Entity:    entity,
		Recursion: types.TaskFilterSpecRecursionOptionSelf,
	}}
	if !since.IsZero() {
		begin := since.Add(-taskWindowSlack)
		filter.Time = &types.TaskFilterSpecByTime{TimeType: types.TaskFilterSpecTimeOptionQueuedTime, BeginTime: &begin}
	}
	create := createCollectorForTasksBody{Req: &types.CreateCollectorForTasks{This: manager, Filter: filter}}
	var created createCollectorForTasksBody
	if err := client.RoundTrip(ctx, &create, &created); err != nil {
		return nil, err
	}
	if created.Res == nil {
		return nil, errors.New("CreateCollectorForTasks returned no response")
	}
	collector := created.Res.Returnval
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), destroyCollectorTimeout)
		defer cancel()
		destroy := destroyCollectorBody{Req: &types.DestroyCollector{This: collector}}
		var destroyed destroyCollectorBody
		if derr := client.RoundTrip(cleanup, &destroy, &destroyed); derr != nil && err == nil {
			err = fmt.Errorf("release task collector: %w", derr)
		}
	}()

	seen := map[string]bool{}
	for len(tasks) < maxVMTasks {
		read := readNextTasksBody{Req: &types.ReadNextTasks{This: collector, MaxCount: taskPageSize}}
		var page readNextTasksBody
		if err := client.RoundTrip(ctx, &read, &page); err != nil {
			return tasks, err
		}
		if page.Res == nil {
			break
		}
		added := false
		for _, t := range page.Res.Returnval {
			// A page that repeats records would otherwise never end.
			if t.Key == "" || !seen[t.Key] {
				seen[t.Key] = true
				tasks = append(tasks, t)
				added = true
			}
		}
		if !added {
			break
		}
	}
	return tasks, nil
}

// VMTasks reads the task history of one VM, identified by its managed object
// reference value, oldest first. It exists to learn how tasks ended, which the
// event log does not record. Only tasks queued at or after since are read; a
// zero since reads them all. Unlike QueryEvents it creates a server-side
// collector, which it destroys before returning; it changes no inventory.
func (c *Client) VMTasks(ctx context.Context, vmID string, since time.Time) ([]types.TaskInfo, error) {
	if c == nil || c.vim == nil || c.vim.Client == nil {
		return nil, errors.New("not connected")
	}
	ref := c.vim.ServiceContent.TaskManager
	if ref == nil {
		return nil, errors.New("this server does not expose a TaskManager")
	}
	if strings.TrimSpace(vmID) == "" {
		return nil, errors.New("the VM has no managed object reference")
	}
	tasks, err := readEntityTasks(ctx, c.vim.Client, *ref, types.ManagedObjectReference{Type: "VirtualMachine", Value: vmID}, since)
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].QueueTime.Before(tasks[j].QueueTime) })
	return tasks, err
}

// ApplyTaskHistory joins task outcomes onto a VM's classified events and
// returns the result, in the order SortVMEvents gives.
//
// A task event is matched to its task by task key, falling back to the event
// chain ID when the key is absent. A matched task that finished sets the
// event's result: ok for a task that succeeded, failed, with the error as
// detail, for one that did not. A task still queued or running, and a task
// event with no matching record, keep their unknown result. A failed task is
// never minor and explains no stored change.
//
// A failed task with no event of its own is added as a row, because the
// failure is what an operator is looking for. since bounds those rows: when
// the event read was truncated, tasks queued before its oldest event are
// outside the window the listing claims to cover. A zero since adds all of
// them.
func ApplyTaskHistory(events []VMEvent, tasks []types.TaskInfo, since time.Time) []VMEvent {
	byKey := make(map[string]*types.TaskInfo, len(tasks))
	byChain := make(map[int32]*types.TaskInfo, len(tasks))
	for i := range tasks {
		t := &tasks[i]
		if t.Key != "" {
			byKey[t.Key] = t
		}
		if t.EventChainId != 0 {
			byChain[t.EventChainId] = t
		}
	}
	seen := make(map[string]bool, len(tasks))
	for i := range events {
		ev := &events[i]
		if ev.taskKey == "" {
			continue
		}
		t := byKey[ev.taskKey]
		if t == nil && ev.ChainID != 0 {
			t = byChain[ev.ChainID]
		}
		if t == nil {
			continue
		}
		seen[t.Key] = true
		applyTaskOutcome(ev, t)
	}
	for i := range tasks {
		t := &tasks[i]
		if t.State != types.TaskInfoStateError || seen[t.Key] || t.QueueTime.Before(since) {
			continue
		}
		ev := VMEvent{
			ChainID: t.EventChainId,
			Time:    t.QueueTime,
			Type:    "TaskInfo",
			taskKey: t.Key,
			Message: "Task: " + t.Name,
		}
		if u, ok := t.Reason.(*types.TaskReasonUser); ok {
			ev.User = u.UserName
		}
		classifyTask(&ev, *t)
		applyTaskOutcome(&ev, t)
		events = append(events, ev)
	}
	SortVMEvents(events)
	return events
}

// applyTaskOutcome sets an event's result from a finished task.
func applyTaskOutcome(ev *VMEvent, t *types.TaskInfo) {
	switch t.State {
	case types.TaskInfoStateSuccess:
		ev.Result = ResultOK
	case types.TaskInfoStateError:
		ev.Result, ev.Minor, ev.Explains, ev.Fields = ResultFailed, false, "", nil
		if msg := taskErrorText(t); msg != "" {
			ev.Detail = msg
		}
	}
}

// taskErrorText is what vCenter says went wrong with a task: the localized
// message when there is one, the fault's type otherwise.
func taskErrorText(t *types.TaskInfo) string {
	if t.Cancelled {
		return "canceled"
	}
	if t.Error == nil {
		return ""
	}
	if msg := strings.TrimSpace(t.Error.LocalizedMessage); msg != "" {
		return msg
	}
	if t.Error.Fault != nil {
		name := fmt.Sprintf("%T", t.Error.Fault)
		return humanizeEventType(name[strings.LastIndex(name, ".")+1:])
	}
	return ""
}
