package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/easonliuuuuu/vsfleet/internal/humanize"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// rowStatus drives the colour and glyph of a row, and nothing else. Keeping it
// separate from the domain objects means the UI decides what "worrying" looks
// like without the inventory layer growing opinions about presentation.
type rowStatus int

const (
	statusNone rowStatus = iota
	statusIdle
	statusGood
	statusWarn
	statusBad
)

// sortMode orders the resource table. It is deliberately just two settings:
// the grouped, alphabetical order that rowsFor already produces needs no
// further sorting to earn its name, and the one thing worth reordering for
// is surfacing trouble — a suspended VM or a host in maintenance anywhere in
// the scope, not only the one at the top of its own vCenter's list.
type sortMode int

const (
	sortByName sortMode = iota
	sortByStatus
)

func (s sortMode) label() string {
	if s == sortByStatus {
		return "status"
	}
	return "name"
}

func (s sortMode) next() sortMode {
	if s == sortByStatus {
		return sortByName
	}
	return sortByStatus
}

// apply reorders rows in place. Name order is left exactly as rowsFor built
// it — grouped by context, alphabetical within each — because that grouping
// is itself information once more than one vCenter is in scope. Status order
// uses a stable sort, so within a status the name grouping survives as the
// tiebreaker.
func (s sortMode) apply(rows []row) {
	if s != sortByStatus {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return statusRank(rows[i].status) < statusRank(rows[j].status)
	})
}

// statusRank orders the worst news first: a failure before a warning before
// a healthy row before one with nothing to report.
func statusRank(s rowStatus) int {
	switch s {
	case statusBad:
		return 0
	case statusWarn:
		return 1
	case statusGood:
		return 2
	case statusIdle:
		return 3
	default:
		return 4
	}
}

// column describes one table column. A width of zero means the column absorbs
// whatever space the fixed ones leave over; exactly one column per kind is
// flexible, and it is always the name.
type column struct {
	title string
	width int
	right bool
	// keepTail truncates an over-long value from the left instead of the
	// right, so the part that identifies the object (the end of an inventory
	// path) stays visible.
	keepTail bool
	// fit stops the flexible column at its widest value instead of absorbing
	// all the slack, so the column after it stays next to it. The slack is
	// left at the end of the line.
	fit bool
}

// field is one label/value pair in a detail pane.
type field struct {
	label string
	value string
}

// sectionValue is the value that makes a field a section heading rather than
// a value: drawn in the header style, never focused, never acted on. A
// heading with no label is the blank line between sections. It is a value
// no vCenter string can hold, so every kind keeps its two-element literals.
const sectionValue = "\x00section"

func heading(label string) field { return field{label: label, value: sectionValue} }

func (f field) heading() bool { return f.value == sectionValue }

// actionTarget is a row's own structured identity — the values a handoff
// action needs, as opposed to their rendered form in row.detail. row.detail
// exists to be read; actionTarget exists to be acted on, which is why the
// guest IP and the managed object reference live here even though they are
// already, separately, formatted into detail fields for display.
type actionTarget struct {
	// moref is the bare managed object reference value, e.g. "vm-1234" — the
	// same string rendered as the "Managed object" detail field.
	moref string
	// morefKind is the vSphere managed object type a deep link needs
	// ("VirtualMachine", "HostSystem", ...). It is derived from row.kind by
	// the constructor below rather than stored per call site, so it can
	// never drift out of step with it.
	morefKind string
	// address is what an SSH action connects to: the VM's guest IP, or the
	// ESXi host's registered name (there is no management IP in Host — see
	// hostRow). Empty means SSH has nowhere to reach.
	address string
	// hostName is the DNS-style name the guest reports for itself, for the
	// one kind that has one. SSH offers it beside address; see sshTargets.
	hostName string
	// path is a datastore-style path ("[datastore1]") for the one kind that
	// has one; empty otherwise.
	path string
	// inaccessible marks a datastore no host can currently reach. It is
	// carried here so the file browser can refuse with that reason rather
	// than opening onto a directory that would look merely empty.
	inaccessible bool
}

// actionJoins names the other objects a row points at, read by a
// cross-resource jump to filter a different kind's table down to only the
// rows that belong to this one — "the VMs on this host", not every VM in
// scope. Only VM/template rows populate the VM-shaped fields, and only host
// rows populate cluster, because those are the only jumps the interface
// offers; see jumpAction in actions.go.
type actionJoins struct {
	host       string
	cluster    string
	datastores []string
	networks   []string
}

