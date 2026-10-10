package tui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// eventsBackend is a fake that also reads vCenter events, recording every
// read so the tests can pin when the timeline talks to a vCenter.
type eventsBackend struct {
	*fakeBackend
	mu     sync.Mutex
	reads  []string
	events map[string][]vsphere.VMEvent
}

func (b *eventsBackend) VMEvents(_ context.Context, cc *config.Context, vmID string, limit int) (vsphere.VMEventListing, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reads = append(b.reads, cc.Name+"/"+vmID)
	return vsphere.VMEventListing{Context: cc.Name, VMID: vmID, Limit: limit, Events: b.events[cc.Name+"/"+vmID]}, nil
}

func (b *eventsBackend) readCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.reads)
}

// billingRemoved is the event that explains oneVMDiffStore's vanished VM:
// billing was removed between its two runs.
func billingRemoved() vsphere.VMEvent {
	return vsphere.VMEvent{Context: "prod", Key: 11, Time: time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC), Type: "VmRemovedEvent",
		Label: "remove", Explains: vsphere.EventRemoved, Result: vsphere.ResultOK, User: "ops@corp.example"}
}

func timelineModel(t *testing.T, b Backend) *Model {
	t.Helper()
	store := oneVMDiffStore(t)
	m := New(context.Background(), b, Options{Current: "prod", Assessment: &assessment.Service{Store: store}, RefreshInterval: -1, Handoff: &fakeHandoff{}})
	m.width, m.height = 140, 40
	drive(t, m, m.Init())
	return m
}

func viewText(m *Model) string { return ansi.Strip(m.View()) }

// TestTimelineReadsEventsOnlyWhenAsked pins the promise the events tab makes:
// opening the timeline, and staying on the stored Changes tab, never talks to
// a vCenter. The first visit to an events tab reads once; coming back reuses
// that read; r reads again.
func TestTimelineReadsEventsOnlyWhenAsked(t *testing.T) {
	b := &eventsBackend{fakeBackend: twoHealthy(), events: map[string][]vsphere.VMEvent{"prod/vm-1": {billingRemoved()}}}
	m := timelineModel(t, b)
	drive(t, m, m.openTimeline("billing", modeBrowse, nil))
	if m.timelineSource != timelineSourceChanges {
		t.Fatalf("the timeline opened on source %d, want Changes", m.timelineSource)
	}
	if n := b.readCount(); n != 0 {
		t.Fatalf("opening the stored timeline read vCenter events %d times", n)
	}
	if got := viewText(m); !strings.Contains(got, "[1 Changes]") || strings.Contains(got, "LIVE") {
		t.Fatalf("the Changes tab must be marked and never claim to be live:\n%s", got)
	}

	press(t, m, "2")
	if n := b.readCount(); n != 1 {
		t.Fatalf("the events tab read %d times, want 1: %v", n, b.reads)
	}
	got := viewText(m)
	for _, want := range []string{"[2 vCenter events]", "LIVE from prod", "remove", "ops@corp.example"} {
		if !strings.Contains(got, want) {
			t.Fatalf("events tab missing %q:\n%s", want, got)
		}
	}

	press(t, m, "1", "3")
	if n := b.readCount(); n != 1 {
		t.Fatalf("returning to an events tab read again (%d reads)", n)
	}
	press(t, m, "r")
	if n := b.readCount(); n != 2 {
		t.Fatalf("r did not read again (%d reads)", n)
	}
}

// TestTimelineCombinedExplainsTheStoredChange checks the Combined tab end to
// end: the stored "vanished" is shown with the removal that caused it, and
// the event's detail says which run recorded its effect.
func TestTimelineCombinedExplainsTheStoredChange(t *testing.T) {
	b := &eventsBackend{fakeBackend: twoHealthy(), events: map[string][]vsphere.VMEvent{"prod/vm-1": {billingRemoved()}}}
	m := timelineModel(t, b)
	drive(t, m, m.openTimeline("billing", modeBrowse, nil))
	press(t, m, "3")
	got := viewText(m)
	if !strings.Contains(got, "[3 Combined]") || !strings.Contains(got, "vanished") {
		t.Fatalf("combined view lost the stored change:\n%s", got)
	}
	var line string
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "vanished") {
			line = l
		}
	}
	if !strings.Contains(line, "← ") || !strings.Contains(line, "remove") {
		t.Fatalf("the removal does not explain the vanished VM on its line: %q", line)
	}

	// The vanished row is the second selectable row, after first_seen; on a
	// wide terminal its explaining event shares the row, so its detail is
	// the change's, which lists the event.
	press(t, m, "down", "enter")
	if m.mode != modeHistoryTimelineDetail {
		t.Fatalf("enter did not open the detail (mode %v)", m.mode)
	}
	if got := viewText(m); !strings.Contains(got, "vCenter events") || !strings.Contains(got, "remove") {
		t.Fatalf("the change detail does not list the event that caused it:\n%s", got)
	}

	// Narrow, the event gets its own selectable row and its own detail.
	press(t, m, "esc")
	m.width = 80
	m.combinedCursor = 0
	press(t, m, "down", "down", "enter")
	got = viewText(m)
	for _, want := range []string{"LIVE", "VmRemovedEvent", "Recorded", "yes", "the vanished stored by run #2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("event detail missing %q:\n%s", want, got)
		}
	}
}

