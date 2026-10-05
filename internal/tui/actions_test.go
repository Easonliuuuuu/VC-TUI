package tui

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/uistate"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// fakeHandoff records what the field-cursor actions asked of the operator's
// workstation instead of touching it — the same reason fakeBackend stands in
// for a real vCenter connection. Its SSH never actually runs anything: see
// the comment on sshCmd in commands.go for why tea.ExecProcess makes that
// safe to call from this synchronous test harness in the first place.
type fakeHandoff struct {
	copied []string
	opened []string
	ssh    []SSHSpec
	// resolved scripts ResolveUser by address; resolveCalls records the
	// addresses it was asked about.
	resolved     map[string]string
	resolveCalls []string
	copyErr      error
	openErr      error
	sshErr       error
	identities   []SSHIdentity
	identityErr  error
	// destinations scripts DiscoverSSHDestinations by targetIP; destCalls
	// records the (targetIP, remembered) pairs it was asked about.
	destinations     map[string][]string
	destStale        map[string]bool
	destErr          error
	destCalls        []string
	destRememberedIn []string
}

func (f *fakeHandoff) Copy(v string) error {
	f.copied = append(f.copied, v)
	return f.copyErr
}

func (f *fakeHandoff) OpenURL(u string) error {
	f.opened = append(f.opened, u)
	return f.openErr
}

func (f *fakeHandoff) ResolveUser(address string) string {
	f.resolveCalls = append(f.resolveCalls, address)
	return f.resolved[address]
}
func (f *fakeHandoff) DiscoverSSHIdentities(address string) ([]SSHIdentity, error) {
	return append([]SSHIdentity(nil), f.identities...), f.identityErr
}
func (f *fakeHandoff) DiscoverSSHDestinations(targetIP, remembered string) ([]string, bool, error) {
	f.destCalls = append(f.destCalls, targetIP)
	f.destRememberedIn = append(f.destRememberedIn, remembered)
	return append([]string(nil), f.destinations[targetIP]...), f.destStale[targetIP], f.destErr
}
func (f *fakeHandoff) SSH(spec SSHSpec) (*exec.Cmd, error) {
	f.ssh = append(f.ssh, spec)
	if f.sshErr != nil {
		return nil, f.sshErr
	}
	return exec.Command("true"), nil
}

// proxiedCtx builds a context routed through an unauthenticated SOCKS5 proxy
// — the case #84 did not account for: the browser cannot follow the route
// vsfleet itself uses, but ssh(1) can be told to.
func proxiedCtx(name, endpoint, proxyAddr string) *config.Context {
	cc := ctx(name, endpoint)
	cc.Transport = config.TransportConfig{Type: config.TransportSOCKS5, Address: proxyAddr}
	return cc
}

// findRow switches to kind and returns the named row, ignoring any jump
// constraint left over from an earlier action in the same test.
func findRow(t *testing.T, m *Model, kind vsphere.Kind, name string) row {
	t.Helper()
	m.kind = kind
	m.jump = nil
	for _, r := range m.rows() {
		if r.name == name {
			return r
		}
	}
	t.Fatalf("row %q of kind %v not found", name, kind)
	return row{}
}

// asVCenters gives every VM named vm in b's inventories a guest DNS name that
// reads as a vCenter appliance, the evidence that makes "Add as a vCenter
// context" apply to it. The guest name is offered as an SSH target first, so
// anything that needs a fixed action position should look the action up by
// label.
func asVCenters(b *fakeBackend, vm string) {
	for _, inv := range b.inventories {
		for i := range inv.VMs {
			if inv.VMs[i].Name == vm {
				inv.VMs[i].GuestHostName = "vcsa-" + vm + ".lab.internal"
			}
		}
	}
}

// selectAction moves the open popup's cursor onto the action labelled label.
func selectAction(t *testing.T, m *Model, label string) {
	t.Helper()
	if m.actions == nil {
		t.Fatal("no action popup is open")
	}
	for i, a := range m.actions.items {
		if a.label == label {
			m.actions.cursor = i
			return
		}
	}
	t.Fatalf("popup has no action %q: %v", label, labels(m.actions.items))
}

func findAction(items []action, label string) (action, bool) {
	for _, a := range items {
		if a.label == label {
			return a, true
		}
	}
	return action{}, false
}

// TestDetailCursorSkipsDashFields checks that Up/Down never stop on a field
// humanize.Dash blanked out — build-runner-3 has no guest IP, no tools
// state, and no guest state, and none of those should ever be reachable by
// the field cursor, only by paging past them.
func TestDetailCursorSkipsDashFields(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "down", "enter") // app-01, then build-runner-3
	r, ok := m.currentRow()
	if !ok || r.name != "build-runner-3" {
		t.Fatalf("expected build-runner-3 detail, got %+v ok=%v", r, ok)
	}
	focusable := detailFocusable(r)
	sawIPAddress := false
	for i := 0; i < len(focusable)+3; i++ {
		if !focusable[m.detailCursor] {
			t.Fatalf("cursor landed on non-focusable line %d after %d presses", m.detailCursor, i)
		}
		if m.detailCursor >= 2 && r.detail[m.detailCursor-2].label == "IP address" {
			sawIPAddress = true
		}
		press(t, m, "down")
	}
	if sawIPAddress {
		t.Fatalf("cursor should never stop on build-runner-3's blank IP address field")
	}
}

