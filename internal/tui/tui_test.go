package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/contextops"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
	"github.com/easonliuuuuu/vsfleet/internal/session"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// fakeBackend answers instantly from fixtures. The whole point of the Backend
// interface is that everything below is exercised here without a vCenter, a
// proxy or a certificate anywhere in sight.
//
// SaveContext, RemoveContext and TestContext apply to an in-memory context
// list rather than exercising contextops itself — that package has its own
// tests against a real simulated vCenter. What these tests are exercising is
// how the model reacts to the outcome: the sidebar rebuilding after a save,
// the form staying open on a failure, the last context's removal reopening
// setup.
type fakeBackend struct {
	contexts    []*config.Context
	inventories map[string]*vsphere.Inventory
	failures    map[string]error
	diagnoses   map[string]*vsphere.Diagnosis
	calls       map[string]int

	// testDiag, keyed by the input's context name, overrides what
	// TestContext and a tested SaveContext report; unset names get a passing
	// stub. saveErr forces SaveContext to fail outright, past any test.
	testDiag       map[string]*vsphere.Diagnosis
	saveErr        map[string]error
	discoverErr    error
	discoverThumb  string
	discoverSubj   string
	currentContext string

	// instanceIDs, keyed by context name, is what Status reports as
	// session.Status.InstanceID — the vCenter server GUID that deep links
	// need. Unset names report empty, matching an unconnected context or a
	// vcsim-backed one.
	instanceIDs map[string]string
}

func (b *fakeBackend) Contexts() []*config.Context { return b.contexts }

// BeginInventory implements tui.Backend. calls counts one per call here —
// one per load — not one per fetch group, so the many existing assertions
// on b.calls[name] keep meaning "how many times was this context loaded".
func (b *fakeBackend) BeginInventory(_ context.Context, cc *config.Context) (InventoryHandle, error) {
	if b.calls == nil {
		b.calls = map[string]int{}
	}
	b.calls[cc.Name]++
	if err, ok := b.failures[cc.Name]; ok {
		return nil, err
	}
	inv, ok := b.inventories[cc.Name]
	if !ok {
		// A context with no fixture registered — a freshly saved context in
		// the form tests, say — connects successfully to an empty vCenter
		// rather than to nothing at all.
		inv = &vsphere.Inventory{Context: cc.Name}
	}
	return fakeInventoryHandle{inv: inv}, nil
}

// fakeInventoryHandle answers each fetch group by slicing it out of the
// fixture's whole Inventory, the same way the demo backend does.
type fakeInventoryHandle struct{ inv *vsphere.Inventory }

// FetchGroup implements tui.InventoryHandle: one page, delivered to partial
// before the group's own result, so the paged path is what the tests drive.
func (h fakeInventoryHandle) FetchGroup(group vsphere.FetchGroup, partial func(*vsphere.Inventory)) *vsphere.Inventory {
	part := h.inv.Slice(group)
	if partial != nil {
		partial(h.inv.Slice(group))
	}
	return part
}

func (b *fakeBackend) Status(name string) (session.Status, bool) {
	return session.Status{Name: name, InstanceID: b.instanceIDs[name]}, false
}

func (b *fakeBackend) Diagnose(_ context.Context, cc *config.Context) *vsphere.Diagnosis {
	return b.diagnoses[cc.Name]
}

func (b *fakeBackend) diagnosisFor(name string) *vsphere.Diagnosis {
	if d, ok := b.testDiag[name]; ok {
		return d
	}
	return &vsphere.Diagnosis{
		Context: name,
		Checks:  []vsphere.Check{{Name: "Configuration valid", Status: vsphere.CheckPass}, {Name: "Authentication", Status: vsphere.CheckPass}},
	}
}

func (b *fakeBackend) TestContext(_ context.Context, in contextops.Input) (*config.Context, *vsphere.Diagnosis) {
	return contextops.Build(in), b.diagnosisFor(in.Name)
}

func (b *fakeBackend) SaveContext(_ context.Context, in contextops.Input, test bool) (*contextops.Result, error) {
	cc := contextops.Build(in)
	res := &contextops.Result{Context: cc}
	if err := cc.Validate(); err != nil {
		return res, err
	}
	if test {
		res.Diagnosis = b.diagnosisFor(in.Name)
		if !res.Diagnosis.OK() && !in.SaveOnTestFailure {
			return res, errors.New("connection test failed")
		}
	}
	if err, ok := b.saveErr[in.Name]; ok && err != nil {
		return res, err
	}
	replaced := false
	for i, existing := range b.contexts {
		if existing.Name == cc.Name {
			b.contexts[i] = cc
			replaced = true
			break
		}
	}
	if !replaced {
		b.contexts = append(b.contexts, cc)
	}
	if in.SetCurrent {
		b.currentContext = cc.Name
	}
	return res, nil
}

func (b *fakeBackend) RemoveContext(_ context.Context, name string, _ bool) (*config.Context, error) {
	for i, cc := range b.contexts {
		if cc.Name == name {
			b.contexts = append(b.contexts[:i:i], b.contexts[i+1:]...)
			if b.currentContext == name {
				b.currentContext = ""
			}
			return cc, nil
		}
	}
	return nil, fmt.Errorf("context %q not found", name)
}

func (b *fakeBackend) DiscoverThumbprint(_ context.Context, cc *config.Context) (sha256, sha1, subject string, notAfter time.Time, err error) {
	if b.discoverErr != nil {
		return "", "", "", time.Time{}, b.discoverErr
	}
	thumb := b.discoverThumb
	if thumb == "" {
		thumb = "AA:BB:CC:DD:EE:FF"
	}
	subj := b.discoverSubj
	if subj == "" {
		subj = cc.Host()
	}
	return thumb, thumb, subj, time.Now().AddDate(1, 0, 0), nil
}

func ctx(name, endpoint string) *config.Context {
	cc := &config.Context{Name: name, Endpoint: endpoint, Username: "operator@vsphere.local"}
	cc.Normalize()
	return cc
}

func inventoryFor(name string) *vsphere.Inventory {
	loc := func(kind, obj string) vsphere.Location {
		return vsphere.Location{Context: name, Datacenter: "Taipei", Path: "/Taipei/" + kind + "/" + obj}
	}
	return &vsphere.Inventory{
		Context: name,
		VMs: []vsphere.VM{
			{
				Location: loc("vm", "app-01"), ID: name + "-vm-1", Name: "app-01",
				PowerState: "poweredOn", CPU: 4, MemoryMB: 16384, GuestOS: "Ubuntu Linux (64-bit)",
				IPAddress: "10.20.0.11", Host: "esxi-01", Cluster: "compute", Folder: "/Apps",
				Datastores: []string{"nvme-01"}, StorageGB: 64, Annotation: "front end",
			},
			{
				Location: loc("vm", "build-runner-3"), ID: name + "-vm-2", Name: "build-runner-3",
				PowerState: "poweredOff", CPU: 8, MemoryMB: 32768, Host: "esxi-02", Cluster: "compute",
			},
		},
		Templates: []vsphere.VM{
			{
				Location: loc("vm", "ubuntu-24.04-golden"), ID: name + "-tpl-1", Name: "ubuntu-24.04-golden",
				IsTemplate: true, CPU: 2, MemoryMB: 4096, GuestOS: "Ubuntu Linux (64-bit)", StorageGB: 12,
			},
		},
		Hosts: []vsphere.Host{
			{
				Location: loc("host", "esxi-01"), ID: name + "-host-1", Name: "esxi-01",
				Cluster: "compute", PowerState: "poweredOn", ConnectionState: "connected",
				Vendor: "Dell Inc.", Model: "PowerEdge R750", Version: "8.0.3", Build: "24022515",
				CPUCores: 32, CPUThreads: 64, CPUMHz: 2400, MemoryMB: 524288,
				CPUUsageMHz: 18000, MemoryUsageMB: 262144, VMCount: 41,
			},
		},
		Clusters: []vsphere.Cluster{
			{
				Location: loc("host", "compute"), ID: name + "-cl-1", Name: "compute",
				Hosts: 4, EffectiveHost: 4, CPUCores: 128, TotalCPUMHz: 307200,
				TotalMemoryMB: 2097152, DRSEnabled: true, HAEnabled: true,
			},
		},
		VApps: []vsphere.VApp{
			{
				Location: loc("host", "app-container"), ID: name + "-vapp-1", Name: "app-container",
				Status: "started", ParentContainer: "compute/Resources", DirectVMCount: 1,
				DirectVMs: []string{"app-01"}, DirectVMRefs: []string{"VirtualMachine:" + name + "-vm-1"},
				Cluster: "compute", ComputeResource: "compute",
			},
		},
		Datastores: []vsphere.Datastore{
			{
				Location: loc("datastore", "nvme-01"), ID: name + "-ds-1", Name: "nvme-01",
				Type: "VMFS", Accessible: true,
				CapacityBytes: 8 << 40, FreeBytes: 2 << 40,
			},
		},
		Networks: []vsphere.Network{
			{
				Location: loc("network", "vlan-200"), ID: name + "-net-1", Name: "vlan-200",
				Type: "DistributedVirtualPortgroup", Accessible: true,
			},
		},
	}
}

func newTestModel(t *testing.T, b *fakeBackend, opts Options) *Model {
	t.Helper()
	if opts.RefreshInterval == 0 {
		// The real default arms a one-minute timer. These tests run commands
		// synchronously rather than through the Bubble Tea runtime, so a live
		// timer would not tick in the background — it would block the test
		// that ran it. Refresh behaviour is driven explicitly instead, by
		// tickRefresh below.
		opts.RefreshInterval = -1
	}
	if opts.Handoff == nil {
		// Never fall back to the real one: it would run "ssh -G" on the
		// machine running the tests and put its own username in the labels.
		opts.Handoff = &fakeHandoff{}
	}
	m := New(context.Background(), b, opts)
	m.width, m.height = 140, 30
	// A blinking cursor schedules a real half-second timer, and these tests run
	// commands synchronously rather than through the Bubble Tea runtime.
	m.filter.Cursor.SetMode(cursor.CursorStatic)
	drive(t, m, m.Init())
	settleForm(m)
	return m
}

// settleForm strips the blink timer from every field of an open form, the
// same reason newTestModel does it for the filter: typing a character that
// moves the cursor position schedules a real ~500ms timer once per
// keystroke, which a synchronous test harness blocks on rather than letting
// run in the background the way the real Bubble Tea event loop would.
func settleForm(m *Model) {
	if m.form == nil {
		return
	}
	f := m.form
	for _, ti := range []*textinput.Model{
		&f.name, &f.endpoint, &f.username, &f.password,
		&f.datacenter, &f.proxyAddr, &f.proxyUser, &f.proxyPass, &f.thumbprint,
	} {
		ti.Cursor.SetMode(cursor.CursorStatic)
	}
}

// drive runs every command a model produced and feeds the resulting messages
// back in. Spinner ticks are dropped: they schedule themselves, and the test
// is interested in what the interface shows, not in how it animates.
func drive(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	for _, msg := range collect(cmd) {
		if _, ok := msg.(spinner.TickMsg); ok {
			continue
		}
		drive(t, m, discard(m.Update(msg)))
	}
}

