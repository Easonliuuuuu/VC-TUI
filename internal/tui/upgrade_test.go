package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/config"
)

var brewOffer = UpgradeOffer{
	Current: "0.10.0", Latest: "0.11.0",
	URL:      "https://github.com/Easonliuuuuu/vsfleet/releases/tag/v0.11.0",
	Command:  "brew upgrade vsfleet",
	Runs:     true,
	AskAgain: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC),
}

func newPromptFixture(t *testing.T, offer UpgradeOffer) (*upgradePrompt, *[]UpgradeChoice, *fakeHandoff) {
	t.Helper()
	h := &fakeHandoff{}
	b := &fakeBackend{contexts: []*config.Context{ctx("prod", "https://vcsa.prod.internal")}}
	m := newTestModel(t, b, Options{Current: "prod", Handoff: h, UpdateBadge: offer.Latest})
	var chosen []UpgradeChoice
	p := newUpgradePrompt(m, offer, func(c UpgradeChoice) { chosen = append(chosen, c) })
	p.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return p, &chosen, h
}

func answerPrompt(p *upgradePrompt, k string) (tea.Model, tea.Cmd) {
	if k == "esc" {
		return p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	}
	return p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
}

func runCmd(cmd tea.Cmd) {
	if cmd != nil {
		cmd()
	}
}

func TestUpgradePromptYesQuitsAndReportsTheUpgrade(t *testing.T) {
	p, chosen, _ := newPromptFixture(t, brewOffer)
	next, cmd := answerPrompt(p, "y")
	if next != p.inner {
		t.Fatalf("y should hand back the interface, got %T", next)
	}
	if cmd == nil {
		t.Fatal("y should quit so the caller can run the upgrade")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("y should quit so the caller can run the upgrade")
	}
	if !p.inner.Snapshot().Upgrade {
		t.Error("Snapshot.Upgrade should carry the answer back to the caller")
	}
	if len(*chosen) != 0 {
		t.Errorf("upgrade now is the caller's to record, got %v", *chosen)
	}
}

func TestUpgradePromptNotNowSnoozesAndKeepsTheBadge(t *testing.T) {
	for _, k := range []string{"n", "esc"} {
		p, chosen, _ := newPromptFixture(t, brewOffer)
		next, cmd := answerPrompt(p, k)
		runCmd(cmd)
		if next != p.inner {
			t.Fatalf("%s should hand back the interface, got %T", k, next)
		}
		if len(*chosen) != 1 || (*chosen)[0] != UpgradeLater {
			t.Errorf("%s recorded %v, want not now", k, *chosen)
		}
		if want := "Not now · asks again Sat 17 Oct · the badge stays"; p.inner.message != want {
			t.Errorf("message = %q, want %q", p.inner.message, want)
		}
		if p.inner.updateBadge != "0.11.0" {
			t.Error("not now should keep the header badge")
		}
	}
}

func TestUpgradePromptSkipHidesTheBadge(t *testing.T) {
	p, chosen, _ := newPromptFixture(t, brewOffer)
	_, cmd := answerPrompt(p, "s")
	runCmd(cmd)
	if len(*chosen) != 1 || (*chosen)[0] != UpgradeSkip {
		t.Errorf("s recorded %v, want skip", *chosen)
	}
	if p.inner.updateBadge != "" {
		t.Error("skipping a release should hide its badge")
	}
}

