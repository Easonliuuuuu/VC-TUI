package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// vlanSites is two wired vCenters. prod has VMs on pg-app (VLAN 110) and
// pg-vmotion (VLAN 140); customer-a calls VLAN 110 "app-110" and has no
// VLAN 140 at all, so a prod VM on 140 has nowhere to fail over to.
func vlanSites(t *testing.T) (*Model, *topoFakeBackend) {
	t.Helper()
	b := &topoFakeBackend{fakeBackend: twoHealthy(), topo: map[string]*vsphere.NetworkTopology{}}
	for _, name := range []string{"prod", "customer-a"} {
		inv := switchedInventory(name)
		topo := switchedTopology(name)
		inv.VMs = []vsphere.VM{
			{Name: "web-01", Host: "h1", Cluster: "c1", NICs: []vsphere.VMNIC{{NetworkID: "dvportgroup-1"}}},
			{Name: "web-02", Host: "h2", Cluster: "c1", NICs: []vsphere.VMNIC{{NetworkID: "dvportgroup-1"}, {NetworkID: "network-2"}}},
		}
		if name == "prod" {
			inv.VMs = append(inv.VMs, vsphere.VM{Name: "repl-01", Host: "h1", Cluster: "c1", NICs: []vsphere.VMNIC{{NetworkID: "dvportgroup-2"}}})
		} else {
			inv.Networks[0].Name = "app-110"
			topo.Switches[0].PortGroups[0].Name = "app-110"
			inv.Networks = append(inv.Networks[:1], inv.Networks[2:]...)
			topo.Switches[0].PortGroups = append(topo.Switches[0].PortGroups[:1], topo.Switches[0].PortGroups[2:]...)
			topo.Hosts[0].VMKs = nil
		}
		b.inventories[name] = inv
		b.topo[name] = topo
	}
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod", AllContexts: true})
	m.backend = b
	m.width, m.height = 100, 40
	press(t, m, "R", "6")
	return m, b
}

func vlanRowOf(e *vlanEstate, key string) *vlanRow {
	for _, r := range e.all() {
		if r.vlan.String() == key {
			return &r
		}
	}
	return nil
}

func TestVLANMapPlacesEachVLANAcrossVCenters(t *testing.T) {
	m, _ := vlanSites(t)
	press(t, m, "v")
	if m.mode != modeVLANMap {
		t.Fatalf("v opens the VLAN map, mode = %v", m.mode)
	}
	e := m.vlanEstate()
	if len(e.cols) != 2 {
		t.Fatalf("columns = %+v, want both vCenters", e.cols)
	}

	app := vlanRowOf(e, "110")
	if app == nil {
		t.Fatal("no row for VLAN 110")
	}
	if c := app.cells["prod"]; c.vms != 2 || len(c.clusters) != 1 {
		t.Errorf("VLAN 110 on prod = %+v, want 2 VMs reached from c1", c)
	}
	if app.status != statusWarn || !strings.Contains(app.note, "names differ: app-110, pg-app") {
		t.Errorf("VLAN 110 is named differently at each site, note = %q", app.note)
	}

	vmotion := vlanRowOf(e, "140")
	if vmotion == nil || vmotion.note != "only on prod" {
		t.Fatalf("VLAN 140 row = %+v, want only on prod", vmotion)
	}
	if c := vmotion.cells["prod"]; c.vms != 1 || c.vmks != 1 {
		t.Errorf("VLAN 140 on prod = %+v, want repl-01 and h1's vmk1", c)
	}
	// pg-300 is a standard port group: its VLAN comes from the host, since
	// the inventory has none for standard networks.
	if std := vlanRowOf(e, "300"); std == nil || std.cells["prod"].vms != 1 {
		t.Errorf("the standard port group's VLAN 300 is not placed: %+v", std)
	}
	if vlanRowOf(e, "trunk 0-4094") != nil {
		t.Error("the uplink port group is listed as a trunk")
	}

	out := ansi.Strip(m.View())
	for _, want := range []string{"VLAN map", "prod", "customer-a", "pg-app", "only on prod"} {
		if !strings.Contains(out, want) {
			t.Errorf("the map is missing %q:\n%s", want, out)
		}
	}
	press(t, m, "esc")
	if m.mode != modeBrowse {
		t.Errorf("esc returns to the Networks tab, mode = %v", m.mode)
	}
}

