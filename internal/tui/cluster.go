package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/easonliuuuuu/vsfleet/internal/humanize"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// A cluster's detail pane has three pages, chosen with 0–2 the way a switch
// workspace's are. Summary is the field list, grouped in the order an
// operator triages: is anything wrong, would it survive a host failure, why
// are fewer hosts effective than present, is there room, is DRS acting, what
// lives here. Hosts & VMs is the cluster's own inventory tree and Storage
// which hosts mount which datastore. Everything on all three comes from the
// inventory already loaded; nothing here asks the vCenter for more.
var clusterPages = []string{"Summary", "Hosts & VMs", "Storage"}

const clusterBarWidth = 8

// clusterView is the state of an open cluster pane beyond the field cursor
// every detail pane shares. key is the row it belongs to: moving to another
// cluster keeps the page but starts its cursor, scroll and folds afresh.
type clusterView struct {
	key    string
	page   int
	cursor int
	scroll int
	open   map[string]bool
}

// clusterMembers is what the inventory knows about one cluster's contents.
type clusterMembers struct {
	hosts []vsphere.Host
	vms   map[string][]vsphere.VM
}

// membersOf finds a cluster's hosts and the VMs running on them. A standalone
// host is its own compute resource, named after it and with no cluster.
func membersOf(c vsphere.Cluster, inv *vsphere.Inventory) clusterMembers {
	out := clusterMembers{vms: map[string][]vsphere.VM{}}
	if inv == nil {
		return out
	}
	names := map[string]bool{}
	for _, h := range inv.Hosts {
		if (c.Standalone && h.Cluster == "" && h.Name == c.Name) || (!c.Standalone && h.Cluster == c.Name) {
			out.hosts = append(out.hosts, h)
			names[h.Name] = true
		}
	}
	for _, vm := range inv.VMs {
		if names[vm.Host] {
			out.vms[vm.Host] = append(out.vms[vm.Host], vm)
		}
	}
	return out
}

// mountsRead reports whether any host carries its datastore mounts. Older
// captures and imported RVTools workbooks have none, which must not read as
// "mounted nowhere".
func mountsRead(hosts []vsphere.Host) bool {
	for _, h := range hosts {
		if len(h.Datastores) > 0 {
			return true
		}
	}
	return false
}

// dsCoverage is one datastore as the hosts of one cluster see it.
type dsCoverage struct {
	name    string
	mounted []string
	missing []string
}

// gap reports a datastore most hosts share but some do not: the case that
// breaks HA restart and vMotion onto those hosts. A datastore on exactly one
// host of several is that host's local disk, not a gap.
func (d dsCoverage) gap() bool { return len(d.missing) > 0 && len(d.mounted) > 1 }

func (d dsCoverage) local() bool { return len(d.mounted) == 1 && len(d.missing) > 0 }

// coverage lists every datastore any of hosts mounts, gaps first.
func coverage(hosts []vsphere.Host) []dsCoverage {
	byName := map[string]*dsCoverage{}
	for _, h := range hosts {
		for _, ds := range h.Datastores {
			if byName[ds] == nil {
				byName[ds] = &dsCoverage{name: ds}
			}
		}
	}
	for _, d := range byName {
		for _, h := range hosts {
			if containsString(h.Datastores, d.name) {
				d.mounted = append(d.mounted, h.Name)
			} else {
				d.missing = append(d.missing, h.Name)
			}
		}
	}
	out := make([]dsCoverage, 0, len(byName))
	for _, d := range byName {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].gap() != out[j].gap() {
			return out[i].gap()
		}
		return out[i].name < out[j].name
	})
	return out
}

