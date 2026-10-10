package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func hostFindings(hn *hostNet) string {
	var out []string
	for _, f := range hn.findings {
		out = append(out, f.text)
	}
	return strings.Join(out, "\n")
}

func hostNetOf(t *testing.T, topo *vsphere.NetworkTopology, name string) *hostNet {
	t.Helper()
	inv := switchedInventory("prod")
	h := topologyHost(topo, name)
	if h == nil {
		t.Fatalf("no host %s in the topology", name)
	}
	return buildHostNet(h, inv, buildNetSwitches("prod", inv, topo))
}

func TestHostNetworkNamesWhatTheHostIsMissing(t *testing.T) {
	topo := switchedTopology("prod")

	h3 := hostNetOf(t, topo, "h3")
	var names []string
	for _, s := range h3.switches {
		names = append(names, s.name)
	}
	if got := strings.Join(names, ","); got != "dvs-a,vSwitch0" {
		t.Fatalf("h3 switches = %s, want dvs-a (missing) then vSwitch0", got)
	}
	if h3.switches[0].missing != "2 of 3 hosts in c1 are" {
		t.Errorf("dvs-a on h3 = %q", h3.switches[0].missing)
	}
	got := hostFindings(h3)
	for _, want := range []string{"not on dvs-a (2 of 3 hosts in c1 are)", "vmnic2, vmnic3 have a link but are on no switch"} {
		if !strings.Contains(got, want) {
			t.Errorf("h3 findings are missing %q:\n%s", want, got)
		}
	}

	h1 := hostNetOf(t, topo, "h1")
	if got := hostFindings(h1); !strings.Contains(got, "vmk1 MTU 9000 is above dvs-a's 1500") {
		t.Errorf("h1 findings do not flag its jumbo vmk1:\n%s", got)
	}
	if h1.status() != statusBad {
		t.Errorf("h1 status = %v, want bad", h1.status())
	}
	if len(h1.vmks) != 1 || h1.vmks[0].portGroup != "pg-vmotion" || h1.vmks[0].sw != "dvs-a" {
		t.Errorf("h1 VMkernel adapters = %+v, want vmk1 on dvs-a pg-vmotion", h1.vmks)
	}
	if len(h1.spare) != 0 {
		t.Errorf("h1 has every NIC on a switch, spare = %+v", h1.spare)
	}

	// A NIC that loses its link is the switch's problem and this host's.
	topo.Hosts[0].NICs[2].LinkSpeedMB = nil
	if got := hostFindings(hostNetOf(t, topo, "h1")); !strings.Contains(got, "vmnic3 (dvs-a up2) has no link") {
		t.Errorf("h1 findings do not report vmnic3 down:\n%s", got)
	}
}

func TestHostNetworkSpeedFindingNeedsAUsualSpeed(t *testing.T) {
	topo := switchedTopology("prod")
	// Two hosts, two speeds: neither is the odd one out.
	if got := hostFindings(hostNetOf(t, topo, "h2")); strings.Contains(got, "runs at") {
		t.Errorf("h2 is flagged with no usual speed to differ from:\n%s", got)
	}
	topo.Hosts = append(topo.Hosts, vsphere.Host{Name: "h4", Cluster: "c1", ConnectionState: "connected",
		NICs: topo.Hosts[0].NICs, ProxySwitches: topo.Hosts[0].ProxySwitches, VSwitches: topo.Hosts[0].VSwitches})
	topo.Switches[0].Hosts = append(topo.Switches[0].Hosts, "h4")
	if got := hostFindings(hostNetOf(t, topo, "h2")); !strings.Contains(got, "dvs-a up2 runs at 10G; the switch's other hosts' run at 25G") {
		t.Errorf("h2 is not flagged against the usual 25G:\n%s", got)
	}
}

// openHosts is the Hosts tab of a vCenter whose inventory hosts are the
// wired topology's.
func openHosts(t *testing.T) (*Model, *topoFakeBackend) {
	t.Helper()
	b := &topoFakeBackend{fakeBackend: twoHealthy()}
	inv := switchedInventory("prod")
	topo := switchedTopology("prod")
	inv.Hosts = nil
	for _, h := range topo.Hosts {
		h.Context, h.ID = "prod", "host-"+h.Name
		inv.Hosts = append(inv.Hosts, h)
	}
	b.inventories["prod"] = inv
	b.topo = map[string]*vsphere.NetworkTopology{"prod": topo}
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	m.width, m.height = 100, 40
	press(t, m, "3")
	return m, b
}

func TestHostPagesOpenTheNetworkPageAndItsSwitch(t *testing.T) {
	m, b := openHosts(t)
	press(t, m, "enter")
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "[0 Summary]") || !strings.Contains(out, "1 Network") {
		t.Fatalf("the host pane does not offer its pages:\n%s", out)
	}
	if b.calls != 0 {
		t.Fatalf("the Summary page read the topology %d times", b.calls)
	}
	press(t, m, "1")
	if b.calls != 1 {
		t.Fatalf("the Network page read the topology %d times, want 1", b.calls)
	}
	out = ansi.Strip(m.View())
	for _, want := range []string{"h1", "[1 Network]", "PHYSICAL", "dvs-a", "vSwitch0", "vmnic2 25G", "vmk1", "Findings", "MTU 9000 is above"} {
		if !strings.Contains(out, want) {
			t.Errorf("the Network page is missing %q:\n%s", want, out)
		}
	}
	// Moving to the next host keeps the page.
	press(t, m, "l")
	if out := ansi.Strip(m.View()); !strings.Contains(out, "h2") || !strings.Contains(out, "[1 Network]") {
		t.Errorf("moving to h2 left the Network page:\n%s", out)
	}
	press(t, m, "enter")
	if m.mode != modeSwitchDetail || m.sw == nil || !strings.HasSuffix(m.sw.key, "dvs-1") {
		t.Fatalf("enter on dvs-a opens its workspace, mode %v sw %+v", m.mode, m.sw)
	}
	press(t, m, "esc")
	if m.mode != modeDetail || m.hv.page != 1 {
		t.Errorf("esc from the switch returns to the host's Network page, mode %v", m.mode)
	}
	press(t, m, "0")
	if out := ansi.Strip(m.View()); !strings.Contains(out, "Connection") {
		t.Errorf("0 returns to the Summary fields:\n%s", out)
	}
}

