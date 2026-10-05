package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// topoFakeBackend adds the switch-view extension to fakeBackend.
type topoFakeBackend struct {
	*fakeBackend
	topo  map[string]*vsphere.NetworkTopology
	err   error
	calls int
}

func (b *topoFakeBackend) NetworkTopology(_ context.Context, cc *config.Context) (*vsphere.NetworkTopology, error) {
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	return b.topo[cc.Name], nil
}

func i32(v int32) *int32 { return &v }

// switchedInventory is one vCenter with a distributed switch, its uplink
// port group, a standard switch's network and an opaque network.
func switchedInventory(name string) *vsphere.Inventory {
	inv := inventoryFor(name)
	inv.Networks = []vsphere.Network{
		{ID: "dvportgroup-1", Name: "pg-app", Type: "portgroup", Switch: "dvs-a", VLAN: "110", Accessible: true},
		{ID: "dvportgroup-2", Name: "pg-vmotion", Type: "portgroup", Switch: "dvs-a", VLAN: "140", Accessible: true},
		{ID: "dvportgroup-3", Name: "dvs-a-DVUplinks-9", Type: "portgroup", Switch: "dvs-a", VLAN: "trunk 0-4094", Accessible: true},
		{ID: "network-1", Name: "VM Network", Type: "standard", Accessible: true},
		{ID: "network-2", Name: "pg-300", Type: "standard", Accessible: true},
		{ID: "network-3", Name: "seg-web", Type: "opaque", Accessible: true},
	}
	return inv
}

// switchedTopology wires that vCenter: h1 and h2 are on dvs-a, h3 shares
// their cluster but is not; h2's second uplink runs slower; h1's vMotion
// adapter is above the switch's MTU; pg-300 exists on h1 only.
func switchedTopology(name string) *vsphere.NetworkTopology {
	dvs := vsphere.DVSwitch{
		ID: "dvs-1", Name: "dvs-a", UUID: "uuid-a", MaxMTU: 1500, Hosts: []string{"h1", "h2"},
		UplinkPorts: []string{"dvs-a-DVUplinks-9"},
		PortGroups: []vsphere.DVPortGroup{
			{ID: "dvportgroup-1", Key: "dvportgroup-1", Name: "pg-app", VLAN: "110", ActiveUplinks: []string{"up1", "up2"}, TeamingPolicy: "loadbalance_srcid"},
			{ID: "dvportgroup-2", Key: "dvportgroup-2", Name: "pg-vmotion", VLAN: "140", ActiveUplinks: []string{"up1"}, StandbyUplinks: []string{"up2"}, TeamingPolicy: "failover_explicit"},
			{ID: "dvportgroup-3", Key: "dvportgroup-3", Name: "dvs-a-DVUplinks-9", Uplink: true},
		},
	}
	nics := func(second int32) []vsphere.HostNIC {
		return []vsphere.HostNIC{
			{Device: "vmnic0", LinkSpeedMB: i32(1000)},
			{Device: "vmnic2", LinkSpeedMB: i32(25000)},
			{Device: "vmnic3", LinkSpeedMB: i32(second)},
		}
	}
	proxy := []vsphere.HostProxySwitch{{Switch: "dvs-a", SwitchUUID: "uuid-a", MTU: 1500,
		Uplinks: []vsphere.HostProxyUplink{{Name: "up1", NIC: "vmnic2"}, {Name: "up2", NIC: "vmnic3"}}}}
	vss := []vsphere.HostVSwitch{{Name: "vSwitch0", MTU: 1500, Uplinks: []string{"vmnic0"}}}
	return &vsphere.NetworkTopology{
		Context:  name,
		Switches: []vsphere.DVSwitch{dvs},
		Hosts: []vsphere.Host{
			{Name: "h1", Cluster: "c1", ConnectionState: "connected", NICs: nics(25000), ProxySwitches: proxy, VSwitches: vss,
				PortGroups: []vsphere.HostPortGroup{{Name: "VM Network", Switch: "vSwitch0"}, {Name: "pg-300", Switch: "vSwitch0", VLAN: 300}},
				VMKs:       []vsphere.HostVMKernel{{Device: "vmk1", DVSwitchUUID: "uuid-a", DVPortGroupKey: "dvportgroup-2", MTU: 9000}}},
			{Name: "h2", Cluster: "c1", ConnectionState: "connected", NICs: nics(10000), ProxySwitches: proxy, VSwitches: vss,
				PortGroups: []vsphere.HostPortGroup{{Name: "VM Network", Switch: "vSwitch0"}}},
			{Name: "h3", Cluster: "c1", ConnectionState: "connected", NICs: nics(25000), VSwitches: vss,
				PortGroups: []vsphere.HostPortGroup{{Name: "VM Network", Switch: "vSwitch0"}}},
		},
		NetworkVMs: map[string]int{"dvportgroup-1": 7, "dvportgroup-2": 0, "network-1": 3, "network-2": 0, "network-3": 2},
	}
}

