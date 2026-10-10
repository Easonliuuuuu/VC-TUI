package vsphere

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/vmware/govmomi/vim25/types"
)

// DefaultVMEventLimit bounds one VMEvents read. A busy VM can carry thousands
// of events inside vCenter's retention window, and one keypress must not turn
// into a read that stalls the interface; the newest events are the ones an
// operator is asking about.
const DefaultVMEventLimit = 500

// What a VM event can explain about the VM's stored history. These are the
// kinds of change an assessment records, from the event's side: a stored
// "moved" can be explained by an EventMoved, and so on.
const (
	EventMoved           = "moved"
	EventRenamed         = "renamed"
	EventModified        = "modified"
	EventCreated         = "created"
	EventRemoved         = "removed"
	EventSnapshotCreated = "snapshot-created"
	EventSnapshotRemoved = "snapshot-removed"
)

// Event results. An event that records a finished operation is ResultOK; one
// that records a failure is ResultFailed; one that records only that an
// operation started (a task event) carries no result until the task history
// says how it ended.
const (
	ResultOK     = "ok"
	ResultFailed = "failed"
)

// VMEvent is one entry from a VM's vCenter event log, reduced to what the
// interface shows and what matching it against stored history needs.
type VMEvent struct {
	Context string    `json:"context"`
	Key     int32     `json:"key"`
	ChainID int32     `json:"chain_id,omitempty"`
	Time    time.Time `json:"time"`
	// Type is vCenter's own name for the event: the event class, such as
	// VmMigratedEvent, or an extended event's type ID.
	Type string `json:"type"`
	// Label is a short lower-case name for the event, such as "migrate".
	Label string `json:"label"`
	// Explains is the kind of stored change this event can account for, one
	// of the Event* constants, or empty for an event that changes nothing an
	// assessment records (a power operation, an alarm).
	Explains string `json:"explains,omitempty"`
	// Minor marks routine events — power, guest and tools state, task
	// progress, alarms — that are hidden until asked for.
	Minor  bool   `json:"minor,omitempty"`
	Result string `json:"result,omitempty"`
	User   string `json:"user,omitempty"`
	// Detail is a one-line summary of what the event changed, for example
	// "esxi-01 → esxi-02".
	Detail string `json:"detail,omitempty"`
	// Summary is vCenter's own message on one line, with the VM, host and
	// datacenter it is about removed. It is set only when Detail is empty, for
	// events with no structured summary of their own: alarms, guest messages,
	// permission changes.
	Summary string `json:"summary,omitempty"`
	// Fields names the stored fields a reconfiguration touched, in the
	// vocabulary assessment diffs use (cpu, memory, annotation, guest_os,
	// name, devices), when vCenter says.
	Fields        []string `json:"fields,omitempty"`
	FromHost      string   `json:"from_host,omitempty"`
	ToHost        string   `json:"to_host,omitempty"`
	FromDatastore string   `json:"from_datastore,omitempty"`
	ToDatastore   string   `json:"to_datastore,omitempty"`
	NewName       string   `json:"new_name,omitempty"`
	Task          string   `json:"task,omitempty"`
	Message       string   `json:"message,omitempty"`
	// taskKey is the key of the task a task event records, which joins it to
	// that task's history record.
	taskKey string

	// SetCPU, SetMemoryMB and SetName are what a reconfiguration set, when it
	// set them; matching compares them with the values a run recorded.
	SetCPU      int32  `json:"set_cpu,omitempty"`
	SetMemoryMB int64  `json:"set_memory_mb,omitempty"`
	SetName     string `json:"set_name,omitempty"`
}

// VMEventListing is one VMEvents read: the events for one VM on one vCenter,
// oldest first.
type VMEventListing struct {
	Context string    `json:"context"`
	VMID    string    `json:"vm_id"`
	Events  []VMEvent `json:"events"`
	// Limit is the most events the read asked for. Truncated reports that
	// vCenter returned that many, so older ones may exist and were not read.
	Limit     int  `json:"limit"`
	Truncated bool `json:"truncated,omitempty"`
	// TaskHistoryError is why the task history could not be read, when it
	// could not. The events are still listed, but task events then carry no
	// result and a failed task may be missing.
	TaskHistoryError string `json:"task_history_error,omitempty"`
}

// Oldest is the time of the oldest event read, or zero when there were none.
func (l VMEventListing) Oldest() time.Time {
	if len(l.Events) == 0 {
		return time.Time{}
	}
	return l.Events[0].Time
}

