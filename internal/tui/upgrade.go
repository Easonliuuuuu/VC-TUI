package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// UpgradeOffer is a newer release to offer at launch. The caller decides
// whether one is due — a release is known, it is not skipped, "not now" has
// run out — and how this copy was installed; the interface only asks.
type UpgradeOffer struct {
	// Current and Latest are plain versions, "0.10.0" and "0.11.0".
	Current string
	Latest  string
	// URL is the release notes page.
	URL string
	// Command upgrades vsfleet, as the operator would type it, or "" where
	// there is no single command to give.
	Command string
	// Runs reports whether "y" may hand Command back to the caller to run.
	// When false, Why says why, and the prompt offers to copy the command (or
	// the release page) instead.
	Runs bool
	Why  string
	// AskAgain is when "not now" asks next, for the message line.
	AskAgain time.Time
}

// UpgradeChoice is what the operator answered.
type UpgradeChoice int

const (
	// UpgradeNow means quit and run the upgrade. Snapshot.Upgrade carries it
	// back to the caller, which owns running a process outside the interface.
	UpgradeNow UpgradeChoice = iota + 1
	// UpgradeLater is "not now", and also what copying the command counts as.
	UpgradeLater
	// UpgradeSkip never asks about this release again.
	UpgradeSkip
)

// The prompt is a fixed-size box; a terminal it does not fit just keeps the
// header badge, and the prompt waits for a larger one.
const (
	upgradeBoxWidth = 60
	upgradeMinRows  = 14
)

// upgradePrompt asks about an UpgradeOffer in front of the interface, the
// same way the welcome plays in front of it: everything but a key goes to the
// interface underneath, and an answer hands Bubble Tea the interface itself.
// It never appears in the middle of a session — only at launch.
type upgradePrompt struct {
	inner  *Model
	offer  UpgradeOffer
	chosen func(UpgradeChoice)
	width  int
	height int
	sized  bool
}

func newUpgradePrompt(inner *Model, offer UpgradeOffer, chosen func(UpgradeChoice)) *upgradePrompt {
	return &upgradePrompt{inner: inner, offer: offer, chosen: chosen}
}

func (p *upgradePrompt) base() *Model { return p.inner }

func (p *upgradePrompt) Init() tea.Cmd { return p.inner.Init() }

func (p *upgradePrompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height, p.sized = msg.Width, msg.Height, true
		_, cmd := p.inner.Update(msg)
		if msg.Width < upgradeBoxWidth || msg.Height < upgradeMinRows {
			return p.inner, cmd
		}
		return p, cmd
	case tea.KeyMsg:
		return p.answer(msg)
	case tea.MouseMsg:
		return p, nil
	}
	_, cmd := p.inner.Update(msg)
	return p, cmd
}

func (p *upgradePrompt) answer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := p.inner
	again := p.offer.AskAgain.Format("Mon 2 Jan")
	switch msg.String() {
	case "ctrl+c":
		return m.Update(msg)
	case "y":
		if !p.offer.Runs {
			return p, nil
		}
		m.upgradeNow = true
		m.quitting = true
		return m, tea.Quit
	case "n", "esc":
		m.setMessage("Not now · asks again "+again+" · the badge stays", false)
		return m, p.record(UpgradeLater)
	case "s":
		m.updateBadge = ""
		m.setMessage("Skipped "+displayVersion(p.offer.Latest)+" · the next release will ask again", false)
		return m, p.record(UpgradeSkip)
	case "c":
		if p.offer.Runs {
			return p, nil
		}
		text := p.copyText()
		if err := m.handoff.Copy(text); err != nil {
			m.setMessage("Could not copy: "+err.Error(), true)
		} else {
			m.setMessage("Copied "+text+" · asks again "+again, false)
		}
		return m, p.record(UpgradeLater)
	}
	return p, nil
}

func (p *upgradePrompt) copyText() string {
	if p.offer.Command != "" {
		return p.offer.Command
	}
	return p.offer.URL
}

