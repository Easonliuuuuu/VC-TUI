package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/network"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// The VLAN map is a view of the Networks tab that answers where each VLAN
// exists across the vCenters in scope. Rows are VLANs; each vCenter is a
// column saying how many VMs use the VLAN there and how many of its clusters
// can reach it. Picking a source and a target cluster turns it into the
// comparison `vsfleet network compare` makes from a stored assessment, made
// here from the live inventory and topology read with the same matching
// rules (internal/network), so the two never disagree about what is missing.

// vlanMapView is the open map's cursor and its cluster picker.
type vlanMapView struct {
	cursor int
	offset int
	// where shows, under the table, where the selected VLAN is.
	where bool
	pick  *pairPick
}

type clusterRef struct{ context, name string }

func (c clusterRef) String() string { return c.context + "/" + c.name }

// vlanPair is the source and target cluster the map compares.
type vlanPair struct{ source, target clusterRef }

// pairPick is the cluster picker: the source first, then the target.
type pairPick struct {
	step    int
	cursor  int
	source  clusterRef
	options []clusterRef
	hosts   map[clusterRef]int
}

// ---- live evidence ---------------------------------------------------------

// liveNet is one vCenter's inventory and topology read with the lookups the
// map needs, so a VM's network and a VMkernel adapter's port group resolve to
// a VLAN the same way for both switch kinds.
type liveNet struct {
	context string
	inv     *vsphere.Inventory
	topo    *vsphere.NetworkTopology
	hosts   map[string]*vsphere.Host
	nets    map[string]*vsphere.Network
	dvByID  map[string]*vsphere.DVPortGroup
	dvByKey map[string]*vsphere.DVPortGroup
	uplinks map[string]bool
	// hidden are distributed switches the hosts' own configuration names
	// that the account cannot read, by host: their VLANs are missing here.
	hidden map[string][]string
}

func newLiveNet(context string, inv *vsphere.Inventory, topo *vsphere.NetworkTopology) *liveNet {
	l := &liveNet{context: context, inv: inv, topo: topo,
		hosts: map[string]*vsphere.Host{}, nets: map[string]*vsphere.Network{},
		dvByID: map[string]*vsphere.DVPortGroup{}, dvByKey: map[string]*vsphere.DVPortGroup{}, uplinks: map[string]bool{}}
	for i := range topo.Hosts {
		l.hosts[topo.Hosts[i].Name] = &topo.Hosts[i]
	}
	if inv != nil {
		for i := range inv.Networks {
			l.nets[inv.Networks[i].ID] = &inv.Networks[i]
		}
	}
	readable := map[string]bool{}
	for i := range topo.Switches {
		readable[topo.Switches[i].UUID] = true
		readable[topo.Switches[i].Name] = true
	}
	l.hidden = map[string][]string{}
	for _, h := range topo.Hosts {
		for _, ps := range h.ProxySwitches {
			if !readable[ps.SwitchUUID] && !readable[ps.Switch] {
				l.hidden[h.Name] = append(l.hidden[h.Name], ps.Switch)
			}
		}
	}
	for i := range topo.Switches {
		d := &topo.Switches[i]
		for j := range d.PortGroups {
			pg := &d.PortGroups[j]
			if pg.Uplink || containsString(d.UplinkPorts, pg.Name) {
				l.uplinks[pg.Name] = true
				continue
			}
			l.dvByID[pg.ID] = pg
			l.dvByKey[d.UUID+"/"+pg.Key] = pg
			l.dvByKey["/"+pg.Key] = pg
		}
	}
	return l
}

// group is the cluster a host belongs to, or the host itself when it is
// standalone: its own compute resource, the way the Clusters tab lists it.
func hostGroup(h *vsphere.Host) string {
	if h.Cluster != "" {
		return h.Cluster
	}
	return h.Name
}

func (l *liveNet) clusterHosts() map[string][]vsphere.Host {
	out := map[string][]vsphere.Host{}
	for _, h := range l.topo.Hosts {
		g := hostGroup(&h)
		out[g] = append(out[g], h)
	}
	return out
}

