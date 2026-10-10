package tui

import (
	"slices"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/x/ansi"
)

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
	// VAppSort orders the vApp workspace's member VMs by start order or by
	// peak CPU. It is "s" like Sort, which the workspace never handles.
	VAppSort key.Binding
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
	// TimelineSource moves between the VM timeline's Changes, vCenter
	// events and Combined tabs: tab cycles, 1-3 jump. TimelineEvents is
	// TimelineAll's label on the two events tabs, where "a" shows routine
	// events instead of unchanged runs.
	TimelineSource key.Binding
	TimelineEvents key.Binding

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
	// BaseTarget and NextPaneBrief are display-only footer stand-ins for the
	// two keys Base/Target and NextPane share a hint slot for. The Changes
	// key line has to hold nine hints in 80 columns and still end in "esc
	// back  ? help"; the full names live in the history help section.
	BaseTarget    key.Binding
	NextPaneBrief key.Binding
	// ImpactFilter narrows the change stream to one class of change. "0"
	// clears it, so the filter never becomes a state you cannot leave.
	ImpactFilter key.Binding

	// FindFiles and CopyPath belong to the datastore file browser. Both keys
	// are free everywhere else, so neither has to be relabelled per screen
	// the way AllScopeBrief is.
	FindFiles key.Binding
	CopyPath  key.Binding

	Sort key.Binding
	// Fold and NetView belong to the Networks tab: fold the switch under the
	// cursor, and switch between port groups grouped by switch and the plain
	// list. SwitchPage picks the switch workspace's page; it shares digits
	// with Kind, which the workspace never handles.
	Fold       key.Binding
	NetView    key.Binding
	SwitchPage key.Binding
	// ClusterPage picks a cluster detail pane's page, and FoldHost folds a
	// host on its Hosts & VMs page. Both share their keys with bindings the
	// pane otherwise ignores.
	ClusterPage key.Binding
	FoldHost    key.Binding
	// HostPage picks a host detail pane's page: Summary or Network.
	HostPage key.Binding
	// VLANMap opens the Networks tab's VLAN map. PairClusters and ClearPair
	// pick and drop the source and target cluster it compares; PickCluster
	// is the picker's enter.
	VLANMap      key.Binding
	PairClusters key.Binding
	ClearPair    key.Binding
	PickCluster  key.Binding
	// NetKeys is Fold and NetView's shared help line. The help overlay has to
	// fit the minimum terminal height, and one line more than this moves the
	// Connection keys below the bottom of it.
	NetKeys key.Binding
	Help    key.Binding
	Quit    key.Binding

	// The next four belong to the contexts screen.
	UseContext    key.Binding
	NewContext    key.Binding
	EditContext   key.Binding
	DeleteContext key.Binding
	LogoutContext key.Binding

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

	// The next group is display-only too, and exists for one reason: while a
	// text input has focus, every other key is typed into it, so the footer
	// must list only what the input itself answers to. FilterApply and
	// FilterClear are the filter's enter and esc, FindRun the datastore Find
	// prompt's enter, SaveRun the run label/note editor's. Continue,
	// SwitchField and ForceQuit are the credential and SSH prompts, which
	// own the keyboard ahead of everything else.
	FilterApply key.Binding
	FilterClear key.Binding
	FindRun     key.Binding
	SaveRun     key.Binding
	Continue    key.Binding
	SwitchField key.Binding
	ForceQuit   key.Binding
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

		ShorterRange:   key.NewBinding(key.WithKeys("<", ","), key.WithHelp("<", "shorter range")),
		LongerRange:    key.NewBinding(key.WithKeys(">", "."), key.WithHelp(">", "longer range")),
		PerfRange:      key.NewBinding(key.WithHelp("</>", "range")),
		PerfPage:       key.NewBinding(key.WithKeys("0", "1", "2", "3", "4"), key.WithHelp("0-4", "chart page")),
		VAppSort:       key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort")),
		Doctor:         key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "diagnose")),
		History:        key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "history")),
		Capture:        key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "capture")),
		Base:           key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "baseline")),
		Target:         key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "target")),
		Swap:           key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "swap")),
		Timeline:       key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "timeline")),
		TimelineAll:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "unchanged")),
		TimelineSource: key.NewBinding(key.WithKeys("tab", "1", "2", "3"), key.WithHelp("tab", "source")),
		TimelineEvents: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all events")),
		PrevPane:       key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("⇧tab", "prev pane")),
		NextPane:       key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next pane")),

		// Arrows only: "h" and "l" are the timeline and the browse screen's
		// kind keys, and a scrubber that also fired those would be a trap.
		ScrubPrev:     key.NewBinding(key.WithKeys("left"), key.WithHelp("←/→", "move")),
		ScrubNext:     key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "newer run")),
		PickRun:       key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "pick run")),
		ClipSpan:      key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clip")),
		BaseTarget:    key.NewBinding(key.WithHelp("b/t", "end")),
		NextPaneBrief: key.NewBinding(key.WithHelp("tab", "panes")),
		ImpactFilter:  key.NewBinding(key.WithKeys("0", "1", "2", "3", "4"), key.WithHelp("1-4", "impact")),

		FindFiles: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "find in datastore")),
		CopyPath:  key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy datastore path")),

		Sort:         key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort: name/status")),
		Fold:         key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "fold switch")),
		NetView:      key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "networks: tree/list")),
		SwitchPage:   key.NewBinding(key.WithKeys("0", "1"), key.WithHelp("0/1", "page")),
		ClusterPage:  key.NewBinding(key.WithKeys("0", "1", "2"), key.WithHelp("0-2", "page")),
		FoldHost:     key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "fold")),
		HostPage:     key.NewBinding(key.WithKeys("0", "1"), key.WithHelp("0/1", "page")),
		VLANMap:      key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "VLAN map")),
		PairClusters: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pair clusters")),
		ClearPair:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "clear pair")),
		PickCluster:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "choose")),
		NetKeys:      key.NewBinding(key.WithHelp("space/t/v", "fold · list · VLANs")),
		Help:         key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:         key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),

		UseContext:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "use")),
		NewContext:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		EditContext:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		DeleteContext: key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "delete")),
		LogoutContext: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "log out")),

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

		FilterApply: key.NewBinding(key.WithHelp("enter", "apply")),
		FilterClear: key.NewBinding(key.WithHelp("esc", "clear")),
		FindRun:     key.NewBinding(key.WithHelp("enter", "search")),
		SaveRun:     key.NewBinding(key.WithHelp("enter", "save")),
		Continue:    key.NewBinding(key.WithHelp("enter", "continue")),
		SwitchField: key.NewBinding(key.WithHelp("tab", "switch field")),
		ForceQuit:   key.NewBinding(key.WithHelp("ctrl+c", "quit")),
	}
}