// row is one line in the resource table, already flattened. The table renderer
// knows nothing about virtual machines or datastores: each kind supplies its
// own columns, cells and detail fields, and everything below is generic.
type row struct {
	key     string
	context string
	// kind is what the row is, carried on the row rather than read from the
	// model's current tab so that a search result — which sits in a list of
	// seven kinds at once — still knows what it is.
	kind   vsphere.Kind
	name   string
	glyph  string
	status rowStatus
	// where is the object's place in the estate. The search table lists
	// every kind side by side, so it cannot use any one kind's columns and
	// needs the datacenter and path in a form it can reach.
	where  vsphere.Location
	cells  []string
	detail []field
	// notes are the free-form paragraphs under the detail fields, used for
	// annotations that would not survive being squeezed into a column.
	notes []field
	// target and joins back the detail pane's field-cursor actions — see
	// actions.go. The browse table never reads either.
	target actionTarget
	joins  actionJoins
	// vm is the virtual machine a VM row was built from, so the detail pane
	// can judge its fields and chart its counters. Nil for every other kind.
	vm *vsphere.VM
	// cluster is the cluster a cluster row was built from, so its pane can
	// draw the Hosts & VMs and Storage pages. Nil for every other kind.
	cluster *vsphere.Cluster
	// marks are verdicts on detail fields, by label, drawn after the value
	// the way a VM's field marks are. Kinds without any leave it nil.
	marks map[string]fieldMark
	// tree places a row in the grouped Networks tab; see network.go.
	tree treeInfo
}

// columnsFor returns the columns for a kind. withContext adds the leading
// vCenter column, which only earns its width when more than one is in view.
func columnsFor(kind vsphere.Kind, withContext bool) []column {
	var cols []column
	if withContext {
		cols = append(cols, column{title: "VCENTER", width: 14})
	}
	switch kind {
	case vsphere.KindVM:
		cols = append(cols,
			column{title: "NAME"},
			// POWER is sized to its heading: its values are "on" and "off",
			// and powerCell shortens the one word that would not fit.
			column{title: "POWER", width: 5},
			column{title: "CPU", width: 3, right: true},
			column{title: "MEM", width: 6, right: true},
			column{title: "IP ADDRESS", width: 16},
			column{title: "HOST", width: 18},
			column{title: "DATACENTER", width: 14},
		)
	case vsphere.KindTemplate:
		cols = append(cols,
			// Template names are short beside their guest OS, so the name
			// column fits them rather than pushing GUEST OS off to the right.
			column{title: "NAME", fit: true},
			column{title: "GUEST OS", width: 28},
			column{title: "CPU", width: 4, right: true},
			column{title: "MEM", width: 6, right: true},
			column{title: "DISK", width: 8, right: true},
			column{title: "DATACENTER", width: 14},
		)
	case vsphere.KindHost:
		cols = append(cols,
			column{title: "NAME"},
			// The columns after NAME are kept close to their values ("8.0.3"
			// under VERSION) so a host's FQDN keeps the width.
			column{title: "STATE", width: 12},
			column{title: "CLUSTER", width: 12},
			column{title: "CPU", width: hostBarWidth + usageBarSuffix},
			column{title: "MEMORY", width: hostBarWidth + usageBarSuffix},
			column{title: "VMS", width: 4, right: true},
			column{title: "VERSION", width: 7},
		)
	case vsphere.KindCluster:
		cols = append(cols,
			column{title: "NAME"},
			column{title: "HOSTS", width: 7, right: true},
			column{title: "CORES", width: 7, right: true},
			column{title: "MEMORY", width: 10, right: true},
			column{title: "DRS", width: 5},
			column{title: "HA", width: 5},
			column{title: "DATACENTER", width: 14},
		)
	case vsphere.KindVApp:
		cols = append(cols,
			column{title: "NAME"},
			column{title: "STATUS", width: 10},
			column{title: "VMS", width: 5, right: true},
			column{title: "CHILDREN", width: 18},
			column{title: "DATACENTER", width: 14},
		)
	case vsphere.KindDatastore:
		cols = append(cols,
			column{title: "NAME"},
			column{title: "TYPE", width: 8},
			column{title: "CAPACITY", width: 10, right: true},
			column{title: "FREE", width: 10, right: true},
			column{title: "USED", width: 18},
			column{title: "DATACENTER", width: 14},
		)
	case vsphere.KindNetwork:
		cols = append(cols,
			column{title: "NAME"},
			column{title: "TYPE", width: 10},
			column{title: "SWITCH", width: 16},
			column{title: "VLAN", width: 10},
			column{title: "ACCESSIBLE", width: 12},
			column{title: "DATACENTER", width: 14},
		)
	}
	return cols
}

