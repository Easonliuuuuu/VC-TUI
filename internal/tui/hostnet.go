package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// A host's detail pane has two pages when the backend can read network
// topology: Summary, the field list it always had, and Network, the host's
// wiring seen from the host. Network reads left to right the way traffic
// leaves the host — physical NIC, uplink, switch, what the switch carries
// here — one line per NIC, so it is as tall as the host has NICs. It is built
// from the same switch model as the switch workspace (network.go), so a
// finding there and here never disagree.
var hostPages = []string{"Summary", "Network"}

// hostView is the open host pane's page and switch cursor. Like clusterView,
// moving to another host keeps the page and starts the cursor afresh.
type hostView struct {
	key    string
	page   int
	cursor int
	scroll int
}

// hostNet is one host's network as the Network page draws it.
type hostNet struct {
	host     *vsphere.Host
	switches []hostSwitch
	// spare are the host's physical NICs no switch claims.
	spare    []netNIC
	vmks     []hostVMK
	findings []netFinding
}

type hostSwitch struct {
	key      string
	name     string
	kind     string
	standard bool
	mtu      int32
	links    []hostLink
	pgs      int
	vms      int
	vmks     []string
	// missing is set for a distributed switch the host's cluster uses and
	// the host is not on; it says how many of the cluster's hosts are.
	missing string
	// unreadable marks a distributed switch the host's own configuration
	// names but the account cannot read: its uplinks are known, its port
	// groups are not.
	unreadable bool
}

type hostLink struct {
	uplink string
	nic    *netNIC
}

type hostVMK struct {
	device    string
	portGroup string
	sw        string
	ip        string
	dhcp      bool
	mtu       int32
	netstack  string
}

// topologyHost finds a host's network configuration in a topology read.
func topologyHost(topo *vsphere.NetworkTopology, name string) *vsphere.Host {
	if topo == nil {
		return nil
	}
	for i := range topo.Hosts {
		if topo.Hosts[i].Name == name {
			return &topo.Hosts[i]
		}
	}
	return nil
}