// helpSections groups the bindings for the help panel.
func (k keyMap) helpSections(demo bool) []helpSection {
	ctxBindings := []key.Binding{k.UseContext, k.NewContext, k.EditContext, k.DeleteContext, k.LogoutContext}
	if demo {
		ctxBindings = []key.Binding{k.UseContext}
	}
	return []helpSection{
		{"Move", []key.Binding{k.Up, k.Down, k.PageUp, k.PageDown, k.Home, k.End}},
		{"Resource kinds", []key.Binding{k.Kind, k.NextTab, k.PrevTab, k.Open, k.Back}},
		{"Scope", []key.Binding{k.Contexts, k.AllScope, k.Filter, k.Search}},
		// The history hub's own keys are not here: this overlay has to fit the
		// minimum supported terminal height, and a second history block pushes
		// the Connection keys off the bottom of it. They have a help panel of
		// their own instead (historyHelpSections), which "?" shows in place of
		// these sections when it is opened from the hub. That includes tab,
		// which is "search all" on this screen (Scope above) and "next pane"
		// there, so it is listed once per screen rather than twice here.
		//
		// History sits above Connection so that helpLines, which breaks the two
		// columns at the section crossing the halfway mark, closes the left
		// column on it. Below Connection, the left column grows by the
		// Datastores section's height and "diagnose" falls off a 30 row terminal.
		{"History", []key.Binding{k.History}},
		{"Connection", []key.Binding{k.Reload, k.ReloadAll, k.Doctor}},
		{"Table", []key.Binding{k.Sort, k.NetKeys}},
		// f and y act on a datastore, not on a kind, so they get a section of
		// their own rather than sitting among the keys that switch kinds.
		{"Datastores", []key.Binding{k.FindFiles, k.CopyPath}},
		{"Contexts screen (c)", ctxBindings},
		{"Other", []key.Binding{k.Help, k.Quit}},
	}
}