// TestManagedObjectFieldRunsSingleActionImmediately checks the "one action
// runs, two or more open a list" rule: the Managed object field offers only
// Copy MoRef, so Enter there must copy without opening a popup.
func TestManagedObjectFieldRunsSingleActionImmediately(t *testing.T) {
	fake := &fakeHandoff{}
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Handoff: fake})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	idx := -1
	for i, f := range r.detail {
		if f.label == "Managed object" {
			idx = 2 + i
		}
	}
	if idx < 0 {
		t.Fatal("fixture VM has no Managed object field")
	}
	press(t, m, "enter") // open the detail pane
	m.detailCursor = idx
	press(t, m, "enter")
	if m.actions != nil {
		t.Fatalf("a field with exactly one action must not open a popup, got %+v", m.actions)
	}
	if len(fake.copied) != 1 || fake.copied[0] != r.target.moref {
		t.Fatalf("expected the MoRef %q copied once, got %v", r.target.moref, fake.copied)
	}
}

// TestIPFieldOpensPopupWithSSHActions checks the other half of that rule:
// a VM's IP address offers SSH, SSH as another user, a copyable ssh command,
// and Copy value, so Enter there must open a popup rather than guessing which
// one is meant.
func TestIPFieldOpensPopupWithSSHActions(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	idx := -1
	for i, f := range r.detail {
		if f.label == "IP address" {
			idx = 2 + i
		}
	}
	press(t, m, "enter")
	m.detailCursor = idx
	press(t, m, "enter")
	if m.actions == nil {
		t.Fatal("expected the IP address field to open a popup")
	}
	wantLabels := []string{"SSH to " + r.target.address, "SSH with a different destination, user or key…", "Copy ssh -o ConnectTimeout=15 " + r.target.address, "Copy value"}
	for _, want := range wantLabels {
		if _, ok := findAction(m.actions.items, want); !ok {
			var got []string
			for _, a := range m.actions.items {
				got = append(got, a.label)
			}
			t.Errorf("missing action %q, got %v", want, got)
		}
	}
}

// TestJumpFromHostShowsOnlyItsVMs exercises the cross-resource jump: from
// the host's own header, "Show VMs on this host" must narrow the VM table to
// exactly the VMs whose Host field names it — vm.Host does not contain the
// host's own name as a substring, so the plain text filter could never do
// this on its own.
func TestJumpFromHostShowsOnlyItsVMs(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "3", "enter") // Hosts tab, the fixture's one host: esxi-01
	r, ok := m.currentRow()
	if !ok || r.kind != vsphere.KindHost || r.name != "esxi-01" {
		t.Fatalf("expected esxi-01 host detail, got %+v ok=%v", r, ok)
	}
	press(t, m, "enter") // object header's action list
	if m.actions == nil {
		t.Fatal("expected the host header to open a popup")
	}
	idx := -1
	for i, a := range m.actions.items {
		if a.label == "Show VMs on this host" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no jump action offered on the host header: %+v", m.actions.items)
	}
	m.actions.cursor = idx
	press(t, m, "enter")

	if m.jump == nil || m.kind != vsphere.KindVM {
		t.Fatalf("expected a VM jump after running the action, got jump=%+v kind=%v", m.jump, m.kind)
	}
	names := map[string]bool{}
	for _, row := range m.rows() {
		names[row.name] = true
	}
	if !names["app-01"] || names["build-runner-3"] {
		t.Fatalf("jump should show only the VMs on esxi-01 (app-01), got %v", names)
	}

	// Esc clears the jump before it clears anything else.
	press(t, m, "esc")
	if m.jump != nil {
		t.Fatalf("esc should have cleared the jump")
	}
	names = map[string]bool{}
	for _, row := range m.rows() {
		names[row.name] = true
	}
	if !names["build-runner-3"] {
		t.Fatalf("clearing the jump should restore build-runner-3 to the VM table, got %v", names)
	}
}

// TestDemoDisablesExternalActions checks that "vsfleet demo dials nothing"
// still holds for the detail pane: SSH and the browser links must be listed
// and disabled, never silently omitted or, worse, actually launched.
func TestDemoDisablesExternalActions(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Demo: true})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	items := m.actionsFor(r, 0)

	ssh, ok := findAction(items, "SSH to "+r.target.address)
	if !ok || ssh.disabled != demoDisabledReason {
		t.Errorf("expected SSH disabled with %q in demo, got %+v", demoDisabledReason, ssh)
	}
	open, ok := findAction(items, "Open in vSphere Client")
	if !ok || open.disabled != demoDisabledReason {
		t.Errorf("expected the browser link disabled with %q in demo, got %+v", demoDisabledReason, open)
	}
	copyMoref, ok := findAction(items, "Copy MoRef")
	if !ok || copyMoref.disabled != "" {
		t.Errorf("copying is not an external command and must stay enabled in demo, got %+v", copyMoref)
	}
}

// TestProxiedContextDisablesBrowserActionsButNotSSH checks the case #84's
// original design did not account for: a proxied vCenter has no route for
// the operator's own browser, but ssh(1) can still be told to follow the
// same proxy, so only the browser links should decline.
func TestProxiedContextDisablesBrowserActionsButNotSSH(t *testing.T) {
	name := "osaka"
	b := &fakeBackend{
		contexts:    []*config.Context{proxiedCtx(name, "https://vcsa.osaka.internal", "127.0.0.1:1080")},
		inventories: map[string]*vsphere.Inventory{name: inventoryFor(name)},
		instanceIDs: map[string]string{name: "52-aaaa-bbbb"},
	}
	m := newTestModel(t, b, Options{Current: name})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	items := m.actionsFor(r, 0)

	open, ok := findAction(items, "Open in vSphere Client")
	if !ok || !strings.Contains(open.disabled, "proxied") {
		t.Errorf("expected the browser link disabled for a proxied context, got %+v", open)
	}
	ssh, ok := findAction(items, "SSH to "+r.target.address)
	if !ok || ssh.disabled != "" {
		t.Errorf("SSH should still be offered through the proxy, got %+v", ssh)
	}
}

