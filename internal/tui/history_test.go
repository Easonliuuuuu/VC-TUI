package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func TestChangesRunPickerSelectsAnotherBaseline(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cc := &config.Context{Name: "prod", Endpoint: "https://prod", Username: "user"}
	for i := 0; i < 3; i++ {
		when := time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)
		run, err := store.StartRun(context.Background(), "test", []*config.Context{cc}, when)
		if err != nil {
			t.Fatal(err)
		}
		err = store.SaveContext(context.Background(), run.ID, assessment.ContextResult{
			Name: "prod", VCenterID: "vc-1", Status: "success",
			VMs: []assessment.Observation{{VCenterID: "vc-1", Context: "prod", VM: vsphere.VM{ID: "vm-1", Name: "billing"}}},
		}, when.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.FinishRun(context.Background(), run.ID, when.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
	press(t, m, "H")
	if m.mode != modeChanges || m.targetRun == 0 || m.baseRun == 0 {
		t.Fatalf("changes state: mode=%v base=%d target=%d", m.mode, m.baseRun, m.targetRun)
	}
	// "b" makes the baseline the end the axis moves; "←" walks it one run
	// older. The pane never leaves the screen to do it — that full-screen
	// detour is what the axis replaces.
	press(t, m, "b", "left")
	if m.mode != modeChanges || m.baseRun == m.targetRun {
		t.Fatalf("axis did not select a distinct baseline: mode=%v base=%d target=%d", m.mode, m.baseRun, m.targetRun)
	}
	if m.baseRun != 1 {
		t.Fatalf("base run=%d, want oldest run 1", m.baseRun)
	}
	if m.changeDiff == nil || m.changeDiff.Base.ID != 1 {
		t.Fatalf("axis did not re-diff against the new baseline: diff=%+v", m.changeDiff)
	}
	// "R" is still the way to reach a run the axis window is nowhere near,
	// and it opens the picker for whichever end is active.
	press(t, m, "R")
	if m.mode != modeHistoryRuns || m.pickerRole != "base" {
		t.Fatalf("R did not open the run picker for the active end: mode=%v role=%q", m.mode, m.pickerRole)
	}
	press(t, m, "up", "enter")
	if m.mode != modeChanges || m.baseRun != 2 {
		t.Fatalf("picker did not select the run above: mode=%v base=%d", m.mode, m.baseRun)
	}
}

// TestChangesAxisStepsOverTheOtherEnd pins the one move the axis must refuse
// to make: walking an end onto the run the other end is already on, which
// would diff an assessment against itself.
func TestChangesAxisStepsOverTheOtherEnd(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cc := &config.Context{Name: "prod", Endpoint: "https://prod", Username: "user"}
	for i := 0; i < 3; i++ {
		when := time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)
		run, err := store.StartRun(context.Background(), "test", []*config.Context{cc}, when)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveContext(context.Background(), run.ID, assessment.ContextResult{Name: "prod", VCenterID: "vc-1", Status: "success"}, when); err != nil {
			t.Fatal(err)
		}
		if _, err := store.FinishRun(context.Background(), run.ID, when); err != nil {
			t.Fatal(err)
		}
	}
	m := newTestModel(t, twoHealthy(), Options{Assessment: &assessment.Service{Store: store}})
	press(t, m, "H")
	// Default span is #2 → #3. Moving the target one run older would land it
	// on the baseline, so it steps past to #1 instead — and then, with no
	// older run left, stays put.
	press(t, m, "t", "left")
	if m.targetRun != 1 || m.baseRun != 2 {
		t.Fatalf("target did not step over the baseline: base=%d target=%d", m.baseRun, m.targetRun)
	}
	press(t, m, "left")
	if m.targetRun != 1 {
		t.Fatalf("target moved past the oldest run: target=%d", m.targetRun)
	}
}

// TestComparisonBarNamesBothRunsAndCoverageGap pins the fix for the
// Changes screen's loudest failure mode: a run that covered fewer vCenters
// than its baseline must say so by name, not leave "vanished" looking like
// mass deletion. It also pins that the name is the operator's context name,
// not the raw VCenterID diff.go used to leak into these messages.
func TestComparisonBarNamesBothRunsAndCoverageGap(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prod := &config.Context{Name: "prod", Endpoint: "https://prod", Username: "user"}
	edge := &config.Context{Name: "edge-vc", Endpoint: "https://edge", Username: "user"}
	ctx := context.Background()

	base, err := store.StartRun(ctx, "test", []*config.Context{prod, edge}, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(ctx, base.ID, assessment.ContextResult{Name: "prod", VCenterID: "vc-prod", Status: "success"}, base.StartedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(ctx, base.ID, assessment.ContextResult{Name: "edge-vc", VCenterID: "vc-edge", Status: "success"}, base.StartedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, base.ID, base.StartedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateRun(ctx, strconv.FormatInt(base.ID, 10), assessment.RunMetadata{Label: "pre-maintenance", Pinned: true}); err != nil {
		t.Fatal(err)
	}

	// The target run only covers "prod" — the same narrower-capture scenario
	// that made "-96 vanished" look like an outage in the connected testbed.
	target, err := store.StartRun(ctx, "test", []*config.Context{prod}, time.Date(2026, 1, 1, 14, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(ctx, target.ID, assessment.ContextResult{Name: "prod", VCenterID: "vc-prod", Status: "success"}, target.StartedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, target.ID, target.StartedAt); err != nil {
		t.Fatal(err)
	}

	m := newTestModel(t, twoHealthy(), Options{AllContexts: true, Assessment: &assessment.Service{Store: store}})
	press(t, m, "H")
	if m.mode != modeChanges || m.baseRun != base.ID || m.targetRun != target.ID {
		t.Fatalf("changes state: mode=%v base=%d target=%d", m.mode, m.baseRun, m.targetRun)
	}
	bar := strings.Join(m.viewComparisonBar(m.changeDiff), "\n")
	for _, want := range []string{
		"pre-maintenance", "📌", historyRunLabel(base.ID), historyRunLabel(target.ID),
		"2 vCenters", "1 vCenter", "+2h 1m",
		"not compared: edge-vc",
	} {
		if !strings.Contains(bar, want) {
			t.Fatalf("comparison bar missing %q:\n%s", want, bar)
		}
	}
	if strings.Contains(bar, "vc-edge") || strings.Contains(bar, "vc-prod") {
		t.Fatalf("comparison bar leaked a raw VCenterID instead of its context name:\n%s", bar)
	}
}

// TestSwapKeyExchangesBaselineAndTarget pins "s" on the Changes pane: it
// swaps which run is baseline and which is target, and re-diffs immediately
// rather than leaving the two counts stale until the next unrelated update.
func TestSwapKeyExchangesBaselineAndTarget(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cc := &config.Context{Name: "prod", Endpoint: "https://prod", Username: "user"}
	for i := 0; i < 2; i++ {
		when := time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)
		run, err := store.StartRun(context.Background(), "test", []*config.Context{cc}, when)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveContext(context.Background(), run.ID, assessment.ContextResult{Name: "prod", VCenterID: "vc-1", Status: "success"}, when); err != nil {
			t.Fatal(err)
		}
		if _, err := store.FinishRun(context.Background(), run.ID, when); err != nil {
			t.Fatal(err)
		}
	}
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
	press(t, m, "H")
	base, target := m.baseRun, m.targetRun
	press(t, m, "s")
	if m.baseRun != target || m.targetRun != base {
		t.Fatalf("swap did not exchange runs: base=%d target=%d, want base=%d target=%d", m.baseRun, m.targetRun, target, base)
	}
	if m.changeDiff == nil || m.changeDiff.Base.ID != target || m.changeDiff.Target.ID != base {
		t.Fatalf("swap did not re-diff: diff=%+v", m.changeDiff)
	}
}

// oneVMDiffStore builds a two-run store with a single VM present only in the
// baseline (so the diff reports it "vanished" — the commonest row kind, and
// the one the old change-detail screen showed with an empty "Field changes"
// heading since Fields is only populated for a modified VM).
func oneVMDiffStore(t *testing.T) *assessment.Store {
	t.Helper()
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cc := &config.Context{Name: "prod", Endpoint: "https://prod", Username: "user"}
	ctx := context.Background()
	base, err := store.StartRun(ctx, "test", []*config.Context{cc}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(ctx, base.ID, assessment.ContextResult{
		Name: "prod", VCenterID: "vc-1", Status: "success",
		VMs: []assessment.Observation{{VCenterID: "vc-1", Context: "prod", VM: vsphere.VM{
			ID: "vm-1", Name: "billing", PowerState: "poweredOn", Host: "esx-01",
			CPU: 4, MemoryMB: 8192, InstanceUUID: "uuid-1",
		}}},
	}, base.StartedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, base.ID, base.StartedAt); err != nil {
		t.Fatal(err)
	}
	target, err := store.StartRun(ctx, "test", []*config.Context{cc}, base.StartedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(ctx, target.ID, assessment.ContextResult{Name: "prod", VCenterID: "vc-1", Status: "success"}, target.StartedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, target.ID, target.StartedAt); err != nil {
		t.Fatal(err)
	}
	return store
}

// TestChangeDetailShowsFieldsForVanishedVM pins the fix for the change-detail
// screen's emptiest failure mode: for appeared and vanished rows — 97 of 110
// changes in the scenario that motivated this redesign — it printed a "Field
// changes" heading with nothing under it, because it read only the row's
// flattened summary and never the diff's own Before/After observations. The
// inspector now reads Before/After directly.
func TestChangeDetailShowsFieldsForVanishedVM(t *testing.T) {
	store := oneVMDiffStore(t)
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
	press(t, m, "H")
	rows := m.changeRows()
	if len(rows) != 1 || rows[0].change != "vanished" || rows[0].kind != "vm" {
		t.Fatalf("rows=%+v", rows)
	}
	detail := strings.Join(m.historyInspector(rows[0]), "\n")
	for _, want := range []string{"esx-01", "on", "uuid-1", "4 vCPU", "8G"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("inspector missing %q — the old screen would have shown nothing here:\n%s", want, detail)
		}
	}
}

// TestChangeDetailTimelineKeyOpensTimeline pins the fix for the other half
// of the same screen's failures: its footer advertised "h timeline" while
// the mode's key dispatch handled only Back, so the key was silently
// dropped. This exercises the narrow-terminal fallback specifically, where
// modeChangeDetail is still reached by Enter.
func TestChangeDetailTimelineKeyOpensTimeline(t *testing.T) {
	store := oneVMDiffStore(t)
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
	m.width = 80 // below the split threshold, so Enter opens the fallback mode
	press(t, m, "H")
	press(t, m, "enter")
	if m.mode != modeChangeDetail {
		t.Fatalf("enter did not open the narrow-terminal fallback: mode=%v", m.mode)
	}
	press(t, m, "h")
	if m.mode != modeHistoryTimeline || m.timelineQuery != "billing" {
		t.Fatalf("h did not open billing's timeline from change detail: mode=%v query=%q", m.mode, m.timelineQuery)
	}
	press(t, m, "esc")
	if m.mode != modeChangeDetail {
		t.Fatalf("esc from the timeline did not return to change detail: mode=%v", m.mode)
	}
}

// TestChangesScrollsListToFollowCursor pins a gap the split layout made
// visible: the Changes list had no scroll-into-view logic at all, unlike the
// browse table's scrollIntoView, so Down could move the cursor past what the
// list draws with nothing on screen showing where it went — the inspector
// still tracked it, but the highlighted row itself was invisible.
func TestChangesScrollsListToFollowCursor(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cc := &config.Context{Name: "prod", Endpoint: "https://prod", Username: "user"}
	ctx := context.Background()
	// Zero-padded so the diff's alphabetical sort matches numeric order —
	// "host-15" is genuinely the sixteenth row, not "host-5" landing there
	// the way an unpadded name would under a lexicographic sort.
	var vms []assessment.Observation
	for i := 0; i < 20; i++ {
		suffix := strconv.Itoa(i)
		if i < 10 {
			suffix = "0" + suffix
		}
		vms = append(vms, assessment.Observation{VCenterID: "vc-1", Context: "prod", VM: vsphere.VM{ID: "vm-" + suffix, Name: "host-" + suffix}})
	}
	base, err := store.StartRun(ctx, "test", []*config.Context{cc}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(ctx, base.ID, assessment.ContextResult{Name: "prod", VCenterID: "vc-1", Status: "success", VMs: vms}, base.StartedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, base.ID, base.StartedAt); err != nil {
		t.Fatal(err)
	}
	// Target collects nothing, so all 20 VMs vanish — one row per VM, more
	// than a short terminal can show at once.
	target, err := store.StartRun(ctx, "test", []*config.Context{cc}, base.StartedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(ctx, target.ID, assessment.ContextResult{Name: "prod", VCenterID: "vc-1", Status: "success"}, target.StartedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, target.ID, target.StartedAt); err != nil {
		t.Fatal(err)
	}

	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
	m.height = 24 // tight enough that not all 20 rows fit at once
	press(t, m, "H")
	for i := 0; i < 15; i++ {
		press(t, m, "down")
	}
	if m.changeCursor != 15 {
		t.Fatalf("cursor=%d, want 15", m.changeCursor)
	}
	view := strings.Join(m.viewChanges(), "\n")
	if !strings.Contains(view, "host-15") {
		t.Fatalf("list did not scroll to keep the highlighted row visible:\n%s", view)
	}
}

// TestChangesSplitShowsInlineInspector pins the split layout itself: above
// the split threshold the inspector renders beside the list without Enter,
// and Enter — which used to open a whole separate screen for exactly this
// content — does nothing, because there is nothing left to open.
func TestChangesSplitShowsInlineInspector(t *testing.T) {
	store := oneVMDiffStore(t)
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
	// newTestModel sets width 140, comfortably above the split threshold.
	press(t, m, "H")
	view := strings.Join(m.viewChanges(), "\n")
	for _, want := range []string{"State", "esx-01", "on"} {
		if !strings.Contains(view, want) {
			t.Fatalf("wide Changes view did not show the inline inspector — missing %q:\n%s", want, view)
		}
	}
	press(t, m, "enter")
	if m.mode != modeChanges {
		t.Fatalf("enter should be a no-op once the inspector is already inline: mode=%v", m.mode)
	}
}

// TestLeavingAnyHistoryPaneRestoresBrowseFilterState pins the acceptance
// criteria of issue #121: entering History swaps the shared filter placeholder
// to "filter changes", and every exit path — not just the one from the Changes
// pane — must put the browse placeholder back.
func TestLeavingAnyHistoryPaneRestoresBrowseFilterState(t *testing.T) {
	cases := []struct {
		name string
		// keystrokes to reach the pane after "H" has opened History.
		reach []string
	}{
		{"Changes", nil},
		{"Trends", []string{"tab"}},
		{"Runs", []string{"tab", "tab"}},
		{"Health", []string{"tab", "tab", "tab"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := oneVMDiffStore(t)
			m := newTestModel(t, twoHealthy(), Options{Assessment: &assessment.Service{Store: store}})
			press(t, m, "H")
			if m.filter.Placeholder != "filter changes" {
				t.Fatalf("entering History did not set the changes placeholder: %q", m.filter.Placeholder)
			}
			press(t, m, tc.reach...)
			press(t, m, "esc")
			if m.mode != modeBrowse {
				t.Fatalf("esc from %s pane did not return to browse: mode=%v", tc.name, m.mode)
			}
			if m.filter.Placeholder != filterPlaceholder {
				t.Fatalf("esc from %s pane leaked the changes placeholder: %q", tc.name, m.filter.Placeholder)
			}
			if m.historyPane != historyPaneChanges {
				t.Fatalf("esc from %s pane left historyPane=%d, want it reset to Changes", tc.name, m.historyPane)
			}
		})
	}
}

// TestChangesNarrowStillOpensFullScreenDetail pins the fallback itself:
// below the split threshold, Enter still opens the full-screen inspector
// the way it always has.
func TestChangesNarrowStillOpensFullScreenDetail(t *testing.T) {
	store := oneVMDiffStore(t)
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
	m.width = 80
	press(t, m, "H")
	press(t, m, "enter")
	if m.mode != modeChangeDetail {
		t.Fatalf("enter did not open the narrow-terminal fallback: mode=%v", m.mode)
	}
	view := strings.Join(m.viewChangeDetail(), "\n")
	if !strings.Contains(view, "esx-01") {
		t.Fatalf("narrow change-detail view is missing observation fields:\n%s", view)
	}
}

func TestHistoryHeaderNamesTheSelectedPane(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{})
	m.runs = []assessment.Run{{ID: 42}}
	m.targetRun = 42
	m.mode = modeChanges

	// The run identity itself now lives on the comparison bar beneath the
	// header (see TestComparisonBarNamesBothRuns), so the header's job here
	// is only to name which pane is focused.
	m.historyPane = historyPaneChanges
	if got := m.viewChangesHeader(); !strings.Contains(got, "history  ·  Changes") {
		t.Fatalf("changes header does not name the pane: %q", got)
	}
	m.historyPane = historyPaneTrends
	if got := m.viewChangesHeader(); !strings.Contains(got, "history  ·  Trends") || strings.Contains(got, "target #42") {
		t.Fatalf("trends header was mislabeled: %q", got)
	}
	m.historyPane = historyPaneRuns
	if got := m.viewChangesHeader(); !strings.Contains(got, "history  ·  Runs") || strings.Contains(got, "target #42") {
		t.Fatalf("runs header was mislabeled: %q", got)
	}
	m.historyPane = historyPaneHealth
	if got := m.viewChangesHeader(); !strings.Contains(got, "history  ·  Health") {
		t.Fatalf("health header was mislabeled: %q", got)
	}
	if got := m.viewHeader(); !strings.Contains(got, "history  ·  Health") {
		t.Fatalf("viewHeader for health mode was mislabeled: %q", got)
	}
}

func TestTimelineHeaderNamesTimelineAndEntity(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{})
	m.mode = modeHistoryTimeline
	m.timelineQuery = "billing"

	// Timeline header should identify timeline and the entity being inspected
	got := m.viewHeader()
	if !strings.Contains(got, "history  ·  Timeline") {
		t.Fatalf("timeline header does not identify timeline: %q", got)
	}
	if !strings.Contains(got, "billing") {
		t.Fatalf("timeline header does not name the entity: %q", got)
	}
	if strings.Contains(got, "←/→ switch") {
		t.Fatalf("timeline header unexpectedly contained pane switch hint: %q", got)
	}

	// In timeline detail mode, header should identify timeline detail and entity
	m.mode = modeHistoryTimelineDetail
	gotDetail := m.viewHeader()
	if !strings.Contains(gotDetail, "history  ·  Timeline  ·  Detail") {
		t.Fatalf("timeline detail header does not identify timeline detail: %q", gotDetail)
	}
	if !strings.Contains(gotDetail, "billing") {
		t.Fatalf("timeline detail header does not name the entity: %q", gotDetail)
	}
	if strings.Contains(gotDetail, "←/→ switch") {
		t.Fatalf("timeline detail header unexpectedly contained pane switch hint: %q", gotDetail)
	}

	// When timelineQuery is empty, falls back to event name if present
	m.timelineQuery = ""
	m.timeline = []assessment.VMHistoryEvent{
		{Name: "web-frontend-01"},
	}
	m.timelineCursor = 0
	gotFallback := m.viewHeader()
	if !strings.Contains(gotFallback, "web-frontend-01") {
		t.Fatalf("timeline detail header did not fall back to event name: %q", gotFallback)
	}

	// When no entity is known at all, header still identifies timeline without crashing or stray delimiters
	m.timeline = nil
	m.timelineCursor = 0
	m.mode = modeHistoryTimeline
	gotNoEntity := m.viewHeader()
	if !strings.Contains(gotNoEntity, "history  ·  Timeline") {
		t.Fatalf("timeline header missing timeline label when entity empty: %q", gotNoEntity)
	}
}

func TestTimelineHeaderWidthSafeAtMinimumTerminalWidth(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{})
	m.timelineQuery = "extremely-long-virtual-machine-name-that-would-exceed-the-terminal-width"

	for _, mode := range []mode{modeHistoryTimeline, modeHistoryTimelineDetail} {
		m.mode = mode
		// At minimum terminal width (40 cols)
		m.width = minTermWidth
		got := m.viewHeader()
		if width := ansi.StringWidth(got); width > minTermWidth {
			t.Fatalf("header for mode %v exceeded minTermWidth %d: got width %d (content %q)", mode, minTermWidth, width, got)
		}
		if strings.Contains(got, "\n") {
			t.Fatalf("header for mode %v contained newline: %q", mode, got)
		}

		// Even at a very narrow width (e.g. 20 cols)
		m.width = 20
		gotNarrow := m.viewHeader()
		if width := ansi.StringWidth(gotNarrow); width > 20 {
			t.Fatalf("header for mode %v exceeded width 20: got width %d (content %q)", mode, width, gotNarrow)
		}
	}
}

func TestHistoryHealthPaneLoadsLatestAssessment(t *testing.T) {
	store := oneVMDiffStore(t)
	m := newTestModel(t, twoHealthy(), Options{Assessment: &assessment.Service{Store: store}})
	press(t, m, "H", "tab", "tab", "tab")
	if m.historyPane != historyPaneHealth || m.historyHealth == nil {
		t.Fatalf("health pane did not load: pane=%d report=%+v err=%v", m.historyPane, m.historyHealth, m.historyHealthErr)
	}
	view := strings.Join(m.viewChanges(), "\n")
	if !strings.Contains(view, "Health") || !strings.Contains(view, "assessment") {
		t.Fatalf("health pane view=%s", view)
	}
}

// TestHistoryCaptureKeyIsPaneIndependent pins the resolution of the old "n"
// collision: capture from every pane, and the note editor on "N".
func TestHistoryCaptureKeyIsPaneIndependent(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	m := newTestModel(t, twoHealthy(), Options{Assessment: &assessment.Service{Store: store, Collector: &assessment.Collector{Store: store}}})
	m.mode = modeChanges
	// A populated run list is what used to make "n" ambiguous: the Runs pane
	// read it as "edit this run's note".
	m.runs = []assessment.Run{{ID: 1}, {ID: 2}}

	for _, pane := range []int{historyPaneChanges, historyPaneTrends, historyPaneRuns} {
		m.historyPane = pane
		m.runEditKind = ""
		m.historyErr = nil
		press(t, m, "n")
		if m.runEditKind != "" || m.mode == modeHistoryRunEdit {
			t.Fatalf("pane %d: \"n\" opened the %q editor instead of capturing", pane, m.runEditKind)
		}
		// The service carries no collector, so a capture that was actually
		// dispatched comes back with exactly this error. The note editor, the
		// branch "n" used to take here, reaches no service at all.
		if m.historyErr == nil || !strings.Contains(m.historyErr.Error(), "collector is not configured") {
			t.Fatalf("pane %d: \"n\" did not dispatch a capture (historyErr=%v)", pane, m.historyErr)
		}
	}
}

func TestHistoryNoteKeyOpensTheNoteEditor(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	m := newTestModel(t, twoHealthy(), Options{Assessment: &assessment.Service{Store: store}})
	m.mode = modeChanges
	m.historyPane = historyPaneRuns
	m.runs = []assessment.Run{{ID: 7, Note: "quarter close"}}

	press(t, m, "N")
	if m.mode != modeHistoryRunEdit || m.runEditKind != "note" {
		t.Fatalf("\"N\" did not open the note editor: mode=%v kind=%q", m.mode, m.runEditKind)
	}
	if got := m.runEditInput.Value(); got != "quarter close" {
		t.Fatalf("note editor seeded with %q, want the run's existing note", got)
	}
}

// TestChangesFooterDescribesPanesNotKinds guards the label, not the key: the
// footer used to borrow NextTab/PrevTab and tell the operator "next kind" on a
// screen that has no kinds.
func TestChangesFooterDescribesPanesNotKinds(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{})
	m.mode = modeChanges
	for _, b := range defaultKeys().footerHints(m) {
		if strings.Contains(b.Help().Desc, "kind") {
			t.Fatalf("changes footer still advertises %q/%q", b.Help().Key, b.Help().Desc)
		}
	}
}

// TestCaptureContextsIsScoped checks that a capture reads only the vCenter(s)
// on screen — the selected context alone, and every context once the
// all-vCenters view is on — rather than every configured vCenter regardless
// of scope.
func TestCaptureContextsIsScoped(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})

	got := m.captureContexts()
	if len(got) != 1 || got[0].Name != "prod" {
		t.Fatalf("scoped capture contexts = %+v, want just prod", got)
	}

	m.allScope = true
	got = m.captureContexts()
	if len(got) != 2 {
		t.Fatalf("all-vCenters capture contexts = %+v, want both configured vCenters", got)
	}
}

// TestCaptureCredentialRequestIsScopedAndAttributed pins the fix for a
// capture that both connected to vCenters outside scope and opened an
// unattributed password overlay for them: credentialState now knows about a
// context's contextState.capturing flag, so a request naming an out-of-scope
// vCenter is deferred exactly like a quiet background refresh, while one
// naming the in-scope vCenter opens an overlay labelled with it.
func TestCaptureCredentialRequestIsScopedAndAttributed(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store, Collector: &assessment.Collector{Store: store}}})
	// captureCommand's own tea.Cmd is not run here — running it would resolve
	// the capture immediately (the collector carries no session manager) and
	// clear the very state this test inspects.
	if cmd := m.captureCommand(); cmd == nil {
		t.Fatal("captureCommand returned nil with an assessment service configured")
	}
	if !m.byName["prod"].capturing {
		t.Fatal("captureCommand did not mark the in-scope context as capturing")
	}
	if m.byName["customer-a"].capturing {
		t.Fatal("captureCommand marked an out-of-scope context as capturing")
	}

	outOfScope := credRequest{label: "customer-a", resp: make(chan credResult, 1)}
	m.Update(credRequestMsg{req: outOfScope})
	if m.credPrompt != nil {
		t.Fatal("a request for a vCenter outside the capture's scope opened an overlay")
	}
	select {
	case res := <-outOfScope.resp:
		if res.err == nil {
			t.Fatal("expected the out-of-scope request to be deferred, got a nil error")
		}
	default:
		t.Fatal("the out-of-scope request was never answered")
	}

	inScope := credRequest{label: "prod", resp: make(chan credResult, 1)}
	m.Update(credRequestMsg{req: inScope})
	if m.credPrompt == nil || m.credPrompt.label != "prod" {
		t.Fatalf("expected an overlay labelled %q for the captured vCenter, got %+v", "prod", m.credPrompt)
	}
	m.resolveCredPrompt(credResult{err: errPromptCanceled})
}

