package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/contextops"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// formRowKind is what a row does with a key press: text and secret rows take
// keystrokes as characters, select and toggle rows change value on left and
// right, button rows act on enter, and static rows just display something.
type formRowKind int

const (
	rowText formRowKind = iota
	rowSecret
	rowSelect
	rowToggle
	rowButton
	rowStatic
)

// formRow is one line of the form. idx and flag point back into the
// contextForm's own fields, so changing a row's value through the row is the
// same as changing the field directly.
type formRow struct {
	label   string
	kind    formRowKind
	input   *textinput.Model
	options []string
	idx     *int
	flag    *bool
	static  string
	hint    string
	action  func(m *Model) tea.Cmd
}

// contextForm is the state behind adding or editing one context. It holds a
// plain text input per free-text field and a plain int or bool per choice —
// bubbles has no form widget, and a context has few enough fields that one
// is not worth building as a reusable abstraction.
type contextForm struct {
	editing  bool
	origName string // the context being edited; empty for a new one

	name, endpoint, username, password textinput.Model
	datacenter, proxyAddr, proxyUser   textinput.Model
	proxyPass, thumbprint              textinput.Model
	// credSource is where an env, file or exec credential lives: a variable
	// name, a path or a program. One input serves all three, because only
	// one of them can be chosen at a time.
	credSource    textinput.Model
	via, viaMoRef string

	credIdx int // an index into credOptions()
	// noKeyring, once set, is why the OS keyring cannot hold a password. A
	// new context then offers every source that works without one instead of
	// taking a password it cannot store.
	noKeyring string
	// savedCred is the reference an edited context was saved with. Keeping
	// the scheme saves it unchanged — a custom keyring key or a labelled
	// prompt included — so opening the form and saving rewrites nothing.
	savedCred    credentials.Ref
	transportIdx int // 0 direct, 1 socks5
	tlsIdx       int // 0 system, 1 thumbprint, 2 insecure
	remoteDNS    bool
	setCurrent   bool

	cursor      int
	testing     bool
	discovering bool
	saving      bool

	// forceSave appears once a test has failed: the Save row becomes "Save
	// anyway" and, pressed again, saves despite the failure rather than
	// silently retrying the same test forever.
	forceSave bool

	diag *vsphere.Diagnosis
	err  string
	note string
}

func newFormInput(placeholder string, width int) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.CharLimit = 256
	ti.Width = width
	return ti
}

func newFormSecret(width int) textinput.Model {
	ti := newFormInput("", width)
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	return ti
}

// newContextForm builds a blank form, or one prefilled from an existing
// context when edit is non-nil. Renaming is out of scope: the name is fixed
// once a context exists, because it is also the keyring key and the identity
// everything else — the config file, the last-selected state — refers to it
// by.
func newContextForm(edit *contextState) *contextForm {
	f := &contextForm{
		name:       newFormInput("prod", 36),
		endpoint:   newFormInput("https://vcsa.example.internal", 48),
		username:   newFormInput("administrator@vsphere.local", 40),
		password:   newFormSecret(40),
		datacenter: newFormInput("optional", 30),
		proxyAddr:  newFormInput("127.0.0.1:1080", 30),
		proxyUser:  newFormInput("optional", 30),
		proxyPass:  newFormSecret(40),
		thumbprint: newFormInput("", 60),
		credSource: newFormInput("", 48),
	}
	if edit == nil {
		return f
	}
	cc := edit.cc
	f.editing = true
	f.origName = cc.Name
	f.name.SetValue(cc.Name)
	f.endpoint.SetValue(cc.Endpoint)
	f.username.SetValue(cc.Username)
	f.via = cc.Via
	f.viaMoRef = cc.ViaMoRef
	f.datacenter.SetValue(cc.Datacenter)
	f.savedCred = cc.Credential
	f.selectCredScheme(cc.Credential.Scheme)
	if cc.Credential.NonInteractive() {
		f.credSource.SetValue(cc.Credential.Value)
	}
	switch cc.Transport.Type {
	case config.TransportSOCKS5:
		f.transportIdx = 1
	case config.TransportHTTPProxy:
		f.transportIdx = 2
	case config.TransportHTTPSProxy:
		f.transportIdx = 3
	}
	if f.transportIdx != 0 {
		f.proxyAddr.SetValue(cc.Transport.Address)
		f.proxyUser.SetValue(cc.Transport.Username)
		f.remoteDNS = cc.Transport.RemoteDNS
	}
	switch cc.TLS.Mode {
	case config.TLSThumbprint:
		f.tlsIdx = 1
		f.thumbprint.SetValue(cc.TLS.Thumbprint)
	case config.TLSInsecure:
		f.tlsIdx = 2
	}
	return f
}

