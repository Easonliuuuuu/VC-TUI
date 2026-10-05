package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestConnectionFailureHasOneOperatorLine(t *testing.T) {
	for _, all := range []bool{false, true} {
		b := twoHealthy()
		b.failures = map[string]error{"customer-a": errors.New("socks5 proxy <dynamic>: connection refused")}
		current := "customer-a"
		if all {
			current = "prod"
		}
		m := newTestModel(t, b, Options{Current: current, AllContexts: all})
		press(t, m, "R")
		for _, width := range []int{60, 80, 100, 140} {
			m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
			out := ansi.Strip(m.View())
			if strings.Count(out, "customer-a: connection failed") != 1 || strings.Contains(out, "proxy") || !strings.Contains(out, "d diagnose") {
				t.Fatalf("all=%v width=%d: failure must have one operator line and a recovery hint:\n%s", all, width, out)
			}
			if all && !strings.Contains(out, "c select") {
				t.Fatalf("missing context selection hint: %s", out)
			}
		}
		if all {
			press(t, m, "c")
			press(t, m, "k")
		}
		press(t, m, "d")
		if m.doctor == nil || m.doctor.cc.Name != "customer-a" {
			t.Fatal("hint did not reach the failed vCenter's diagnosis")
		}
	}
}

func TestCredentialCancellationRetainsOneRetryLine(t *testing.T) {
	b := twoHealthy()
	b.failures = map[string]error{"customer-a": errPromptCanceled}
	m := newTestModel(t, b, Options{Current: "customer-a"})
	out := ansi.Strip(m.View())
	if strings.Count(out, "credential entry canceled") != 1 || !strings.Contains(out, "r retry") || strings.Contains(out, "connection failed") {
		t.Fatalf("cancellation must retain one explicit retry line: %s", out)
	}
	press(t, m, "?")
	press(t, m, "esc")
	if !strings.Contains(m.View(), "credential entry canceled") {
		t.Fatal("cancellation disappeared after leaving help")
	}
	delete(b.failures, "customer-a")
	press(t, m, "r")
	if strings.Contains(m.View(), "credential entry canceled") || len(m.rows()) == 0 {
		t.Fatal("retry did not replace the cancelled result")
	}
}