// alarmSummary counts a cluster's triggered alarms by severity and names the
// worst one, with where it fired when that is not the cluster itself.
func alarmSummary(c vsphere.Cluster) string {
	var critical, warning int
	for _, a := range c.Alarms {
		if a.Status == "red" {
			critical++
		} else {
			warning++
		}
	}
	var parts []string
	if critical > 0 {
		parts = append(parts, fmt.Sprintf("%d critical", critical))
	}
	if warning > 0 {
		parts = append(parts, fmt.Sprintf("%d warning", warning))
	}
	worst := c.Alarms[0]
	name := worst.Name
	if worst.Entity != "" && worst.Entity != c.Name {
		name += " on " + worst.Entity
	}
	return strings.Join(append(parts, name), " · ")
}

// hostNameList names up to three hosts and counts the rest.
func hostNameList(names []string) string {
	if len(names) <= 3 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:3], ", ") + fmt.Sprintf(" +%d more", len(names)-3)
}

// clusterRead reports whether the cluster was read with the health and
// configuration fields. Without them (an older capture, an RVTools import)
// an empty EVC mode or issue list is missing evidence, not "off" or "none".
func clusterRead(c vsphere.Cluster) bool { return c.OverallStatus != "" }

func clusterRow(c vsphere.Cluster, inv *vsphere.Inventory, withContext bool) row {
	st, glyph := statusGood, glyphOnline
	switch {
	case c.Hosts == 0:
		st, glyph = statusWarn, glyphOffline
	case c.OverallStatus == "red":
		st, glyph = statusBad, glyphFail
	case c.OverallStatus == "yellow":
		st, glyph = statusWarn, glyphPending
	}
	kind := "Cluster"
	if c.Standalone {
		kind = "Standalone host"
	}
	members := membersOf(c, inv)
	fields, marks := clusterFields(c, members)
	fields = append(fields,
		heading(""),
		heading("Identity"),
		field{"vCenter", c.Context},
		field{"Kind", kind},
		field{"Datacenter", humanize.Dash(c.Datacenter)},
		field{"Inventory path", humanize.Dash(c.Path)},
		field{"Managed object", c.ID},
	)
	cl := c
	return row{
		key:     c.Context + "/" + c.ID,
		context: c.Context,
		where:   c.Location,
		name:    c.Name,
		glyph:   glyph,
		status:  st,
		cells: lead(withContext, c.Context,
			c.Name,
			strconv.Itoa(c.Hosts),
			strconv.FormatInt(int64(c.CPUCores), 10),
			humanize.MB(c.TotalMemoryMB),
			yesNo(c.DRSEnabled),
			yesNo(c.HAEnabled),
			humanize.Dash(c.Datacenter),
		),
		detail:  fields,
		marks:   marks,
		target:  actionTarget{moref: c.ID, morefKind: clusterMorefKind(c.Standalone), path: c.Path},
		cluster: &cl,
	}
}