// buildHostNet builds a host's Network page from the topology read and the
// switch model built from it. inv supplies the VMs, to count the ones on this
// host rather than across the vCenter.
func buildHostNet(h *vsphere.Host, inv *vsphere.Inventory, switches []netSwitch) *hostNet {
	hn := &hostNet{host: h}
	var vms []vsphere.VM
	if inv != nil {
		for _, vm := range inv.VMs {
			if vm.Host == h.Name {
				vms = append(vms, vm)
			}
		}
	}
	claimed := map[string]bool{}
	swMTU := map[string]int32{}
	for i := range switches {
		sw := &switches[i]
		if sw.group {
			continue
		}
		var sh *netSwitchHost
		for j := range sw.hosts {
			if sw.hosts[j].name == h.Name {
				sh = &sw.hosts[j]
				break
			}
		}
		if sh == nil {
			if sw.kind == netDistributed {
				if text, ok := notOnSwitch(sw, h); ok {
					hn.switches = append(hn.switches, hostSwitch{key: sw.key, name: sw.name, kind: sw.kindLabel(), missing: text})
					hn.findings = append(hn.findings, netFinding{statusWarn, fmt.Sprintf("not on %s (%s)", sw.name, text)})
				}
			}
			continue
		}
		hs := hostSwitch{key: sw.key, name: sw.name, kind: sw.kindLabel(), standard: sw.kind == netStandard, mtu: sh.mtu}
		for _, u := range sw.uplinks {
			nic, ok := sh.nics[u]
			if !ok {
				continue
			}
			hs.links = append(hs.links, hostLink{uplink: u, nic: nic})
			if nic != nil {
				claimed[nic.device] = true
			}
		}
		nets := map[string]bool{}
		for _, pg := range sw.portGroups {
			if hs.standard && !hostHasPortGroup(h, sw.name, pg.name) {
				continue
			}
			hs.pgs++
			if pg.network != nil {
				nets[pg.network.ID] = true
			}
			for _, v := range pg.vmks {
				if v.host == h.Name {
					hs.vmks = append(hs.vmks, v.device)
				}
			}
		}
		for _, vm := range vms {
			for _, nic := range vm.NICs {
				if nets[nic.NetworkID] {
					hs.vms++
					break
				}
			}
		}
		sort.Strings(hs.vmks)
		swMTU[sw.name] = sh.mtu
		hn.switches = append(hn.switches, hs)
		judgeHostLinks(hn, sw, &hs)
	}
	// A distributed switch the account cannot read still appears in the
	// host's own configuration. Without this its NICs would look unclaimed,
	// and a missing permission would read as a cabling fault.
	for _, ps := range h.ProxySwitches {
		known := false
		for _, s := range hn.switches {
			known = known || (s.name == ps.Switch && s.missing == "")
		}
		if known {
			continue
		}
		hs := hostSwitch{name: ps.Switch, kind: "distributed switch", mtu: ps.MTU, pgs: -1, unreadable: true}
		for _, u := range ps.Uplinks {
			nic := hostNIC(h, u.NIC)
			hs.links = append(hs.links, hostLink{uplink: u.Name, nic: nic})
			if nic != nil {
				claimed[nic.device] = true
			}
		}
		hn.switches = append(hn.switches, hs)
		hn.findings = append(hn.findings, netFinding{statusWarn, fmt.Sprintf("%s is not readable by this account: its port groups and teaming are not checked", ps.Switch)})
	}
	for _, n := range h.NICs {
		if claimed[n.Device] {
			continue
		}
		nic := netNIC{device: n.Device}
		if n.LinkSpeedMB != nil {
			nic.speedMB, nic.up = *n.LinkSpeedMB, true
		}
		hn.spare = append(hn.spare, nic)
	}
	hn.vmks = hostVMKs(h, switches)
	judgeHost(hn, swMTU)
	return hn
}

// notOnSwitch says how much of the host's cluster is on a distributed switch
// the host is not on, when the switch model counted the host as missing.
func notOnSwitch(sw *netSwitch, h *vsphere.Host) (string, bool) {
	if h.Cluster == "" {
		return "", false
	}
	missing := false
	for _, r := range sw.notJoined {
		missing = missing || r.name == h.Name
	}
	if !missing {
		return "", false
	}
	on, total := 0, 0
	for _, sh := range sw.hosts {
		if sh.cluster == h.Cluster {
			on++
			total++
		}
	}
	for _, r := range sw.notJoined {
		if r.cluster == h.Cluster {
			total++
		}
	}
	return fmt.Sprintf("%d of %d hosts in %s are", on, total, h.Cluster), true
}

func hostHasPortGroup(h *vsphere.Host, sw, name string) bool {
	for _, pg := range h.PortGroups {
		if pg.Switch == sw && pg.Name == name {
			return true
		}
	}
	return false
}

// hostVMKs lists the host's VMkernel adapters with the port group and switch
// each sits on. A distributed port group is named from the switch that owns
// it; one the account cannot see keeps its key.
func hostVMKs(h *vsphere.Host, switches []netSwitch) []hostVMK {
	out := make([]hostVMK, 0, len(h.VMKs))
	for _, v := range h.VMKs {
		k := hostVMK{device: v.Device, portGroup: v.PortGroup, ip: v.Address(), mtu: v.MTU, netstack: v.Netstack}
		if v.DVPortGroupKey != "" {
			k.portGroup = v.DVPortGroupKey
			for i := range switches {
				d := switches[i].dvs
				if d == nil || (v.DVSwitchUUID != "" && d.UUID != "" && d.UUID != v.DVSwitchUUID) {
					continue
				}
				for _, pg := range d.PortGroups {
					if pg.Key == v.DVPortGroupKey {
						k.portGroup, k.sw = pg.Name, d.Name
					}
				}
			}
		} else if v.DVSwitchUUID != "" {
			// A port bound straight to the switch with no port group, as
			// NSX does for its TEP and hyperbus adapters.
			for i := range switches {
				if d := switches[i].dvs; d != nil && d.UUID == v.DVSwitchUUID {
					k.sw = d.Name
				}
			}
		} else {
			for _, pg := range h.PortGroups {
				if pg.Name == v.PortGroup {
					k.sw = pg.Switch
				}
			}
		}
		k.dhcp = v.DHCP != nil && *v.DHCP
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return vmkLess(out[i].device, out[j].device) })
	return out
}