// tabTitle is the label on the resource tab.
func tabTitle(kind vsphere.Kind) string {
	switch kind {
	case vsphere.KindVM:
		return "VMs"
	case vsphere.KindTemplate:
		return "Templates"
	case vsphere.KindHost:
		return "Hosts"
	case vsphere.KindCluster:
		return "Clusters"
	case vsphere.KindVApp:
		return "vApps"
	case vsphere.KindDatastore:
		return "Datastores"
	case vsphere.KindNetwork:
		return "Networks"
	default:
		return string(kind)
	}
}

// Search column limits. A column is sized to its widest value, but never past
// its cap: one freakishly long name or vCenter label must not starve PATH,
// which is the column the operator reads to tell two same-named objects apart.
const (
	searchContextMax    = 20
	searchDatacenterMax = 16
	searchNameMax       = 40
	// searchPathMin is the least PATH is worth drawing: "…/compute-a/esxi-01"
	// still says which host, where a few columns of it say nothing.
	searchPathMin = 20
)

// searchColumns are the columns of the estate-wide search result table: the
// same five, in the same order, that "vsfleet search" prints. It lists seven
// kinds at once, so it can only use what every object has — which vCenter,
// what it is, its name, and where it sits. The widths here are the fallback
// for a terminal too narrow to draw PATH; layoutSearch sizes them to the
// results otherwise.
func searchColumns() []column {
	return []column{
		{title: "VCENTER", width: 12},
		{title: "TYPE", width: 9},
		{title: "NAME"},
		{title: "DATACENTER", width: 12},
		{title: "PATH", width: 26, keepTail: true},
	}
}

// layoutSearch assigns widths to the search columns. Unlike the browse
// tables, whose NAME column absorbs the slack, search sizes every column to
// its widest value and hands the slack to PATH: names are short and PATH is
// the long, discriminating value, so a flexible NAME just wastes columns the
// path needed. When even that does not fit, DATACENTER (the first segment of
// PATH) goes first, then NAME shrinks to its floor; PATH is only left out, as
// it always was, when it cannot reach searchPathMin. The returned slice is
// aligned with searchColumns, a zero marking a column that is not drawn.
func layoutSearch(rows []row, total int) []int {
	cols := searchColumns()
	if len(rows) == 0 {
		// Nothing to size to: the headings alone should not reshuffle.
		return layoutColumns(cols, total)
	}
	nat := make([]int, len(cols))
	for i, c := range cols {
		nat[i] = ansi.StringWidth(c.title)
	}
	for _, r := range rows {
		for i, cell := range searchCells(r) {
			nat[i] = max(nat[i], ansi.StringWidth(cell))
		}
	}
	const vcenter, kind, name, datacenter, path = 0, 1, 2, 3, 4
	nat[vcenter] = min(nat[vcenter], searchContextMax)
	nat[name] = min(nat[name], searchNameMax)
	nat[datacenter] = min(nat[datacenter], searchDatacenterMax)

	need := min(searchPathMin, nat[path])
	for _, attempt := range []struct {
		datacenter bool
		name       int
	}{
		{true, nat[name]},
		{false, nat[name]},
		{false, min(nat[name], minNameWidth)},
	} {
		widths := []int{nat[vcenter], nat[kind], attempt.name, 0, 0}
		if attempt.datacenter {
			widths[datacenter] = nat[datacenter]
		}
		used, drawn := 0, 0
		for _, w := range widths {
			if w > 0 {
				used += w
				drawn++
			}
		}
		// PATH is drawn after the others, so it adds one more gap.
		if spare := total - used - cellGap*drawn; spare >= need {
			widths[path] = spare
			return widths
		}
	}
	return layoutColumns(cols, total)
}

// searchCells renders one row for that table.
func searchCells(r row) []string {
	return []string{
		r.context,
		kindWord(r.kind),
		r.name,
		humanize.Dash(r.where.Datacenter),
		humanize.Dash(r.where.Path),
	}
}