func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, collect(c)...)
		}
		return out
	default:
		return []tea.Msg{msg}
	}
}

func discard(_ tea.Model, cmd tea.Cmd) tea.Cmd { return cmd }

// tickRefresh applies one background-refresh cycle and drives the reads it
// starts. It calls the policy directly rather than sending refreshTickMsg,
// because handling that message also arms the next real timer, which a
// synchronous harness would then sit and wait on.
func tickRefresh(t *testing.T, m *Model) {
	t.Helper()
	for _, cmd := range m.refreshStale() {
		drive(t, m, cmd)
	}
}

// press sends a key by name, the way a person would type it.
func press(t *testing.T, m *Model, keys ...string) {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "left":
			msg = tea.KeyMsg{Type: tea.KeyLeft}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		drive(t, m, discard(m.Update(msg)))
	}
}

// typeText sends a whole string as one key message, the way pasting or a
// fast typist would, rather than one press call per rune.
func typeText(t *testing.T, m *Model, s string) {
	t.Helper()
	drive(t, m, discard(m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})))
}

func twoHealthy() *fakeBackend {
	return &fakeBackend{
		contexts: []*config.Context{ctx("customer-a", "https://vcsa.customer-a.internal"), ctx("prod", "https://vcsa.prod.internal")},
		inventories: map[string]*vsphere.Inventory{
			"customer-a": inventoryFor("customer-a"),
			"prod":       inventoryFor("prod"),
		},
	}
}

// TestBrowseShowsOnlyTheSelectedContext checks that a single-context scope
// controls both what the table displays and what gets fetched: Init loads
// only what is in scope at start-up (issue #27), so customer-a is left
// untouched until the reader actually asks for it — by switching to it,
// widening scope, or opening the estate-wide search.
func TestBrowseShowsOnlyTheSelectedContext(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	if got := b.calls["prod"]; got != 1 {
		t.Fatalf("prod fetched %d times, want 1", got)
	}
	if got := b.calls["customer-a"]; got != 0 {
		t.Errorf("customer-a fetched %d times, want 0 — it is off screen and nobody asked for it", got)
	}
	out := m.View()
	if !strings.Contains(out, "app-01") {
		t.Errorf("VM list is missing app-01:\n%s", out)
	}
	// Both contexts' fake inventories are identical (same VM names), so the
	// count is what proves scope: merged, it would be 4; scoped to prod
	// alone, 2.
	if n := len(m.rows()); n != 2 {
		t.Errorf("a single-context scope must show only prod's rows, got %d", n)
	}
	// The browse screen names only the vCenter in scope; the other one is on
	// the contexts screen, which is where switching happens now.
	if !strings.Contains(out, "prod") {
		t.Errorf("header should name the context in scope:\n%s", out)
	}
	press(t, m, "c")
	out = m.View()
	for _, name := range []string{"prod", "customer-a"} {
		if !strings.Contains(out, name) {
			t.Errorf("contexts screen is missing %q:\n%s", name, out)
		}
	}
}

// TestInitialViewDoesNotNeedCredentialWork proves the ordering contract at the
// model boundary: constructing and rendering the pane is side-effect free, and
// the first connection is only invoked when the command returned by Init is
// actually run by Bubble Tea.
func TestInitialViewDoesNotNeedCredentialWork(t *testing.T) {
	b := twoHealthy()
	m := New(context.Background(), b, Options{Current: "prod", RefreshInterval: -1})
	m.width, m.height = 140, 30

	if got := b.calls["prod"]; got != 0 {
		t.Fatalf("constructing the model contacted prod %d times", got)
	}
	if out := m.View(); !strings.Contains(out, "prod") {
		t.Fatalf("the initial pane does not render the selected context:\n%s", out)
	}
	cmd := m.Init()
	if got := b.calls["prod"]; got != 0 {
		t.Fatalf("Init performed connection work before its command ran: %d calls", got)
	}
	if st := m.byName["prod"]; !st.loading {
		t.Fatal("Init should render the selected context as loading before releasing its startup command")
	}
	drive(t, m, cmd)
	if got := b.calls["prod"]; got != 1 {
		t.Fatalf("running Init's command made %d calls, want 1", got)
	}
}

func TestTabsSwitchTheResourceKind(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})

	if !strings.Contains(m.View(), "IP ADDRESS") {
		t.Fatalf("VM tab should show the IP column:\n%s", m.View())
	}
	press(t, m, "right") // Templates
	if m.kind != vsphere.KindTemplate {
		t.Fatalf("kind is %q, want template", m.kind)
	}
	out := m.View()
	if !strings.Contains(out, "ubuntu-24.04-golden") {
		t.Errorf("template tab is missing the template:\n%s", out)
	}
	if strings.Contains(out, "app-01") {
		t.Errorf("template tab is still showing virtual machines:\n%s", out)
	}
	press(t, m, "left", "left") // wrap backwards to the vApp tab
	if m.kind != vsphere.KindVApp {
		t.Fatalf("kind is %q, want vApp after wrapping backwards", m.kind)
	}
	press(t, m, "6") // numeric shortcuts preserve Network as kind 6
	if m.kind != vsphere.KindNetwork {
		t.Fatalf("kind is %q, want network after its numeric shortcut", m.kind)
	}
}

func TestFilterNarrowsTheTable(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})

	press(t, m, "/", "b", "u", "i", "l", "d")
	rows := m.rows()
	if len(rows) != 1 || rows[0].name != "build-runner-3" {
		t.Fatalf("filter %q kept %d rows, want only build-runner-3", m.filter.Value(), len(rows))
	}
	press(t, m, "enter") // accept the filter, keep it applied
	if m.filtering {
		t.Error("enter should leave the filter input")
	}
	if got := len(m.rows()); got != 1 {
		t.Errorf("filter was dropped on enter: %d rows", got)
	}
	press(t, m, "esc")
	if got := len(m.rows()); got != 2 {
		t.Errorf("esc should clear the filter, got %d rows", got)
	}
}

func TestFocusedFilterControls(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})

	press(t, m, "/")
	typeText(t, m, "q")
	if got := m.filter.Value(); got != "q" {
		t.Fatalf("typing q into the focused filter produced %q, want q", got)
	}
	if m.quitting {
		t.Fatal("typing q into the focused filter must not quit")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c in the focused filter should return tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c in the focused filter should quit the program")
	}
	if !m.quitting {
		t.Fatal("ctrl+c in the focused filter should set quitting")
	}
}

func TestArrowKeysMoveSelectionWhileFilterIsFocused(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})

	press(t, m, "/")
	if got := len(m.rows()); got < 2 {
		t.Fatalf("need at least two rows to move between, got %d", got)
	}
	press(t, m, "down")
	if !m.filtering {
		t.Fatal("down must not leave the focused filter")
	}
	if m.cursor != 1 {
		t.Fatalf("down while filtering should move the cursor to 1, got %d", m.cursor)
	}
	press(t, m, "up")
	if m.cursor != 0 {
		t.Fatalf("up while filtering should move the cursor back to 0, got %d", m.cursor)
	}
	typeText(t, m, "j")
	if got := m.filter.Value(); got != "j" {
		t.Fatalf("letters must still type into the filter, got %q", got)
	}
}

func TestArrowKeysMoveSelectionWhileSearchIsFocused(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})

	press(t, m, "tab")
	if m.mode != modeSearch || !m.filtering {
		t.Fatalf("tab should open a focused search, mode=%v filtering=%v", m.mode, m.filtering)
	}
	typeText(t, m, "-") // matches every generated name, so there is a list to walk
	if got := len(m.visibleRows()); got < 2 {
		t.Fatalf("need at least two search results to move between, got %d", got)
	}
	press(t, m, "down")
	if !m.filtering || m.cursor != 1 {
		t.Fatalf("down should move within results while still typing, filtering=%v cursor=%d", m.filtering, m.cursor)
	}
}

func TestFocusedFilterEscapeClearsLocalFilter(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})

	press(t, m, "/")
	typeText(t, m, "build")
	press(t, m, "esc")

	if m.mode != modeBrowse {
		t.Fatalf("esc from a focused local filter should stay in browse mode, got %v", m.mode)
	}
	if m.filtering {
		t.Fatal("esc should leave the local filter input")
	}
	if got := m.filter.Value(); got != "" {
		t.Errorf("esc should clear the local filter, got %q", got)
	}
	if got := len(m.rows()); got != 2 {
		t.Errorf("esc should restore all local rows, got %d", got)
	}
}

func TestFocusedSearchControls(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})

	press(t, m, "tab")
	if m.mode != modeSearch || !m.filtering {
		t.Fatalf("tab should open a focused estate search, mode=%v filtering=%v", m.mode, m.filtering)
	}
	typeText(t, m, "q")
	if got := m.filter.Value(); got != "q" {
		t.Fatalf("typing q into the focused search produced %q, want q", got)
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c in the focused search should return tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c in the focused search should quit the program")
	}
	if !m.quitting {
		t.Fatal("ctrl+c in the focused search should set quitting")
	}
}

func TestSearchOpenSwitchesToVAppContextAndTab(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "R")

	press(t, m, "tab")
	typeText(t, m, "app-container")
	press(t, m, "enter")
	press(t, m, "enter")

	if m.mode != modeVAppDetail {
		t.Fatalf("opening a search result should show the vAPP workspace, mode=%v", m.mode)
	}
	if m.kind != vsphere.KindVApp || m.selected != 0 || m.allScope {
		t.Fatalf("search result did not switch to its context/tab: kind=%q selected=%d all=%v", m.kind, m.selected, m.allScope)
	}
	if r, ok := m.currentRow(); !ok || r.name != "app-container" || r.kind != vsphere.KindVApp || r.context != "customer-a" {
		t.Fatalf("detail selection = %+v, %v", r, ok)
	}
	press(t, m, "esc")
	if m.mode != modeBrowse || m.kind != vsphere.KindVApp || m.allScope {
		t.Fatalf("leaving search detail did not land on the vApp browse tab: mode=%v kind=%q all=%v", m.mode, m.kind, m.allScope)
	}
}

func TestFocusedSearchEscapeReturnsToBrowse(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})

	press(t, m, "tab")
	typeText(t, m, "ubuntu")
	press(t, m, "esc")

	if m.mode != modeBrowse {
		t.Fatalf("one esc from a focused estate search should return to browse, got %v", m.mode)
	}
	if m.filtering {
		t.Fatal("esc should leave the estate search input")
	}
	if got := m.filter.Value(); got != "ubuntu" {
		t.Errorf("leaving estate search should preserve the shared query, got %q", got)
	}
}

