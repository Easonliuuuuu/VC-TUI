package tui

import "github.com/charmbracelet/bubbles/key"

// keyMap is the whole keyboard surface. It is one struct rather than a switch
// on raw strings so that the help panel is generated from the bindings and
// cannot drift out of date with them.
//
// The browse screen deliberately keeps only the keys an operator uses hourly.
// Everything about a vCenter itself — switching, adding, editing, removing —
// lives behind Contexts, so the always-visible key line fits an 80 column
// terminal without truncating.
type keyMap struct {
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Home     key.Binding
	End      key.Binding

	// Kind jumps straight to a resource tab by its number. Cycling with
	// NextTab and PrevTab still works, but five presses of "l" to reach
	// Networks is not a way to move around an estate.
	Kind    key.Binding
	NextTab key.Binding
	PrevTab key.Binding

	Open   key.Binding
	Back   key.Binding
	Filter key.Binding
	// Search widens the filter into every vCenter and every kind. It shares
	// "tab" with nothing: removing the two-pane layout freed the key, and
	// widening a search is the closest thing left to changing pane.
	Search   key.Binding
	Contexts key.Binding
	AllScope key.Binding
	// AllScopeBrief is the same key with a shorter label. The browse key line
	// is the one place that has to fit eight hints into 80 columns, and a
	// truncated key line is the problem this screen exists to fix.
	AllScopeBrief key.Binding

	Reload    key.Binding
	ReloadAll key.Binding
	// ShorterRange and LongerRange step the VM detail pane's chart window
	// through perfRanges. PerfRange is their shared footer hint, carrying no
	// keys of its own, the way AllScopeBrief shortens AllScope.
	ShorterRange key.Binding
	LongerRange  key.Binding
	PerfRange    key.Binding
	// PerfPage picks the VM detail pane's chart page. It shares 0–4 with
	// Kind, which the detail pane never handles. It has no footer hint: the
	// detail key line is already at 80 columns, and the page tabs show their
	// own digits.
	PerfPage key.Binding
	Doctor   key.Binding
	History  key.Binding
	Capture  key.Binding
	Base     key.Binding
	Target   key.Binding
	// Swap exchanges baseline and target on the Changes pane. It is "s"
	// rather than sharing anything with Sort — Sort belongs to the browse
	// table, which the history hub never shows, so the two never collide.
	Swap        key.Binding
	Timeline    key.Binding
	TimelineAll key.Binding

	// PrevPane and NextPane move between the history hub's Changes, Trends,
	// Runs and Health panes. They exist so the history footer stops borrowing
	// NextTab and PrevTab, whose "next kind"/"prev kind" labels describe the
	// browse screen and are wrong here. They are on tab and shift+tab rather
	// than the arrows because the Changes pane's run axis is what the arrows
	// move: on that screen ← and → are a scrubber, not a tab strip.
	PrevPane key.Binding
	NextPane key.Binding

	// The next four belong to the Changes pane's run axis. ScrubPrev and
	// ScrubNext move whichever end of the comparison is active, PickRun opens
	// the full run list for it when the axis window is not where you want to
	// go, and ClipSpan moves the baseline to the nearest older run that
	// covered the same vCenters as the target — the one-key answer to a diff
	// that reads as mass deletion because one site was dark.
	ScrubPrev key.Binding
	ScrubNext key.Binding
	PickRun   key.Binding
	ClipSpan  key.Binding
	// ImpactFilter narrows the change stream to one class of change. "0"
	// clears it, so the filter never becomes a state you cannot leave.
	ImpactFilter key.Binding

	// FindFiles and CopyPath belong to the datastore file browser. Both keys
	// are free everywhere else, so neither has to be relabelled per screen
	// the way AllScopeBrief is.
	FindFiles key.Binding
	CopyPath  key.Binding

	Sort key.Binding
	Help key.Binding
	Quit key.Binding

	// The next four belong to the contexts screen.
	UseContext    key.Binding
	NewContext    key.Binding
	EditContext   key.Binding
	DeleteContext key.Binding

	// The next three describe the form's own dispatch (up/down move the row,
	// left/right change a select or toggle, enter activates a button) —
	// display only, since the form reads raw key types rather than matching
	// these bindings.
	FormMove     key.Binding
	FormChange   key.Binding
	FormActivate key.Binding

	// Confirm and ToggleKeep belong to the delete confirmation screen.
	Confirm    key.Binding
	ToggleKeep key.Binding
	EditRun    key.Binding
	NoteRun    key.Binding
	PinRun     key.Binding

	// RunAction and CancelAction are display-only relabels of Open and Back
	// for the detail pane's action popup — same physical keys as
	// AllScopeBrief is to AllScope, carrying no keys of their own so
	// key.Matches never matches them directly.
	RunAction    key.Binding
	CancelAction key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+f"), key.WithHelp("pgdn", "page down")),
		Home:     key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g", "first")),
		End:      key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G", "last")),

		Kind:    key.NewBinding(key.WithKeys("1", "2", "3", "4", "5", "6", "7"), key.WithHelp("1-7", "kind")),
		NextTab: key.NewBinding(key.WithKeys("right", "l", "]"), key.WithHelp("→/l", "next kind")),
		PrevTab: key.NewBinding(key.WithKeys("left", "h", "["), key.WithHelp("←/h", "prev kind")),

		Open:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Back:          key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Filter:        key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Search:        key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "search all")),
		Contexts:      key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "contexts")),
		AllScope:      key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all vCenters")),
		AllScopeBrief: key.NewBinding(key.WithHelp("a", "all")),

		Reload:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload")),
		ReloadAll: key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "reload all")),

		ShorterRange: key.NewBinding(key.WithKeys("<", ","), key.WithHelp("<", "shorter range")),
		LongerRange:  key.NewBinding(key.WithKeys(">", "."), key.WithHelp(">", "longer range")),
		PerfRange:    key.NewBinding(key.WithHelp("</>", "range")),
		PerfPage:     key.NewBinding(key.WithKeys("0", "1", "2", "3", "4"), key.WithHelp("0-4", "chart page")),
		Doctor:       key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "diagnose")),
		History:      key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "history")),
		Capture:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "capture")),
		Base:         key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "baseline")),
		Target:       key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "target")),
		Swap:         key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "swap")),
		Timeline:     key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "timeline")),
		TimelineAll:  key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all observations")),
		PrevPane:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("⇧tab", "prev pane")),
		NextPane:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next pane")),

		// Arrows only: "h" and "l" are the timeline and the browse screen's
		// kind keys, and a scrubber that also fired those would be a trap.
		ScrubPrev:    key.NewBinding(key.WithKeys("left"), key.WithHelp("←/→", "move end")),
		ScrubNext:    key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "newer run")),
		PickRun:      key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "pick run")),
		ClipSpan:     key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clip to shared coverage")),
		ImpactFilter: key.NewBinding(key.WithKeys("0", "1", "2", "3", "4"), key.WithHelp("1-4", "impact")),

		FindFiles: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "find in datastore")),
		CopyPath:  key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy datastore path")),

		Sort: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort: name/status")),
		Help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit: key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),

		UseContext:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "use")),
		NewContext:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		EditContext:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		DeleteContext: key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "delete")),

		FormMove:     key.NewBinding(key.WithHelp("↑/↓", "move")),
		FormChange:   key.NewBinding(key.WithHelp("←/→", "change")),
		FormActivate: key.NewBinding(key.WithHelp("enter", "activate")),

		Confirm:    key.NewBinding(key.WithHelp("y", "delete")),
		ToggleKeep: key.NewBinding(key.WithHelp("c", "keep password")),
		EditRun:    key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "label")),
		// "n" captures a new assessment everywhere in the history hub, so the
		// note editor takes "N". The two used to share "n", which did whichever
		// the focused pane happened to mean.
		NoteRun: key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "note")),
		PinRun:  key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pin")),

		RunAction:    key.NewBinding(key.WithHelp("enter", "run")),
		CancelAction: key.NewBinding(key.WithHelp("esc", "cancel")),
	}
}

