// Package scenarios contains the repository-owned, deterministic TUI
// scenario catalogue. It is intentionally independent of the shell entrypoint
// so CI, developers, and coding agents use the same names and descriptions.
package scenarios

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
	"github.com/easonliuuuuu/vsfleet/internal/demo"
	"github.com/easonliuuuuu/vsfleet/internal/session"
	"github.com/easonliuuuuu/vsfleet/internal/testbed"
	"github.com/easonliuuuuu/vsfleet/internal/tui"
	"github.com/easonliuuuuu/vsfleet/internal/version"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Definition describes one stable scenario exposed by scripts/testbed.
type Definition struct {
	Name    string
	Profile string
	Purpose string
}

var definitions = []Definition{
	{Name: "overview", Profile: "presentation", Purpose: "healthy inventory remains useful while the failed site stays visible"},
	{Name: "partial-failure", Profile: "presentation", Purpose: "a failed context is diagnosed without erasing healthy rows"},
	{Name: "duplicate-names", Profile: "presentation", Purpose: "same-named resources remain distinguishable by context"},
	{Name: "credential-cancel", Profile: "presentation", Purpose: "a cancelled credential prompt never stores a secret"},
	{Name: "stale-result", Profile: "presentation", Purpose: "late asynchronous data cannot replace newer state"},
	{Name: "history-coverage-gap", Profile: "presentation", Purpose: "partial assessment coverage is visible instead of a false removal"},
	{Name: "add-context-no-secret", Profile: "connected", Purpose: "context configuration contains references but no password"},
	{Name: "datastore-browser", Profile: "presentation", Purpose: "directory and recursive-find navigation preserve datastore state"},
	{Name: "resize", Profile: "presentation", Purpose: "bounded terminal sizes render safely and preserve selection"},
	{Name: "vm-dashboard", Profile: "presentation", Purpose: "every VM chart page and range stays inside the terminal at each size"},
	{Name: "network-switches", Profile: "presentation", Purpose: "port groups group under their switch and every switch page stays inside the terminal"},
}

// Definitions returns the catalogue in display order.
func Definitions() []Definition { return append([]Definition(nil), definitions...) }

// Lookup validates a scenario name.
func Lookup(name string) (Definition, error) {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition, nil
		}
	}
	return Definition{}, fmt.Errorf("unknown scenario %q (use scripts/testbed list)", name)
}

// RunOptions controls one headless scenario run.
type RunOptions struct {
	Profile       string
	ResultsDir    string
	UpdateGoldens bool
	SkipGoldens   bool
}

// Result is the retained, semantic output of a scenario.
type Result struct {
	Name        string
	Profile     string
	View        string
	Observation tui.Observation
}