// contextSeed is the connection information inherited when a VM is promoted
// to a context. The parent route is copied so a nested vCenter reached through
// a proxy starts with the same path back to the estate.
type contextSeed struct {
	name, endpoint string
	transport      config.TransportConfig
	via, viaMoRef  string
}

// newSeededContextForm opens the ordinary new-context form with the values a
// VM can already prove. A nested VCSA is normally self-signed, so thumbprint
// mode is selected with a blank fingerprint: system trust would fail and
// insecure would hide the trust decision. Discover is the honest next step;
// formDiscover deliberately does not call validate, while Test and Save do.
func newSeededContextForm(seed contextSeed) *contextForm {
	f := newContextForm(nil)
	f.name.SetValue(seed.name)
	f.endpoint.SetValue(seed.endpoint)
	f.transportIdx = 0
	switch seed.transport.Type {
	case config.TransportSOCKS5:
		f.transportIdx = 1
	case config.TransportHTTPProxy:
		f.transportIdx = 2
	case config.TransportHTTPSProxy:
		f.transportIdx = 3
	}
	if f.transportIdx != 0 {
		f.proxyAddr.SetValue(seed.transport.Address)
		f.proxyUser.SetValue(seed.transport.Username)
		f.remoteDNS = seed.transport.RemoteDNS
	}
	f.tlsIdx = 1
	f.thumbprint.SetValue("")
	f.via, f.viaMoRef = seed.via, seed.viaMoRef
	// Name, endpoint and provenance are seeded. Username is the only blank
	// required field, so put the cursor there for the next honest input.
	f.cursor = 3
	return f
}

// credOptions is what the Credential row offers. A new context with a keyring
// gets the familiar choice of storing the password or being asked for it.
// Without one, storing is not possible, so the row offers the prompt and the
// three sources that resolve a password where it already lives. An edited
// context offers every source, because changing where its password comes from
// — after the variable or file it used is gone — is a reason to edit it; the
// keyring stays on the list while it works or while it is what was saved.
func (f *contextForm) credOptions() []string {
	unattended := []string{credentials.SchemePrompt, credentials.SchemeEnv, credentials.SchemeFile, credentials.SchemeExec}
	switch {
	case f.editing && (f.noKeyring == "" || f.savedCred.Scheme == credentials.SchemeKeyring):
		return append([]string{credentials.SchemeKeyring}, unattended...)
	case f.editing, f.noKeyring != "":
		return unattended
	default:
		return []string{credentials.SchemeKeyring, credentials.SchemePrompt}
	}
}

// selectCredScheme points the Credential row at scheme, or at the prompt
// when scheme is not on offer.
func (f *contextForm) selectCredScheme(scheme string) {
	opts := f.credOptions()
	f.credIdx = 0
	for i, o := range opts {
		if o == scheme {
			f.credIdx = i
			return
		}
	}
	for i, o := range opts {
		if o == credentials.SchemePrompt {
			f.credIdx = i
		}
	}
}

func (f *contextForm) credScheme() string {
	opts := f.credOptions()
	if f.credIdx < 0 || f.credIdx >= len(opts) {
		return opts[0]
	}
	return opts[f.credIdx]
}

// keyringUnavailable switches the form to the sources that work without a
// keyring. Whatever was selected stays selected when it is still on offer —
// always, for an edited context, whose saved reference is never changed
// because of this machine; only a new form's keyring choice moves to prompt.
func (f *contextForm) keyringUnavailable(reason string) {
	if reason == "" {
		return
	}
	was := f.credScheme()
	f.noKeyring = reason
	f.selectCredScheme(was)
	if f.credScheme() != credentials.SchemeKeyring {
		f.password.SetValue("")
	}
	f.syncFocus()
}