// kindWord is the one-word name of a kind, matching what the command line
// calls it: a search result reading "template" is a row you could have asked
// for with "vsfleet template list".
func kindWord(kind vsphere.Kind) string {
	return string(kind)
}

// shortTabTitle is the abbreviated label the kind bar falls back to when the
// full titles and their counts no longer fit the terminal.
func shortTabTitle(kind vsphere.Kind) string {
	switch kind {
	case vsphere.KindVM:
		return "VM"
	case vsphere.KindTemplate:
		return "Tpl"
	case vsphere.KindHost:
		return "Host"
	case vsphere.KindCluster:
		return "Clus"
	case vsphere.KindVApp:
		return "vApp"
	case vsphere.KindDatastore:
		return "DS"
	case vsphere.KindNetwork:
		return "Net"
	default:
		return string(kind)
	}
}

// compactTabTitle is the last-resort label used at the documented 40-column
// minimum. vApp keeps its name because hiding the final kind behind an opaque
// number makes the tab look as though it does not exist at all.
func compactTabTitle(kind vsphere.Kind) string {
	switch kind {
	case vsphere.KindVM:
		return "VM"
	case vsphere.KindTemplate:
		return "T"
	case vsphere.KindHost:
		return "H"
	case vsphere.KindCluster:
		return "C"
	case vsphere.KindVApp:
		return "vApp"
	case vsphere.KindDatastore:
		return "D"
	case vsphere.KindNetwork:
		return "N"
	default:
		return string(kind)
	}
}

// kindLabel names one object of a kind, for a detail pane where the plural tab
// title would be describing a single thing.
func kindLabel(kind vsphere.Kind) string {
	switch kind {
	case vsphere.KindVM:
		return "Virtual machine"
	case vsphere.KindTemplate:
		return "Template"
	case vsphere.KindHost:
		return "ESXi host"
	case vsphere.KindCluster:
		return "Cluster"
	case vsphere.KindVApp:
		return "vApp"
	case vsphere.KindDatastore:
		return "Datastore"
	case vsphere.KindNetwork:
		return "Network"
	default:
		return string(kind)
	}
}

// countFor reports how many objects of a kind an inventory holds, so the tab
// bar can carry counts without every tab building its rows.
func countFor(inv *vsphere.Inventory, kind vsphere.Kind) int {
	if inv == nil {
		return 0
	}
	switch kind {
	case vsphere.KindVM:
		return len(inv.VMs)
	case vsphere.KindTemplate:
		return len(inv.Templates)
	case vsphere.KindHost:
		return len(inv.Hosts)
	case vsphere.KindCluster:
		return len(inv.Clusters)
	case vsphere.KindVApp:
		return len(inv.VApps)
	case vsphere.KindDatastore:
		return len(inv.Datastores)
	case vsphere.KindNetwork:
		return len(inv.Networks)
	default:
		return 0
	}
}

// rowsFor flattens one inventory into table rows for a kind.
func rowsFor(inv *vsphere.Inventory, kind vsphere.Kind, withContext bool) []row {
	if inv == nil {
		return nil
	}
	var out []row
	switch kind {
	case vsphere.KindVM:
		for _, vm := range inv.VMs {
			out = append(out, vmRow(vm, withContext))
		}
	case vsphere.KindTemplate:
		for _, vm := range inv.Templates {
			out = append(out, templateRow(vm, withContext))
		}
	case vsphere.KindHost:
		for _, h := range inv.Hosts {
			out = append(out, hostRow(h, withContext))
		}
	case vsphere.KindCluster:
		for _, c := range inv.Clusters {
			out = append(out, clusterRow(c, inv, withContext))
		}
	case vsphere.KindVApp:
		for _, v := range inv.VApps {
			out = append(out, vappRow(v, withContext))
		}
	case vsphere.KindDatastore:
		for _, d := range inv.Datastores {
			out = append(out, datastoreRow(d, inv, withContext))
		}
	case vsphere.KindNetwork:
		for _, n := range inv.Networks {
			out = append(out, networkRow(n, withContext))
		}
	}
	// The constructors each know one kind; stamping it here keeps them from
	// having to repeat it, and keeps it impossible to forget.
	for i := range out {
		out[i].kind = kind
	}
	return out
}

func lead(withContext bool, ctxName string, cells ...string) []string {
	if withContext {
		return append([]string{ctxName}, cells...)
	}
	return cells
}

