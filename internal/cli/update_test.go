package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/tui"
	"github.com/easonliuuuuu/vsfleet/internal/update"
)

var testNow = time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)

type fakeUpdater struct {
	*updater
	checks   int
	ran      [][]string
	runErr   error
	restarts int
}

func newFakeUpdater(t *testing.T, st update.State) *fakeUpdater {
	t.Helper()
	path := filepath.Join(t.TempDir(), "update.json")
	if err := update.Modify(path, func(s *update.State) { *s = st }); err != nil {
		t.Fatal(err)
	}
	f := &fakeUpdater{}
	f.updater = &updater{
		path:    path,
		current: "0.10.0",
		enabled: true,
		method:  update.Method{Name: "Homebrew", Command: []string{"brew", "upgrade", "vsfleet"}, Runs: true},
		now:     func() time.Time { return testNow },
		latest: func(context.Context) (string, error) {
			f.checks++
			return "0.11.0", nil
		},
		runCmd: func(_ context.Context, argv []string, _ io.Reader, _, _ io.Writer) error {
			f.ran = append(f.ran, argv)
			return f.runErr
		},
		restart: func() error { f.restarts++; return nil },
		isTTY:   func(io.Writer) bool { return true },
	}
	return f
}

func TestUIOptionsOffersAKnownReleaseOnce(t *testing.T) {
	f := newFakeUpdater(t, update.State{CheckedAt: testNow.Add(-time.Hour), Latest: "0.11.0"})
	var opts tui.Options
	f.uiOptions(&opts)
	if opts.UpdateBadge != "0.11.0" {
		t.Errorf("badge = %q, want 0.11.0", opts.UpdateBadge)
	}
	if opts.CheckUpdate != nil {
		t.Error("an hour-old check should not run again")
	}
	if opts.Upgrade == nil || opts.Upgrade.Command != "brew upgrade vsfleet" || !opts.Upgrade.Runs {
		t.Fatalf("offer = %+v, want a runnable brew upgrade", opts.Upgrade)
	}

	opts.UpgradeChosen(tui.UpgradeLater)
	var again tui.Options
	f.uiOptions(&again)
	if again.Upgrade != nil {
		t.Error("not now should keep the prompt away")
	}
	if again.UpdateBadge != "0.11.0" {
		t.Error("not now should keep the badge")
	}
	if got := update.Load(f.path).SnoozedUntil; !got.Equal(testNow.Add(7 * 24 * time.Hour)) {
		t.Errorf("snoozed until %v, want a week from now", got)
	}
}

func TestUIOptionsSkipSilencesTheRelease(t *testing.T) {
	f := newFakeUpdater(t, update.State{CheckedAt: testNow, Latest: "0.11.0"})
	var opts tui.Options
	f.uiOptions(&opts)
	opts.UpgradeChosen(tui.UpgradeSkip)
	var again tui.Options
	f.uiOptions(&again)
	if again.Upgrade != nil || again.UpdateBadge != "" {
		t.Errorf("a skipped release should neither prompt nor badge: %+v", again)
	}
}

func TestUIOptionsChecksInTheBackgroundWhenStale(t *testing.T) {
	f := newFakeUpdater(t, update.State{})
	var opts tui.Options
	f.uiOptions(&opts)
	if opts.Upgrade != nil || opts.UpdateBadge != "" {
		t.Error("nothing is known yet, so nothing should be offered")
	}
	if opts.CheckUpdate == nil {
		t.Fatal("a never-checked install should check in the background")
	}
	if got := opts.CheckUpdate(context.Background()); got != "0.11.0" {
		t.Errorf("background check returned %q, want 0.11.0 for the badge", got)
	}
	st := update.Load(f.path)
	if st.Latest != "0.11.0" || !st.CheckedAt.Equal(testNow) {
		t.Errorf("check was not remembered: %+v", st)
	}
}

func TestUIOptionsRemembersAFailedCheckToo(t *testing.T) {
	f := newFakeUpdater(t, update.State{})
	f.latest = func(context.Context) (string, error) { return "", errors.New("blocked") }
	var opts tui.Options
	f.uiOptions(&opts)
	opts.CheckUpdate(context.Background())
	if st := update.Load(f.path); !st.CheckedAt.Equal(testNow) || st.Latest != "" {
		t.Errorf("a failed check should wait a day before trying again: %+v", st)
	}
}

