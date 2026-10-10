package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/tui"
	"github.com/easonliuuuuu/vsfleet/internal/uistate"
)

func TestDemoWelcomeHonorsOptOutWithoutRememberedState(t *testing.T) {
	for _, env := range []map[string]string{nil, {EnvNoWelcome: "1"}, {"CI": "true"}} {
		got := demoWelcome(func(key string) string { return env[key] })
		want := tui.WelcomeDemo
		if len(env) != 0 {
			want = tui.WelcomeNone
		}
		if got.Kind != want {
			t.Errorf("env %v: welcome=%v, want %v", env, got.Kind, want)
		}
	}
}

// runDemoCommand executes "vsfleet demo" with buffers standing in for the
// terminal. Run refuses to start Bubble Tea without a TTY, so every one of
// these tests ends in that error — which is exactly what makes it a probe of
// the setup path, since the refusal happens after all of it.
func runDemoCommand(t *testing.T) (*App, string, error) {
	t.Helper()
	var in, out, errOut bytes.Buffer
	a := &App{In: &in, Out: &out, Err: &errOut}
	root := NewRootCommand(a)
	root.SetArgs([]string{"demo"})
	err := root.ExecuteContext(context.Background())
	return a, errOut.String(), err
}

// scratchPaths points the configuration and remembered-screen files at a
// temporary directory so the test can prove the demo wrote neither.
func scratchPaths(t *testing.T) (configPath, statePath string) {
	t.Helper()
	dir := t.TempDir()
	configPath = filepath.Join(dir, "config.toml")
	statePath = filepath.Join(dir, "state.json")
	t.Setenv(config.EnvConfigPath, configPath)
	t.Setenv(uistate.EnvStatePath, statePath)
	return configPath, statePath
}

func TestDemoCommandIsRegistered(t *testing.T) {
	root := NewRootCommand(&App{})
	cmd, _, err := root.Find([]string{"demo"})
	if err != nil {
		t.Fatalf("finding the demo command: %v", err)
	}
	if cmd.Name() != "demo" {
		t.Fatalf("expected the demo command, got %q", cmd.Name())
	}
}

// The demo's promise is that it leaves the operator's machine alone. Every
// side-effecting dependency on App is a lazy accessor, so each one still
// being nil after the run is the strongest single signal that the demo
// touched no configuration, no keyring, no session and no history database.
func TestDemoLoadsNothingFromTheMachine(t *testing.T) {
	scratchPaths(t)
	keyring.MockInit()

	a, _, err := runDemoCommand(t)
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("expected the no-terminal refusal, got %v", err)
	}

	if a.cfg != nil {
		t.Error("demo loaded the configuration file")
	}
	if a.resolver != nil {
		t.Error("demo built the credential resolver")
	}
	if a.mgr != nil {
		t.Error("demo built the session manager")
	}
	if a.history != nil {
		t.Error("demo opened the history database")
	}
}

func TestDemoWritesNothingToDisk(t *testing.T) {
	configPath, statePath := scratchPaths(t)
	keyring.MockInit()

	if _, _, err := runDemoCommand(t); err == nil {
		t.Fatal("expected the no-terminal refusal")
	}

	for _, path := range []string{configPath, statePath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("demo wrote %s (stat error: %v)", path, err)
		}
	}
}

func TestDemoIgnoresAnExistingConfiguration(t *testing.T) {
	configPath, _ := scratchPaths(t)
	keyring.MockInit()
	// A real operator running the demo almost certainly has contexts already.
	// The demo must show its own estate regardless, and must not read theirs.
	if err := os.WriteFile(configPath, []byte(`current_context = "live"

[[contexts]]
name = "live"
endpoint = "https://vcsa.internal"
username = "administrator@vsphere.local"
credential = "keyring:live"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	a, _, err := runDemoCommand(t)
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("expected the no-terminal refusal, got %v", err)
	}
	if a.cfg != nil {
		t.Error("demo read the operator's configuration file")
	}
	if a.resolver != nil {
		t.Error("demo built the credential resolver, which is what reads the keyring")
	}
}
