package assessment

import (
	"sort"
	"strings"

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
	pending := make([][]vsphere.VMEvent, len(spans))
	for _, e := range sorted {
		i := sort.Search(len(runs), func(i int) bool { return !runs[i].StartedAt.Before(e.Time) })
		pending[i] = append(pending[i], e)
	}

	out := make([]EventSpan, 0, len(spans))
	for i := range spans {
		explainSpan(&spans[i], pending[i])
		if len(spans[i].Changes) == 0 && len(spans[i].Unexplained) == 0 {
			continue
		}
		out = append(out, spans[i])
	}
	return out
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

func explainSpan(span *EventSpan, events []vsphere.VMEvent) {
	used := make([]bool, len(events))
	for c := range span.Changes {
		ch := &span.Changes[c]
		for _, i := range explainers(ch.Change, events, used) {
			used[i] = true
			ch.Events = append(ch.Events, events[i])
		}
	}
	for i, e := range events {
		if !used[i] {
			span.Unexplained = append(span.Unexplained, e)
		}
	}
}

// explainers picks the events, by index, that account for one change.
func explainers(change VMHistoryEvent, events []vsphere.VMEvent, used []bool) []int {
	want := ExplainedBy(change.Kind)
	if want == "" {
		return nil
	}
	var candidates []int
	for i, e := range events {
		if used[i] || e.Explains != want || e.Result == vsphere.ResultFailed {
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
		after := fieldAfter(change, "name")
		for k := len(candidates) - 1; k >= 0; k-- {
			e := events[candidates[k]]
			if e.NewName == "" || after == "" || e.NewName == after {
				return []int{candidates[k]}
			}
		}
		return nil
	case "modified":
		changed := reconfigurableFields(change)
		if len(changed) == 0 {
			return nil
		}
		var out []int
		for _, i := range candidates {
			if len(events[i].Fields) == 0 || intersects(events[i].Fields, changed) {
				out = append(out, i)
			}
		}
		return out
	default:
		// Lifecycle and snapshot changes: the earliest event of the kind.
		return candidates[:1]
	}
}

// explainedBy maps a stored change kind to the event kind that can produce it.
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
