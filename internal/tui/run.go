package tui

import (
	"context"
	"errors"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

type terminalFile interface {
	Fd() uintptr
}

func isTerminal(v any) bool {
	f, ok := v.(terminalFile)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// Run starts the interface and blocks until the operator quits. The alternate
// screen is used so the terminal is left exactly as it was found.
//
// The returned Snapshot reflects wherever the interface was left — even on
// error, since a canceled context still exits through the same path and the
// caller decides for itself whether a partial run is worth remembering.
func Run(ctx context.Context, b Backend, opts Options) (Snapshot, error) {
	in := opts.In
	if in == nil {
		in = os.Stdin
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	if !isTerminal(in) || !isTerminal(out) {
		return Snapshot{}, errors.New("terminal interface requires a terminal")
	}

	m := New(ctx, b, opts)
	var model tea.Model = m
	if opts.Welcome.Kind != WelcomeNone {
		model = newWelcomeModel(m, opts.Welcome)
	}
	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithContext(ctx),
		tea.WithInput(in),
		tea.WithOutput(out),
	)
	final, err := p.Run()
	// Quitting with ctrl+c while the welcome is still playing leaves it as the
	// program's model; the interface underneath is what has a position.
	if wm, ok := final.(*welcomeModel); ok {
		final = wm.inner
	}
	if fm, ok := final.(*Model); ok {
		return fm.Snapshot(), err
	}
	return Snapshot{}, err
}
