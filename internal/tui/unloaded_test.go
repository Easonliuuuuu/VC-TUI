package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// threeVCenters is the estate from the issue: one connected vCenter and two
// that nothing has contacted.
func threeVCenters() *fakeBackend {
	return &fakeBackend{
		contexts: []*config.Context{
			ctx("prod", "https://vcsa.prod.internal"),
			ctx("edge-vc", "https://vcsa.edge.internal"),
			ctx("dr-vc", "https://vcsa.dr.internal"),
		},
		inventories: map[string]*vsphere.Inventory{
			"prod":    inventoryFor("prod"),
			"edge-vc": inventoryFor("edge-vc"),
			"dr-vc":   inventoryFor("dr-vc"),
		},
	}
}

var unloadedSizes = []struct{ w, h int }{{60, 20}, {80, 24}, {100, 30}}

func resize(m *Model, w, h int) { m.Update(tea.WindowSizeMsg{Width: w, Height: h}) }

// assertFrame checks what must hold at every size: the frame is exactly the
// terminal's height, no line is wider than the terminal, and the table is
// still on screen.
func assertFrame(t *testing.T, m *Model, w, h int) string {
	t.Helper()
	out := ansi.Strip(m.View())
	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Fatalf("%dx%d: frame is %d lines, want %d:\n%s", w, h, len(lines), h, out)
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > w {
			t.Fatalf("%dx%d: line is %d columns wide: %q", w, h, ansi.StringWidth(l), l)
		}
	}
	if !strings.Contains(out, "app-01") {
		t.Fatalf("%dx%d: the table was pushed off screen:\n%s", w, h, out)
	}
	return out
}

func TestAllScopeNamesVCentersThatAreNotConnected(t *testing.T) {
	b := threeVCenters()
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "a")
	for _, s := range unloadedSizes {
		resize(m, s.w, s.h)
		out := assertFrame(t, m, s.w, s.h)
		for _, want := range []string{"not connected:", "edge-vc, dr-vc", "R connects all"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%dx%d: missing %q:\n%s", s.w, s.h, want, out)
			}
		}
		if strings.Count(out, "not connected") != 1 {
			t.Fatalf("%dx%d: the two vCenters must share one line:\n%s", s.w, s.h, out)
		}
	}
	// The widest size also names the per-vCenter keys.
	resize(m, 100, 30)
	if out := ansi.Strip(m.View()); !strings.Contains(out, "c then enter connects one, d diagnoses") {
		t.Fatalf("missing per-vCenter keys at 100 columns:\n%s", out)
	}
	if b.calls["edge-vc"] != 0 || b.calls["dr-vc"] != 0 {
		t.Fatalf("widening the scope must not connect anything: %v", b.calls)
	}
}

// The keys the line names must do what it says.
func TestUnloadedLineKeysConnectAndDiagnose(t *testing.T) {
	b := threeVCenters()
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "a")
	resize(m, 100, 30)

	// c, move to edge-vc, d: diagnoses that vCenter without connecting it.
	press(t, m, "c", "j", "d")
	if m.doctor == nil || m.doctor.cc.Name != "edge-vc" {
		t.Fatalf("c then d did not diagnose edge-vc: %+v", m.doctor)
	}
	press(t, m, "esc", "esc")
	if m.mode != modeBrowse {
		t.Fatalf("did not return to browse: %v", m.mode)
	}
	if !m.allScope {
		press(t, m, "a")
	}

	// R connects every vCenter, so nothing is left to list.
	press(t, m, "R")
	out := ansi.Strip(m.View())
	if b.calls["edge-vc"] == 0 || b.calls["dr-vc"] == 0 {
		t.Fatalf("R did not connect the missing vCenters: %v", b.calls)
	}
	if strings.Contains(out, "not connected") {
		t.Fatalf("connected vCenters are still listed as missing:\n%s", out)
	}
}