func TestSSHUsesPerKindUsers(t *testing.T) {
	fake := &fakeHandoff{}
	m := newTestModel(t, twoHealthy(), Options{
		Current:     "prod",
		Handoff:     fake,
		SSHUser:     "legacy",
		SSHVMUser:   "ubuntu",
		SSHHostUser: "root",
	})
	vm := findRow(t, m, vsphere.KindVM, "app-01")
	host := findRow(t, m, vsphere.KindHost, "esxi-01")
	if cmd := m.sshAction(vm, vm.target.address).run(m); cmd == nil {
		t.Fatal("VM SSH action did not create a command")
	}
	if cmd := m.sshAction(host, host.target.address).run(m); cmd == nil {
		t.Fatal("host SSH action did not create a command")
	}
	if len(fake.ssh) != 2 || fake.ssh[0].User != "ubuntu" || fake.ssh[1].User != "root" {
		t.Fatalf("per-kind SSH users were not applied: %+v", fake.ssh)
	}
}

func TestSSHUserFallsBackToLegacySharedUser(t *testing.T) {
	fake := &fakeHandoff{}
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Handoff: fake, SSHUser: "admin"})
	vm := findRow(t, m, vsphere.KindVM, "app-01")
	if cmd := m.sshAction(vm, vm.target.address).run(m); cmd == nil {
		t.Fatal("SSH action did not create a command")
	}
	if len(fake.ssh) != 1 || fake.ssh[0].User != "admin" {
		t.Fatalf("legacy SSH user was not used: %+v", fake.ssh)
	}
}

func TestSSHFailureRetainsLastDiagnostic(t *testing.T) {
	err := sshFailure(errors.New("exit status 255"), "ssh: connect to host 10.20.0.11 port 22: Connection refused\n")
	if err == nil || !strings.Contains(err.Error(), "Connection refused") || !strings.Contains(err.Error(), "255") {
		t.Fatalf("SSH diagnostic was not retained: %v", err)
	}
}

func TestVMHeaderOffersAddingItAsAContext(t *testing.T) {
	b := twoHealthy()
	asVCenters(b, "app-01")
	m := newTestModel(t, b, Options{Current: "prod"})
	r := findRow(t, m, vsphere.KindVM, "app-01")

	if _, ok := findAction(m.actionsFor(r, 0), `Add "app-01" as a vCenter context`); !ok {
		t.Fatalf("VM header did not offer context promotion: %+v", m.actionsFor(r, 0))
	}
}

func TestAddAsContextIsDisabledWithoutAnAddress(t *testing.T) {
	b := twoHealthy()
	asVCenters(b, "build-runner-3")
	m := newTestModel(t, b, Options{Current: "prod"})
	r := findRow(t, m, vsphere.KindVM, "build-runner-3")
	a, ok := findAction(m.actionsFor(r, 0), `Add "build-runner-3" as a vCenter context`)
	if !ok {
		t.Fatalf("VM without an address did not offer the disabled action: %+v", m.actionsFor(r, 0))
	}
	if a.disabled != "no address available" {
		t.Errorf("disabled reason is %q, want no address available", a.disabled)
	}
}

func TestAddAsContextSlugDeduplicatesAgainstExistingNames(t *testing.T) {
	b := twoHealthy()
	b.contexts = append(b.contexts, ctx("app-01", "https://vcsa.app-01.internal"))
	asVCenters(b, "app-01")
	m := newTestModel(t, b, Options{Current: "prod"})
	r := findRow(t, m, vsphere.KindVM, "app-01")

	if _, ok := findAction(m.actionsFor(r, 0), `Add "app-01-2" as a vCenter context`); !ok {
		t.Fatalf("context slug did not avoid the existing name: %+v", m.actionsFor(r, 0))
	}
}

func TestAddAsContextSeedsTheFormFromTheVM(t *testing.T) {
	b := twoHealthy()
	b.contexts[1].Transport = config.TransportConfig{Type: config.TransportSOCKS5, Address: "127.0.0.1:1080", RemoteDNS: true}
	asVCenters(b, "app-01")
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "7", "enter", "enter")

	a, ok := findAction(m.actionsFor(*m.vappVM, 0), `Add "app-01" as a vCenter context`)
	if !ok {
		t.Fatalf("member VM did not offer context promotion: %+v", m.actionsFor(*m.vappVM, 0))
	}
	a.run(m)
	settleForm(m)
	if m.form == nil {
		t.Fatal("context promotion did not open the form")
	}
	if m.form.endpoint.Value() != "https://10.20.0.11" || m.form.username.Value() != "" {
		t.Errorf("form was not seeded with endpoint/blank username: endpoint=%q username=%q", m.form.endpoint.Value(), m.form.username.Value())
	}
	if m.form.tlsIdx != 1 || m.form.thumbprint.Value() != "" {
		t.Errorf("seeded form trust policy is tls=%d thumbprint=%q", m.form.tlsIdx, m.form.thumbprint.Value())
	}
	if m.form.transportIdx != 1 || m.form.proxyAddr.Value() != "127.0.0.1:1080" || !m.form.remoteDNS {
		t.Errorf("parent route was not seeded: form=%+v", m.form)
	}
	if m.form.via != "prod" || m.form.viaMoRef != "prod-vm-1" || m.returnTo != modeVAppVMDetail {
		t.Errorf("seeded provenance/return state is via=%q moref=%q return=%v", m.form.via, m.form.viaMoRef, m.returnTo)
	}
	if m.form.cursor != 3 {
		t.Errorf("seeded form cursor is %d, want Username row 3", m.form.cursor)
	}
}