// helpSections groups the bindings for the help panel.
func (k keyMap) helpSections(demo bool) []helpSection {
	ctxBindings := []key.Binding{k.UseContext, k.NewContext, k.EditContext, k.DeleteContext}
	if demo {
		ctxBindings = []key.Binding{k.UseContext}
	}
	return []helpSection{
		{"Move", []key.Binding{k.Up, k.Down, k.PageUp, k.PageDown, k.Home, k.End}},
		// The datastore file browser's two extra keys sit here rather than in
		// a section of their own: enter, esc and "/" already mean the same
		// thing there as everywhere else, and a fourth block would push the
		// second column past the minimum supported terminal height.
		{"Resource kinds", []key.Binding{k.Kind, k.NextTab, k.PrevTab, k.Open, k.Back, k.FindFiles, k.CopyPath}},
		{"Scope", []key.Binding{k.Contexts, k.AllScope, k.Filter, k.Search}},
		{"Connection", []key.Binding{k.Reload, k.ReloadAll, k.Doctor}},
		// The changes screen puts its run-picker and capture bindings in the
		// footer; keeping this section to one line preserves the compact help
		// overlay at the minimum supported terminal height.
		// The Changes pane's own bindings stay in its footer rather than
		// claiming a section here: this overlay has to fit the minimum
		// supported terminal height, and a second history block pushes the
		// Connection keys off the bottom of it.
		{"History", []key.Binding{k.History, k.NextPane}},
		{"Table", []key.Binding{k.Sort}},
		{"Contexts screen (c)", ctxBindings},
		{"Other", []key.Binding{k.Help, k.Quit}},
	}
}