func TestAllContextsMergesAndLabelsRows(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "a")
	if !m.allScope {
		t.Fatal("'a' should widen the scope to every vCenter")
	}
	if got := len(m.rows()); got != 2 {
		t.Fatalf("all-contexts shows %d loaded VMs, want 2 until the other context is selected", got)
	}
	if got := b.calls["customer-a"]; got != 0 {
		t.Fatalf("widening the view contacted the unselected context %d times", got)
	}
	// Reload-all is an explicit request for the rest of the estate.
	press(t, m, "R")
	if got := len(m.rows()); got != 4 {
		t.Fatalf("explicit reload-all shows %d VMs, want 4 (two per vCenter)", got)
	}
	out := m.View()
	if !strings.Contains(out, "VCENTER") {
		t.Errorf("merged view must name the vCenter each row came from:\n%s", out)
	}
	press(t, m, "a")
	if got := len(m.rows()); got != 2 {
		t.Errorf("narrowing back gives %d rows, want 2", got)
	}
}

// TestOneBrokenVCenterKeepsTheRest is the behaviour the whole tool exists for,
// restated in the interface: a customer environment behind a dead proxy costs
// one line, not the screen.
func TestOneBrokenVCenterKeepsTheRest(t *testing.T) {
	b := twoHealthy()
	b.failures = map[string]error{"customer-a": errors.New("socks5 proxy 127.0.0.1:1080 unreachable: connection refused")}
	m := newTestModel(t, b, Options{Current: "prod", AllContexts: true})
	if b.calls["customer-a"] != 0 {
		t.Fatalf("the unselected broken context was contacted at startup: %d calls", b.calls["customer-a"])
	}
	press(t, m, "R")

	if got := len(m.rows()); got != 2 {
		t.Fatalf("healthy vCenter contributed %d rows, want 2", got)
	}
	out := m.View()
	if !strings.Contains(out, "app-01") {
		t.Errorf("healthy results were lost:\n%s", out)
	}
	if !strings.Contains(out, "socks5 proxy") {
		t.Errorf("the failure must be reported, not swallowed:\n%s", out)
	}
	if !strings.Contains(out, "1 failed") {
		t.Errorf("header should count the failure:\n%s", out)
	}
}

func TestDetailViewShowsEveryProperty(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})

	press(t, m, "enter")
	if m.mode != modeDetail {
		t.Fatalf("enter on a row should open the detail view, mode is %v", m.mode)
	}
	out := m.View()
	for _, want := range []string{"app-01", "Ubuntu Linux (64-bit)", "10.20.0.11", "16G", "esxi-01", "/Taipei/vm/app-01", "front end"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail view is missing %q:\n%s", want, out)
		}
	}
	press(t, m, "esc")
	if m.mode != modeBrowse {
		t.Error("esc should return to the table")
	}
}

func TestVAppWorkspaceShowsNestedVMsAndSupportsDrillDown(t *testing.T) {
	b := twoHealthy()
	inv := *b.inventories["prod"]
	inv.VMs = append(inv.VMs, vsphere.VM{
		Location: vsphere.Location{Context: "prod", Datacenter: "Taipei", Path: "/Taipei/vm/postgres-01"},
		ID:       "prod-vm-nested", Name: "postgres-01", PowerState: "poweredOn", CPU: 8, MemoryMB: 32768, Host: "esxi-02",
	})
	inv.VApps[0].ChildVApps = []string{"web-cache"}
	inv.VApps[0].ChildVAppRefs = []string{"VirtualApp:prod-vapp-2"}
	inv.VApps[0].ChildVAppCount = 1
	inv.VApps[0].ChildResourcePools = []string{"app-pool"}
	inv.VApps[0].ChildResourcePoolRefs = []string{"ResourcePool:prod-pool-1"}
	inv.VApps[0].ChildResourcePoolCount = 1
	inv.VApps = append(inv.VApps, vsphere.VApp{
		Location: vsphere.Location{Context: "prod", Datacenter: "Taipei", Path: "/Taipei/host/web-cache"},
		ID:       "prod-vapp-2", Name: "web-cache", Status: "stopped", ParentVApp: "app-container",
		DirectVMCount: 1, DirectVMs: []string{"postgres-01"}, DirectVMRefs: []string{"VirtualMachine:prod-vm-nested"},
	})
	b.inventories["prod"] = &inv
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "7", "enter")

	if m.mode != modeVAppDetail {
		t.Fatalf("enter on a vAPP should open its workspace, mode=%v", m.mode)
	}
	out := m.View()
	for _, want := range []string{"Summary", "Members", "web-cache", "postgres-01", "app-01", "app-pool"} {
		if !strings.Contains(out, want) {
			t.Errorf("vAPP workspace is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Direct VMs") {
		t.Error("vAPP workspace should expose members in the hierarchy, not only in a detail field")
	}

	press(t, m, "down") // nested postgres-01 member
	if m.vapp == nil || m.vapp.cursor != 1 {
		t.Fatalf("down should select the nested VM, workspace=%+v", m.vapp)
	}
	press(t, m, "enter")
	if m.mode != modeVAppVMDetail || !strings.Contains(m.View(), "postgres-01") {
		t.Fatalf("enter on a nested VM should open VM detail, mode=%v:\n%s", m.mode, m.View())
	}
	press(t, m, "esc")
	if m.mode != modeVAppDetail || m.vapp == nil || m.vapp.cursor != 1 {
		t.Fatalf("Esc should restore the vAPP member selection, mode=%v workspace=%+v", m.mode, m.vapp)
	}
	press(t, m, "esc")
	if m.mode != modeBrowse || m.kind != vsphere.KindVApp {
		t.Fatalf("Esc from the root workspace should return to the vAPP list, mode=%v kind=%q", m.mode, m.kind)
	}
}

func TestVAppMemberDetailOffersTheMemberActionsNotTheVAppActions(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "7", "enter", "enter")
	m.openFieldActions()

	if m.mode != modeVAppVMDetail || m.vappVM == nil {
		t.Fatalf("expected the vAPP member detail pane, mode=%v member=%+v", m.mode, m.vappVM)
	}
	if m.actions == nil {
		t.Fatalf("member header should open an action list: mode=%v cursor=%d member=%+v", m.mode, m.detailCursor, m.vappVM)
	}
	if _, ok := findAction(m.actions.items, "SSH to 10.20.0.11"); !ok {
		t.Fatalf("member detail opened the wrong actions: %+v", m.actions)
	}
}

func TestVAppMemberDetailResetsTheFieldCursorOnEntry(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	m.detailCursor = 8
	press(t, m, "7", "enter", "enter")

	if m.detailCursor != 0 {
		t.Fatalf("member detail cursor is %d, want 0", m.detailCursor)
	}
}

func TestVAppDetailResetsTheFieldCursorOnEntry(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	m.detailCursor = 8
	press(t, m, "7", "enter")

	if m.detailCursor != 0 {
		t.Fatalf("vAPP detail cursor is %d, want 0", m.detailCursor)
	}
}

func TestVAppMemberDetailCursorWalksTheMember(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "7", "enter", "enter")
	for range 20 {
		press(t, m, "down")
	}

	want := len(m.vappVM.detail) + 1
	if m.detailCursor != want {
		t.Fatalf("member detail cursor stopped at %d, want %d", m.detailCursor, want)
	}
}

func TestJumpFromAVAppMemberLeavesTheWorkspace(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "7", "enter", "enter")
	idx := -1
	for i, f := range m.vappVM.detail {
		if f.label == "Host" {
			idx = i + 2
			break
		}
	}
	if idx < 0 {
		t.Fatal("member detail has no Host field")
	}
	m.detailCursor = idx
	press(t, m, "enter", "enter")

	if m.mode != modeBrowse || m.vapp != nil || m.vappVM != nil {
		t.Fatalf("jump should leave the vAPP workspace, mode=%v vapp=%+v member=%+v", m.mode, m.vapp, m.vappVM)
	}
}

func TestVAppWorkspaceFitsMinimumWidth(t *testing.T) {
	b := twoHealthy()
	nested := ctx("nested-vcenter-context-with-a-long-name", "https://10.20.0.11")
	nested.Via, nested.ViaMoRef = "prod", "prod-vm-1"
	b.contexts = append(b.contexts, nested)
	m := newTestModel(t, b, Options{Current: "prod"})
	m.width, m.height = minTermWidth, 24
	press(t, m, "7", "enter")
	out := m.View()
	for _, line := range strings.Split(m.View(), "\n") {
		if width := ansi.StringWidth(line); width > minTermWidth {
			t.Fatalf("vAPP detail line is %d columns wide at minimum width: %q", width, line)
		}
	}
	if !strings.Contains(out, "app-01") {
		t.Errorf("minimum-width vAPP workspace should retain the VM member name:\n%s", out)
	}
	if strings.Contains(out, "→ nested-vcenter-context-with-a-long-name") {
		t.Errorf("minimum-width vAPP workspace should truncate the annotation suffix:\n%s", out)
	}
}

func TestVAppMemberIsMarkedWithItsSavedContext(t *testing.T) {
	b := twoHealthy()
	nested := ctx("nested-app", "https://10.20.0.11")
	nested.Via, nested.ViaMoRef = "prod", "prod-vm-1"
	b.contexts = append(b.contexts, nested)
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "7", "enter")

	for _, member := range m.vappMembers(mustActiveVApp(t, m), m.byName["prod"].inv) {
		if member.name == "app-01" {
			if member.asContext != "nested-app" {
				t.Fatalf("member annotation is %q, want nested-app", member.asContext)
			}
			return
		}
	}
	t.Fatal("app-01 member not found")
}

func TestHandMadeContextStillAnnotatesAMember(t *testing.T) {
	b := twoHealthy()
	b.contexts = append(b.contexts, ctx("handmade", "https://10.20.0.11"))
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "7", "enter")

	for _, member := range m.vappMembers(mustActiveVApp(t, m), m.byName["prod"].inv) {
		if member.name == "app-01" {
			if member.asContext != "handmade" {
				t.Fatalf("member annotation is %q, want handmade", member.asContext)
			}
			return
		}
	}
	t.Fatal("app-01 member not found")
}

func mustActiveVApp(t *testing.T, m *Model) *vsphere.VApp {
	t.Helper()
	root, _, ok := m.activeVApp()
	if !ok || root == nil {
		t.Fatal("vAPP workspace has no active root")
	}
	return root
}

func TestVAppWorkspaceReportsMissingAndCyclicMembers(t *testing.T) {
	b := twoHealthy()
	inv := *b.inventories["prod"]
	inv.VApps[0].ChildVApps = []string{"lost-vapp"}
	inv.VApps[0].ChildVAppRefs = []string{"VirtualApp:prod-vapp-1", "VirtualApp:missing-vapp"}
	inv.VApps[0].ChildVAppCount = 2
	b.inventories["prod"] = &inv
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "7", "enter")
	out := m.View()
	for _, want := range []string{"cycle", "vAPP missing-vapp", "lost-vapp"} {
		if !strings.Contains(out, want) {
			t.Errorf("vAPP workspace is missing unresolved-member marker %q:\n%s", want, out)
		}
	}
}