// TestCaptureCredentialGateClosesOnCompletion checks that a finished capture
// stops answering for the vCenters it covered — contextState.capturing is
// cleared once historyCaptureMsg lands, the same way a load's own gate closes
// in finishLoad.
func TestCaptureCredentialGateClosesOnCompletion(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store, Collector: &assessment.Collector{Store: store}}})
	if cmd := m.captureCommand(); cmd == nil {
		t.Fatal("captureCommand returned nil with an assessment service configured")
	}
	if !m.byName["prod"].capturing {
		t.Fatal("captureCommand did not mark the in-scope context as capturing")
	}

	m.Update(historyCaptureMsg{err: errors.New("capture failed")})
	if m.byName["prod"].capturing {
		t.Fatal("a finished capture left the context marked as capturing")
	}

	request := credRequest{label: "prod", resp: make(chan credResult, 1)}
	m.Update(credRequestMsg{req: request})
	if m.credPrompt != nil {
		t.Fatal("a request after the capture finished still opened an overlay")
	}
	select {
	case res := <-request.resp:
		if res.err == nil {
			t.Fatal("expected the post-capture request to be deferred, got a nil error")
		}
	default:
		t.Fatal("the post-capture request was never answered")
	}
}

// TestStoreOnlyServiceDoesNotAdvertiseOrEnterCapture pins issue #119: the demo
// seeds a store-only assessment service (no collector), so the History hub must
// neither offer the "n capture" action nor let a keypress enter a capturing
// state that only a collector could leave.
func TestStoreOnlyServiceDoesNotAdvertiseOrEnterCapture(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
	if m.canCapture() {
		t.Fatal("a store-only service reported itself as capture-capable")
	}
	m.mode = modeChanges
	m.runs = []assessment.Run{{ID: 1}, {ID: 2}}

	for _, pane := range []int{historyPaneChanges, historyPaneRuns} {
		m.historyPane = pane
		for _, b := range defaultKeys().footerHints(m) {
			if b.Help().Key == "n" {
				t.Fatalf("pane %d footer still advertises the capture key", pane)
			}
		}
	}

	m.historyPane = historyPaneChanges
	m.message = ""
	press(t, m, "n")
	if m.capturing {
		t.Fatal("\"n\" entered the capturing state against a store-only service")
	}
	if m.message != "" {
		t.Fatalf("\"n\" set a sticky progress message %q against a store-only service", m.message)
	}
	if m.historyErr == nil {
		t.Fatal("\"n\" against a store-only service left no explanation on screen")
	}

	// The Changes error panel must not tell the operator to repeat a key that
	// can never succeed here.
	m.historyErr = errors.New("some earlier failure")
	for _, line := range m.viewChanges() {
		if strings.Contains(ansi.Strip(line), "press n to capture") {
			t.Fatal("the Changes error panel still tells a store-only session to press n")
		}
	}
}