// VMEvents reads the vCenter event log of one VM, identified by its managed
// object reference value (VM.ID). It is read-only: one QueryEvents call that
// leaves nothing behind on the server, and a task history read that creates a
// collector and destroys it before returning. limit bounds the read; zero
// means DefaultVMEventLimit.
//
// The event log records a task when it is queued, so how tasks ended comes
// from the task history. A task history that cannot be read, for example for
// lack of permission, does not fail the read: the events are returned without
// task outcomes and TaskHistoryError says why.
func (c *Client) VMEvents(ctx context.Context, vmID string, limit int) (VMEventListing, error) {
	if limit <= 0 {
		limit = DefaultVMEventLimit
	}
	out := VMEventListing{VMID: vmID, Limit: limit}
	if c == nil || c.vim == nil || c.vim.Client == nil {
		return out, errors.New("not connected")
	}
	if c.Context != nil {
		out.Context = c.Context.Name
	}
	ref := c.vim.ServiceContent.EventManager
	if ref == nil {
		return out, errors.New("this server does not expose an EventManager")
	}
	if strings.TrimSpace(vmID) == "" {
		return out, errors.New("the VM has no managed object reference")
	}
	filter := types.EventFilterSpec{
		Entity: &types.EventFilterSpecByEntity{
			Entity:    types.ManagedObjectReference{Type: "VirtualMachine", Value: vmID},
			Recursion: types.EventFilterSpecRecursionOptionSelf,
		},
		MaxCount: int32(limit),
	}
	raw, err := queryEvents(ctx, c.vim.Client, *ref, filter)
	if err != nil {
		return out, fmt.Errorf("read events: %w", err)
	}
	out.Events = make([]VMEvent, 0, len(raw))
	for _, e := range raw {
		if e == nil {
			continue
		}
		ev := ClassifyEvent(e)
		ev.Context = out.Context
		out.Events = append(out.Events, ev)
	}
	SortVMEvents(out.Events)
	out.Truncated = len(raw) >= limit

	// A truncated read says nothing about what came before its oldest event,
	// so older tasks are neither read nor shown.
	var since time.Time
	if out.Truncated {
		since = out.Oldest()
	}
	tasks, err := c.VMTasks(ctx, vmID, since)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return out, ctxErr
	}
	if err != nil {
		out.TaskHistoryError = err.Error()
	}
	out.Events = ApplyTaskHistory(out.Events, tasks, since)
	return out, nil
}

// SortVMEvents orders events oldest first, breaking ties by event key, which
// vCenter assigns in the order events were logged.
func SortVMEvents(events []VMEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].Time.Equal(events[j].Time) {
			return events[i].Time.Before(events[j].Time)
		}
		return events[i].Key < events[j].Key
	})
}

// ClassifyEvent reduces one vCenter event to a VMEvent. It is a pure function
// of the event, so the mapping from vCenter's event classes to what the
// timeline shows is testable without a server.
func ClassifyEvent(e types.BaseEvent) VMEvent {
	base := e.GetEvent()
	ev := VMEvent{
		Key:     base.Key,
		ChainID: base.ChainId,
		Time:    base.CreatedTime,
		Type:    eventTypeName(e),
		User:    base.UserName,
		Message: strings.TrimSpace(base.FullFormattedMessage),
		Result:  ResultOK,
	}
	switch x := e.(type) {
	case *types.DrsVmMigratedEvent:
		ev.Label, ev.Explains = "drs migrate", EventMoved
		migration(&ev, base, x.SourceHost, x.SourceDatastore)
		if ev.User == "" {
			ev.User = "DRS"
		}
	case *types.VmMigratedEvent:
		ev.Label, ev.Explains = "migrate", EventMoved
		migration(&ev, base, x.SourceHost, x.SourceDatastore)
	case *types.VmRelocatedEvent:
		ev.Label, ev.Explains = "relocate", EventMoved
		migration(&ev, base, x.SourceHost, x.SourceDatastore)
	case *types.VmRenamedEvent:
		ev.Label, ev.Explains = "rename", EventRenamed
		ev.NewName = x.NewName
		ev.Fields = []string{"name"}
		ev.Detail = x.OldName + " → " + x.NewName
	case *types.VmReconfiguredEvent:
		ev.Label, ev.Explains = "reconfigure", EventModified
		ev.Fields, ev.Detail = reconfiguredFields(x.ConfigSpec)
		ev.SetCPU, ev.SetMemoryMB, ev.SetName = x.ConfigSpec.NumCPUs, x.ConfigSpec.MemoryMB, x.ConfigSpec.Name
	case *types.VmCreatedEvent:
		ev.Label, ev.Explains = "create", EventCreated
	case *types.VmClonedEvent:
		ev.Label, ev.Explains = "clone", EventCreated
		if x.SourceVm.Name != "" {
			ev.Detail = "from " + x.SourceVm.Name
		}
	case *types.VmDeployedEvent:
		ev.Label, ev.Explains = "deploy", EventCreated
		if x.SrcTemplate.Name != "" {
			ev.Detail = "from " + x.SrcTemplate.Name
		}
	case *types.VmRegisteredEvent:
		ev.Label, ev.Explains = "register", EventCreated
	case *types.VmDiscoveredEvent:
		ev.Label, ev.Explains = "discover", EventCreated
	case *types.VmRemovedEvent:
		ev.Label, ev.Explains = "remove", EventRemoved
	case *types.TaskEvent:
		classifyTask(&ev, x.Info)
	case *types.EventEx:
		ev.Label = humanizeEventType(lastSegment(x.EventTypeId))
		if strings.TrimSpace(x.Message) != "" && ev.Message == "" {
			ev.Message = strings.TrimSpace(x.Message)
		}
		switch strings.ToLower(x.Severity) {
		case "error":
			ev.Result = ResultFailed
		case "warning":
		default:
			ev.Minor = true
		}
	default:
		ev.Label = humanizeEventType(ev.Type)
		ev.Minor = true
	}
	if strings.Contains(ev.Type, "Failed") {
		ev.Result, ev.Minor, ev.Explains = ResultFailed, false, ""
	}
	if ev.Detail == "" {
		ev.Summary = messageSummary(ev.Message, base)
	}
	return ev
}