func TestContextsScreenSwitchesContext(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "c")
	if m.mode != modeContexts {
		t.Fatalf("'c' should open the contexts screen, mode is %v", m.mode)
	}
	press(t, m, "up") // customer-a sorts first in the fixture order
	press(t, m, "enter")

	if m.mode != modeBrowse {
		t.Fatalf("choosing a context should return to the table, mode is %v", m.mode)
	}
	if m.current().cc.Name != "customer-a" {
		t.Fatalf("selected context is %q, want customer-a", m.current().cc.Name)
	}
	if m.allScope {
		t.Error("choosing one vCenter should narrow the scope to it")
	}
	if b.calls["customer-a"] != 1 {
		t.Errorf("switching to a context should fetch it once, got %d", b.calls["customer-a"])
	}
}

// TestContextsScreenShowsRouteAndFailure is the point of the screen: it
// carries what the sidebar used to, with room to say why a vCenter is not
// answering rather than truncating the reason to nothing.
func TestContextsScreenShowsRouteAndFailure(t *testing.T) {
	b := twoHealthy()
	b.failures = map[string]error{"customer-a": errors.New("socks5 proxy 127.0.0.1:1080 unreachable")}
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "c")
	out := m.View()
	for _, want := range []string{"Contexts", "prod", "https://vcsa.prod.internal", "customer-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("contexts screen is missing %q:\n%s", want, out)
		}
	}
	// customer-a is off screen and was never loaded at start-up (issue #27),
	// so the screen must not claim it failed before anyone asked it to
	// connect — that would misreport "not yet tried" as "broken".
	if !strings.Contains(out, "not connected") {
		t.Errorf("customer-a has not been reached yet, contexts screen should say so rather than showing a failure:\n%s", out)
	}
	if strings.Contains(out, "socks5 proxy 127.0.0.1:1080 unreachable") {
		t.Errorf("customer-a's failure appeared before it was ever asked to connect:\n%s", out)
	}

	// Reloading it — what an operator does to find out why a vCenter is not
	// answering — is what actually surfaces the failure.
	press(t, m, "up") // customer-a sorts first in the fixture order
	press(t, m, "r")
	out = m.View()
	if !strings.Contains(out, "socks5 proxy 127.0.0.1:1080 unreachable") {
		t.Errorf("reloading customer-a should surface its failure:\n%s", out)
	}
}

// TestContextsScreenWidensScope checks the other way out of the screen: "a"
// asks for every vCenter at once rather than choosing one.
func TestContextsScreenWidensScope(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "c", "a")
	if m.mode != modeBrowse {
		t.Fatalf("'a' should return to the table, mode is %v", m.mode)
	}
	if !m.allScope {
		t.Fatal("'a' on the contexts screen should widen the scope to every vCenter")
	}
	if got := len(m.rows()); got != 2 {
		t.Errorf("all-contexts shows %d loaded VMs, want 2 until the other context is selected", got)
	}
	if got := b.calls["customer-a"]; got != 0 {
		t.Errorf("opening all-contexts contacted customer-a %d times", got)
	}
}

// TestSearchWidensAFilterThatFoundNothing is the whole point of the escalation:
// the VM tab of one vCenter has no "ubuntu" in it, and the template on both
// vCenters is exactly what the reader was looking for.
func TestSearchWidensAFilterThatFoundNothing(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "/")
	typeText(t, m, "ubuntu")
	if got := len(m.rows()); got != 0 {
		t.Fatalf("the VM tab of one vCenter has %d ubuntu rows, want 0", got)
	}
	// The offer has to be visible, or nobody presses the key.
	if out := m.View(); !strings.Contains(out, "tab to widen") {
		t.Errorf("a filter that found less than the estate should offer to widen:\n%s", out)
	}

	press(t, m, "tab")
	if m.mode != modeSearch {
		t.Fatalf("tab should open the estate-wide search, mode is %v", m.mode)
	}
	rows := m.visibleRows()
	if len(rows) != 1 {
		t.Fatalf("search found %d matches, want 1 from the selected vCenter until the other is loaded", len(rows))
	}
	for _, r := range rows {
		if r.kind != vsphere.KindTemplate {
			t.Errorf("match %q is kind %q, want template", r.name, r.kind)
		}
	}
	if b.calls["customer-a"] != 0 {
		t.Fatalf("opening search contacted customer-a %d times", b.calls["customer-a"])
	}
	out := m.View()
	for _, want := range []string{"VCENTER", "TYPE", "prod", "customer-a", "ubuntu-24.04-golden"} {
		if !strings.Contains(out, want) {
			t.Errorf("search results are missing %q:\n%s", want, out)
		}
	}

	// The query survives narrowing back: the filter and the search are the
	// same query at two widths.
	press(t, m, "tab")
	if m.mode != modeBrowse {
		t.Fatalf("tab should return to the table, mode is %v", m.mode)
	}
	if m.filter.Value() != "ubuntu" {
		t.Errorf("narrowing back lost the query: %q", m.filter.Value())
	}
}

// TestSearchIgnoresScopeAndKind checks the two axes it widens: it must answer
// from every vCenter and every kind regardless of which tab is open or which
// context is in scope.
func TestSearchIgnoresScopeAndKind(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "3") // hosts, still scoped to prod alone
	press(t, m, "tab")
	typeText(t, m, "esxi-01")

	rows := m.visibleRows()
	if len(rows) != 1 {
		t.Fatalf("search found %d hosts, want 1 from the selected vCenter until the other is loaded", len(rows))
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.context] = true
	}
	if !seen["prod"] || seen["customer-a"] {
		t.Errorf("search contacted an unselected context, saw %v", seen)
	}
}

// TestSearchNamesTheVCentersItCouldNotRead is the honesty requirement: fewer
// matches because a proxy is down, without saying so, is the one way these
// results could mislead.
func TestSearchNamesTheVCentersItCouldNotRead(t *testing.T) {
	b := twoHealthy()
	b.failures = map[string]error{"customer-a": errors.New("socks5 proxy 127.0.0.1:1080 unreachable")}
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "tab")
	typeText(t, m, "ubuntu")

	if got := len(m.visibleRows()); got != 1 {
		t.Fatalf("search found %d matches, want 1 — only prod was selected", got)
	}
	out := m.View()
	for _, want := range []string{"customer-a", "not searched"} {
		if !strings.Contains(out, want) {
			t.Errorf("search must name the vCenter it could not read, missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "socks5 proxy 127.0.0.1:1080 unreachable") {
		t.Errorf("search exposed an error for customer-a before it was selected:\n%s", out)
	}
	// The explicit reload-all action is allowed to contact it and then reports
	// its failure without affecting the selected context's result.
	press(t, m, "enter", "R")
	if !strings.Contains(m.View(), "socks5 proxy 127.0.0.1:1080 unreachable") {
		t.Errorf("explicit reload-all should surface customer-a's failure:\n%s", m.View())
	}
}

// TestSearchResultOpensItsOwnKind checks that a detail pane opened from a
// search result describes what the row actually is, not whichever tab happened
// to be open behind it, and that esc goes back to the selected kind's table.
func TestSearchResultOpensItsOwnKind(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})

	press(t, m, "tab") // the VM tab is open behind the search
	typeText(t, m, "ubuntu")
	press(t, m, "enter") // commit the query, as in the filter
	press(t, m, "enter") // open the row under the cursor

	if m.mode != modeDetail {
		t.Fatalf("enter should open the result, mode is %v", m.mode)
	}
	out := m.View()
	if !strings.Contains(out, "Template") {
		t.Errorf("detail should name the row's own kind:\n%s", out)
	}
	if strings.Contains(out, "Virtual machine") {
		t.Errorf("detail is describing the tab behind the search, not the result:\n%s", out)
	}

	press(t, m, "esc")
	if m.mode != modeBrowse || m.kind != vsphere.KindTemplate || m.allScope {
		t.Fatalf("esc should return to the selected kind's table, mode=%v kind=%q all=%v", m.mode, m.kind, m.allScope)
	}
}

// TestNumberKeysJumpToAKind is what replaced cycling: Networks is the sixth
// kind, and reaching it should cost one keystroke rather than five.
func TestNumberKeysJumpToAKind(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})

	press(t, m, "6")
	if m.kind != vsphere.KindNetwork {
		t.Fatalf("'6' should select networks, kind is %q", m.kind)
	}
	press(t, m, "1")
	if m.kind != vsphere.KindVM {
		t.Fatalf("'1' should select VMs, kind is %q", m.kind)
	}
	// The bar has to say which number is which, or the numbers are a secret.
	out := m.View()
	for i, want := range []string{"1 VMs", "2 Templates", "3 Hosts", "4 Clusters", "5 Datastores", "6 Networks", "7 vApps"} {
		if !strings.Contains(out, want) {
			t.Errorf("kind bar is missing %q (position %d):\n%s", want, i+1, out)
		}
	}
}

func TestMinimumWidthKeepsVAppAndHistoryVisible(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	m.width, m.height = minTermWidth, minTermHeight

	out := m.View()
	for _, want := range []string{"7 vApp", "H history"} {
		if !strings.Contains(out, want) {
			t.Errorf("%d-column browse screen is missing %q:\n%s", minTermWidth, want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > minTermWidth {
			t.Fatalf("line is %d columns wide, want at most %d: %q", w, minTermWidth, line)
		}
	}
}

// TestBrowseTableGetsTheWholeWidth is the reason the sidebar went. At 80
// columns the sidebar cost the table its IP address column — the field an
// operator most often opened the table to read. Nothing may overflow either:
// a key line that truncates is the other half of the same problem.
func TestBrowseTableGetsTheWholeWidth(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	m.width, m.height = 80, 24

	out := m.View()
	if !strings.Contains(out, "IP ADDRESS") {
		t.Errorf("an 80 column table should fit the IP column:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > 80 {
			t.Fatalf("line is %d columns wide, want at most 80: %q", w, line)
		}
	}

	// The host column costs another two columns beyond that; it should
	// appear as soon as it fits rather than staying dropped.
	m.width = 82
	if !strings.Contains(m.View(), "HOST") {
		t.Errorf("82 columns is enough for the host column:\n%s", m.View())
	}
}

func TestDoctorPanelShowsTheFailingStage(t *testing.T) {
	b := twoHealthy()
	b.failures = map[string]error{"prod": errors.New("login failed")}
	b.diagnoses = map[string]*vsphere.Diagnosis{
		"prod": {
			Context:  "prod",
			Endpoint: "https://vcsa.prod.internal",
			Route:    "Direct",
			TLS:      "Pinned thumbprint",
			Checks: []vsphere.Check{
				{Name: "Configuration valid", Status: vsphere.CheckPass},
				{Name: "TCP connection", Status: vsphere.CheckPass, Detail: "12 ms"},
				{Name: "Authentication", Status: vsphere.CheckFail, Err: errors.New("incorrect user name or password")},
				{Name: "API available", Status: vsphere.CheckSkip, Detail: "skipped"},
			},
		},
	}
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "d")
	if m.mode != modeDoctor {
		t.Fatalf("'d' should open the diagnosis panel, mode is %v", m.mode)
	}
	out := m.View()
	for _, want := range []string{"Diagnosing prod", "Authentication", "incorrect user name or password", "Stopped at the first failing stage"} {
		if !strings.Contains(out, want) {
			t.Errorf("diagnosis panel is missing %q:\n%s", want, out)
		}
	}
}

func TestHelpListsEveryBinding(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "?")
	out := m.View()
	for _, want := range []string{"Keys", "contexts", "all vCenters", "diagnose", "1-7", "history"} {
		if !strings.Contains(out, want) {
			t.Errorf("help is missing %q:\n%s", want, out)
		}
	}
}

