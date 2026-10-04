//go:build linux && pty

package pty_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"

	"github.com/easonliuuuuu/vsfleet/internal/testbed"
)

const journeyTimeout = 45 * time.Second

var (
	repoRoot       string
	testbedBinary  string
	resultsRoot    string
	autoResultsDir bool
	portSequence   atomic.Int64
)

func TestMain(m *testing.M) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "resolve PTY test source path")
		os.Exit(1)
	}
	repoRoot = filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))

	buildDir, err := os.MkdirTemp("", "vsfleet-pty-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create PTY build directory: %v\n", err)
		os.Exit(1)
	}
	testbedBinary = filepath.Join(buildDir, "vsfleet-testbed")
	build := exec.Command("go", "build", "-trimpath", "-o", testbedBinary, "./cmd/vsfleet-testbed")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build vsfleet-testbed: %v\n%s", err, output)
		os.Exit(1)
	}

	resultsRoot = strings.TrimSpace(os.Getenv("VSFLEET_PTY_RESULTS_DIR"))
	if resultsRoot == "" {
		resultsRoot, err = os.MkdirTemp("", "vsfleet-pty-results-")
		autoResultsDir = true
	} else {
		resultsRoot, err = filepath.Abs(resultsRoot)
		if err == nil {
			err = os.MkdirAll(resultsRoot, 0o700)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare PTY results directory: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	if code == 0 && autoResultsDir {
		_ = os.RemoveAll(resultsRoot)
	} else if code != 0 {
		fmt.Fprintf(os.Stderr, "PTY failure artifacts: %s\n", resultsRoot)
	}
	_ = os.RemoveAll(buildDir)
	os.Exit(code)
}

func TestPTYJourneys(t *testing.T) {
	t.Run("launch inventory and exit cleanly", func(t *testing.T) {
		s := startSession(t, "launch-inventory", 30, 100)
		s.authenticate()
		s.send("quit", "q")
		s.expectExitZero()
	})

	t.Run("SSH failure restores the TUI", func(t *testing.T) {
		s := startSessionWithSSH(t, "ssh-failure", 30, 100, "fail")
		s.authenticate()
		mark := s.mark()
		s.send("select hosts", "3")
		s.waitFor(mark, "DC0_C0_H")
		mark = s.mark()
		s.send("open first host detail", "\r")
		s.waitFor(mark, "Managed object")
		mark = s.mark()
		s.send("open VM actions", "\r")
		s.waitFor(mark, "SSH to")
		mark = s.mark()
		s.send("start SSH", "\r")
		s.waitFor(mark, "Permission denied", "ssh session ended failed")
		mark = s.mark()
		s.send("prove TUI restored", "?")
		s.waitFor(mark, "Keys", "Resource kinds")
		s.send("quit", "q")
		s.expectExitZero()
	})

	t.Run("SSH hang cancels and restores the TUI", func(t *testing.T) {
		s := startSessionWithSSH(t, "ssh-cancel", 30, 100, "hang")
		s.authenticate()
		mark := s.mark()
		s.send("select hosts", "3")
		s.waitFor(mark, "DC0_C0_H")
		mark = s.mark()
		s.send("open first host detail", "\r")
		s.waitFor(mark, "Managed object")
		mark = s.mark()
		s.send("open VM actions", "\r")
		s.waitFor(mark, "SSH to")
		mark = s.mark()
		s.send("start SSH", "\r")
		s.waitFor(mark, "Connecting:")
		mark = s.mark()
		s.send("cancel SSH", "\x03")
		s.waitFor(mark, "ssh session ended failed")
		mark = s.mark()
		s.send("prove TUI restored", "?")
		s.waitFor(mark, "Keys", "Resource kinds")
		s.send("quit", "q")
		s.expectExitZero()
	})

	t.Run("cancel credential prompt and keep using UI", func(t *testing.T) {
		s := startSession(t, "credential-cancel", 30, 100)
		s.waitFor(0, "credentials required")
		mark := s.mark()
		s.send("reload selected context", "r")
		s.waitFor(mark, "Password for prod-vc")
		mark = s.mark()
		s.send("cancel credential prompt", "\x1b")
		s.waitFor(mark, "credential entry canceled")
		mark = s.mark()
		s.send("open help after cancellation", "?")
		s.waitFor(mark, "Keys", "Resource kinds")
		mark = s.mark()
		s.send("close help", "\x1b")
		s.waitFor(mark, "credential entry canceled")
		s.send("quit", "q")
		s.expectExitZero()
	})

	t.Run("browse datastore find recursively and navigate back", func(t *testing.T) {
		s := startSession(t, "datastore-browser", 30, 120)
		s.authenticate()

		mark := s.mark()
		s.send("select datastores", "5")
		s.waitFor(mark, "LocalDS_0")
		mark = s.mark()
		s.send("open datastore detail", "\r")
		s.waitFor(mark, "Capacity", "Managed object")
		mark = s.mark()
		s.send("open datastore actions", "\r")
		s.waitFor(mark, "Browse files", "Find in datastore")
		mark = s.mark()
		s.send("browse files", "\r")
		s.waitFor(mark, "Path  /", "database01/")

		mark = s.mark()
		s.send("open recursive find", "f")
		s.waitFor(mark, "recursive", "enter searches every directory")
		mark = s.mark()
		s.send("type recursive pattern", "*.vmdk")
		s.send("run recursive find", "\r")
		s.waitFor(mark, "database01/deep/database01.vmdk")

		mark = s.mark()
		s.send("open result directory", "\r")
		s.waitFor(mark, "/database01/deep", "database01.vmdk")
		mark = s.mark()
		s.send("navigate to parent directory", "\x1b")
		s.waitFor(mark, "/database01")
		mark = s.mark()
		s.send("navigate to datastore root", "\x1b")
		s.waitFor(mark, "Path  /", "database01/")
		mark = s.mark()
		s.send("return to datastore detail", "\x1b")
		s.waitFor(mark, "Capacity", "Managed object")
		s.send("quit", "q")
		s.expectExitZero()
	})

	t.Run("switch history panes and return without leaked state", func(t *testing.T) {
		s := startSession(t, "history-panes", 30, 100)
		s.waitFor(0, "credentials required")
		mark := s.mark()
		s.send("open history", "H")
		s.waitFor(mark, "history  ·  Changes")
		for _, pane := range []string{"Trends", "Runs", "Health"} {
			mark = s.mark()
			s.send("next history pane", "\t")
			s.waitFor(mark, "history  ·  "+pane)
		}
		mark = s.mark()
		s.send("return to inventory", "\x1b")
		s.waitFor(mark, "credentials required")
		mark = s.mark()
		s.send("reopen history", "H")
		s.waitFor(mark, "history  ·  Changes")
		s.send("quit", "q")
		s.expectExitZero()
	})

	t.Run("resize narrow to wide without corrupting state", func(t *testing.T) {
		s := startSession(t, "resize", 20, 60)
		s.waitFor(0, "credentials required")
		mark := s.mark()
		s.send("open history", "H")
		s.waitFor(mark, "history", "Changes")
		mark = s.mark()
		s.resize(40, 140)
		s.waitFor(mark, "history  ·  Changes")
		mark = s.mark()
		s.send("next history pane after resize", "\t")
		s.waitFor(mark, "history  ·  Trends")
		mark = s.mark()
		s.send("return to inventory", "\x1b")
		s.waitFor(mark, "credentials required")
		s.send("quit", "q")
		s.expectExitZero()
	})

	t.Run("VM dashboard pages, ranges, resize, and quit mid-query", func(t *testing.T) {
		s := startSession(t, "vm-dashboard", 30, 120)
		s.authenticate()
		// Virtual machines are the default kind, so enter opens the first VM.
		mark := s.mark()
		s.send("open first VM detail", "\r")
		s.waitFor(mark, "[0 Overview]", "[1h]", "CPU usage")
		mark = s.mark()
		s.send("open disk chart page", "3")
		s.waitFor(mark, "[3 Disk]")
		mark = s.mark()
		s.send("step to the 24h range", ">")
		s.waitFor(mark, "[24h]", "5 min roll-up")
		// At 60 columns the charts move below the properties, past the
		// bottom of a 20-row terminal; the pane header must still repaint.
		mark = s.mark()
		s.resize(20, 60)
		s.waitFor(mark, "Virtual machine")
		mark = s.mark()
		s.resize(40, 140)
		s.waitFor(mark, "[3 Disk]", "[24h]")
		// Stepping to a range not yet loaded issues its query in the same
		// update that lights the tab, so quit lands while that query is very
		// likely still in flight. Keys go one per write: bubbletea reads
		// back-to-back runes as a single multi-rune key.
		mark = s.mark()
		s.send("step to the unloaded 7d range", ">")
		s.waitFor(mark, "[7d]")
		s.send("quit while the 7d query runs", "q")
		s.expectExitZero()
	})

	t.Run("ctrl-c cancels active background work", func(t *testing.T) {
		s := startSession(t, "ctrl-c-background", 30, 100)
		s.waitFor(0, "credentials required")
		mark := s.mark()
		s.send("open history", "H")
		s.waitFor(mark, "history  ·  Changes")
		mark = s.mark()
		s.send("start assessment capture", "n")
		s.waitFor(mark, "capturing prod-vc")
		s.send("cancel process while capture is active", "\x03")
		s.expectExitZero()
	})
}

type terminalSession struct {
	t       *testing.T
	name    string
	dir     string
	state   string
	started time.Time
	cmd     *exec.Cmd
	ptmx    *os.File

	mu      sync.Mutex
	raw     bytes.Buffer
	events  []string
	waitErr error
	done    chan struct{}
	reader  chan struct{}
	once    sync.Once
}

func startSession(t *testing.T, name string, rows, cols uint16) *terminalSession {
	return startSessionWithSSH(t, name, rows, cols, "")
}

func startSessionWithSSH(t *testing.T, name string, rows, cols uint16, sshMode string) *terminalSession {
	t.Helper()
	dir := filepath.Join(resultsRoot, name)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("reset journey results: %v", err)
	}
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("create journey state: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(state, "home"), 0o700); err != nil {
		t.Fatalf("create journey home: %v", err)
	}
	portBase, err := availablePortBase()
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(testbedBinary, "--root", state, "--port-base", strconv.Itoa(portBase))
	cmd.Dir = repoRoot
	cmd.Env = isolatedEnvironment(os.Environ(), state, name)
	if sshMode != "" {
		shimDir := filepath.Join(state, "ssh-bin")
		if err := installSSHShim(shimDir, sshMode); err != nil {
			t.Fatalf("install SSH shim: %v", err)
		}
		cmd.Env = prependEnv(cmd.Env, "PATH", shimDir)
		cmd.Env = append(cmd.Env, "VSFLEET_TESTBED_SSH_MODE="+sshMode)
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		t.Fatalf("start %s in PTY: %v", name, err)
	}
	s := &terminalSession{
		t: t, name: name, dir: dir, state: state, started: time.Now(),
		cmd: cmd, ptmx: ptmx, done: make(chan struct{}), reader: make(chan struct{}),
	}
	s.event("start rows=%d cols=%d port-base=%d", rows, cols, portBase)
	go s.readOutput()
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		s.waitErr = err
		s.mu.Unlock()
		close(s.done)
	}()
	t.Cleanup(s.close)
	return s
}