// TestTimelineByColumnGrowsWithTheTerminal: a vSphere SSO user is 27 columns,
// wider than the BY column a narrow terminal affords, so the column widens
// where the room exists and still truncates where it does not. Combined
// keeps less room for it beside the stored change, and more below 110
// columns, where the event gets a row of its own.
func TestTimelineByColumnGrowsWithTheTerminal(t *testing.T) {
	const user = `VSPHERE.LOCAL\Administrator`
	e := billingRemoved()
	e.User, e.Summary = user, "Alarm 'CPU usage' changed from Gray to Green"
	b := &eventsBackend{fakeBackend: twoHealthy(), events: map[string][]vsphere.VMEvent{"prod/vm-1": {e}}}
	m := timelineModel(t, b)
	drive(t, m, m.openTimeline("billing", modeBrowse, nil))
	for _, tab := range []string{"2", "3"} {
		press(t, m, tab)
		for _, tc := range []struct {
			width int
			whole bool
		}{{160, true}, {140, tab == "2"}, {100, tab == "3"}, {80, false}} {
			m.width = tc.width
			got := viewText(m)
			if has := strings.Contains(got, user); has != tc.whole {
				t.Fatalf("tab %s at %d columns: full user shown = %v, want %v:\n%s", tab, tc.width, has, tc.whole, got)
			}
			for _, l := range strings.Split(got, "\n") {
				if ansi.StringWidth(l) > tc.width {
					t.Fatalf("tab %s at %d columns: line is %d wide: %q", tab, tc.width, ansi.StringWidth(l), l)
				}
			}
			if tc.width >= 140 && !strings.Contains(got, "Alarm 'CPU") {
				t.Fatalf("tab %s at %d columns lost the detail:\n%s", tab, tc.width, got)
			}
		}
	}
}

// TestTimelineEventsWaitForAnUnconnectedVCenter: a VM seen on a vCenter that
// is configured but not connected is named, not silently skipped, and is only
// read when r asks — which is what connects it.
func TestTimelineEventsWaitForAnUnconnectedVCenter(t *testing.T) {
	b := &eventsBackend{fakeBackend: twoHealthy(), events: map[string][]vsphere.VMEvent{}}
	m := timelineModel(t, b)
	drive(t, m, m.openTimeline("billing", modeBrowse, &vmEventsTarget{context: "customer-a", vmID: "vm-9"}))
	press(t, m, "2")
	if strings.Join(b.reads, ",") != "prod/vm-1" {
		t.Fatalf("opening the tab read %v, want only the connected prod", b.reads)
	}
	if got := viewText(m); !strings.Contains(got, "customer-a is not connected") || !strings.Contains(got, "r connects") {
		t.Fatalf("the unconnected vCenter is not named:\n%s", got)
	}
	press(t, m, "r")
	// The vCenters are read at once, so in either order.
	if again := append([]string(nil), b.reads[1:]...); len(again) != 2 || !slices.Contains(again, "customer-a/vm-9") || !slices.Contains(again, "prod/vm-1") {
		t.Fatalf("r read %v, want both vCenters", b.reads)
	}
	if got := viewText(m); strings.Contains(got, "customer-a is not connected") || !strings.Contains(got, "LIVE from customer-a, prod") {
		t.Fatalf("after r the events tab should name both vCenters:\n%s", got)
	}
}

func TestTimelineIgnoresEventsForAnEarlierTimeline(t *testing.T) {
	b := &eventsBackend{fakeBackend: twoHealthy(), events: map[string][]vsphere.VMEvent{}}
	m := timelineModel(t, b)
	drive(t, m, m.openTimeline("billing", modeBrowse, nil))
	m.tlEvents = &timelineEventsState{loading: true}
	stale := m.tlGen - 1
	m.applyVMEvents(vmEventsMsg{generation: stale, results: []vmEventsResult{{target: vmEventsTarget{context: "prod", vmID: "vm-1"}, listing: vsphere.VMEventListing{Events: []vsphere.VMEvent{billingRemoved()}}}}})
	if m.tlEvents.loaded || len(m.timelineEvents()) != 0 {
		t.Fatal("a reply for an earlier timeline replaced the open one's events")
	}
}

func TestTimelineWithoutAnEventsBackendSaysSo(t *testing.T) {
	m := timelineModel(t, twoHealthy())
	drive(t, m, m.openTimeline("billing", modeBrowse, nil))
	press(t, m, "2")
	if got := viewText(m); !strings.Contains(got, "no live vCenter to read events from") {
		t.Fatalf("the events tab does not explain why it is empty:\n%s", got)
	}
	press(t, m, "3")
	if got := viewText(m); !strings.Contains(got, "stored changes only") || !strings.Contains(got, "vanished") {
		t.Fatalf("the combined tab must fall back to stored changes:\n%s", got)
	}
}