// TestReloadRefetches checks 'r' against 'R': customer-a starts at a call
// count of 0 — it is off screen, and Init loads only what is in scope
// (issue #27) — so 'r' (scoped to prod alone) must leave it untouched, and
// only 'R' (every configured context) is what reaches it, for the first
// time.
func TestReloadRefetches(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})
	if b.calls["customer-a"] != 0 {
		t.Fatalf("customer-a calls after Init = %d, want 0 — it is off screen", b.calls["customer-a"])
	}
	press(t, m, "r")
	if b.calls["prod"] != 2 {
		t.Errorf("'r' should refetch the context in scope, calls: %d", b.calls["prod"])
	}
	if b.calls["customer-a"] != 0 {
		t.Errorf("'r' must not reach out of scope, customer-a calls: %d", b.calls["customer-a"])
	}
	press(t, m, "a", "r")
	if b.calls["customer-a"] != 0 {
		t.Errorf("'r' in all-context view must still reload only the selected context, customer-a calls: %d", b.calls["customer-a"])
	}
	press(t, m, "R")
	if b.calls["customer-a"] != 1 {
		t.Errorf("'R' should refetch every context, customer-a calls: %d", b.calls["customer-a"])
	}
}

// TestReloadFailureKeepsShowingTheStaleData is the stale-while-revalidate
// half of Section 4: a context that loaded once and whose next refresh fails
// must keep showing what it already had, with the failure noted rather than
// the table going blank.
func TestReloadFailureKeepsShowingTheStaleData(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})
	if n := len(m.rows()); n == 0 {
		t.Fatalf("prod has no rows after the initial load")
	}
	st := m.byName["prod"]
	if st.rowStatus() != statusGood {
		t.Fatalf("status after a healthy load = %v, want good", st.rowStatus())
	}

	if b.failures == nil {
		b.failures = map[string]error{}
	}
	b.failures["prod"] = errors.New("connection reset")
	press(t, m, "r")

	if n := len(m.rows()); n == 0 {
		t.Errorf("a failed refresh erased the stale rows instead of keeping them")
	}
	if st.err == nil {
		t.Error("a failed refresh left no error recorded")
	}
	if st.rowStatus() != statusWarn {
		t.Errorf("status after a stale-but-failing refresh = %v, want warn (not bad — there is still real data)", st.rowStatus())
	}
	if !m.messageBad || !strings.Contains(m.message, "refresh failed") {
		t.Errorf("message does not report the failed refresh: bad=%v %q", m.messageBad, m.message)
	}
}

// TestEditingAContextDropsWhatTheOldOneLoaded is the limit of the stale-data
// rule the previous test pins. Keeping the last inventory through a failed
// refresh is right while the name still means the same vCenter; once it has
// been edited to mean another one, that inventory is another server's and must
// go, however convenient it would look on screen.
//
// The edit is arranged to fail its first read on purpose: a cache that still
// held the old entry would answer with it — that is exactly what it is built
// to do — and the row would show a stale-but-plausible table for a vCenter it
// has never actually reached.
func TestEditingAContextDropsWhatTheOldOneLoaded(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})
	if n := len(m.rows()); n == 0 {
		t.Fatalf("prod has no rows after the initial load")
	}
	if st := m.byName["prod"]; st == nil || st.inv == nil {
		t.Fatal("prod has no inventory after the initial load")
	}

	press(t, m, "c", "e")
	settleForm(m)
	if !m.form.editing {
		t.Fatalf("'e' did not open an edit of prod")
	}
	// Row order while editing: Name(static) Endpoint Username Credential
	// Password Route TLS Datacenter Current Test Save Cancel.
	for range 7 {
		press(t, m, "down") // -> Datacenter
	}
	typeText(t, m, "Somewhere-Else")

	// From here the context describes a different vCenter, and that vCenter
	// does not answer.
	if b.failures == nil {
		b.failures = map[string]error{}
	}
	b.failures["prod"] = errors.New("no route to host")

	for range 3 {
		press(t, m, "down") // Current, Test, Save
	}
	press(t, m, "enter")

	st := m.byName["prod"]
	if st.inv != nil {
		t.Errorf("an edited context is still showing the old vCenter's inventory (%d VMs)", len(st.inv.VMs))
	}
	if n := len(m.rows()); n != 0 {
		t.Errorf("an edited context that cannot be reached is still showing %d rows from the old one", n)
	}
	if st.err == nil {
		t.Error("the failed read of the edited context was not recorded")
	}
	if st.rowStatus() != statusBad {
		t.Errorf("status = %v, want bad: there is no data behind this context, only another one's", st.rowStatus())
	}
}

// TestRemovingAContextDropsItsState keeps a removed context from haunting a
// later one that happens to reuse its name: inventory now lives on the
// contextState itself rather than in a separate cache keyed by name, so
// removing the context removes its data along with it.
func TestRemovingAContextDropsItsState(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})
	if st := m.byName["prod"]; st == nil || st.inv == nil {
		t.Fatal("prod has no inventory after the initial load")
	}

	press(t, m, "c", "x", "y")

	if _, err := findContext(b.contexts, "prod"); err == nil {
		t.Fatal("prod was not removed")
	}
	if _, ok := m.byName["prod"]; ok {
		t.Error("a removed context left its state behind")
	}
}

// TestTabBarFlagsAKindWithAListingError checks that a resource kind
// ListInventory could not enumerate (Inventory.Errors) is visible from the
// tab bar, not just discoverable by opening that tab and finding it empty.
func TestTabBarFlagsAKindWithAListingError(t *testing.T) {
	b := twoHealthy()
	inv := *b.inventories["prod"]
	inv.Datastores = nil
	inv.Errors = []vsphere.InventoryError{{Kind: vsphere.KindDatastore, Message: "permission denied"}}
	b.inventories["prod"] = &inv

	m := newTestModel(t, b, Options{Current: "prod"})
	out := m.View()
	if !strings.Contains(out, "Datastores") {
		t.Fatalf("tab bar is missing the Datastores tab:\n%s", out)
	}
	if !m.kindErrorInScope(vsphere.KindDatastore) {
		t.Error("kindErrorInScope(KindDatastore) = false, want true")
	}
	if m.kindErrorInScope(vsphere.KindHost) {
		t.Error("kindErrorInScope(KindHost) = true, want false — hosts were never denied")
	}
}

// TestNarrowTerminalKeepsTheNameColumn checks the column layout degrades by
// dropping detail from the right rather than by squeezing names into nothing.
func TestNarrowTerminalKeepsTheNameColumn(t *testing.T) {
	cols := columnsFor(vsphere.KindVM, false)
	widths := layoutColumns(cols, 40-glyphGutter)

	if widths[0] < minNameWidth {
		t.Errorf("name column is %d wide, want at least %d", widths[0], minNameWidth)
	}
	total, drawn := 0, 0
	for i, w := range widths {
		if w == 0 {
			continue
		}
		drawn++
		total += w
		if i > 0 && widths[i-1] == 0 {
			t.Errorf("column %q is drawn after a dropped column; columns must drop from the right", cols[i].title)
		}
	}
	if drawn == len(cols) {
		t.Error("a 40 column terminal should have dropped something")
	}
	if width := glyphGutter + total + cellGap*(drawn-1); width > 40 {
		t.Errorf("laid out %d columns of total width %d in 40 columns", drawn, width)
	}
}

// TestPaddingMeasuresStyledTextByTerminalCells guards the colored interface:
// ANSI escape sequences change presentation but occupy no terminal columns.
// Counting their bytes used to truncate sidebar names to one character as
// soon as color output was enabled.
func TestPaddingMeasuresStyledTextByTerminalCells(t *testing.T) {
	styled := "\x1b[38;2;126;231;135m●\x1b[0m prod-vc"
	got := pad(styled, 18, false)

	if width := ansi.StringWidth(got); width != 18 {
		t.Fatalf("pad produced width %d, want 18: %q", width, got)
	}
	if !strings.Contains(got, "prod-vc") {
		t.Fatalf("pad truncated visible text while measuring ANSI styling: %q", got)
	}

	got = truncate(styled, 9)
	if width := ansi.StringWidth(got); width > 9 {
		t.Fatalf("truncate produced width %d, want at most 9: %q", width, got)
	}
	if !strings.Contains(got, "prod-vc") {
		t.Fatalf("truncate removed text that fits in nine terminal cells: %q", got)
	}
}

func TestRowsAreFlattenedForEveryKind(t *testing.T) {
	inv := inventoryFor("prod")
	for _, kind := range vsphere.AllKinds {
		rows := rowsFor(inv, kind, true)
		if len(rows) == 0 {
			t.Errorf("%s produced no rows", kind)
			continue
		}
		cols := columnsFor(kind, true)
		for _, r := range rows {
			if len(r.cells) != len(cols) {
				t.Errorf("%s row %q has %d cells for %d columns", kind, r.name, len(r.cells), len(cols))
			}
			if len(r.detail) == 0 {
				t.Errorf("%s row %q has no detail fields", kind, r.name)
			}
		}
	}
}

// TestSetupFormOpensWithNoContexts is the entry-experience requirement from
// the v0.1.0 plan: a fresh install has nowhere to browse, so the interface
// opens straight into adding the first context instead of an empty table.
func TestSetupFormOpensWithNoContexts(t *testing.T) {
	m := newTestModel(t, &fakeBackend{}, Options{})
	if m.mode != modeForm {
		t.Fatalf("mode is %v, want modeForm", m.mode)
	}
	out := m.View()
	if !strings.Contains(out, "Add a vCenter") || !strings.Contains(out, "first one") {
		t.Errorf("setup form did not open as expected:\n%s", out)
	}
}

// fillNewContextBasics types name, endpoint and username and leaves the
// cursor on the Credential row, which is where every new-context test above
// diverges (prompt vs. keyring, direct vs. socks5, and so on).
func fillNewContextBasics(t *testing.T, m *Model, name, endpoint, username string) {
	t.Helper()
	typeText(t, m, name)
	press(t, m, "down")
	typeText(t, m, endpoint)
	press(t, m, "down")
	typeText(t, m, username)
	press(t, m, "down") // -> Credential
}