func TestVLANMapPairNamesTheStrandedVMs(t *testing.T) {
	m, _ := vlanSites(t)
	press(t, m, "v", "p")
	if m.vmap.pick == nil {
		t.Fatal("p opens the cluster picker")
	}
	// Options are customer-a/c1 then prod/c1: pick prod as the source; the
	// picker then offers the same-named cluster at the other site.
	press(t, m, "j", "enter")
	if p := m.vmap.pick; p == nil || p.source != (clusterRef{"prod", "c1"}) || p.options[p.cursor] != (clusterRef{"customer-a", "c1"}) {
		t.Fatalf("picker after the source = %+v", m.vmap.pick)
	}
	press(t, m, "enter")
	if m.vpair == nil || m.vmap.pick != nil {
		t.Fatalf("the second enter sets the pair, pair %+v", m.vpair)
	}
	res := m.vlanPairResult(*m.vpair)
	// repl-01 on VLAN 140, and the two networks that, as in network
	// compare, do not reach every host of the clusters: h3 is not on dvs-a
	// and pg-300 is on h1 only.
	if res.blockers != 3 {
		t.Errorf("blockers = %d, want 3", res.blockers)
	}
	var missing, renamed *vlanPairRow
	for i := range res.rows {
		switch res.rows[i].name {
		case "pg-vmotion":
			missing = &res.rows[i]
		case "pg-app":
			renamed = &res.rows[i]
		}
	}
	if missing == nil || !missing.missing || missing.note != "1 VM, no network on customer-a" {
		t.Errorf("pg-vmotion = %+v", missing)
	}
	if renamed == nil || renamed.missing || !strings.Contains(renamed.note, "named app-110 there") {
		t.Errorf("pg-app = %+v, want matched by VLAN to app-110", renamed)
	}
	out := ansi.Strip(m.View())
	for _, want := range []string{"prod/c1 → customer-a/c1", "3 blockers", "no network on customer-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("the pair view is missing %q:\n%s", want, out)
		}
	}
	press(t, m, "x")
	if m.vpair != nil {
		t.Error("x clears the pair")
	}
}

func TestVLANMapFitsTheTerminal(t *testing.T) {
	m, _ := vlanSites(t)
	press(t, m, "v")
	for _, size := range [][2]int{{60, 20}, {80, 24}, {100, 30}} {
		m.width, m.height = size[0], size[1]
		for _, keys := range [][]string{{}, {"enter"}, {"j"}, {"G"}, {"enter"}, {"p"}, {"j", "enter"}, {"enter"}, {"j"}, {"enter"}, {"x"}} {
			press(t, m, keys...)
			assertFits(t, m, size, keys)
		}
	}
}

// Missing access never improves a verdict: a pair whose target cannot read
// its distributed switch is incomplete, not ready.
func TestVLANMapSaysWhatTheAccountCannotRead(t *testing.T) {
	m, b := vlanSites(t)
	b.topo["customer-a"].Switches = nil
	press(t, m, "v", "r")
	e := m.vlanEstate()
	var blind []string
	for _, c := range e.cols {
		blind = append(blind, c.blind...)
	}
	if !strings.Contains(strings.Join(blind, "\n"), "dvs-a is on its hosts but not readable") {
		t.Fatalf("the unreadable switch is not reported: %q", blind)
	}
	m.vpair = &vlanPair{source: clusterRef{"prod", "c1"}, target: clusterRef{"customer-a", "c1"}}
	res := m.vlanPairResult(*m.vpair)
	if len(res.blind) == 0 {
		t.Fatal("the pair does not say the target is partly unreadable")
	}
	if out := ansi.Strip(m.View()); !strings.Contains(out, "? incomplete") || strings.Contains(out, "ready") {
		t.Errorf("a blind pair claims a verdict:\n%s", out)
	}
}