// clusterFields is the Summary page, section by section, with the marks the
// fields that can be judged carry.
func clusterFields(c vsphere.Cluster, mem clusterMembers) ([]field, map[string]fieldMark) {
	marks := map[string]fieldMark{}
	read := clusterRead(c)
	var out []field
	add := func(label, value string) { out = append(out, field{label: label, value: value}) }
	section := func(label string) {
		if len(out) > 0 {
			out = append(out, heading(""))
		}
		out = append(out, heading(label))
	}

	section("Health (live)")
	add("Status", humanize.Dash(c.OverallStatus))
	switch c.OverallStatus {
	case "green":
		marks["Status"] = fieldMark{statusGood, ""}
	case "yellow":
		marks["Status"] = fieldMark{statusWarn, ""}
	case "red":
		marks["Status"] = fieldMark{statusBad, ""}
	}
	switch {
	case !read:
		add("Config issues", "-")
	case len(c.ConfigIssues) == 0:
		add("Config issues", "none")
	default:
		v := strconv.Itoa(len(c.ConfigIssues)) + " · " + c.ConfigIssues[0]
		if n := len(c.ConfigIssues) - 1; n > 0 {
			v += fmt.Sprintf(" (+%d more)", n)
		}
		add("Config issues", v)
		marks["Config issues"] = fieldMark{statusBad, ""}
	}
	switch {
	case len(c.Alarms) == 0 && !c.AlarmsRead:
		add("Alarms", "-")
	case len(c.Alarms) == 0:
		add("Alarms", "none")
	default:
		v := alarmSummary(c)
		if !c.AlarmsRead {
			v += " · some hosts not visible"
		}
		add("Alarms", v)
		marks["Alarms"] = fieldMark{statusWarn, ""}
		if c.Alarms[0].Status == "red" {
			marks["Alarms"] = fieldMark{statusBad, ""}
		}
	}

	if !c.Standalone {
		section("Resilience")
		haFields(c, add, marks)
	}

	section("Hosts")
	add("Hosts", fmt.Sprintf("%d total · %d effective", c.Hosts, c.EffectiveHost))
	if len(mem.hosts) > 0 {
		var connected, maint, down int
		for _, h := range mem.hosts {
			switch {
			case h.InMaintenance:
				maint++
			case h.ConnectionState == "connected":
				connected++
			default:
				down++
			}
		}
		states := []string{fmt.Sprintf("%d connected", connected)}
		if maint > 0 {
			states = append(states, fmt.Sprintf("%d maintenance", maint))
		}
		if down > 0 {
			states = append(states, fmt.Sprintf("%d not responding", down))
		}
		add("Host states", strings.Join(states, " · "))
		switch {
		case down > 0:
			marks["Host states"] = fieldMark{statusBad, ""}
		case maint > 0:
			marks["Host states"] = fieldMark{statusWarn, ""}
		default:
			marks["Host states"] = fieldMark{statusGood, ""}
		}
	} else {
		add("Host states", "-")
	}
	if !c.Standalone {
		switch {
		case c.EVCMode != "":
			add("EVC mode", c.EVCMode)
		case read:
			add("EVC mode", "off")
		default:
			add("EVC mode", "-")
		}
	}

	section("Capacity")
	var cpuUsed, memUsed int64
	for _, h := range mem.hosts {
		cpuUsed += h.CPUUsageMHz
		memUsed += h.MemoryUsageMB
	}
	add("CPU", usedOfTotal(cpuUsed, c.TotalCPUMHz, humanize.MHz))
	add("Memory", usedOfTotal(memUsed, c.TotalMemoryMB, humanize.MB))
	effective("Effective CPU", c.EffectiveCPUMHz, humanize.MHz, add)
	effective("Effective memory", c.EffectiveMemoryMB, humanize.MB, add)
	add("CPU cores", strconv.FormatInt(int64(c.CPUCores), 10))

	if !c.Standalone {
		section("DRS")
		switch {
		case !c.DRSEnabled:
			add("DRS", "off")
		case c.DRSBehavior != "":
			add("DRS", "on · "+drsBehaviorWords(c.DRSBehavior))
			if c.DRSBehavior == "manual" {
				marks["DRS"] = fieldMark{statusWarn, "nothing moves on its own"}
			}
		default:
			add("DRS", "on")
		}
		if c.DRSEnabled && c.DRSScore > 0 {
			add("DRS score", fmt.Sprintf("%d%%", c.DRSScore))
		}
	}

	section("Contents")
	if len(mem.hosts) > 0 {
		var on, total int
		for _, vms := range mem.vms {
			for _, vm := range vms {
				total++
				if vm.PowerState == "poweredOn" {
					on++
				}
			}
		}
		add("VMs", fmt.Sprintf("%d · %d on · %d off", total, on, total-on))
	} else {
		add("VMs", "-")
	}
	if mountsRead(mem.hosts) {
		cov := coverage(mem.hosts)
		gaps := 0
		for _, d := range cov {
			if d.gap() {
				gaps++
			}
		}
		v := strconv.Itoa(len(cov))
		if gaps > 0 {
			v += " · " + countWord(gaps, "not on every host", "not on every host")
			marks["Datastores"] = fieldMark{statusWarn, ""}
		}
		add("Datastores", v)
	} else {
		add("Datastores", "-")
	}
	return out, marks
}

