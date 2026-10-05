package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
)

// monochrome renders as NO_COLOR does: lipgloss strips every colour, the
// background and the bold. Only glyphs and text survive, so a test run under
// it proves a state is visible without the highlight.
func monochrome(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// cursorLines is every line of a rendered frame that starts with the cursor
// glyph, in the plain text a monochrome terminal would show.
func cursorLines(view string) []string {
	var out []string
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		if strings.HasPrefix(line, glyphCursor) {
			out = append(out, line)
		}
	}
	return out
}

// onlyCursorLine fails unless exactly one line carries the cursor and returns
// it. "Exactly one" is the point: a cursor that also appears on other rows
// (a folder glyph, a sticky marker) marks nothing.
func onlyCursorLine(t *testing.T, view string) string {
	t.Helper()
	got := cursorLines(view)
	if len(got) != 1 {
		t.Fatalf("want exactly one line starting with %q, got %d: %q\n%s", glyphCursor, len(got), got, ansi.Strip(view))
	}
	return got[0]
}

// TestBrowseCursorIsVisibleWithoutColor pins the cursor under NO_COLOR. The
// selected row used to be marked by a background and bold alone, so with the
// colour profile stripped nothing on the screen said which VM Enter would open.
func TestBrowseCursorIsVisibleWithoutColor(t *testing.T) {
	monochrome(t)
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	m.width, m.height = 80, 24

	first := onlyCursorLine(t, m.View())
	if !strings.Contains(first, "app-01") {
		t.Fatalf("cursor starts on %q, want the first row", first)
	}
	// The status glyph keeps its own column next to the cursor.
	if !strings.HasPrefix(first, glyphCursor+glyphOnline) {
		t.Errorf("cursor row lost its status glyph: %q", first)
	}

	press(t, m, "down")
	second := onlyCursorLine(t, m.View())
	if !strings.Contains(second, "build-runner-3") || !strings.HasPrefix(second, glyphCursor+glyphOffline) {
		t.Fatalf("cursor did not follow the selection: %q", second)
	}
}

// TestColumnHeadingsStayAlignedWithTheCursorGutter keeps the heading over its
// column on a cursor row and on one without.
func TestColumnHeadingsStayAlignedWithTheCursorGutter(t *testing.T) {
	monochrome(t)
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	m.width, m.height = 100, 24
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	var head, cursorRow, plainRow string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "NAME") && strings.Contains(l, "POWER"):
			head = l
		case strings.Contains(l, "app-01"):
			cursorRow = l
		case strings.Contains(l, "build-runner-3"):
			plainRow = l
		}
	}
	if head == "" || cursorRow == "" || plainRow == "" {
		t.Fatalf("browse frame is missing rows:\n%s", strings.Join(lines, "\n"))
	}
	// Columns, not byte offsets: the cursor and status glyphs are multi-byte.
	col := func(line, sub string) int { return ansi.StringWidth(line[:strings.Index(line, sub)]) }
	if h, c, p := col(head, "POWER"), col(cursorRow, " on "), col(plainRow, " off"); h != c+1 || h != p+1 {
		t.Errorf("POWER heading at %d but values at %d and %d:\n%s\n%s\n%s", h, c, p, head, cursorRow, plainRow)
	}
}

func TestSearchCursorIsVisibleWithoutColor(t *testing.T) {
	monochrome(t)
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "tab")
	typeText(t, m, "app")
	press(t, m, "enter")
	if m.mode != modeSearch {
		t.Fatalf("mode=%v, want the search results", m.mode)
	}
	if line := onlyCursorLine(t, m.View()); !strings.Contains(line, "app") {
		t.Fatalf("search cursor is on %q", line)
	}
}

// TestDatastoreFileCursorIsVisibleWithoutColor also pins the folder glyph: it
// used to be the cursor glyph itself, so every folder looked selected.
func TestDatastoreFileCursorIsVisibleWithoutColor(t *testing.T) {
	monochrome(t)
	b := browsing()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	openBrowser(t, m)

	line := onlyCursorLine(t, m.View())
	if !strings.Contains(line, "ISO/") {
		t.Fatalf("file cursor starts on %q, want the first folder", line)
	}
	press(t, m, "down")
	if line := onlyCursorLine(t, m.View()); !strings.Contains(line, "database01/") {
		t.Fatalf("file cursor did not move: %q", line)
	}
}

// TestCursorMarkIsAGlyphNotAStyle is the shared helper behind every table
// row (browse, search, vApp members, datastore files).
func TestCursorMarkIsAGlyphNotAStyle(t *testing.T) {
	monochrome(t)
	m := &Model{theme: newTheme()}
	if got := ansi.Strip(m.cursorMark(true)); got != glyphCursor {
		t.Fatalf("selected mark = %q, want %q", got, glyphCursor)
	}
	if got := ansi.Strip(m.cursorMark(false)); got != " " {
		t.Fatalf("unselected mark = %q, want a blank", got)
	}
}

func TestHistoryListCursorsAreVisibleWithoutColor(t *testing.T) {
	monochrome(t)
	m := newTestModel(t, twoHealthy(), Options{})
	m.width, m.height = 100, 30
	m.mode = modeChanges
	m.runs = []assessment.Run{{ID: 3, Label: "newest"}, {ID: 2}, {ID: 1}}
	m.runCursor = 1

	m.historyPane = historyPaneRuns
	if line := onlyCursorLine(t, strings.Join(m.viewHistoryHubRuns(), "\n")); !strings.Contains(line, "#2") {
		t.Errorf("Runs pane cursor is on %q, want run #2", line)
	}

	m.mode = modeHistoryRuns
	m.baseRun, m.targetRun, m.pickerRole = 1, 3, "target"
	line := onlyCursorLine(t, strings.Join(m.viewHistoryRuns(), "\n"))
	if !strings.Contains(line, "#2") {
		t.Errorf("run picker cursor is on %q, want run #2", line)
	}
	// The B/T roles are still shown next to rows, and apart from the cursor.
	plain := ansi.Strip(strings.Join(m.viewHistoryRuns(), "\n"))
	if !strings.Contains(plain, " T #3") || !strings.Contains(plain, " B #1") {
		t.Errorf("run picker lost its baseline/target markers:\n%s", plain)
	}
}

func TestChangeStreamCursorIsVisibleWithoutColor(t *testing.T) {
	monochrome(t)
	base := []assessment.Observation{vmObservation("aaa-renamed", 2, 4096), vmObservation("zzz-doomed", 2, 4096)}
	target := []assessment.Observation{vmObservation("aaa-renamed", 2, 4096)}
	target[0].VM.Name = "aaa-renamed-now"
	store := twoRunStore(t, base, target)
	m := newTestModel(t, twoHealthy(), Options{Current: "prod", Assessment: &assessment.Service{Store: store}})
	press(t, m, "H")

	if line := onlyCursorLine(t, strings.Join(m.viewChanges(), "\n")); !strings.Contains(line, "zzz-doomed") {
		t.Fatalf("change cursor is on %q, want the first (blocking) row", line)
	}
	press(t, m, "down")
	if line := onlyCursorLine(t, strings.Join(m.viewChanges(), "\n")); strings.Contains(line, "zzz-doomed") {
		t.Fatalf("change cursor did not move: %q", line)
	}
}
