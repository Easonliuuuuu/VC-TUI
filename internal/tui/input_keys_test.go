package tui

import (
	"path/filepath"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// freeTextInput is one place the interface takes typed text. open puts the
// model in that state, value reads what the input holds, and active reports
// whether the input is still the one the operator is typing in.
type freeTextInput struct {
	name   string
	open   func(t *testing.T) *Model
	value  func(m *Model) string
	active func(m *Model) bool
	// escapes is how many esc presses leave the input; the key path field
	// steps back to the identity list first. Zero means one.
	escapes int
}

// freeTextInputs is every text input in the interface. A new one belongs in
// this list, and in handleInputKey, so "q" and "?" are letters while it has
// focus (issue #301).
func freeTextInputs() []freeTextInput {
	plain := func(setup func(t *testing.T, m *Model)) func(t *testing.T) *Model {
		return func(t *testing.T) *Model {
			m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
			setup(t, m)
			return m
		}
	}
	browseDatastore := func(t *testing.T) *Model {
		b := browsing()
		m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
		m.backend = b
		openBrowser(t, m)
		return m
	}
	sshField := func(focus sshPromptFocus) func(t *testing.T) *Model {
		return plain(func(t *testing.T, m *Model) {
			openSSHPrompt(t, m, findRow(t, m, vsphere.KindVM, "app-01"))
			m.sshPrompt.focus = focus
			m.sshPrompt.syncFocus()
			m.sshPrompt.pathInput.Cursor.SetMode(cursor.CursorStatic)
			if focus == sshFocusPath {
				m.sshPrompt.pathInput.Focus()
			}
		})
	}
	return []freeTextInput{
		{
			name:   "browse filter",
			open:   plain(func(t *testing.T, m *Model) { press(t, m, "/") }),
			value:  func(m *Model) string { return m.filter.Value() },
			active: func(m *Model) bool { return m.filtering },
		},
		{
			name:   "estate search",
			open:   plain(func(t *testing.T, m *Model) { press(t, m, "tab") }),
			value:  func(m *Model) string { return m.filter.Value() },
			active: func(m *Model) bool { return m.filtering && m.mode == modeSearch },
		},
		{
			name: "history filter",
			open: func(t *testing.T) *Model {
				m := captureCapableModel(t)
				m.mode, m.historyPane = modeChanges, historyPaneChanges
				press(t, m, "/")
				return m
			},
			value:  func(m *Model) string { return m.filter.Value() },
			active: func(m *Model) bool { return m.filtering && m.mode == modeChanges },
		},
		{
			name: "datastore directory filter",
			open: func(t *testing.T) *Model {
				m := browseDatastore(t)
				press(t, m, "/")
				return m
			},
			value:  func(m *Model) string { return m.filter.Value() },
			active: func(m *Model) bool { return m.filtering && m.mode == modeDatastoreFiles },
		},
		{
			name: "datastore find prompt",
			open: func(t *testing.T) *Model {
				m := browseDatastore(t)
				press(t, m, "f")
				return m
			},
			value:  func(m *Model) string { return m.ds.find.Value() },
			active: func(m *Model) bool { return m.mode == modeDatastoreFind && m.ds != nil && m.ds.findPrompt },
		},
		{
			name: "datastore find prompt over results",
			open: func(t *testing.T) *Model {
				b := browsing()
				b.results = []vsphere.DatastoreEntry{file("ubuntu.iso", "[nvme-01] ISO/linux/ubuntu.iso", 3<<30)}
				m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
				m.backend = b
				openBrowser(t, m)
				press(t, m, "f")
				typeText(t, m, "ubuntu")
				press(t, m, "enter")
				press(t, m, "f")
				m.ds.find.SetValue("")
				return m
			},
			value:   func(m *Model) string { return m.ds.find.Value() },
			active:  func(m *Model) bool { return m.mode == modeDatastoreFind && m.ds != nil && m.ds.findPrompt },
			escapes: 1,
		},
		{
			name: "context form",
			open: plain(func(t *testing.T, m *Model) {
				drive(t, m, m.enterForm(nil))
				m.form.cursor = 0
				m.form.syncFocus()
			}),
			value:  func(m *Model) string { return m.form.name.Value() },
			active: func(m *Model) bool { return m.mode == modeForm },
		},
		{
			name: "run label editor",
			open: func(t *testing.T) *Model {
				store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { store.Close() })
				m := newTestModel(t, twoHealthy(), Options{Assessment: &assessment.Service{Store: store}})
				m.mode, m.historyPane = modeChanges, historyPaneRuns
				m.runs = []assessment.Run{{ID: 7}}
				press(t, m, "e")
				return m
			},
			value:  func(m *Model) string { return m.runEditInput.Value() },
			active: func(m *Model) bool { return m.mode == modeHistoryRunEdit },
		},
		{
			name: "credential prompt",
			open: plain(func(t *testing.T, m *Model) {
				m.credPrompt = newCredPromptState(credRequest{label: "lab", resp: make(chan credResult, 1)})
				m.credPrompt.input.Cursor.SetMode(cursor.CursorStatic)
			}),
			value:  func(m *Model) string { return m.credPrompt.input.Value() },
			active: func(m *Model) bool { return m.credPrompt != nil },
		},
		{
			name:   "ssh prompt user",
			open:   sshField(sshFocusUser),
			value:  func(m *Model) string { return m.sshPrompt.input.Value() },
			active: func(m *Model) bool { return m.sshPrompt != nil },
		},
		{
			name:    "ssh prompt key path",
			open:    sshField(sshFocusPath),
			value:   func(m *Model) string { return m.sshPrompt.pathInput.Value() },
			active:  func(m *Model) bool { return m.sshPrompt != nil },
			escapes: 2,
		},
		{
			name:   "ssh prompt proxy address",
			open:   sshField(sshFocusProxyAddr),
			value:  func(m *Model) string { return m.sshPrompt.proxyInput.Value() },
			active: func(m *Model) bool { return m.sshPrompt != nil },
		},
	}
}