func TestUpgradePromptOffersOnlyWhatTheInstallMethodAllows(t *testing.T) {
	winget := brewOffer
	winget.Command, winget.Runs, winget.Why = "winget upgrade vsfleet", false, "Windows can't replace a vsfleet.exe that is running."
	p, chosen, h := newPromptFixture(t, winget)

	if next, _ := answerPrompt(p, "y"); next != p {
		t.Fatal("y must do nothing where vsfleet cannot run the upgrade itself")
	}
	view := ansi.Strip(p.View())
	for _, want := range []string{"Run after quitting:  winget upgrade vsfleet", "Windows can't replace", "c copy command"} {
		if !strings.Contains(view, want) {
			t.Errorf("prompt is missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "upgrade now") {
		t.Errorf("prompt offers an upgrade it cannot run:\n%s", view)
	}

	_, cmd := answerPrompt(p, "c")
	runCmd(cmd)
	if len(h.copied) != 1 || h.copied[0] != "winget upgrade vsfleet" {
		t.Errorf("copied %v, want the winget command", h.copied)
	}
	if len(*chosen) != 1 || (*chosen)[0] != UpgradeLater {
		t.Errorf("copying recorded %v, want not now", *chosen)
	}
}

func TestUpgradePromptWithoutACommandCopiesTheReleasePage(t *testing.T) {
	pkg := brewOffer
	pkg.Command, pkg.Runs, pkg.Why = "", false, "Installing it needs sudo, which vsfleet never runs."
	p, _, h := newPromptFixture(t, pkg)
	if !strings.Contains(ansi.Strip(p.View()), "c copy link") {
		t.Errorf("prompt should offer the link:\n%s", ansi.Strip(p.View()))
	}
	_, cmd := answerPrompt(p, "c")
	runCmd(cmd)
	if len(h.copied) != 1 || h.copied[0] != pkg.URL {
		t.Errorf("copied %v, want the release page", h.copied)
	}
}

func TestUpgradePromptDrawsOverAFadedInterface(t *testing.T) {
	p, _, _ := newPromptFixture(t, brewOffer)
	view := p.View()
	plain := ansi.Strip(view)
	for _, want := range []string{"vsfleet 0.11.0 is available (you have 0.10.0)", "github.com/Easonliuuuuu/vsfleet/releases/tag/v0.11.0", "This runs:  brew upgrade vsfleet", "y upgrade now", "n not now", "s skip this version"} {
		if !strings.Contains(plain, want) {
			t.Errorf("prompt is missing %q:\n%s", want, plain)
		}
	}
	lines := strings.Split(view, "\n")
	if len(lines) != 30 {
		t.Fatalf("prompt view has %d lines, want the terminal's 30", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 120 {
			t.Errorf("line %d is %d cells, wider than the terminal", i, w)
		}
	}
}

func TestUpgradePromptStepsAsideInASmallTerminal(t *testing.T) {
	p, _, _ := newPromptFixture(t, brewOffer)
	if next, _ := p.Update(tea.WindowSizeMsg{Width: upgradeBoxWidth - 1, Height: 30}); next != p.inner {
		t.Fatalf("a terminal narrower than the prompt should get the interface, got %T", next)
	}
}

func TestWelcomeHandsOverToTheUpgradePrompt(t *testing.T) {
	p, _, _ := newPromptFixture(t, brewOffer)
	w := newWelcomeModel(p.inner, updatedWelcome)
	w.next = p
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if next, _ := w.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}); next != p {
		t.Fatalf("skipping the welcome should show the prompt, got %T", next)
	}
	if p.inner.upgradeNow {
		t.Error("the key that skipped the welcome must not also answer the prompt")
	}
}

func TestBackgroundCheckOnlyLightsTheBadge(t *testing.T) {
	b := &fakeBackend{contexts: []*config.Context{ctx("prod", "https://vcsa.prod.internal")}}
	m := newTestModel(t, b, Options{Current: "prod"})
	m.Update(updateCheckedMsg{latest: "0.11.0"})
	if !strings.Contains(ansi.Strip(m.View()), "↑ v0.11.0 available") {
		t.Errorf("header should show the release found mid-session:\n%s", ansi.Strip(m.View()))
	}
	m.Update(updateCheckedMsg{})
	if m.updateBadge != "0.11.0" {
		t.Error("an empty answer (a failed check) should not clear what is already known")
	}
}