// DisplayDetail is the one-line text a list shows for the event: the
// structured Detail when there is one, otherwise the Summary of vCenter's
// message.
func (e VMEvent) DisplayDetail() string {
	if e.Detail != "" {
		return e.Detail
	}
	return e.Summary
}

// maxSummaryRunes bounds a Summary. A reconfiguration message can run to
// hundreds of characters; the full text stays in Message.
const maxSummaryRunes = 160

// messageSummary reduces vCenter's formatted message to one line that adds to
// the event's label. The VM, host and datacenter the event was logged against
// are already the question the reader asked, so the phrasing that names them
// ("on vapp-web on esxi-01 in DC-Lab") is dropped, and line breaks collapse
// to single spaces.
func messageSummary(msg string, base *types.Event) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if msg == "" {
		return ""
	}
	var vm, host, dc string
	if base.Vm != nil {
		vm = base.Vm.Name
	}
	if base.Host != nil {
		host = base.Host.Name
	}
	if base.Datacenter != nil {
		dc = base.Datacenter.Name
	}
	// scope joins the places an event can be about into "vm on host in dc",
	// or is empty when any of them is unnamed.
	scope := func(places ...[2]string) string {
		var s string
		for _, p := range places {
			if p[1] == "" {
				return ""
			}
			s += p[0] + p[1]
		}
		return s
	}
	vmP, hostP, dcP := [2]string{"", vm}, [2]string{" on ", host}, [2]string{" in ", dc}
	hostOnP, dcCommaP := [2]string{" on host ", host}, [2]string{", in ", dc}
	// The messages read "<vm> on <host> in <dc> is powered on" when the event
	// opens with its scope, and "<what> on <vm> on <host> in <dc>: <why>" when
	// it ends a clause with it. Longest phrasing first, so a host and a
	// datacenter are not left behind by a shorter match.
	phrases := []string{
		scope(vmP, hostP, dcP), scope(vmP, hostP, dcCommaP), scope(vmP, hostOnP, dcP), scope(vmP, hostP), scope(vmP, dcP),
		scope([2]string{"", host}, dcP), scope([2]string{"", host}, dcCommaP), scope(vmP),
	}
	for _, s := range phrases {
		if s != "" && strings.HasPrefix(msg, s+" ") {
			msg = strings.TrimPrefix(msg, s)
			break
		}
	}
	for _, s := range phrases {
		if s != "" {
			msg = strings.ReplaceAll(msg, " on "+s, "")
		}
	}
	msg = strings.TrimSpace(msg)
	if r := []rune(msg); len(r) > maxSummaryRunes {
		msg = strings.TrimSpace(string(r[:maxSummaryRunes-1])) + "…"
	}
	return msg
}