// credSourceRow is the input for an env, file or exec reference.
func (f *contextForm) credSourceRow(scheme string) formRow {
	// The placeholder shows what kind of value belongs here: an empty input
	// next to "Environment variable" reads as a request for the password.
	switch scheme {
	case credentials.SchemeEnv:
		f.credSource.Placeholder = "VSFLEET_PROD_PASSWORD"
		return formRow{label: "Environment variable", kind: rowText, input: &f.credSource,
			hint: "the variable's name, not the password; read from the shell vsfleet starts in"}
	case credentials.SchemeFile:
		f.credSource.Placeholder = "/run/secrets/vcenter"
		return formRow{label: "Password file", kind: rowText, input: &f.credSource,
			hint: "one trailing newline is stripped; protecting the file is up to you"}
	default:
		f.credSource.Placeholder = "/usr/local/bin/vsfleet-credential"
		return formRow{label: "Helper program", kind: rowText, input: &f.credSource,
			hint: "prints the password on stdout; told the context name in VSFLEET_CONTEXT"}
	}
}

// rows lays the form out. It is rebuilt on every keystroke rather than cached,
// because which rows exist depends on the current values of others — the
// SOCKS5 fields only make sense once socks5 is chosen, the thumbprint only
// once thumbprint pinning is.
func (f *contextForm) rows() []formRow {
	rows := make([]formRow, 0, 16)
	if f.editing {
		rows = append(rows, formRow{label: "Name", kind: rowStatic, static: f.name.Value()})
	} else {
		rows = append(rows, formRow{label: "Name", kind: rowText, input: &f.name})
	}
	rows = append(rows, formRow{label: "Endpoint", kind: rowText, input: &f.endpoint, hint: "e.g. https://vcsa.example.internal"})
	if f.via != "" {
		rows = append(rows, formRow{label: "Added from", kind: rowStatic, static: f.via})
	}
	rows = append(rows, formRow{label: "Username", kind: rowText, input: &f.username, hint: "e.g. administrator@vsphere.local"})
	if f.noKeyring != "" {
		rows = append(rows, formRow{label: "OS keyring", kind: rowStatic, static: "not available here — choose where the password comes from",
			hint: f.noKeyring})
	}
	opts := f.credOptions()
	var hint []string
	if opts[0] == credentials.SchemeKeyring {
		hint = append(hint, "keyring stores the password in the OS secret store")
	}
	hint = append(hint, "prompt asks every run")
	if len(opts) > 2 {
		hint = append(hint, "env, file and exec read it when vsfleet connects")
	}
	rows = append(rows, formRow{label: "Credential", kind: rowSelect, options: opts, idx: &f.credIdx,
		hint: strings.Join(hint, "; ")})
	switch scheme := f.credScheme(); scheme {
	case credentials.SchemeKeyring:
		label := "Password"
		if f.editing && f.savedCred.Scheme == credentials.SchemeKeyring {
			label = "Password (blank keeps the stored one)"
		}
		rows = append(rows, formRow{label: label, kind: rowSecret, input: &f.password})
	case credentials.SchemeEnv, credentials.SchemeFile, credentials.SchemeExec:
		rows = append(rows, f.credSourceRow(scheme))
	}
	rows = append(rows, formRow{label: "Route", kind: rowSelect, options: []string{"direct", "socks5", "http", "https"}, idx: &f.transportIdx})
	if f.transportIdx != 0 {
		rows = append(rows,
			formRow{label: "Proxy address", kind: rowText, input: &f.proxyAddr, hint: "host:port"},
			formRow{label: "Proxy username (optional)", kind: rowText, input: &f.proxyUser},
		)
		if strings.TrimSpace(f.proxyUser.Value()) != "" {
			label := "Proxy password"
			if f.editing {
				label = "Proxy password (blank keeps the stored one)"
			}
			rows = append(rows, formRow{label: label, kind: rowSecret, input: &f.proxyPass})
		}
		if f.transportIdx == 1 {
			rows = append(rows, formRow{label: "Resolve DNS at the proxy", kind: rowToggle, flag: &f.remoteDNS,
				hint: "http and https always resolve at the proxy; only socks5 has a choice"})
		}
	}
	rows = append(rows, formRow{label: "Certificate policy", kind: rowSelect, options: []string{"system", "thumbprint", "insecure"}, idx: &f.tlsIdx})
	if f.tlsIdx == 1 {
		rows = append(rows,
			formRow{label: "Thumbprint", kind: rowText, input: &f.thumbprint, hint: "SHA-256 or SHA-1, or use Discover below"},
			formRow{label: "", kind: rowButton, static: "Discover from the server", action: (*Model).formDiscover},
		)
	}
	rows = append(rows,
		formRow{label: "Default datacenter (optional)", kind: rowText, input: &f.datacenter},
		formRow{label: "Make this the current context", kind: rowToggle, flag: &f.setCurrent},
	)
	saveLabel := "Save"
	if f.forceSave {
		saveLabel = "Save anyway"
	}
	rows = append(rows,
		formRow{label: "", kind: rowButton, static: "Test connection", action: (*Model).formTest},
		formRow{label: "", kind: rowButton, static: saveLabel, action: (*Model).formSave},
		formRow{label: "", kind: rowButton, static: "Cancel", action: (*Model).formCancel},
	)
	return rows
}