// TestFreeTextInputsOwnQuitAndHelpKeys types a query with "q" and "?" in it
// into every free-text input. Each key must land in the value; none may quit
// or open the help. esc still leaves the input and ctrl+c still quits. The
// datastore Find prompt used to lose its "q" and "?" to the global shortcuts,
// so a query like "sql" quit the interface (issue #301).
func TestFreeTextInputsOwnQuitAndHelpKeys(t *testing.T) {
	const typed = "sql?q"
	for _, in := range freeTextInputs() {
		t.Run(in.name, func(t *testing.T) {
			m := in.open(t)
			if !in.active(m) {
				t.Fatalf("setup did not leave the input focused: mode=%v", m.mode)
			}
			before := in.value(m)
			// One key at a time, the way a person types: a single message
			// holding exactly "q" is the one the global shortcut matches.
			for _, r := range typed {
				press(t, m, string(r))
				if m.quitting {
					t.Fatalf("typing %q quit at %q", typed, string(r))
				}
				if m.mode == modeHelp {
					t.Fatalf("typing %q opened the help at %q", typed, string(r))
				}
			}
			if got := in.value(m); got != before+typed {
				t.Fatalf("input holds %q, want %q", got, before+typed)
			}
			if !in.active(m) {
				t.Fatalf("typing left the input: mode=%v", m.mode)
			}

			for i := 0; i < max(1, in.escapes); i++ {
				press(t, m, "esc")
			}
			if m.quitting {
				t.Fatal("esc quit")
			}
			if in.active(m) {
				t.Fatalf("esc did not leave the input: mode=%v", m.mode)
			}
		})
		t.Run(in.name+" ctrl+c", func(t *testing.T) {
			m := in.open(t)
			drive(t, m, discard(m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})))
			if !m.quitting {
				t.Fatal("ctrl+c did not quit")
			}
		})
	}
}
