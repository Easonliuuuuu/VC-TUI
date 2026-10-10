package assessment

import (
	"sort"
	"strings"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// EventSpan is the time between two consecutive runs that observed one VM,
// with the stored changes the later run recorded and the vCenter events
// logged in between. It is the only window a stored change can be placed in:
// a run says what the VM looked like, not when it got that way.
//
// From is nil for the span before the first run that saw the VM, and To is
// nil for the span since the last one — events there are newer than any
// stored assessment.
type EventSpan struct {
	From    *Run
	To      *Run
	Changes []ExplainedChange
	// Unexplained are the events in this span that account for none of its
	// changes: operations that were undone before the next run, failures,
	// and routine events. They are kept rather than dropped, because an
	// event that left no stored trace is often the one worth seeing.
	Unexplained []vsphere.VMEvent
}

// ExplainedChange is one stored change and the events that account for it.
// Events is empty when nothing in the span does.
type ExplainedChange struct {
	Change VMHistoryEvent
	Events []vsphere.VMEvent
}

// ExplainTimeline lines a VM's stored timeline up with its vCenter events.
//
// history must include unchanged observations (Timeline with
// includeUnchanged), because the spans are drawn between every run that saw
// the VM, not only the runs where something changed; "observed" entries mark
// runs and are not reported as changes. events may come from several
// vCenters and in any order.
//
// An event explains a change when it falls in the same span, its kind can
// produce that change, it did not fail, and the details both sides carry
// agree: a migration explains "moved" only if it ended on the host the run
// recorded, and a reconfiguration explains "modified" only if it touched one
// of the changed fields, when vCenter says which. Each event explains at most
// one change. Spans with neither changes nor events are left out.
//
// Events carry the vCenter's clock and runs the local one. Where a run stored
// the offset it measured for that vCenter, event times are moved onto the local
// clock before they are placed. Where it did not (every run captured before
// the offset was recorded), an event within boundaryTolerance of a run edge
// that explains no change on its own side but explains one on the other side
// is attributed there: see repairBoundaries.
func ExplainTimeline(history []VMHistoryEvent, events []vsphere.VMEvent) []EventSpan {
	runs := timelineRuns(history)
	spans := make([]EventSpan, len(runs)+1)
	for i := range spans {
		if i > 0 {
			spans[i].From = &runs[i-1]
		}
		if i < len(runs) {
			spans[i].To = &runs[i]
		}
	}
	runIndex := make(map[int64]int, len(runs))
	for i, r := range runs {
		runIndex[r.ID] = i
	}
	for _, h := range history {
		if h.Kind == "observed" {
			continue
		}
		i, ok := runIndex[h.Run.ID]
		if !ok {
			continue
		}
		spans[i].Changes = append(spans[i].Changes, ExplainedChange{Change: h})
	}

	sorted := append([]vsphere.VMEvent(nil), events...)
	vsphere.SortVMEvents(sorted)
	// A run collects each VM some time after it starts, so an event logged
	// while run i was collecting may already be in run i's observation, or
	// may not be until run i+1. Such an event is offered to span i first and
	// carried to span i+1 if nothing there needs it.
	between := make([][]vsphere.VMEvent, len(spans))
	during := make([][]vsphere.VMEvent, len(runs))
	for _, e := range sorted {
		i := sort.Search(len(runs), func(i int) bool {
			t, _ := onRunClock(e, runs[i])
			return !runs[i].StartedAt.Before(t)
		})
		if i > 0 {
			if t, _ := onRunClock(e, runs[i-1]); !t.After(runEnd(runs[i-1])) {
				during[i-1] = append(during[i-1], e)
				continue
			}
		}
		between[i] = append(between[i], e)
	}

	var carried []vsphere.VMEvent
	for i := range spans {
		settled := append(carried, between[i]...)
		carried = nil
		offered := settled
		if i < len(runs) {
			offered = append(append([]vsphere.VMEvent(nil), settled...), during[i]...)
		}
		used := explainSpan(&spans[i], offered)
		for k, e := range offered {
			switch {
			case used[k]:
			case k >= len(settled):
				carried = append(carried, e)
			default:
				spans[i].Unexplained = append(spans[i].Unexplained, e)
			}
		}
	}
	repairBoundaries(spans, runs)
	out := make([]EventSpan, 0, len(spans))
	for _, span := range spans {
		if len(span.Changes) > 0 || len(span.Unexplained) > 0 {
			out = append(out, span)
		}
	}
	return out
}

// boundaryTolerance is how far from a run edge an event may sit and still be
// attributed across it, for a run that stored no clock offset. Events are
// stamped by the vCenter and runs by the machine that captured them, and two
// clocks that are not the same one disagree: by milliseconds when both follow
// NTP, by a second or two on the lab vCenter that prompted this, by more on a
// vCenter that has drifted. Five seconds covers a clock that is visibly off
// without reaching changes made well clear of the edge. It costs little when
// too wide, because it only ever moves an event that explains nothing where it
// sits to a change that nothing else explains.
const boundaryTolerance = 5 * time.Second

// onRunClock is an event's time on the local clock run r was stamped with,
// and whether r measured the offset to do it. The event keeps its own time for
// display; this is only for placing it among runs.
func onRunClock(e vsphere.VMEvent, r Run) (time.Time, bool) {
	if offset, ok := r.ClockOffset(e.Context); ok {
		return e.Time.Add(-offset), true
	}
	return e.Time, false
}

// toleranceAt is the doubt about where an event falls relative to run r's
// edges: none when r measured its vCenter's clock, boundaryTolerance when not.
func toleranceAt(e vsphere.VMEvent, r Run) time.Duration {
	if _, ok := r.ClockOffset(e.Context); ok {
		return 0
	}
	return boundaryTolerance
}

// repairBoundaries looks across run edges for the events that clock skew put
// on the wrong side. A change no event of its own kind explains takes, from
// the unexplained events of the neighboring gaps, those that sit within
// tolerance of the edge between them and that explain it: events just before
// the earlier run's start, which may really have been logged after it, and
// events just after the later run's end, which may really have been logged
// before it. Events that explain nothing stay where the clock put them.
func repairBoundaries(spans []EventSpan, runs []Run) {
	type ref struct{ span, idx int }
	taken := make(map[ref]bool)
	for s := range spans {
		for c := range spans[s].Changes {
			ch := &spans[s].Changes[c]
			want := ExplainedBy(ch.Change.Kind)
			if want == "" || hasKind(ch.Events, want) {
				continue
			}
			var pool []vsphere.VMEvent
			var from []ref
			gather := func(span int, near func(vsphere.VMEvent) bool) {
				for k, e := range spans[span].Unexplained {
					if r := (ref{span, k}); !taken[r] && near(e) {
						pool = append(pool, e)
						from = append(from, r)
					}
				}
			}
			if s > 0 {
				edge := runs[s-1]
				gather(s-1, func(e vsphere.VMEvent) bool {
					t, _ := onRunClock(e, edge)
					return !t.Before(edge.StartedAt.Add(-toleranceAt(e, edge))) && t.Before(edge.StartedAt)
				})
			}
			if s < len(runs) && s+1 < len(spans) {
				edge := runs[s]
				gather(s+1, func(e vsphere.VMEvent) bool {
					t, _ := onRunClock(e, edge)
					return t.After(runEnd(edge)) && !t.After(runEnd(edge).Add(toleranceAt(e, edge)))
				})
			}
			picked := explainers(ch.Change, pool, make([]bool, len(pool)))
			for _, k := range picked {
				taken[from[k]] = true
				ch.Events = append(ch.Events, pool[k])
			}
			if len(picked) > 0 {
				vsphere.SortVMEvents(ch.Events)
			}
		}
	}
	for s := range spans {
		kept := spans[s].Unexplained[:0:0]
		for k, e := range spans[s].Unexplained {
			if !taken[ref{s, k}] {
				kept = append(kept, e)
			}
		}
		spans[s].Unexplained = kept
	}
}

// hasKind reports whether any event is of the kind that can produce a change.
func hasKind(events []vsphere.VMEvent, kind string) bool {
	for _, e := range events {
		if e.Explains == kind {
			return true
		}
	}
	return false
}

// runEnd is when a run stopped collecting: its finish time, or its start for
// a run that never recorded one.
func runEnd(r Run) time.Time {
	if r.FinishedAt.After(r.StartedAt) {
		return r.FinishedAt
	}
	return r.StartedAt
}

// timelineRuns is every distinct run in a timeline, oldest first.
func timelineRuns(history []VMHistoryEvent) []Run {
	seen := make(map[int64]bool, len(history))
	var runs []Run
	for _, h := range history {
		if h.Run.ID == 0 || seen[h.Run.ID] {
			continue
		}
		seen[h.Run.ID] = true
		runs = append(runs, h.Run)
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt.Before(runs[j].StartedAt) })
	return runs
}

