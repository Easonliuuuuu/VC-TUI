package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// perfCapableBackend is a fake that also offers the vApp chart extension, which
// is what puts "</> range" and "s sort" on the vApp workspace's key line.
type perfCapableBackend struct{ *fakeBackend }

func (perfCapableBackend) VMsPerfSeries(context.Context, *config.Context, []vsphere.VM, time.Duration, int, time.Time) ([]vsphere.VMSeriesResult, error) {
	return nil, nil
}

type footerVariant struct {
	name  string
	build func(t *testing.T) *Model
	// modal marks a footer that lists only the keys of a popup or prompt that
	// has taken over its screen. "?" still opens the help over it, but the
	// line is about the modal's own keys, so it is not required to say so.
	modal bool
}

// footerVariants is every key line the interface can show: each mode, each
// history pane with and without a capture-capable service, and the states that
// swap a screen's footer for another (a focused input, a popup, a prompt, the
// demo's reduced Contexts screen, the VM and vApp dashboards).
func footerVariants() []footerVariant {
	plain := func(setup func(m *Model)) func(t *testing.T) *Model {
		return func(t *testing.T) *Model {
			m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
			setup(m)
			return m
		}
	}
	var out []footerVariant
	for md := modeBrowse; md <= modeSwitchPGDetail; md++ {
		out = append(out, footerVariant{name: fmt.Sprintf("mode %d", md), build: plain(func(m *Model) { m.mode = md })})
	}
	for _, pane := range []int{historyPaneChanges, historyPaneTrends, historyPaneRuns, historyPaneHealth} {
		out = append(out,
			footerVariant{name: fmt.Sprintf("history pane %d, capture", pane), build: func(t *testing.T) *Model {
				m := captureCapableModel(t)
				m.mode, m.historyPane = modeChanges, pane
				return m
			}},
			footerVariant{name: fmt.Sprintf("history pane %d, store only", pane), build: plain(func(m *Model) {
				m.mode, m.historyPane = modeChanges, pane
			})})
	}
	out = append(out,
		footerVariant{name: "detail, action popup", build: plain(func(m *Model) {
			m.mode = modeDetail
			m.actions = &actionList{items: []action{{label: "Copy name"}}}
		}), modal: true},
		footerVariant{name: "vapp member popup", build: plain(func(m *Model) {
			m.mode = modeVAppVMDetail
			m.actions = &actionList{items: []action{{label: "Copy name"}}}
		}), modal: true},
		footerVariant{name: "vapp with charts", build: func(t *testing.T) *Model {
			m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
			m.backend = perfCapableBackend{twoHealthy()}
			m.mode = modeVAppDetail
			return m
		}},
		footerVariant{name: "switch wiring page", build: plain(func(m *Model) {
			m.mode, m.sw = modeSwitchDetail, &switchWorkspace{page: 1}
		})},
		footerVariant{name: "demo contexts", build: func(t *testing.T) *Model {
			m := newTestModel(t, twoHealthy(), Options{Current: "prod", Demo: true})
			m.mode = modeContexts
			return m
		}},
		footerVariant{name: "browse filter input", build: plain(func(m *Model) { m.filtering = true })},
		footerVariant{name: "search input", build: plain(func(m *Model) { m.mode, m.filtering = modeSearch, true })},
		footerVariant{name: "datastore filter input", build: plain(func(m *Model) { m.mode, m.filtering = modeDatastoreFiles, true })},
		footerVariant{name: "changes filter input", build: plain(func(m *Model) { m.mode, m.filtering = modeChanges, true })},
		footerVariant{name: "datastore files popup", build: plain(func(m *Model) {
			m.mode = modeDatastoreFiles
			m.actions = &actionList{items: []action{{label: "Copy path"}}}
		}), modal: true},
		footerVariant{name: "datastore find prompt", build: plain(func(m *Model) {
			m.mode, m.ds = modeDatastoreFind, &dsWorkspace{findPrompt: true}
		}), modal: true},
		footerVariant{name: "credential prompt", build: plain(func(m *Model) { m.credPrompt = &credPromptState{} })},
		footerVariant{name: "ssh prompt", build: plain(func(m *Model) { m.sshPrompt = &sshPromptState{} })},
	)
	return out
}