// syncFocus gives the textinput under the cursor keyboard focus and takes it
// away from every other one: an unfocused textinput.Model silently discards
// key presses, so exactly one row must be focused at a time.
func (f *contextForm) syncFocus() {
	rows := f.rows()
	if f.cursor >= len(rows) {
		f.cursor = len(rows) - 1
	}
	if f.cursor < 0 {
		f.cursor = 0
	}
	for i, r := range rows {
		if r.input == nil {
			continue
		}
		if i == f.cursor {
			r.input.Focus()
		} else {
			r.input.Blur()
		}
	}
}

// input builds the contextops.Input the backend actually acts on.
func (f *contextForm) input() contextops.Input {
	in := contextops.Input{
		Name:       strings.TrimSpace(f.name.Value()),
		Endpoint:   strings.TrimSpace(f.endpoint.Value()),
		Username:   strings.TrimSpace(f.username.Value()),
		Via:        strings.TrimSpace(f.via),
		ViaMoRef:   strings.TrimSpace(f.viaMoRef),
		Datacenter: strings.TrimSpace(f.datacenter.Value()),
		SetCurrent: f.setCurrent,
		Replace:    f.editing,
		// Changing the source here is the operator's explicit choice, so the
		// password the old keyring reference held goes with it.
		DropReplacedKeyring: f.editing,
	}
	if f.editing {
		in.Name = f.origName
	}
	switch f.transportIdx {
	case 1, 2, 3:
		routeType := map[int]string{1: config.TransportSOCKS5, 2: config.TransportHTTPProxy, 3: config.TransportHTTPSProxy}[f.transportIdx]
		in.Transport = config.TransportConfig{
			Type:      routeType,
			Address:   strings.TrimSpace(f.proxyAddr.Value()),
			Username:  strings.TrimSpace(f.proxyUser.Value()),
			RemoteDNS: f.transportIdx == 1 && f.remoteDNS,
		}
		if in.Transport.Username != "" {
			if pw := f.proxyPass.Value(); pw != "" {
				in.ProxyPassword, in.HaveProxyPassword = pw, true
			}
		}
	default:
		in.Transport = config.TransportConfig{Type: config.TransportDirect}
	}
	switch f.tlsIdx {
	case 1:
		in.TLS = config.TLSConfig{Mode: config.TLSThumbprint, Thumbprint: strings.TrimSpace(f.thumbprint.Value())}
	case 2:
		in.TLS = config.TLSConfig{Mode: config.TLSInsecure}
	default:
		in.TLS = config.TLSConfig{Mode: config.TLSSystem}
	}
	switch scheme := f.credScheme(); {
	case scheme == f.savedCred.Scheme && !f.savedCred.NonInteractive():
		// Unchanged keyring or prompt: keep the exact reference, so a custom
		// keyring key is not rewritten to the context's name.
		in.Credential = f.savedCred
		if pw := f.password.Value(); scheme == credentials.SchemeKeyring && pw != "" {
			in.Password, in.HavePassword = pw, true
		}
	case scheme == credentials.SchemePrompt:
		in.Credential = credentials.Ref{Scheme: credentials.SchemePrompt}
	case scheme != credentials.SchemeKeyring:
		// env, file or exec: the reference is the whole of it. There is no
		// password to carry, so the connection test resolves it the way
		// every later run will.
		in.Credential = credentials.Ref{Scheme: scheme, Value: strings.TrimSpace(f.credSource.Value())}
	default:
		in.Credential = credentials.Ref{Scheme: credentials.SchemeKeyring, Value: in.Name}
		if pw := f.password.Value(); pw != "" {
			in.Password, in.HavePassword = pw, true
		}
	}
	return in
}