func vmRow(vm vsphere.VM, withContext bool) row {
	st := statusIdle
	glyph := glyphOffline
	switch vm.PowerState {
	case "poweredOn":
		st, glyph = statusGood, glyphOnline
	case "suspended":
		st, glyph = statusWarn, glyphPending
	}
	r := row{
		key:     vm.Context + "/" + vm.ID,
		context: vm.Context,
		where:   vm.Location,
		name:    vm.Name,
		glyph:   glyph,
		status:  st,
		cells: lead(withContext, vm.Context,
			vm.Name,
			powerCell(vm.PowerState),
			strconv.FormatInt(int64(vm.CPU), 10),
			humanize.MB(vm.MemoryMB),
			humanize.Dash(vm.IPAddress),
			humanize.Dash(vm.Host),
			humanize.Dash(vm.Datacenter),
		),
		detail: []field{
			{"vCenter", vm.Context},
			{"Power state", powerWord(vm.PowerState)},
			{"Guest OS", humanize.Dash(vm.GuestOS)},
			{"Guest state", guestStateWord(vm.GuestState)},
			{"VMware Tools", toolsWord(vm.ToolsState, vm.ToolsVersionStatus)},
			{"Tools version", humanize.Dash(vm.ToolsVersion)},
			{"IP address", humanize.Dash(vm.IPAddress)},
			{"DNS name", humanize.Dash(vm.GuestHostName)},
			{"CPU", strconv.FormatInt(int64(vm.CPU), 10) + " vCPU"},
			{"Memory", humanize.MB(vm.MemoryMB)},
			{"Committed storage", humanize.GB(vm.StorageGB)},
			{"Snapshots", snapshotsValue(vm)},
			{"Guest disks", guestDisksValue(vm)},
			{"Host", humanize.Dash(vm.Host)},
			{"Cluster", humanize.Dash(vm.Cluster)},
			{"Datacenter", humanize.Dash(vm.Datacenter)},
			{"Folder", humanize.Dash(vm.Folder)},
			{"Datastores", humanize.Dash(strings.Join(vm.Datastores, ", "))},
			{"Inventory path", humanize.Dash(vm.Path)},
			{"Managed object", vm.ID},
		},
		target: actionTarget{moref: vm.ID, morefKind: "VirtualMachine", address: vm.IPAddress, hostName: vm.GuestHostName, path: vm.Path},
		joins:  actionJoins{host: vm.Host, cluster: vm.Cluster, datastores: vm.Datastores, networks: nicNetworks(vm.NICs)},
		vm:     &vm,
	}
	if vm.Annotation != "" {
		r.notes = append(r.notes, field{"Notes", vm.Annotation})
	}
	return r
}

// nicNetworks lists the distinct networks a VM's adapters are attached to.
// NICs are only populated at full fetch detail, so a VM loaded at summary
// detail simply offers no network jump — that is missing evidence, not a VM
// with no networking, the same convention VMPartition documents for guest
// filesystems.
func nicNetworks(nics []vsphere.VMNIC) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range nics {
		if n.Network == "" || seen[n.Network] {
			continue
		}
		seen[n.Network] = true
		out = append(out, n.Network)
	}
	return out
}

func templateRow(vm vsphere.VM, withContext bool) row {
	return row{
		key:     vm.Context + "/" + vm.ID,
		context: vm.Context,
		where:   vm.Location,
		name:    vm.Name,
		glyph:   glyphSkip,
		status:  statusNone,
		cells: lead(withContext, vm.Context,
			vm.Name,
			humanize.Dash(vm.GuestOS),
			strconv.FormatInt(int64(vm.CPU), 10),
			humanize.MB(vm.MemoryMB),
			humanize.GB(vm.StorageGB),
			humanize.Dash(vm.Datacenter),
		),
		detail: []field{
			{"vCenter", vm.Context},
			{"Guest OS", humanize.Dash(vm.GuestOS)},
			{"CPU", strconv.FormatInt(int64(vm.CPU), 10) + " vCPU"},
			{"Memory", humanize.MB(vm.MemoryMB)},
			{"Committed storage", humanize.GB(vm.StorageGB)},
			{"Datacenter", humanize.Dash(vm.Datacenter)},
			{"Folder", humanize.Dash(vm.Folder)},
			{"Datastores", humanize.Dash(strings.Join(vm.Datastores, ", "))},
			{"Inventory path", humanize.Dash(vm.Path)},
			{"Managed object", vm.ID},
		},
		notes:  noteOf(vm.Annotation),
		target: actionTarget{moref: vm.ID, morefKind: "VirtualMachine", path: vm.Path},
	}
}

