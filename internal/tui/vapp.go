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

// vappWorkspace owns the navigation that is specific to a vAPP. The browse
// cursor remains on the vAPP row underneath, while roots lets Enter on a
// nested vAPP behave like following the inventory tree and Esc walk back up.
type vappWorkspace struct {
	roots  []string
	cursor int
	offset int
}

type vappMember struct {
	key       string
	context   string
	id        string
	name      string
	kind      vsphere.Kind
	depth     int
	state     string
	cpu       string
	memory    string
	host      string
	glyph     string
	status    rowStatus
	openable  bool
	asContext string
	missing   bool
	cycle     bool
	vm        *vsphere.VM
}

type vappChild struct {
	app  *vsphere.VApp
	key  string
	name string
}

const (
	vappMemberTypeWidth                = 7
	vappMemberStateWidth               = 12
	vappMemberCPUWidth                 = 5
	vappMemberMemoryWidth              = 7
	vappMemberHostWidth                = 18
	vappMemberPool        vsphere.Kind = "pool"
)

func (m *Model) openVApp(r row) tea.Cmd {
	m.vapp = &vappWorkspace{roots: []string{r.key}}
	m.vappVM = nil
	m.detailCursor, m.detailY = 0, 0
	m.mode = modeVAppDetail
	return nil
}

func (m *Model) activeVApp() (*vsphere.VApp, *contextState, bool) {
	if m.vapp == nil || len(m.vapp.roots) == 0 {
		return nil, nil, false
	}
	contextName, id, ok := splitVAppKey(m.vapp.roots[len(m.vapp.roots)-1])
	if !ok {
		return nil, nil, false
	}
	st, ok := m.byName[contextName]
	if !ok || st.inv == nil {
		return nil, st, false
	}
	for i := range st.inv.VApps {
		if st.inv.VApps[i].ID == id {
			return &st.inv.VApps[i], st, true
		}
	}
	return nil, st, false
}

func splitVAppKey(key string) (string, string, bool) {
	contextName, id, ok := strings.Cut(key, "/")
	return contextName, id, ok && contextName != "" && id != ""
}

func managedRef(ref string) (string, string, bool) {
	kind, id, ok := strings.Cut(ref, ":")
	return kind, id, ok && kind != "" && id != ""
}

func vappKey(contextName, id string) string {
	return contextName + "/" + id
}

func (m *Model) vappMembers(root *vsphere.VApp, inv *vsphere.Inventory) []vappMember {
	if root == nil || inv == nil {
		return nil
	}
	members := m.vappMembersFrom(root, inv, 0, map[string]bool{vappKey(root.Context, root.ID): true})
	for i := range members {
		if members[i].kind != vsphere.KindVM {
			continue
		}
		address := ""
		if members[i].vm != nil {
			address = members[i].vm.IPAddress
		}
		if nested := m.nestedContextFor(members[i].context, members[i].id, address); nested != nil {
			members[i].asContext = nested.cc.Name
		}
	}
	return members
}

func (m *Model) vappMembersFrom(v *vsphere.VApp, inv *vsphere.Inventory, depth int, path map[string]bool) []vappMember {
	var out []vappMember

	// Containers come first so the hierarchy reads like an inventory tree;
	// their descendants immediately follow them at the next indentation level.
	children := childVApps(v, inv)
	sort.SliceStable(children, func(i, j int) bool {
		return children[i].name < children[j].name
	})
	for _, child := range children {
		key := child.key
		member := vappMember{key: key, context: v.Context, id: key, name: child.name, kind: vsphere.KindVApp, depth: depth, state: "not loaded", glyph: glyphFail, status: statusWarn, missing: child.app == nil}
		if child.app != nil {
			member = vappMemberFromVApp(child.app, depth, key)
		}
		if path[key] {
			member.cycle = true
			member.state = "cycle"
			member.glyph = glyphFail
			member.status = statusBad
			out = append(out, member)
			continue
		}
		member.openable = child.app != nil
		out = append(out, member)
		if child.app == nil {
			continue
		}
		nextPath := cloneBoolMap(path)
		nextPath[key] = true
		out = append(out, m.vappMembersFrom(child.app, inv, depth+1, nextPath)...)
	}

	vmMembers := directVMs(v, inv, depth)
	sort.SliceStable(vmMembers, func(i, j int) bool {
		return vmMembers[i].name < vmMembers[j].name
	})
	out = append(out, vmMembers...)

	pools := resourcePools(v, depth)
	sort.SliceStable(pools, func(i, j int) bool {
		return pools[i].name < pools[j].name
	})
	out = append(out, pools...)
	return out
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in)+1)
	for key, value := range in {
		out[key] = value
	}
	return out
}

