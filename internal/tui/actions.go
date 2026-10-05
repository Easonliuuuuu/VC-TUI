package tui

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/uistate"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Per-VM route override kinds — the values uistate.SSHDestination.Route
// carries when Kind is "route". "openssh" and "direct" need no address;
// "http" and "socks5" are the same unauthenticated proxy types
// config.SSHRoute already supports, reused so routeOverrideArgs (see
// handoff.go) can build on proxyArgs instead of duplicating it.
const (
	sshRouteOpenSSH = "openssh"
	sshRouteDirect  = "direct"
)

// action is one entry in the field cursor's action list: what to call it, an
// optional second column showing the command or URL it would actually run,
// why it cannot run right now (empty means it can), and what running it
// does. disabled and run are mutually exclusive in practice — an action with
// a reason never has a run func — but nothing enforces that beyond runAction
// checking disabled first.
type action struct {
	label    string
	detail   string
	disabled string
	run      func(m *Model) tea.Cmd
}

// actionList is the popup opened on a line with more than one action,
// non-nil on Model exactly while it is open — the same idiom credPrompt
// already uses. Unlike credPrompt it is only ever reachable from
// modeDetail or modeVAppVMDetail, so it takes key priority inside their
// handlers rather than globally in handleKey.
type actionList struct {
	items  []action
	cursor int
}

// demoDisabledReason is why every action that would launch a process or a
// browser is disabled while Options.Demo is set — see the Handoff doc
// comment for why that check lives here instead of inside a Handoff
// implementation.
const demoDisabledReason = "demo — no external commands"

// demoReadOnlyReason is why actions that mutate contexts or persistent
// configuration are disabled in demo mode. The presentation is read-only.
const demoReadOnlyReason = "demo — read-only"

// actionsFor builds the actions available on one line of an open detail
// pane. Line 0 is the object's own header; every other line is
// r.detail[cursor-2] — see detailFocusable, whose indexing this matches
// exactly (index 1 is always the blank line under the title).
//
// Whatever the builders return, runnable actions come first and actions that
// cannot run right now follow in their original order, so the popup opens on
// something Enter can do and the unavailable entries read as a footnote. An
// action that does not apply to the object at all is not built in the first
// place; see addContextAction for the distinction.
func (m *Model) actionsFor(r row, cursor int) []action {
	if cursor == 0 {
		return runnableFirst(m.objectActions(r))
	}
	i := cursor - 2
	if i < 0 || i >= len(r.detail) {
		return nil
	}
	return runnableFirst(m.fieldActions(r, r.detail[i]))
}

// runnable reports whether Enter on a can do anything.
func (a action) runnable() bool { return a.disabled == "" && a.run != nil }

// runnableFirst stably moves actions that cannot run behind those that can.
func runnableFirst(items []action) []action {
	slices.SortStableFunc(items, func(a, b action) int {
		switch {
		case a.runnable() == b.runnable():
			return 0
		case a.runnable():
			return -1
		default:
			return 1
		}
	})
	return items
}

// firstRunnable is the index of the first action Enter can run, or -1.
func firstRunnable(items []action) int {
	return slices.IndexFunc(items, action.runnable)
}

// copyAction is "Copy value" — the one action every addressable line offers,
// which every other action sits alongside rather than replaces.
func copyAction(value string) action {
	return action{label: "Copy value", run: func(m *Model) tea.Cmd { return m.copyCmd(value) }}
}

func copyNamed(label, value string) action {
	return action{label: label, run: func(m *Model) tea.Cmd { return m.copyCmd(value) }}
}