// vmkLess orders vmk2 before vmk10.
func vmkLess(a, b string) bool {
	na, ea := strconv.Atoi(strings.TrimPrefix(a, "vmk"))
	nb, eb := strconv.Atoi(strings.TrimPrefix(b, "vmk"))
	if ea == nil && eb == nil {
		return na < nb
	}
	return a < b
}

// judgeHostLinks records one switch's uplink findings for this host: a NIC
// with no link, an uplink with no NIC, and a speed that differs from the
// switch's other hosts.
func judgeHostLinks(hn *hostNet, sw *netSwitch, hs *hostSwitch) {
	add := func(st rowStatus, format string, args ...any) {
		hn.findings = append(hn.findings, netFinding{status: st, text: fmt.Sprintf(format, args...)})
	}
	for _, l := range hs.links {
		switch {
		case l.nic == nil:
			add(statusWarn, "%s %s has no physical NIC", sw.name, l.uplink)
			continue
		case !l.nic.up:
			where := sw.name
			if !hs.standard {
				where += " " + l.uplink
			}
			add(statusBad, "%s (%s) has no link", l.nic.device, where)
			continue
		}
		speeds := map[int32]int{}
		for _, other := range sw.hosts {
			if nic := other.nics[l.uplink]; nic != nil && nic.up {
				speeds[nic.speedMB]++
			}
		}
		if common, ok := strictMajority(speeds); ok && len(speeds) > 1 && l.nic.speedMB != common {
			add(statusWarn, "%s %s runs at %s; the switch's other hosts' run at %s", sw.name, l.uplink, speedLabel(l.nic.speedMB), speedLabel(common))
		}
	}
}

// judgeHost adds the findings that are about the host as a whole, then
// orders every finding worst first.
func judgeHost(hn *hostNet, swMTU map[string]int32) {
	var linked []string
	for _, n := range hn.spare {
		if n.up {
			linked = append(linked, n.device)
		}
	}
	switch len(linked) {
	case 0:
	case 1:
		hn.findings = append(hn.findings, netFinding{statusWarn, linked[0] + " has a link but is on no switch"})
	default:
		hn.findings = append(hn.findings, netFinding{statusWarn, strings.Join(linked, ", ") + " have a link but are on no switch"})
	}
	for _, v := range hn.vmks {
		if mtu := swMTU[v.sw]; mtu > 0 && v.mtu > mtu {
			hn.findings = append(hn.findings, netFinding{statusBad, fmt.Sprintf("%s MTU %d is above %s's %d", v.device, v.mtu, v.sw, mtu)})
		}
	}
	sort.SliceStable(hn.findings, func(i, j int) bool {
		return statusRank(hn.findings[i].status) < statusRank(hn.findings[j].status)
	})
}

func (hn *hostNet) status() rowStatus {
	st := statusGood
	for _, f := range hn.findings {
		if statusRank(f.status) < statusRank(st) {
			st = f.status
		}
	}
	return st
}

// ---- the pane -------------------------------------------------------------

// host reports whether a row is an ESXi host.
func (r row) host() bool { return r.kind == vsphere.KindHost }

// hostPagesOffered reports whether the host pane has pages at all: without
// the topology extension there is nothing to put on a Network page.
func (m *Model) hostPagesOffered() bool {
	_, ok := m.backend.(networkTopologyBackend)
	return ok
}