func childVApps(parent *vsphere.VApp, inv *vsphere.Inventory) []vappChild {
	var out []vappChild
	seen := map[string]bool{}
	for _, ref := range parent.ChildVAppRefs {
		kind, id, ok := managedRef(ref)
		if !ok || kind != "VirtualApp" {
			continue
		}
		var matched *vsphere.VApp
		for i := range inv.VApps {
			if inv.VApps[i].ID != id {
				continue
			}
			if parent.Context != "" && inv.VApps[i].Context == parent.Context {
				matched = &inv.VApps[i]
				break
			}
			if matched == nil && (parent.Context == "" || inv.VApps[i].Context == "") {
				matched = &inv.VApps[i]
			}
		}
		if matched != nil {
			key := vappKey(matched.Context, id)
			if !seen[key] {
				out = append(out, vappChild{app: matched, key: key, name: matched.Name})
				seen[key] = true
			}
		} else {
			key := vappKey(parent.Context, id)
			if !seen[key] {
				out = append(out, vappChild{key: key, name: "vAPP " + id})
				seen[key] = true
			}
		}
	}
	// Older snapshots and hand-written backends may not have reference fields.
	for _, name := range parent.ChildVApps {
		var exactMatches []*vsphere.VApp
		var fallbackMatches []*vsphere.VApp
		for i := range inv.VApps {
			if inv.VApps[i].Name != name {
				continue
			}
			if parent.Context != "" && inv.VApps[i].Context == parent.Context {
				exactMatches = append(exactMatches, &inv.VApps[i])
			} else if parent.Context == "" || inv.VApps[i].Context == "" {
				fallbackMatches = append(fallbackMatches, &inv.VApps[i])
			}
		}

		candidates := exactMatches
		if len(candidates) == 0 {
			candidates = fallbackMatches
		}

		if len(candidates) > 1 {
			sort.SliceStable(candidates, func(i, j int) bool {
				keyI := vappKey(candidates[i].Context, candidates[i].ID)
				keyJ := vappKey(candidates[j].Context, candidates[j].ID)
				return keyI < keyJ
			})
		}

		alreadyResolved := false
		for _, c := range candidates {
			key := vappKey(c.Context, c.ID)
			if seen[key] {
				alreadyResolved = true
				break
			}
		}
		if alreadyResolved {
			continue
		}

		if len(candidates) > 0 {
			chosen := candidates[0]
			key := vappKey(chosen.Context, chosen.ID)
			out = append(out, vappChild{app: chosen, key: key, name: chosen.Name})
			seen[key] = true
			continue
		}

		key := vappKey(parent.Context, "missing-vapp:"+name)
		if !seen[key] {
			out = append(out, vappChild{key: key, name: name})
			seen[key] = true
		}
	}
	return out
}

func vappMemberFromVApp(v *vsphere.VApp, depth int, key string) vappMember {
	r := vappRow(*v, false)
	return vappMember{
		key: key, context: v.Context, id: v.ID, name: v.Name,
		kind: vsphere.KindVApp, depth: depth, state: humanize.Dash(v.Status),
		glyph: r.glyph, status: r.status,
	}
}