// openAction is "Open in vSphere Client" for whatever row it is given,
// built once and shared by the object header of every kind that has a deep
// link. It reports its own unavailability — no server GUID yet, or a route
// the browser cannot follow — as a disabled reason rather than being
// omitted, so an operator sees why rather than wondering where it went.
func (m *Model) openAction(r row) action {
	st := m.byName[r.context]
	if st == nil {
		return action{label: "Open in vSphere Client", disabled: "vCenter not in scope"}
	}
	if m.demo {
		return action{label: "Open in vSphere Client", disabled: demoDisabledReason}
	}
	statusInfo, _ := m.backend.Status(r.context)
	url, ok := vsphereClientURL(st.cc, r.target.morefKind, r.target.moref, statusInfo.InstanceID)
	if !ok {
		return action{label: "Open in vSphere Client", disabled: "vCenter identity not known yet — reload and try again"}
	}
	if !isDirect(st.cc.Transport) {
		return action{
			label:    "Open in vSphere Client",
			disabled: "no route — " + r.context + " is proxied",
			run:      nil,
		}
	}
	return action{label: "Open in vSphere Client", detail: url, run: func(m *Model) tea.Cmd { return m.openURLCmd(url) }}
}

// hostClientAction is "Open in Host Client", offered only for hosts. It
// needs no vCenter identity, only the host's own address, but the browser
// still cannot reach it through a proxy any more than it can reach the
// vSphere Client — so the same disabled reason applies.
func (m *Model) hostClientAction(r row) action {
	url, ok := hostClientURL(r.target.address)
	if !ok {
		return action{label: "Open in Host Client", disabled: "no address for this host"}
	}
	if m.demo {
		return action{label: "Open in Host Client", disabled: demoDisabledReason}
	}
	if st := m.byName[r.context]; st != nil && !isDirect(st.cc.Transport) {
		return action{label: "Open in Host Client", disabled: "no route — " + r.context + " is proxied"}
	}
	return action{label: "Open in Host Client", detail: url, run: func(m *Model) tea.Cmd { return m.openURLCmd(url) }}
}

// sshAction is "SSH to <user>@<address>", offered on a VM's or host's object
// header and on a VM's IP address and DNS name fields alike — the target and
// the route are the same question wherever it is asked from. The label names
// the user ssh will really connect as, so it never reads as if the address
// alone were enough; see sshShownUser.
func (m *Model) sshAction(r row, address string) action {
	label := "SSH to " + address
	if address == "" {
		return action{label: "SSH", disabled: "no address available"}
	}
	if m.demo {
		return action{label: label, disabled: demoDisabledReason}
	}
	spec, reason := m.sshSpec(r, address)
	if reason != "" {
		return action{label: label, disabled: reason}
	}
	shown := spec
	shown.User = m.sshShownUser(spec)
	return action{label: "SSH to " + sshTarget(shown), detail: sshCommand(shown), run: func(m *Model) tea.Cmd { return m.sshCmd(spec) }}
}

// sshAsAction opens the combined destination, username and identity picker
// instead of connecting immediately. vSphere cannot supply a guest's login —
// VMware Tools reports no account names — and a nonstandard private-key path
// is not part of OpenSSH's default search list; an OpenSSH alias, or a
// per-VM route override, is not something the object header's single "SSH
// to ..." action can offer a choice between either.
func (m *Model) sshAsAction(r row, address string) action {
	const label = "SSH with a different destination, user or key…"
	if address == "" {
		return action{label: label, disabled: "no address available"}
	}
	if m.demo {
		return action{label: label, disabled: demoDisabledReason}
	}
	spec, reason := m.sshSpec(r, address)
	if reason != "" {
		return action{label: label, disabled: reason}
	}
	prefill, explicit := m.sshShownUser(spec), spec.User != ""
	key := sshUserKey(r)
	info := m.sshDestInfoFor(r)
	return action{label: label, detail: address, run: func(m *Model) tea.Cmd {
		return m.openSSHPrompt(r, spec, key, prefill, explicit, sshIdentityKey(r), info)
	}}
}