func TestUnloadedLineDistinguishesStates(t *testing.T) {
	b := threeVCenters()
	b.contexts = append(b.contexts, ctx("lab-vc", "https://vcsa.lab.internal"), ctx("old-vc", "https://vcsa.old.internal"))
	b.failures = map[string]error{"old-vc": errors.New("dial tcp: i/o timeout")}
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "a")
	// old-vc has failed, edge-vc is mid-connection, dr-vc needs a password,
	// lab-vc has never been contacted.
	press(t, m, "c", "j", "j", "j", "j", "r")
	press(t, m, "esc")
	if !m.allScope {
		press(t, m, "a")
	}
	m.byName["edge-vc"].loading = true
	m.byName["edge-vc"].phase = phaseAuthenticating
	m.byName["dr-vc"].err = errDeferredCredentialPrompt
	m.byName["dr-vc"].credentialPrompted = true
	m.byName["dr-vc"].inv = nil

	for _, s := range unloadedSizes {
		resize(m, s.w, s.h)
		out := assertFrame(t, m, s.w, s.h)
		for _, want := range []string{"✕ old-vc: connection failed", "connecting: edge-vc", "credentials required: dr-vc", "not connected: lab-vc"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%dx%d: missing %q:\n%s", s.w, s.h, want, out)
			}
		}
		// A failure is reported once, by its own line: not also as missing.
		if strings.Count(out, "old-vc") != 1 {
			t.Fatalf("%dx%d: failed vCenter listed twice:\n%s", s.w, s.h, out)
		}
	}
}

func TestUnloadedLineStaysCompactWhenManyAreMissing(t *testing.T) {
	b := &fakeBackend{inventories: map[string]*vsphere.Inventory{}}
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("site-%02d-vcenter", i)
		b.contexts = append(b.contexts, ctx(name, "https://"+name+".internal"))
		b.inventories[name] = inventoryFor(name)
	}
	m := newTestModel(t, b, Options{Current: "site-00-vcenter"})
	press(t, m, "a")
	for _, s := range unloadedSizes {
		resize(m, s.w, s.h)
		out := assertFrame(t, m, s.w, s.h)
		if !strings.Contains(out, "more") || !strings.Contains(out, "R connects all") {
			t.Fatalf("%dx%d: many missing vCenters must collapse to a count and keep the key:\n%s", s.w, s.h, out)
		}
		if strings.Count(out, "not connected") != 1 {
			t.Fatalf("%dx%d: one line for the whole group:\n%s", s.w, s.h, out)
		}
	}
}

func TestUnloadedLineIsAllScopeOnlyAndSearchWordingIsShared(t *testing.T) {
	b := threeVCenters()
	m := newTestModel(t, b, Options{Current: "prod"})
	resize(m, 100, 30)
	if out := ansi.Strip(m.View()); strings.Contains(out, "not connected") {
		t.Fatalf("single-vCenter scope should not list other vCenters:\n%s", out)
	}
	press(t, m, "a", "tab")
	out := ansi.Strip(m.View())
	for _, want := range []string{"✕ edge-vc not searched: not connected", "✕ dr-vc not searched: not connected"} {
		if !strings.Contains(out, want) {
			t.Fatalf("search lost its wording %q:\n%s", want, out)
		}
	}
	if got := m.byName["edge-vc"].unloadedReason(); got != "not connected" {
		t.Fatalf("reason = %q", got)
	}
}

func TestUnloadedEmptyTableDoesNotClaimNoVMs(t *testing.T) {
	b := threeVCenters()
	b.contexts = b.contexts[1:] // prod is not configured; nothing is selected-and-loaded
	m := newTestModel(t, b, Options{Current: "edge-vc", AllContexts: true})
	m.byName["edge-vc"].inv = nil
	m.byName["edge-vc"].invalidateRows()
	resize(m, 80, 24)
	out := ansi.Strip(m.View())
	if strings.Contains(out, "no VMs") {
		t.Fatalf("empty table claims there are no VMs while vCenters are unloaded:\n%s", out)
	}
	if !strings.Contains(out, "not connected: edge-vc") {
		t.Fatalf("unloaded vCenter not named:\n%s", out)
	}
}