func TestNewContextFormSavesAndSelectsIt(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "c", "n")
	settleForm(m)
	if m.mode != modeForm {
		t.Fatalf("'n' on the contexts screen should open the form, mode is %v", m.mode)
	}
	if m.form.editing {
		t.Fatal("'n' should open a blank form, not an edit")
	}

	fillNewContextBasics(t, m, "staging", "https://vcsa.staging.internal", "operator@vsphere.local")
	press(t, m, "right")                                // Credential: keyring -> prompt, skips the password field
	press(t, m, "down", "down", "down", "down", "down") // Route, TLS, Datacenter, Current, Test
	press(t, m, "down")                                 // Save
	press(t, m, "enter")

	if m.mode != modeContexts {
		t.Fatalf("save should return to the contexts screen it was opened from, mode is %v", m.mode)
	}
	if len(b.contexts) != 3 {
		t.Fatalf("backend has %d contexts, want 3", len(b.contexts))
	}
	cur := m.current()
	if cur == nil || cur.cc.Name != "staging" {
		t.Fatalf("selected context is %v, want staging", cur)
	}
	if b.calls["staging"] == 0 {
		t.Error("the new context should have been loaded right after saving")
	}
}

func TestFormBlocksSaveOnFailedTestUntilSaveAnyway(t *testing.T) {
	b := twoHealthy()
	b.testDiag = map[string]*vsphere.Diagnosis{
		"broken": {
			Context: "broken",
			Checks:  []vsphere.Check{{Name: "TCP connection", Status: vsphere.CheckFail, Err: errors.New("connection refused")}},
		},
	}
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "c", "n")
	settleForm(m)
	fillNewContextBasics(t, m, "broken", "https://vcsa.broken.internal", "operator@vsphere.local")
	press(t, m, "right")                                // prompt credential
	press(t, m, "down", "down", "down", "down", "down") // Route, TLS, Datacenter, Current, Test
	press(t, m, "enter")                                // run the test — fails

	if m.mode != modeForm {
		t.Fatalf("a failed test must not close the form, mode is %v", m.mode)
	}
	if !strings.Contains(m.View(), "connection refused") {
		t.Errorf("form should show the failing diagnosis:\n%s", m.View())
	}

	press(t, m, "down") // -> Save
	press(t, m, "enter")
	if m.mode != modeForm {
		t.Fatalf("save should be blocked by the failing test, mode is %v", m.mode)
	}
	if len(b.contexts) != 2 {
		t.Fatalf("nothing should be saved yet, have %d contexts", len(b.contexts))
	}
	if !m.form.forceSave {
		t.Fatal("a failed save should flip the Save row to \"Save anyway\"")
	}

	press(t, m, "enter") // same row, now "Save anyway"
	if m.mode != modeContexts {
		t.Fatalf("save anyway should go through, mode is %v", m.mode)
	}
	if len(b.contexts) != 3 {
		t.Errorf("context should be saved despite the failing test, have %d", len(b.contexts))
	}
}

func TestEditContextPrefillsAndUpdatesInPlace(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "c", "e")
	settleForm(m)
	if m.mode != modeForm {
		t.Fatalf("'e' on the contexts screen should open the form, mode is %v", m.mode)
	}
	if !m.form.editing || m.form.origName != "prod" {
		t.Fatalf("form is not editing prod: editing=%v origName=%q", m.form.editing, m.form.origName)
	}
	if got := m.form.endpoint.Value(); got != "https://vcsa.prod.internal" {
		t.Errorf("endpoint not prefilled: %q", got)
	}

	// Row order while editing: Name(static) Endpoint Username Credential
	// Password Route TLS Datacenter Current Test Save Cancel.
	for range 7 {
		press(t, m, "down") // -> Datacenter
	}
	typeText(t, m, "Lab-DC")
	for range 3 {
		press(t, m, "down") // Current, Test, Save
	}
	press(t, m, "enter")

	if m.mode != modeContexts {
		t.Fatalf("saving an edit should return to the contexts screen, mode is %v", m.mode)
	}
	if len(b.contexts) != 2 {
		t.Fatalf("editing should replace, not add: have %d contexts", len(b.contexts))
	}
	edited, err := findContext(b.contexts, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Datacenter != "Lab-DC" {
		t.Errorf("datacenter is %q, want Lab-DC", edited.Datacenter)
	}
	if edited.Endpoint != "https://vcsa.prod.internal" {
		t.Errorf("editing changed the endpoint to %q", edited.Endpoint)
	}
}

func TestEditingANestedContextKeepsItsProvenance(t *testing.T) {
	b := twoHealthy()
	nested := ctx("nested-app", "https://10.20.0.11")
	nested.Via, nested.ViaMoRef = "prod", "prod-vm-1"
	b.contexts = append(b.contexts, nested)
	m := newTestModel(t, b, Options{Current: "nested-app"})

	press(t, m, "c", "e")
	settleForm(m)
	if m.form == nil || m.form.via != "prod" || m.form.viaMoRef != "prod-vm-1" {
		t.Fatalf("nested form lost its provenance: form=%+v", m.form)
	}
	save := -1
	for i, r := range m.form.rows() {
		if r.kind == rowButton && r.static == "Save" {
			save = i
			break
		}
	}
	if save < 0 {
		t.Fatal("nested context form has no Save button")
	}
	m.form.cursor = save
	m.form.syncFocus()
	press(t, m, "enter")

	saved, err := findContext(b.contexts, "nested-app")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Via != "prod" || saved.ViaMoRef != "prod-vm-1" {
		t.Fatalf("editing dropped provenance: via=%q moref=%q", saved.Via, saved.ViaMoRef)
	}
}

func TestSavingANestedContextReturnsToTheMemberPane(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "7", "enter", "enter", "enter")
	// The member header's actions are SSH, SSH with a different user or key,
	// add-context,
	// open, MoRef, copy.
	press(t, m, "down", "down", "enter")
	settleForm(m)
	if m.form == nil {
		t.Fatal("adding a nested context did not open the form")
	}
	m.form.username.SetValue("administrator@vsphere.local")
	m.form.thumbprint.SetValue("AA:BB:CC")
	save := -1
	for i, r := range m.form.rows() {
		if r.kind == rowButton && r.static == "Save" {
			save = i
			break
		}
	}
	if save < 0 {
		t.Fatal("seeded context form has no Save button")
	}
	m.form.cursor = save
	m.form.syncFocus()
	press(t, m, "enter")

	if m.mode != modeVAppVMDetail || m.vapp == nil || m.vappVM == nil {
		t.Fatalf("saving should return to the member pane, mode=%v vapp=%+v member=%+v", m.mode, m.vapp, m.vappVM)
	}
	saved, err := findContext(b.contexts, "app-01")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Via != "prod" || saved.ViaMoRef != "prod-vm-1" {
		t.Fatalf("saved nested context provenance is via=%q moref=%q", saved.Via, saved.ViaMoRef)
	}
}

func findContext(contexts []*config.Context, name string) (*config.Context, error) {
	for _, cc := range contexts {
		if cc.Name == name {
			return cc, nil
		}
	}
	return nil, fmt.Errorf("context %q not found among %d", name, len(contexts))
}

func TestDeleteContextConfirmationRemovesIt(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "c", "x")
	if m.mode != modeConfirmDelete {
		t.Fatalf("'x' should open the delete confirmation, mode is %v", m.mode)
	}
	if !strings.Contains(m.View(), "Delete prod?") {
		t.Errorf("confirmation should name the context:\n%s", m.View())
	}

	press(t, m, "y")
	if m.mode != modeContexts {
		t.Fatalf("confirming should return to the contexts screen, mode is %v", m.mode)
	}
	if len(b.contexts) != 1 || b.contexts[0].Name != "customer-a" {
		t.Fatalf("prod should have been removed, contexts: %v", b.contexts)
	}
}

func TestDeleteConfirmationCancels(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "c", "x", "n")
	if m.mode != modeContexts {
		t.Fatalf("'n' should cancel back to the contexts screen, mode is %v", m.mode)
	}
	if len(b.contexts) != 2 {
		t.Errorf("nothing should have been removed, have %d contexts", len(b.contexts))
	}
}

// TestDeleteLastContextReopensSetup is the same principle as the empty-config
// start-up: a screen with nothing to show and no way back in is never the
// resting state.
func TestDeleteLastContextReopensSetup(t *testing.T) {
	b := &fakeBackend{contexts: []*config.Context{ctx("only", "https://vcsa.only.internal")}}
	m := newTestModel(t, b, Options{Current: "only"})

	press(t, m, "c", "x", "y")
	if m.mode != modeForm {
		t.Fatalf("deleting the last context should reopen setup, mode is %v", m.mode)
	}
	if len(b.contexts) != 0 {
		t.Errorf("context should have been removed, have %d", len(b.contexts))
	}
}

func TestDiscoverThumbprintFillsTheField(t *testing.T) {
	b := twoHealthy()
	b.discoverThumb = "11:22:33:44"
	b.discoverSubj = "vcsa.staging.internal"
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "c", "n")
	settleForm(m)
	fillNewContextBasics(t, m, "staging", "https://vcsa.staging.internal", "operator@vsphere.local")
	press(t, m, "down", "down", "down") // Password, Route, TLS
	press(t, m, "right")                // TLS: system -> thumbprint
	press(t, m, "down", "down")         // Thumbprint, Discover
	press(t, m, "enter")

	if got := m.form.thumbprint.Value(); got != "11:22:33:44" {
		t.Fatalf("thumbprint field is %q, want the discovered value", got)
	}
	if !strings.Contains(m.View(), "Discovered vcsa.staging.internal") {
		t.Errorf("form should report what was discovered:\n%s", m.View())
	}
}

func TestFormEscapeCancelsWithoutQuitting(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "c", "n")
	settleForm(m)
	typeText(t, m, "this contains the letters q and colon: q")
	press(t, m, "esc")

	if m.mode != modeContexts {
		t.Fatalf("esc should cancel the form back to the contexts screen, mode is %v", m.mode)
	}
	if m.quitting {
		t.Error("typing 'q' into a field must not quit the program")
	}
	if len(b.contexts) != 2 {
		t.Errorf("cancelling must not save anything, have %d contexts", len(b.contexts))
	}
}

func TestSortBringsTroubleToTheTop(t *testing.T) {
	inv := inventoryFor("prod")
	// app-01 sorts first alphabetically. Giving build-runner-3 the worse
	// status is what makes name order and status order actually disagree,
	// so the test tells them apart rather than passing by coincidence.
	inv.VMs[1].PowerState = "suspended"
	b := &fakeBackend{
		contexts:    []*config.Context{ctx("prod", "https://vcsa.prod.internal")},
		inventories: map[string]*vsphere.Inventory{"prod": inv},
	}
	m := newTestModel(t, b, Options{Current: "prod"})

	rows := m.rows()
	if rows[0].name != "app-01" {
		t.Fatalf("name order should put app-01 first, got %q", rows[0].name)
	}

	press(t, m, "s")
	if m.sortMode != sortByStatus {
		t.Fatalf("'s' should switch to status order, sortMode is %v", m.sortMode)
	}
	rows = m.rows()
	if rows[0].name != "build-runner-3" {
		t.Errorf("status order should put the suspended VM first, got %q", rows[0].name)
	}
	if !strings.Contains(m.View(), "sort: status") {
		t.Errorf("the active sort should be visible in the header:\n%s", m.View())
	}

	press(t, m, "s")
	if m.sortMode != sortByName {
		t.Fatalf("'s' should cycle back to name order, sortMode is %v", m.sortMode)
	}
}