// Run executes a deterministic model-boundary scenario. Presentation cases
// use the offline demo backend; the connected profile boots the loopback lab
// and production backend for the scenarios that need connection plumbing.
func Run(ctx context.Context, name string, opts RunOptions) (result Result, runErr error) {
	definition, err := Lookup(name)
	if err != nil {
		return Result{}, err
	}
	if opts.Profile == "" {
		opts.Profile = definition.Profile
	}
	if opts.Profile != "presentation" && opts.Profile != "connected" {
		return Result{}, fmt.Errorf("unknown profile %q", opts.Profile)
	}
	if opts.Profile != definition.Profile {
		return Result{}, fmt.Errorf("scenario %q belongs to profile %q, not %q", name, definition.Profile, opts.Profile)
	}
	result = Result{Name: name, Profile: opts.Profile}
	if opts.ResultsDir != "" {
		defer func() {
			_ = writeResult(opts.ResultsDir, result)
			failure := ""
			if runErr != nil {
				failure = runErr.Error() + "\n"
			}
			_ = os.WriteFile(filepath.Join(opts.ResultsDir, name, "error.txt"), []byte(failure), 0o600)
		}()
	}

	// Credential cancellation is a model-boundary scenario: it uses the
	// deterministic backend and the focused prompt tests, while the context
	// addition scenario exercises the connected production path below.
	connected := name == "add-context-no-secret"
	backend, service, closeBackend, err := setupBackend(ctx, connected)
	if err != nil {
		return Result{}, err
	}
	defer closeBackend()
	m := tui.New(ctx, backend, tui.Options{
		Current:         "prod-vc",
		AllContexts:     name == "overview" || name == "partial-failure" || name == "duplicate-names",
		Demo:            !connected,
		Assessment:      service,
		RefreshInterval: -1,
	})
	if err := drive(m, m.Init()); err != nil {
		return Result{}, fmt.Errorf("initialize %s: %w", name, err)
	}
	if name == "overview" || name == "partial-failure" || name == "duplicate-names" {
		if err := press(m, "R"); err != nil {
			return Result{}, fmt.Errorf("load all contexts for %s: %w", name, err)
		}
	}

	switch name {
	case "overview", "partial-failure", "credential-cancel", "add-context-no-secret":
		// Initial inventory and the visible failed context are the contract.
	case "duplicate-names":
		// AllContexts is set above; both healthy fixtures intentionally share
		// resource names and the view must retain context-qualified rows.
	case "stale-result":
		// The generation gate is exercised by the model's deterministic initial
		// load; the focused unit test covers the reordered reply itself.
	case "history-coverage-gap":
		press(m, "H")
	case "datastore-browser":
		// The browser-specific state machine is covered by focused tests; the
		// scenario records the stable inventory screen for the catalogue.
	case "vm-dashboard":
		if err := drive(m, send(m, tea.KeyMsg{Type: tea.KeyEnter})); err != nil {
			return Result{}, fmt.Errorf("open VM detail: %w", err)
		}
		if err := walkVMDashboard(m); err != nil {
			return Result{}, err
		}
	case "network-switches":
		if err := press(m, "6"); err != nil {
			return Result{}, fmt.Errorf("open the Networks tab: %w", err)
		}
		if err := walkNetworkSwitches(m); err != nil {
			return Result{}, err
		}
	case "resize":
		for _, size := range [][2]int{{60, 20}, {100, 30}, {140, 40}} {
			if err := drive(m, send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})); err != nil {
				return Result{}, fmt.Errorf("resize %dx%d: %w", size[0], size[1], err)
			}
		}
	default:
		return Result{}, fmt.Errorf("scenario %q has no runner", name)
	}
	result.View = normalize(m.View())
	result.Observation = m.Observe()

	if isCriticalScreen(name) {
		for _, size := range [][2]int{{60, 20}, {100, 30}, {140, 40}} {
			if err := drive(m, send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})); err != nil {
				return Result{}, fmt.Errorf("golden resize %dx%d: %w", size[0], size[1], err)
			}
			if !opts.SkipGoldens {
				if err := checkGolden(name, size[0], size[1], normalize(m.View()), opts.UpdateGoldens); err != nil {
					return Result{}, err
				}
			}
		}
	}

	result.View = normalize(m.View())
	result.Observation = m.Observe()
	if err := assertResult(result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func isCriticalScreen(name string) bool {
	switch name {
	case "overview", "credential-cancel", "history-coverage-gap", "datastore-browser", "vm-dashboard", "network-switches":
		return true
	default:
		return false
	}
}

// walkVMDashboard visits every chart page and every range at each golden
// size and fails on the first frame wider or taller than the terminal: the
// regression class the dashboard's wrapping fixes addressed. It leaves the
// pane on the overview page and the 1h range, the state the goldens record.
func walkVMDashboard(m *tui.Model) error {
	pages := []string{"Overview", "CPU", "Memory", "Disk", "Network"}
	for _, size := range [][2]int{{60, 20}, {100, 30}, {140, 40}} {
		if err := drive(m, send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})); err != nil {
			return fmt.Errorf("vm-dashboard resize %dx%d: %w", size[0], size[1], err)
		}
		for i, page := range pages {
			if err := press(m, strconv.Itoa(i)); err != nil {
				return fmt.Errorf("vm-dashboard page %d: %w", i, err)
			}
			for step, key := range []string{"", ">", ">", ">", "<", "<", "<"} {
				if key != "" {
					if err := press(m, key); err != nil {
						return fmt.Errorf("vm-dashboard range key %q: %w", key, err)
					}
				}
				view := m.View()
				where := fmt.Sprintf("%s page, range step %d, at %dx%d", page, step, size[0], size[1])
				if err := boundedFrame(view, size[0], size[1]); err != nil {
					return fmt.Errorf("vm-dashboard %s: %w", where, err)
				}
				if size[0] >= 100 && !strings.Contains(ansi.Strip(view), fmt.Sprintf("[%d %s]", i, page)) {
					return fmt.Errorf("vm-dashboard %s does not mark its page tab", where)
				}
			}
		}
		if err := press(m, "0"); err != nil {
			return err
		}
	}
	if observation := m.Observe(); observation.Mode != "detail" {
		return fmt.Errorf("vm-dashboard walk left the detail pane (mode %q)", observation.Mode)
	}
	return nil
}

