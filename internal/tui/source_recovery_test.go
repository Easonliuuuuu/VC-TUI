package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// sourceMiss is the error a real provider returns for ref when its source
// cannot answer, wrapped the way the connection path wraps it. Building it
// through the provider keeps these tests honest about the wording an operator
// actually sees.
func sourceMiss(t *testing.T, cc *config.Context) error {
	t.Helper()
	var p credentials.Provider
	switch cc.Credential.Scheme {
	case credentials.SchemeEnv:
		p = credentials.NewEnv()
	case credentials.SchemeFile:
		p = credentials.NewFile()
	default:
		p = credentials.NewExec()
	}
	_, err := p.Get(context.Background(), cc.Credential)
	if err == nil {
		t.Fatalf("%s answered; the fixture expects it to be missing", cc.Credential)
	}
	return fmt.Errorf("prod: context %q: %w", cc.Name, err)
}

// missingSourceBackend is one context, prod, whose password source is gone:
// loading fails with that source's error and so does the diagnosis, which
// stops at "Credential available" before any network stage, as the real walk
// does.
func missingSourceBackend(t *testing.T, ref string) (*fakeBackend, *config.Context) {
	t.Helper()
	cc := ctx("prod", "https://vcsa.prod.internal")
	r, err := credentials.ParseRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	cc.Credential = r
	miss := sourceMiss(t, cc)
	return &fakeBackend{
		contexts: []*config.Context{cc},
		failures: map[string]error{"prod": miss},
		diagnoses: map[string]*vsphere.Diagnosis{"prod": {
			Context: "prod", Endpoint: cc.Endpoint,
			Checks: []vsphere.Check{
				{Name: "Configuration valid", Status: vsphere.CheckPass, Detail: cc.Endpoint},
				{Name: "Credential available", Status: vsphere.CheckFail, Err: miss},
				{Name: "Route configured", Status: vsphere.CheckSkip},
				{Name: "Authentication", Status: vsphere.CheckSkip},
			},
		}},
	}, cc
}

func TestFailureLineNamesAMissingPasswordSource(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "lab.pw")
	for _, tc := range []struct {
		ref, want string
	}{
		{"env:VSFLEET_TUI_TEST_ABSENT", "✕ prod: VSFLEET_TUI_TEST_ABSENT is not set · d diagnose"},
		{"file:" + gone, "✕ prod: password file " + gone + " does not exist · d diagnose"},
		{"exec:" + filepath.Join(t.TempDir(), "vsfleet-credential"), "✕ prod: password helper vsfleet-credential not found · d diagnose"},
	} {
		t.Run(strings.SplitN(tc.ref, ":", 2)[0], func(t *testing.T) {
			b, _ := missingSourceBackend(t, tc.ref)
			m := newTestModel(t, b, Options{Current: "prod"})
			m.width = 200 // the whole line, untruncated
			out := m.View()
			if !strings.Contains(out, tc.want) {
				t.Errorf("failure line missing %q:\n%s", tc.want, out)
			}
			if strings.Contains(out, "connection failed") {
				t.Errorf("a password that never left its source is reported as a connection failure:\n%s", out)
			}
		})
	}
}

// Every other failure still says "connection failed": its cause is somewhere
// on the network path, and only the diagnosis can say where.
func TestFailureLineKeepsConnectionFailedForOtherCauses(t *testing.T) {
	b := twoHealthy()
	b.failures = map[string]error{"prod": fmt.Errorf("prod: dial tcp: no route to host")}
	m := newTestModel(t, b, Options{Current: "prod"})
	if out := m.View(); !strings.Contains(out, "✕ prod: connection failed · d diagnose") {
		t.Errorf("a network failure lost its usual line:\n%s", out)
	}
}

func TestDiagnosisSaysHowToFixEachSource(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "lab.pw")
	helper := filepath.Join(t.TempDir(), "vsfleet-credential")
	for _, tc := range []struct {
		ref  string
		want []string
	}{
		{"env:VSFLEET_TUI_TEST_ABSENT", []string{"reads VSFLEET_TUI_TEST_ABSENT from the shell it was started in", "start vsfleet again"}},
		{"file:" + gone, []string{"Put the password back in " + gone, "press r"}},
		{"exec:" + helper, []string{"VSFLEET_CONTEXT=prod " + helper, "press r"}},
	} {
		t.Run(strings.SplitN(tc.ref, ":", 2)[0], func(t *testing.T) {
			b, _ := missingSourceBackend(t, tc.ref)
			m := newTestModel(t, b, Options{Current: "prod"})
			m.width = 200
			press(t, m, "d")
			if m.mode != modeDoctor {
				t.Fatalf("'d' did not open the diagnosis panel, mode is %v", m.mode)
			}
			// The block wraps to the panel's width; compare the words.
			out := strings.Join(strings.Fields(m.View()), " ")
			want := append([]string{"How to fix", "press e to change where this context's password comes from", "e change password source"}, tc.want...)
			for _, w := range want {
				if !strings.Contains(out, w) {
					t.Errorf("diagnosis is missing %q:\n%s", w, out)
				}
			}
			// A restart is the only fix for env; offering r as the fix
			// would send the operator round in a loop.
			if strings.HasPrefix(tc.ref, "env:") && strings.Contains(out, "then press r") {
				t.Errorf("the env hint suggests a retry that cannot work:\n%s", out)
			}
		})
	}
}