func findingTexts(sw netSwitch) string {
	var out []string
	for _, f := range sw.findings {
		out = append(out, f.text)
	}
	return strings.Join(out, "\n")
}

func TestBuildNetSwitchesGroupsWiresAndJudges(t *testing.T) {
	switches := buildNetSwitches("prod", switchedInventory("prod"), switchedTopology("prod"))
	var names []string
	for _, sw := range switches {
		names = append(names, sw.name)
	}
	if got, want := strings.Join(names, ","), "dvs-a,vSwitch0,opaque networks"; got != want {
		t.Fatalf("switches = %s, want %s", got, want)
	}
	dvs := switches[0]
	if len(dvs.portGroups) != 2 || dvs.portGroups[0].name != "pg-app" || dvs.portGroups[1].name != "pg-vmotion" {
		t.Fatalf("dvs-a port groups = %+v; the uplink port group must be hidden", dvs.portGroups)
	}
	if dvs.portGroups[0].vms != 7 {
		t.Errorf("pg-app VMs = %d, want 7", dvs.portGroups[0].vms)
	}
	if strings.Join(dvs.uplinks, ",") != "up1,up2" {
		t.Errorf("uplinks = %v", dvs.uplinks)
	}
	if got := dvs.hosts[1].nics["up2"]; got == nil || got.device != "vmnic3" || got.speedMB != 10000 {
		t.Errorf("h2 up2 = %+v, want vmnic3 at 10G", got)
	}
	if len(dvs.portGroups[1].vmks) != 1 {
		t.Errorf("pg-vmotion VMkernel adapters = %+v, want h1's vmk1", dvs.portGroups[1].vmks)
	}
	findings := findingTexts(dvs)
	for _, want := range []string{
		"h3 is in c1 but not on this switch",
		"up2 speeds differ between hosts: 25G on h1, 10G on h2",
		"h1 vmk1 on pg-vmotion has MTU 9000, above the switch's 1500",
	} {
		if !strings.Contains(findings, want) {
			t.Errorf("dvs-a findings are missing %q:\n%s", want, findings)
		}
	}
	if dvs.status() != statusBad {
		t.Errorf("an adapter above the switch MTU should make the switch bad, got %v", dvs.status())
	}

	vss := switches[1]
	if len(vss.hosts) != 3 || len(vss.portGroups) != 2 {
		t.Fatalf("vSwitch0 = %d hosts, %d port groups; want 3 and 2", len(vss.hosts), len(vss.portGroups))
	}
	pg300 := vss.portGroups[1]
	if pg300.name != "pg-300" || pg300.vlan != "300" || pg300.hosts != 1 || pg300.vms != 0 {
		t.Errorf("pg-300 = %+v", pg300)
	}
	if want := "pg-300 is on 1 of the 3 hosts with vSwitch0"; !strings.Contains(findingTexts(vss), want) {
		t.Errorf("vSwitch0 findings are missing %q:\n%s", want, findingTexts(vss))
	}
	if vss.portGroups[0].vms != 3 || vss.portGroups[0].vlan != "none" {
		t.Errorf("VM Network = %+v, want 3 VMs and no VLAN", vss.portGroups[0])
	}
}

func TestBuildNetSwitchesWithoutTopologyGroupsBySwitchName(t *testing.T) {
	switches := buildNetSwitches("prod", switchedInventory("prod"), nil)
	if len(switches) != 3 {
		t.Fatalf("switches = %d, want dvs-a, the standard networks and the opaque ones", len(switches))
	}
	dvs := switches[0]
	if dvs.name != "dvs-a" || dvs.wired || len(dvs.portGroups) != 2 {
		t.Fatalf("dvs-a = %+v; it must group its port groups, hide the uplink one by name, and not claim wiring", dvs)
	}
	if dvs.portGroups[0].vms != -1 {
		t.Errorf("VM counts come from the topology read; without it they are unknown, got %d", dvs.portGroups[0].vms)
	}
	if !switches[1].group || len(switches[1].portGroups) != 2 {
		t.Errorf("standard networks = %+v", switches[1])
	}
	if len(dvs.findings) != 0 {
		t.Errorf("no wiring, no findings: %v", findingTexts(dvs))
	}
}