// explainSpan attaches events to the span's changes and reports which of
// them it used.
func explainSpan(span *EventSpan, events []vsphere.VMEvent) []bool {
	used := make([]bool, len(events))
	for c := range span.Changes {
		ch := &span.Changes[c]
		for _, i := range explainers(ch.Change, events, used) {
			used[i] = true
			ch.Events = append(ch.Events, events[i])
		}
		vsphere.SortVMEvents(ch.Events)
	}
	return used
}

// explainers picks the events, by index, that account for one change.
func explainers(change VMHistoryEvent, events []vsphere.VMEvent, used []bool) []int {
	want := ExplainedBy(change.Kind)
	if want == "" {
		return nil
	}
	var candidates []int
	for i, e := range events {
		if used[i] || e.Result == vsphere.ResultFailed {
			continue
		}
		if e.Explains != want && !(change.Kind == "renamed" && isNameReconfiguration(e)) {
			continue
		}
		candidates = append(candidates, i)
	}
	if len(candidates) == 0 {
		return nil
	}
	switch change.Kind {
	case "moved":
		return pickMigration(change, events, candidates)
	case "renamed":
		// vCenter logs one rename twice: as a rename event and as a
		// reconfiguration that set the name. Both are the cause.
		after := fieldAfter(change, "name")
		var out []int
		renamed := false
		for k := len(candidates) - 1; k >= 0; k-- {
			e := events[candidates[k]]
			switch {
			case isNameReconfiguration(e):
				if after == "" || e.Detail == "name "+after {
					out = append(out, candidates[k])
				}
			case !renamed && (e.NewName == "" || after == "" || e.NewName == after):
				renamed = true
				out = append(out, candidates[k])
			}
		}
		return out
	case "modified":
		return pickReconfigurations(change, events, candidates)
	default:
		// Lifecycle and snapshot changes: the earliest event of the kind.
		return candidates[:1]
	}
}