// haFields adds the Resilience section: whether HA is on, whether the
// cluster can still absorb the failures it is configured for, and how
// admission control protects that.
func haFields(c vsphere.Cluster, add func(string, string), marks map[string]fieldMark) {
	if !c.HAEnabled {
		add("HA", "off")
		if clusterRead(c) && c.Hosts > 1 {
			marks["HA"] = fieldMark{statusWarn, "VMs do not restart after a host failure"}
		}
		return
	}
	ha := c.HA
	if ha == nil {
		add("HA", "on")
		return
	}
	add("HA", "on")
	if ha.HostMonitoring != "" {
		add("Host monitoring", onWord(ha.HostMonitoring == "enabled"))
		if ha.HostMonitoring != "enabled" {
			marks["Host monitoring"] = fieldMark{statusWarn, "host failures go unnoticed"}
		}
	}
	if ha.VMMonitoring != "" {
		add("VM monitoring", onWord(ha.VMMonitoring != "vmMonitoringDisabled"))
	}

	switch ha.Policy {
	case vsphere.HAPolicyHostFailures:
		if ha.FailoverLevel > 0 {
			add("HA failover", fmt.Sprintf("tolerates %s (configured %d)", countWord(int(ha.CurrentFailoverLevel), "host failure", "host failures"), ha.FailoverLevel))
			if ha.CurrentFailoverLevel < ha.FailoverLevel {
				marks["HA failover"] = fieldMark{statusBad, ""}
			} else {
				marks["HA failover"] = fieldMark{statusGood, ""}
			}
		}
	case vsphere.HAPolicyResources:
		if ha.CPUFailoverPct > 0 || ha.MemFailoverPct > 0 {
			add("HA failover", fmt.Sprintf("CPU %d%% · memory %d%% free for failover", ha.CPUFailoverPct, ha.MemFailoverPct))
			if ha.CPUFailoverPct < ha.CPUReservePct || ha.MemFailoverPct < ha.MemReservePct {
				marks["HA failover"] = fieldMark{statusBad, "below the reserve"}
			} else {
				marks["HA failover"] = fieldMark{statusGood, ""}
			}
		}
	}

	var ac string
	switch ha.Policy {
	case vsphere.HAPolicyHostFailures:
		ac = "host failures to tolerate · " + strconv.Itoa(int(ha.FailoverLevel))
	case vsphere.HAPolicyResources:
		ac = fmt.Sprintf("reserve CPU %d%% · memory %d%%", ha.CPUReservePct, ha.MemReservePct)
	case vsphere.HAPolicyFailoverHosts:
		ac = "dedicated failover hosts · " + humanize.Dash(hostNameList(ha.FailoverHosts))
	}
	if !ha.AdmissionControl {
		marks["Admission control"] = fieldMark{statusWarn, "power-ons can use failover capacity"}
		ac = "off"
	}
	if ac != "" {
		add("Admission control", ac)
	}
}

func onWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func drsBehaviorWords(b string) string {
	switch b {
	case "fullyAutomated":
		return "fully automated"
	case "partiallyAutomated":
		return "partially automated"
	}
	return b
}

// usedOfTotal is "38.1GHz used of 112.0GHz (34%)", or the total alone when
// no host reported its use.
func usedOfTotal(used, total int64, unit func(int64) string) string {
	if total <= 0 {
		return "-"
	}
	if used <= 0 {
		return unit(total) + " total"
	}
	return fmt.Sprintf("%s used of %s (%.0f%%)", unit(used), unit(total), float64(used)/float64(total)*100)
}

// effective adds the capacity available to VMs after unavailable hosts and
// the hypervisor's share are taken out.
func effective(label string, capacity int64, unit func(int64) string, add func(string, string)) {
	if capacity <= 0 {
		return
	}
	add(label, unit(capacity))
}