// validate catches what does not need the network before a test or save is
// attempted, so a missing field is reported instantly rather than after a
// round trip.
func (f *contextForm) validate() string {
	in := f.input()
	switch {
	case in.Name == "":
		return "name is required"
	case in.Endpoint == "":
		return "endpoint is required"
	case in.Username == "":
		return "username is required"
	case in.Transport.Type != config.TransportDirect && in.Transport.Address == "":
		return "proxy address is required"
	case in.TLS.Mode == config.TLSThumbprint && in.TLS.Thumbprint == "":
		return "thumbprint is required in thumbprint mode — use Discover, or switch policy"
	case in.Credential.NonInteractive() && in.Credential.Value == "":
		return strings.ToLower(f.credSourceRow(in.Credential.Scheme).label) + " is required for a " + in.Credential.Scheme + " credential"
	default:
		return ""
	}
}

// showForm opens the supplied add/edit form. Keeping the mode, focus and
// cursor setup here means ordinary and VM-seeded forms behave identically.
func (m *Model) showForm(f *contextForm) tea.Cmd {
	if m.demo {
		if f.editing {
			return nil
		}
		f.note = "Demo preview — connection tests and saving are disabled. Esc cancels."
		m.form, m.mode = f, modeForm
		m.form.syncFocus()
		return textinput.Blink
	}
	m.form = f
	m.mode = modeForm
	m.form.syncFocus()
	if kb, ok := m.backend.(keyringBackend); ok {
		return tea.Batch(textinput.Blink, probeFormKeyring(m.ctx, kb, f))
	}
	return textinput.Blink
}

// enterForm opens the add/edit form. edit is nil for a new context.
func (m *Model) enterForm(edit *contextState) tea.Cmd {
	if m.demo && (edit != nil || len(m.states) == 0) {
		return nil
	}
	return m.showForm(newContextForm(edit))
}

func (m *Model) enterFormSeeded(seed contextSeed) tea.Cmd {
	if m.demo {
		return nil
	}
	return m.showForm(newSeededContextForm(seed))
}

func (m *Model) formTest() tea.Cmd {
	if m.demo {
		return nil
	}
	f := m.form
	if f == nil || f.testing {
		return nil
	}
	if err := f.validate(); err != "" {
		f.err = err
		return nil
	}
	f.testing, f.err, f.note = true, "", ""
	return tea.Batch(testFormContext(m.ctx, m.backend, f.input()), m.spin.Tick)
}

func (m *Model) formSave() tea.Cmd {
	if m.demo {
		return nil
	}
	f := m.form
	if f == nil || f.saving {
		return nil
	}
	if err := f.validate(); err != "" {
		f.err = err
		return nil
	}
	f.saving, f.err = true, ""
	in := f.input()
	in.SaveOnTestFailure = f.forceSave
	return tea.Batch(saveFormContext(m.ctx, m.backend, in), m.spin.Tick)
}

func (m *Model) formDiscover() tea.Cmd {
	if m.demo {
		return nil
	}
	f := m.form
	if f == nil || f.discovering {
		return nil
	}
	cc := contextops.Build(f.input())
	if cc.Endpoint == "" {
		f.err = "set the endpoint before discovering its certificate"
		return nil
	}
	f.discovering, f.err = true, ""
	return tea.Batch(discoverThumbprint(m.ctx, m.backend, cc), m.spin.Tick)
}

// formCancel leaves the form. With no contexts configured there is nothing to
// browse, so cancelling reopens a blank form instead of a screen with no way
// back into it.
func (m *Model) formCancel() tea.Cmd {
	m.form = nil
	if len(m.states) == 0 {
		return m.enterForm(nil)
	}
	m.leaveOverlay()
	return nil
}
