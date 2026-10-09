package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/easonliuuuuu/vsfleet/internal/config"
)

// runWizard answers the add-context wizard from a script, one line per
// question, and returns the context it filled in and what it printed.
func runWizard(t *testing.T, f *contextFlags, answers ...string) (*config.Context, string) {
	t.Helper()
	var out bytes.Buffer
	a := &App{In: strings.NewReader(strings.Join(answers, "\n") + "\n"), Out: &out, Err: &out}
	cc := &config.Context{}
	if err := runAddWizard(context.Background(), a, cc, f); err != nil {
		t.Fatalf("wizard: %v\n%s", err, out.String())
	}
	return cc, out.String()
}

func withoutKeyring(t *testing.T) {
	t.Helper()
	keyring.MockInitWithError(errors.New("The name org.freedesktop.secrets was not provided by any .service files"))
	t.Cleanup(keyring.MockInit)
}

func TestAddWizardLeavesTheKeyringDefaultWhenOneIsAvailable(t *testing.T) {
	keyring.MockInit()
	cc, out := runWizard(t, &contextFlags{},
		"lab", "https://vcsa.lab", "ro@vsphere.local", // name, endpoint, username
		"direct", "insecure", "", // route, certificate policy, datacenter
	)
	if !cc.Credential.IsZero() {
		t.Errorf("credential = %q, want unset so Save defaults it to keyring:<name>", cc.Credential)
	}
	if strings.Contains(out, "Password source") {
		t.Errorf("asked for a password source although the keyring works:\n%s", out)
	}
}

func TestAddWizardOffersEverySourceWithoutAKeyring(t *testing.T) {
	cases := []struct {
		answers []string
		want    string
	}{
		{answers: []string{""}, want: "prompt"}, // the default
		{answers: []string{"prompt"}, want: "prompt"},
		{answers: []string{"env", "VSFLEET_LAB_PASSWORD"}, want: "env:VSFLEET_LAB_PASSWORD"},
		{answers: []string{"file", "/run/secrets/vcenter"}, want: "file:/run/secrets/vcenter"},
		{answers: []string{"exec", "/usr/local/bin/vsfleet-credential"}, want: "exec:/usr/local/bin/vsfleet-credential"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			withoutKeyring(t)
			answers := append([]string{"lab", "https://vcsa.lab", "ro@vsphere.local"}, tc.answers...)
			answers = append(answers, "direct", "insecure", "")
			cc, out := runWizard(t, &contextFlags{}, answers...)
			if got := cc.Credential.String(); got != tc.want {
				t.Errorf("credential = %q, want %q\n%s", got, tc.want, out)
			}
			// The operator is told why, not just handed a different default.
			for _, want := range []string{"No OS keyring", "org.freedesktop.secrets", "env", "file", "exec"} {
				if !strings.Contains(out, want) {
					t.Errorf("wizard output does not mention %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "Password for") {
				t.Errorf("the wizard asked for the password itself:\n%s", out)
			}
		})
	}
}

func TestAddWizardRejectsAnUnknownSourceAndAsksAgain(t *testing.T) {
	withoutKeyring(t)
	cc, out := runWizard(t, &contextFlags{},
		"lab", "https://vcsa.lab", "ro@vsphere.local",
		"keyring", "vault", "env", "LAB_PW", // keyring is not offered here; neither is vault
		"direct", "insecure", "",
	)
	if got := cc.Credential.String(); got != "env:LAB_PW" {
		t.Errorf("credential = %q, want env:LAB_PW\n%s", got, out)
	}
	if !strings.Contains(out, "choose one of: prompt, env, file, exec") {
		t.Errorf("an unknown source was not rejected with the choices:\n%s", out)
	}
}

func TestAddWizardDoesNotAskForASourceTheFlagsAlreadyGave(t *testing.T) {
	for name, f := range map[string]*contextFlags{
		"--credential":     {credential: "exec:/usr/local/bin/helper"},
		"--password-stdin": {passwordStdin: true},
	} {
		t.Run(name, func(t *testing.T) {
			withoutKeyring(t)
			cc, out := runWizard(t, f, "lab", "https://vcsa.lab", "ro@vsphere.local", "direct", "insecure", "")
			if strings.Contains(out, "Password source") {
				t.Errorf("asked for a password source although %s chose one:\n%s", name, out)
			}
			if !cc.Credential.IsZero() {
				t.Errorf("the wizard set credential %q over %s", cc.Credential, name)
			}
		})
	}
}
