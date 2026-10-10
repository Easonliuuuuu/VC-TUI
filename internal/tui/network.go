package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/humanize"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// The Networks tab groups port groups under the switch that carries them,
// and a switch opens into a workspace of its own (netview.go). Port groups
// and their switch names come from the ordinary inventory load; everything
// about wiring — which hosts are on a switch, which physical NIC backs each
// uplink, which VMkernel adapters sit on a port group, how many VMs are
// attached — comes from a separate topology read made only while one of
// these screens is open. Host network configuration is too heavy to fetch on
// every load of a large estate, the same reason VM charts are read on demand.

// networkTopologyBackend is the live-query extension behind the switch
// views. It is optional and type-asserted like vmPerfBackend: without it the
// tab still groups port groups by switch, from the inventory alone.
type networkTopologyBackend interface {
	NetworkTopology(ctx context.Context, cc *config.Context) (*vsphere.NetworkTopology, error)
}

// netTopoEntry is one vCenter's topology read. Like vmPerfEntry it keeps the
// last good read on screen while a re-read runs or fails.
type netTopoEntry struct {
	gen     uint64
	loading bool
	topo    *vsphere.NetworkTopology
	asOf    time.Time
	err     error
	// forLoad is the generation of the network inventory load this read
	// follows. A newer load (a refresh, a reload) makes the read stale, so
	// the topology is re-read at the same cadence as the table it decorates.
	forLoad uint64
}

type netTopoMsg struct {
	context string
	gen     uint64
	topo    *vsphere.NetworkTopology
	err     error
	asOf    time.Time
}

// showingNetworks reports whether a screen that reads topology is open.
func (m *Model) showingNetworks() bool {
	switch m.mode {
	case modeBrowse:
		return m.kind == vsphere.KindNetwork
	case modeSwitchDetail, modeSwitchPGDetail:
		return true
	case modeDetail:
		if _, open := m.hostNetworkOpen(); open {
			return true
		}
		return m.detailFrom == modeBrowse && m.kind == vsphere.KindNetwork
	case modeVLANMap:
		return true
	}
	return false
}

// ensureNetTopology starts a topology read for every vCenter on screen whose
// network inventory is loaded and whose read is missing or older than that
// inventory. force re-reads regardless, for r.
func (m *Model) ensureNetTopology(force bool) tea.Cmd {
	if !m.showingNetworks() {
		return nil
	}
	b, ok := m.backend.(networkTopologyBackend)
	if !ok {
		return nil
	}
	states := m.inScope()
	if r, open := m.hostNetworkOpen(); open {
		if st, ok := m.byName[r.context]; ok {
			states = []*contextState{st}
		}
	}
	if m.sw != nil {
		if st, ok := m.byName[m.sw.context]; ok {
			states = []*contextState{st}
		}
	}
	var cmds []tea.Cmd
	for _, st := range states {
		if st.inv == nil || st.cc == nil {
			continue
		}
		ks := st.kind(vsphere.KindNetwork)
		if !ks.loaded {
			continue
		}
		e := st.netTopo
		if e != nil && e.loading {
			continue
		}
		if e != nil && !force && e.forLoad == ks.generation {
			continue
		}
		if e == nil {
			e = &netTopoEntry{}
			st.netTopo = e
		}
		m.netTopoGen++
		e.gen, e.loading, e.forLoad = m.netTopoGen, true, ks.generation
		gen, name, cc, ctx, now := e.gen, st.cc.Name, st.cc, m.ctx, m.now()
		cmds = append(cmds, func() tea.Msg {
			topo, err := b.NetworkTopology(ctx, cc)
			return netTopoMsg{context: name, gen: gen, topo: topo, err: err, asOf: now}
		})
	}
	return tea.Batch(cmds...)
}

func (m *Model) applyNetTopo(msg netTopoMsg) {
	st, ok := m.byName[msg.context]
	if !ok || st.netTopo == nil || st.netTopo.gen != msg.gen {
		return
	}
	e := st.netTopo
	e.loading = false
	e.err = msg.err
	if msg.err == nil && msg.topo != nil {
		e.topo, e.asOf = msg.topo, msg.asOf
	}
	m.preserveCursor(st.invalidateNetwork)
}

// netTopoState is what the table says about a vCenter's topology read.
type netTopoState int

const (
	topoUnsupported netTopoState = iota
	topoLoading
	topoFailed
	topoLoaded
)

func (m *Model) topoStateOf(st *contextState) netTopoState {
	if _, ok := m.backend.(networkTopologyBackend); !ok {
		return topoUnsupported
	}
	e := st.netTopo
	switch {
	case e == nil || (e.topo == nil && e.err == nil):
		return topoLoading
	case e.topo == nil:
		return topoFailed
	default:
		return topoLoaded
	}
}

// treeInfo places a row in the grouped Networks tab. switchKey names the
// switch a row belongs to, or is, when head is set.
type treeInfo struct {
	head      bool
	switchKey string
}

// ---- the switch model ----------------------------------------------------

const (
	netDistributed = "distributed"
	netStandard    = "standard"
	netOpaque      = "opaque"
)