func TestTeamingFindingsNameTheHostLeftWithoutAPath(t *testing.T) {
	topo := switchedTopology("prod")
	topo.Hosts[0].NICs[1].LinkSpeedMB = nil // h1 vmnic2, up1: no link
	switches := buildNetSwitches("prod", switchedInventory("prod"), topo)
	findings := findingTexts(switches[0])
	for _, want := range []string{
		"h1 vmnic2 (up1) has no link",
		"pg-vmotion runs on its standby uplink only on h1",
	} {
		if !strings.Contains(findings, want) {
			t.Errorf("findings are missing %q:\n%s", want, findings)
		}
	}
	if strings.Contains(findings, "pg-app has no working uplink") {
		t.Errorf("pg-app still has up2 on h1:\n%s", findings)
	}
}

func openNetworks(t *testing.T) (*Model, *topoFakeBackend) {
	t.Helper()
	b := &topoFakeBackend{fakeBackend: twoHealthy()}
	b.inventories["prod"] = switchedInventory("prod")
	b.topo = map[string]*vsphere.NetworkTopology{"prod": switchedTopology("prod")}
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	m.height = 40
	press(t, m, "6")
	return m, b
}

func rowNames(rows []row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.name
	}
	return out
}

func TestNetworksTabGroupsFoldsAndLists(t *testing.T) {
	m, b := openNetworks(t)
	if b.calls != 1 {
		t.Fatalf("topology reads = %d, want 1 once the Networks tab is open", b.calls)
	}
	got := strings.Join(rowNames(m.rows()), ",")
	want := "dvs-a,pg-app,pg-vmotion,vSwitch0,VM Network,pg-300,opaque networks,seg-web"
	if got != want {
		t.Fatalf("rows = %s\nwant   %s", got, want)
	}
	out := ansi.Strip(m.View())
	for _, s := range []string{"▾ dvs-a  distributed switch", "├ pg-app", "└ pg-vmotion", "NOTES", "on 1 of 3 hosts"} {
		if !strings.Contains(out, s) {
			t.Errorf("table is missing %q:\n%s", s, out)
		}
	}

	press(t, m, "j", " ")
	if got := strings.Join(rowNames(m.rows()), ","); !strings.HasPrefix(got, "dvs-a,vSwitch0") {
		t.Errorf("space on a port group folds its switch, rows = %s", got)
	}
	if r, _ := m.currentRow(); r.name != "dvs-a" {
		t.Errorf("folding leaves the cursor on the switch, got %q", r.name)
	}
	if !strings.Contains(ansi.Strip(m.View()), "▹ dvs-a") {
		t.Errorf("a folded switch is marked ▹:\n%s", ansi.Strip(m.View()))
	}

	press(t, m, "t")
	if n := len(m.rows()); n != len(b.inventories["prod"].Networks) {
		t.Errorf("t lists every network flat, got %d rows", n)
	}
	press(t, m, "t")
	if r, _ := m.currentRow(); r.name != "dvs-a" {
		t.Errorf("t back to the tree keeps the cursor on dvs-a, got %q", r.name)
	}
}

func TestNetworksFilterKeepsTheSwitchOfAMatch(t *testing.T) {
	m, _ := openNetworks(t)
	press(t, m, " ") // a folded switch must not hide a match
	press(t, m, "/")
	typeText(t, m, "vmotion")
	press(t, m, "enter")
	if got := strings.Join(rowNames(m.rows()), ","); got != "dvs-a,pg-vmotion" {
		t.Errorf("filtered rows = %s, want the match under its switch", got)
	}
	if hint := m.filterHint(); !strings.Contains(hint, "1 network in prod") {
		t.Errorf("the switch kept for its port group is not a match, hint = %q", hint)
	}
}

