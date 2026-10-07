package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/config"
)

// logoutFakeBackend adds the production-only logout to fakeBackend.
type logoutFakeBackend struct {
	*fakeBackend
	loggedOut []string
}

func (b *logoutFakeBackend) Logout(_ context.Context, cc *config.Context) error {
	b.loggedOut = append(b.loggedOut, cc.Name)
	return nil
}

func loggedOutProd(t *testing.T) (*Model, *logoutFakeBackend) {
	t.Helper()
	b := &logoutFakeBackend{fakeBackend: twoHealthy()}
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "c")
	m.ctxCursor = m.selected
	press(t, m, "o")
	if len(b.loggedOut) != 1 || b.loggedOut[0] != "prod" {
		t.Fatalf("o on the contexts screen should log out of prod, logged out %v", b.loggedOut)
	}
	return m, b
}

func TestLogoutKeepsTheRowsAndSaysSo(t *testing.T) {
	m, _ := loggedOutProd(t)
	st := m.byName["prod"]
	if !st.loggedOut || st.inv == nil {
		t.Fatalf("logout should keep the last inventory and mark the context, loggedOut=%v inv=%v", st.loggedOut, st.inv != nil)
	}
	if _, l := lineWith(m.View(), "vcsa.prod.internal"); !strings.Contains(l, "logged out") {
		t.Errorf("the contexts screen should say prod is logged out, got %q", l)
	}
	if strings.Contains(ansi.Strip(m.View()), "1 connected") {
		t.Errorf("a logged-out context should not count as connected:\n%s", ansi.Strip(m.View()))
	}
}

func TestBackgroundRefreshDoesNotUndoALogout(t *testing.T) {
	m, b := loggedOutProd(t)
	m.refreshInterval = time.Minute
	for _, s := range m.states {
		s.loadedAt = time.Now().Add(-2 * time.Minute)
	}
	before := b.calls["prod"]
	tickRefresh(t, m)
	if b.calls["prod"] != before {
		t.Error("a background refresh logged back in to a context the operator logged out of")
	}
}

func TestSelectingALoggedOutContextLogsIn(t *testing.T) {
	m, b := loggedOutProd(t)
	before := b.calls["prod"]
	press(t, m, "enter")
	if b.calls["prod"] != before+1 {
		t.Fatalf("selecting a logged-out context should load it again, calls %d -> %d", before, b.calls["prod"])
	}
	if m.byName["prod"].loggedOut {
		t.Error("an explicit load should clear the logout")
	}
}

func TestLogoutIsNotOfferedWithoutASession(t *testing.T) {
	b := twoHealthy()
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "c", "o")
	if m.byName["prod"].loggedOut {
		t.Error("a backend with no sessions has nothing to log out of")
	}
}
