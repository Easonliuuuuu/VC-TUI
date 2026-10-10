package assessment

import (
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func eventAt(m, d, h int) time.Time { return time.Date(2026, time.Month(m), d, h, 0, 0, 0, time.UTC) }

// TestExplainTimelineAttachesEventsToTheChangesTheyCaused pins the matching
// rules on the case the design was drawn from: a memory change and a DRS move
// recorded by one run, with a vMotion that went out and came back in the same
// span, a snapshot in the next, a failed reconfiguration, and a migration
// newer than any run.
func TestExplainTimelineAttachesEventsToTheChangesTheyCaused(t *testing.T) {
	r1 := Run{ID: 1, StartedAt: eventAt(8, 2, 0)}
	r7 := Run{ID: 7, StartedAt: eventAt(9, 13, 0)}
	r8 := Run{ID: 8, StartedAt: eventAt(9, 20, 0)}
	r9 := Run{ID: 9, StartedAt: eventAt(9, 27, 0)}
	obs := &Observation{VM: vsphere.VM{Host: "esxi-02"}}
	history := []VMHistoryEvent{
		{Kind: "first_seen", Run: r1},
		{Kind: "observed", Run: r7},
		{Kind: "modified", Run: r8, Changes: []FieldChange{{Field: "memory", Before: "6144", After: "8192"}}},
		{Kind: "moved", Run: r8, Changes: []FieldChange{{Field: "host", Before: "esxi-01", After: "esxi-02"}}, Observation: obs},
		{Kind: "snapshot-created", Run: r9},
	}
	reconfigure := vsphere.VMEvent{Key: 1, Time: eventAt(9, 14, 10), Label: "reconfigure", Explains: vsphere.EventModified, Result: vsphere.ResultOK, Fields: []string{"memory"}}
	drs := vsphere.VMEvent{Key: 2, Time: eventAt(9, 16, 2), Label: "drs migrate", Explains: vsphere.EventMoved, Result: vsphere.ResultOK, FromHost: "esxi-01", ToHost: "esxi-02"}
	out := vsphere.VMEvent{Key: 3, Time: eventAt(9, 18, 3), Label: "migrate", Explains: vsphere.EventMoved, Result: vsphere.ResultOK, FromHost: "esxi-02", ToHost: "esxi-03"}
	back := vsphere.VMEvent{Key: 4, Time: eventAt(9, 18, 5), Label: "migrate", Explains: vsphere.EventMoved, Result: vsphere.ResultOK, FromHost: "esxi-03", ToHost: "esxi-02"}
	snap := vsphere.VMEvent{Key: 5, Time: eventAt(9, 22, 21), Label: "snapshot create", Explains: vsphere.EventSnapshotCreated}
	failed := vsphere.VMEvent{Key: 6, Time: eventAt(9, 23, 21), Label: "reconfigure", Explains: vsphere.EventModified, Result: vsphere.ResultFailed}
	later := vsphere.VMEvent{Key: 7, Time: eventAt(10, 9, 16), Label: "migrate", Explains: vsphere.EventMoved, Result: vsphere.ResultOK}
	// Deliberately out of order: listings from several vCenters arrive so.
	events := []vsphere.VMEvent{later, back, snap, drs, failed, out, reconfigure}

	spans := ExplainTimeline(history, events)
	if len(spans) != 4 {
		t.Fatalf("got %d spans, want #1, #7→#8, #8→#9 and since #9: %+v", len(spans), spans)
	}

	first := spans[0]
	if first.From != nil || first.To.ID != 1 || len(first.Changes) != 1 || len(first.Changes[0].Events) != 0 {
		t.Fatalf("first span = %+v", first)
	}

	mid := spans[1]
	if mid.From.ID != 7 || mid.To.ID != 8 || len(mid.Changes) != 2 {
		t.Fatalf("#7→#8 span = %+v", mid)
	}
	if got := mid.Changes[0].Events; len(got) != 1 || got[0].Key != reconfigure.Key {
		t.Fatalf("memory change explained by %+v, want the reconfigure", got)
	}
	if got := mid.Changes[1].Events; len(got) != 1 || got[0].Key != drs.Key {
		t.Fatalf("move explained by %+v, want the DRS migration from the recorded host", got)
	}
	if len(mid.Unexplained) != 2 || mid.Unexplained[0].Key != out.Key || mid.Unexplained[1].Key != back.Key {
		t.Fatalf("the out-and-back vMotion must stay unexplained, oldest first: %+v", mid.Unexplained)
	}

	next := spans[2]
	if next.From.ID != 8 || next.To.ID != 9 || len(next.Changes[0].Events) != 1 || next.Changes[0].Events[0].Key != snap.Key {
		t.Fatalf("#8→#9 span = %+v", next)
	}
	if len(next.Unexplained) != 1 || next.Unexplained[0].Key != failed.Key {
		t.Fatalf("a failed reconfigure explains nothing and stays visible: %+v", next.Unexplained)
	}

	since := spans[3]
	if since.From.ID != 9 || since.To != nil || len(since.Changes) != 0 || len(since.Unexplained) != 1 || since.Unexplained[0].Key != later.Key {
		t.Fatalf("since-last-run span = %+v", since)
	}
}

func TestExplainTimelineRespectsFieldsAndNames(t *testing.T) {
	r1 := Run{ID: 1, StartedAt: eventAt(9, 1, 0)}
	r2 := Run{ID: 2, StartedAt: eventAt(9, 8, 0)}
	history := []VMHistoryEvent{
		{Kind: "observed", Run: r1},
		{Kind: "modified", Run: r2, Changes: []FieldChange{{Field: "cpu", Before: "2", After: "4"}, {Field: "metadata.tags[a/b]", After: "x"}}},
		{Kind: "renamed", Run: r2, Changes: []FieldChange{{Field: "name", Before: "web-01", After: "web-01-old"}}},
	}
	memoryOnly := vsphere.VMEvent{Key: 1, Time: eventAt(9, 2, 0), Explains: vsphere.EventModified, Result: vsphere.ResultOK, Fields: []string{"memory"}}
	unknown := vsphere.VMEvent{Key: 2, Time: eventAt(9, 3, 0), Explains: vsphere.EventModified, Result: vsphere.ResultOK}
	wrongName := vsphere.VMEvent{Key: 3, Time: eventAt(9, 4, 0), Explains: vsphere.EventRenamed, Result: vsphere.ResultOK, NewName: "something-else"}
	rightName := vsphere.VMEvent{Key: 4, Time: eventAt(9, 5, 0), Explains: vsphere.EventRenamed, Result: vsphere.ResultOK, NewName: "web-01-old"}

	spans := ExplainTimeline(history, []vsphere.VMEvent{memoryOnly, unknown, wrongName, rightName})
	if len(spans) != 1 {
		t.Fatalf("spans = %+v", spans)
	}
	s := spans[0]
	if got := s.Changes[0].Events; len(got) != 1 || got[0].Key != unknown.Key {
		t.Fatalf("a cpu change is explained by %+v; want only the reconfigure that does not say what it touched", got)
	}
	if got := s.Changes[1].Events; len(got) != 1 || got[0].Key != rightName.Key {
		t.Fatalf("rename explained by %+v, want the event with the recorded new name", got)
	}
	if len(s.Unexplained) != 2 {
		t.Fatalf("unexplained = %+v, want the memory reconfigure and the other rename", s.Unexplained)
	}
}

func TestExplainTimelineWithoutEventsKeepsEveryChange(t *testing.T) {
	r1 := Run{ID: 1, StartedAt: eventAt(9, 1, 0)}
	r2 := Run{ID: 2, StartedAt: eventAt(9, 8, 0)}
	history := []VMHistoryEvent{{Kind: "first_seen", Run: r1}, {Kind: "observed", Run: r2}, {Kind: "vanished", Run: Run{ID: 3, StartedAt: eventAt(9, 15, 0)}}}
	spans := ExplainTimeline(history, nil)
	if len(spans) != 2 || spans[0].Changes[0].Change.Kind != "first_seen" || spans[1].Changes[0].Change.Kind != "vanished" || spans[1].From.ID != 2 {
		t.Fatalf("spans = %+v", spans)
	}
}

// TestExplainTimelinePlacesEventsLoggedWhileARunCollected: a run reaches each
// VM some time after it starts, so an event logged during the run may be in
// that run's observation (and explains its change) or only in the next one.
func TestExplainTimelinePlacesEventsLoggedWhileARunCollected(t *testing.T) {
	r1 := Run{ID: 1, StartedAt: eventAt(9, 1, 0)}
	r2 := Run{ID: 2, StartedAt: eventAt(9, 8, 10), FinishedAt: eventAt(9, 8, 11)}
	r3 := Run{ID: 3, StartedAt: eventAt(9, 15, 0)}
	history := []VMHistoryEvent{
		{Kind: "observed", Run: r1},
		{Kind: "moved", Run: r2, Changes: []FieldChange{{Field: "host", Before: "esxi-01", After: "esxi-02"}}},
		{Kind: "snapshot-created", Run: r3},
	}
	// Both are logged after run 2 started but before it finished.
	migrate := vsphere.VMEvent{Key: 1, Time: eventAt(9, 8, 10).Add(3 * time.Minute), Explains: vsphere.EventMoved, Result: vsphere.ResultOK, FromHost: "esxi-01", ToHost: "esxi-02"}
	snap := vsphere.VMEvent{Key: 2, Time: eventAt(9, 8, 10).Add(40 * time.Minute), Explains: vsphere.EventSnapshotCreated}
	spans := ExplainTimeline(history, []vsphere.VMEvent{migrate, snap})
	if len(spans) != 2 {
		t.Fatalf("spans = %+v", spans)
	}
	if got := spans[0].Changes[0].Events; len(got) != 1 || got[0].Key != migrate.Key || len(spans[0].Unexplained) != 0 {
		t.Fatalf("the migration logged during run 2 must explain run 2's move: %+v", spans[0])
	}
	if got := spans[1].Changes[0].Events; len(got) != 1 || got[0].Key != snap.Key {
		t.Fatalf("the snapshot logged during run 2 must carry to run 3's change: %+v", spans[1])
	}
}

// probeAt is a time in the second-resolution lab session the clock-skew tests
// replay: seconds after 13:34:00 UTC.
func probeAt(sec float64) time.Time {
	return time.Date(2026, 10, 10, 13, 34, 0, 0, time.UTC).Add(time.Duration(sec * float64(time.Second)))
}

// probeScenario is the ground-truth session behind the clock-skew bug: a
// throwaway VM changed in steps, one assessment after each, every step
// strictly after the previous run finished by the local clock. The vCenter's
// clock was about 0.8 s behind, so every event carries a time ~0.8 s earlier
// than the run that follows it, and the rename and the snapshot removal even
// read as logged before the run that preceded them started. offset, when
// non-nil, is stored with every run as the clock offset it measured.
func probeScenario(offset *time.Duration) (history []VMHistoryEvent, events []vsphere.VMEvent) {
	run := func(id int64, start, end float64) Run {
		r := Run{ID: id, StartedAt: probeAt(start), FinishedAt: probeAt(end)}
		if offset != nil {
			r.ClockOffsetsMS = map[string]int64{"lab": offset.Milliseconds()}
		}
		return r
	}
	r11, r12, r13, r14, r15 := run(11, 41.0, 41.4), run(12, 43.458, 44.038), run(13, 44.8, 45.3), run(14, 45.654, 46.23), run(15, 46.7, 47.2)
	history = []VMHistoryEvent{
		{Kind: "observed", Run: r11},
		{Kind: "modified", Run: r12, Changes: []FieldChange{{Field: "cpu", Before: "1", After: "2"}, {Field: "memory", Before: "128", After: "256"}}},
		{Kind: "snapshot-created", Run: r12},
		{Kind: "renamed", Run: r13, Changes: []FieldChange{{Field: "name", Before: "tl-probe", After: "tl-probe-renamed"}}},
		{Kind: "observed", Run: r14},
		{Kind: "snapshot-removed", Run: r15},
	}
	vc := func(key int32, sec float64, e vsphere.VMEvent) vsphere.VMEvent {
		e.Context, e.Key, e.Time, e.Result = "lab", key, probeAt(sec), vsphere.ResultOK
		return e
	}
	events = []vsphere.VMEvent{
		vc(1, 41.9, vsphere.VMEvent{Label: "reconfigure", Explains: vsphere.EventModified, Fields: []string{"cpu", "memory"}, Detail: "cpu 2 · memory 256 MB"}),
		vc(2, 42.0, vsphere.VMEvent{Label: "snapshot create", Explains: vsphere.EventSnapshotCreated}),
		vc(3, 43.326, vsphere.VMEvent{Label: "rename", Explains: vsphere.EventRenamed, NewName: "tl-probe-renamed"}),
		vc(4, 43.329, vsphere.VMEvent{Label: "reconfigure", Explains: vsphere.EventModified, Fields: []string{"name"}, Detail: "name tl-probe-renamed"}),
		vc(5, 43.634, vsphere.VMEvent{Label: "reconfigure", Explains: vsphere.EventModified, Fields: []string{"memory"}, Detail: "memory 512 MB"}),
		vc(6, 43.802, vsphere.VMEvent{Label: "reconfigure", Explains: vsphere.EventModified, Fields: []string{"memory"}, Detail: "memory 256 MB"}),
		vc(7, 45.615, vsphere.VMEvent{Label: "snapshot remove", Explains: vsphere.EventSnapshotRemoved}),
	}
	return history, events
}

func eventKeys(events []vsphere.VMEvent) []int32 {
	keys := make([]int32, len(events))
	for i, e := range events {
		keys[i] = e.Key
	}
	return keys
}

func sameKeys(got []vsphere.VMEvent, want ...int32) bool {
	keys := eventKeys(got)
	if len(keys) != len(want) {
		return false
	}
	for i := range keys {
		if keys[i] != want[i] {
			return false
		}
	}
	return true
}

func spanTo(t *testing.T, spans []EventSpan, run int64) EventSpan {
	t.Helper()
	for _, s := range spans {
		if s.To != nil && s.To.ID == run {
			return s
		}
	}
	t.Fatalf("no span ends at run #%d: %+v", run, spans)
	return EventSpan{}
}

// checkProbeAttribution holds the ground truth of the probe session: each
// event in the gap of the change it caused, and the undone memory edits left
// as unexplained events in the gap after the run that did not see them.
func checkProbeAttribution(t *testing.T, spans []EventSpan) {
	t.Helper()
	s1 := spanTo(t, spans, 12)
	if len(s1.Changes) != 2 || !sameKeys(s1.Changes[0].Events, 1) || !sameKeys(s1.Changes[1].Events, 2) {
		t.Fatalf("#11→#12: the setup reconfigure and snapshot must explain their changes, and the memory edits made after #12 must not: %+v", s1)
	}
	if len(s1.Unexplained) != 0 {
		t.Fatalf("#11→#12 unexplained = %+v, want none", s1.Unexplained)
	}
	rename := spanTo(t, spans, 13)
	if len(rename.Changes) != 1 || !sameKeys(rename.Changes[0].Events, 3, 4) {
		t.Fatalf("#12→#13: renamed must be explained by the rename and its name reconfigure: %+v", rename)
	}
	if !sameKeys(rename.Unexplained, 5, 6) {
		t.Fatalf("#12→#13 unexplained = %+v, want the 512 MB and 256 MB edits that were undone", rename.Unexplained)
	}
	removal := spanTo(t, spans, 15)
	if len(removal.Changes) != 1 || !sameKeys(removal.Changes[0].Events, 7) {
		t.Fatalf("#14→#15: snapshot-removed must be explained by the snapshot removal: %+v", removal)
	}
	for _, s := range spans {
		if s.To != nil && s.To.ID == 14 {
			t.Fatalf("#13→#14 has neither changes nor events and must be left out: %+v", s)
		}
	}
}

// TestExplainTimelineShiftsEventsByTheOffsetTheRunsMeasured is the issue's
// table with the offsets stored: events are moved onto the local clock before
// they are placed, so no tolerance is needed.
func TestExplainTimelineShiftsEventsByTheOffsetTheRunsMeasured(t *testing.T) {
	offset := -779 * time.Millisecond
	history, events := probeScenario(&offset)
	checkProbeAttribution(t, ExplainTimeline(history, events))
}

// TestExplainTimelineToleratesSkewForRunsWithoutAnOffset is the same session
// as stored before offsets were recorded: the events sit up to 0.7 s on the
// wrong side of run edges, and attribution has to come from the tolerance.
func TestExplainTimelineToleratesSkewForRunsWithoutAnOffset(t *testing.T) {
	history, events := probeScenario(nil)
	checkProbeAttribution(t, ExplainTimeline(history, events))
}

func TestExplainTimelineToleranceReachesBothWaysAcrossARunEdge(t *testing.T) {
	r1 := Run{ID: 1, StartedAt: probeAt(0), FinishedAt: probeAt(1)}
	r2 := Run{ID: 2, StartedAt: probeAt(100), FinishedAt: probeAt(101)}
	r3 := Run{ID: 3, StartedAt: probeAt(200), FinishedAt: probeAt(201)}
	event := func(key int32, sec float64, kind string) vsphere.VMEvent {
		return vsphere.VMEvent{Key: key, Time: probeAt(sec), Explains: kind, Result: vsphere.ResultOK}
	}
	snapshot := func(r Run) []VMHistoryEvent {
		return []VMHistoryEvent{{Kind: "observed", Run: r1}, {Kind: "snapshot-created", Run: r}, {Kind: "observed", Run: r3}}
	}

	// A vCenter ahead of the machine: the snapshot taken before run 2 was
	// logged 1.4 s after run 2 finished. Run 2 saw it, so run 2 owns it.
	spans := ExplainTimeline(snapshot(r2), []vsphere.VMEvent{event(1, 102.4, vsphere.EventSnapshotCreated)})
	if got := spanTo(t, spans, 2); !sameKeys(got.Changes[0].Events, 1) {
		t.Fatalf("an event just after a run, for a change that run recorded: %+v", spans)
	}

	// A vCenter behind the machine: the snapshot taken after run 1 was
	// logged 1.4 s before run 1 started.
	spans = ExplainTimeline([]VMHistoryEvent{{Kind: "observed", Run: r1}, {Kind: "snapshot-created", Run: r2}}, []vsphere.VMEvent{event(1, -1.4, vsphere.EventSnapshotCreated)})
	if len(spans) == 0 || !sameKeys(spanTo(t, spans, 2).Changes[0].Events, 1) {
		t.Fatalf("an event just before the earlier run, for the change after it: %+v", spans)
	}

	// Outside the tolerance the event stays where the clock put it, and the
	// change stays unexplained.
	spans = ExplainTimeline([]VMHistoryEvent{{Kind: "observed", Run: r1}, {Kind: "snapshot-created", Run: r2}}, []vsphere.VMEvent{event(1, -(boundaryTolerance.Seconds() + 1), vsphere.EventSnapshotCreated)})
	if got := spanTo(t, spans, 2); len(got.Changes[0].Events) != 0 {
		t.Fatalf("an event %v before the run edge explained a change: %+v", boundaryTolerance+time.Second, got)
	}
	if spans[0].To == nil || spans[0].To.ID != 1 || len(spans[0].Unexplained) != 1 {
		t.Fatalf("the distant event must stay unexplained in its own gap: %+v", spans)
	}
}

// With a measured offset there is nothing left to forgive: an event 2 s before
// a run is before it, and does not explain the change after.
func TestExplainTimelineAppliesNoToleranceWhereTheOffsetWasMeasured(t *testing.T) {
	offset := time.Duration(0)
	measured := func(id int64, start float64) Run {
		return Run{ID: id, StartedAt: probeAt(start), FinishedAt: probeAt(start + 1), ClockOffsetsMS: map[string]int64{"lab": offset.Milliseconds()}}
	}
	r1, r2 := measured(1, 100), measured(2, 200)
	history := []VMHistoryEvent{{Kind: "observed", Run: r1}, {Kind: "snapshot-created", Run: r2}}
	spans := ExplainTimeline(history, []vsphere.VMEvent{{Context: "lab", Key: 1, Time: probeAt(98), Explains: vsphere.EventSnapshotCreated, Result: vsphere.ResultOK}})
	if got := spanTo(t, spans, 2); len(got.Changes[0].Events) != 0 {
		t.Fatalf("a measured clock still tolerated skew: %+v", got)
	}
}

// A reconfigure that set memory to something else did not cause the recorded
// value, and of two that did, the first is the cause.
func TestExplainTimelineChecksReconfiguredValuesAgainstTheRecordedOnes(t *testing.T) {
	r1 := Run{ID: 1, StartedAt: probeAt(0)}
	r2 := Run{ID: 2, StartedAt: probeAt(100)}
	history := []VMHistoryEvent{
		{Kind: "observed", Run: r1},
		{Kind: "modified", Run: r2, Changes: []FieldChange{{Field: "memory", Before: "128", After: "256"}, {Field: "annotation", After: "x"}}},
	}
	reconfigure := func(key int32, sec float64, detail string, fields ...string) vsphere.VMEvent {
		return vsphere.VMEvent{Key: key, Time: probeAt(sec), Explains: vsphere.EventModified, Result: vsphere.ResultOK, Fields: fields, Detail: detail}
	}
	events := []vsphere.VMEvent{
		reconfigure(1, 10, "memory 512 MB", "memory"),
		reconfigure(2, 20, "memory 256 MB", "memory"),
		reconfigure(3, 30, "memory 256 MB", "memory"),
		reconfigure(4, 40, "annotation", "annotation"),
		reconfigure(5, 50, "annotation", "annotation"),
		reconfigure(6, 60, "cpu 8", "cpu"),
	}
	spans := ExplainTimeline(history, events)
	got := spanTo(t, spans, 2)
	if !sameKeys(got.Changes[0].Events, 2, 4, 5) {
		t.Fatalf("modified explained by %v, want the first 256 MB edit and both annotation edits", eventKeys(got.Changes[0].Events))
	}
	if !sameKeys(got.Unexplained, 1, 3, 6) {
		t.Fatalf("unexplained %v, want the 512 MB edit, the repeated 256 MB edit and the unrelated cpu edit", eventKeys(got.Unexplained))
	}
}
