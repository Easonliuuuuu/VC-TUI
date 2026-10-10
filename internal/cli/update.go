package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/easonliuuuuu/vsfleet/internal/tui"
	"github.com/easonliuuuuu/vsfleet/internal/update"
	"github.com/easonliuuuuu/vsfleet/internal/version"
)

// updater is the release check as one run of the program uses it. Every
// field that touches the outside world — the clock, GitHub, the package
// manager, re-executing — is a field, so tests can replace it; App.updates
// builds the real one.
type updater struct {
	path    string
	current string
	enabled bool
	method  update.Method

	now     func() time.Time
	latest  func(context.Context) (string, error)
	runCmd  func(ctx context.Context, argv []string, in io.Reader, out, errOut io.Writer) error
	restart func() error
	isTTY   func(io.Writer) bool
}

func (a *App) updates() *updater {
	if a.upd != nil {
		return a.upd
	}
	current := version.Release()
	u := &updater{
		current: current,
		now:     time.Now,
		latest: func(ctx context.Context) (string, error) {
			return update.Latest(ctx, nil, update.LatestURL, version.UserAgent())
		},
		runCmd:  runForeground,
		restart: restartSelf,
		isTTY:   isTerminalWriter,
	}
	if p, err := update.DefaultPath(); err == nil {
		u.path = p
		u.enabled = update.Enabled(current, os.Getenv)
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		home, _ := os.UserHomeDir()
		u.method = update.Detect(exe, update.Env{GOOS: runtime.GOOS, Home: home, Getenv: os.Getenv, LookPath: exec.LookPath})
	}
	a.upd = u
	return u
}

// check asks GitHub and records the answer. A failure is recorded too, so an
// estate that cannot reach GitHub is tried again tomorrow, not every launch.
func (u *updater) check(ctx context.Context) {
	latest, err := u.latest(ctx)
	now := u.now()
	_ = update.Modify(u.path, func(s *update.State) {
		s.CheckedAt = now
		if err == nil {
			s.Latest = latest
		}
	})
}

// uiOptions fills in the interface's side of the release check: the badge
// for a release already known, a background check when the last one is a
// day old, and the launch prompt when one is due.
func (u *updater) uiOptions(opts *tui.Options) {
	if !u.enabled {
		return
	}
	st := update.Load(u.path)
	now := u.now()
	opts.UpdateBadge = st.Available(u.current)
	if st.Stale(now) {
		opts.CheckUpdate = func(ctx context.Context) string {
			u.check(ctx)
			return update.Load(u.path).Available(u.current)
		}
	}
	if !st.ShouldPrompt(u.current, now) {
		return
	}
	latest := st.Available(u.current)
	opts.Upgrade = &tui.UpgradeOffer{
		Current:  u.current,
		Latest:   latest,
		URL:      update.ReleaseURL(latest),
		Command:  u.method.CommandLine(),
		Runs:     u.method.Runs,
		Why:      u.method.Why,
		AskAgain: now.Add(update.SnoozeInterval),
	}
	opts.UpgradeChosen = func(c tui.UpgradeChoice) { u.record(c, latest) }
}

// record remembers an answer to the launch prompt.
func (u *updater) record(c tui.UpgradeChoice, latest string) {
	now := u.now()
	_ = update.Modify(u.path, func(s *update.State) {
		if c == tui.UpgradeSkip {
			s.Skipped = latest
			return
		}
		s.SnoozedUntil = now.Add(update.SnoozeInterval)
	})
}

// upgrade runs the install method's command in the foreground, with the
// package manager's own output on the terminal, and on success replaces this
// process with the new binary so the welcome plays the upgrade. It returns
// only if the upgrade failed — with the note the interface reopens with — or
// if the restart itself did not happen.
func (a *App) upgrade(ctx context.Context, u *updater) (notice string, failed bool) {
	latest := update.Load(u.path).Latest
	w := a.errOut()
	// "Not now" first: if the restart lands on the same version, or the
	// upgrade fails, the prompt must not come straight back.
	u.record(tui.UpgradeLater, latest)
	again := u.now().Add(update.SnoozeInterval).Format("Mon 2 Jan")
	m := u.method
	if !m.Runs || len(m.Command) == 0 {
		return "", false
	}

	fmt.Fprintf(w, "Upgrading vsfleet %s → %s with %s\n$ %s\n", u.current, latest, m.Name, m.CommandLine())
	if err := u.runCmd(ctx, m.Command, a.in(), a.out(), w); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			fmt.Fprintf(w, "✕ %s exited with status %d · still on v%s\n", m.Command[0], exit.ExitCode(), u.current)
		} else {
			fmt.Fprintf(w, "✕ %s could not run: %v · still on v%s\n", m.Command[0], err, u.current)
		}
		fmt.Fprint(w, "Press enter to return to vsfleet.")
		_, _ = bufio.NewReader(a.in()).ReadString('\n')
		return fmt.Sprintf("Upgrade failed · still on v%s · asks again %s", u.current, again), true
	}
	fmt.Fprintf(w, "✓ Upgraded to v%s · restarting…\n", latest)
	if err := u.restart(); err != nil {
		fmt.Fprintf(w, "Run vsfleet again to start v%s (%v).\n", latest, err)
	}
	return "", false
}

func runForeground(ctx context.Context, argv []string, in io.Reader, out, errOut io.Writer) error {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = in, out, errOut
	return c.Run()
}

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// quietCommand reports whether cmd never prints the release notice: the
// interface handles releases itself, the demo promises to dial nothing, and
// completion output is read by a shell, not a person.
func quietCommand(cmd *cobra.Command) bool {
	if cmd == cmd.Root() {
		return true
	}
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "ui", "demo", "completion", "help", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			return true
		}
	}
	return false
}

// notices reports whether this command run takes part in the release check
// at all: only a person at a terminal reads the notice, so a script whose
// stderr is captured neither checks nor prints.
func (a *App) notices(cmd *cobra.Command) (*updater, bool) {
	u := a.updates()
	return u, u.enabled && !quietCommand(cmd) && u.isTTY(a.errOut())
}

// startUpdateCheck begins a background check before a command runs, when the
// last one is a day old.
func (a *App) startUpdateCheck(cmd *cobra.Command) {
	u, ok := a.notices(cmd)
	if !ok || !update.Load(u.path).Stale(u.now()) {
		return
	}
	done := make(chan struct{})
	a.updateDone = done
	ctx := cmd.Context()
	go func() {
		defer close(done)
		u.check(ctx)
	}()
}

// finishUpdateCheck prints gh's three-line notice after a command, at most
// once a day. It waits up to a second for a check still in flight, so the
// answer is at least remembered for next time.
func (a *App) finishUpdateCheck(cmd *cobra.Command) {
	u, ok := a.notices(cmd)
	if !ok {
		return
	}
	if a.updateDone != nil {
		select {
		case <-a.updateDone:
		case <-time.After(time.Second):
		}
	}
	st := update.Load(u.path)
	latest := st.Available(u.current)
	now := u.now()
	if latest == "" || now.Sub(st.NotifiedAt) < update.CheckInterval {
		return
	}
	w := a.errOut()
	fmt.Fprintf(w, "\nA new release of vsfleet is available: %s → %s\n", u.current, latest)
	if cl := u.method.CommandLine(); cl != "" {
		fmt.Fprintf(w, "To upgrade, run: %s\n", cl)
	} else {
		fmt.Fprintln(w, "Download it from the release page:")
	}
	fmt.Fprintln(w, update.ReleaseURL(latest))
	_ = update.Modify(u.path, func(s *update.State) { s.NotifiedAt = now })
}