// migration fills a migration's endpoints. The destination is the event's
// own host and datastore; the source is carried separately.
func migration(ev *VMEvent, base *types.Event, from types.HostEventArgument, fromDS *types.DatastoreEventArgument) {
	ev.FromHost = from.Name
	if base.Host != nil {
		ev.ToHost = base.Host.Name
	}
	if fromDS != nil {
		ev.FromDatastore = fromDS.Name
	}
	if base.Ds != nil {
		ev.ToDatastore = base.Ds.Name
	}
	var parts []string
	if ev.FromHost != ev.ToHost && (ev.FromHost != "" || ev.ToHost != "") {
		parts = append(parts, orDash(ev.FromHost)+" → "+orDash(ev.ToHost))
	}
	if ev.FromDatastore != ev.ToDatastore && (ev.FromDatastore != "" || ev.ToDatastore != "") {
		parts = append(parts, orDash(ev.FromDatastore)+" → "+orDash(ev.ToDatastore))
	}
	ev.Detail = strings.Join(parts, " · ")
}

// reconfiguredFields names what a reconfiguration set, in the field names
// assessment diffs use, with a summary of the new values. A spec that sets
// nothing recognizable yields no fields, which matching reads as "unknown",
// not as "nothing changed".
func reconfiguredFields(spec types.VirtualMachineConfigSpec) ([]string, string) {
	var fields, parts []string
	if spec.NumCPUs != 0 {
		fields = append(fields, "cpu")
		parts = append(parts, fmt.Sprintf("cpu %d", spec.NumCPUs))
	}
	if spec.MemoryMB != 0 {
		fields = append(fields, "memory")
		parts = append(parts, fmt.Sprintf("memory %d MB", spec.MemoryMB))
	}
	if spec.Annotation != "" {
		fields = append(fields, "annotation")
		parts = append(parts, "annotation")
	}
	if spec.GuestId != "" {
		fields = append(fields, "guest_os")
		parts = append(parts, "guest os "+spec.GuestId)
	}
	if spec.Name != "" {
		fields = append(fields, "name")
		parts = append(parts, "name "+spec.Name)
	}
	if n := len(spec.DeviceChange); n > 0 {
		fields = append(fields, "devices")
		if n == 1 {
			parts = append(parts, "1 device")
		} else {
			parts = append(parts, fmt.Sprintf("%d devices", n))
		}
	}
	return fields, strings.Join(parts, " · ")
}

// classifyTask names a task event. Snapshot tasks are the ones that explain
// stored history; every other task is the start of an operation whose own
// completion event says more, so it is minor. A task event records when the
// task was queued, not how it ended, so it carries no result unless the task
// is already in an error state; ApplyTaskHistory fills in the rest from the
// task history.
func classifyTask(ev *VMEvent, info types.TaskInfo) {
	ev.Task = info.Name
	ev.taskKey = info.Key
	ev.Result = ""
	if info.State == types.TaskInfoStateError {
		ev.Result = ResultFailed
	}
	id := info.DescriptionId
	if id == "" {
		id = info.Name
	}
	switch {
	case strings.EqualFold(id, "VirtualMachine.createSnapshot") || info.Name == "CreateSnapshot_Task":
		ev.Label, ev.Explains = "snapshot create", EventSnapshotCreated
	case strings.EqualFold(id, "VirtualMachine.removeSnapshot") || strings.EqualFold(id, "VirtualMachine.removeAllSnapshots") ||
		info.Name == "RemoveSnapshot_Task" || info.Name == "RemoveAllSnapshots_Task":
		ev.Label, ev.Explains = "snapshot remove", EventSnapshotRemoved
	case strings.EqualFold(id, "VirtualMachine.revertToSnapshot") || info.Name == "RevertToSnapshot_Task" || info.Name == "RevertToCurrentSnapshot_Task":
		ev.Label = "snapshot revert"
	default:
		ev.Label = "task " + humanizeEventType(strings.TrimSuffix(lastSegment(id), "_Task"))
		ev.Minor = ev.Result != ResultFailed
	}
}

func eventTypeName(e types.BaseEvent) string {
	if x, ok := e.(*types.EventEx); ok && x.EventTypeId != "" {
		return x.EventTypeId
	}
	name := fmt.Sprintf("%T", e)
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}

func lastSegment(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

// humanizeEventType turns an event class name into a short label:
// VmPoweredOnEvent becomes "powered on", VmGuestRebootEvent "guest reboot".
func humanizeEventType(name string) string {
	name = strings.TrimSuffix(name, "Event")
	name = strings.TrimPrefix(name, "Vm")
	var words []string
	var cur []rune
	runes := []rune(name)
	for i, r := range runes {
		boundary := i > 0 && unicode.IsUpper(r) && (unicode.IsLower(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1])))
		if boundary && len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		words = append(words, strings.ToLower(string(cur)))
	}
	if len(words) == 0 {
		return "event"
	}
	return strings.Join(words, " ")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