// described is b with a different label, for a help section that has room to
// say more than the footer does.
func described(b key.Binding, keys, desc string) key.Binding {
	b.SetHelp(keys, desc)
	return b
}

// historyHelpSections is the help panel for the history hub. It replaces the
// browse sections instead of adding to them: the overlay has to fit the
// minimum supported terminal height, and none of the browse-only keys (kinds,
// scope, contexts, sort) do anything inside the hub. Descriptions stay within
// 22 columns, which is what a help column has after its key.
//
// The Changes pane is the one whose bindings are documented nowhere else; the
// other panes' footers already name theirs, but they are listed too so "?" is
// complete wherever it is opened.
func (k keyMap) historyHelpSections(pane int, canCapture bool) []helpSection {
	var panes helpSection
	switch pane {
	case historyPaneRuns:
		panes = helpSection{"Runs pane", []key.Binding{
			k.Up, k.Down,
			described(k.EditRun, "e", "edit label"),
			described(k.NoteRun, "N", "edit note"),
			described(k.PinRun, "p", "pin or unpin"),
		}}
	case historyPaneTrends:
		panes = helpSection{"Trends pane", []key.Binding{
			described(k.Up, "↑/k", "scroll up"),
			described(k.Down, "↓/j", "scroll down"),
		}}
	case historyPaneHealth:
		panes = helpSection{"Health pane", []key.Binding{
			described(k.Up, "↑/k", "scroll up"),
			described(k.Down, "↓/j", "scroll down"),
		}}
	default:
		panes = helpSection{"Changes pane", []key.Binding{
			k.Up, k.Down,
			described(k.ScrubPrev, "←/→", "move active end"),
			described(k.Base, "b", "move baseline end"),
			described(k.Target, "t", "move target end"),
			described(k.Swap, "s", "swap the two ends"),
			described(k.PickRun, "R", "pick from all runs"),
			described(k.ClipSpan, "c", "clip to same vCenters"),
			described(k.ImpactFilter, "1-4", "impact · 0 clears"),
			described(k.Open, "enter", "open inspector"),
			described(k.Timeline, "h", "VM timeline"),
		}}
	}
	hub := []key.Binding{k.PrevPane, k.NextPane}
	if canCapture {
		hub = append(hub, k.Capture)
	}
	hub = append(hub, described(k.Back, "esc", "back to browse"), k.Help, k.Quit)
	// The pane's own keys come first: the panel splits into two columns at
	// the block that crosses the halfway mark, and the long Changes block
	// has to start the left column for the hub keys to land in the right.
	return []helpSection{panes, {"History hub", hub}}
}

// timelineHelpSections is the help panel for the VM timeline and its detail
// screen. Like the history hub's it replaces the browse sections, none of
// which do anything here.
func (k keyMap) timelineHelpSections() []helpSection {
	return []helpSection{
		{"VM timeline", []key.Binding{
			k.Up, k.Down,
			described(k.Open, "enter", "open detail"),
			described(k.TimelineSource, "tab", "next source"),
			described(k.PrevPane, "⇧tab", "previous source"),
			described(k.TimelineSource, "1-3", "pick a source"),
			described(k.TimelineAll, "a", "unchanged · all events"),
			described(k.Reload, "r", "read vCenter events"),
			described(k.Back, "esc", "back"),
		}},
		{"Other", []key.Binding{k.Help, k.Quit}},
	}
}