func TestSnapshotReportsCurrentPosition(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "right") // Templates tab
	press(t, m, "s")     // status sort

	snap := m.Snapshot()
	if snap.Context != "prod" {
		t.Errorf("snapshot context is %q, want prod", snap.Context)
	}
	if snap.Kind != "template" {
		t.Errorf("snapshot kind is %q, want template", snap.Kind)
	}
	if snap.Sort != "status" {
		t.Errorf("snapshot sort is %q, want status", snap.Sort)
	}
}

func TestOptionsSeedTheStartingPosition(t *testing.T) {
	b := twoHealthy()
	m := New(context.Background(), b, Options{Current: "customer-a", Kind: "host", Sort: "status"})

	if got := m.current(); got == nil || got.cc.Name != "customer-a" {
		t.Errorf("starting context is %v, want customer-a", got)
	}
	if m.kind != vsphere.KindHost {
		t.Errorf("starting kind is %v, want host", m.kind)
	}
	if m.sortMode != sortByStatus {
		t.Errorf("starting sort is %v, want status", m.sortMode)
	}
}

func hasLabel(rows []formRow, label string) bool {
	for _, r := range rows {
		if r.label == label {
			return true
		}
	}
	return false
}

// TestFormRouteRowsMatchEachProxyType checks the row generation directly,
// independent of the keypresses needed to reach each route: direct shows no
// proxy fields, every proxy type shows an address, only socks5 shows the
// remote-DNS toggle (http and https always resolve at the proxy, so there is
// nothing to choose), and the password field only appears once a proxy
// username has actually been typed.
func TestFormRouteRowsMatchEachProxyType(t *testing.T) {
	f := newContextForm(nil)

	if hasLabel(f.rows(), "Proxy address") {
		t.Error("direct route should not show proxy fields")
	}

	f.transportIdx = 1 // socks5
	if rows := f.rows(); !hasLabel(rows, "Proxy address") || !hasLabel(rows, "Resolve DNS at the proxy") {
		t.Error("socks5 should show a proxy address and the remote-DNS toggle")
	}

	f.transportIdx = 2 // http
	if rows := f.rows(); !hasLabel(rows, "Proxy address") {
		t.Error("http should show a proxy address field")
	} else if hasLabel(rows, "Resolve DNS at the proxy") {
		t.Error("http has no remote-DNS choice — it always resolves at the proxy")
	}

	f.transportIdx = 3 // https
	if rows := f.rows(); !hasLabel(rows, "Proxy address") {
		t.Error("https should show a proxy address field")
	} else if hasLabel(rows, "Resolve DNS at the proxy") {
		t.Error("https has no remote-DNS choice either")
	} else if hasLabel(rows, "Proxy password") {
		t.Error("the password field should not appear before a proxy username is typed")
	}

	f.proxyUser.SetValue("svc-proxy")
	if !hasLabel(f.rows(), "Proxy password") {
		t.Error("a non-empty proxy username should reveal the password field")
	}
}

func TestNewContextFormSavesAnHTTPSProxyRoute(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})

	press(t, m, "c", "n")
	settleForm(m)
	fillNewContextBasics(t, m, "secure-proxy", "https://vcsa.secure.internal", "operator@vsphere.local")
	press(t, m, "down")                    // -> Password (leave blank)
	press(t, m, "down")                    // -> Route
	press(t, m, "right", "right", "right") // direct -> socks5 -> http -> https
	if m.form.transportIdx != 3 {
		t.Fatalf("transportIdx is %d, want 3 (https)", m.form.transportIdx)
	}
	press(t, m, "down") // -> Proxy address
	typeText(t, m, "proxy.example.internal:3128")
	press(t, m, "down") // -> Proxy username
	typeText(t, m, "svc-proxy")
	press(t, m, "down") // -> Proxy password (now visible)
	typeText(t, m, "s3cret")
	press(t, m, "down", "down", "down", "down", "down") // TLS, Datacenter, Current, Test, Save
	press(t, m, "enter")

	if m.mode != modeContexts {
		t.Fatalf("save should return to the contexts screen, mode is %v", m.mode)
	}
	saved, err := findContext(b.contexts, "secure-proxy")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Transport.Type != config.TransportHTTPSProxy {
		t.Errorf("route type is %q, want https", saved.Transport.Type)
	}
	if saved.Transport.Address != "proxy.example.internal:3128" {
		t.Errorf("proxy address is %q", saved.Transport.Address)
	}
	if saved.Transport.Username != "svc-proxy" {
		t.Errorf("proxy username is %q, want svc-proxy", saved.Transport.Username)
	}
}

func TestEditContextPrefillsAnHTTPProxyRoute(t *testing.T) {
	b := &fakeBackend{contexts: []*config.Context{func() *config.Context {
		cc := ctx("via-proxy", "https://vcsa.via-proxy.internal")
		cc.Transport = config.TransportConfig{Type: config.TransportHTTPProxy, Address: "10.0.0.1:8080", Username: "svc"}
		return cc
	}()}}
	m := newTestModel(t, b, Options{Current: "via-proxy"})

	press(t, m, "c", "e")
	settleForm(m)
	if m.form.transportIdx != 2 {
		t.Fatalf("transportIdx is %d, want 2 (http)", m.form.transportIdx)
	}
	if got := m.form.proxyAddr.Value(); got != "10.0.0.1:8080" {
		t.Errorf("proxy address not prefilled: %q", got)
	}
	if got := m.form.proxyUser.Value(); got != "svc" {
		t.Errorf("proxy username not prefilled: %q", got)
	}
	if hasLabel(m.form.rows(), "Resolve DNS at the proxy") {
		t.Error("http should not show the remote-DNS toggle even when editing")
	}
}

// TestScheduleRefreshOnlyArmsWhenEnabled checks the switch itself, without
// waiting on a timer: a positive interval produces a command, anything else
// produces none at all.
func TestScheduleRefreshOnlyArmsWhenEnabled(t *testing.T) {
	if scheduleRefresh(time.Minute) == nil {
		t.Error("a positive interval should arm a refresh")
	}
	for _, d := range []time.Duration{0, -time.Second} {
		if scheduleRefresh(d) != nil {
			t.Errorf("scheduleRefresh(%v) armed a refresh; it should not", d)
		}
	}
}

func TestRefreshIntervalResolution(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want time.Duration
	}{
		{0, DefaultRefreshInterval},
		{30 * time.Second, 30 * time.Second},
		{-1, 0},
		{-time.Hour, 0},
	} {
		if got := refreshInterval(tc.in); got != tc.want {
			t.Errorf("refreshInterval(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestBackgroundRefreshTiersByWhatIsOnScreen covers the split that makes the
// refresh affordable: the vCenter being looked at is held to the configured
// interval, everything else to backgroundRefreshFactor times it.
func TestBackgroundRefreshTiersByWhatIsOnScreen(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})
	// Enabled after construction: Init would otherwise arm a real timer that
	// this synchronous harness would sit and wait on.
	m.refreshInterval = time.Minute
	// customer-a is off screen, so Init (issue #27) never touched it; this
	// test is about the tiered rate once a context has data, not about
	// start-up itself, so give it an initial load the way visiting it once
	// would — the same as a completed background refresh.
	drive(t, m, m.startLoad(m.byName["customer-a"], false))
	if b.calls["prod"] != 1 || b.calls["customer-a"] != 1 {
		t.Fatalf("initial load calls = prod:%d customer-a:%d, want 1 each", b.calls["prod"], b.calls["customer-a"])
	}

	// Nothing is due a moment after loading, so a tick now is a no-op — that
	// is what keeps a manual reload from being immediately repeated.
	tickRefresh(t, m)
	if b.calls["prod"] != 1 {
		t.Errorf("a tick straight after loading re-read prod; calls = %d", b.calls["prod"])
	}

	// Two minutes: past the on-screen threshold, nowhere near the off-screen
	// one. Only the vCenter in scope should be re-read.
	for _, st := range m.states {
		st.loadedAt = time.Now().Add(-2 * time.Minute)
	}
	tickRefresh(t, m)
	if b.calls["prod"] != 2 {
		t.Errorf("prod calls = %d, want 2 — it is on screen and past its interval", b.calls["prod"])
	}
	if b.calls["customer-a"] != 1 {
		t.Errorf("customer-a calls = %d, want 1 — off screen it is held to a slower rate", b.calls["customer-a"])
	}

	// Past the off-screen threshold too, it is re-read as well: the header
	// count and estate-wide search still have to be roughly right.
	for _, st := range m.states {
		st.loadedAt = time.Now().Add(-30 * time.Minute)
	}
	tickRefresh(t, m)
	if b.calls["customer-a"] != 2 {
		t.Errorf("customer-a calls = %d, want 2 — past %dx the interval it is due", b.calls["customer-a"], backgroundRefreshFactor)
	}
}

// TestAllScopeHoldsEveryContextToTheOnScreenRate checks that asking to watch
// the whole estate at once really does keep all of it current: in that view
// every context is on screen, so every context gets the fast interval.
func TestAllScopeHoldsEveryContextToTheOnScreenRate(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod", AllContexts: true})
	m.refreshInterval = time.Minute

	for _, st := range m.states {
		st.loadedAt = time.Now().Add(-2 * time.Minute)
	}
	tickRefresh(t, m)
	if b.calls["prod"] != 2 {
		t.Errorf("prod calls = %d, want 2 — it is the selected context and is stale", b.calls["prod"])
	}
	if b.calls["customer-a"] != 0 {
		t.Errorf("customer-a calls = %d, want 0 — a timer must not select an untouched context", b.calls["customer-a"])
	}
}

// TestSwitchingScopeRereadsANewlyVisibleContext checks that arriving at a
// vCenter shows its current state rather than whatever the slower off-screen
// rate last left behind.
func TestSwitchingScopeRereadsANewlyVisibleContext(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})
	m.refreshInterval = time.Minute

	// customer-a is off screen, so Init (issue #27) never loaded it. Give it
	// one load — a prior visit, or a completed background refresh — so there
	// is something for this test to leave stale before switching to it.
	drive(t, m, m.startLoad(m.byName["customer-a"], false))

	// customer-a has been sitting off screen long enough to be stale for a
	// reader, but not long enough for the background rate to have acted.
	for _, st := range m.states {
		st.loadedAt = time.Now().Add(-2 * time.Minute)
	}
	if b.calls["customer-a"] != 1 {
		t.Fatalf("customer-a calls = %d, want 1 before switching", b.calls["customer-a"])
	}

	// Switch scope to it, the way "c" then enter does.
	m.ctxCursor = 0
	for i, st := range m.states {
		if st.cc.Name == "customer-a" {
			m.ctxCursor = i
		}
	}
	drive(t, m, m.useContext())

	if b.calls["customer-a"] != 1 {
		t.Errorf("customer-a calls after switching to it = %d, want 1 — a valid in-process session is reused", b.calls["customer-a"])
	}
}