func directVMs(v *vsphere.VApp, inv *vsphere.Inventory, depth int) []vappMember {
	var out []vappMember
	seen := map[string]bool{}
	for _, ref := range v.DirectVMRefs {
		kind, id, ok := managedRef(ref)
		if !ok || kind != "VirtualMachine" || seen[id] {
			continue
		}
		seen[id] = true
		found := false
		for i := range inv.VMs {
			if inv.VMs[i].ID == id {
				out = append(out, vappMemberFromVM(&inv.VMs[i], depth))
				found = true
				break
			}
		}
		if !found {
			out = append(out, missingVMMember(v.Context, id, "VM "+id))
		}
	}
	// Keep name-only data useful for old snapshots, while avoiding duplicate
	// rows when both the reference and the display name resolve successfully.
	for _, name := range v.DirectVMs {
		found := false
		for i := range inv.VMs {
			if inv.VMs[i].Name == name {
				found = true
				key := vappKey(inv.VMs[i].Context, inv.VMs[i].ID)
				if !seen[key] && !seen[inv.VMs[i].ID] {
					out = append(out, vappMemberFromVM(&inv.VMs[i], depth))
					seen[key], seen[inv.VMs[i].ID] = true, true
				}
				break
			}
		}
		if !found {
			out = append(out, missingVMMember(v.Context, "", name))
		}
	}
	if len(out) == 0 && v.DirectVMCount > 0 {
		out = append(out, missingVMMember(v.Context, "", fmt.Sprintf("%d VM(s) unavailable", v.DirectVMCount)))
	}
	return out
}

func vappMemberFromVM(vm *vsphere.VM, depth int) vappMember {
	r := vmRow(*vm, false)
	return vappMember{
		key: vappKey(vm.Context, vm.ID), context: vm.Context, id: vm.ID,
		name: vm.Name, kind: vsphere.KindVM, depth: depth,
		state: powerWord(vm.PowerState), cpu: strconv.FormatInt(int64(vm.CPU), 10),
		memory: humanize.MB(vm.MemoryMB), host: humanize.Dash(vm.Host),
		glyph: r.glyph, status: r.status, openable: true, vm: vm,
	}
}

func missingVMMember(contextName, id, name string) vappMember {
	return vappMember{
		key: vappKey(contextName, id), context: contextName, id: id,
		name: name, kind: vsphere.KindVM, state: "not loaded", glyph: glyphFail,
		status: statusWarn, missing: true,
	}
}

func resourcePools(v *vsphere.VApp, depth int) []vappMember {
	var out []vappMember
	seen := map[string]bool{}
	for i, name := range v.ChildResourcePools {
		id := name
		if i < len(v.ChildResourcePoolRefs) {
			if kind, refID, ok := managedRef(v.ChildResourcePoolRefs[i]); ok && kind == "ResourcePool" {
				id = refID
			}
		}
		key := vappKey(v.Context, id)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, vappMember{
			key: key, context: v.Context, id: id, name: name,
			kind: vappMemberPool, depth: depth, state: humanize.Dash(""),
			glyph: glyphSkip, status: statusNone,
		})
	}
	// Preserve reference-only children from partial or restricted inventory
	// responses, using the managed ID as an honest fallback display name.
	refStart := len(v.ChildResourcePools)
	if refStart > len(v.ChildResourcePoolRefs) {
		refStart = len(v.ChildResourcePoolRefs)
	}
	for _, ref := range v.ChildResourcePoolRefs[refStart:] {
		kind, id, ok := managedRef(ref)
		if !ok || kind != "ResourcePool" {
			continue
		}
		key := vappKey(v.Context, id)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, vappMember{
			key: key, context: v.Context, id: id, name: id,
			kind: vappMemberPool, depth: depth, state: humanize.Dash(""),
			glyph: glyphSkip, status: statusNone,
		})
	}
	return out
}

func vappMemberColumns(withContext bool) []column {
	cols := []column{
		{title: "TYPE", width: vappMemberTypeWidth},
		{title: "NAME"},
		{title: "STATE", width: vappMemberStateWidth},
		{title: "CPU", width: vappMemberCPUWidth, right: true},
		{title: "MEM", width: vappMemberMemoryWidth, right: true},
		{title: "HOST", width: vappMemberHostWidth},
	}
	if withContext {
		cols = append([]column{{title: "VCENTER", width: 14}}, cols...)
	}
	return cols
}