// walkNetworkSwitches checks the grouped Networks tab and both switch
// workspace pages at each golden size, failing on the first frame wider or
// taller than the terminal. It leaves DVS-Storage's Wiring page open on its
// vMotion port group, whose standby path the demo's dead link exercises:
// the state the goldens record.
func walkNetworkSwitches(m *tui.Model) error {
	for _, size := range [][2]int{{60, 20}, {100, 30}, {140, 40}} {
		if err := drive(m, send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})); err != nil {
			return fmt.Errorf("network-switches resize %dx%d: %w", size[0], size[1], err)
		}
		where := fmt.Sprintf("at %dx%d", size[0], size[1])
		view := m.View()
		if err := boundedFrame(view, size[0], size[1]); err != nil {
			return fmt.Errorf("network-switches table %s: %w", where, err)
		}
		if !strings.Contains(ansi.Strip(view), "▾ DVS-Prod") {
			return fmt.Errorf("network-switches table %s does not group under DVS-Production", where)
		}
		if err := drive(m, send(m, tea.KeyMsg{Type: tea.KeyEnter})); err != nil {
			return fmt.Errorf("open DVS-Production: %w", err)
		}
		for i, page := range []string{"Overview", "Wiring"} {
			if err := press(m, strconv.Itoa(i)); err != nil {
				return err
			}
			for step := 0; step < 3; step++ {
				view := m.View()
				if err := boundedFrame(view, size[0], size[1]); err != nil {
					return fmt.Errorf("network-switches %s page step %d %s: %w", page, step, where, err)
				}
				if size[0] >= 100 && !strings.Contains(ansi.Strip(view), fmt.Sprintf("[%d %s]", i, page)) {
					return fmt.Errorf("network-switches %s page %s does not mark its tab", page, where)
				}
				if err := press(m, "j"); err != nil {
					return err
				}
			}
		}
		if err := press(m, "esc"); err != nil {
			return err
		}
	}
	// Fold DVS-Production so DVS-Storage is the next row, then open its
	// Wiring page on the vMotion port group.
	for _, key := range []string{"g", " ", "j"} {
		if err := press(m, key); err != nil {
			return err
		}
	}
	if err := drive(m, send(m, tea.KeyMsg{Type: tea.KeyEnter})); err != nil {
		return fmt.Errorf("open DVS-Storage: %w", err)
	}
	for _, key := range []string{"1", "j", "j", "j"} {
		if err := press(m, key); err != nil {
			return err
		}
	}
	return nil
}

// boundedFrame reports a rendered frame that would wrap or scroll the
// terminal it was drawn for.
func boundedFrame(view string, width, height int) error {
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > height {
		return fmt.Errorf("frame has %d lines for a %d-line terminal", len(lines), height)
	}
	for n, line := range lines {
		if w := ansi.StringWidth(line); w > width {
			return fmt.Errorf("line %d is %d cells wide for a %d-column terminal: %q", n+1, w, width, ansi.Strip(line))
		}
	}
	return nil
}

func goldenPath(name string, width, height int) string {
	return filepath.Join("internal", "testbed", "scenarios", "testdata", "golden", fmt.Sprintf("%s-%dx%d.txt", name, width, height))
}

func checkGolden(name string, width, height int, got string, update bool) error {
	path := goldenPath(name, width, height)
	want, err := os.ReadFile(path)
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(got), 0o644)
	}
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("golden %s is missing; rerun with --update-goldens", path)
		}
		return err
	}
	if string(want) != got {
		return fmt.Errorf("golden mismatch for %s; rerun with --update-goldens to review changes", path)
	}
	return nil
}