func TestAddAsContextSwitchesToItsExistingContext(t *testing.T) {
	b := twoHealthy()
	nested := ctx("nested-app", "https://10.20.0.11")
	nested.Via, nested.ViaMoRef = "prod", "prod-vm-1"
	b.contexts = append(b.contexts, nested)
	m := newTestModel(t, b, Options{Current: "prod"})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	a, ok := findAction(m.actionsFor(r, 0), `Switch to context "nested-app"`)
	if !ok {
		t.Fatalf("existing nested context was not recognized: %+v", m.actionsFor(r, 0))
	}
	a.run(m)
	if m.mode != modeBrowse || m.current() == nil || m.current().cc.Name != "nested-app" || m.allScope {
		t.Fatalf("switch action did not select the nested context: mode=%v current=%v all=%v", m.mode, m.current(), m.allScope)
	}
}

func TestAddAsContextDisabledInDemo(t *testing.T) {
	b := twoHealthy()
	asVCenters(b, "app-01")
	m := newTestModel(t, b, Options{Current: "prod", Demo: true})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	a, ok := findAction(m.actionsFor(r, 0), `Add "app-01" as a vCenter context`)
	if !ok {
		t.Fatalf("demo VM did not offer context promotion: %+v", m.actionsFor(r, 0))
	}
	if a.disabled != demoReadOnlyReason {
		t.Fatalf("expected context promotion disabled with %q in demo, got disabled=%q", demoReadOnlyReason, a.disabled)
	}
	if a.run != nil {
		t.Error("disabled action must not have a run func")
	}
}

func TestSwitchToExistingContextEnabledInDemo(t *testing.T) {
	b := twoHealthy()
	nested := ctx("nested-app", "https://10.20.0.11")
	nested.Via, nested.ViaMoRef = "prod", "prod-vm-1"
	b.contexts = append(b.contexts, nested)
	m := newTestModel(t, b, Options{Current: "prod", Demo: true})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	a, ok := findAction(m.actionsFor(r, 0), `Switch to context "nested-app"`)
	if !ok {
		t.Fatalf("existing nested context was not recognized in demo: %+v", m.actionsFor(r, 0))
	}
	if a.disabled != "" {
		t.Fatalf("switch action should be enabled in demo, got disabled=%q", a.disabled)
	}
	if a.run == nil {
		t.Fatal("switch action must have a run func")
	}
	a.run(m)
	if m.mode != modeBrowse || m.current() == nil || m.current().cc.Name != "nested-app" || m.allScope {
		t.Fatalf("switch action did not select the nested context in demo: mode=%v current=%v all=%v", m.mode, m.current(), m.allScope)
	}
}

// TestActionPopupFitsTerminalWidth guards the popup the way
// TestDetailViewNeverExceedsWidth already guards the rest of the interface:
// a long label plus a long URL must truncate rather than wrap the frame.
func TestActionPopupFitsTerminalWidth(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	m.width, m.height = 80, 24
	r := findRow(t, m, vsphere.KindVM, "app-01")
	press(t, m, "enter")
	m.detailCursor = 0
	press(t, m, "enter")
	if m.actions == nil {
		t.Fatal("expected the object header to open a popup")
	}
	_ = r
	for _, line := range m.actionListLines() {
		if w := ansi.StringWidth(line); w > m.width {
			t.Errorf("action popup line exceeds terminal width (%d > %d): %q", w, m.width, line)
		}
	}
}

// sshRouteModel builds a model over contexts a (given transport) and b, where
// every VM is app-01 at 10.20.0.11 — the same guest IP in both, as two
// isolated estates can have — and hands back what the fake handoff records.
func sshRouteModel(t *testing.T, contexts []*config.Context, routes []config.SSHRoute, opts Options) (*Model, *fakeHandoff) {
	t.Helper()
	b := &fakeBackend{contexts: contexts, inventories: map[string]*vsphere.Inventory{}}
	for _, c := range contexts {
		inv := inventoryFor(c.Name)
		inv.VMs[0].GuestHostName = "app-01.corp.local"
		b.inventories[c.Name] = inv
	}
	fake := &fakeHandoff{}
	opts.Handoff, opts.SSHRoutes = fake, routes
	return newTestModel(t, b, opts), fake
}

func runSSH(t *testing.T, m *Model, fake *fakeHandoff, r row, target string) SSHSpec {
	t.Helper()
	n := len(fake.ssh)
	a := m.sshAction(r, target)
	if a.disabled != "" {
		t.Fatalf("SSH to %s disabled: %s", target, a.disabled)
	}
	if a.run(m) == nil || len(fake.ssh) != n+1 {
		t.Fatalf("SSH to %s did not launch", target)
	}
	return fake.ssh[n]
}

const httpProxyArg = "ProxyCommand=nc -X connect -x 100.109.21.17:8080 %h %p"