func isolatedEnvironment(base []string, state, name string) []string {
	drop := map[string]bool{
		"VSFLEET_CONFIG": true, "VSFLEET_HISTORY_DB": true,
		"VSFLEET_TESTBED_ROOT": true, "VSFLEET_TESTBED_SCENARIO": true,
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
		"HOME": true, "SSH_AUTH_SOCK": true,
	}
	out := make([]string, 0, len(base)+7)
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if !ok || drop[key] || key == "TERM" || key == "COLORTERM" {
			continue
		}
		out = append(out, item)
	}
	return append(out,
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"TZ=UTC",
		"HOME="+filepath.Join(state, "home"),
		"VSFLEET_TESTBED_SCENARIO=pty-"+name,
		"XDG_CONFIG_HOME="+filepath.Join(state, "xdg-config"),
		"XDG_DATA_HOME="+filepath.Join(state, "xdg-data"),
		"XDG_CACHE_HOME="+filepath.Join(state, "xdg-cache"),
	)
}

func prependEnv(env []string, key, value string) []string {
	for i, item := range env {
		if strings.HasPrefix(item, key+"=") {
			env[i] = key + "=" + value + string(os.PathListSeparator) + strings.TrimPrefix(item, key+"=")
			return env
		}
	}
	return append(env, key+"="+value)
}