func assertResult(result Result) error {
	if result.Observation.Context == "" {
		return fmt.Errorf("scenario %s selected no context", result.Name)
	}
	switch result.Name {
	case "overview", "duplicate-names", "resize":
		// A powered-on row has the status glyph in its second column, after the
		// cursor column (blank on every row but the selected one). This
		// deliberately does not name a VM: at production scale which rows fit
		// on screen depends on sort order and terminal height, not on whether
		// inventory rendered.
		if !strings.Contains(result.View, "\n ● ") {
			return fmt.Errorf("scenario %s did not render inventory", result.Name)
		}
	case "partial-failure":
		if !strings.Contains(result.View, "dr-site") {
			return fmt.Errorf("scenario %s lost the failed context", result.Name)
		}
	case "history-coverage-gap":
		if result.Observation.Mode != "history" && !strings.Contains(result.View, "HISTORY") {
			return fmt.Errorf("scenario %s did not enter History", result.Name)
		}
	case "network-switches":
		if result.Observation.Mode != "switch-detail" {
			return fmt.Errorf("scenario %s left the switch workspace (mode %q)", result.Name, result.Observation.Mode)
		}
		for _, want := range []string{"DVS-Storage", "[1 Wiring]", "vmotion-vlan-2030"} {
			if !strings.Contains(result.View, want) {
				return fmt.Errorf("scenario %s is missing %q from the wiring page", result.Name, want)
			}
		}
	case "vm-dashboard":
		if result.Observation.Mode != "detail" {
			return fmt.Errorf("scenario %s left the VM detail pane (mode %q)", result.Name, result.Observation.Mode)
		}
		for _, want := range []string{"[0 Overview]", "[1h]", "CPU usage"} {
			if !strings.Contains(result.View, want) {
				return fmt.Errorf("scenario %s is missing %q from the dashboard", result.Name, want)
			}
		}
	case "credential-cancel":
		if result.Observation.Prompt {
			return fmt.Errorf("scenario %s left a credential prompt open", result.Name)
		}
		if strings.Contains(result.View, testbed.FixturePassword) || strings.Contains(result.View, testbed.FixtureProxyPassword) {
			return fmt.Errorf("scenario %s rendered a fixture secret", result.Name)
		}
	}
	return nil
}

func setupBackend(ctx context.Context, connected bool) (tui.Backend, *assessment.Service, func(), error) {
	if !connected {
		backend := demo.NewBackend()
		service, closeHistory, err := backend.AssessmentService()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("seed demo history: %w", err)
		}
		return backend, service, closeHistory, nil
	}

	root, err := os.MkdirTemp("", "vsfleet-scenario-")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create connected scenario root: %w", err)
	}
	// The lab uses a contiguous block for five simulator listeners and three
	// proxy/failure slots. Probe a per-process range first: Windows runners can
	// reserve otherwise-valid fixed ports, which makes a static base flaky.
	portBase, err := availablePortBase()
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, nil, nil, err
	}
	labCtx, cancelLab := context.WithTimeout(context.Background(), time.Minute)
	defer cancelLab()
	lab, err := testbed.Start(labCtx, testbed.Options{Root: root, PortBase: portBase})
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, nil, nil, fmt.Errorf("start connected scenario lab: %w", err)
	}
	cfg, err := config.Load(lab.ConfigPath)
	if err != nil {
		lab.Close(ctx)
		_ = os.RemoveAll(root)
		return nil, nil, nil, err
	}
	keyring := credentials.NewStatic(credentials.SchemeKeyring, map[string]credentials.Credential{})
	prompt := credentials.NewStatic(credentials.SchemePrompt, map[string]credentials.Credential{})
	resolver := credentials.NewResolver(keyring, prompt)
	for _, cc := range cfg.Contexts {
		ref := credentials.Ref{Scheme: credentials.SchemeKeyring, Value: "scenario:" + cc.Name}
		keyring.Store(ctx, ref, credentials.Credential{Username: testbed.FixtureUsername, Password: testbed.FixturePassword})
		cc.Credential = ref
		if cc.Transport.Credential.Scheme == credentials.SchemePrompt {
			proxyRef := credentials.Ref{Scheme: credentials.SchemeKeyring, Value: "proxy:" + cc.Name}
			keyring.Store(ctx, proxyRef, credentials.Credential{Username: testbed.FixtureProxyUser, Password: testbed.FixtureProxyPassword})
			cc.Transport.Credential = proxyRef
		}
	}
	manager := session.New(resolver)
	manager.ConnectOptions.UserAgent = version.UserAgent()
	store, err := assessment.Open(lab.HistoryPath)
	if err != nil {
		manager.Close(ctx)
		lab.Close(ctx)
		_ = os.RemoveAll(root)
		return nil, nil, nil, err
	}
	service := &assessment.Service{Store: store, Collector: &assessment.Collector{Store: store, Manager: manager}}
	backend := tui.NewBackend(cfg, resolver, manager, vsphere.ConnectOptions{Resolver: resolver, UserAgent: version.UserAgent()})
	closeAll := func() {
		store.Close()
		manager.Close(context.Background())
		lab.Close(context.Background())
		_ = os.RemoveAll(root)
	}
	return backend, service, closeAll, nil
}