// TestBackgroundRefreshIsQuiet checks that keeping the table current is not
// something the operator has to watch happen: no pending glyph, no spinner,
// and the message line left alone.
func TestBackgroundRefreshIsQuiet(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})
	// Enabled after construction: Init would otherwise arm a real one-minute
	// timer that this synchronous harness would sit and wait on.
	m.refreshInterval = time.Minute
	st := m.byName["prod"]

	m.setMessage("something the operator was reading", false)
	for _, s := range m.states {
		s.loadedAt = time.Now().Add(-2 * time.Minute)
	}

	// Mid-flight: the read is in progress but nothing advertises it.
	cmds := m.refreshStale()
	if !st.loading {
		t.Fatal("a due context did not start loading")
	}
	if st.showsLoading() {
		t.Error("a background refresh is advertising itself as loading")
	}
	if st.rowStatus() != statusGood {
		t.Errorf("status during a background refresh = %v, want good — the data on screen is still good", st.rowStatus())
	}
	if m.pendingScope() || m.busy() {
		t.Error("a background refresh put the header into a loading state")
	}

	for _, cmd := range cmds {
		drive(t, m, cmd)
	}
	if m.message != "something the operator was reading" {
		t.Errorf("a successful background refresh overwrote the message line with %q", m.message)
	}
	if st.quiet {
		t.Error("quiet was left set after the read landed")
	}
}

// TestBackgroundRefreshFailureIsReported is the other half of quiet: silence
// while it works, but never silence about it having stopped working. Serving
// data that is no longer being updated without saying so is the failure mode
// this whole feature exists to avoid.
func TestBackgroundRefreshFailureIsReported(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})
	// Enabled after construction: Init would otherwise arm a real one-minute
	// timer that this synchronous harness would sit and wait on.
	m.refreshInterval = time.Minute
	st := m.byName["prod"]
	rowsBefore := len(m.rows())

	if b.failures == nil {
		b.failures = map[string]error{}
	}
	b.failures["prod"] = errors.New("connection reset")
	for _, s := range m.states {
		s.loadedAt = time.Now().Add(-2 * time.Minute)
	}
	tickRefresh(t, m)

	if st.err == nil {
		t.Fatal("a failed background refresh recorded no error")
	}
	if !m.messageBad || !strings.Contains(m.message, "refresh failed") {
		t.Errorf("a failed background refresh said nothing: bad=%v %q", m.messageBad, m.message)
	}
	if got := len(m.rows()); got != rowsBefore {
		t.Errorf("rows after a failed background refresh = %d, want the stale %d kept", got, rowsBefore)
	}
	if st.rowStatus() != statusWarn {
		t.Errorf("status = %v, want warn — stale data present but the last read failed", st.rowStatus())
	}
}

// TestBackgroundRefreshRetriesAContextThatNeverLoaded checks the recovery
// path: a vCenter every attempt has failed against comes back on its own
// rather than waiting for someone to press a key.
func TestBackgroundRefreshRetriesAContextThatNeverLoaded(t *testing.T) {
	b := twoHealthy()
	b.failures = map[string]error{"customer-a": errors.New("no route to host")}
	m := newTestModel(t, b, Options{Current: "prod"})
	// Enabled after construction: Init would otherwise arm a real one-minute
	// timer that this synchronous harness would sit and wait on.
	m.refreshInterval = time.Minute

	// customer-a is off screen, so Init (issue #27) never attempted it. Give
	// it the first, failing attempt explicitly — a prior visit — which is
	// what a background refresh is retrying from here on.
	drive(t, m, m.startLoad(m.byName["customer-a"], false))

	st := m.byName["customer-a"]
	if st.err == nil || st.inv != nil {
		t.Fatalf("customer-a should have failed outright, got err=%v inv=%v", st.err, st.inv)
	}
	if !m.refreshDue(st, false) {
		t.Error("a context that never loaded is not due a retry; it always should be")
	}

	delete(b.failures, "customer-a")
	tickRefresh(t, m)

	if st.err != nil {
		t.Errorf("customer-a did not recover on its own: %v", st.err)
	}
	if st.inv == nil {
		t.Error("customer-a recovered without an inventory")
	}
}

// TestBackgroundRefreshDisabledDoesNothing checks that --refresh with a
// negative value really is off, not merely slower.
func TestBackgroundRefreshDisabledDoesNothing(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod", RefreshInterval: -1})
	for _, st := range m.states {
		st.loadedAt = time.Now().Add(-24 * time.Hour)
	}
	tickRefresh(t, m)
	if b.calls["prod"] != 1 {
		t.Errorf("prod calls = %d, want 1 — refresh is disabled", b.calls["prod"])
	}
}

// TestDemoBadgeMarksSampleData covers the one thing standing between a demo
// screenshot and a reader who thinks they are looking at a real estate.
func TestDemoBadgeMarksSampleData(t *testing.T) {
	withBadge := newTestModel(t, twoHealthy(), Options{Current: "prod", Demo: true})
	if header := ansi.Strip(withBadge.viewHeader()); !strings.Contains(header, demoBadge) {
		t.Errorf("demo header should carry %q, got %q", demoBadge, header)
	}

	// The scope and connection summary must survive beside it.
	header := ansi.Strip(withBadge.viewHeader())
	if !strings.Contains(header, "prod") || !strings.Contains(header, "connected") {
		t.Errorf("demo header dropped the scope or summary: %q", header)
	}

	plain := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	if header := ansi.Strip(plain.viewHeader()); strings.Contains(header, demoBadge) {
		t.Errorf("a real run must not claim to be a demo, got %q", header)
	}
}

// The form's credential row is a two-way choice between keyring and prompt, so
// a context resolved from an environment variable, a file or a helper has no
// index to sit on. It must be preserved rather than quietly rewritten to
// keyring:<name>, which would drop the reference and leave the context looking
// for a password nothing ever stored.
func TestFormPreservesANonInteractiveCredential(t *testing.T) {
	cc := &config.Context{
		Name:       "prod",
		Endpoint:   "https://vcsa.example.internal",
		Username:   "operator@vsphere.local",
		Credential: credentials.Ref{Scheme: credentials.SchemeEnv, Value: "VSFLEET_PROD_PASSWORD"},
	}
	f := newContextForm(&contextState{cc: cc})

	// Shown, not offered: no scheme picker and no password field, because
	// neither applies to a reference the form cannot edit.
	rows := f.rows()
	if !hasLabel(rows, "Credential") {
		t.Fatal("the credential row disappeared")
	}
	if hasLabel(rows, "Password") {
		t.Error("a read-only credential source has no password to type into the form")
	}

	in := f.input()
	if in.Credential != cc.Credential {
		t.Errorf("the form rewrote the credential to %v, want %v", in.Credential, cc.Credential)
	}
	if in.HavePassword {
		t.Error("the form invented a password for a reference that cannot store one")
	}
}

// The keyring and prompt paths are unchanged by that.
func TestFormStillChoosesBetweenKeyringAndPrompt(t *testing.T) {
	f := newContextForm(nil)
	f.name.SetValue("prod")

	if !hasLabel(f.rows(), "Password") {
		t.Error("the keyring choice should offer a password field")
	}
	if in := f.input(); in.Credential.Scheme != credentials.SchemeKeyring || in.Credential.Value != "prod" {
		t.Errorf("keyring choice produced %v", in.Credential)
	}

	f.credIdx = 1
	if hasLabel(f.rows(), "Password") {
		t.Error("the prompt choice stores nothing, so it has no password field")
	}
	if in := f.input(); in.Credential.Scheme != credentials.SchemePrompt {
		t.Errorf("prompt choice produced %v", in.Credential)
	}
}

func TestDemoModeContextsScreenDisablesMutations(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod", Demo: true})

	press(t, m, "c")
	if m.mode != modeContexts {
		t.Fatalf("expected modeContexts, got %v", m.mode)
	}

	// Footer hints must not advertise runnable mutation keybinds in demo mode.
	for _, hint := range m.keys.footerHints(m) {
		switch hint.Help().Key {
		case "n", "e", "x":
			t.Errorf("footer hints advertised mutation key %q in demo mode", hint.Help().Key)
		}
	}

	// Help panel must not advertise mutation keybinds in demo mode.
	for _, sec := range m.keys.helpSections(m.demo) {
		if sec.title == "Contexts screen (c)" {
			for _, b := range sec.bindings {
				switch b.Help().Key {
				case "n", "e", "x":
					t.Errorf("help section advertised mutation key %q in demo mode", b.Help().Key)
				}
			}
		}
	}

	// Direct keypresses cannot enter context add/edit/delete in demo mode.
	press(t, m, "n")
	if m.mode != modeContexts || m.form != nil {
		t.Fatalf("'n' in demo mode entered form or changed mode: mode=%v form=%v", m.mode, m.form)
	}

	press(t, m, "e")
	if m.mode != modeContexts || m.form != nil {
		t.Fatalf("'e' in demo mode entered form or changed mode: mode=%v form=%v", m.mode, m.form)
	}

	press(t, m, "x")
	if m.mode != modeContexts || m.confirmDelete != nil {
		t.Fatalf("'x' in demo mode entered delete confirmation or changed mode: mode=%v confirmDelete=%v", m.mode, m.confirmDelete)
	}

	// Defensive model methods also refuse mutation entry in demo mode.
	if cmd := m.enterForm(nil); cmd != nil || m.mode != modeContexts || m.form != nil {
		t.Errorf("enterForm(nil) should be a no-op in demo mode, got mode=%v form=%v", m.mode, m.form)
	}
	if cmd := m.enterFormSeeded(contextSeed{}); cmd != nil || m.mode != modeContexts || m.form != nil {
		t.Errorf("enterFormSeeded should be a no-op in demo mode, got mode=%v form=%v", m.mode, m.form)
	}
}

func TestDemoModeEmptyContextsViewDoesNotAdvertiseNewContext(t *testing.T) {
	m := newTestModel(t, &fakeBackend{}, Options{Demo: true})
	press(t, m, "c")
	if m.mode != modeContexts {
		t.Fatalf("expected modeContexts, got %v", m.mode)
	}
	view := m.View()
	if strings.Contains(view, "n adds the first one") {
		t.Errorf("empty demo contexts screen advertised 'n adds the first one':\n%s", view)
	}
	if !strings.Contains(view, "No vCenters configured.") {
		t.Errorf("empty demo contexts screen did not display expected message:\n%s", view)
	}
}

func TestHostCapacityColumnsFitAtNarrowWidths(t *testing.T) {
	cols := columnsFor(vsphere.KindHost, false)
	for _, total := range []int{80, 100, 140} {
		widths := layoutColumns(cols, total)
		for i, c := range cols {
			switch c.title {
			case "CPU":
				if widths[i] < len("12.4GHz/102.4GHz") {
					t.Errorf("width %d: CPU column is %d wide", total, widths[i])
				}
			case "MEMORY":
				if widths[i] < len("512.0G/1.0T") {
					t.Errorf("width %d: MEMORY column is %d wide", total, widths[i])
				}
			}
		}
	}
}