// pickReconfigurations chooses the reconfigurations that produced a stored
// modification. One must touch a changed field, and for cpu and memory, where
// the event says what it set, set the value the later run recorded: a
// reconfigure to 512 MB does not explain memory that ended at 256. A change
// to a field is credited to the first event that brought it to the recorded
// value; a later one that set it again changed nothing.
func pickReconfigurations(change VMHistoryEvent, events []vsphere.VMEvent, candidates []int) []int {
	changed := reconfigurableFields(change)
	if len(changed) == 0 {
		return nil
	}
	ordered := append([]int(nil), candidates...)
	sort.SliceStable(ordered, func(a, b int) bool { return events[ordered[a]].Time.Before(events[ordered[b]].Time) })
	reached := make(map[string]bool)
	var out []int
	for _, i := range ordered {
		e := events[i]
		if len(e.Fields) == 0 {
			out = append(out, i)
			continue
		}
		explains, fresh := false, false
		var brought []string
		for _, f := range e.Fields {
			if !intersects([]string{f}, changed) {
				continue
			}
			value, ok := e.ReconfiguredValue(f)
			switch {
			case !ok:
				explains, fresh = true, true
			case value == fieldAfter(change, f):
				explains = true
				fresh = fresh || !reached[f]
				brought = append(brought, f)
			}
		}
		if !explains || !fresh {
			continue
		}
		for _, f := range brought {
			reached[f] = true
		}
		out = append(out, i)
	}
	return out
}

// isNameReconfiguration is a reconfiguration that set only the VM's name:
// the second record vCenter writes for a rename.
func isNameReconfiguration(e vsphere.VMEvent) bool {
	return e.Explains == vsphere.EventModified && len(e.Fields) == 1 && e.Fields[0] == "name"
}

// ExplainedBy maps a stored change kind to the event kind that can produce it.
func ExplainedBy(kind string) string {
	switch kind {
	case "first_seen", "appeared":
		return vsphere.EventCreated
	case "vanished":
		return vsphere.EventRemoved
	case "moved":
		return vsphere.EventMoved
	case "renamed":
		return vsphere.EventRenamed
	case "modified":
		return vsphere.EventModified
	case "snapshot-created":
		return vsphere.EventSnapshotCreated
	case "snapshot-removed":
		return vsphere.EventSnapshotRemoved
	}
	return ""
}

// pickMigration chooses the one migration that produced a stored move. The
// best evidence is a migration from the host the earlier run recorded to the
// host the later run did; failing that, the last migration that ended on the
// recorded host. A migration that went somewhere else and came back explains
// nothing, and stays unexplained.
func pickMigration(change VMHistoryEvent, events []vsphere.VMEvent, candidates []int) []int {
	before, after := fieldBefore(change, "host"), fieldAfter(change, "host")
	if after == "" && change.Observation != nil {
		after = change.Observation.VM.Host
	}
	for k := len(candidates) - 1; k >= 0; k-- {
		e := events[candidates[k]]
		if before != "" && e.FromHost == before && e.ToHost == after {
			return []int{candidates[k]}
		}
	}
	for k := len(candidates) - 1; k >= 0; k-- {
		e := events[candidates[k]]
		if e.ToHost == "" || e.ToHost == after {
			return []int{candidates[k]}
		}
	}
	return nil
}

// reconfigurableFields translates a stored change's fields into the names a
// reconfiguration event uses. Tag and custom attribute changes are not made
// by reconfiguring a VM, so they have no counterpart.
func reconfigurableFields(change VMHistoryEvent) []string {
	var out []string
	for _, f := range change.Changes {
		switch {
		case strings.HasPrefix(f.Field, "metadata."):
		case f.Field == "cpu" || f.Field == "memory" || f.Field == "annotation" || f.Field == "guest_os" || f.Field == "name":
			out = append(out, f.Field)
		default:
			out = append(out, "devices")
		}
	}
	return out
}

func fieldBefore(change VMHistoryEvent, name string) string {
	for _, f := range change.Changes {
		if f.Field == name {
			return f.Before
		}
	}
	return ""
}

func fieldAfter(change VMHistoryEvent, name string) string {
	for _, f := range change.Changes {
		if f.Field == name {
			return f.After
		}
	}
	return ""
}

func intersects(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}