// netSwitch is one switch as the network views show it: a distributed
// switch, every host's standard switch of one name taken together, or a
// catch-all group for networks no switch could be found for.
type netSwitch struct {
	key     string
	context string
	id      string
	name    string
	kind    string
	dvs     *vsphere.DVSwitch
	where   vsphere.Location
	// wired reports that host-level wiring was read for this switch: hosts,
	// uplinks and VMkernel adapters below are evidence, not just empty.
	wired bool
	// group marks a catch-all rather than a real switch.
	group      bool
	mtu        int32
	mtuNote    string
	uplinks    []string
	hosts      []netSwitchHost
	notJoined  []netHostRef
	portGroups []netPortGroup
	findings   []netFinding
}

type netHostRef struct{ name, cluster string }

type netSwitchHost struct {
	name      string
	cluster   string
	connected bool
	mtu       int32
	// nics maps each uplink to the physical NIC behind it on this host. An
	// uplink present with a nil NIC has nothing assigned.
	nics map[string]*netNIC
}

type netNIC struct {
	device  string
	speedMB int32
	up      bool
}

type netVMK struct {
	host   string
	device string
	ip     string
	mtu    int32
}

type netPortGroup struct {
	key        string
	name       string
	network    *vsphere.Network
	dv         *vsphere.DVPortGroup
	vlan       string
	vlanNote   string
	vms        int // -1 when not counted
	vmks       []netVMK
	hosts      int
	active     []string
	standby    []string
	teaming    string
	promisc    *bool
	macChanges *bool
	forged     *bool
	accessible bool
	notes      []string
	status     rowStatus
}

type netFinding struct {
	status rowStatus
	text   string
}

func (s *netSwitch) kindLabel() string {
	switch s.kind {
	case netDistributed:
		return "distributed switch"
	case netStandard:
		if s.group {
			return "standard networks"
		}
		return "standard switch"
	default:
		return "opaque networks"
	}
}

// status is the worst finding's.
func (s *netSwitch) status() rowStatus {
	st := statusGood
	for _, f := range s.findings {
		if statusRank(f.status) < statusRank(st) {
			st = f.status
		}
	}
	return st
}

func (s *netSwitch) vmTotal() (int, bool) {
	total, counted := 0, false
	for _, pg := range s.portGroups {
		if pg.vms >= 0 {
			total += pg.vms
			counted = true
		}
	}
	return total, counted
}

func (s *netSwitch) vmkTotal() int {
	n := 0
	for _, pg := range s.portGroups {
		n += len(pg.vmks)
	}
	return n
}

// buildNetSwitches groups one vCenter's networks under their switches. topo
// may be nil, in which case distributed port groups are still grouped by the
// switch name the inventory records and everything host-level is unknown.
func buildNetSwitches(ctxName string, inv *vsphere.Inventory, topo *vsphere.NetworkTopology) []netSwitch {
	var nets []vsphere.Network
	if inv != nil {
		nets = inv.Networks
	}
	used := make([]bool, len(nets))
	vmCount := func(n *vsphere.Network) int {
		if topo == nil || n == nil {
			return -1
		}
		if c, ok := topo.NetworkVMs[n.ID]; ok {
			return c
		}
		return -1
	}
	hostsByName := map[string]*vsphere.Host{}
	if topo != nil {
		for i := range topo.Hosts {
			hostsByName[topo.Hosts[i].Name] = &topo.Hosts[i]
		}
	}

	var out []netSwitch
	var dvss []*vsphere.DVSwitch
	known := map[string]bool{}
	if topo != nil {
		for i := range topo.Switches {
			dvss = append(dvss, &topo.Switches[i])
			known[topo.Switches[i].Name] = true
		}
	}
	for _, n := range nets {
		if n.Type == "portgroup" && n.Switch != "" && !known[n.Switch] {
			known[n.Switch] = true
			dvss = append(dvss, &vsphere.DVSwitch{Name: n.Switch})
		}
	}
	for _, d := range dvss {
		sw := netSwitch{context: ctxName, name: d.Name, kind: netDistributed, id: d.ID}
		if d.ID == "" {
			sw.id = "dvs:" + d.Name
		} else {
			sw.dvs, sw.where, sw.mtu, sw.wired = d, d.Location, d.MaxMTU, true
		}
		sw.key = ctxName + "/switch:" + sw.id
		hidden := map[string]bool{}
		for _, u := range d.UplinkPorts {
			hidden[u] = true
		}
		dvByName := map[string]*vsphere.DVPortGroup{}
		for j := range d.PortGroups {
			pg := &d.PortGroups[j]
			if pg.Uplink || hidden[pg.Name] {
				hidden[pg.Name] = true
				continue
			}
			dvByName[pg.Name] = pg
		}
		seen := map[string]bool{}
		for j := range nets {
			n := &nets[j]
			if used[j] || n.Type != "portgroup" || n.Switch != d.Name {
				continue
			}
			used[j] = true
			// Without the switch's own record, the uplink port group is told
			// apart by the name vCenter gives it.
			if hidden[n.Name] || (sw.dvs == nil && strings.Contains(n.Name, "-DVUplinks-")) {
				continue
			}
			seen[n.Name] = true
			sw.portGroups = append(sw.portGroups, newNetPortGroup(ctxName, n, dvByName[n.Name], vmCount(n)))
		}
		// A port group the account cannot see as a network still belongs.
		for j := range d.PortGroups {
			pg := &d.PortGroups[j]
			if hidden[pg.Name] || seen[pg.Name] {
				continue
			}
			sw.portGroups = append(sw.portGroups, newNetPortGroup(ctxName, nil, pg, -1))
		}
		if sw.dvs != nil && topo != nil {
			wireDistributed(&sw, topo, hostsByName)
		}
		out = append(out, sw)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })

	if topo != nil {
		out = append(out, buildStandardSwitches(ctxName, nets, used, topo, vmCount)...)
	}

	// Whatever is left had no switch to sit under.
	var standard, opaque []netPortGroup
	for j := range nets {
		if used[j] {
			continue
		}
		n := &nets[j]
		pg := newNetPortGroup(ctxName, n, nil, vmCount(n))
		if n.Type == "opaque" {
			opaque = append(opaque, pg)
		} else {
			standard = append(standard, pg)
		}
	}
	if len(standard) > 0 {
		name := "standard networks"
		if topo != nil {
			name = "standard networks on no host"
		}
		out = append(out, netSwitch{key: ctxName + "/switch:standard", context: ctxName, id: "standard", name: name, kind: netStandard, group: true, portGroups: standard})
	}
	if len(opaque) > 0 {
		out = append(out, netSwitch{key: ctxName + "/switch:opaque", context: ctxName, id: "opaque", name: "opaque networks", kind: netOpaque, group: true, portGroups: opaque})
	}
	for i := range out {
		sort.SliceStable(out[i].portGroups, func(a, b int) bool { return out[i].portGroups[a].name < out[i].portGroups[b].name })
		judgeSwitch(&out[i])
	}
	return out
}