func TestSwitchWorkspaceOpensPagesAndReturns(t *testing.T) {
	m, _ := openNetworks(t)
	press(t, m, "enter")
	if m.mode != modeSwitchDetail {
		t.Fatalf("enter on a switch row opens the workspace, mode = %v", m.mode)
	}
	out := ansi.Strip(m.View())
	for _, s := range []string{"dvs-a   distributed switch · prod", "[0 Overview]", "Uplinks by host", "h2", "vmnic3 10G", "not on this switch (c1)", "Findings", "Port groups"} {
		if !strings.Contains(out, s) {
			t.Errorf("overview is missing %q:\n%s", s, out)
		}
	}

	press(t, m, "1", "j")
	out = ansi.Strip(m.View())
	for _, s := range []string{"[1 Wiring]", "PHYSICAL", "up1", "━━ pg-vmotion", "active up1 · standby up2"} {
		if !strings.Contains(out, s) {
			t.Errorf("wiring page is missing %q:\n%s", s, out)
		}
	}

	press(t, m, "enter")
	if m.mode != modeSwitchPGDetail {
		t.Fatalf("enter on the wiring page opens the port group, mode = %v", m.mode)
	}
	if out := ansi.Strip(m.View()); !strings.Contains(out, "Standby uplinks") || !strings.Contains(out, "h1 vmk1") {
		t.Errorf("port group detail is missing its teaming or adapters:\n%s", out)
	}
	press(t, m, "esc")
	if m.mode != modeSwitchDetail || m.sw.page != 1 {
		t.Fatalf("esc returns to the wiring page, mode = %v", m.mode)
	}
	press(t, m, "esc")
	if r, _ := m.currentRow(); m.mode != modeBrowse || r.name != "dvs-a" {
		t.Errorf("esc returns to the table on the switch, mode = %v row = %q", m.mode, r.name)
	}
}

func TestJumpFromASwitchPortGroupLeavesTheWorkspace(t *testing.T) {
	m, _ := openNetworks(t)
	press(t, m, "enter", "1", "enter", "enter")
	if m.actions == nil {
		t.Fatal("enter on the port group header opens its actions")
	}
	for i, a := range m.actions.items {
		if a.label == "Show VMs on this network" {
			m.actions.cursor = i
		}
	}
	press(t, m, "enter")
	if m.mode != modeBrowse || m.kind != vsphere.KindVM || m.sw != nil || m.swPG != nil {
		t.Errorf("the jump lands on the VM table with the workspace closed: mode %v kind %v sw %v", m.mode, m.kind, m.sw)
	}
}

func TestTopologyIsReadOnlyForNetworkScreens(t *testing.T) {
	b := &topoFakeBackend{fakeBackend: twoHealthy(), topo: map[string]*vsphere.NetworkTopology{}}
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "j", "2")
	if b.calls != 0 {
		t.Fatalf("topology was read %d times away from the Networks tab", b.calls)
	}
	press(t, m, "6", "j", "k")
	if b.calls != 1 {
		t.Fatalf("topology reads = %d, want 1: moving around must not re-read it", b.calls)
	}
	press(t, m, "r")
	if b.calls != 2 {
		t.Errorf("a reload re-reads the inventory and so the topology, reads = %d", b.calls)
	}
}

func TestFailedTopologyKeepsTheInventoryGrouping(t *testing.T) {
	b := &topoFakeBackend{fakeBackend: twoHealthy(), err: errors.New("permission to perform this operation was denied")}
	b.inventories["prod"] = switchedInventory("prod")
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	press(t, m, "6")
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "dvs-a") || !strings.Contains(out, "wiring not read") {
		t.Errorf("the table still groups by switch and says the wiring was not read:\n%s", out)
	}
	press(t, m, "enter")
	out = ansi.Strip(m.View())
	if !strings.Contains(out, "Wiring could not be read: permission to perform this operation was denied") {
		t.Errorf("the workspace reports why:\n%s", out)
	}
	if strings.Contains(out, "nothing to report") {
		t.Errorf("an unread switch must not claim it has nothing to report:\n%s", out)
	}
}

func TestSwitchScreensFitTheTerminal(t *testing.T) {
	m, _ := openNetworks(t)
	for _, size := range [][2]int{{60, 20}, {80, 24}, {100, 30}} {
		m.width, m.height = size[0], size[1]
		for _, keys := range [][]string{{}, {"enter"}, {"1"}, {"j"}, {"esc"}} {
			press(t, m, keys...)
			lines := strings.Split(m.View(), "\n")
			if len(lines) > size[1] {
				t.Fatalf("%v after %v: %d lines", size, keys, len(lines))
			}
			for _, l := range lines {
				if w := ansi.StringWidth(l); w > size[0] {
					t.Fatalf("%v after %v: line %d cells wide: %q", size, keys, w, ansi.Strip(l))
				}
			}
		}
	}
}