func (m *Model) hostState(r row) *hostView {
	if m.hv == nil {
		m.hv = &hostView{}
	}
	if m.hv.key != r.key {
		m.hv = &hostView{key: r.key, page: m.hv.page}
	}
	return m.hv
}

// hostNetworkOpen reports whether the detail pane is showing a host's
// Network page, which is what keeps that vCenter's topology read current.
func (m *Model) hostNetworkOpen() (row, bool) {
	if m.mode != modeDetail || !m.hostPagesOffered() {
		return row{}, false
	}
	r, ok := m.currentRow()
	if !ok || r.kind != vsphere.KindHost {
		return row{}, false
	}
	return r, m.hostState(r).page == 1
}

// hostNetFor builds the Network page's model for a host row, or says why
// there is none to draw.
func (m *Model) hostNetFor(r row) (*hostNet, *contextState, string) {
	st, ok := m.byName[r.context]
	if !ok {
		return nil, nil, "this host's vCenter is no longer configured"
	}
	switch m.topoStateOf(st) {
	case topoLoading:
		return nil, st, m.spin.View() + "reading wiring…"
	case topoFailed:
		return nil, st, "host wiring not read"
	}
	topo := st.netTopo.topo
	h := topologyHost(topo, r.name)
	if h == nil {
		if _, failed := topo.ErrorFor(vsphere.KindHost); failed {
			return nil, st, "host network configuration not read"
		}
		return nil, st, "this host is not in the wiring read · r reads it again"
	}
	return buildHostNet(h, st.inv, st.netSwitches(topoLoaded)), st, ""
}

// handleHostKey takes the keys the host pages give their own meaning. It
// reports false for anything else, which the detail pane then handles as for
// every other kind.
func (m *Model) handleHostKey(msg tea.KeyMsg, r row) (tea.Cmd, bool) {
	hv := m.hostState(r)
	if key.Matches(msg, m.keys.HostPage) {
		if i, err := strconv.Atoi(msg.String()); err == nil && i >= 0 && i < len(hostPages) {
			hv.page, hv.cursor, hv.scroll = i, 0, 0
		}
		return m.ensureNetTopology(false), true
	}
	if hv.page == 0 {
		return nil, false
	}
	hn, _, _ := m.hostNetFor(r)
	last := 0
	if hn != nil {
		last = max(0, len(hn.switches)-1)
	}
	page := max(1, m.bodyHeight()-4)
	switch {
	case key.Matches(msg, m.keys.Up):
		hv.cursor = clamp(hv.cursor-1, 0, last)
	case key.Matches(msg, m.keys.Down):
		hv.cursor = clamp(hv.cursor+1, 0, last)
	case key.Matches(msg, m.keys.PageUp):
		hv.scroll = max(0, hv.scroll-page)
	case key.Matches(msg, m.keys.PageDown):
		hv.scroll += page
	case key.Matches(msg, m.keys.Home):
		hv.cursor, hv.scroll = 0, 0
	case key.Matches(msg, m.keys.End):
		hv.cursor, hv.scroll = last, 1<<30
	case key.Matches(msg, m.keys.Open):
		if hn == nil || len(hn.switches) == 0 {
			return nil, true
		}
		sw := hn.switches[clamp(hv.cursor, 0, last)]
		if sw.key == "" {
			m.setMessage(sw.name+" is not readable by this account", true)
			return nil, true
		}
		m.sw = &switchWorkspace{key: sw.key, context: r.context, from: modeDetail}
		m.swPG = nil
		m.mode = modeSwitchDetail
		return m.ensureNetTopology(false), true
	case key.Matches(msg, m.keys.Reload):
		return m.ensureNetTopology(true), true
	default:
		return nil, false
	}
	return nil, true
}