// helpCovers reports whether the help panel names a key by its label, or, for
// a combined label such as "b/t" or "</>", every key in it.
func helpCovers(help []string, label string) bool {
	has := func(l string) bool {
		for _, line := range help {
			if strings.HasPrefix(strings.TrimSpace(line), l+" ") {
				return true
			}
		}
		return false
	}
	if has(label) {
		return true
	}
	parts := strings.FieldsFunc(label, func(r rune) bool { return r == '/' })
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if !has(p) {
			return false
		}
	}
	return true
}

func hintText(b key.Binding) string {
	h := b.Help()
	return h.Key + " " + h.Desc
}

// TestEveryFooterFitsAndKeepsBackAndHelp renders the key line of every screen
// at the narrowest supported width and two common ones. Each must be a single
// untruncated line that keeps "esc back" (or the screen's own cancel) and
// "? help" wherever the screen has them, offers "? help" on every screen where
// "?" really opens it, and leaves nothing behind that "?" cannot explain: a
// hint the line had to drop is in the help panel of that screen.
func TestEveryFooterFitsAndKeepsBackAndHelp(t *testing.T) {
	keys := defaultKeys()
	for _, width := range []int{60, 80, 100} {
		for _, v := range footerVariants() {
			t.Run(fmt.Sprintf("%s at %d", v.name, width), func(t *testing.T) {
				m := v.build(t)
				m.width, m.height = width, 24
				full := m.keys.footerHints(m)
				got := footerText(m)

				if strings.Contains(got, "\n") || strings.Contains(got, "…") {
					t.Fatalf("footer is cut or spans lines: %q", got)
				}
				if w := ansi.StringWidth(got); w > width {
					t.Fatalf("footer is %d columns wide, want at most %d: %q", w, width, got)
				}
				var dropped []key.Binding
				for _, b := range full {
					if strings.Contains(got, hintText(b)) {
						continue
					}
					if sameHint(b, keys.Back) || sameHint(b, keys.CancelAction) || sameHint(b, keys.Help) {
						t.Errorf("footer lost %q: %q", hintText(b), got)
					}
					dropped = append(dropped, b)
				}

				// "?" must be offered wherever it opens the help.
				hasHelp := false
				for _, b := range full {
					hasHelp = hasHelp || sameHint(b, keys.Help)
				}
				press(t, m, "?")
				if m.mode != modeHelp {
					return
				}
				if !hasHelp && !v.modal {
					t.Errorf("%q opens help but does not advertise it", got)
				}
				help := make([]string, 0, 32)
				// One column, so every key starts its own line.
				m.width = 60
				for _, l := range m.helpLines() {
					help = append(help, ansi.Strip(l))
				}
				for _, b := range dropped {
					if !helpCovers(help, b.Help().Key) {
						t.Errorf("dropped hint %q is not in this screen's help:\n%s", hintText(b), strings.Join(help, "\n"))
					}
				}
			})
		}
	}
}

// TestFooterDropsLeastEssentialHintsFirst pins the order the line gives
// things up in, on the two screens the narrow width used to truncate: the Runs
// pane keeps its label and note keys and loses pin, and the Changes pane keeps
// the pane switch and capture and loses the filter and clip.
func TestFooterDropsLeastEssentialHintsFirst(t *testing.T) {
	m := captureCapableModel(t)
	m.width, m.height = 60, 24

	m.mode, m.historyPane = modeChanges, historyPaneRuns
	requireFooter(t, m, []string{"e label", "N note", "tab next pane", "n capture", "esc back", "? help"}, []string{"p pin", "q quit", "↑/k up"})

	m.historyPane = historyPaneChanges
	requireFooter(t, m, []string{"←/→ move", "b/t end", "tab panes", "n capture", "esc back", "? help"}, []string{"1-4 impact", "c clip"})
}