// sshAliasAction is the menu's top SSH entry once discovery has proven an
// OpenSSH alias already reaches this VM: "ssh <alias>", with OpenSSH itself
// — never vsfleet — owning whatever ProxyJump, ProxyCommand, Port or other
// route that alias's Host block applies.
func (m *Model) sshAliasAction(r row, alias string) action {
	spec := SSHSpec{Address: alias, Alias: true, User: m.sshUserFor(r), IdentityFile: m.sshIdentityFor(r)}
	shown := spec
	shown.User = m.sshShownUser(spec)
	label := "SSH to " + sshTarget(shown) + " — OpenSSH alias"
	return action{label: label, detail: sshCommand(shown), run: func(m *Model) tea.Cmd {
		m.rememberSSHDestination(sshUserKey(r), uistate.SSHDestination{Kind: "openssh_alias", Alias: alias})
		return m.sshCmd(spec)
	}}
}

// sshChooseAliasAction is offered instead of sshAliasAction when discovery
// found more than one alias and none is remembered: choosing arbitrarily
// among several routes an operator might have configured for different
// reasons is exactly what the design this issue follows refuses to do, so
// the destination picker opens instead.
func (m *Model) sshChooseAliasAction(r row, info sshDestInfo) action {
	label := fmt.Sprintf("Choose OpenSSH alias… (%d matches)", len(info.aliases))
	targets := sshTargets(r)
	address := ""
	if len(targets) > 0 {
		address = targets[0]
	}
	spec, reason := m.sshSpec(r, address)
	if reason != "" {
		return action{label: label, disabled: reason}
	}
	prefill, explicit := m.sshShownUser(spec), spec.User != ""
	key := sshUserKey(r)
	return action{label: label, run: func(m *Model) tea.Cmd {
		return m.openSSHPrompt(r, spec, key, prefill, explicit, sshIdentityKey(r), info)
	}}
}

// sshSpec builds the plain (non-alias) SSH handoff for one of a VM's or
// host's own native addresses — its guest DNS name or IP, never an OpenSSH
// alias, which sshAliasAction builds directly instead so a menu entry
// labeled with the guest's own address is never silently redirected
// elsewhere. Precedence, most specific first: a remembered per-VM route
// override, an explicit SSH route for this context and the machine's IP
// (#178), else the context's own transport, else whatever ssh(1) resolves
// on its own. The override and #178 route are both matched on the row's IP
// rather than on address, which may be the guest's DNS name — otherwise
// neither would apply to the first action offered on most VMs, and matching
// a name would need a lookup just to draw the menu.
func (m *Model) sshSpec(r row, address string) (SSHSpec, string) {
	spec := SSHSpec{Address: address, User: m.sshUserFor(r), IdentityFile: m.sshIdentityFor(r)}
	if dest, ok := m.sshDestinations[sshUserKey(r)]; ok && dest.Kind == "route" {
		args, reason := routeOverrideArgs(dest.Route, dest.ProxyAddress)
		if reason != "" {
			return spec, reason
		}
		spec.ProxyArgs = args
		return spec, ""
	}
	return m.autoSSHSpec(spec, r)
}

// autoSSHSpec is sshSpec's fallback tail — #178's route, then the context
// transport, then plain ssh(1) — factored out so the SSH prompt's
// "Automatic" route choice can preview and use the exact same resolution
// without first consulting (or overwriting) a remembered override that the
// operator has not actually confirmed yet.
func (m *Model) autoSSHSpec(spec SSHSpec, r row) (SSHSpec, string) {
	transport, routed := config.ResolveSSHRoute(m.sshRoutes, r.context, r.target.address)
	if !routed {
		st := m.byName[r.context]
		if st == nil {
			return spec, ""
		}
		transport = st.cc.Transport
	}
	args, reason := proxyArgs(transport)
	if reason != "" {
		return spec, reason
	}
	spec.ProxyArgs = args
	return spec, ""
}