func TestHostPagesNeedTheTopologyExtension(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	m.width, m.height = 100, 40
	press(t, m, "3", "enter")
	if out := ansi.Strip(m.View()); strings.Contains(out, "1 Network") {
		t.Errorf("a backend without topology offers a Network page:\n%s", out)
	}
	press(t, m, "1")
	if m.hv != nil && m.hv.page != 0 {
		t.Errorf("1 opened a Network page with nothing to draw")
	}
}

func TestHostNetworkPageFitsTheTerminal(t *testing.T) {
	m, _ := openHosts(t)
	press(t, m, "enter", "1")
	for _, size := range [][2]int{{60, 20}, {80, 24}, {100, 30}} {
		m.width, m.height = size[0], size[1]
		for _, keys := range [][]string{{}, {"j"}, {"j"}, {"l"}, {"l"}} {
			press(t, m, keys...)
			assertFits(t, m, size, keys)
		}
	}
}

func assertFits(t *testing.T, m *Model, size [2]int, keys []string) {
	t.Helper()
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

// VMkernel adapters as a hyperconverged host with NSX has them: port group
// names too long for any column, an IPv6-only management adapter, one with
// no address at all, and NSX adapters bound to the switch with no port
// group. Each cell keeps its column.
func TestHostNetworkVMKernelTableKeepsItsColumns(t *testing.T) {
	topo := switchedTopology("prod")
	h := topologyHost(topo, "h1")
	h.VMKs = []vsphere.HostVMKernel{
		{Device: "vmk0", PortGroup: "HCI Management-1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", MTU: 1500, Netstack: "defaultTcpipStack",
			IPv6: []string{"fe80::250:56ff:fe6a:1/64"}},
		{Device: "vmk9", PortGroup: "pg-vmotion", MTU: 1500},
		{Device: "vmk10", DVSwitchUUID: "uuid-a", IP: "192.168.77.13", MTU: 9000, Netstack: "vxlan"},
		{Device: "vmk2", DVSwitchUUID: "uuid-a", DVPortGroupKey: "dvportgroup-2", IP: "192.168.101.134", MTU: 1500, Netstack: "vmotion"},
	}
	inv := switchedInventory("prod")
	hn := buildHostNet(h, inv, buildNetSwitches("prod", inv, topo))
	if hn.vmks[3].device != "vmk10" || hn.vmks[3].sw != "dvs-a" {
		t.Fatalf("vmk10 is not placed on the switch it is bound to: %+v", hn.vmks[3])
	}
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	for _, w := range []int{60, 100} {
		lines := m.hostVMKLines(hn, w)
		head := ansi.Strip(lines[0])
		// A narrow terminal drops the stack; the port group's edge is then
		// checked instead.
		addr, stack := strings.Index(head, "ADDRESS"), strings.Index(head, "STACK")
		if stack < 0 {
			stack = strings.Index(head, "PORT GROUP")
		}
		for _, l := range lines[1:] {
			l = ansi.Strip(l)
			if ansi.StringWidth(l) > w {
				t.Errorf("%d columns: %q is too wide", w, l)
			}
			if r := []rune(l); r[addr-1] != ' ' || r[stack-1] != ' ' {
				t.Errorf("%d columns: %q runs one cell into the next", w, l)
			}
		}
		got := ansi.Strip(strings.Join(lines, "\n"))
		wants := []string{"vmk0    fe80::250:56ff:fe6a:1", "vmk9    —", "vmk10   192.168.77.13", "· dvs-a"}
		if w >= 100 {
			wants = append(wants, "default")
		}
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%d columns: the table is missing %q:\n%s", w, want, got)
			}
		}
	}
}

// An account that cannot read a distributed switch still sees the host's own
// configuration of it. The page must say the switch is unreadable, not that
// its NICs are on no switch.
func TestHostNetworkSaysWhatTheAccountCannotRead(t *testing.T) {
	topo := switchedTopology("prod")
	topo.Switches = nil
	inv := switchedInventory("prod")
	inv.Networks = nil
	h := topologyHost(topo, "h1")
	hn := buildHostNet(h, inv, buildNetSwitches("prod", inv, topo))
	got := hostFindings(hn)
	if strings.Contains(got, "on no switch") {
		t.Errorf("NICs on an unreadable switch are reported as unclaimed:\n%s", got)
	}
	if !strings.Contains(got, "dvs-a is not readable by this account") {
		t.Errorf("the unreadable switch is not reported:\n%s", got)
	}
	var dvs *hostSwitch
	for i := range hn.switches {
		if hn.switches[i].name == "dvs-a" {
			dvs = &hn.switches[i]
		}
	}
	if dvs == nil || !dvs.unreadable || len(dvs.links) != 2 || dvs.links[0].nic == nil || dvs.links[0].nic.device != "vmnic2" {
		t.Errorf("dvs-a is not drawn from the host's own configuration: %+v", dvs)
	}
}