// TestTimelineChangesTabTogglesUnchangedRunsWithoutReloading: the store is
// asked for every observation once, and "a" only changes what is listed.
func TestTimelineChangesTabTogglesUnchangedRunsWithoutReloading(t *testing.T) {
	m := timelineModel(t, twoHealthy())
	drive(t, m, m.openTimeline("billing", modeBrowse, nil))
	before := len(m.timeline)
	for _, e := range m.timeline {
		if e.Kind == "observed" {
			t.Fatalf("unchanged observations listed before a was pressed: %+v", m.timeline)
		}
	}
	m.timelineFull = append(m.timelineFull, assessment.VMHistoryEvent{Kind: "observed", Run: assessment.Run{ID: 99}})
	press(t, m, "a")
	if len(m.timeline) != before+1 {
		t.Fatalf("a listed %d rows, want %d", len(m.timeline), before+1)
	}
}

// TestTimelineReadsVCentersHistoryNamesWhileTheSeedReadRuns: opening the
// timeline on an events tab reads the live VM at once, before stored history
// has said where else the VM was. When history arrives naming another
// managed object, that one is read too rather than waiting for r.
func TestTimelineReadsVCentersHistoryNamesWhileTheSeedReadRuns(t *testing.T) {
	b := &eventsBackend{fakeBackend: twoHealthy(), events: map[string][]vsphere.VMEvent{"prod/vm-1": {billingRemoved()}}}
	m := timelineModel(t, b)
	m.timelineSource = timelineSourceCombined
	drive(t, m, m.openTimeline("billing", modeBrowse, &vmEventsTarget{context: "prod", vmID: "vm-77"}))
	slices.Sort(b.reads)
	if strings.Join(b.reads, ",") != "prod/vm-1,prod/vm-77" {
		t.Fatalf("read %v, want the seed and the stored managed object", b.reads)
	}
	if got := viewText(m); !strings.Contains(got, "remove") {
		t.Fatalf("the stored managed object's events are missing:\n%s", got)
	}
}

// TestTimelineIgnoresHistoryForAnEarlierTimeline: stored history that
// arrives after its timeline was replaced must not replace the open one.
func TestTimelineIgnoresHistoryForAnEarlierTimeline(t *testing.T) {
	m := timelineModel(t, twoHealthy())
	drive(t, m, m.openTimeline("billing", modeBrowse, nil))
	want := len(m.timelineFull)
	m.applyHistoryTimeline(historyTimelineMsg{generation: m.tlGen - 1, events: []assessment.VMHistoryEvent{{Kind: "observed"}, {Kind: "observed"}, {Kind: "observed"}, {Kind: "observed"}}})
	if len(m.timelineFull) != want {
		t.Fatalf("a load for an earlier timeline replaced the open one (%d rows, want %d)", len(m.timelineFull), want)
	}
}

// TestTimelineReloadDoesNothingOnTheChangesTab: r on the stored tab must not
// connect to or read any vCenter.
func TestTimelineReloadDoesNothingOnTheChangesTab(t *testing.T) {
	b := &eventsBackend{fakeBackend: twoHealthy(), events: map[string][]vsphere.VMEvent{}}
	m := timelineModel(t, b)
	drive(t, m, m.openTimeline("billing", modeBrowse, &vmEventsTarget{context: "customer-a", vmID: "vm-9"}))
	press(t, m, "r")
	if n := b.readCount(); n != 0 {
		t.Fatalf("r on the Changes tab read vCenter events: %v", b.reads)
	}
}

// TestTimelineHintIsVMOnly pins the detail footer's "h timeline" hint to the
// screens where "h" opens one: a VM with a history store. Every other kind's
// detail used to advertise it while the key did nothing.
func TestTimelineHintIsVMOnly(t *testing.T) {
	offers := func(m *Model) bool {
		for _, b := range m.keys.footerHints(m) {
			if b.Help().Desc == "timeline" {
				return true
			}
		}
		return false
	}
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{}})
	cases := []struct {
		kind vsphere.Kind
		name string
		want bool
	}{
		{vsphere.KindVM, "app-01", true},
		{vsphere.KindHost, "esxi-01", false},
		{vsphere.KindDatastore, "nvme-01", false},
		{vsphere.KindNetwork, "vlan-200", false},
	}
	for _, tc := range cases {
		findRow(t, m, tc.kind, tc.name)
		m.mode = modeDetail
		if got := offers(m); got != tc.want {
			t.Errorf("%v detail offers timeline = %v, want %v", tc.kind, got, tc.want)
		}
	}

	m = newTestModel(t, twoHealthy(), Options{Current: "prod"})
	findRow(t, m, vsphere.KindVM, "app-01")
	m.mode = modeDetail
	if offers(m) {
		t.Error("VM detail offers timeline without a history store")
	}
}