// sshDestInfo is the result of one bounded alias-discovery pass for a VM: a
// deterministically ordered, deduplicated set of OpenSSH aliases confirmed
// (via a bounded "ssh -G", never trusted from config text alone) to reach
// its guest IP, plus which one — if any — should be preferred without
// asking. err is a non-fatal discovery problem to show alongside whatever
// aliases were still found, the same way identity discovery's err works.
type sshDestInfo struct {
	aliases   []string
	preferred string
	err       error
}

// sshDiscoveryCache memoizes the most recent sshDestInfoFor result. Building
// one row's action menu calls into it from several places — the alias
// action, each native target's action, the "different destination" prompt —
// and a key mismatch (a different VM, or none yet queried) is simply a
// cache miss, not something that needs invalidating explicitly.
type sshDiscoveryCache struct {
	key  string
	info sshDestInfo
}

// sshDestInfoFor runs (or reuses the last) bounded alias discovery for r,
// dropping a remembered alias from state the moment discovery proves it no
// longer reaches this VM's current guest IP — "fail closed and rediscover,"
// never fall back to silently connecting somewhere else. Demo mode and a VM
// with no known guest IP both skip discovery entirely and report no
// aliases, the same "nothing to say" empty value sshSpec's own fallbacks
// already treat as unremarkable.
func (m *Model) sshDestInfoFor(r row) sshDestInfo {
	if m.demo || r.target.address == "" {
		return sshDestInfo{}
	}
	key := sshUserKey(r)
	if key == "" {
		return sshDestInfo{}
	}
	if m.sshDiscovery != nil && m.sshDiscovery.key == key {
		return m.sshDiscovery.info
	}
	remembered := ""
	if dest, ok := m.sshDestinations[key]; ok && dest.Kind == "openssh_alias" {
		remembered = dest.Alias
	}
	aliases, stale, err := m.handoff.DiscoverSSHDestinations(r.target.address, remembered)
	if stale {
		delete(m.sshDestinations, key)
		remembered = ""
	}
	preferred := ""
	switch {
	case remembered != "" && slices.Contains(aliases, remembered):
		preferred = remembered
	case len(aliases) == 1:
		preferred = aliases[0]
	}
	info := sshDestInfo{aliases: aliases, preferred: preferred, err: err}
	m.sshDiscovery = &sshDiscoveryCache{key: key, info: info}
	return info
}

// rememberSSHDestination persists dest as key's SSH destination, replacing
// whatever was remembered before. An empty key (a row with no MoRef) is a
// no-op — there is nothing stable to key it by.
func (m *Model) rememberSSHDestination(key string, dest uistate.SSHDestination) {
	if key == "" {
		return
	}
	if m.sshDestinations == nil {
		m.sshDestinations = map[string]uistate.SSHDestination{}
	}
	m.sshDestinations[key] = dest
}

// forgetSSHDestination removes key's remembered SSH destination — chosen
// when the operator picks "Automatic" explicitly, the same way choosing
// OpenSSH default forgets a remembered identity.
func (m *Model) forgetSSHDestination(key string) {
	delete(m.sshDestinations, key)
}

// sshUserFor picks the user vsfleet itself supplies for row r, most specific
// first: what the operator last typed for this exact machine, then the
// configured per-kind default, then the shared one. Empty means vsfleet has
// no opinion and ssh(1) resolves it.
func (m *Model) sshUserFor(r row) string {
	if key := sshUserKey(r); key != "" {
		if u := m.sshUsers[key]; u != "" {
			return u
		}
	}
	switch r.kind {
	case vsphere.KindHost:
		if m.sshHostUser != "" {
			return m.sshHostUser
		}
	case vsphere.KindVM:
		if m.sshVMUser != "" {
			return m.sshVMUser
		}
	}
	return m.sshUser
}

func (m *Model) sshIdentityFor(r row) string {
	if key := sshIdentityKey(r); key != "" {
		return m.sshIdentityFiles[key]
	}
	return ""
}

// sshUserKey identifies one machine for remembering the user typed for it. A
// moref is only unique within one vCenter, so the context is part of it.
func sshUserKey(r row) string {
	if r.target.moref == "" {
		return ""
	}
	return r.context + "/" + r.target.moref
}