// viewHostNetwork draws the Network page under the pane's header line.
func (m *Model) viewHostNetwork(r row, hv *hostView) []string {
	t := m.theme
	w := m.width
	title := "  " + t.title.Render(r.name) + t.dim.Render("   "+kindLabel(r.kind)+" · "+r.context)
	lines := []string{joinEnds(title, m.pageTabs(hostPages, hv.page), w)}
	hn, st, why := m.hostNetFor(r)
	verdict := ""
	if hn != nil {
		verdict = hostVerdict(t, hn)
	}
	source := ""
	if st != nil {
		source = t.dim.Render(m.topoSource(st))
	}
	lines = append(lines, joinEnds("  "+verdict, source, w))
	if st != nil {
		for _, l := range m.topoProblems(st, w-2) {
			lines = append(lines, "  "+l)
		}
	}
	lines = append(lines, "")
	head := len(lines)
	if hn == nil {
		return scrollLines(append(lines, t.dim.Render("  "+why)), 0, m.bodyHeight())
	}
	if hn.host.ConnectionState != "" && hn.host.ConnectionState != "connected" {
		msg := fmt.Sprintf("%s The host is %s; this is the last configuration vCenter has for it.", glyphCheckWarn, hn.host.ConnectionState)
		for _, l := range wrap(msg, w-4) {
			lines = append(lines, t.warn.Render("  "+l))
		}
		head = len(lines)
	}
	hv.cursor = clamp(hv.cursor, 0, max(0, len(hn.switches)-1))
	// Findings come before the VMkernel table: they are why the page is
	// opened, and on a short terminal the table is what scrolls off.
	body, cursorLine := m.hostWiringLines(hn, hv.cursor, w)
	body = append(body, "", t.header.Render("Findings"))
	if len(hn.findings) == 0 {
		body = append(body, t.ok.Render("  "+glyphCheckOK+" nothing to report"))
	}
	for _, f := range hn.findings {
		glyph := glyphCheckWarn
		if f.status == statusBad {
			glyph = glyphFail
		}
		for i, l := range wrap(f.text, w-6) {
			prefix := "    "
			if i == 0 {
				prefix = "  " + glyph + " "
			}
			body = append(body, t.statusStyle(f.status).Render(prefix+l))
		}
	}
	body = append(body, "", t.header.Render("VMkernel adapters"))
	body = append(body, m.hostVMKLines(hn, w)...)
	h := max(1, m.bodyHeight()-head)
	limit := max(0, len(body)-h)
	if cursorLine >= 0 {
		if cursorLine < hv.scroll {
			hv.scroll = cursorLine
		}
		if cursorLine >= hv.scroll+h {
			hv.scroll = cursorLine - h + 1
		}
	}
	hv.scroll = clamp(hv.scroll, 0, limit)
	return scrollLines(append(lines, body[hv.scroll:]...), 0, m.bodyHeight())
}

func hostVerdict(t theme, hn *hostNet) string {
	bad, warn := 0, 0
	for _, f := range hn.findings {
		if f.status == statusBad {
			bad++
		} else {
			warn++
		}
	}
	switch {
	case bad > 0 && warn > 0:
		return t.bad.Render(fmt.Sprintf("%s %d · %s %d", glyphFail, bad, glyphCheckWarn, warn))
	case bad > 0:
		return t.bad.Render(glyphFail + " " + nounCount(bad, "problem"))
	case warn > 0:
		return t.warn.Render(glyphCheckWarn + " " + nounCount(warn, "finding"))
	}
	return t.ok.Render(glyphCheckOK + " no findings")
}

