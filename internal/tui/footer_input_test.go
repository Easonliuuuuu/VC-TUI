package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
)

// footerText is the key line as a reader sees it.
func footerText(m *Model) string {
	return ansi.Strip(m.viewKeys())
}

// requireFooter asserts which hints the key line carries. A hint that cannot
// work while an input has focus is worse than a missing one: it sends the
// reader to a key that types a letter.
func requireFooter(t *testing.T, m *Model, want, forbid []string) {
	t.Helper()
	got := footerText(m)
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("footer %q is missing %q", got, w)
		}
	}
	for _, f := range forbid {
		if strings.Contains(got, f) {
			t.Errorf("footer %q still advertises %q", got, f)
		}
	}
}

// TestFooterWhileTypingAFilterListsOnlyWhatWorks pins the browse filter. The
// footer used to keep saying "1-7 kind  H history  c contexts" while those
// keys were being typed into the query.
func TestFooterWhileTypingAFilterListsOnlyWhatWorks(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	requireFooter(t, m, []string{"H history", "c contexts"}, nil)

	press(t, m, "/")
	if !m.filtering {
		t.Fatal(`"/" did not focus the filter`)
	}
	requireFooter(t, m,
		[]string{"enter apply", "esc clear", "tab search all"},
		[]string{"1-7 kind", "H history", "contexts", "reload", "help", "quit"})

	// The advertised keys are the ones that work: "H" is text, not history.
	press(t, m, "H")
	if m.mode != modeBrowse || m.filter.Value() != "H" {
		t.Fatalf(`"H" in the filter: mode=%v value=%q, want it typed`, m.mode, m.filter.Value())
	}

	press(t, m, "enter")
	requireFooter(t, m, []string{"H history"}, []string{"enter apply"})
}

func TestFooterInSearchInputListsOnlyWhatWorks(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "tab")
	if m.mode != modeSearch || !m.filtering {
		t.Fatalf("tab did not open the search with its input focused: mode=%v filtering=%v", m.mode, m.filtering)
	}
	requireFooter(t, m,
		[]string{"enter apply", "esc back"},
		[]string{"filter", "sort", "reload", "help", "quit", "open"})

	typeText(t, m, "app")
	press(t, m, "enter")
	requireFooter(t, m, []string{"open", "sort", "reload", "? help"}, []string{"enter apply"})
}

func TestFooterInDatastoreInputsListsOnlyWhatWorks(t *testing.T) {
	b := browsing()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	openBrowser(t, m)
	requireFooter(t, m, []string{"f find in datastore", "y copy datastore path"}, nil)

	// The directory filter.
	press(t, m, "/")
	requireFooter(t, m,
		[]string{"enter apply", "esc clear"},
		[]string{"find in datastore", "copy datastore path", "quit"})
	press(t, m, "esc")

	// The Find prompt: "f" and "y" are letters of the query.
	press(t, m, "f")
	if m.mode != modeDatastoreFind || !m.ds.findPrompt {
		t.Fatalf(`"f" did not open the Find prompt: mode=%v`, m.mode)
	}
	requireFooter(t, m,
		[]string{"enter search", "esc back"},
		[]string{"find in datastore", "copy datastore path", "open", "quit"})
	press(t, m, "f", "y")
	if got := m.ds.find.Value(); got != "fy" {
		t.Fatalf("the Find prompt holds %q, want the typed letters", got)
	}
}

func TestFooterWhileEditingARunListsOnlyWhatWorks(t *testing.T) {
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := newTestModel(t, twoHealthy(), Options{Assessment: &assessment.Service{Store: store}})
	m.mode, m.historyPane = modeChanges, historyPaneRuns
	m.runs = []assessment.Run{{ID: 7, Label: "quarter close"}}

	press(t, m, "e")
	if m.mode != modeHistoryRunEdit {
		t.Fatalf(`"e" did not open the label editor: mode=%v`, m.mode)
	}
	requireFooter(t, m, []string{"enter save", "esc cancel"}, []string{"help", "quit"})
	press(t, m, "?", "q")
	if m.mode != modeHistoryRunEdit || !strings.HasSuffix(m.runEditInput.Value(), "?q") {
		t.Fatalf(`"?" and "q" did not reach the editor: mode=%v value=%q`, m.mode, m.runEditInput.Value())
	}
}

func TestFooterUnderCredentialAndSSHPromptsListsOnlyTheirKeys(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})

	m.credPrompt = &credPromptState{}
	requireFooter(t, m, []string{"enter continue", "esc cancel", "ctrl+c quit"}, []string{"kind", "history", "filter"})
	m.credPrompt = nil

	m.sshPrompt = &sshPromptState{}
	requireFooter(t, m, []string{"enter continue", "tab switch field", "esc cancel", "ctrl+c quit"}, []string{"kind", "history", "filter"})
}
