package contextops

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

const storedFixture = "stored-credential-fixture" // not a credential

// keyringWith returns a resolver whose keyring is in memory and already holds
// a password under each key.
func keyringWith(keys ...string) (*credentials.Resolver, *credentials.Static) {
	creds := map[string]credentials.Credential{}
	for _, k := range keys {
		creds[k] = credentials.Credential{Password: storedFixture}
	}
	kr := credentials.NewStatic(credentials.SchemeKeyring, creds)
	return credentials.NewResolver(kr, credentials.NewEnv()), kr
}

func stored(kr *credentials.Static, key string) bool {
	_, err := kr.Get(context.Background(), credentials.Ref{Scheme: credentials.SchemeKeyring, Value: key})
	return err == nil
}

func newConfig(t *testing.T, contexts ...Input) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range contexts {
		cc := Build(in)
		if err := cfg.Add(cc, false); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

func input(name string, ref credentials.Ref) Input {
	return Input{
		Name: name, Endpoint: "https://vcsa." + name + ".internal", Username: "operator@vsphere.local",
		Transport: config.TransportConfig{Type: config.TransportDirect}, TLS: config.TLSConfig{Mode: config.TLSInsecure},
		Credential: ref,
	}
}

var (
	envRef = credentials.Ref{Scheme: credentials.SchemeEnv, Value: "VSFLEET_PROD_PASSWORD"}
	prodKR = credentials.Ref{Scheme: credentials.SchemeKeyring, Value: "prod"}
)

func save(t *testing.T, cfg *config.Config, res *credentials.Resolver, in Input) *Result {
	t.Helper()
	r, err := Save(context.Background(), cfg, res, vsphere.ConnectOptions{}, in, false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return r
}

func TestSaveDropsTheKeyringEntryAnEditMovedAwayFrom(t *testing.T) {
	res, kr := keyringWith("prod")
	cfg := newConfig(t, input("prod", prodKR))

	in := input("prod", envRef)
	in.Replace, in.DropReplacedKeyring = true, true
	r := save(t, cfg, res, in)

	if stored(kr, "prod") {
		t.Error("the old keyring password survived an edit that no longer refers to it")
	}
	if r.Dropped != prodKR || r.DropWarning != nil {
		t.Errorf("Dropped = %v, DropWarning = %v; want %v and no warning", r.Dropped, r.DropWarning, prodKR)
	}
}

func TestSaveKeepsAKeyringEntryInEveryOtherCase(t *testing.T) {
	shared := credentials.Ref{Scheme: credentials.SchemeKeyring, Value: "lab-shared"}
	cases := []struct {
		name     string
		existing []Input
		edit     Input
		key      string
		flag     bool
	}{
		// "context add --force" replaces without asking for the cleanup:
		// deleting a stored password stays an explicit choice there.
		{"not asked", []Input{input("prod", prodKR)}, input("prod", envRef), "prod", false},
		{"still referenced", []Input{input("prod", prodKR)}, input("prod", prodKR), "prod", true},
		{"shared with another context", []Input{input("prod", shared), input("dr", shared)}, input("prod", envRef), "lab-shared", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, kr := keyringWith(tc.key)
			cfg := newConfig(t, tc.existing...)
			in := tc.edit
			in.Replace, in.DropReplacedKeyring = true, tc.flag
			r := save(t, cfg, res, in)
			if !stored(kr, tc.key) {
				t.Errorf("keyring entry %q was removed", tc.key)
			}
			if !r.Dropped.IsZero() {
				t.Errorf("Dropped = %v, want nothing", r.Dropped)
			}
		})
	}
}

// failingDelete is a keyring that cannot delete anything.
type failingDelete struct{ *credentials.Static }

func (failingDelete) Delete(context.Context, credentials.Ref) error {
	return errors.New("secret service is locked")
}

func TestSaveKeepsTheEditWhenTheOldEntryCannotBeRemoved(t *testing.T) {
	_, kr := keyringWith("prod")
	res := credentials.NewResolver(failingDelete{kr}, credentials.NewEnv())
	cfg := newConfig(t, input("prod", prodKR))

	in := input("prod", envRef)
	in.Replace, in.DropReplacedKeyring = true, true
	r := save(t, cfg, res, in)

	if r.DropWarning == nil {
		t.Error("a failed removal was not reported")
	}
	got, err := cfg.Context("prod")
	if err != nil {
		t.Fatal(err)
	}
	if got.Credential != envRef {
		t.Errorf("saved credential = %v, want %v: a failed cleanup must not undo the edit", got.Credential, envRef)
	}
}