// hostWiringLines draws NIC → uplink → switch, a switch's links joined by a
// bus to its name, and reports the line the switch cursor is on. Narrow
// terminals lose the link speed, then the column of what each switch carries.
func (m *Model) hostWiringLines(hn *hostNet, cursor, w int) ([]string, int) {
	t := m.theme
	if len(hn.switches) == 0 && len(hn.spare) == 0 {
		return []string{t.dim.Render("  no switches or physical NICs on this host")}, -1
	}
	speeds := true
	nicW, upW, swW := 6, 0, 12
	measure := func() {
		nicW, upW, swW = 6, 0, 12
		for _, s := range hn.switches {
			swW = max(swW, ansi.StringWidth(s.name))
			for _, l := range s.links {
				nicW = max(nicW, ansi.StringWidth(hostNICLabel(l.nic, speeds)))
				if !s.standard {
					upW = max(upW, ansi.StringWidth(l.uplink))
				}
			}
		}
		for _, n := range hn.spare {
			nicW = max(nicW, ansi.StringWidth(hostNICLabel(&n, speeds)))
		}
		nicW, upW, swW = min(nicW, 18), min(upW, 12), min(swW, 22)
	}
	measure()
	const mtuW = 5
	left := func() int {
		n := 2 + nicW + 4
		if upW > 0 {
			n += upW + 1
		}
		return n + 3 + swW + 1 + mtuW
	}
	if left()+24 > w {
		speeds = false
		measure()
	}
	carries := left()+20 <= w
	uplinkCell := func(s hostSwitch, l hostLink) string {
		if upW == 0 {
			return ""
		}
		if s.standard {
			return strings.Repeat("─", upW+1)
		}
		return pad(l.uplink, upW, false) + " "
	}

	head := "  " + pad("PHYSICAL", nicW+4, false)
	if upW > 0 {
		head += pad("UPLINK", upW+1, false)
	}
	head += pad("    SWITCH", 4+swW+1+mtuW, false)
	if carries {
		head += "  ON THIS HOST"
	}
	out := []string{t.header.Render(truncate(head, w))}
	cursorLine := -1
	for si, s := range hn.switches {
		selected := si == cursor
		if s.missing != "" {
			text := strings.Repeat(" ", nicW+4)
			if upW > 0 {
				text += strings.Repeat(" ", upW+1)
			}
			text += "    " + pad(s.name, swW, false) + " " + strings.Repeat(" ", mtuW)
			note := "not on it · " + s.missing
			if selected {
				cursorLine = len(out)
				out = append(out, t.focused.Render(truncate(glyphCursor+" "+text+"  "+note, w)))
				continue
			}
			out = append(out, truncate("  "+t.dim.Render(text)+"  "+t.warn.Render(glyphCheckWarn+" "+note), w))
			continue
		}
		n := max(len(s.links), 2)
		for i := 0; i < n; i++ {
			var nicText, link, bus string
			var nicStyle = t.text
			if i < len(s.links) {
				l := s.links[i]
				nicText = hostNICLabel(l.nic, speeds)
				switch {
				case l.nic == nil:
					nicStyle = t.warn
				case !l.nic.up:
					nicStyle, nicText = t.bad, nicText+" "+glyphFail
				}
				link = "── " + uplinkCell(s, l)
				if s.standard {
					// A standard switch's uplink is the NIC itself, so the
					// line runs straight through the uplink column.
					link = "───" + uplinkCell(s, l)
				}
				switch {
				case len(s.links) == 1:
					bus = "─"
				case i == 0:
					bus = "┬"
				case i == len(s.links)-1:
					bus = "┘"
				default:
					bus = "┤"
				}
			} else {
				link = strings.Repeat(" ", 3)
				if upW > 0 {
					link += strings.Repeat(" ", upW+1)
				}
				bus = " "
				if i == 0 {
					bus = "─"
				}
			}
			var mid string
			switch i {
			case 0:
				mtu := ""
				if s.mtu > 0 {
					mtu = strconv.Itoa(int(s.mtu))
				}
				mid = "── " + pad(s.name, swW, false) + " " + pad(mtu, mtuW, true)
			case 1:
				mid = "   " + pad(s.kind, swW+1+mtuW, false)
			default:
				mid = strings.Repeat(" ", 3+swW+1+mtuW)
			}
			right := ""
			if carries {
				switch {
				case i == 0 && s.unreadable:
					right = "not readable by this account"
				case i == 0:
					right = nounCount(s.pgs, "port group") + " · " + nounCount(s.vms, "VM")
				case i == 1:
					right = strings.Join(s.vmks, ", ")
				}
			}
			if selected && i == 0 {
				cursorLine = len(out)
				plain := glyphCursor + " " + pad(nicText, nicW, false) + " " + link + bus + mid
				if right != "" {
					plain += "  " + right
				}
				out = append(out, t.focused.Render(truncate(plain, w)))
				continue
			}
			line := "  " + nicStyle.Render(pad(nicText, nicW, false)) + " " + t.faint.Render(link+bus)
			if i == 0 {
				line += t.faint.Render("── ") + t.text.Render(strings.TrimPrefix(mid, "── "))
			} else {
				line += t.dim.Render(mid)
			}
			if right != "" {
				line += "  " + t.dim.Render(right)
			}
			out = append(out, truncate(line, w))
		}
	}
	for _, n := range hn.spare {
		label := hostNICLabel(&n, speeds)
		if n.up {
			out = append(out, truncate("  "+pad(label, nicW, false)+" "+t.warn.Render(glyphCheckWarn+" on no switch"), w))
		} else {
			out = append(out, truncate("  "+t.dim.Render(pad(n.device, nicW, false)+" no link · on no switch"), w))
		}
	}
	return out, cursorLine
}