func sshIdentityKey(r row) string { return sshUserKey(r) }

// sshShownUser is the user to display for spec: the one vsfleet supplies, or
// else whatever ssh(1) would resolve on its own. Demo mode never asks —
// "vsfleet demo" launches nothing, including ssh -G.
func (m *Model) sshShownUser(spec SSHSpec) string {
	if spec.User != "" || m.demo {
		return spec.User
	}
	return m.handoff.ResolveUser(spec.Address)
}

// sshTargets lists what a VM can be reached at, name first: a name is the
// only thing a Host block in ~/.ssh/config can match, while the IP is kept
// because it cannot fail on a resolver that has never heard of the guest's
// own idea of its name.
func sshTargets(r row) []string {
	var out []string
	if h := r.target.hostName; h != "" && !isLoopbackName(h) {
		out = append(out, h)
	}
	if a := r.target.address; a != "" && (len(out) == 0 || out[0] != a) {
		out = append(out, a)
	}
	return out
}

// isLoopbackName spots the name an unconfigured guest reports for itself,
// which would send ssh to the operator's own machine.
func isLoopbackName(name string) bool {
	name = strings.ToLower(name)
	return name == "localhost" || strings.HasPrefix(name, "localhost.")
}

// vmSSHActions is every SSH entry for a VM's header: an OpenSSH alias entry
// first when discovery found one to prefer or several to choose between,
// then one action per native target (its guest DNS name, its IP), then the
// combined prompt aimed at the first.
func (m *Model) vmSSHActions(r row) []action {
	targets := sshTargets(r)
	out := make([]action, 0, len(targets)+2)
	if !m.demo {
		if info := m.sshDestInfoFor(r); info.preferred != "" {
			out = append(out, m.sshAliasAction(r, info.preferred))
		} else if len(info.aliases) > 1 {
			out = append(out, m.sshChooseAliasAction(r, info))
		}
	}
	for _, t := range targets {
		out = append(out, m.sshAction(r, t))
	}
	if len(targets) > 0 {
		out = append(out, m.sshAsAction(r, targets[0]))
	}
	return out
}

// sshCommandCopyAction is "Copy ssh user@address" — the string an operator
// pastes into a second tmux pane rather than handing this program's own
// terminal over.
func (m *Model) sshCommandCopyAction(r row, address string) action {
	spec, _ := m.sshSpec(r, address)
	spec.User = m.sshShownUser(spec)
	cmd := sshCommand(spec)
	return action{label: "Copy " + cmd, run: func(m *Model) tea.Cmd { return m.copyCmd(cmd) }}
}

func sshTarget(spec SSHSpec) string {
	if spec.User != "" {
		return spec.User + "@" + spec.Address
	}
	return spec.Address
}

func sshCommand(spec SSHSpec) string {
	args := sshArgs(spec)
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return "ssh " + strings.Join(quoted, " ")
}

func sshArgs(spec SSHSpec) []string {
	args := append([]string{}, spec.ProxyArgs...)
	if !spec.Alias {
		// An alias's own Host block may set its own ConnectTimeout — or
		// deliberately not — and layering vsfleet's default over it would
		// be exactly the kind of reconstruction #187 exists to avoid. Every
		// other target still gets it, since nothing else pins one.
		args = append(args, "-o", "ConnectTimeout=15")
	}
	if spec.IdentityFile != "" {
		args = append(args,
			"-i", spec.IdentityFile,
			"-o", "IdentitiesOnly=yes",
			"-o", "PubkeyAuthentication=yes",
			"-o", "PreferredAuthentications=publickey",
			"-o", "PasswordAuthentication=no",
			"-o", "KbdInteractiveAuthentication=no",
			"-o", "BatchMode=no",
		)
	}
	return append(args, sshTarget(spec))
}

