package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
)

// keyringFake is a fakeBackend that also answers whether a keyring is
// usable, the way the production backend does.
type keyringFake struct {
	*fakeBackend
	keyringErr error
	probes     int
}

func (b *keyringFake) KeyringAvailable(context.Context) error {
	b.probes++
	return b.keyringErr
}

// newKeyringTestModel is newTestModel for a keyringFake. With no contexts
// configured, the model opens the add-context form on start-up.
func newKeyringTestModel(t *testing.T, b *keyringFake) *Model {
	t.Helper()
	m := New(context.Background(), b, Options{RefreshInterval: -1, Handoff: &fakeHandoff{}})
	m.width, m.height = 140, 30
	m.filter.Cursor.SetMode(cursor.CursorStatic)
	drive(t, m, m.Init())
	settleForm(m)
	if m.form == nil {
		t.Fatal("no contexts configured, but the add-context form did not open")
	}
	return m
}

var errNoSecretService = errors.New("system keyring unavailable: The name org.freedesktop.secrets was not provided by any .service files")

func TestDemoFormPreviewNeverProbesTheKeyring(t *testing.T) {
	b := &keyringFake{fakeBackend: twoHealthy()}
	m := New(context.Background(), b, Options{Current: "prod", Demo: true, RefreshInterval: -1})
	m.width, m.height = 140, 30
	press(t, m, "c", "n")
	settleForm(m)
	if m.form == nil || b.probes != 0 {
		t.Fatalf("demo preview form=%v keyring probes=%d", m.form, b.probes)
	}
}

func TestFormKeepsTheKeyringWhenItWorks(t *testing.T) {
	b := &keyringFake{fakeBackend: &fakeBackend{}}
	m := newKeyringTestModel(t, b)

	if b.probes != 1 {
		t.Errorf("keyring probed %d times opening a new form, want 1", b.probes)
	}
	if hasLabel(m.form.rows(), "OS keyring") {
		t.Error("a working keyring was reported as unavailable")
	}
	if got := m.form.credOptions(); strings.Join(got, ",") != "keyring,prompt" {
		t.Errorf("credential options = %v, want keyring and prompt", got)
	}
}

func TestFormOffersEverySourceWithoutAKeyring(t *testing.T) {
	b := &keyringFake{fakeBackend: &fakeBackend{}, keyringErr: errNoSecretService}
	m := newKeyringTestModel(t, b)
	f := m.form

	if got := f.credOptions(); strings.Join(got, ",") != "prompt,env,file,exec" {
		t.Fatalf("credential options = %v, want prompt, env, file and exec", got)
	}
	view := m.View()
	for _, want := range []string{"OS keyring", "not available here", "[prompt]", "env", "file", "exec"} {
		if !strings.Contains(view, want) {
			t.Errorf("form does not show %q:\n%s", want, view)
		}
	}
	// The reason is there for whoever wants it, on the row that says so.
	for i, r := range f.rows() {
		if r.label == "OS keyring" {
			f.cursor = i
		}
	}
	if view := m.View(); !strings.Contains(view, "org.freedesktop.secrets") {
		t.Errorf("the keyring row does not explain why:\n%s", view)
	}
	// Prompt, the default here, has nothing to store and nothing to type.
	if hasLabel(f.rows(), "Password") {
		t.Error("a password field is offered with no keyring to keep it in")
	}
}

func TestFormBuildsEachSourceWithoutAKeyring(t *testing.T) {
	cases := []struct {
		idx    int
		value  string
		label  string
		want   string
		errMsg string
	}{
		{idx: 1, value: "VSFLEET_PROD_PASSWORD", label: "Environment variable", want: "env:VSFLEET_PROD_PASSWORD", errMsg: "environment variable is required"},
		{idx: 2, value: "/run/secrets/vcenter", label: "Password file", want: "file:/run/secrets/vcenter", errMsg: "password file is required"},
		{idx: 3, value: "/usr/local/bin/vsfleet-credential", label: "Helper program", want: "exec:/usr/local/bin/vsfleet-credential", errMsg: "helper program is required"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			f := newContextForm(nil)
			f.name.SetValue("prod")
			f.endpoint.SetValue("https://vcsa.example.internal")
			f.username.SetValue("operator@vsphere.local")
			f.keyringUnavailable(errNoSecretService.Error())
			f.credIdx = tc.idx

			if !hasLabel(f.rows(), tc.label) {
				t.Fatalf("no %q row for %s", tc.label, tc.want)
			}
			if got := f.validate(); !strings.Contains(got, tc.errMsg) {
				t.Errorf("an empty source validates as %q, want it to mention %q", got, tc.errMsg)
			}

			f.credSource.SetValue(tc.value)
			if got := f.validate(); got != "" {
				t.Fatalf("a complete form failed validation: %s", got)
			}
			in := f.input()
			if in.Credential.String() != tc.want {
				t.Errorf("credential = %q, want %q", in.Credential, tc.want)
			}
			// The connection test resolves the source itself; carrying a
			// password would test something other than what is saved.
			if in.HavePassword {
				t.Error("the form carried a password for a source that stores none")
			}
		})
	}
}

func TestFormSavesTheChosenSourceWithoutAKeyring(t *testing.T) {
	b := &keyringFake{fakeBackend: &fakeBackend{}, keyringErr: errNoSecretService}
	m := newKeyringTestModel(t, b)
	f := m.form
	f.name.SetValue("prod")
	f.endpoint.SetValue("https://vcsa.example.internal")
	f.username.SetValue("operator@vsphere.local")
	f.credIdx = 3 // exec
	f.credSource.SetValue("/usr/local/bin/vsfleet-credential")

	drive(t, m, m.formSave())

	if len(b.contexts) != 1 {
		t.Fatalf("saved %d contexts, want 1 (form error: %q)", len(b.contexts), f.err)
	}
	if got := b.contexts[0].Credential.String(); got != "exec:/usr/local/bin/vsfleet-credential" {
		t.Errorf("saved credential = %q, want the exec reference", got)
	}
}

func TestFormEditKeepsItsCredentialWithoutAKeyring(t *testing.T) {
	for _, saved := range []credentials.Ref{
		{Scheme: credentials.SchemeKeyring, Value: "prod-secret"},
		{Scheme: credentials.SchemeExec, Value: "/usr/local/bin/vsfleet-credential"},
	} {
		t.Run(saved.Scheme, func(t *testing.T) {
			cc := &config.Context{
				Name: "prod", Endpoint: "https://vcsa.example.internal", Username: "operator@vsphere.local",
				Credential: saved,
			}
			b := &keyringFake{fakeBackend: &fakeBackend{contexts: []*config.Context{cc}}, keyringErr: errNoSecretService}
			m := New(context.Background(), b, Options{RefreshInterval: -1, Handoff: &fakeHandoff{}})
			drive(t, m, m.enterForm(&contextState{cc: cc}))

			if b.probes != 1 {
				t.Errorf("editing probed the keyring %d times, want 1", b.probes)
			}
			// The answer changes what is on offer, never what was saved.
			if got := m.form.input().Credential; got != saved {
				t.Errorf("an unavailable keyring rewrote the credential to %q, want %q", got, saved)
			}
		})
	}
}

func TestFormIgnoresAKeyringAnswerForAnotherForm(t *testing.T) {
	b := &keyringFake{fakeBackend: &fakeBackend{}}
	m := newKeyringTestModel(t, b)
	stale := newContextForm(nil)

	drive(t, m, discard(m.Update(formKeyringMsg{form: stale, err: errNoSecretService})))

	if m.form.noKeyring != "" {
		t.Error("an answer meant for a closed form changed the open one")
	}
}