// datastoreHosts is a datastore's Hosts field: how many of each cluster's
// hosts mount it, naming the ones that do not when the rest do.
func datastoreHosts(d vsphere.Datastore, inv *vsphere.Inventory) (string, fieldMark, bool) {
	if inv == nil || !mountsRead(inv.Hosts) {
		return "-", fieldMark{}, false
	}
	groups := map[string][]vsphere.Host{}
	var order []string
	for _, h := range inv.Hosts {
		g := h.Cluster
		if g == "" {
			g = h.Name
		}
		if groups[g] == nil {
			order = append(order, g)
		}
		groups[g] = append(groups[g], h)
	}
	sort.Strings(order)
	var parts []string
	gap := false
	for _, g := range order {
		for _, c := range coverage(groups[g]) {
			if c.name != d.Name {
				continue
			}
			total := len(c.mounted) + len(c.missing)
			p := fmt.Sprintf("%d of %d in %s", len(c.mounted), total, g)
			if total == 1 {
				p = g
			}
			if c.gap() {
				p += " · not on " + hostNameList(c.missing)
				gap = true
			}
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "none", fieldMark{statusWarn, "no host mounts it"}, true
	}
	if gap {
		return strings.Join(parts, "; "), fieldMark{statusWarn, ""}, true
	}
	return strings.Join(parts, "; "), fieldMark{}, false
}

// ---- the pane -------------------------------------------------------------

// clusterState returns the pane state for r, starting it afresh when the
// pane has moved to another cluster. The page survives the move, so two
// clusters' Storage pages can be compared with ← and →.
func (m *Model) clusterState(r row) *clusterView {
	if m.cl == nil {
		m.cl = &clusterView{}
	}
	if m.cl.key != r.key {
		m.cl = &clusterView{key: r.key, page: m.cl.page}
	}
	return m.cl
}

// handleClusterKey takes the keys the cluster pages give their own meaning.
// It reports false for anything else, which the detail pane then handles as
// for every other kind.
func (m *Model) handleClusterKey(msg tea.KeyMsg, r row) (tea.Cmd, bool) {
	cv := m.clusterState(r)
	if key.Matches(msg, m.keys.ClusterPage) {
		if i, err := strconv.Atoi(msg.String()); err == nil && i >= 0 && i < len(clusterPages) {
			cv.page, cv.scroll = i, 0
		}
		return nil, true
	}
	if cv.page == 0 {
		return nil, false
	}
	hosts := membersOf(*r.cluster, m.inventoryOf(r.context)).hosts
	last := max(0, len(hosts)-1)
	page := max(1, m.bodyHeight()-4)
	tree := cv.page == 1
	switch {
	case key.Matches(msg, m.keys.Up):
		if tree {
			cv.cursor = clamp(cv.cursor-1, 0, last)
		} else {
			cv.scroll = max(0, cv.scroll-1)
		}
	case key.Matches(msg, m.keys.Down):
		if tree {
			cv.cursor = clamp(cv.cursor+1, 0, last)
		} else {
			cv.scroll++
		}
	case key.Matches(msg, m.keys.PageUp):
		if tree {
			cv.cursor = clamp(cv.cursor-page, 0, last)
		} else {
			cv.scroll = max(0, cv.scroll-page)
		}
	case key.Matches(msg, m.keys.PageDown):
		if tree {
			cv.cursor = clamp(cv.cursor+page, 0, last)
		} else {
			cv.scroll += page
		}
	case key.Matches(msg, m.keys.Home):
		cv.cursor, cv.scroll = 0, 0
	case key.Matches(msg, m.keys.End):
		cv.cursor, cv.scroll = last, 1<<30
	case tree && key.Matches(msg, m.keys.Fold):
		if len(hosts) > 0 {
			name := hosts[clamp(cv.cursor, 0, last)].Name
			if cv.open == nil {
				cv.open = map[string]bool{}
			}
			cv.open[name] = !cv.open[name]
		}
	case tree && key.Matches(msg, m.keys.Open):
		if len(hosts) == 0 {
			return nil, true
		}
		return m.runAction(jumpToNamed("Show this host", vsphere.KindHost, hosts[clamp(cv.cursor, 0, last)].Name)), true
	case key.Matches(msg, m.keys.Open):
		// Storage has no cursor; enter there does nothing rather than
		// opening field actions for a field that is not on screen.
		return nil, true
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) inventoryOf(context string) *vsphere.Inventory {
	if st, ok := m.byName[context]; ok {
		return st.inv
	}
	return nil
}

// clusterPageTabs is the page bar shown at the right end of the pane's
// header line.
func (m *Model) clusterPageTabs(page int) string { return m.pageTabs(clusterPages, page) }

// viewClusterPage draws the Hosts & VMs or Storage page under the pane's
// header line.
func (m *Model) viewClusterPage(r row, cv *clusterView) []string {
	// The field cursor belongs to Summary; here the header is drawn plain so
	// the only highlighted line is the host the page's own cursor is on.
	t := m.theme
	title := "  " + t.title.Render(r.name) + t.dim.Render("   "+kindLabel(r.kind)+" · "+r.context)
	lines := []string{joinEnds(title, m.clusterPageTabs(cv.page), m.width), ""}
	head := len(lines)
	mem := membersOf(*r.cluster, m.inventoryOf(r.context))
	var body []string
	cursorLine := -1
	if cv.page == 1 {
		body, cursorLine = m.clusterTreeLines(mem, cv)
	} else {
		body = m.clusterStorageLines(mem)
	}
	h := max(1, m.bodyHeight()-head)
	limit := max(0, len(body)-h)
	if cursorLine >= 0 {
		if cursorLine < cv.scroll {
			cv.scroll = cursorLine
		}
		if cursorLine >= cv.scroll+h {
			cv.scroll = cursorLine - h + 1
		}
	}
	cv.scroll = clamp(cv.scroll, 0, limit)
	return scrollLines(append(lines, body[cv.scroll:]...), 0, m.bodyHeight())
}

// clusterTreeLines draws the hosts, each foldable to the VMs it runs, and
// says which line the host cursor is on.
func (m *Model) clusterTreeLines(mem clusterMembers, cv *clusterView) ([]string, int) {
	t := m.theme
	if len(mem.hosts) == 0 {
		return []string{t.dim.Render("  No hosts of this cluster are in the loaded inventory.")}, -1
	}
	cv.cursor = clamp(cv.cursor, 0, len(mem.hosts)-1)
	nameW := clamp(m.width-58, 14, 32)
	head := "  " + pad("HOST / VM", nameW+4, false) + pad("STATE", 16, false) + pad("CPU", clusterBarWidth+7, false) + pad("MEM", clusterBarWidth+7, false) + "VMS"
	out := []string{t.header.Render(truncate(head, m.width))}
	cursorLine := -1
	for i, h := range mem.hosts {
		fold := "▸ "
		if cv.open[h.Name] {
			fold = "▾ "
		}
		state, st := hostStateWord(h)
		vms := mem.vms[h.Name]
		line := fold + pad(h.Name, nameW+2, false) +
			t.statusStyle(st).Render(pad(state, 16, false)) +
			pad(capacityBar(h.CPUUsageMHz, h.TotalCPU(), clusterBarWidth), clusterBarWidth+7, false) +
			pad(capacityBar(h.MemoryUsageMB, h.MemoryMB, clusterBarWidth), clusterBarWidth+7, false) +
			pad(strconv.Itoa(len(vms)), 4, true)
		if i == cv.cursor {
			cursorLine = len(out)
			out = append(out, t.focused.Render(truncate("▸ "+fold+pad(h.Name, nameW+2, false)+pad(state, 16, false)+
				pad(capacityBar(h.CPUUsageMHz, h.TotalCPU(), clusterBarWidth), clusterBarWidth+7, false)+
				pad(capacityBar(h.MemoryUsageMB, h.MemoryMB, clusterBarWidth), clusterBarWidth+7, false)+
				pad(strconv.Itoa(len(vms)), 4, true), m.width)))
		} else {
			out = append(out, truncate("  "+line, m.width))
		}
		if !cv.open[h.Name] {
			continue
		}
		if len(vms) == 0 {
			out = append(out, t.dim.Render("      no VMs"))
			continue
		}
		for _, vm := range vms {
			glyph, st := glyphOffline, statusNone
			if vm.PowerState == "poweredOn" {
				glyph, st = glyphOnline, statusGood
			}
			size := fmt.Sprintf("%d vCPU · %s", vm.CPU, humanize.MB(vm.MemoryMB))
			out = append(out, truncate("      "+t.statusStyle(st).Render(glyph)+" "+pad(vm.Name, nameW, false)+t.dim.Render(size), m.width))
		}
	}
	return out, cursorLine
}

func hostStateWord(h vsphere.Host) (string, rowStatus) {
	switch {
	case h.InMaintenance:
		return glyphPending + " maintenance", statusWarn
	case h.ConnectionState == "connected":
		return glyphOnline + " connected", statusGood
	case h.ConnectionState == "":
		return "-", statusNone
	}
	return glyphFail + " " + h.ConnectionState, statusBad
}

// clusterStorageLines lists the datastores the cluster's hosts mount, the
// ones some hosts lack first, with free space from the datastore inventory.
func (m *Model) clusterStorageLines(mem clusterMembers) []string {
	t := m.theme
	if !mountsRead(mem.hosts) {
		return []string{t.dim.Render("  Host datastore mounts were not read for this vCenter.")}
	}
	free := map[string]vsphere.Datastore{}
	if inv := m.inventoryOf(mem.hosts[0].Context); inv != nil {
		for _, d := range inv.Datastores {
			free[d.Name] = d
		}
	}
	cov := coverage(mem.hosts)
	gaps := 0
	for _, d := range cov {
		if d.gap() {
			gaps++
		}
	}
	var out []string
	if gaps > 0 {
		them := "them"
		if gaps == 1 {
			them = "it"
		}
		msg := fmt.Sprintf("%s %s not mounted on every host: VMs on %s cannot restart or run on the hosts named.",
			glyphCheckWarn, countWord(gaps, "datastore", "datastores"), them)
		for _, l := range wrap(msg, m.width-4) {
			out = append(out, t.warn.Render("  "+l))
		}
		out = append(out, "")
	} else {
		out = append(out, t.ok.Render("  "+glyphCheckOK+" every shared datastore is mounted on every host"), "")
	}
	nameW := clamp(m.width-50, 16, 36)
	out = append(out, t.header.Render(truncate("  "+pad("DATASTORE", nameW+2, false)+pad("HOSTS", 9, false)+pad("FREE", 7, false)+"NOTES", m.width)))
	for _, d := range cov {
		total := len(d.mounted) + len(d.missing)
		freeCell := "-"
		if ds, ok := free[d.name]; ok && ds.CapacityBytes > 0 {
			freeCell = fmt.Sprintf("%.0f%%", 100-ds.UsedPercent())
		}
		line := "  " + pad(d.name, nameW+2, false) + pad(fmt.Sprintf("%d/%d", len(d.mounted), total), 9, false) + pad(freeCell, 7, false)
		switch {
		case d.gap():
			out = append(out, truncate(t.warn.Render(line+glyphCheckWarn+" not on "+hostNameList(d.missing)), m.width))
		case d.local():
			out = append(out, truncate(t.dim.Render(line+"only on "+d.mounted[0]), m.width))
		default:
			out = append(out, truncate(line, m.width))
		}
	}
	return out
}
