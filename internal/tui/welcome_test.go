package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/config"
)

func newWelcomeFixture(t *testing.T, w Welcome) *welcomeModel {
	t.Helper()
	b := &fakeBackend{contexts: []*config.Context{
		ctx("prod", "https://vcsa.prod.internal"),
		ctx("dr", "https://vcsa.dr.internal"),
	}}
	return newWelcomeModel(newTestModel(t, b, Options{Current: "prod"}), w)
}

var updatedWelcome = Welcome{Kind: WelcomeUpdated, Version: "0.10.0", Previous: "0.9.0"}

func TestWelcomePlaysEveryFrameThenHandsOverToTheInterface(t *testing.T) {
	w := newWelcomeFixture(t, updatedWelcome)
	inner := w.inner
	if next, _ := w.Update(tea.WindowSizeMsg{Width: 100, Height: 30}); next != w {
		t.Fatalf("a terminal large enough for the scene should keep the welcome, got %T", next)
	}
	for i := 1; i < welcomeFrames; i++ {
		next, cmd := w.Update(welcomeTickMsg{})
		if next != w || cmd == nil {
			t.Fatalf("frame %d: want the welcome to keep playing and schedule the next frame", i)
		}
	}
	next, cmd := w.Update(welcomeTickMsg{})
	if next != inner {
		t.Fatalf("after the last frame the program's model should be the interface itself, got %T", next)
	}
	if cmd != nil {
		t.Error("handing over should not schedule another frame")
	}
}

func TestWelcomeAnyKeySkipsWithoutActingOnTheKey(t *testing.T) {
	w := newWelcomeFixture(t, updatedWelcome)
	inner := w.inner
	next, cmd := w.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if next != inner {
		t.Fatalf("a key should skip straight to the interface, got %T", next)
	}
	if cmd != nil || inner.quitting {
		t.Error("the key that skipped the welcome must not also reach the interface: q would have quit")
	}
}

func TestWelcomeCtrlCStillQuits(t *testing.T) {
	w := newWelcomeFixture(t, updatedWelcome)
	_, cmd := w.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c during the welcome should quit, got no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("ctrl+c during the welcome should quit, got %T", cmd())
	}
}

func TestWelcomePassesOtherMessagesToTheInterface(t *testing.T) {
	w := newWelcomeFixture(t, updatedWelcome)
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if w.inner.width != 120 || w.inner.height != 40 {
		t.Errorf("the interface should learn the terminal size underneath the welcome, got %dx%d", w.inner.width, w.inner.height)
	}
}

func TestWelcomeInASmallTerminalOpensTheInterfaceWithANote(t *testing.T) {
	w := newWelcomeFixture(t, updatedWelcome)
	inner := w.inner
	next, _ := w.Update(tea.WindowSizeMsg{Width: welcomeCols - 1, Height: 30})
	if next != inner {
		t.Fatalf("a terminal narrower than the scene should open the interface at once, got %T", next)
	}
	want := "Updated v0.9.0 → v0.10.0 · what's new: github.com/Easonliuuuuu/vsfleet/releases/tag/v0.10.0"
	if inner.message != want || inner.messageBad {
		t.Errorf("message line = %q (bad=%v), want %q", inner.message, inner.messageBad, want)
	}
}

func TestWelcomeLastFrameShowsTheFleet(t *testing.T) {
	cases := []struct {
		name string
		w    Welcome
		want []string
	}{
		{"update", updatedWelcome, []string{"█████", "Updated  v0.9.0 → v0.10.0", "github.com/Easonliuuuuu/vsfleet/releases/tag/v0.10.0", "prod", "dr", "any key to skip"}},
		{"update from before the welcome", Welcome{Kind: WelcomeUpdated, Version: "0.10.0"}, []string{"Updated to v0.10.0"}},
		{"development build", Welcome{Kind: WelcomeUpdated, Version: "dev", Previous: "0.9.0"}, []string{"Running a development build"}},
		{"first run", Welcome{Kind: WelcomeFirstRun, Version: "0.10.0"}, []string{"Welcome aboard.", "Let's commission your first vCenter.", "your first vCenter"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWelcomeFixture(t, tc.w)
			got := ansi.Strip(w.scene(welcomeFrames - 1))
			for _, s := range tc.want {
				if !strings.Contains(got, s) {
					t.Errorf("last frame is missing %q:\n%s", s, got)
				}
			}
			if tc.w.Version == "dev" && strings.Contains(got, "releases/tag") {
				t.Errorf("a development build has no release page to link to:\n%s", got)
			}
		})
	}
}

func TestWelcomeSceneIsDeterministicAndFitsItsGrid(t *testing.T) {
	w := newWelcomeFixture(t, updatedWelcome)
	for f := 0; f < welcomeFrames; f++ {
		a, b := w.scene(f), w.scene(f)
		if a != b {
			t.Fatalf("frame %d drew differently twice", f)
		}
		lines := strings.Split(a, "\n")
		if len(lines) != welcomeRows {
			t.Fatalf("frame %d has %d lines, want %d", f, len(lines), welcomeRows)
		}
		for i, l := range lines {
			if n := ansi.StringWidth(l); n != welcomeCols {
				t.Fatalf("frame %d line %d is %d cells wide, want %d", f, i, n, welcomeCols)
			}
		}
	}
}

func TestWelcomeShipsAlwaysIncludeTheContextInView(t *testing.T) {
	var contexts []*config.Context
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		contexts = append(contexts, ctx(n, "https://vcsa."+n+".internal"))
	}
	m := newTestModel(t, &fakeBackend{contexts: contexts}, Options{Current: "f"})
	got := newWelcomeModel(m, updatedWelcome).shipNames()
	want := []string{"a", "b", "c", "f"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ships = %v, want %v", got, want)
	}
}
