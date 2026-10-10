// Package tests drives vsfleet end to end against a simulated vCenter and a real
// SOCKS5 proxy, so that the parts of the design that are hard to reason about
// — per-context routing, remote DNS, certificate pinning and failure isolation
// — are proven without access to physical VMware infrastructure.
package tests

import (
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vmware/govmomi/simulator"

	"github.com/easonliuuuuu/vsfleet/internal/cli"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

const testPassword = "correct-horse"

// vcenter is a simulated vCenter with the details a context needs.
type vcenter struct {
	URL        string
	Host       string
	Port       string
	Address    string
	Thumbprint string
	// Service is the simulator's request handler, exposed so a test can
	// attach fault injection — an artificial delay on a SOAP method, say —
	// after the server is already up rather than through the tune callback,
	// which runs before the simulator's Service exists.
	Service *simulator.Service
}

func startVCenter(t *testing.T, tune func(*simulator.Model)) *vcenter {
	t.Helper()
	model := simulator.VPX()
	if tune != nil {
		tune(model)
	}
	if err := model.Create(); err != nil {
		t.Fatalf("create simulator model: %v", err)
	}
	t.Cleanup(model.Remove)
	// vcsim serves plain HTTP unless a TLS configuration is set. A real
	// vCenter is always TLS, and the certificate policy is what these tests
	// need to exercise.
	model.Service.TLS = new(tls.Config)
	server := model.Service.NewServer()
	t.Cleanup(server.Close)

	u := *server.URL
	u.User = nil
	u.Path = ""

	vc := &vcenter{URL: u.String(), Host: u.Hostname(), Port: u.Port(), Address: u.Host, Service: model.Service}
	cc := &config.Context{Name: "probe", Endpoint: vc.URL, Username: "u", TLS: config.TLSConfig{Mode: config.TLSInsecure}}
	cc.Normalize()
	sha256, _, _, _, err := vsphere.FetchThumbprint(context.Background(), cc, vsphere.ConnectOptions{})
	if err != nil {
		t.Fatalf("fetch simulator thumbprint: %v", err)
	}
	vc.Thumbprint = sha256
	return vc
}

// runner invokes vsfleet the way a shell would, against a throwaway config file.
type runner struct {
	t          *testing.T
	configPath string
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.WriteString(string(value))
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newRunner(t *testing.T) *runner {
	t.Helper()
	// --config alone leaves the history database and the interface state at
	// their per-user defaults, so a command that opens history would migrate
	// and write the developer's own database. Keep both inside the test.
	dir := t.TempDir()
	t.Setenv("VSFLEET_HISTORY_DB", filepath.Join(dir, "history.db"))
	t.Setenv("VSFLEET_STATE", filepath.Join(dir, "state.json"))
	return &runner{t: t, configPath: filepath.Join(dir, "config.toml")}
}

// run executes one command. stdin feeds interactive prompts, which is how the
// tests supply passwords without touching the operating system keyring.
func (r *runner) run(stdin string, args ...string) (stdout, stderr string, err error) {
	r.t.Helper()
	var out, errOut lockedBuffer
	app := &cli.App{
		In:  strings.NewReader(stdin),
		Out: &out,
		Err: &errOut,
	}
	root := cli.NewRootCommand(app)
	root.SetArgs(append([]string{"--config", r.configPath}, args...))
	defer func() { _ = app.Close(context.Background()) }()
	err = root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestRunnerClosesHistoryDatabase(t *testing.T) {
	r := newRunner(t)
	dbPath := filepath.Join(t.TempDir(), "history.db")

	if _, _, err := r.run("", "--history-db", dbPath, "assessment", "list"); err != nil {
		t.Fatalf("assessment list: %v", err)
	}
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("history database remained open after command: %v", err)
	}
}

// mustRun fails the test if the command does not succeed.
func (r *runner) mustRun(stdin string, args ...string) string {
	r.t.Helper()
	stdout, stderr, err := r.run(stdin, args...)
	if err != nil {
		r.t.Fatalf("vsfleet %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

// addContext registers a direct, thumbprint-pinned context.
func (r *runner) addContext(name string, vc *vcenter, extra ...string) {
	r.t.Helper()
	args := []string{
		"context", "add",
		"--name", name,
		"--endpoint", vc.URL,
		"--username", "operator@vsphere.local",
		"--credential", "prompt",
		"--password-stdin",
		"--tls", "thumbprint",
		"--thumbprint", vc.Thumbprint,
	}
	r.mustRun(testPassword+"\n"+testPassword+"\n", append(args, extra...)...)
}

func TestContextLifecycleAndInventory(t *testing.T) {
	vc := startVCenter(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Cluster = 1
		m.ClusterHost = 2
		m.App = 1
		m.Machine = 3
	})
	r := newRunner(t)
	r.addContext("lab", vc)

	list := r.mustRun("", "context", "list")
	if !strings.Contains(list, "lab") {
		t.Fatalf("context list did not show the context:\n%s", list)
	}
	if !strings.Contains(list, "Direct") {
		t.Errorf("context list did not show the route:\n%s", list)
	}

	test := r.mustRun(testPassword+"\n", "context", "test", "lab")
	for _, want := range []string{"Context: lab", "Connection successful."} {
		if !strings.Contains(test, want) {
			t.Errorf("context test output is missing %q:\n%s", want, test)
		}
	}

	vms := r.mustRun(testPassword+"\n", "vm", "list")
	if !strings.Contains(vms, "poweredOn") {
		t.Errorf("vm list did not show any running VM:\n%s", vms)
	}

	hosts := r.mustRun(testPassword+"\n", "host", "list")
	if !strings.Contains(hosts, "connected") {
		t.Errorf("host list did not show a connected host:\n%s", hosts)
	}

	vapps := r.mustRun(testPassword+"\n", "vapp", "list", "--context", "lab")
	if !strings.Contains(vapps, "APP0") || !strings.Contains(vapps, "PATH") {
		t.Errorf("vApp list did not show the simulated vApp and path:\n%s", vapps)
	}
	vappsJSON := r.mustRun(testPassword+"\n", "vapps", "list", "--context", "lab", "-o", "json")
	if !strings.Contains(vappsJSON, `"direct_vm_count"`) {
		t.Errorf("vApp JSON did not use the domain model:\n%s", vappsJSON)
	}
	searchVApp := r.mustRun(testPassword+"\n", "search", "APP0", "--context", "lab")
	if !strings.Contains(searchVApp, "vapp") {
		t.Errorf("global search did not include vApps:\n%s", searchVApp)
	}

	stores := r.mustRun(testPassword+"\n", "datastore", "list")
	if !strings.Contains(stores, "LocalDS_0") {
		t.Errorf("datastore list did not show the simulated datastore:\n%s", stores)
	}

	doctor := r.mustRun(testPassword+"\n", "doctor", "lab")
	for _, want := range []string{"Configuration valid", "TLS certificate", "Authentication", "API available"} {
		if !strings.Contains(doctor, want) {
			t.Errorf("doctor output is missing the %q stage:\n%s", want, doctor)
		}
	}
}

func TestSearchAcrossVCenters(t *testing.T) {
	a := startVCenter(t, func(m *simulator.Model) { m.Datacenter = 1; m.Machine = 2 })
	b := startVCenter(t, func(m *simulator.Model) { m.Datacenter = 1; m.Machine = 2 })
	r := newRunner(t)
	r.addContext("alpha", a)
	r.addContext("beta", b)

	stdin := strings.Repeat(testPassword+"\n", 4)
	out := r.mustRun(stdin, "search", "DC0_H0_VM0")
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Fatalf("search did not cover both vCenters:\n%s", out)
	}
	if !strings.Contains(out, "2 match(es) across 2 vCenter(s)") {
		t.Errorf("search summary is wrong:\n%s", out)
	}

	kinds := r.mustRun(stdin, "search", "DC0", "--kind", "datastore")
	if strings.Contains(kinds, "\tvm\t") || strings.Contains(kinds, " vm ") {
		t.Errorf("--kind datastore returned virtual machines:\n%s", kinds)
	}
}

// TestFailureIsolation is the behaviour that separates this tool from a set of
// per-vCenter scripts: one broken environment costs one line of output.
func TestFailureIsolation(t *testing.T) {
	good := startVCenter(t, nil)
	r := newRunner(t)
	r.addContext("healthy", good)

	// A context pointing at a port nothing is listening on.
	broken := &vcenter{URL: "https://127.0.0.1:1", Thumbprint: good.Thumbprint}
	r.mustRun(testPassword+"\n", "context", "add",
		"--name", "unreachable",
		"--endpoint", broken.URL,
		"--username", "operator@vsphere.local",
		"--credential", "prompt",
		"--password-stdin",
		"--tls", "insecure",
		"--no-test",
	)

	stdin := strings.Repeat(testPassword+"\n", 4)
	stdout, stderr, err := r.run(stdin, "search", "DC0", "--all-contexts")
	if err != nil {
		t.Fatalf("search failed outright instead of isolating the broken context: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "healthy") {
		t.Errorf("results from the healthy vCenter were lost:\n%s", stdout)
	}
	if !strings.Contains(stderr, "unreachable") {
		t.Errorf("the broken context was not reported:\n%s", stderr)
	}

	stdout, stderr, _ = r.run(stdin, "status")
	if !strings.Contains(stdout, "connected") {
		t.Errorf("status did not show the healthy vCenter as connected:\n%s", stdout)
	}
	if !strings.Contains(stdout, "failed") {
		t.Errorf("status did not show the broken vCenter as failed:\n%s\n%s", stdout, stderr)
	}
}

// TestUIStartsTheInterfaceBeforeResolvingCredentials covers issue #36's
// startup contract. A "prompt" credential with nothing on stdin to answer it
// must not fail before the interface claims the terminal: in this headless
// integration test the expected first failure is the missing TTY itself.
func TestUIStartsTheInterfaceBeforeResolvingCredentials(t *testing.T) {
	r := newRunner(t)
	r.mustRun("", "context", "add",
		"--name", "lab",
		"--endpoint", "https://vcsa.example.internal",
		"--username", "operator@vsphere.local",
		"--credential", "prompt",
		"--tls", "insecure",
		"--no-test",
	)

	_, stderr, err := r.run("", "ui")
	if err == nil {
		t.Fatal("vsfleet ui should have failed because this integration test has no TTY")
	}
	if strings.Contains(err.Error(), "lab") || strings.Contains(stderr, "lab") {
		t.Errorf("credential resolution happened before the interface opened: %v\nstderr: %s", err, stderr)
	}
}

// TestUINamedContextMustExist checks that "vsfleet ui --context" is validated
// before the terminal interface opens, on an empty configuration where no
// name could possibly resolve. With no --context, an empty configuration is
// exactly when the interface is supposed to open — into its own setup form —
// so that path needs a real terminal and is exercised in internal/tui, where
// the model is driven directly rather than through a live TTY.
func TestUINamedContextMustExist(t *testing.T) {
	r := newRunner(t)

	_, _, err := r.run("", "ui", "--context", "does-not-exist")
	if err == nil {
		t.Fatal("vsfleet ui --context does-not-exist on an empty configuration should fail")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should name the context that was asked for, got: %v", err)
	}
}

// TestBareCommandRoutesToTheInterface checks that "vsfleet" with no subcommand
// is wired to open the terminal interface — cobra resolves the empty
// argument list to the root command itself, and the root command has a RunE
// that would run instead of cobra's default of printing help.
//
// It stops at that structural check rather than actually invoking RunE.
// Doing so means starting a real Bubble Tea program, and there is no safe
// way to bound that in an automated test: an earlier version of this test
// gave it a context deadline, which does stop the event loop, but Bubble
// Tea's Windows input reader then blocks tea.Program.Run() itself trying to
// close a console handle out from under a goroutine parked in a blocking
// ReadConsole syscall — Windows will not let that Close return early, no
// matter how the context was cancelled. There is no context short enough to
// avoid it. The interface's actual behaviour is exercised in internal/tui,
// driven directly through its model rather than a live terminal.
func TestBareCommandRoutesToTheInterface(t *testing.T) {
	app := &cli.App{}
	root := cli.NewRootCommand(app)

	cmd, _, err := root.Find(nil)
	if err != nil {
		t.Fatalf("Find(nil): %v", err)
	}
	if cmd != root {
		t.Fatalf("a bare invocation should resolve to the root command itself, resolved to %q instead", cmd.Name())
	}
	if root.RunE == nil {
		t.Fatal("root command has no RunE: a bare \"vsfleet\" would print help instead of opening the interface")
	}
}

// TestUnknownSubcommandIsRejected checks that a real typo is still reported
// as such rather than being silently swallowed by the interface launch that
// a bare "vsfleet" now performs.
func TestUnknownSubcommandIsRejected(t *testing.T) {
	r := newRunner(t)

	_, _, err := r.run("", "statas")
	if err == nil {
		t.Fatal("an unknown subcommand should fail")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("expected an unknown-command error, got: %v", err)
	}
}
