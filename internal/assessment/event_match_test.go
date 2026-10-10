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