func TestSSHRouteProxiesGuestOfDirectVCenter(t *testing.T) {
	routes := []config.SSHRoute{{Context: "lab", CIDR: "10.20.0.0/24", Type: "http", ProxyAddress: "100.109.21.17:8080"}}
	m, fake := sshRouteModel(t, []*config.Context{ctx("lab", "https://vcsa.lab.internal")}, routes, Options{Current: "lab", SSHVMUser: "devops"})
	r := findRow(t, m, vsphere.KindVM, "app-01")

	// Both the DNS-name action and the IP action follow the route: it is
	// matched on the guest's IP, not on whichever string ssh connects to.
	for _, target := range sshTargets(r) {
		spec := runSSH(t, m, fake, r, target)
		if want := []string{"-o", httpProxyArg}; !slices.Equal(spec.ProxyArgs, want) {
			t.Errorf("SSH to %s: ProxyArgs = %q, want %q", target, spec.ProxyArgs, want)
		}
		if spec.User != "devops" {
			t.Errorf("SSH to %s: user = %q, want devops", target, spec.User)
		}
	}
	if targets := sshTargets(r); len(targets) != 2 || targets[0] != "app-01.corp.local" {
		t.Fatalf("fixture no longer offers a hostname first: %q", targets)
	}
	if cmd := sshCommand(SSHSpec{Address: "x", ProxyArgs: fake.ssh[0].ProxyArgs}); !strings.Contains(cmd, "'ProxyCommand=nc -X connect -x 100.109.21.17:8080 %h %p'") {
		t.Errorf("proxy is not visible, shell-quoted, in the rendered command: %s", cmd)
	}
}

func TestSSHRouteSOCKS5(t *testing.T) {
	routes := []config.SSHRoute{{Context: "lab", CIDR: "10.20.0.0/16", Type: "socks5", ProxyAddress: "127.0.0.1:1080"}}
	m, fake := sshRouteModel(t, []*config.Context{ctx("lab", "https://vcsa.lab.internal")}, routes, Options{Current: "lab"})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	spec := runSSH(t, m, fake, r, r.target.address)
	if want := []string{"-o", "ProxyCommand=nc -X 5 -x 127.0.0.1:1080 %h %p"}; !slices.Equal(spec.ProxyArgs, want) {
		t.Errorf("ProxyArgs = %q, want %q", spec.ProxyArgs, want)
	}
}

func TestSSHWithoutMatchingRouteInheritsContextTransport(t *testing.T) {
	inherited := []string{"-o", "ProxyCommand=nc -X 5 -x 127.0.0.1:1080 %h %p"}
	// A route for another network, and one for this network in another context.
	routes := []config.SSHRoute{
		{Context: "osaka", CIDR: "192.168.0.0/16", Type: "http", ProxyAddress: "100.109.21.17:8080"},
		{Context: "elsewhere", CIDR: "10.20.0.0/24", Type: "http", ProxyAddress: "100.109.21.17:8080"},
	}
	m, fake := sshRouteModel(t, []*config.Context{proxiedCtx("osaka", "https://vcsa.osaka.internal", "127.0.0.1:1080")}, routes, Options{Current: "osaka"})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	if spec := runSSH(t, m, fake, r, r.target.address); !slices.Equal(spec.ProxyArgs, inherited) {
		t.Errorf("ProxyArgs = %q, want the context's own route %q", spec.ProxyArgs, inherited)
	}
}

func TestSSHDirectRouteOverridesContextProxy(t *testing.T) {
	routes := []config.SSHRoute{{Context: "osaka", CIDR: "10.20.0.0/24", Type: "direct"}}
	m, fake := sshRouteModel(t, []*config.Context{proxiedCtx("osaka", "https://vcsa.osaka.internal", "127.0.0.1:1080")}, routes, Options{Current: "osaka"})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	if spec := runSSH(t, m, fake, r, r.target.address); len(spec.ProxyArgs) != 0 {
		t.Errorf("direct route should bypass the proxied context, got %q", spec.ProxyArgs)
	}
}

func TestSSHRouteLongestPrefixAndContextIsolation(t *testing.T) {
	routes := []config.SSHRoute{
		{Context: "one", CIDR: "10.20.0.0/16", Type: "socks5", ProxyAddress: "wide:1"},
		{Context: "one", CIDR: "10.20.0.0/24", Type: "http", ProxyAddress: "narrow:1"},
		{Context: "two", CIDR: "10.20.0.0/24", Type: "socks5", ProxyAddress: "other:1"},
	}
	contexts := []*config.Context{ctx("one", "https://vcsa.one.internal"), ctx("two", "https://vcsa.two.internal"), ctx("three", "https://vcsa.three.internal")}
	want := map[string][]string{
		"one":   {"-o", "ProxyCommand=nc -X connect -x narrow:1 %h %p"},
		"two":   {"-o", "ProxyCommand=nc -X 5 -x other:1 %h %p"},
		"three": nil,
	}
	// Each context holds the same guest IP; the same routes must resolve
	// them independently.
	for _, c := range contexts {
		m, fake := sshRouteModel(t, contexts, routes, Options{Current: c.Name})
		r := findRow(t, m, vsphere.KindVM, "app-01")
		if r.context != c.Name || r.target.address != "10.20.0.11" {
			t.Fatalf("expected app-01 at 10.20.0.11 in %s, got %s at %q", c.Name, r.context, r.target.address)
		}
		if spec := runSSH(t, m, fake, r, r.target.address); !slices.Equal(spec.ProxyArgs, want[c.Name]) {
			t.Errorf("context %s: ProxyArgs = %q, want %q", c.Name, spec.ProxyArgs, want[c.Name])
		}
	}
}