func newNetPortGroup(ctxName string, n *vsphere.Network, dv *vsphere.DVPortGroup, vms int) netPortGroup {
	pg := netPortGroup{vms: vms, accessible: true}
	if n != nil {
		pg.key, pg.network, pg.name, pg.vlan, pg.accessible = ctxName+"/"+n.ID, n, n.Name, n.VLAN, n.Accessible
	}
	if dv != nil {
		pg.dv = dv
		if pg.key == "" {
			pg.key, pg.name = ctxName+"/pg:"+dv.Key, dv.Name
		}
		if pg.vlan == "" {
			pg.vlan = dv.VLAN
		}
		pg.active, pg.standby, pg.teaming = dv.ActiveUplinks, dv.StandbyUplinks, dv.TeamingPolicy
		pg.promisc, pg.macChanges, pg.forged = dv.Promiscuous, dv.MACChanges, dv.ForgedTransmits
	}
	return pg
}

// wireDistributed fills in a distributed switch's hosts, uplinks and
// VMkernel adapters from the topology read.
func wireDistributed(sw *netSwitch, topo *vsphere.NetworkTopology, hostsByName map[string]*vsphere.Host) {
	d := sw.dvs
	members := map[string]bool{}
	clusters := map[string]bool{}
	var order []string
	seenUplink := map[string]bool{}
	addUplink := func(name string) {
		if name != "" && !seenUplink[name] {
			seenUplink[name] = true
			order = append(order, name)
		}
	}
	names := append([]string(nil), d.Hosts...)
	sort.Strings(names)
	for _, hn := range names {
		members[hn] = true
		sh := netSwitchHost{name: hn, nics: map[string]*netNIC{}}
		if h := hostsByName[hn]; h != nil {
			sh.cluster, sh.connected = h.Cluster, h.ConnectionState == "connected"
			if h.Cluster != "" {
				clusters[h.Cluster] = true
			}
			if ps := proxySwitchFor(h, d); ps != nil {
				sh.mtu = ps.MTU
				for _, u := range ps.Uplinks {
					addUplink(u.Name)
					sh.nics[u.Name] = hostNIC(h, u.NIC)
				}
			}
		}
		sw.hosts = append(sw.hosts, sh)
	}
	var extra []string
	for _, pg := range sw.portGroups {
		for _, u := range append(append([]string(nil), pg.active...), pg.standby...) {
			if !seenUplink[u] {
				extra = append(extra, u)
				seenUplink[u] = true
			}
		}
	}
	sort.Strings(extra)
	sw.uplinks = append(order, extra...)
	for _, h := range topo.Hosts {
		if !members[h.Name] && h.Cluster != "" && clusters[h.Cluster] {
			sw.notJoined = append(sw.notJoined, netHostRef{h.Name, h.Cluster})
		}
	}
	for i := range sw.portGroups {
		pg := &sw.portGroups[i]
		if pg.dv == nil {
			continue
		}
		for _, h := range topo.Hosts {
			for _, v := range h.VMKs {
				if v.DVPortGroupKey == pg.dv.Key && (v.DVSwitchUUID == "" || d.UUID == "" || v.DVSwitchUUID == d.UUID) {
					pg.vmks = append(pg.vmks, netVMK{host: h.Name, device: v.Device, ip: v.IP, mtu: v.MTU})
				}
			}
		}
	}
}

func proxySwitchFor(h *vsphere.Host, d *vsphere.DVSwitch) *vsphere.HostProxySwitch {
	for i := range h.ProxySwitches {
		ps := &h.ProxySwitches[i]
		if (d.UUID != "" && ps.SwitchUUID == d.UUID) || (ps.SwitchUUID == "" && ps.Switch == d.Name) {
			return ps
		}
	}
	return nil
}

func hostNIC(h *vsphere.Host, device string) *netNIC {
	if device == "" {
		return nil
	}
	for _, n := range h.NICs {
		if n.Device == device {
			nic := &netNIC{device: device}
			if n.LinkSpeedMB != nil {
				nic.speedMB, nic.up = *n.LinkSpeedMB, true
			}
			return nic
		}
	}
	// Assigned, but the host does not list it: a NIC with no known link.
	return &netNIC{device: device}
}