func TestDiagnosisOffersNoFixForANetworkFailure(t *testing.T) {
	b := twoHealthy()
	b.diagnoses = map[string]*vsphere.Diagnosis{"prod": {
		Context: "prod",
		Checks:  []vsphere.Check{{Name: "TCP connection", Status: vsphere.CheckFail, Err: fmt.Errorf("no route to host")}},
	}}
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "d")
	out := m.View()
	if strings.Contains(out, "How to fix") || strings.Contains(out, "change password source") {
		t.Errorf("a network failure was given a password-source fix:\n%s", out)
	}
}

func TestEditFromDiagnosisChangesTheSource(t *testing.T) {
	b, cc := missingSourceBackend(t, "env:VSFLEET_TUI_TEST_ABSENT")
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "d", "e")
	settleForm(m)
	if m.mode != modeForm || m.form == nil || !m.form.editing || m.form.origName != "prod" {
		t.Fatalf("'e' on the diagnosis did not open an edit of prod (mode %v)", m.mode)
	}
	f := m.form
	// The saved source is selected and filled in, so saving without a
	// change keeps it.
	if got := f.credScheme(); got != credentials.SchemeEnv {
		t.Errorf("credential row opened on %q, want env", got)
	}
	if got := f.credSource.Value(); got != "VSFLEET_TUI_TEST_ABSENT" {
		t.Errorf("source input = %q, want the saved variable name", got)
	}
	if got := strings.Join(f.credOptions(), ","); got != "keyring,prompt,env,file,exec" {
		t.Errorf("an edit offers %s, want every source", got)
	}

	f.selectCredScheme(credentials.SchemeExec)
	f.credSource.SetValue("/usr/local/bin/vsfleet-credential")
	b.failures = nil // the new source answers
	drive(t, m, m.formSave())

	if m.form != nil {
		t.Fatalf("the edit did not save: %q", m.form.err)
	}
	if m.mode != modeBrowse {
		t.Errorf("saving an edit opened from the diagnosis should land on the main screen, mode is %v", m.mode)
	}
	saved, err := findContext(b.contexts, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.Credential.String(); got != "exec:/usr/local/bin/vsfleet-credential" {
		t.Errorf("saved credential = %q, want the exec reference", got)
	}
	if saved.Endpoint != cc.Endpoint {
		t.Errorf("the edit changed the endpoint to %q", saved.Endpoint)
	}
}

func TestEditWithoutChangesKeepsTheExactReference(t *testing.T) {
	for _, saved := range []credentials.Ref{
		{Scheme: credentials.SchemeKeyring, Value: "shared-lab-secret"}, // not the context's name
		{Scheme: credentials.SchemePrompt, Value: "lab"},
		{Scheme: credentials.SchemeFile, Value: "/run/secrets/vcenter"},
	} {
		t.Run(saved.Scheme, func(t *testing.T) {
			cc := ctx("prod", "https://vcsa.prod.internal")
			cc.Credential = saved
			f := newContextForm(&contextState{cc: cc})
			in := f.input()
			if in.Credential != saved {
				t.Errorf("an untouched edit saves %q, want %q", in.Credential, saved)
			}
			if !in.DropReplacedKeyring {
				t.Error("an edit does not ask for a replaced keyring entry to be removed")
			}
		})
	}
}

func TestCredentialSourceInputShowsAnExample(t *testing.T) {
	f := newContextForm(nil)
	f.keyringUnavailable(errNoSecretService.Error())
	for scheme, want := range map[string]string{
		credentials.SchemeEnv:  "VSFLEET_PROD_PASSWORD",
		credentials.SchemeFile: "/run/secrets/vcenter",
		credentials.SchemeExec: "/usr/local/bin/vsfleet-credential",
	} {
		f.selectCredScheme(scheme)
		f.rows()
		if f.credSource.Placeholder != want {
			t.Errorf("%s placeholder = %q, want %q", scheme, f.credSource.Placeholder, want)
		}
	}
}