func TestSSHRouteComposesWithRememberedIdentity(t *testing.T) {
	routes := []config.SSHRoute{{Context: "lab", CIDR: "10.20.0.0/24", Type: "http", ProxyAddress: "100.109.21.17:8080"}}
	contexts := []*config.Context{ctx("lab", "https://vcsa.lab.internal")}
	m, fake := sshRouteModel(t, contexts, routes, Options{
		Current:          "lab",
		SSHUsers:         map[string]string{"lab/lab-vm-1": "ops"},
		SSHIdentityFiles: map[string]string{"lab/lab-vm-1": "/home/me/.ssh/id_devops"},
	})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	spec := runSSH(t, m, fake, r, r.target.address)
	if spec.User != "ops" || spec.IdentityFile != "/home/me/.ssh/id_devops" || !slices.Equal(spec.ProxyArgs, []string{"-o", httpProxyArg}) {
		t.Errorf("route, user and identity should compose: %+v", spec)
	}
	args := sshArgs(spec)
	if !slices.Contains(args, httpProxyArg) || !slices.Contains(args, "/home/me/.ssh/id_devops") {
		t.Errorf("generated args lost the route or the identity: %q", args)
	}
}

func TestSSHRouteNeverAppliesToHostnameTargets(t *testing.T) {
	// An ESXi host is known by name, not IP. A route must not trigger a
	// lookup to decide, so the host keeps its context's transport.
	routes := []config.SSHRoute{{Context: "lab", CIDR: "0.0.0.0/0", Type: "http", ProxyAddress: "100.109.21.17:8080"}}
	m, fake := sshRouteModel(t, []*config.Context{ctx("lab", "https://vcsa.lab.internal")}, routes, Options{Current: "lab"})
	host := findRow(t, m, vsphere.KindHost, "esxi-01")
	if spec := runSSH(t, m, fake, host, host.target.address); len(spec.ProxyArgs) != 0 {
		t.Errorf("host named %q should not be routed by CIDR, got %q", host.target.address, spec.ProxyArgs)
	}
}

func TestSSHRouteDoesNotBypassUnsupportedContextTransport(t *testing.T) {
	// An unmatched target still hits proxyArgs' refusal of an authenticated
	// proxy; a matching route is what opts a subnet out of it.
	cc := proxiedCtx("osaka", "https://vcsa.osaka.internal", "127.0.0.1:1080")
	cc.Transport.Username = "svc"
	m, _ := sshRouteModel(t, []*config.Context{cc}, nil, Options{Current: "osaka"})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	if a := m.sshAction(r, r.target.address); !strings.Contains(a.disabled, "authenticated proxy") {
		t.Errorf("authenticated context proxy must stay explicitly unsupported, got %+v", a)
	}
}

// A validated OpenSSH alias outranks a configured #178 route: it connects
// through the alias with no vsfleet-generated ProxyCommand at all, and
// OpenSSH ProxyJump. This is #187's central precedence rule.
func TestSSHAliasPreferredOverConfiguredRoute(t *testing.T) {
	routes := []config.SSHRoute{{Context: "lab", CIDR: "10.20.0.0/24", Type: "http", ProxyAddress: "100.109.21.17:8080"}}
	m, fake := sshRouteModel(t, []*config.Context{ctx("lab", "https://vcsa.lab.internal")}, routes, Options{Current: "lab"})
	fake.destinations = map[string][]string{"10.20.0.11": {"app-prod"}}
	r := findRow(t, m, vsphere.KindVM, "app-01")

	actions := m.vmSSHActions(r)
	if len(actions) == 0 || !strings.Contains(actions[0].label, "OpenSSH alias") {
		t.Fatalf("expected the alias action first, got %v", labels(actions))
	}
	if cmd := actions[0].run(m); cmd == nil || len(fake.ssh) != 1 {
		t.Fatalf("alias action did not launch: %+v", fake.ssh)
	}
	spec := fake.ssh[0]
	if !spec.Alias || spec.Address != "app-prod" || len(spec.ProxyArgs) != 0 {
		t.Errorf("spec = %+v, want the bare alias with no vsfleet route", spec)
	}
	key := sshUserKey(r)
	if got := m.sshDestinations[key]; got.Kind != "openssh_alias" || got.Alias != "app-prod" {
		t.Errorf("remembered destination = %+v", got)
	}
	if got := m.Snapshot().SSHDestinations[key]; got.Alias != "app-prod" {
		t.Errorf("snapshot did not carry the remembered alias: %v", m.Snapshot().SSHDestinations)
	}
}

// Several aliases and nothing remembered must never be chosen between
// arbitrarily: the menu offers a chooser that opens the destination picker
// instead of a direct connect action.
func TestSSHMultipleAliasesOpensChooserRatherThanGuessing(t *testing.T) {
	m, fake := sshRouteModel(t, []*config.Context{ctx("lab", "https://vcsa.lab.internal")}, nil, Options{Current: "lab"})
	fake.destinations = map[string][]string{"10.20.0.11": {"app-prod", "app-prod-via-bastion"}}
	r := findRow(t, m, vsphere.KindVM, "app-01")

	actions := m.vmSSHActions(r)
	if len(actions) == 0 || !strings.Contains(actions[0].label, "Choose OpenSSH alias") || !strings.Contains(actions[0].label, "2 matches") {
		t.Fatalf("expected a chooser action first, got %v", labels(actions))
	}
	if cmd := actions[0].run(m); cmd == nil {
		t.Fatal("chooser action did not run")
	}
	if m.sshPrompt == nil {
		t.Fatal("chooser action should open the destination prompt")
	}
	var gotAliases []string
	for _, d := range m.sshPrompt.destinations {
		if d.kind == destAlias {
			gotAliases = append(gotAliases, d.value)
		}
	}
	if want := []string{"app-prod", "app-prod-via-bastion"}; !slices.Equal(gotAliases, want) {
		t.Errorf("prompt destinations = %v, want %v", gotAliases, want)
	}
	if len(fake.ssh) != 0 {
		t.Errorf("opening the chooser must not itself connect: %+v", fake.ssh)
	}
}