func (m *Model) viewVAppDetail() []string {
	t := m.theme
	root, st, ok := m.activeVApp()
	if !ok || root == nil {
		return []string{t.dim.Render("vAPP inventory is no longer available")}
	}
	rootRow := vappRow(*root, false)
	title := t.title.Render(root.Name) + t.dim.Render("   vAPP · "+root.Context)
	state := t.statusStyle(rootRow.status).Render(rootRow.glyph + " " + humanize.Dash(root.Status))
	lines := []string{joinEnds(title, state, m.width), ""}
	lines = append(lines,
		t.header.Render("Summary"),
		truncate("  "+t.label.Render(pad("Parent", labelColumnPad, false))+t.value.Render(humanize.Dash(root.ParentContainer)), m.width),
		truncate("  "+t.label.Render(pad("Placement", labelColumnPad, false))+t.value.Render(humanize.Dash(root.Cluster)+" · "+humanize.Dash(root.Datacenter)), m.width),
		truncate("  "+t.label.Render(pad("Direct children", labelColumnPad, false))+t.value.Render(fmt.Sprintf("%d VM · %d vAPP · %d pool", root.DirectVMCount, root.ChildVAppCount, root.ChildResourcePoolCount)), m.width),
		"",
		t.header.Render("Members"),
	)

	members := m.vappMembers(root, st.inv)
	if m.vapp == nil {
		return lines
	}
	m.vapp.cursor = clamp(m.vapp.cursor, 0, max(0, len(members)-1))
	cols := vappMemberColumns(false)
	widths := layoutColumns(cols, m.width-glyphGutter)
	head := make([]string, 0, len(cols))
	for i, c := range cols {
		if widths[i] > 0 {
			head = append(head, pad(c.title, widths[i], c.right))
		}
	}
	lines = append(lines, t.header.Render(strings.Repeat(" ", glyphGutter)+strings.Join(head, strings.Repeat(" ", cellGap))))
	memberHeight := max(1, m.bodyHeight()-len(lines))
	if len(members) == 0 {
		lines = append(lines, t.dim.Render("  no members"))
		return scrollLines(lines, 0, m.bodyHeight())
	}
	if m.vapp.cursor < m.vapp.offset {
		m.vapp.offset = m.vapp.cursor
	}
	if m.vapp.cursor >= m.vapp.offset+memberHeight {
		m.vapp.offset = m.vapp.cursor - memberHeight + 1
	}
	m.vapp.offset = clamp(m.vapp.offset, 0, max(0, len(members)-memberHeight))
	for i := m.vapp.offset; i < len(members) && i < m.vapp.offset+memberHeight; i++ {
		lines = append(lines, m.renderVAppMember(members[i], cols, widths, i == m.vapp.cursor))
	}
	return scrollLines(lines, 0, m.bodyHeight())
}

func (m *Model) renderVAppMember(member vappMember, cols []column, widths []int, selected bool) string {
	name := strings.Repeat("  ", member.depth) + member.name
	if member.asContext != "" {
		name += "  → " + member.asContext
	}
	kind := "VM"
	switch member.kind {
	case vsphere.KindVApp:
		kind = "vAPP"
	case vappMemberPool:
		kind = "POOL"
	}
	cells := []string{kind, name, member.state, member.cpu, member.memory, member.host}
	if len(cols) > len(cells) {
		cells = append([]string{member.context}, cells...)
	}
	drawn := make([]string, 0, len(cols))
	for i, c := range cols {
		if widths[i] == 0 {
			continue
		}
		drawn = append(drawn, pad(cells[i], widths[i], c.right))
	}
	line := strings.Join(drawn, strings.Repeat(" ", cellGap))
	if selected {
		line = m.theme.focused.Render(line)
	} else if member.missing || member.cycle {
		line = m.theme.warn.Render(line)
	} else {
		line = m.theme.text.Render(line)
	}
	return m.theme.statusStyle(member.status).Render(member.glyph) + " " + line
}