func hostRow(h vsphere.Host, withContext bool) row {
	st, glyph := statusGood, glyphOnline
	state := h.ConnectionState
	switch {
	case h.InMaintenance:
		st, glyph, state = statusWarn, glyphPending, "maintenance"
	case h.ConnectionState != "connected":
		st, glyph = statusBad, glyphFail
	}
	return row{
		key:     h.Context + "/" + h.ID,
		context: h.Context,
		where:   h.Location,
		name:    h.Name,
		glyph:   glyph,
		status:  st,
		cells: lead(withContext, h.Context,
			h.Name,
			state,
			humanize.Dash(h.Cluster),
			capacityBar(h.CPUUsageMHz, h.TotalCPU(), hostBarWidth),
			capacityBar(h.MemoryUsageMB, h.MemoryMB, hostBarWidth),
			strconv.Itoa(h.VMCount),
			humanize.Dash(h.Version),
		),
		detail: []field{
			{"vCenter", h.Context},
			{"Connection", state},
			{"Power state", powerWord(h.PowerState)},
			{"Maintenance mode", yesNo(h.InMaintenance)},
			{"Cluster", humanize.Dash(h.Cluster)},
			{"Hardware", humanize.Dash(strings.TrimSpace(h.Vendor + " " + h.Model))},
			{"ESXi", humanize.Dash(strings.TrimSpace(h.Version + " build-" + h.Build))},
			{"CPU", fmt.Sprintf("%d cores / %d threads @ %s", h.CPUCores, h.CPUThreads, humanize.MHz(int64(h.CPUMHz)))},
			{"CPU used", ratio(humanize.MHz(h.CPUUsageMHz), humanize.MHz(h.TotalCPU()))},
			{"Memory", humanize.MB(h.MemoryMB)},
			{"Memory used", ratio(humanize.MBPrecise(h.MemoryUsageMB), humanize.MBPrecise(h.MemoryMB))},
			{"Virtual machines", strconv.Itoa(h.VMCount)},
			{"Datacenter", humanize.Dash(h.Datacenter)},
			{"Inventory path", humanize.Dash(h.Path)},
			{"Managed object", h.ID},
		},
		// address is the host's registered name, usually its FQDN. Host
		// carries no management IP — config.network.vnic would supply the
		// real vmk address, but it is a heavy property to fetch on every
		// load of a large estate, so the name is what SSH and the Host
		// Client link both use.
		target: actionTarget{moref: h.ID, morefKind: "HostSystem", address: h.Name, path: h.Path},
		joins:  actionJoins{cluster: h.Cluster},
	}
}

// clusterMorefKind names the managed object type behind a Cluster row.
// listClusters retrieves both ComputeResource and ClusterComputeResource and
// tells them apart by Self.Type (cluster.go); Standalone survives that one
// bit of the distinction onto the domain object, so the deep link builder
// does not need vim25/types imported here to rebuild it.
func clusterMorefKind(standalone bool) string {
	if standalone {
		return "ComputeResource"
	}
	return "ClusterComputeResource"
}

func vappRow(v vsphere.VApp, withContext bool) row {
	st, glyph := statusIdle, glyphOffline
	switch v.Status {
	case "started":
		st, glyph = statusGood, glyphOnline
	case "starting", "stopping", "suspended":
		st, glyph = statusWarn, glyphPending
	}
	return row{
		key:     v.Context + "/" + v.ID,
		context: v.Context,
		where:   v.Location,
		name:    v.Name,
		glyph:   glyph,
		status:  st,
		cells: lead(withContext, v.Context,
			v.Name,
			humanize.Dash(v.Status),
			strconv.Itoa(v.DirectVMCount),
			childSummary(v.ChildVAppCount, v.ChildResourcePoolCount),
			humanize.Dash(v.Datacenter),
		),
		detail: []field{
			{"vCenter", v.Context},
			{"Status", humanize.Dash(v.Status)},
			{"Parent container", humanize.Dash(v.ParentContainer)},
			{"Parent vApp", humanize.Dash(v.ParentVApp)},
			{"Direct VMs", listOrDash(v.DirectVMs)},
			{"Direct VM references", listOrDash(v.DirectVMRefs)},
			{"Nested vApps", listOrDash(v.ChildVApps)},
			{"Resource pools", listOrDash(v.ChildResourcePools)},
			{"Cluster / compute resource", humanize.Dash(v.Cluster)},
			{"Datacenter", humanize.Dash(v.Datacenter)},
			{"Inventory path", humanize.Dash(v.Path)},
			{"Managed object", v.ID},
		},
		target: actionTarget{moref: v.ID, morefKind: "VirtualApp", path: v.Path},
	}
}