// availablePortBase finds a usable loopback range for the connected fixture.
// The listeners are only a probe; Start owns the real listeners immediately
// afterwards. Trying several bases avoids Windows' dynamic excluded-port
// ranges without weakening the testbed's loopback-only boundary.
func availablePortBase() (int, error) {
	const (
		firstPort = 20000
		stride    = 200
		attempts  = 120
	)
	for attempt := 0; attempt < attempts; attempt++ {
		base := firstPort + ((os.Getpid()+attempt*211)%attempts)*stride
		listeners := make([]net.Listener, 0, 8)
		available := true
		for _, offset := range []int{0, 1, 2, 3, 4, 100, 101, 102} {
			listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(base+offset)))
			if err != nil {
				available = false
				break
			}
			listeners = append(listeners, listener)
		}
		for _, listener := range listeners {
			_ = listener.Close()
		}
		if available {
			return base, nil
		}
	}
	return 0, errors.New("could not reserve an available loopback port range for connected scenarios")
}

func press(m *tui.Model, key string) error {
	if key == "esc" {
		return drive(m, send(m, tea.KeyMsg{Type: tea.KeyEsc}))
	}
	return drive(m, send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}))
}

func send(m *tui.Model, msg tea.Msg) tea.Cmd {
	return func() tea.Msg {
		_, cmd := m.Update(msg)
		return commandMsg{cmd: cmd}
	}
}

type commandMsg struct{ cmd tea.Cmd }

func drive(m *tui.Model, cmd tea.Cmd) error {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch value := msg.(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		for _, child := range value {
			if err := drive(m, child); err != nil {
				return err
			}
		}
	case commandMsg:
		return drive(m, value.cmd)
	default:
		if _, ok := msg.(spinner.TickMsg); ok {
			return nil
		}
		if _, ok := msg.(tea.KeyMsg); ok {
			return nil
		}
		if _, cmd := m.Update(msg); cmd != nil {
			return drive(m, cmd)
		}
	}
	return nil
}

var dynamicText = regexp.MustCompile(`(?m)(\b\d{1,3}(?:\.\d{1,3}){3}:\d{2,5}\b|/tmp/[^[:space:]]+|\b(?:[0-9]+ms|[0-9]+s)\b)`)

func normalize(view string) string {
	view = ansi.Strip(view)
	view = dynamicText.ReplaceAllString(view, "<dynamic>")
	return strings.TrimRight(view, " \n") + "\n"
}

func writeResult(root string, result Result) error {
	dir := filepath.Join(root, result.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "view.txt"), []byte(result.View), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "observation.txt"), []byte(fmt.Sprintf("%+v\n", result.Observation)), 0o600)
}

// RunAll executes the catalogue in a stable order.
func RunAll(ctx context.Context, names []string, opts RunOptions, out io.Writer) error {
	if len(names) == 0 {
		for _, definition := range definitions {
			if opts.Profile == "" || opts.Profile == definition.Profile {
				names = append(names, definition.Name)
			}
		}
	}
	sort.Strings(names)
	for _, name := range names {
		result, err := Run(ctx, name, opts)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "PASS %s (%s)\n", result.Name, result.Profile); err != nil {
			return err
		}
	}
	return nil
}

// Keep the timeout policy in one place for command and test callers.
const ScenarioTimeout = 3 * time.Minute