// record hands the answer to the caller to persist, off the update loop.
func (p *upgradePrompt) record(c UpgradeChoice) tea.Cmd {
	if p.chosen == nil {
		return nil
	}
	return func() tea.Msg {
		p.chosen(c)
		return nil
	}
}

func (p *upgradePrompt) View() string {
	if !p.sized {
		return ""
	}
	return overlay(p.inner.View(), p.box(), p.width, p.height, p.inner.theme.faint)
}

func (p *upgradePrompt) box() string {
	t := p.inner.theme
	bright := t.text.Bold(true)
	inner := upgradeBoxWidth - 6
	o := p.offer
	fit := func(s string) string { return ansi.Truncate(s, inner, "…") }

	head := "vsfleet " + o.Latest + " is available"
	lines := []string{
		fit(bright.Render(head) + t.dim.Render(" (you have "+o.Current+")")),
		fit(t.dim.Render(strings.TrimPrefix(o.URL, "https://"))),
		"",
	}
	switch {
	case o.Runs:
		lines = append(lines, fit(t.dim.Render("This runs:  ")+bright.Render(o.Command)))
	case o.Command != "":
		lines = append(lines, fit(t.dim.Render("Run after quitting:  ")+bright.Render(o.Command)))
	default:
		lines = append(lines, fit(t.text.Render("Download the new release from the page above.")))
	}
	if !o.Runs && o.Why != "" {
		lines = append(lines, fit(t.faint.Render(o.Why)))
	}
	lines = append(lines, "")

	type key struct{ k, label string }
	keys := []key{{"y", "upgrade now"}, {"n", "not now"}, {"s", "skip this version"}}
	if !o.Runs {
		copyLabel := "copy command"
		if o.Command == "" {
			copyLabel = "copy link"
		}
		keys[0] = key{"c", copyLabel}
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = t.title.Render(k.k) + " " + t.dim.Render(k.label)
	}
	lines = append(lines, strings.Join(parts, "   "))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colAccent).
		Padding(1, 2).
		Width(upgradeBoxWidth - 2).
		Render(strings.Join(lines, "\n"))
}

// overlay centres box over a faded copy of bg, a width×height screen.
func overlay(bg, box string, width, height int, fade lipgloss.Style) string {
	rows := strings.Split(bg, "\n")
	for len(rows) < height {
		rows = append(rows, "")
	}
	rows = rows[:height]
	for i, r := range rows {
		rows[i] = ansi.Strip(r)
	}
	boxRows := strings.Split(box, "\n")
	bw := ansi.StringWidth(boxRows[0])
	x0 := max((width-bw)/2, 0)
	y0 := max((height-len(boxRows))/2, 0)
	out := make([]string, len(rows))
	for y, r := range rows {
		by := y - y0
		if by < 0 || by >= len(boxRows) {
			out[y] = fade.Render(r)
			continue
		}
		left := ansi.Truncate(r, x0, "")
		left += strings.Repeat(" ", x0-ansi.StringWidth(left))
		right := ansi.TruncateLeft(r, x0+bw, "")
		out[y] = fade.Render(left) + boxRows[by] + fade.Render(right)
	}
	return strings.Join(out, "\n")
}

// updateCheckedMsg carries the background release check's answer: a newer
// release to show in the header, or "".
type updateCheckedMsg struct{ latest string }

func (m *Model) updateCheckCmd() tea.Cmd {
	if m.checkUpdate == nil {
		return nil
	}
	check, ctx := m.checkUpdate, m.ctx
	return func() tea.Msg { return updateCheckedMsg{latest: check(ctx)} }
}

// updateBadgeText is the header's note of a newer release, or "".
func (m *Model) updateBadgeText() string {
	if m.updateBadge == "" {
		return ""
	}
	return "↑ " + displayVersion(m.updateBadge) + " available"
}

// checkUpdateFunc is the shape of Options.CheckUpdate, named for the field.
type checkUpdateFunc = func(context.Context) string