// buildStandardSwitches takes every host's standard switch of one name
// together. Two hosts' vSwitch0 are separate objects in vSphere, but they
// are almost always meant to be the same switch, and a difference between
// them is exactly what is worth showing.
func buildStandardSwitches(ctxName string, nets []vsphere.Network, used []bool, topo *vsphere.NetworkTopology, vmCount func(*vsphere.Network) int) []netSwitch {
	byName := map[string]*netSwitch{}
	var names []string
	type pgAgg struct {
		pg    netPortGroup
		vlans map[int32]int
	}
	pgs := map[string]map[string]*pgAgg{}
	for _, h := range topo.Hosts {
		for _, vs := range h.VSwitches {
			sw := byName[vs.Name]
			if sw == nil {
				sw = &netSwitch{context: ctxName, id: "vss:" + vs.Name, name: vs.Name, kind: netStandard, wired: true}
				sw.key = ctxName + "/switch:" + sw.id
				byName[vs.Name] = sw
				names = append(names, vs.Name)
				pgs[vs.Name] = map[string]*pgAgg{}
			}
			sh := netSwitchHost{name: h.Name, cluster: h.Cluster, connected: h.ConnectionState == "connected", mtu: vs.MTU, nics: map[string]*netNIC{}}
			host := h
			for _, dev := range vs.Uplinks {
				sh.nics[dev] = hostNIC(&host, dev)
				if !containsString(sw.uplinks, dev) {
					sw.uplinks = append(sw.uplinks, dev)
				}
			}
			sw.hosts = append(sw.hosts, sh)
		}
		for _, hp := range h.PortGroups {
			if byName[hp.Switch] == nil {
				continue
			}
			agg := pgs[hp.Switch][hp.Name]
			if agg == nil {
				agg = &pgAgg{pg: netPortGroup{name: hp.Name, vms: -1, accessible: true, promisc: hp.Promiscuous, macChanges: hp.MACChanges, forged: hp.ForgedTransmits,
					key: ctxName + "/vsspg:" + hp.Switch + "/" + hp.Name}, vlans: map[int32]int{}}
				pgs[hp.Switch][hp.Name] = agg
			}
			agg.pg.hosts++
			agg.vlans[hp.VLAN]++
			for _, v := range h.VMKs {
				if v.PortGroup == hp.Name && v.DVPortGroupKey == "" {
					agg.pg.vmks = append(agg.pg.vmks, netVMK{host: h.Name, device: v.Device, ip: v.IP, mtu: v.MTU})
				}
			}
		}
	}
	sort.Strings(names)
	out := make([]netSwitch, 0, len(names))
	for _, name := range names {
		sw := byName[name]
		sort.SliceStable(sw.hosts, func(i, j int) bool { return sw.hosts[i].name < sw.hosts[j].name })
		sort.Strings(sw.uplinks)
		mtus := map[int32]int{}
		for _, h := range sw.hosts {
			mtus[h.mtu]++
		}
		sw.mtu, sw.mtuNote = majority(mtus, func(v int32) string { return strconv.Itoa(int(v)) })
		for pgName, agg := range pgs[name] {
			pg := agg.pg
			var vlan int32
			vlan, pg.vlanNote = majorityInt(agg.vlans)
			pg.vlan = standardVLAN(vlan)
			for j := range nets {
				n := &nets[j]
				if n.Type == "standard" && n.Name == pgName {
					pg.network, pg.key, pg.accessible, pg.vms = n, ctxName+"/"+n.ID, n.Accessible, vmCount(n)
					used[j] = true
					break
				}
			}
			sw.portGroups = append(sw.portGroups, pg)
		}
		out = append(out, *sw)
	}
	return out
}

// majority picks the most common value and describes the spread when there
// is more than one, for a setting that should match on every host.
func majority(counts map[int32]int, label func(int32) string) (int32, string) {
	v, _ := majorityPair(counts)
	if len(counts) <= 1 {
		return v, ""
	}
	return v, spread(counts, label)
}

func majorityInt(counts map[int32]int) (int32, string) {
	return majority(counts, standardVLAN)
}

// strictMajority is the value more hosts have than any other, if one does.
func strictMajority(counts map[int32]int) (int32, bool) {
	best, n := majorityPair(counts)
	for v, c := range counts {
		if v != best && c == n {
			return 0, false
		}
	}
	return best, true
}

func majorityPair(counts map[int32]int) (int32, int) {
	var best int32
	n := -1
	for v, c := range counts {
		if c > n || (c == n && v < best) {
			best, n = v, c
		}
	}
	return best, n
}