func shellQuote(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_@%+=:,./-", r))
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// nestedContextFor finds the context represented by a VM in the parent
// context. Saved provenance is authoritative because a VM's IP can change;
// the host-name fallback keeps hand-made contexts and older files useful.
func (m *Model) nestedContextFor(parentContext, moref, address string) *contextState {
	for _, st := range m.states {
		if st.cc.Via == parentContext && st.cc.ViaMoRef == moref {
			return st
		}
	}
	if address == "" {
		return nil
	}
	for _, st := range m.states {
		if st.cc.Name == parentContext {
			continue
		}
		if st.cc.Host() == address {
			return st
		}
	}
	return nil
}

// vcenterNameEvidence reports whether a VM's own name, or the name its guest
// reports, reads as a vCenter Server: a name part that starts with "vcsa",
// "vcenter" or "vcentre", or is "vc" or "vc" plus digits ("vc01"). Parts are
// split on anything that is not a letter or digit, so "prod-vcsa-01" and
// "VCenter.lab.local" match while "vcls-1" (a vSphere cluster service agent),
// "abc01" and "dc-01" do not. It is deliberately name evidence only: the guest
// OS cannot tell a vCenter appliance from any other Photon OS appliance.
func vcenterNameEvidence(names ...string) bool {
	for _, name := range names {
		parts := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		for _, p := range parts {
			switch {
			case strings.HasPrefix(p, "vcsa"), strings.HasPrefix(p, "vcenter"), strings.HasPrefix(p, "vcentre"):
				return true
			case p == "vc":
				return true
			case strings.HasPrefix(p, "vc") && strings.Trim(p[2:], "0123456789") == "":
				return true
			}
		}
	}
	return false
}

// addContextAction promotes a VM that plausibly is a vCenter to a context
// form, or switches to the context it already is. The second result says
// whether the action applies to this VM at all; a VM that does not look like
// a vCenter (a domain controller, a build runner) is not offered it, instead
// of being offered a disabled entry that can never become runnable. What does
// apply but cannot run right now stays visible with its reason: no address
// yet, or demo mode, where the presentation is read-only and does not persist
// context mutations. Existing nested contexts remain switchable and are always
// offered, since saved provenance or an address match already proves the VM
// is a vCenter the operator has added.
func (m *Model) addContextAction(r row, address string) (action, bool) {
	if nested := m.nestedContextFor(r.context, r.target.moref, address); nested != nil {
		name := nested.cc.Name
		return action{
			label: fmt.Sprintf("Switch to context %q", name),
			run: func(m *Model) tea.Cmd {
				m.selectByName(name)
				m.allScope = false
				m.cursor, m.offset = 0, 0
				m.setMessage("", false)
				m.mode = modeBrowse
				return tea.Batch(m.ensureSelectedLoaded(false)...)
			},
		}, true
	}

	if !vcenterNameEvidence(r.name, r.target.hostName) {
		return action{}, false
	}
	slug := m.uniqueContextName(r.name)
	label := fmt.Sprintf("Add %q as a vCenter context", slug)
	if address == "" {
		return action{label: label, disabled: "no address available"}, true
	}
	if m.demo {
		return action{label: label, disabled: demoReadOnlyReason}, true
	}
	transport := config.TransportConfig{Type: config.TransportDirect}
	if parent := m.byName[r.context]; parent != nil {
		transport = parent.cc.Transport
	}
	seed := contextSeed{
		name:      slug,
		endpoint:  "https://" + address,
		transport: transport,
		via:       r.context,
		viaMoRef:  r.target.moref,
	}
	return action{label: label, run: func(m *Model) tea.Cmd {
		m.returnTo = m.mode
		return m.enterFormSeeded(seed)
	}}, true
}

// jumpAction narrows the table to kind's rows whose actionJoins field named
// by matcher equals value — "the VMs on this host" — until Esc clears it.
// See jumpConstraint and its use in Model.rows.
func jumpAction(label string, kind vsphere.Kind, matcher, value string) action {
	return action{label: label, run: func(m *Model) tea.Cmd {
		m.jump = &jumpConstraint{kind: kind, matcher: matcher, value: value, label: label}
		m.kind = kind
		m.filter.SetValue("")
		m.cursor, m.offset = 0, 0
		m.clearVAppWorkspace()
		m.clearSwitchWorkspace()
		m.clearDSWorkspace()
		m.mode = m.detailFrom
		return nil
	}}
}

// jumpToNamed switches to kind's tab filtered to name by the ordinary text
// filter — the simple direction of a cross-resource jump, where the target
// is one specific object named on the row already ("this VM's Host field"
// pointing at that host), as opposed to jumpAction's "everything joined to
// this row", which the name filter cannot express.
func jumpToNamed(label string, kind vsphere.Kind, name string) action {
	return action{label: label, run: func(m *Model) tea.Cmd {
		m.jump = nil
		m.kind = kind
		m.filter.SetValue(name)
		m.cursor, m.offset = 0, 0
		m.clearVAppWorkspace()
		m.clearSwitchWorkspace()
		m.clearDSWorkspace()
		m.mode = m.detailFrom
		return nil
	}}
}

func (m *Model) objectActions(r row) []action {
	var out []action
	switch r.kind {
	case vsphere.KindVM:
		out = append(out, m.vmSSHActions(r)...)
		if a, ok := m.addContextAction(r, r.target.address); ok {
			out = append(out, a)
		}
		out = append(out, m.openAction(r))
		out = append(out, copyNamed("Copy MoRef", r.target.moref))
	case vsphere.KindTemplate:
		out = append(out, m.openAction(r))
		out = append(out, copyNamed("Copy MoRef", r.target.moref))
	case vsphere.KindHost:
		out = append(out, m.sshAction(r, r.target.address))
		if r.target.address != "" {
			out = append(out, m.sshAsAction(r, r.target.address))
		}
		out = append(out, m.hostClientAction(r))
		out = append(out, m.openAction(r))
		out = append(out, jumpAction("Show VMs on this host", vsphere.KindVM, "host", r.name))
		out = append(out, copyNamed("Copy MoRef", r.target.moref))
	case vsphere.KindCluster:
		out = append(out, m.openAction(r))
		out = append(out, jumpAction("Show hosts in cluster", vsphere.KindHost, "cluster", r.name))
		out = append(out, jumpAction("Show VMs in cluster", vsphere.KindVM, "cluster", r.name))
		out = append(out, copyNamed("Copy MoRef", r.target.moref))
	case vsphere.KindDatastore:
		// Browsing comes first: it is the thing an operator opened a
		// datastore to do that they could not do here before.
		out = append(out, m.browseFilesAction(r))
		out = append(out, m.findFilesAction(r))
		out = append(out, m.openAction(r))
		out = append(out, jumpAction("Show VMs on this datastore", vsphere.KindVM, "datastore", r.name))
		if r.target.path != "" {
			out = append(out, copyNamed("Copy datastore path", r.target.path))
		}
		out = append(out, copyNamed("Copy MoRef", r.target.moref))
	case vsphere.KindNetwork:
		out = append(out, m.openAction(r))
		out = append(out, jumpAction("Show VMs on this network", vsphere.KindVM, "network", r.name))
		out = append(out, copyNamed("Copy MoRef", r.target.moref))
	case vsphere.KindVApp:
		out = append(out, m.openAction(r))
		out = append(out, copyNamed("Copy MoRef", r.target.moref))
	}
	out = append(out, copyAction(r.name))
	return out
}

func (m *Model) fieldActions(r row, f field) []action {
	switch {
	case r.kind == vsphere.KindVM && f.label == "IP address":
		return m.sshFieldActions(r, f.value)
	case r.kind == vsphere.KindVM && f.label == "DNS name":
		return m.sshFieldActions(r, f.value)
	case r.kind == vsphere.KindVM && f.label == "Host":
		return []action{jumpToNamed("Show this host", vsphere.KindHost, f.value), copyAction(f.value)}
	case r.kind == vsphere.KindVM && f.label == "Cluster":
		return []action{jumpToNamed("Show this cluster", vsphere.KindCluster, f.value), copyAction(f.value)}
	case f.label == "Managed object":
		return []action{copyNamed("Copy MoRef", f.value)}
	default:
		return []action{copyAction(f.value)}
	}
}

func (m *Model) sshFieldActions(r row, address string) []action {
	var out []action
	if !m.demo {
		if info := m.sshDestInfoFor(r); info.preferred != "" {
			out = append(out, m.sshAliasAction(r, info.preferred))
		} else if len(info.aliases) > 1 {
			out = append(out, m.sshChooseAliasAction(r, info))
		}
	}
	return append(out, m.sshAction(r, address), m.sshAsAction(r, address), m.sshCommandCopyAction(r, address), copyAction(address))
}

// runAction closes the popup (if one was open) and, unless the action
// declined to be runnable, runs it. A disabled action reaching here at all
// would be a bug in how the list was navigated, not a state runAction needs
// to explain — the popup never lets the cursor land on one; see
// actionListLines.
func (m *Model) runAction(a action) tea.Cmd {
	m.actions = nil
	if a.disabled != "" || a.run == nil {
		return nil
	}
	return a.run(m)
}

// openFieldActions is Enter's meaning inside a detail pane: collect the
// focused line's actions, run the one action immediately if there is
// exactly one, or open the popup to choose among several. A line with no
// actions at all (there is always at least "Copy value", so this is
// unreachable today, but a future field type might have nothing to copy)
// does nothing. The popup's cursor starts on the first runnable action, and
// when nothing on the line can run, Enter says why in the message line
// instead of opening a list nobody can choose from.
func (m *Model) openFieldActions() tea.Cmd {
	r, ok := m.detailRow()
	if !ok {
		return nil
	}
	return m.openActionList(m.actionsFor(r, m.detailCursor))
}

// openActionList acts on items, already ordered runnable-first: nothing for
// none, the action itself for exactly one, a message when none can run, else
// the popup with its cursor on the first runnable entry.
func (m *Model) openActionList(items []action) tea.Cmd {
	first := firstRunnable(items)
	switch {
	case len(items) == 0:
		return nil
	case first < 0:
		a := items[0]
		m.setMessage(fmt.Sprintf("Nothing to run here — %s: %s", a.label, a.disabled), true)
		return nil
	case len(items) == 1:
		return m.runAction(items[0])
	default:
		m.actions = &actionList{items: items, cursor: first}
		m.scrollVMActionIntoView()
		return nil
	}
}

// move steps the popup cursor by dir to the next runnable action, staying
// put when there is none that way: a disabled entry is shown for its reason,
// never selectable.
func (al *actionList) move(dir int) {
	for i := al.cursor + dir; i >= 0 && i < len(al.items); i += dir {
		if al.items[i].runnable() {
			al.cursor = i
			return
		}
	}
}

// handleActionsKey drives the open popup: it owns every key ahead of the
// pane underneath it, the same priority credPrompt holds globally — except
// this overlay is reachable only from modeDetail or modeVAppVMDetail, so the
// check lives at the top of those handlers instead of handleKey.
func (m *Model) handleActionsKey(msg tea.KeyMsg) tea.Cmd {
	al := m.actions
	switch {
	case key.Matches(msg, m.keys.Back):
		m.actions = nil
	case key.Matches(msg, m.keys.Up):
		al.move(-1)
	case key.Matches(msg, m.keys.Down):
		al.move(1)
	case key.Matches(msg, m.keys.Open):
		if al.cursor >= 0 && al.cursor < len(al.items) && al.items[al.cursor].runnable() {
			return m.runAction(al.items[al.cursor])
		}
	}
	m.scrollVMActionIntoView()
	return nil
}