type helpSection struct {
	title    string
	bindings []key.Binding
}

// footerHints is the always-visible key line, kept short enough to survive an
// 80 column terminal without an ellipsis.
func (k keyMap) footerHints(m *Model) []key.Binding {
	switch m.mode {
	case modeDetail:
		if m.actions != nil {
			return []key.Binding{k.Up, k.Down, k.RunAction, k.CancelAction}
		}
		if r, ok := m.detailRow(); ok && r.vm != nil {
			return []key.Binding{k.Up, k.Down, k.Open, k.PerfRange, k.Timeline, k.Back, k.Help, k.Quit}
		}
		return []key.Binding{k.Up, k.Down, k.Open, k.Timeline, k.Back, k.Help, k.Quit}
	case modeVAppDetail:
		return []key.Binding{k.Up, k.Down, k.Open, k.Back, k.Help, k.Quit}
	case modeVAppVMDetail:
		if m.actions != nil {
			return []key.Binding{k.Up, k.Down, k.RunAction, k.CancelAction}
		}
		return []key.Binding{k.Up, k.Down, k.Open, k.PerfRange, k.Timeline, k.Back, k.Help, k.Quit}
	case modeDoctor:
		return []key.Binding{k.Reload, k.Back, k.Help, k.Quit}
	case modeHelp:
		return []key.Binding{k.Up, k.Down, k.Back, k.Quit}
	case modeForm:
		return []key.Binding{k.FormMove, k.FormChange, k.FormActivate, k.Back}
	case modeConfirmDelete:
		return []key.Binding{k.Confirm, k.ToggleKeep, k.Back}
	case modeContexts:
		if m.demo {
			return []key.Binding{k.UseContext, k.AllScope, k.Doctor, k.Back}
		}
		return []key.Binding{k.UseContext, k.AllScope, k.NewContext, k.EditContext, k.DeleteContext, k.Doctor, k.Back}
	case modeSearch:
		return []key.Binding{k.Open, k.Filter, k.Sort, k.Reload, k.Back, k.Help, k.Quit}
	case modeChanges:
		// Capture is only offered when the service can actually run one; a
		// store-only history (the demo) drops the "n" hint rather than
		// advertising an action that always fails.
		capture := []key.Binding{k.Capture}
		if !m.canCapture() {
			capture = nil
		}
		if m.historyPane == historyPaneRuns {
			// The Runs pane's per-run actions belong in the footer next to
			// every other binding, the way the standalone run picker lists
			// them, rather than on a hint line above the list.
			return append(append([]key.Binding{k.Up, k.Down, k.EditRun, k.NoteRun, k.PinRun, k.NextPane}, capture...), k.Back, k.Help, k.Quit)
		}
		if m.historyPane != historyPaneChanges {
			return append(append([]key.Binding{k.Up, k.Down, k.NextPane}, capture...), k.Back, k.Help, k.Quit)
		}
		return append(append([]key.Binding{k.ScrubPrev, k.Base, k.Target, k.ClipSpan, k.ImpactFilter, k.NextPane}, capture...), k.Back, k.Quit)
	case modeChangeDetail:
		return []key.Binding{k.Up, k.Down, k.Timeline, k.Back, k.Help, k.Quit}
	case modeHistoryRuns:
		return []key.Binding{k.Up, k.Down, k.Open, k.EditRun, k.NoteRun, k.PinRun, k.Back, k.Help, k.Quit}
	case modeHistoryRunEdit:
		return []key.Binding{k.FormActivate, k.Back, k.Help, k.Quit}
	case modeHistoryTimeline:
		return []key.Binding{k.Up, k.Down, k.Open, k.TimelineAll, k.Back, k.Help, k.Quit}
	case modeHistoryTimelineDetail:
		return []key.Binding{k.Back, k.Help, k.Quit}
	case modeDatastoreFiles:
		if m.actions != nil {
			return []key.Binding{k.Up, k.Down, k.RunAction, k.CancelAction}
		}
		return []key.Binding{k.Up, k.Down, k.Open, k.Filter, k.FindFiles, k.CopyPath, k.Back, k.Quit}
	case modeDatastoreEntry:
		return []key.Binding{k.Up, k.Down, k.Open, k.CopyPath, k.Back, k.Quit}
	case modeDatastoreFind:
		return []key.Binding{k.Up, k.Down, k.Open, k.FindFiles, k.CopyPath, k.Back, k.Quit}
	default:
		// History comes before lower-priority browse hints so it remains
		// discoverable even when a narrow terminal truncates the footer. Enter
		// opening the selected row is conventional and remains in the help view.
		return []key.Binding{k.Kind, k.History, k.Contexts, k.AllScopeBrief, k.Filter, k.Reload, k.Help, k.Quit}
	}
}