func hostNICLabel(n *netNIC, speed bool) string {
	switch {
	case n == nil:
		return "no NIC"
	case !n.up:
		return n.device
	case speed:
		return n.device + " " + speedLabel(n.speedMB)
	}
	return n.device
}

// hostVMKLines is the VMkernel table: device, port group, switch, address,
// MTU and TCP/IP stack, the last columns given up first.
func (m *Model) hostVMKLines(hn *hostNet, w int) []string {
	t := m.theme
	if len(hn.vmks) == 0 {
		return []string{t.dim.Render("  none")}
	}
	// The short, fixed-width cells come first so they line up; the port
	// group, whose names run long, takes whatever width is left.
	devW, ipW, stackW := 6, 7, 5
	for _, v := range hn.vmks {
		devW = max(devW, ansi.StringWidth(v.device))
		ipW = max(ipW, ansi.StringWidth(v.ip))
		stackW = max(stackW, ansi.StringWidth(vmkStack(v.netstack)))
	}
	ipW, stackW = min(ipW, 39), min(stackW, 14)
	// The stack is the least asked-for cell, so a narrow terminal drops it
	// to leave the port group room.
	if 2+devW+2+ipW+2+5+2+stackW+2+12 > w {
		stackW = -2
	}
	head := "  " + pad("DEVICE", devW+2, false) + pad("ADDRESS", ipW+2, false) + pad("MTU", 5, true) + "  " +
		pad("STACK", stackW+2, false) + "PORT GROUP"
	out := []string{t.header.Render(truncate(head, w))}
	for _, v := range hn.vmks {
		ip := v.ip
		switch {
		case ip == "" && v.dhcp:
			ip = t.dim.Render("dhcp")
		case ip == "":
			ip = t.dim.Render("—")
		}
		pg := v.portGroup
		if pg == "" {
			pg = t.dim.Render("—")
		}
		if v.sw != "" {
			pg += t.dim.Render(" · " + v.sw)
		}
		line := "  " + pad(v.device, devW+2, false) + pad(ip, ipW+2, false) +
			pad(strconv.Itoa(int(v.mtu)), 5, true) + "  " + pad(t.dim.Render(vmkStack(v.netstack)), stackW+2, false) + pg
		out = append(out, truncate(line, w))
	}
	return out
}

// vmkStack shortens a TCP/IP stack key: "defaultTcpipStack" reads "default".
func vmkStack(key string) string {
	if s := strings.TrimSuffix(key, "TcpipStack"); s != "" {
		return s
	}
	return key
}