func childSummary(vapps, pools int) string {
	return fmt.Sprintf("%d vApp / %d pool", vapps, pools)
}

func listOrDash(items []string) string {
	return humanize.Dash(strings.Join(items, ", "))
}

func datastoreRow(d vsphere.Datastore, inv *vsphere.Inventory, withContext bool) row {
	st, glyph := statusGood, glyphOnline
	switch {
	case !d.Accessible:
		st, glyph = statusBad, glyphFail
	case d.UsedPercent() >= 90:
		st, glyph = statusBad, glyphOnline
	case d.UsedPercent() >= 75:
		st, glyph = statusWarn, glyphOnline
	}
	hostsValue, mark, marked := datastoreHosts(d, inv)
	var marks map[string]fieldMark
	if marked {
		marks = map[string]fieldMark{"Hosts": mark}
	}
	return row{
		key:     d.Context + "/" + d.ID,
		context: d.Context,
		where:   d.Location,
		name:    d.Name,
		glyph:   glyph,
		status:  st,
		cells: lead(withContext, d.Context,
			d.Name,
			humanize.Dash(d.Type),
			humanize.Bytes(d.CapacityBytes),
			humanize.Bytes(d.FreeBytes),
			usageBar(d.UsedPercent(), 10),
			humanize.Dash(d.Datacenter),
		),
		detail: []field{
			{"vCenter", d.Context},
			{"Type", humanize.Dash(d.Type)},
			{"Accessible", yesNo(d.Accessible)},
			{"Capacity", humanize.Bytes(d.CapacityBytes)},
			{"Used", fmt.Sprintf("%s (%.0f%%)", humanize.Bytes(d.UsedBytes()), d.UsedPercent())},
			{"Free", humanize.Bytes(d.FreeBytes)},
			{"Hosts", hostsValue},
			{"Maintenance", humanize.Dash(d.Maintenance)},
			{"Datacenter", humanize.Dash(d.Datacenter)},
			{"Inventory path", humanize.Dash(d.Path)},
			{"Managed object", d.ID},
		},
		marks:  marks,
		target: actionTarget{moref: d.ID, morefKind: "Datastore", path: "[" + d.Name + "]", inaccessible: !d.Accessible},
	}
}

func networkRow(n vsphere.Network, withContext bool) row {
	st, glyph := statusGood, glyphOnline
	if !n.Accessible {
		st, glyph = statusBad, glyphFail
	}
	return row{
		key:     n.Context + "/" + n.ID,
		context: n.Context,
		where:   n.Location,
		name:    n.Name,
		glyph:   glyph,
		status:  st,
		cells: lead(withContext, n.Context,
			n.Name,
			humanize.Dash(n.Type),
			humanize.Dash(n.Switch),
			humanize.Dash(n.VLAN),
			yesNo(n.Accessible),
			humanize.Dash(n.Datacenter),
		),
		detail: []field{
			{"vCenter", n.Context},
			{"Type", humanize.Dash(n.Type)},
			{"Switch", humanize.Dash(n.Switch)},
			{"VLAN", humanize.Dash(n.VLAN)},
			{"Accessible", yesNo(n.Accessible)},
			{"Datacenter", humanize.Dash(n.Datacenter)},
			{"Inventory path", humanize.Dash(n.Path)},
			{"Managed object", n.ID},
		},
		target: actionTarget{moref: n.ID, morefKind: networkMorefKind(n.Type)},
	}
}

// networkMorefKind inverts networkTypeName's humanization back into the
// managed object type a deep link needs. It is an approximation — a link
// built from "standard" always resolves to "Network" even though vCenter
// itself resolves several MO types that way — but it is exactly the same
// three-way distinction networkTypeName already draws, so it costs nothing
// beyond the inverse table living here instead of on vsphere.Network.
func networkMorefKind(humanized string) string {
	switch humanized {
	case "portgroup":
		return "DistributedVirtualPortgroup"
	case "opaque":
		return "OpaqueNetwork"
	default:
		return "Network"
	}
}