// A remembered alias that no longer validates is dropped rather than kept
// or trusted, and SSH falls back through the normal precedence instead of
// silently connecting through the stale route.
func TestSSHStaleRememberedAliasIsDroppedAndFallsBack(t *testing.T) {
	routes := []config.SSHRoute{{Context: "lab", CIDR: "10.20.0.0/24", Type: "http", ProxyAddress: "100.109.21.17:8080"}}
	m, fake := sshRouteModel(t, []*config.Context{ctx("lab", "https://vcsa.lab.internal")}, routes, Options{Current: "lab"})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	key := sshUserKey(r)
	m.sshDestinations = map[string]uistate.SSHDestination{key: {Kind: "openssh_alias", Alias: "app-old"}}
	// The fake reports no valid aliases this time, and that the remembered
	// one is confirmed stale (it would report false if the check merely
	// failed or timed out — see Handoff.DiscoverSSHDestinations).
	fake.destStale = map[string]bool{"10.20.0.11": true}

	actions := m.vmSSHActions(r)
	if strings.Contains(labels(actions)[0], "OpenSSH") {
		t.Fatalf("a stale alias must not still be offered: %v", labels(actions))
	}
	if _, ok := m.sshDestinations[key]; ok {
		t.Errorf("stale remembered destination should have been dropped, got %+v", m.sshDestinations[key])
	}
	if len(fake.destRememberedIn) == 0 || fake.destRememberedIn[0] != "app-old" {
		t.Errorf("discovery was not asked to re-check the remembered alias: %v", fake.destRememberedIn)
	}
	// It falls through to the configured #178 route, exactly as if nothing
	// had ever been remembered.
	spec := runSSH(t, m, fake, r, r.target.address)
	if want := []string{"-o", httpProxyArg}; !slices.Equal(spec.ProxyArgs, want) {
		t.Errorf("ProxyArgs = %q, want the fallback #178 route %q", spec.ProxyArgs, want)
	}
}

// A per-VM route override (chosen through the destination prompt, or
// remembered from an earlier session) outranks a configured #178 route —
// it is more specific to this one machine.
func TestSSHPerVMRouteOverrideBeatsConfiguredRoute(t *testing.T) {
	routes := []config.SSHRoute{{Context: "lab", CIDR: "10.20.0.0/24", Type: "http", ProxyAddress: "100.109.21.17:8080"}}
	m, fake := sshRouteModel(t, []*config.Context{ctx("lab", "https://vcsa.lab.internal")}, routes, Options{Current: "lab"})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	key := sshUserKey(r)
	m.sshDestinations = map[string]uistate.SSHDestination{key: {Kind: "route", Route: sshRouteDirect}}

	spec := runSSH(t, m, fake, r, r.target.address)
	joined := strings.Join(spec.ProxyArgs, " ")
	if !strings.Contains(joined, "ProxyCommand=none") || !strings.Contains(joined, "ProxyJump=none") {
		t.Errorf("ProxyArgs = %q, want the remembered Direct override, not the configured route", spec.ProxyArgs)
	}
}

// Plain guest DNS/IP SSH, with neither an alias nor a remembered override,
// keeps behaving exactly as it did before #187.
func TestSSHWithNoAliasOrOverrideIsUnchanged(t *testing.T) {
	routes := []config.SSHRoute{{Context: "lab", CIDR: "10.20.0.0/24", Type: "socks5", ProxyAddress: "127.0.0.1:1080"}}
	m, fake := sshRouteModel(t, []*config.Context{ctx("lab", "https://vcsa.lab.internal")}, routes, Options{Current: "lab"})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	spec := runSSH(t, m, fake, r, r.target.address)
	if want := []string{"-o", "ProxyCommand=nc -X 5 -x 127.0.0.1:1080 %h %p"}; !slices.Equal(spec.ProxyArgs, want) {
		t.Errorf("ProxyArgs = %q, want the unchanged #178 route %q", spec.ProxyArgs, want)
	}
	if spec.Alias {
		t.Error("no alias was discovered; spec must not claim to be one")
	}
}

// "vsfleet demo" dials nothing, including alias discovery.
func TestDemoNeverRunsAliasDiscovery(t *testing.T) {
	m, fake := sshRouteModel(t, []*config.Context{ctx("lab", "https://vcsa.lab.internal")}, nil, Options{Current: "lab", Demo: true})
	fake.destinations = map[string][]string{"10.20.0.11": {"app-prod"}}
	r := findRow(t, m, vsphere.KindVM, "app-01")

	for _, a := range m.vmSSHActions(r) {
		if strings.Contains(a.label, "OpenSSH") {
			t.Errorf("demo mode must not offer an alias action: %v", labels(m.vmSSHActions(r)))
		}
	}
	for _, a := range m.sshFieldActions(r, r.target.address) {
		if strings.Contains(a.label, "OpenSSH") {
			t.Errorf("demo mode must not offer an alias action on a field either: %v", labels(m.sshFieldActions(r, r.target.address)))
		}
	}
	if len(fake.destCalls) != 0 {
		t.Errorf("demo mode ran alias discovery: %v", fake.destCalls)
	}
}