func installSSHShim(dir, mode string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	shim := `#!/bin/sh
if [ "$1" = "-G" ]; then
  printf 'user testuser\n'
  exit 0
fi
if [ "${VSFLEET_TESTBED_SSH_MODE:-}" = "hang" ]; then
  trap 'exit 130' INT TERM
  while :; do sleep 1; done
fi
printf 'Permission denied (publickey).\n' >&2
exit 255
`
	return os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0o700)
}

func (s *terminalSession) authenticate() {
	s.t.Helper()
	s.waitFor(0, "credentials required")
	mark := s.mark()
	s.send("reload selected context", "r")
	s.waitFor(mark, "Password for prod-vc")
	mark = s.mark()
	s.sendSecret(testbed.FixturePassword + "\r")
	s.waitFor(mark, "DC0_")
}

func (s *terminalSession) readOutput() {
	defer close(s.reader)
	buf := make([]byte, 16*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.mu.Lock()
			_, _ = s.raw.Write(buf[:n])
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *terminalSession) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.raw.Len()
}

func (s *terminalSession) send(label, value string) {
	s.t.Helper()
	s.event("input %s", label)
	if _, err := io.WriteString(s.ptmx, value); err != nil {
		s.t.Fatalf("%s: %v", label, err)
	}
}

func (s *terminalSession) sendSecret(value string) {
	s.t.Helper()
	s.event("input fixture credential [REDACTED]")
	if _, err := io.WriteString(s.ptmx, value); err != nil {
		s.t.Fatalf("enter fixture credential: %v", err)
	}
}