// vlanMapHelpSections is the help panel for the VLAN map. Like the history
// hub's it replaces the browse sections, none of which do anything here.
func (k keyMap) vlanMapHelpSections() []helpSection {
	return []helpSection{
		{"VLAN map", []key.Binding{
			k.Up, k.Down, k.PageUp, k.PageDown,
			described(k.Open, "enter", "where it is · VMs"),
			described(k.PairClusters, "p", "pick source, target"),
			k.ClearPair,
			described(k.Reload, "r", "read wiring again"),
			described(k.Back, "esc", "back to Networks"),
		}},
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
	// A focused text input takes every printable key, so the screen's own
	// shortcuts are not available until it lets go. Advertising them anyway
	// meant the line said "H history" while "H" was being typed into a filter.
	// The same precedence handleKey applies: the credential and SSH overlays
	// first, then the filter, then the per-mode inputs below.
	if m.credPrompt != nil {
		return []key.Binding{k.Continue, k.CancelAction, k.ForceQuit}
	}
	if m.sshPrompt != nil {
		return []key.Binding{k.Continue, k.SwitchField, k.CancelAction, k.ForceQuit}
	}
	if m.filtering {
		switch m.mode {
		case modeSearch:
			// Esc leaves the search with the query kept, so it is "back" and
			// not "clear" here.
			return []key.Binding{k.FormMove, k.FilterApply, k.Back}
		case modeDatastoreFiles:
			return []key.Binding{k.FormMove, k.FilterApply, k.FilterClear}
		case modeBrowse:
			return []key.Binding{k.FormMove, k.FilterApply, k.Search, k.FilterClear}
		default:
			// The other panes keep their own cursors; up and down reach the
			// input, not a list.
			return []key.Binding{k.FilterApply, k.FilterClear}
		}
	}
	switch m.mode {
	case modeDetail:
		if m.actions != nil {
			return []key.Binding{k.Up, k.Down, k.RunAction, k.CancelAction}
		}
		if r, ok := m.detailRow(); ok && r.cluster != nil {
			switch m.clusterState(r).page {
			case 1:
				return []key.Binding{k.Up, k.Down, k.FoldHost, k.Open, k.ClusterPage, k.Back, k.Help, k.Quit}
			case 2:
				return []key.Binding{k.Up, k.Down, k.ClusterPage, k.Back, k.Help, k.Quit}
			}
			return []key.Binding{k.Up, k.Down, k.Open, k.ClusterPage, k.Back, k.Help, k.Quit}
		}
		if r, ok := m.detailRow(); ok && r.host() && m.hostPagesOffered() {
			if m.hostState(r).page == 1 {
				return []key.Binding{k.Up, k.Down, described(k.Open, "enter", "open switch"), k.HostPage, k.Reload, k.Back, k.Help, k.Quit}
			}
			return []key.Binding{k.Up, k.Down, k.Open, k.HostPage, k.Timeline, k.Back, k.Help, k.Quit}
		}
		if m.vmChartsDrawn() {
			return []key.Binding{k.Up, k.Down, k.Open, k.PerfRange, k.Timeline, k.Back, k.Help, k.Quit}
		}
		return []key.Binding{k.Up, k.Down, k.Open, k.Timeline, k.Back, k.Help, k.Quit}
	case modeVAppDetail:
		if _, ok := m.backend.(vmsPerfBackend); ok {
			return []key.Binding{k.Up, k.Down, k.Open, k.PerfRange, k.VAppSort, k.Back, k.Help, k.Quit}
		}
		return []key.Binding{k.Up, k.Down, k.Open, k.Back, k.Help, k.Quit}
	case modeVAppVMDetail:
		if m.actions != nil {
			return []key.Binding{k.Up, k.Down, k.RunAction, k.CancelAction}
		}
		if m.vmChartsDrawn() {
			return []key.Binding{k.Up, k.Down, k.Open, k.PerfRange, k.Timeline, k.Back, k.Help, k.Quit}
		}
		return []key.Binding{k.Up, k.Down, k.Open, k.Timeline, k.Back, k.Help, k.Quit}
	case modeSwitchDetail:
		if m.sw != nil && m.sw.page == 1 {
			return []key.Binding{k.Up, k.Down, k.Open, k.SwitchPage, k.Reload, k.Back, k.Help, k.Quit}
		}
		return []key.Binding{k.Up, k.Down, k.SwitchPage, k.Reload, k.Back, k.Help, k.Quit}
	case modeSwitchPGDetail:
		if m.actions != nil {
			return []key.Binding{k.Up, k.Down, k.RunAction, k.CancelAction}
		}
		return []key.Binding{k.Up, k.Down, k.Open, k.Back, k.Help, k.Quit}
	case modeVLANMap:
		if m.vmap != nil && m.vmap.pick != nil {
			return []key.Binding{k.Up, k.Down, k.PickCluster, k.CancelAction, k.Help}
		}
		if m.vpair != nil {
			return []key.Binding{k.Up, k.Down, described(k.Open, "enter", "VMs"), k.PairClusters, k.ClearPair, k.Reload, k.Back, k.Help, k.Quit}
		}
		return []key.Binding{k.Up, k.Down, described(k.Open, "enter", "where"), k.PairClusters, k.Reload, k.Back, k.Help, k.Quit}
	case modeDoctor:
		if m.demo || m.doctor == nil {
			return []key.Binding{k.Reload, k.Back, k.Help, k.Quit}
		}
		edit := k.EditContext
		if se, own := sourceFailure(m.doctor.diag); se != nil && own {
			edit = described(edit, "e", "change password source")
		}
		return []key.Binding{edit, k.Reload, k.Back, k.Help, k.Quit}
	case modeHelp:
		return []key.Binding{k.Up, k.Down, k.Back, k.Quit}
	case modeForm:
		return []key.Binding{k.FormMove, k.FormChange, k.FormActivate, k.Back}
	case modeConfirmDelete:
		return []key.Binding{k.Confirm, k.ToggleKeep, k.Back}
	case modeContexts:
		if m.demo {
			return []key.Binding{k.UseContext, k.AllScopeBrief, k.Doctor, k.Back, k.Help}
		}
		return []key.Binding{k.UseContext, k.AllScopeBrief, k.NewContext, k.EditContext, k.DeleteContext, k.LogoutContext, k.Doctor, k.Back, k.Help}
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
		// "? help" and "esc back" are the two hints this line may never lose,
		// and the key line used to cut them off. Every label is the shortest
		// that still names the action; b, t, s, R, h, enter and q are in the
		// history help (see historyHelpSections), and ctrl+c still quits.
		return append(append([]key.Binding{k.ScrubPrev, k.BaseTarget, k.ClipSpan, k.ImpactFilter, k.NextPaneBrief}, capture...), k.Back, k.Help)
	case modeChangeDetail:
		return []key.Binding{k.Up, k.Down, k.Timeline, k.Back, k.Help, k.Quit}
	case modeHistoryRuns:
		return []key.Binding{k.Up, k.Down, k.Open, k.EditRun, k.NoteRun, k.PinRun, k.Back, k.Help, k.Quit}
	case modeHistoryRunEdit:
		// The editor is a text input: "?" and "q" are typed into it, so
		// neither help nor quit is offered.
		return []key.Binding{k.SaveRun, k.CancelAction}
	case modeHistoryTimeline:
		if m.timelineSource == timelineSourceChanges {
			return []key.Binding{k.Up, k.Down, k.Open, k.TimelineSource, k.TimelineAll, k.Back, k.Help, k.Quit}
		}
		return []key.Binding{k.Up, k.Down, k.Open, k.TimelineSource, k.TimelineEvents, k.Reload, k.Back, k.Help, k.Quit}
	case modeHistoryTimelineDetail:
		return []key.Binding{k.Back, k.Help, k.Quit}
	case modeDatastoreFiles:
		if m.actions != nil {
			return []key.Binding{k.Up, k.Down, k.RunAction, k.CancelAction}
		}
		return []key.Binding{k.Up, k.Down, k.Open, k.Filter, k.FindFiles, k.CopyPath, k.Back, k.Help, k.Quit}
	case modeDatastoreEntry:
		return []key.Binding{k.Up, k.Down, k.Open, k.CopyPath, k.Back, k.Help, k.Quit}
	case modeDatastoreFind:
		if m.ds != nil && m.ds.findPrompt {
			// "f" and "y" are letters of the query while the prompt is open.
			return []key.Binding{k.FindRun, k.Back}
		}
		return []key.Binding{k.Up, k.Down, k.Open, k.FindFiles, k.CopyPath, k.Back, k.Help, k.Quit}
	default:
		// History comes before lower-priority browse hints so it remains
		// discoverable even when a narrow terminal truncates the footer. Enter
		// opening the selected row is conventional and remains in the help view.
		return []key.Binding{k.Kind, k.History, k.Contexts, k.AllScopeBrief, k.Filter, k.Reload, k.Help, k.Quit}
	}
}

// footerDropOrder is what the key line gives up, least essential first, when
// the terminal is too narrow for every hint a screen has. Each entry is
// dropped whole, so the up and down arrows go together. Back and Help are
// never in it: "esc back" and "? help" are how an operator leaves a screen
// and finds everything dropped here, so the line keeps them at any width.
// Every key below is described in the "?" panel of the screens that show it
// (TestEveryFooterFitsAndKeepsBackAndHelp checks that).
func (k keyMap) footerDropOrder() [][]key.Binding {
	return [][]key.Binding{
		{k.Quit}, // ctrl+c still quits, and "q quit" is in the help.
		{k.Up, k.Down},
		{k.ImpactFilter},
		{k.ClipSpan},
		{k.PinRun},
		{k.Reload},
		{k.CopyPath},
		{k.Doctor},
		{k.NoteRun},
		{k.TimelineAll},
		{k.TimelineEvents},
		{k.VAppSort},
		{k.PerfRange},
		{k.Timeline},
	}
}

// sameHint reports whether two bindings read the same on the key line.
// Bindings are not comparable, and their labels are what the reader sees.
func sameHint(a, b key.Binding) bool { return a.Help() == b.Help() }

// footerWidth is the width of hints laid out the way viewKeys joins them.
func footerWidth(hints []key.Binding) int {
	w := 0
	for i, b := range hints {
		if i > 0 {
			w += 2
		}
		h := b.Help()
		w += ansi.StringWidth(h.Key) + 1 + ansi.StringWidth(h.Desc)
	}
	return w
}

// fitFooter drops hints from a screen's full key line until it fits width:
// first by footerDropOrder, then, for hints that list does not name, from the
// right, since the lists put the most used keys first. Back, the cancel hint
// and Help are kept; only a line that cannot fit even with those alone is
// left for truncate to cut.
func (k keyMap) fitFooter(hints []key.Binding, width int) []key.Binding {
	pinned := func(b key.Binding) bool {
		return sameHint(b, k.Back) || sameHint(b, k.CancelAction) || sameHint(b, k.Help)
	}
	out := slices.Clone(hints)
	for _, tier := range k.footerDropOrder() {
		if footerWidth(out) <= width {
			return out
		}
		out = slices.DeleteFunc(out, func(b key.Binding) bool {
			return slices.ContainsFunc(tier, func(t key.Binding) bool { return sameHint(b, t) })
		})
	}
	for footerWidth(out) > width {
		i := -1
		for j := len(out) - 1; j >= 0 && i < 0; j-- {
			if !pinned(out[j]) {
				i = j
			}
		}
		if i < 0 {
			break
		}
		out = slices.Delete(out, i, i+1)
	}
	return out
}