func TestUIOptionsDisabled(t *testing.T) {
	f := newFakeUpdater(t, update.State{CheckedAt: testNow, Latest: "0.11.0"})
	f.enabled = false
	var opts tui.Options
	f.uiOptions(&opts)
	if opts.Upgrade != nil || opts.UpdateBadge != "" || opts.CheckUpdate != nil {
		t.Errorf("a disabled check must do nothing: %+v", opts)
	}
}

func TestUpgradeRunsTheCommandThenRestarts(t *testing.T) {
	f := newFakeUpdater(t, update.State{CheckedAt: testNow, Latest: "0.11.0"})
	var errOut bytes.Buffer
	a := &App{In: strings.NewReader(""), Out: io.Discard, Err: &errOut, upd: f.updater}
	notice, failed := a.upgrade(context.Background(), f.updater)
	if failed || notice != "" {
		t.Fatalf("upgrade reported failure: %q", notice)
	}
	if len(f.ran) != 1 || strings.Join(f.ran[0], " ") != "brew upgrade vsfleet" {
		t.Errorf("ran %v, want brew upgrade vsfleet", f.ran)
	}
	if f.restarts != 1 {
		t.Errorf("restarted %d times, want once", f.restarts)
	}
	for _, want := range []string{"Upgrading vsfleet 0.10.0 → 0.11.0 with Homebrew", "$ brew upgrade vsfleet", "✓ Upgraded to v0.11.0 · restarting…"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("output is missing %q:\n%s", want, errOut.String())
		}
	}
	if !update.Load(f.path).SnoozedUntil.After(testNow) {
		t.Error("the prompt must be snoozed before restarting, or a restart onto the same version would ask again")
	}
}

func TestUpgradeFailureReturnsToTheInterface(t *testing.T) {
	f := newFakeUpdater(t, update.State{CheckedAt: testNow, Latest: "0.11.0"})
	f.runErr = exec.Command("sh", "-c", "exit 3").Run()
	var errOut bytes.Buffer
	a := &App{In: strings.NewReader("\n"), Out: io.Discard, Err: &errOut, upd: f.updater}
	notice, failed := a.upgrade(context.Background(), f.updater)
	if !failed {
		t.Fatal("a failed upgrade should reopen the interface")
	}
	if want := "Upgrade failed · still on v0.10.0 · asks again Sat 17 Oct"; notice != want {
		t.Errorf("notice = %q, want %q", notice, want)
	}
	if !strings.Contains(errOut.String(), "✕ brew exited with status 3 · still on v0.10.0") {
		t.Errorf("output should report the exit status:\n%s", errOut.String())
	}
	if f.restarts != 0 {
		t.Error("a failed upgrade must not restart")
	}
}

func TestCommandLineNoticeAtMostOnceADay(t *testing.T) {
	f := newFakeUpdater(t, update.State{CheckedAt: testNow, Latest: "0.11.0"})
	var errOut bytes.Buffer
	a := &App{Err: &errOut, upd: f.updater}
	root := &cobra.Command{Use: "vsfleet"}
	list := &cobra.Command{Use: "list"}
	root.AddCommand(list)

	a.finishUpdateCheck(list)
	want := "\nA new release of vsfleet is available: 0.10.0 → 0.11.0\nTo upgrade, run: brew upgrade vsfleet\nhttps://github.com/Easonliuuuuu/vsfleet/releases/tag/v0.11.0\n"
	if errOut.String() != want {
		t.Errorf("notice = %q, want %q", errOut.String(), want)
	}
	errOut.Reset()
	a.finishUpdateCheck(list)
	if errOut.Len() != 0 {
		t.Errorf("a second command the same day printed the notice again: %q", errOut.String())
	}
}

func TestCommandLineNoticeStaysOutOfScriptsAndTheInterface(t *testing.T) {
	f := newFakeUpdater(t, update.State{Latest: "0.11.0"})
	var errOut bytes.Buffer
	a := &App{Err: &errOut, upd: f.updater}
	root := &cobra.Command{Use: "vsfleet"}
	list, ui, demo := &cobra.Command{Use: "list"}, &cobra.Command{Use: "ui"}, &cobra.Command{Use: "demo"}
	root.AddCommand(list, ui, demo)

	for _, c := range []*cobra.Command{root, ui, demo} {
		a.startUpdateCheck(c)
		a.finishUpdateCheck(c)
	}
	f.isTTY = func(io.Writer) bool { return false }
	a.startUpdateCheck(list)
	a.finishUpdateCheck(list)
	if errOut.Len() != 0 || f.checks != 0 {
		t.Errorf("printed %q and checked %d times; the interface, the demo and captured stderr should do neither", errOut.String(), f.checks)
	}
}