// TestCaptureErrorReplacesTheStickyProgressMessage pins the other half of
// issue #119: a failed capture cleared m.capturing but left the footer stuck
// on "capturing …" with no way back.
func TestCaptureErrorReplacesTheStickyProgressMessage(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store, Collector: &assessment.Collector{Store: store}}})
	if cmd := m.captureCommand(); cmd == nil {
		t.Fatal("captureCommand returned nil with a capture-capable service")
	}
	if !strings.HasPrefix(m.message, "capturing ") {
		t.Fatalf("capture did not set a progress message, got %q", m.message)
	}

	m.Update(historyCaptureMsg{err: errors.New("proxy connection refused")})
	if m.capturing {
		t.Fatal("a failed capture left the model in the capturing state")
	}
	if strings.HasPrefix(m.message, "capturing ") {
		t.Fatalf("a failed capture left the sticky progress message %q", m.message)
	}
	if !strings.Contains(m.message, "proxy connection refused") {
		t.Fatalf("the failure was not surfaced in the status line, got %q", m.message)
	}
}

// TestRunsPaneAdvertisesItsActionsInTheFooter pins the fix for the Runs pane
// being the one History pane whose key hints sat on a line above the list
// instead of in the footer with every other pane's bindings. The pane answers
// e/N/p, so the footer has to say so.
func TestRunsPaneAdvertisesItsActionsInTheFooter(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{})
	m.mode = modeChanges
	m.historyPane = historyPaneRuns
	m.runs = []assessment.Run{{ID: 1, Label: "baseline"}}

	want := map[string]bool{"e": false, "N": false, "p": false}
	for _, b := range defaultKeys().footerHints(m) {
		if _, ok := want[b.Help().Key]; ok {
			want[b.Help().Key] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("Runs pane footer omits %q, a key the pane handles", k)
		}
	}

	// The hint line the footer replaced must not come back: two places naming
	// the same keys is how they drifted apart in the first place.
	for _, line := range m.viewHistoryHubRuns() {
		plain := ansi.Strip(line)
		for _, stale := range []string{"e label", "N note", "p pin", "n capture"} {
			if strings.Contains(plain, stale) {
				t.Errorf("Runs pane body still carries the inline hint %q", stale)
			}
		}
	}
}

// TestTrendsPaneKeepsScrollingHintsInTheFooter keeps the Trends subtitle
// descriptive instead of duplicating key bindings above the data.
func TestTrendsPaneKeepsScrollingHintsInTheFooter(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{})
	m.mode = modeChanges
	m.historyPane = historyPaneTrends

	want := map[string]bool{"↑/k": false, "↓/j": false}
	for _, b := range defaultKeys().footerHints(m) {
		if _, ok := want[b.Help().Key]; ok {
			want[b.Help().Key] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("Trends pane footer omits %q", k)
		}
	}

	for _, line := range m.viewHistoryTrends() {
		if strings.Contains(ansi.Strip(line), "scroll") {
			t.Errorf("Trends body repeats the footer scrolling hint: %q", ansi.Strip(line))
		}
	}
}