func noteOf(s string) []field {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return []field{{"Notes", s}}
}

// powerWord turns vSphere's poweredOn into the word an operator would say.
func powerWord(s string) string {
	switch s {
	case "poweredOn":
		return "on"
	case "poweredOff":
		return "off"
	case "suspended":
		return "suspended"
	case "":
		return "-"
	default:
		return s
	}
}

// powerCell is powerWord for the VM table's POWER column, which is only as
// wide as its heading. The detail pane keeps the full word.
func powerCell(s string) string {
	if s == "suspended" {
		return "susp"
	}
	return powerWord(s)
}

// toolsWord gives installation evidence precedence over the running enum:
// vSphere reports not-running even when Tools is not installed.
func toolsWord(state, versionStatus string) string {
	if versionStatus == "guestToolsNotInstalled" {
		return "not installed"
	}
	switch state {
	case "guestToolsRunning":
		return "running"
	case "guestToolsNotRunning":
		return "not running"
	case "guestToolsExecutingScripts":
		return "executing scripts"
	default:
		return humanize.Dash(state)
	}
}

func guestStateWord(state string) string {
	switch state {
	case "notRunning":
		return "not running"
	case "shuttingDown":
		return "shutting down"
	case "resetting":
		return "restarting"
	case "standby":
		return "standby"
	default:
		return humanize.Dash(state)
	}
}

// countWord renders irregular plurals without placeholder suffixes.
func countWord(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// kindCount is "1 VM" or "10 VMs": n of one kind, in the words the estate
// summary uses.
func kindCount(kind vsphere.Kind, n int) string {
	switch kind {
	case vsphere.KindVM:
		return countWord(n, "VM", "VMs")
	case vsphere.KindTemplate:
		return countWord(n, "template", "templates")
	case vsphere.KindHost:
		return countWord(n, "host", "hosts")
	case vsphere.KindCluster:
		return countWord(n, "cluster", "clusters")
	case vsphere.KindVApp:
		return countWord(n, "vApp", "vApps")
	case vsphere.KindDatastore:
		return countWord(n, "datastore", "datastores")
	case vsphere.KindNetwork:
		return countWord(n, "network", "networks")
	default:
		return countWord(n, string(kind), string(kind)+"s")
	}
}

// historyFieldValue shares browse vocabulary while leaving stored evidence
// and exports intact. Other field values keep their original meaning.
func historyFieldValue(name, value string) string {
	if value == "" {
		return "—"
	}
	switch name {
	case "power_state", "power":
		return powerWord(value)
	case "tools_state":
		return toolsWord(value, "")
	case "guest_state":
		return guestStateWord(value)
	default:
		return value
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func ratio(used, total string) string {
	if total == "-" {
		return "-"
	}
	if used == "-" {
		used = "0"
	}
	return used + "/" + total
}

// usageBarSuffix is what usageBar appends after the bar itself: a space and a
// right-aligned percentage such as " 75%". A column holding a bar is the bar's
// width plus this. hostBarWidth is the bar the Hosts table draws for CPU and
// memory, short enough that both still fit beside the cluster at 80 columns;
// Datastores keep their longer bar because they have only one.
const (
	usageBarSuffix = 5
	hostBarWidth   = 8
)

// capacityBar is usageBar for a used/total pair, as the Hosts table draws CPU
// and memory. Unlike a datastore, a host with a known capacity and nothing
// used is a real, idle 0%, so it gets an empty bar rather than the dash that
// means "not reported". Without a capacity there is nothing to take a share of.
func capacityBar(used, total int64, width int) string {
	if total <= 0 {
		return "-"
	}
	if used <= 0 {
		return strings.Repeat("·", width) + "   0%"
	}
	return usageBar(float64(used)/float64(total)*100, width)
}

// usageBar renders a percentage as a bar plus the number. The bar is for the
// glance and the number is for the decision; neither is enough alone.
func usageBar(pct float64, width int) string {
	if pct <= 0 {
		return "-"
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct/100*float64(width) + 0.5)
	if filled == 0 {
		filled = 1
	}
	return strings.Repeat("█", filled) + strings.Repeat("·", width-filled) + fmt.Sprintf(" %3.0f%%", pct)
}