func (s *terminalSession) resize(rows, cols uint16) {
	s.t.Helper()
	s.event("resize rows=%d cols=%d", rows, cols)
	if err := pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		s.t.Fatalf("resize PTY: %v", err)
	}
}

func (s *terminalSession) waitFor(mark int, wants ...string) {
	s.t.Helper()
	deadline := time.Now().Add(journeyTimeout)
	for {
		text := s.textAfter(mark)
		matched := true
		for _, want := range wants {
			if !strings.Contains(text, want) {
				matched = false
				break
			}
		}
		if matched {
			s.event("observed %q", strings.Join(wants, " + "))
			return
		}
		select {
		case <-s.done:
			s.t.Fatalf("process exited before observing %q: %v\n%s", strings.Join(wants, " + "), s.processError(), tail(text, 4000))
		default:
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("timed out waiting for %q\n%s", strings.Join(wants, " + "), tail(text, 4000))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *terminalSession) textAfter(mark int) string {
	s.mu.Lock()
	raw := append([]byte(nil), s.raw.Bytes()...)
	s.mu.Unlock()
	if mark > len(raw) {
		mark = len(raw)
	}
	return normalizeTranscript(string(raw[mark:]))
}

func (s *terminalSession) expectExitZero() {
	s.t.Helper()
	select {
	case <-s.done:
	case <-time.After(journeyTimeout):
		s.t.Fatal("timed out waiting for clean process exit")
	}
	if err := s.processError(); err != nil {
		s.t.Fatalf("process exit: %v", err)
	}
	s.event("exit status=0")
}

func (s *terminalSession) processError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitErr
}

func (s *terminalSession) event(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	elapsed := time.Since(s.started).Round(time.Millisecond)
	s.events = append(s.events, fmt.Sprintf("%s %s", elapsed, fmt.Sprintf(format, args...)))
}

func (s *terminalSession) close() {
	s.once.Do(func() {
		select {
		case <-s.done:
		default:
			_, _ = s.ptmx.Write([]byte{3})
			select {
			case <-s.done:
			case <-time.After(2 * time.Second):
				_ = s.cmd.Process.Kill()
				<-s.done
			}
		}
		_ = s.ptmx.Close()
		select {
		case <-s.reader:
		case <-time.After(time.Second):
		}
		s.writeArtifacts()
	})
}

func (s *terminalSession) writeArtifacts() {
	s.mu.Lock()
	raw := redact(string(s.raw.Bytes()))
	events := strings.Join(s.events, "\n") + "\n"
	waitErr := s.waitErr
	s.mu.Unlock()
	metadata, _ := json.MarshalIndent(map[string]any{
		"journey": s.name,
		"state":   s.state,
		"exit":    fmt.Sprint(waitErr),
	}, "", "  ")
	files := map[string][]byte{
		"process-output.ansi": []byte(raw),
		"terminal.txt":        []byte(normalizeTranscript(raw)),
		"events.log":          []byte(events),
		"result.json":         append(metadata, '\n'),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(s.dir, name), data, 0o600); err != nil {
			s.t.Logf("write %s artifact: %v", name, err)
		}
	}
}

func normalizeTranscript(raw string) string {
	plain := ansi.Strip(redact(raw))
	plain = strings.ReplaceAll(plain, "\r", "")
	plain = strings.ReplaceAll(plain, "\x00", "")
	return plain
}

func redact(value string) string {
	return strings.NewReplacer(
		testbed.FixturePassword, "[REDACTED]",
		testbed.FixtureProxyPassword, "[REDACTED]",
	).Replace(value)
}

func tail(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[len(value)-max:]
}

func availablePortBase() (int, error) {
	for attempt := 0; attempt < 100; attempt++ {
		sequence := int(portSequence.Add(1))
		base := 22000 + ((os.Getpid()+sequence*211)%120)*200
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
	return 0, errors.New("could not reserve an available loopback port range for PTY testbed")
}