func TestDemoSSHRouteLaunchesNothing(t *testing.T) {
	routes := []config.SSHRoute{{Context: "lab", CIDR: "10.20.0.0/24", Type: "http", ProxyAddress: "100.109.21.17:8080"}}
	m, fake := sshRouteModel(t, []*config.Context{ctx("lab", "https://vcsa.lab.internal")}, routes, Options{Current: "lab", Demo: true})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	a := m.sshAction(r, r.target.address)
	if a.disabled == "" || a.run != nil {
		t.Errorf("demo mode must not offer a runnable SSH action: %+v", a)
	}
	if len(fake.ssh) != 0 {
		t.Errorf("demo mode launched ssh: %+v", fake.ssh)
	}
}

func TestOrdinaryVMDoesNotOfferToBeAddedAsAContext(t *testing.T) {
	// app-01 has an address but nothing marks it as a vCenter, the way a
	// domain controller does not.
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	r := findRow(t, m, vsphere.KindVM, "app-01")
	for _, a := range m.actionsFor(r, 0) {
		if strings.Contains(a.label, "vCenter context") {
			t.Fatalf("ordinary VM was offered %q: %v", a.label, labels(m.actionsFor(r, 0)))
		}
	}
	press(t, m, "enter", "enter")
	if m.actions == nil || m.actions.cursor != 0 || !m.actions.items[0].runnable() {
		t.Fatalf("popup on an ordinary VM did not start on its first runnable action: %+v", m.actions)
	}
}

func TestVCenterNameEvidence(t *testing.T) {
	for name, want := range map[string]bool{
		"vcsa":                 true,
		"prod-vcsa-01":         true,
		"VCSA01":               true,
		"vcenter.lab.local":    true,
		"vCenter Server 8":     true,
		"vc01":                 true,
		"dc-vc-02":             true,
		"ad-dc-01":             false,
		"vcls-1":               false,
		"abc01":                false,
		"build-runner-3":       false,
		"app-01":               false,
		"vcard-service":        false,
		"":                     false,
		"nvcenter-lookalike-x": false,
	} {
		if got := vcenterNameEvidence(name); got != want {
			t.Errorf("vcenterNameEvidence(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestUnavailableActionsSortAfterRunnableOnes(t *testing.T) {
	// build-runner-3 looks like a vCenter but has no address: the add action
	// applies and cannot run yet, so it stays listed, last, with its reason.
	b := twoHealthy()
	asVCenters(b, "build-runner-3")
	m := newTestModel(t, b, Options{Current: "prod"})
	r := findRow(t, m, vsphere.KindVM, "build-runner-3")
	items := m.actionsFor(r, 0)
	seenDisabled := false
	for _, a := range items {
		if !a.runnable() {
			seenDisabled = true
		} else if seenDisabled {
			t.Fatalf("runnable %q listed after an unavailable action: %v", a.label, labels(items))
		}
	}
	add, ok := findAction(items, `Add "build-runner-3" as a vCenter context`)
	if !ok || add.disabled != "no address available" {
		t.Fatalf("applicable but unavailable action missing or lost its reason: %+v", items)
	}
	if last := items[len(items)-1]; last.runnable() {
		t.Fatalf("expected the list to end with an unavailable action, got %q", last.label)
	}
}

func TestActionPopupCursorSkipsUnavailableActions(t *testing.T) {
	// In demo mode every launching action is disabled and used to be listed
	// first: the cursor must start on, and never leave, the runnable entries.
	b := twoHealthy()
	asVCenters(b, "app-01")
	m := newTestModel(t, b, Options{Current: "prod", Demo: true})
	m.width, m.height = 80, 24
	press(t, m, "enter", "enter")
	if m.actions == nil {
		t.Fatal("expected a popup on the VM header")
	}
	if !m.actions.items[m.actions.cursor].runnable() || m.actions.cursor != 0 {
		t.Fatalf("cursor starts on %d (%q), want the first runnable action", m.actions.cursor, m.actions.items[m.actions.cursor].label)
	}
	for i := 0; i < len(m.actions.items)+2; i++ {
		press(t, m, "down")
		if !m.actions.items[m.actions.cursor].runnable() {
			t.Fatalf("down moved the cursor onto unavailable %q", m.actions.items[m.actions.cursor].label)
		}
	}
	for i := 0; i < len(m.actions.items)+2; i++ {
		press(t, m, "up")
		if !m.actions.items[m.actions.cursor].runnable() {
			t.Fatalf("up moved the cursor onto unavailable %q", m.actions.items[m.actions.cursor].label)
		}
	}
	for _, line := range m.actionListLines() {
		if strings.Contains(line, "▸") && strings.Contains(line, "demo —") {
			t.Errorf("popup marks an unavailable action as the cursor row: %q", line)
		}
	}
	press(t, m, "enter")
	if m.actions != nil {
		t.Error("Enter on the runnable entry did not run and close the popup")
	}
}

func TestOpenActionListWithNothingRunnableExplainsWhy(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "enter")
	cmd := m.openActionList([]action{
		{label: "SSH to 10.20.0.11", disabled: "no route"},
		{label: "Open in vSphere Client", disabled: "vCenter not in scope"},
	})
	if cmd != nil || m.actions != nil {
		t.Fatalf("popup opened with nothing to run: cmd=%v actions=%+v", cmd, m.actions)
	}
	if !m.messageBad || !strings.Contains(m.message, "no route") {
		t.Errorf("message does not give the reason: %q", m.message)
	}
}