// blindness says what this vCenter's read could not see among the given
// hosts: distributed switches the hosts are on that the account cannot read.
func (l *liveNet) blindness(hosts []vsphere.Host) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hosts {
		for _, s := range l.hidden[h.Name] {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

// unreadClusters are the inventory's clusters with no host in the wiring
// read: the account cannot see their hosts, so no VLAN can be placed there.
func (l *liveNet) unreadClusters() []string {
	if l.inv == nil {
		return nil
	}
	groups := l.clusterHosts()
	var out []string
	for _, c := range l.inv.Clusters {
		if !c.Standalone && len(groups[c.Name]) == 0 {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// reachable is network.Reachable for a set of hosts, less the networks none
// of them is on and the uplink port groups, with VLANs in canonical form so
// a standard and a distributed port group on the same VLAN match.
func (l *liveNet) reachable(hosts []vsphere.Host) []network.NetworkSummary {
	var out []network.NetworkSummary
	for _, s := range network.Reachable(hosts, l.topo.Switches) {
		if s.CoveredHosts == 0 || (s.Type == "distributed" && l.uplinks[s.Name]) {
			continue
		}
		s.VLAN = network.ParseVLAN(s.VLAN).String()
		out = append(out, s)
	}
	return out
}

// vmGroup is the cluster a VM runs in.
func (l *liveNet) vmGroup(vm *vsphere.VM) string {
	if h := l.hosts[vm.Host]; h != nil {
		return hostGroup(h)
	}
	if vm.Cluster != "" {
		return vm.Cluster
	}
	return vm.Host
}

// networkVLAN resolves a VM adapter's network to its VLAN and name. A
// standard network's VLAN is the port group's on the VM's own host, since the
// inventory records none for standard networks.
func (l *liveNet) networkVLAN(id, host string) (network.VLAN, string, bool) {
	n := l.nets[id]
	if n == nil {
		return network.VLAN{}, "", false
	}
	switch n.Type {
	case "portgroup":
		if l.uplinks[n.Name] {
			return network.VLAN{}, "", false
		}
		if pg := l.dvByID[id]; pg != nil {
			return network.ParseVLAN(pg.VLAN), n.Name, true
		}
		return network.ParseVLAN(n.VLAN), n.Name, true
	case "opaque":
		return network.VLAN{}, "", false
	}
	if h := l.hosts[host]; h != nil {
		for _, pg := range h.PortGroups {
			if pg.Name == n.Name {
				return network.StandardVLAN(pg.VLAN), n.Name, true
			}
		}
	}
	for _, h := range l.topo.Hosts {
		for _, pg := range h.PortGroups {
			if pg.Name == n.Name {
				return network.StandardVLAN(pg.VLAN), n.Name, true
			}
		}
	}
	return network.VLAN{}, "", false
}

// vmkVLAN resolves a VMkernel adapter's port group to its VLAN and name.
func (l *liveNet) vmkVLAN(h *vsphere.Host, v vsphere.HostVMKernel) (network.VLAN, string, bool) {
	if v.DVPortGroupKey != "" {
		pg := l.dvByKey[v.DVSwitchUUID+"/"+v.DVPortGroupKey]
		if pg == nil {
			pg = l.dvByKey["/"+v.DVPortGroupKey]
		}
		if pg == nil {
			return network.VLAN{}, "", false
		}
		return network.ParseVLAN(pg.VLAN), pg.Name, true
	}
	for _, pg := range h.PortGroups {
		if pg.Name == v.PortGroup {
			return network.StandardVLAN(pg.VLAN), pg.Name, true
		}
	}
	return network.VLAN{}, "", false
}

// ---- the estate map --------------------------------------------------------

type vlanColumn struct {
	context  string
	state    netTopoState
	clusters []string
	// blind says what the column cannot show: unreadable switches and
	// clusters whose hosts the account cannot see.
	blind []string
}

type vlanCell struct {
	clusters   map[string]bool
	names      map[string]bool
	vms        int
	clusterVMs map[string]int
	vmks       int
}

func (c *vlanCell) present() bool {
	return c != nil && (len(c.clusters) > 0 || c.vms > 0 || c.vmks > 0)
}

type vlanRow struct {
	vlan   network.VLAN
	cells  map[string]*vlanCell
	name   string
	names  []string
	note   string
	status rowStatus
}

type vlanEstate struct {
	cols   []vlanColumn
	rows   []vlanRow
	trunks []vlanRow
}

func (e *vlanEstate) clusterCount() int {
	n := 0
	for _, c := range e.cols {
		n += len(c.clusters)
	}
	return n
}

// all is the rows in the order the cursor moves through them.
func (e *vlanEstate) all() []vlanRow { return append(append([]vlanRow(nil), e.rows...), e.trunks...) }

func (m *Model) vlanEstate() *vlanEstate {
	e := &vlanEstate{}
	byKey := map[string]*vlanRow{}
	var order []string
	cell := func(v network.VLAN, ctx string) *vlanCell {
		k := v.String()
		r := byKey[k]
		if r == nil {
			r = &vlanRow{vlan: v, cells: map[string]*vlanCell{}}
			byKey[k] = r
			order = append(order, k)
		}
		c := r.cells[ctx]
		if c == nil {
			c = &vlanCell{clusters: map[string]bool{}, names: map[string]bool{}, clusterVMs: map[string]int{}}
			r.cells[ctx] = c
		}
		return c
	}
	for _, st := range m.inScope() {
		if st.inv == nil || st.cc == nil {
			continue
		}
		col := vlanColumn{context: st.cc.Name, state: m.topoStateOf(st)}
		if col.state != topoLoaded {
			e.cols = append(e.cols, col)
			continue
		}
		l := newLiveNet(col.context, st.inv, st.netTopo.topo)
		groups := l.clusterHosts()
		col.clusters = sortedKeys(groups)
		if hidden := l.blindness(l.topo.Hosts); len(hidden) > 0 {
			col.blind = append(col.blind, strings.Join(hidden, ", ")+" is on its hosts but not readable, so its VLANs are missing")
		}
		if unread := l.unreadClusters(); len(unread) > 0 {
			they := "it is"
			if len(unread) > 1 {
				they = "they are"
			}
			col.blind = append(col.blind, "no host of "+strings.Join(unread, ", ")+" is readable, so "+they+" not on the map")
		}
		for _, g := range col.clusters {
			for _, s := range l.reachable(groups[g]) {
				c := cell(network.ParseVLAN(s.VLAN), col.context)
				c.clusters[g] = true
				c.names[s.Name] = true
			}
		}
		for i := range st.inv.VMs {
			vm := &st.inv.VMs[i]
			seen := map[string]bool{}
			for _, nic := range vm.NICs {
				v, name, ok := l.networkVLAN(nic.NetworkID, vm.Host)
				if !ok || seen[v.String()] {
					continue
				}
				seen[v.String()] = true
				c := cell(v, col.context)
				c.names[name] = true
				c.vms++
				c.clusterVMs[l.vmGroup(vm)]++
			}
		}
		for i := range l.topo.Hosts {
			h := &l.topo.Hosts[i]
			for _, v := range h.VMKs {
				if vl, name, ok := l.vmkVLAN(h, v); ok {
					c := cell(vl, col.context)
					c.names[name] = true
					c.vmks++
				}
			}
		}
		e.cols = append(e.cols, col)
	}
	loaded := 0
	for _, c := range e.cols {
		if c.state == topoLoaded {
			loaded++
		}
	}
	for _, k := range order {
		r := byKey[k]
		judgeVLANRow(r, e.cols, loaded)
		if r.vlan.Kind == network.VLANTrunk {
			e.trunks = append(e.trunks, *r)
		} else {
			e.rows = append(e.rows, *r)
		}
	}
	sort.SliceStable(e.rows, func(i, j int) bool { return vlanLess(e.rows[i].vlan, e.rows[j].vlan) })
	sort.SliceStable(e.trunks, func(i, j int) bool { return e.trunks[i].vlan.String() < e.trunks[j].vlan.String() })
	return e
}

// vlanLess orders untagged first, then VLAN IDs, then private VLANs.
func vlanLess(a, b network.VLAN) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.ID < b.ID
}

// judgeVLANRow names the row and notes what is worth seeing: the same VLAN
// under names that no vCenter shares, which breaks mapping by name, and a
// VLAN only one vCenter has.
func judgeVLANRow(r *vlanRow, cols []vlanColumn, loaded int) {
	inCtx := map[string]int{}
	var present, firstSeen []string
	for _, col := range cols {
		c := r.cells[col.context]
		if !c.present() {
			continue
		}
		present = append(present, col.context)
		for _, n := range sortedKeys(c.names) {
			if inCtx[n] == 0 {
				firstSeen = append(firstSeen, n)
			}
			inCtx[n]++
		}
	}
	r.names = sortedKeys(inCtx)
	// The row is named for the name most vCenters use, and among equals for
	// the one the first column uses, so the table reads from its left.
	for _, n := range firstSeen {
		if r.name == "" || inCtx[n] > inCtx[r.name] {
			r.name = n
		}
	}
	r.status = statusGood
	var notes []string
	switch {
	case len(present) > 1 && inCtx[r.name] < len(present):
		r.status = statusWarn
		notes = append(notes, glyphCheckWarn+" names differ: "+strings.Join(r.names, ", "))
	case len(r.names) > 1:
		notes = append(notes, nounCount(len(r.names), "name"))
	}
	if loaded > 1 && len(present) == 1 {
		notes = append(notes, "only on "+present[0])
	}
	r.note = strings.Join(notes, " · ")
}

// ---- the cluster pair ------------------------------------------------------

type vlanPairRow struct {
	vlan     network.VLAN
	name     string
	src, tgt string
	missing  bool
	note     string
	status   rowStatus
	attached []string
}

type vlanPairResult struct {
	rows       []vlanPairRow
	blockers   int
	advisories int
	// blind names what either side could not read, which makes the verdict
	// unknown the way network compare's blind contexts do.
	blind []string
	// why says why there is nothing to compare yet.
	why string
}

// clusterSide is one cluster's networks with the VMs and VMkernel adapters
// on each, by network name.
type clusterSide struct {
	nets  []network.NetworkSummary
	vms   map[string][]string
	vmks  map[string]int
	hosts int
	blind []string
}

func (m *Model) clusterSide(ref clusterRef) (*clusterSide, string) {
	st, ok := m.byName[ref.context]
	if !ok || st.inv == nil {
		return nil, ref.context + " is not loaded"
	}
	switch m.topoStateOf(st) {
	case topoLoading:
		return nil, "reading " + ref.context + " wiring…"
	case topoFailed, topoUnsupported:
		return nil, ref.context + " wiring not read"
	}
	l := newLiveNet(ref.context, st.inv, st.netTopo.topo)
	hosts := l.clusterHosts()[ref.name]
	if len(hosts) == 0 {
		return nil, ref.String() + " has no hosts in the wiring read"
	}
	side := &clusterSide{nets: l.reachable(hosts), vms: map[string][]string{}, vmks: map[string]int{}, hosts: len(hosts), blind: l.blindness(hosts)}
	for i := range st.inv.VMs {
		vm := &st.inv.VMs[i]
		if l.vmGroup(vm) != ref.name {
			continue
		}
		seen := map[string]bool{}
		for _, nic := range vm.NICs {
			name := nic.Network
			if n := l.nets[nic.NetworkID]; n != nil {
				name = n.Name
			}
			if name != "" && !seen[name] {
				seen[name] = true
				side.vms[name] = append(side.vms[name], vm.Name)
			}
		}
	}
	for i := range hosts {
		for _, v := range hosts[i].VMKs {
			if _, name, ok := l.vmkVLAN(&hosts[i], v); ok {
				side.vmks[name]++
			}
		}
	}
	return side, ""
}

func (s *clusterSide) cell(name string) string {
	switch {
	case len(s.vms[name]) > 0:
		return glyphOnline + " " + strconv.Itoa(len(s.vms[name]))
	case s.vmks[name] > 0:
		return "vmk " + strconv.Itoa(s.vmks[name])
	}
	return glyphOffline
}

// differenceWords names a network.Difference field for a notes column.
var differenceWords = map[string]string{
	"vlan": "VLAN", "mtu": "MTU", "teaming_policy": "teaming", "active_uplinks": "active uplinks",
	"standby_uplinks": "standby uplinks", "security": "security", "host_coverage": "hosts",
	"contact": "contact", "contact_detail": "contact detail",
}

func (m *Model) vlanPairResult(p vlanPair) vlanPairResult {
	src, why := m.clusterSide(p.source)
	if src == nil {
		return vlanPairResult{why: why}
	}
	tgt, why := m.clusterSide(p.target)
	if tgt == nil {
		return vlanPairResult{why: why}
	}
	matched, srcOnly, _ := network.MatchNetworks(src.nets, tgt.nets)
	var res vlanPairResult
	for _, s := range src.blind {
		res.blind = append(res.blind, p.source.String()+": "+s+" not readable")
	}
	for _, s := range tgt.blind {
		res.blind = append(res.blind, p.target.String()+": "+s+" not readable")
	}
	for _, mt := range matched {
		r := vlanPairRow{vlan: network.ParseVLAN(mt.Source.VLAN), name: mt.Source.Name, src: src.cell(mt.Source.Name), tgt: tgt.cell(mt.Target.Name), status: statusGood, attached: src.vms[mt.Source.Name]}
		var notes []string
		for _, d := range network.Differences(mt) {
			word := differenceWords[d.Field]
			if word == "" {
				word = d.Field
			}
			if d.Field == "security" {
				notes = append(notes, "security differs")
			} else {
				notes = append(notes, word+" "+d.Source+"→"+d.Target)
			}
			if network.IsBlocker(d.Severity) {
				r.status = statusBad
				res.blockers++
			} else {
				if r.status != statusBad {
					r.status = statusWarn
				}
				res.advisories++
			}
		}
		if !strings.EqualFold(mt.Source.Name, mt.Target.Name) {
			notes = append(notes, "named "+mt.Target.Name+" there")
		}
		r.note = strings.Join(notes, " · ")
		res.rows = append(res.rows, r)
	}
	for _, s := range srcOnly {
		vms := src.vms[s.Name]
		r := vlanPairRow{vlan: network.ParseVLAN(s.VLAN), name: s.Name, src: src.cell(s.Name), tgt: glyphFail, missing: true, attached: vms}
		if network.IsBlocker(network.GapSeverity(len(vms))) {
			r.status = statusBad
			r.note = fmt.Sprintf("%s, no network on %s", nounCount(len(vms), "VM"), p.target.context)
			res.blockers++
		} else {
			r.status = statusWarn
			r.note = "unused here, missing there"
			res.advisories++
		}
		res.rows = append(res.rows, r)
	}
	sort.SliceStable(res.rows, func(i, j int) bool {
		a, b := res.rows[i].vlan, res.rows[j].vlan
		if a.String() == b.String() {
			return res.rows[i].name < res.rows[j].name
		}
		if a.Kind == network.VLANTrunk || b.Kind == network.VLANTrunk {
			return a.Kind != network.VLANTrunk
		}
		return vlanLess(a, b)
	})
	return res
}

// ---- keys -----------------------------------------------------------------

func (m *Model) openVLANMap() tea.Cmd {
	if m.vmap == nil {
		m.vmap = &vlanMapView{}
	}
	m.vmap.pick = nil
	m.mode = modeVLANMap
	return m.ensureNetTopology(false)
}

// pairOptions lists the clusters in scope whose wiring has been read.
func (m *Model) pairOptions() ([]clusterRef, map[clusterRef]int) {
	var out []clusterRef
	hosts := map[clusterRef]int{}
	for _, st := range m.inScope() {
		if st.inv == nil || st.cc == nil || m.topoStateOf(st) != topoLoaded {
			continue
		}
		groups := newLiveNet(st.cc.Name, st.inv, st.netTopo.topo).clusterHosts()
		for _, g := range sortedKeys(groups) {
			ref := clusterRef{st.cc.Name, g}
			out = append(out, ref)
			hosts[ref] = len(groups[g])
		}
	}
	return out, hosts
}

func (m *Model) handleVLANMapKey(msg tea.KeyMsg) tea.Cmd {
	if m.vmap == nil {
		m.vmap = &vlanMapView{}
	}
	v := m.vmap
	if v.pick != nil {
		return m.handlePairPickKey(msg)
	}
	n := m.vlanMapLen()
	last := max(0, n-1)
	page := max(1, m.bodyHeight()-6)
	switch {
	case key.Matches(msg, m.keys.Back):
		m.mode = modeBrowse
	case key.Matches(msg, m.keys.Up):
		v.cursor = clamp(v.cursor-1, 0, last)
	case key.Matches(msg, m.keys.Down):
		v.cursor = clamp(v.cursor+1, 0, last)
	case key.Matches(msg, m.keys.PageUp):
		v.cursor = clamp(v.cursor-page, 0, last)
	case key.Matches(msg, m.keys.PageDown):
		v.cursor = clamp(v.cursor+page, 0, last)
	case key.Matches(msg, m.keys.Home):
		v.cursor = 0
	case key.Matches(msg, m.keys.End):
		v.cursor = last
	case key.Matches(msg, m.keys.Open):
		v.where = !v.where
	case key.Matches(msg, m.keys.PairClusters):
		options, hosts := m.pairOptions()
		if len(options) == 0 {
			m.setMessage("no cluster's wiring has been read yet", true)
			return nil
		}
		v.pick = &pairPick{options: options, hosts: hosts}
	case key.Matches(msg, m.keys.ClearPair):
		if m.vpair != nil {
			m.vpair = nil
			v.cursor, v.offset = 0, 0
		}
	case key.Matches(msg, m.keys.Reload):
		return m.ensureNetTopology(true)
	}
	return nil
}

func (m *Model) handlePairPickKey(msg tea.KeyMsg) tea.Cmd {
	v := m.vmap
	p := v.pick
	last := max(0, len(p.options)-1)
	switch {
	case key.Matches(msg, m.keys.Back):
		v.pick = nil
	case key.Matches(msg, m.keys.Up):
		p.cursor = clamp(p.cursor-1, 0, last)
	case key.Matches(msg, m.keys.Down):
		p.cursor = clamp(p.cursor+1, 0, last)
	case key.Matches(msg, m.keys.Home):
		p.cursor = 0
	case key.Matches(msg, m.keys.End):
		p.cursor = last
	case key.Matches(msg, m.keys.Open):
		chosen := p.options[clamp(p.cursor, 0, last)]
		if p.step == 0 {
			p.step, p.source = 1, chosen
			// The usual target is the cluster of the same name at another
			// site, so the cursor starts there when there is one.
			p.cursor = 0
			for i, o := range p.options {
				if o.name == chosen.name && o.context != chosen.context {
					p.cursor = i
					break
				}
			}
			if p.options[p.cursor] == chosen && len(p.options) > 1 {
				p.cursor = (p.cursor + 1) % len(p.options)
			}
			return nil
		}
		if chosen == p.source {
			m.setMessage("pick a different cluster for the target", true)
			return nil
		}
		m.vpair = &vlanPair{source: p.source, target: chosen}
		v.pick, v.cursor, v.offset = nil, 0, 0
	}
	return nil
}

// vlanMapLen is how many rows the map's cursor moves through.
func (m *Model) vlanMapLen() int {
	if m.vpair != nil {
		return len(m.vlanPairResult(*m.vpair).rows)
	}
	e := m.vlanEstate()
	return len(e.rows) + len(e.trunks)
}

// ---- the view -------------------------------------------------------------

func (m *Model) viewVLANMap() []string {
	t := m.theme
	w := m.width
	if m.vmap == nil {
		m.vmap = &vlanMapView{}
	}
	v := m.vmap
	e := m.vlanEstate()
	sub := fmt.Sprintf(" · %s · %s", nounCount(len(e.cols), "vCenter"), nounCount(e.clusterCount(), "cluster"))
	lines := []string{joinEnds("  "+t.title.Render("VLAN map")+t.dim.Render(sub), t.dim.Render(m.vlanMapSource(e)), w)}
	for _, col := range e.cols {
		if st, ok := m.byName[col.context]; ok {
			for _, l := range m.topoProblems(st, w-4) {
				lines = append(lines, "  "+t.dim.Render(col.context+": ")+l)
			}
		}
		// What the account cannot see is said, never left to look like a
		// VLAN that is not there.
		for _, b := range col.blind {
			for _, l := range wrap(glyphCheckWarn+" "+col.context+": "+b, w-4) {
				lines = append(lines, "  "+t.warn.Render(l))
			}
		}
	}
	if v.pick != nil {
		return scrollLines(append(lines, m.pairPickLines(v.pick)...), 0, m.bodyHeight())
	}
	if m.vpair != nil {
		res := m.vlanPairResult(*m.vpair)
		lines = append(lines, joinEnds("  "+t.accent.Render(m.vpair.source.String()+" → "+m.vpair.target.String()), pairVerdict(t, res), w))
		for _, b := range res.blind {
			for _, l := range wrap(glyphCheckWarn+" "+b, w-4) {
				lines = append(lines, "  "+t.warn.Render(l))
			}
		}
		lines = append(lines, "")
		if res.why != "" {
			return scrollLines(append(lines, t.dim.Render("  "+res.why)), 0, m.bodyHeight())
		}
		return m.vlanTable(lines, m.pairTableLines(res, w), m.pairWhere(res, w))
	}
	lines = append(lines, "")
	if len(e.rows)+len(e.trunks) == 0 {
		msg := "no VLANs to show yet"
		if len(e.cols) == 0 {
			msg = "no vCenter in scope has its inventory loaded"
		}
		return scrollLines(append(lines, t.dim.Render("  "+msg)), 0, m.bodyHeight())
	}
	return m.vlanTable(lines, m.estateTableLines(e, w), m.estateWhere(e, w))
}

// vlanMapSource is the read status of the columns: still reading, or the
// time of the oldest read on screen.
func (m *Model) vlanMapSource(e *vlanEstate) string {
	var oldest string
	for _, col := range e.cols {
		st := m.byName[col.context]
		switch col.state {
		case topoLoading:
			return m.spin.View() + "reading wiring…"
		case topoLoaded:
			if s := st.netTopo.asOf.Local().Format("15:04:05"); oldest == "" || s < oldest {
				oldest = s
			}
		}
	}
	if oldest == "" {
		return ""
	}
	return "wiring as of " + oldest
}

// vlanTable lays the table under the head lines, keeping the cursor's row on
// screen above the where panel.
func (m *Model) vlanTable(head []string, table tableLines, where []string) []string {
	v := m.vmap
	t := m.theme
	h := m.bodyHeight() - len(head) - 1
	if v.where && len(where) > 0 {
		where = where[:min(len(where), max(2, h/3))]
		h -= len(where) + 1
	} else {
		where = nil
	}
	h = max(1, h)
	if table.cursor < v.offset {
		v.offset = table.cursor
	}
	if table.cursor >= v.offset+h {
		v.offset = table.cursor - h + 1
	}
	v.offset = clamp(v.offset, 0, max(0, len(table.rows)-h))
	out := append(head, table.header)
	end := min(len(table.rows), v.offset+h)
	out = append(out, table.rows[v.offset:end]...)
	if more := len(table.rows) - (end - v.offset); more > 0 && len(out) < m.bodyHeight() && where == nil {
		out = append(out, t.dim.Render(fmt.Sprintf("  %d more · j/k scrolls", more)))
	}
	if len(where) > 0 {
		out = append(out, "")
		out = append(out, where...)
	}
	return scrollLines(out, 0, m.bodyHeight())
}

// tableLines is a table's header and rows, and the row the cursor is on:
// a label row such as TRUNKS sits between rows without being a cursor stop,
// so the cursor's line is not its index.
type tableLines struct {
	header string
	rows   []string
	cursor int
}

// vlanCellText is a vCenter's cell for a VLAN: VMs, VMkernel adapters, present
// and unused, or not present, with how many of its clusters reach it.
func vlanCellText(c *vlanCell, col vlanColumn) (string, string) {
	switch col.state {
	case topoLoading:
		return "…", ""
	case topoFailed, topoUnsupported:
		return "?", ""
	}
	if !c.present() {
		return "·", ""
	}
	cover := ""
	if len(c.clusters) > 0 {
		cover = fmt.Sprintf("%d/%d", len(c.clusters), len(col.clusters))
	}
	switch {
	case c.vms > 0:
		return glyphOnline + " " + strconv.Itoa(c.vms), cover
	case c.vmks > 0:
		return "vmk " + strconv.Itoa(c.vmks), cover
	}
	return glyphOffline, cover
}

func vlanLabel(v network.VLAN) string {
	switch v.Kind {
	case network.VLANTrunk:
		return strings.TrimPrefix(v.String(), "trunk ")
	}
	return v.String()
}

func (m *Model) estateTableLines(e *vlanEstate, w int) tableLines {
	t := m.theme
	all := e.all()
	vlanW, cellW, coverW := 7, 8, 6
	nameW := 10
	for _, r := range all {
		nameW = max(nameW, ansi.StringWidth(r.name)+1)
		vlanW = max(vlanW, ansi.StringWidth(vlanLabel(r.vlan))+1)
	}
	nameW, vlanW = min(nameW, 21), min(vlanW, 12)
	fixed := func() int { return 2 + vlanW + nameW + len(e.cols)*(cellW+coverW) }
	if fixed()+8 > w {
		coverW = 0
	}
	if fixed()+16 > w {
		nameW = max(10, nameW-(fixed()+16-w))
	}
	colW := cellW + coverW
	head := "  " + pad("VLAN", vlanW, false) + pad("NAME", nameW, false)
	for _, col := range e.cols {
		head += pad(col.context, colW-1, false) + " "
	}
	head += "NOTES"
	out := tableLines{header: m.theme.header.Render(truncate(head, w))}
	for i, r := range all {
		var plain, styled strings.Builder
		vlan := pad(vlanLabel(r.vlan), vlanW, false)
		name := pad(r.name, nameW-1, false) + " "
		plain.WriteString(vlan + name)
		styled.WriteString(t.text.Render(vlan + name))
		for _, col := range e.cols {
			text, cover := vlanCellText(r.cells[col.context], col)
			cell := pad(text, cellW, false)
			if coverW > 0 {
				cell += pad(cover, coverW, false)
			}
			plain.WriteString(cell)
			switch {
			case strings.HasPrefix(text, glyphOnline):
				styled.WriteString(t.accent.Render(pad(text, cellW, false)) + t.dim.Render(cell[len(pad(text, cellW, false)):]))
			case text == "·" || text == glyphOffline || text == "?" || text == "…":
				styled.WriteString(t.dim.Render(cell))
			default:
				styled.WriteString(t.text.Render(cell))
			}
		}
		plain.WriteString(r.note)
		noteStyle := t.dim
		if r.status == statusWarn {
			noteStyle = t.warn
		}
		styled.WriteString(noteStyle.Render(r.note))
		if i == len(e.rows) && len(e.trunks) > 0 {
			// Trunks follow the VLANs they may carry, under a label of their
			// own; the label is not a cursor stop.
			out.rows = append(out.rows, t.header.Render("  TRUNKS"))
		}
		if i == m.vmap.cursor {
			out.cursor = len(out.rows)
			out.rows = append(out.rows, t.focused.Render(truncate(glyphCursor+" "+plain.String(), w)))
		} else {
			out.rows = append(out.rows, truncate("  "+styled.String(), w))
		}
	}
	return out
}

// estateWhere is the where panel: each vCenter's clusters that reach the
// selected VLAN, with the VMs on it in each, and the trunks that carry it.
func (m *Model) estateWhere(e *vlanEstate, w int) []string {
	t := m.theme
	all := e.all()
	if len(all) == 0 {
		return nil
	}
	r := all[clamp(m.vmap.cursor, 0, len(all)-1)]
	title := "VLAN " + r.vlan.String()
	switch r.vlan.Kind {
	case network.VLANNone:
		title = "Untagged"
	case network.VLANTrunk:
		title = "Trunk " + vlanLabel(r.vlan)
	}
	if len(r.names) > 0 {
		title += " · " + strings.Join(r.names, ", ")
	}
	out := []string{t.header.Render(truncate("  "+title, w))}
	for _, col := range e.cols {
		c := r.cells[col.context]
		if !c.present() {
			continue
		}
		var parts []string
		for _, g := range col.clusters {
			switch {
			case c.clusterVMs[g] > 0:
				parts = append(parts, fmt.Sprintf("%s %s", g, nounCount(c.clusterVMs[g], "VM")))
			case c.clusters[g]:
				parts = append(parts, g)
			}
		}
		text := "no cluster reaches it"
		if len(parts) > 0 {
			text = strings.Join(parts, " · ")
		}
		if c.vmks > 0 {
			text += " · " + nounCount(c.vmks, "VMkernel adapter")
		}
		for i, l := range wrap(col.context+": "+text, w-6) {
			prefix := "  "
			if i > 0 {
				prefix = "      "
			}
			out = append(out, prefix+l)
		}
	}
	if r.vlan.Kind == network.VLANID {
		for _, tr := range e.trunks {
			if !tr.vlan.Carries(r.vlan.ID) {
				continue
			}
			for _, col := range e.cols {
				if c := tr.cells[col.context]; c.present() {
					text := fmt.Sprintf("also carried by %s (%s) on %s: %s", tr.vlan, strings.Join(tr.names, ", "), col.context, strings.Join(sortedKeys(c.clusters), ", "))
					for i, l := range wrap(text, w-6) {
						prefix := "  "
						if i > 0 {
							prefix = "      "
						}
						out = append(out, t.dim.Render(prefix+l))
					}
				}
			}
		}
	}
	return out
}

func pairVerdict(t theme, res vlanPairResult) string {
	if res.why != "" {
		return ""
	}
	if len(res.blind) > 0 {
		// Missing access never improves a verdict: what could not be read
		// may be exactly what is missing.
		if res.blockers == 0 {
			return t.warn.Render("? incomplete")
		}
		return t.warn.Render("? incomplete") + " " + t.bad.Render(fmt.Sprintf("%s %s", glyphFail, nounCount(res.blockers, "blocker")))
	}
	switch {
	case res.blockers > 0 && res.advisories > 0:
		return t.bad.Render(fmt.Sprintf("%s %s", glyphFail, nounCount(res.blockers, "blocker"))) + " " + t.warn.Render(fmt.Sprintf("%s %d", glyphCheckWarn, res.advisories))
	case res.blockers > 0:
		return t.bad.Render(glyphFail + " " + nounCount(res.blockers, "blocker"))
	case res.advisories > 0:
		return t.warn.Render(glyphCheckWarn + " " + nounCount(res.advisories, "advisory"))
	}
	return t.ok.Render(glyphCheckOK + " ready")
}

func (m *Model) pairTableLines(res vlanPairResult, w int) tableLines {
	t := m.theme
	vlanW, nameW, cellW := 7, 10, 10
	for _, r := range res.rows {
		nameW = max(nameW, ansi.StringWidth(r.name)+1)
		vlanW = max(vlanW, ansi.StringWidth(vlanLabel(r.vlan))+1)
	}
	nameW, vlanW = min(nameW, 21), min(vlanW, 12)
	// The notes say what is wrong, so they keep room before the name does.
	if w < 80 {
		cellW = 8
	}
	if 2+vlanW+nameW+2*cellW+24 > w {
		nameW = max(10, w-(2+vlanW+2*cellW+24))
	}
	head := "  " + pad("VLAN", vlanW, false) + pad("NAME", nameW, false) + pad("SOURCE", cellW, false) + pad("TARGET", cellW, false) + "NOTES"
	out := tableLines{header: t.header.Render(truncate(head, w))}
	if len(res.rows) == 0 {
		out.rows = append(out.rows, t.dim.Render("  the source cluster reaches no networks"))
		return out
	}
	for i, r := range res.rows {
		lead := pad(vlanLabel(r.vlan), vlanW, false) + pad(r.name, nameW-1, false) + " "
		src, tgt := pad(r.src, cellW, false), pad(r.tgt, cellW, false)
		if i == m.vmap.cursor {
			out.cursor = len(out.rows)
			out.rows = append(out.rows, t.focused.Render(truncate(glyphCursor+" "+lead+src+tgt+r.note, w)))
			continue
		}
		noteStyle := t.dim
		switch r.status {
		case statusBad:
			noteStyle = t.bad
		case statusWarn:
			noteStyle = t.warn
		}
		tgtStyle := t.text
		if r.missing {
			tgtStyle = noteStyle
		}
		out.rows = append(out.rows, truncate("  "+t.text.Render(lead+src)+tgtStyle.Render(tgt)+noteStyle.Render(r.note), w))
	}
	return out
}

// pairWhere lists the source cluster's VMs on the selected network.
func (m *Model) pairWhere(res vlanPairResult, w int) []string {
	t := m.theme
	if len(res.rows) == 0 {
		return nil
	}
	r := res.rows[clamp(m.vmap.cursor, 0, len(res.rows)-1)]
	out := []string{t.header.Render(truncate("  "+r.name+" · VLAN "+r.vlan.String(), w))}
	if len(r.attached) == 0 {
		return append(out, t.dim.Render("  no VMs in the source cluster use it"))
	}
	names := append([]string(nil), r.attached...)
	sort.Strings(names)
	for _, l := range wrap(nounCount(len(names), "VM")+" in "+m.vpair.source.String()+": "+strings.Join(names, ", "), w-4) {
		out = append(out, "  "+l)
	}
	return out
}

func (m *Model) pairPickLines(p *pairPick) []string {
	t := m.theme
	w := m.width
	prompt := "Pick the source cluster: the one whose VMs would move"
	if p.step == 1 {
		prompt = "Pick the target cluster for " + p.source.String()
	}
	out := []string{"", "  " + t.accent.Render(prompt), ""}
	ctxW, nameW := 8, 10
	for _, o := range p.options {
		ctxW = max(ctxW, ansi.StringWidth(o.context)+2)
		nameW = max(nameW, ansi.StringWidth(o.name)+2)
	}
	h := max(1, m.bodyHeight()-len(out)-2)
	start := 0
	if p.cursor >= h {
		start = p.cursor - h + 1
	}
	for i := start; i < min(len(p.options), start+h); i++ {
		o := p.options[i]
		line := pad(o.context, ctxW, false) + pad(o.name, nameW, false) + nounCount(p.hosts[o], "host")
		switch {
		case i == p.cursor:
			out = append(out, t.focused.Render(truncate(glyphCursor+" "+line, w)))
		case p.step == 1 && o == p.source:
			out = append(out, t.dim.Render(truncate("  "+line+"  (source)", w)))
		default:
			out = append(out, truncate("  "+line, w))
		}
	}
	return out
}