func spread(counts map[int32]int, label func(int32) string) string {
	keys := make([]int32, 0, len(counts))
	for v := range counts {
		keys = append(keys, v)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, v := range keys {
		parts = append(parts, fmt.Sprintf("%s on %s", label(v), nounCount(counts[v], "host")))
	}
	return strings.Join(parts, ", ")
}

// standardVLAN renders a standard port group's VLAN ID the way the vSphere
// Client does: 0 is no tagging and 4095 passes every VLAN through.
func standardVLAN(v int32) string {
	switch v {
	case 0:
		return "none"
	case 4095:
		return "all (4095)"
	default:
		return strconv.Itoa(int(v))
	}
}

// nounCount states a number with its noun, singular for one.
func nounCount(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// speedLabel shortens a link speed to what a grid cell has room for.
func speedLabel(mb int32) string {
	switch {
	case mb >= 1000 && mb%1000 == 0:
		return strconv.Itoa(int(mb/1000)) + "G"
	case mb >= 1000:
		return strconv.FormatFloat(float64(mb)/1000, 'f', 1, 64) + "G"
	default:
		return strconv.Itoa(int(mb)) + "M"
	}
}

func (n *netNIC) label() string {
	if n == nil {
		return "–"
	}
	if !n.up {
		return n.device + " down"
	}
	return n.device + " " + speedLabel(n.speedMB)
}

// ---- findings ------------------------------------------------------------

// judgeSwitch records what is worth an operator's attention on a switch and
// sets each port group's status and notes. Every finding names the host and
// the part involved, so it can be acted on without opening anything else.
func judgeSwitch(sw *netSwitch) {
	var f []netFinding
	add := func(st rowStatus, format string, args ...any) {
		f = append(f, netFinding{status: st, text: fmt.Sprintf(format, args...)})
	}
	for _, h := range sw.notJoined {
		add(statusWarn, "%s is in %s but not on this switch", h.name, h.cluster)
	}
	if sw.mtuNote != "" {
		add(statusWarn, "MTU differs between hosts: %s", sw.mtuNote)
	}
	// Uplinks a port group relies on, for judging the ones with no NIC.
	inUse := map[string]bool{}
	for _, pg := range sw.portGroups {
		for _, u := range append(append([]string(nil), pg.active...), pg.standby...) {
			inUse[u] = true
		}
	}
	// Uplinks: no NIC behind one, a NIC with no link, and a speed that
	// differs from the rest.
	for _, u := range sw.uplinks {
		speeds := map[int32]int{}
		var without []string
		withNIC := 0
		for _, h := range sw.hosts {
			nic, ok := h.nics[u]
			if !ok {
				continue
			}
			if nic == nil {
				without = append(without, h.name)
				continue
			}
			withNIC++
		}
		switch {
		case withNIC == 0 && len(without) > 0 && inUse[u]:
			add(statusWarn, "%s has no physical NIC on any host, so port groups that use it have one uplink fewer", u)
		case withNIC > 0 && len(without) > 0:
			add(statusWarn, "%s has no physical NIC on %s", u, strings.Join(without, ", "))
		}
		for _, h := range sw.hosts {
			nic, ok := h.nics[u]
			if !ok || nic == nil {
				continue
			}
			if !nic.up {
				add(statusBad, "%s %s (%s) has no link", h.name, nic.device, u)
				continue
			}
			speeds[nic.speedMB]++
		}
		if len(speeds) > 1 {
			common, ok := strictMajority(speeds)
			var each []string
			for _, h := range sw.hosts {
				nic := h.nics[u]
				if nic == nil || !nic.up {
					continue
				}
				switch {
				case !ok:
					each = append(each, speedLabel(nic.speedMB)+" on "+h.name)
				case nic.speedMB != common:
					add(statusWarn, "%s %s runs at %s; the other hosts' are %s", h.name, u, speedLabel(nic.speedMB), speedLabel(common))
				}
			}
			if !ok {
				// No speed is the usual one, so no host is the odd one out.
				add(statusWarn, "%s speeds differ between hosts: %s", u, strings.Join(each, ", "))
			}
		}
	}
	for i := range sw.portGroups {
		pg := &sw.portGroups[i]
		pg.status, pg.notes = statusGood, nil
		note := func(st rowStatus, s string) {
			pg.notes = append(pg.notes, s)
			if statusRank(st) < statusRank(pg.status) {
				pg.status = st
			}
		}
		if !pg.accessible {
			note(statusBad, "inaccessible")
		}
		if pg.promisc != nil && *pg.promisc {
			note(statusWarn, "promiscuous")
			add(statusWarn, "%s allows promiscuous mode", pg.name)
		}
		if pg.vlanNote != "" {
			note(statusWarn, "VLAN differs")
			add(statusWarn, "%s VLAN differs between hosts: %s", pg.name, pg.vlanNote)
		}
		if sw.kind == netStandard && !sw.group && pg.hosts > 0 && pg.hosts < len(sw.hosts) {
			// A VM on it cannot move to a host without it.
			note(statusWarn, fmt.Sprintf("on %d of %d hosts", pg.hosts, len(sw.hosts)))
			add(statusWarn, "%s is on %d of the %d hosts with %s", pg.name, pg.hosts, len(sw.hosts), sw.name)
		}
		if sw.mtu > 0 {
			for _, v := range pg.vmks {
				if v.mtu > sw.mtu {
					note(statusBad, "vmk MTU")
					add(statusBad, "%s %s on %s has MTU %d, above the switch's %d", v.host, v.device, pg.name, v.mtu, sw.mtu)
				}
			}
		}
		// Teaming: on each host, does the port group still have a path out?
		if sw.kind == netDistributed && sw.wired && len(pg.active)+len(pg.standby) > 0 {
			var none, standbyOnly []string
			for _, h := range sw.hosts {
				if !h.connected || len(h.nics) == 0 {
					continue
				}
				active, standby := linkedUplinks(h, pg.active), linkedUplinks(h, pg.standby)
				switch {
				case active == 0 && standby == 0:
					none = append(none, h.name)
				case active == 0:
					standbyOnly = append(standbyOnly, h.name)
				}
			}
			if len(none) > 0 {
				note(statusBad, "down on "+nounCount(len(none), "host"))
				add(statusBad, "%s has no working uplink on %s", pg.name, strings.Join(none, ", "))
			}
			if len(standbyOnly) > 0 {
				note(statusWarn, "standby on "+nounCount(len(standbyOnly), "host"))
				add(statusWarn, "%s runs on its standby uplink only on %s", pg.name, strings.Join(standbyOnly, ", "))
			}
		}
		if pg.vms == 0 && len(pg.vmks) == 0 && sw.wired {
			// Unused is not a problem, so it only changes a status nothing
			// else has.
			pg.notes = append(pg.notes, "unused")
			if pg.status == statusGood {
				pg.status = statusIdle
			}
		}
	}
	sw.findings = f
}

func linkedUplinks(h netSwitchHost, uplinks []string) int {
	n := 0
	for _, u := range uplinks {
		if nic := h.nics[u]; nic != nil && nic.up {
			n++
		}
	}
	return n
}

// ---- rows ----------------------------------------------------------------

// netRowGroup is a switch's row and its port groups' rows. The table is
// filtered, sorted and folded a group at a time, so a port group is never
// shown without the switch it belongs to.
type netRowGroup struct {
	head     row
	children []row
}

// networkTreeColumns are the grouped tab's columns. NOTES comes before VMK
// and HOSTS so it is the last to be dropped on a narrow terminal: it is
// where a problem shows.
func networkTreeColumns(withContext bool) []column {
	var cols []column
	if withContext {
		cols = append(cols, column{title: "VCENTER", width: 14})
	}
	return append(cols,
		column{title: "NAME"},
		column{title: "VLAN", width: 10},
		column{title: "VMS", width: 5, right: true},
		column{title: "NOTES", width: 20},
		column{title: "VMK", width: 4, right: true},
		column{title: "HOSTS", width: 6, right: true},
	)
}

// networkGroups returns this context's grouped rows, built once per
// inventory and topology change like rowsFor.
func (s *contextState) networkGroups(withContext bool, state netTopoState) []netRowGroup {
	if s.netGroups != nil && s.netGroupsContext == withContext && s.netGroupsState == state {
		return s.netGroups
	}
	switches := s.netSwitches(state)
	groups := make([]netRowGroup, 0, len(switches))
	for i := range switches {
		groups = append(groups, switchRowGroup(&switches[i], withContext, state))
	}
	s.netGroups, s.netGroupsContext, s.netGroupsState = groups, withContext, state
	return groups
}

// netSwitches is the switch model the rows and the workspace both read.
func (s *contextState) netSwitches(state netTopoState) []netSwitch {
	if s.netSwitchCache != nil {
		return s.netSwitchCache
	}
	var topo *vsphere.NetworkTopology
	if state == topoLoaded {
		topo = s.netTopo.topo
	}
	name := ""
	if s.cc != nil {
		name = s.cc.Name
	}
	s.netSwitchCache = buildNetSwitches(name, s.inv, topo)
	return s.netSwitchCache
}

// invalidateNetwork drops the grouped rows and switch model, for a new
// topology read; invalidateRows drops them along with every other kind's.
func (s *contextState) invalidateNetwork() {
	s.netGroups, s.netSwitchCache = nil, nil
}

func switchRowGroup(sw *netSwitch, withContext bool, state netTopoState) netRowGroup {
	vms, counted := sw.vmTotal()
	vmCell := "-"
	if counted {
		vmCell = strconv.Itoa(vms)
	}
	vmkCell, hostsCell := "", ""
	if sw.wired {
		vmkCell = strconv.Itoa(sw.vmkTotal())
		hostsCell = strconv.Itoa(len(sw.hosts))
		if len(sw.notJoined) > 0 {
			hostsCell = fmt.Sprintf("%d/%d", len(sw.hosts), len(sw.hosts)+len(sw.notJoined))
		}
	}
	notes := switchNotes(sw, state)
	status, glyph := sw.status(), glyphOnline
	if status == statusBad {
		glyph = glyphFail
	}
	if sw.group {
		status, glyph = statusNone, glyphSkip
	}
	head := row{
		key: sw.key, context: sw.context, kind: vsphere.KindNetwork, name: sw.name,
		where: sw.where, glyph: glyph, status: status,
		cells:  lead(withContext, sw.context, switchCell(sw), "", vmCell, notes, vmkCell, hostsCell),
		detail: switchFields(sw),
		target: actionTarget{moref: sw.dvsID(), morefKind: "VmwareDistributedVirtualSwitch", path: sw.where.Path},
		tree:   treeInfo{head: true, switchKey: sw.key},
	}
	g := netRowGroup{head: head}
	for i := range sw.portGroups {
		g.children = append(g.children, portGroupRow(sw, &sw.portGroups[i], withContext))
	}
	return g
}

// switchCell names a switch in the table with its kind; a catch-all group's
// name already says what it is.
func switchCell(sw *netSwitch) string {
	if sw.group {
		return sw.name
	}
	return sw.name + "  " + sw.kindLabel()
}

// shortUplinks drops the prefix every uplink of a switch shares when what
// is left is a number, so "dvUplink1, dvUplink2" fits a column as "1, 2".
func shortUplinks(sw *netSwitch, uplinks []string) string {
	prefix := uplinkPrefix(sw.uplinks)
	if prefix == "" {
		return listOrDash(uplinks)
	}
	out := make([]string, 0, len(uplinks))
	for _, u := range uplinks {
		rest, ok := strings.CutPrefix(u, prefix)
		if _, err := strconv.Atoi(rest); !ok || err != nil {
			return listOrDash(uplinks)
		}
		out = append(out, rest)
	}
	return listOrDash(out)
}

// uplinkPrefix is the non-numeric prefix every uplink name shares.
func uplinkPrefix(names []string) string {
	if len(names) < 2 {
		return ""
	}
	prefix := strings.TrimRight(names[0], "0123456789")
	for _, n := range names[1:] {
		if strings.TrimRight(n, "0123456789") != prefix {
			return ""
		}
	}
	return prefix
}

func (s *netSwitch) dvsID() string {
	if s.dvs != nil {
		return s.dvs.ID
	}
	return ""
}

func switchNotes(sw *netSwitch, state netTopoState) string {
	switch {
	case sw.kind == netOpaque:
		return "NSX segments"
	case state == topoLoading && !sw.wired:
		return "reading wiring…"
	case state == topoFailed && !sw.wired:
		return "wiring not read"
	case sw.group:
		return ""
	}
	bad, warn := 0, 0
	for _, f := range sw.findings {
		switch f.status {
		case statusBad:
			bad++
		case statusWarn:
			warn++
		}
	}
	switch {
	case bad > 0 && warn > 0:
		return fmt.Sprintf("%s %d · %s %d", glyphFail, bad, glyphCheckWarn, warn)
	case bad > 0:
		return glyphFail + " " + nounCount(bad, "problem")
	case warn > 0:
		return glyphCheckWarn + " " + nounCount(warn, "finding")
	}
	return ""
}

func portGroupRow(sw *netSwitch, pg *netPortGroup, withContext bool) row {
	glyph := glyphOnline
	switch pg.status {
	case statusBad:
		glyph = glyphFail
	case statusIdle:
		glyph = glyphOffline
	}
	vms := "-"
	if pg.vms >= 0 {
		vms = strconv.Itoa(pg.vms)
	}
	vmk, hosts := "", ""
	if sw.wired {
		vmk = strconv.Itoa(len(pg.vmks))
	}
	if sw.kind == netStandard && pg.hosts > 0 {
		hosts = strconv.Itoa(pg.hosts)
	}
	r := row{
		key: pg.key, context: sw.context, kind: vsphere.KindNetwork, name: pg.name,
		glyph: glyph, status: pg.status,
		cells:  lead(withContext, sw.context, pg.name, humanize.Dash(pg.vlan), vms, strings.Join(pg.notes, " · "), vmk, hosts),
		detail: portGroupFields(sw, pg),
		tree:   treeInfo{switchKey: sw.key},
	}
	if n := pg.network; n != nil {
		r.where = n.Location
		r.target = actionTarget{moref: n.ID, morefKind: networkMorefKind(n.Type)}
	}
	return r
}

func switchFields(sw *netSwitch) []field {
	fields := []field{{"vCenter", sw.context}, {"Kind", sw.kindLabel()}}
	if d := sw.dvs; d != nil {
		fields = append(fields,
			field{"Version", humanize.Dash(strings.TrimSpace(d.Vendor + " " + d.Version))},
			field{"MTU", mtuText(sw)},
			field{"LACP", humanize.Dash(d.LACPVersion)},
			field{"Discovery", humanize.Dash(strings.TrimSpace(strings.ToUpper(d.LinkDiscoveryProtocol) + " " + d.LinkDiscoveryOperation))},
			field{"Ports", portsText(d)},
		)
	} else if sw.kind == netStandard && !sw.group {
		fields = append(fields, field{"MTU", mtuText(sw)})
	}
	if sw.wired {
		hosts := strconv.Itoa(len(sw.hosts))
		if len(sw.notJoined) > 0 {
			hosts += fmt.Sprintf(" (%d more in the same clusters are not on it)", len(sw.notJoined))
		}
		fields = append(fields, field{"Hosts", hosts}, field{"Uplinks", listOrDash(sw.uplinks)})
	}
	fields = append(fields, field{"Port groups", strconv.Itoa(len(sw.portGroups))})
	if d := sw.dvs; d != nil {
		if d.Contact != "" {
			fields = append(fields, field{"Contact", strings.TrimSpace(d.Contact + " " + d.ContactDetail)})
		}
		fields = append(fields,
			field{"Datacenter", humanize.Dash(d.Datacenter)},
			field{"Inventory path", humanize.Dash(d.Path)},
			field{"Managed object", d.ID},
		)
	}
	return fields
}

// portsText is a switch's port count against its limit. vCenter reports
// "no limit" as the largest int32.
func portsText(d *vsphere.DVSwitch) string {
	if d.MaxPorts <= 0 || d.MaxPorts == 1<<31-1 {
		return fmt.Sprintf("%d (no limit)", d.NumPorts)
	}
	return fmt.Sprintf("%d of %d", d.NumPorts, d.MaxPorts)
}

func mtuText(sw *netSwitch) string {
	if sw.mtu == 0 {
		return "-"
	}
	s := strconv.Itoa(int(sw.mtu))
	if sw.mtuNote != "" {
		s += " (" + sw.mtuNote + ")"
	}
	return s
}

func portGroupFields(sw *netSwitch, pg *netPortGroup) []field {
	fields := []field{{"vCenter", sw.context}, {"Switch", sw.name + " (" + sw.kindLabel() + ")"}}
	vlan := humanize.Dash(pg.vlan)
	if pg.vlanNote != "" {
		vlan += " (" + pg.vlanNote + ")"
	}
	fields = append(fields, field{"VLAN", vlan})
	if pg.vms >= 0 {
		fields = append(fields, field{"Virtual machines", strconv.Itoa(pg.vms)})
	}
	if sw.wired {
		fields = append(fields, field{"VMkernel adapters", vmkText(pg.vmks)})
	}
	if pg.hosts > 0 {
		fields = append(fields, field{"Hosts", strconv.Itoa(pg.hosts)})
	}
	if pg.dv != nil {
		fields = append(fields,
			field{"Teaming", humanize.Dash(pg.teaming)},
			field{"Active uplinks", listOrDash(pg.active)},
			field{"Standby uplinks", listOrDash(pg.standby)},
		)
	}
	if sec := securityText(pg); sec != "" {
		fields = append(fields, field{"Security", sec})
	}
	if n := pg.network; n != nil {
		fields = append(fields,
			field{"Accessible", yesNo(n.Accessible)},
			field{"Datacenter", humanize.Dash(n.Datacenter)},
			field{"Inventory path", humanize.Dash(n.Path)},
			field{"Managed object", n.ID},
		)
	}
	return fields
}

func vmkText(vmks []netVMK) string {
	if len(vmks) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(vmks))
	for _, v := range vmks {
		s := v.host + " " + v.device
		if v.ip != "" {
			s += " " + v.ip
		}
		parts = append(parts, s+fmt.Sprintf(" (MTU %d)", v.mtu))
	}
	return strings.Join(parts, ", ")
}

// securityText states the three security policy settings, leaving out any
// the collector could not read.
func securityText(pg *netPortGroup) string {
	var parts []string
	add := func(label string, v *bool) {
		if v == nil {
			return
		}
		word := "reject"
		if *v {
			word = "accept"
		}
		parts = append(parts, label+" "+word)
	}
	add("promiscuous", pg.promisc)
	add("MAC changes", pg.macChanges)
	add("forged transmits", pg.forged)
	return strings.Join(parts, " · ")
}

// ---- the grouped table ----------------------------------------------------

const (
	foldOpen   = "▾"
	foldClosed = "▹"
)

// networkTreeRows is rows() for the grouped Networks tab. A filter or a jump
// keeps a switch when it or any of its port groups match, and keeps only the
// matching port groups unless the switch itself matched. Sorting by status
// moves whole switches. A folded switch hides its port groups unless a
// filter is narrowing the table, so a match is never hidden.
func (m *Model) networkTreeRows() []row {
	withContext := m.showContext()
	var groups []netRowGroup
	for _, st := range m.inScope() {
		if st.inv == nil {
			continue
		}
		groups = append(groups, st.networkGroups(withContext, m.topoStateOf(st))...)
	}
	needle := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	jump := m.jump
	if jump != nil && jump.kind != m.kind {
		jump = nil
	}
	narrowed := needle != "" || jump != nil
	match := func(r row) bool {
		if needle != "" && !strings.Contains(strings.ToLower(r.name), needle) {
			return false
		}
		return jump == nil || jump.matches(r)
	}
	kept := make([]netRowGroup, 0, len(groups))
	for _, g := range groups {
		if !narrowed || match(g.head) {
			kept = append(kept, g)
			continue
		}
		var kids []row
		for _, c := range g.children {
			if match(c) {
				kids = append(kids, c)
			}
		}
		if len(kids) > 0 {
			kept = append(kept, netRowGroup{head: g.head, children: kids})
		}
	}
	if m.sortMode == sortByStatus {
		sort.SliceStable(kept, func(i, j int) bool {
			return statusRank(kept[i].head.status) < statusRank(kept[j].head.status)
		})
	}
	nameAt := 0
	if withContext {
		nameAt = 1
	}
	var out []row
	for _, g := range kept {
		folded := m.netFolded[g.head.key] && !narrowed
		head := g.head
		head.cells = append([]string(nil), head.cells...)
		glyph := foldOpen
		if folded || len(g.children) == 0 {
			glyph = foldClosed
		}
		head.cells[nameAt] = glyph + " " + head.cells[nameAt]
		out = append(out, head)
		if folded {
			continue
		}
		for i, c := range g.children {
			c.cells = append([]string(nil), c.cells...)
			branch := "├ "
			if i == len(g.children)-1 {
				branch = "└ "
			}
			c.cells[nameAt] = "  " + branch + c.cells[nameAt]
			out = append(out, c)
		}
	}
	return out
}

// toggleFold folds or unfolds the switch under the cursor, or the switch of
// the port group under it, leaving the cursor on that switch.
func (m *Model) toggleFold() {
	r, ok := m.currentRow()
	if !ok || r.kind != vsphere.KindNetwork || r.tree.switchKey == "" {
		return
	}
	if m.netFolded == nil {
		m.netFolded = map[string]bool{}
	}
	key := r.tree.switchKey
	m.netFolded[key] = !m.netFolded[key]
	for i, candidate := range m.visibleRows() {
		if candidate.key == key {
			m.moveTo(i)
			return
		}
	}
	m.clampCursor()
}

// findSwitch returns the switch a key names, from the context it belongs to.
func (m *Model) findSwitch(key string) (*netSwitch, *contextState) {
	for _, st := range m.states {
		if st.inv == nil || st.cc == nil || !strings.HasPrefix(key, st.cc.Name+"/") {
			continue
		}
		switches := st.netSwitches(m.topoStateOf(st))
		for i := range switches {
			if switches[i].key == key {
				return &switches[i], st
			}
		}
	}
	return nil, nil
}