func (m *Model) handleVAppDetailKey(msg tea.KeyMsg) tea.Cmd {
	root, st, ok := m.activeVApp()
	if !ok || root == nil || st == nil || st.inv == nil || m.vapp == nil {
		if key.Matches(msg, m.keys.Back) {
			m.clearVAppWorkspace()
			m.mode = modeBrowse
		}
		return nil
	}
	members := m.vappMembers(root, st.inv)
	page := max(1, m.bodyHeight()-8)
	switch {
	case key.Matches(msg, m.keys.Back):
		if len(m.vapp.roots) > 1 {
			m.vapp.roots = m.vapp.roots[:len(m.vapp.roots)-1]
			m.vapp.cursor, m.vapp.offset = 0, 0
		} else {
			m.clearVAppWorkspace()
			m.mode = modeBrowse
		}
	case key.Matches(msg, m.keys.Up):
		m.vapp.cursor = clamp(m.vapp.cursor-1, 0, max(0, len(members)-1))
	case key.Matches(msg, m.keys.Down):
		m.vapp.cursor = clamp(m.vapp.cursor+1, 0, max(0, len(members)-1))
	case key.Matches(msg, m.keys.PageUp):
		m.vapp.cursor = clamp(m.vapp.cursor-page, 0, max(0, len(members)-1))
	case key.Matches(msg, m.keys.PageDown):
		m.vapp.cursor = clamp(m.vapp.cursor+page, 0, max(0, len(members)-1))
	case key.Matches(msg, m.keys.Home):
		m.vapp.cursor = 0
	case key.Matches(msg, m.keys.End):
		m.vapp.cursor = max(0, len(members)-1)
	case key.Matches(msg, m.keys.Open):
		if m.vapp.cursor < 0 || m.vapp.cursor >= len(members) {
			return nil
		}
		member := members[m.vapp.cursor]
		if member.kind == vsphere.KindVApp && member.openable {
			m.vapp.roots = append(m.vapp.roots, member.key)
			m.vapp.cursor, m.vapp.offset = 0, 0
			m.detailCursor = 0
			return nil
		}
		if member.kind == vsphere.KindVM && member.openable && member.vm != nil {
			r := vmRow(*member.vm, false)
			r.kind = vsphere.KindVM
			m.vappVM = &r
			m.detailCursor, m.detailY = 0, 0
			m.mode = modeVAppVMDetail
			return m.ensureVMPerf(false)
		}
	}
	return nil
}

func (m *Model) viewVAppVMDetail() []string {
	if m.vappVM == nil {
		return []string{m.theme.dim.Render("nothing selected")}
	}
	return m.viewDetailRow(*m.vappVM)
}

func (m *Model) handleVAppVMDetailKey(msg tea.KeyMsg) tea.Cmd {
	if m.vappVM == nil {
		m.mode = modeVAppDetail
		return nil
	}
	if m.actions != nil {
		return m.handleActionsKey(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Back):
		m.vappVM = nil
		m.mode = modeVAppDetail
	case key.Matches(msg, m.keys.Timeline):
		if m.assessment == nil {
			return nil
		}
		m.timelineQuery = m.vappVM.name
		m.timelineAll, m.timelineCursor, m.timelineOffset = false, 0, 0
		m.historyErr = nil
		m.timelineFrom = modeVAppVMDetail
		m.mode = modeHistoryTimeline
		return loadHistoryTimelineCmd(m.ctx, m.assessment, m.vappVM.name, false, false)
	case key.Matches(msg, m.keys.Open):
		return m.openFieldActions()
	case key.Matches(msg, m.keys.Up):
		m.moveDetailCursor(-1)
	case key.Matches(msg, m.keys.Down):
		m.moveDetailCursor(1)
	case key.Matches(msg, m.keys.PageUp):
		m.scrollDetailPage(-1)
	case key.Matches(msg, m.keys.PageDown):
		m.scrollDetailPage(1)
	case key.Matches(msg, m.keys.ShorterRange):
		return m.shiftPerfRange(-1)
	case key.Matches(msg, m.keys.LongerRange):
		return m.shiftPerfRange(1)
	case key.Matches(msg, m.keys.Reload):
		return m.ensureVMPerf(true)
	}
	return nil
}
