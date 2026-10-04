package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func testVApp(context, id, name string) vsphere.VApp {
	return vsphere.VApp{
		Location: vsphere.Location{Context: context},
		ID:       id,
		Name:     name,
	}
}

func TestChildVApps_ReferenceAndLegacyDuplicate(t *testing.T) {
	parent := testVApp("prod", "vapp-parent", "api-stack")
	parent.ChildVAppRefs = []string{"VirtualApp:vapp-child"}
	parent.ChildVApps = []string{"api-cache"}

	inv := &vsphere.Inventory{
		Context: "prod",
		VApps: []vsphere.VApp{
			testVApp("prod", "vapp-child", "api-cache"),
		},
	}

	children := childVApps(&parent, inv)
	if len(children) != 1 {
		t.Fatalf("expected exactly 1 child, got %d: %+v", len(children), children)
	}
	if children[0].name != "api-cache" || children[0].app == nil {
		t.Fatalf("expected resolved child api-cache, got %+v", children[0])
	}
	if children[0].key != "prod/vapp-child" {
		t.Fatalf("expected key prod/vapp-child, got %q", children[0].key)
	}
}

func TestChildVApps_LegacyOnly(t *testing.T) {
	parent := testVApp("prod", "vapp-parent", "api-stack")
	parent.ChildVApps = []string{"legacy-worker"}

	inv := &vsphere.Inventory{
		Context: "prod",
		VApps: []vsphere.VApp{
			testVApp("prod", "vapp-worker", "legacy-worker"),
		},
	}

	children := childVApps(&parent, inv)
	if len(children) != 1 {
		t.Fatalf("expected exactly 1 child, got %d: %+v", len(children), children)
	}
	if children[0].name != "legacy-worker" || children[0].app == nil {
		t.Fatalf("expected resolved legacy-worker, got %+v", children[0])
	}
	if children[0].key != "prod/vapp-worker" {
		t.Fatalf("expected key prod/vapp-worker, got %q", children[0].key)
	}
}

func TestChildVApps_GenuinelyMissing(t *testing.T) {
	parent := testVApp("prod", "vapp-parent", "api-stack")
	parent.ChildVApps = []string{"ghost-vapp"}

	inv := &vsphere.Inventory{
		Context: "prod",
		VApps:   []vsphere.VApp{},
	}

	children := childVApps(&parent, inv)
	if len(children) != 1 {
		t.Fatalf("expected 1 child for missing vapp, got %d: %+v", len(children), children)
	}
	if children[0].name != "ghost-vapp" {
		t.Fatalf("expected name ghost-vapp, got %q", children[0].name)
	}
	if children[0].app != nil {
		t.Fatalf("expected app to be nil for missing child, got %+v", children[0].app)
	}
	wantKey := "prod/missing-vapp:ghost-vapp"
	if children[0].key != wantKey {
		t.Fatalf("expected key %q, got %q", wantKey, children[0].key)
	}
}

func TestChildVApps_DeterministicMultiContext(t *testing.T) {
	// Inventory has two vApps named "db-tier" in different contexts,
	// ordered with ctx-b first.
	inv := &vsphere.Inventory{
		VApps: []vsphere.VApp{
			testVApp("ctx-b", "vapp-b", "db-tier"),
			testVApp("ctx-a", "vapp-a", "db-tier"),
		},
	}

	// Parent in ctx-a should match ctx-a's db-tier
	parentA := testVApp("ctx-a", "parent-a", "stack-a")
	parentA.ChildVApps = []string{"db-tier"}
	childrenA := childVApps(&parentA, inv)
	if len(childrenA) != 1 || childrenA[0].key != "ctx-a/vapp-a" {
		t.Fatalf("parent in ctx-a did not match ctx-a child: %+v", childrenA)
	}

	// Parent in ctx-b should match ctx-b's db-tier
	parentB := testVApp("ctx-b", "parent-b", "stack-b")
	parentB.ChildVApps = []string{"db-tier"}
	childrenB := childVApps(&parentB, inv)
	if len(childrenB) != 1 || childrenB[0].key != "ctx-b/vapp-b" {
		t.Fatalf("parent in ctx-b did not match ctx-b child: %+v", childrenB)
	}

	// Parent in ctx-a with ref to vapp-a and legacy name db-tier:
	// must not match ctx-b's db-tier during legacy pass.
	parentDup := testVApp("ctx-a", "parent-a", "stack-a")
	parentDup.ChildVAppRefs = []string{"VirtualApp:vapp-a"}
	parentDup.ChildVApps = []string{"db-tier"}
	childrenDup := childVApps(&parentDup, inv)
	if len(childrenDup) != 1 || childrenDup[0].key != "ctx-a/vapp-a" {
		t.Fatalf("multi-context ref+legacy duplicate child produced unwanted matches: %+v", childrenDup)
	}
}

func TestChildVApps_DuplicateLegacyEntries(t *testing.T) {
	parent := testVApp("prod", "vapp-parent", "api-stack")
	parent.ChildVApps = []string{"api-cache", "api-cache", "missing-one", "missing-one"}

	inv := &vsphere.Inventory{
		Context: "prod",
		VApps: []vsphere.VApp{
			testVApp("prod", "vapp-cache", "api-cache"),
		},
	}

	children := childVApps(&parent, inv)
	if len(children) != 2 {
		t.Fatalf("expected exactly 2 children (1 resolved, 1 missing), got %d: %+v", len(children), children)
	}
	if children[0].name != "api-cache" || children[0].app == nil {
		t.Fatalf("expected resolved api-cache, got %+v", children[0])
	}
	if children[1].name != "missing-one" || children[1].app != nil {
		t.Fatalf("expected missing child missing-one, got %+v", children[1])
	}
}

func TestVAppWorkspace_NoDuplicateResolvedChild(t *testing.T) {
	b := twoHealthy()
	inv := *b.inventories["prod"]
	inv.VMs = append(inv.VMs, vsphere.VM{
		Location: vsphere.Location{Context: "prod", Datacenter: "Taipei", Path: "/Taipei/vm/postgres-01"},
		ID:       "prod-vm-nested", Name: "postgres-01", PowerState: "poweredOn", CPU: 8, MemoryMB: 32768, Host: "esxi-02",
	})
	inv.VApps[0].ChildVApps = []string{"web-cache"}
	inv.VApps[0].ChildVAppRefs = []string{"VirtualApp:prod-vapp-2"}
	inv.VApps[0].ChildVAppCount = 1
	inv.VApps = append(inv.VApps, vsphere.VApp{
		Location: vsphere.Location{Context: "prod", Datacenter: "Taipei", Path: "/Taipei/host/web-cache"},
		ID:       "prod-vapp-2", Name: "web-cache", Status: "stopped", ParentVApp: "app-container",
		DirectVMCount: 1, DirectVMs: []string{"postgres-01"}, DirectVMRefs: []string{"VirtualMachine:prod-vm-nested"},
	})
	b.inventories["prod"] = &inv
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "7", "enter")

	if m.mode != modeVAppDetail {
		t.Fatalf("enter on a vAPP should open its workspace, mode=%v", m.mode)
	}

	out := m.View()
	// web-cache should only appear once in members (not as "not loaded")
	if strings.Contains(out, "not loaded") {
		t.Fatalf("workspace contains unexpected 'not loaded' member row:\n%s", out)
	}
	// Verify members count: web-cache (1) + postgres-01 (1) + app-01 (1)
	members := m.vappMembers(mustActiveVApp(t, m), m.byName["prod"].inv)
	cacheCount := 0
	for _, member := range members {
		if member.name == "web-cache" {
			cacheCount++
			if member.missing {
				t.Fatalf("web-cache member should not be marked missing: %+v", member)
			}
		}
	}
	if cacheCount != 1 {
		t.Fatalf("expected web-cache to appear exactly once in members, found %d times", cacheCount)
	}
}

// vappWithAllocation gives prod's vApp a second member, a CPU limit and a
// startup order that starts db-01 before app-01.
func vappWithAllocation(t *testing.T) *Model {
	t.Helper()
	b := twoHealthy()
	inv := *b.inventories["prod"]
	inv.VMs = append(inv.VMs, vsphere.VM{
		Location: vsphere.Location{Context: "prod", Datacenter: "Taipei", Path: "/Taipei/vm/db-01"},
		ID:       "prod-vm-db", Name: "db-01", PowerState: "poweredOff", CPU: 2, MemoryMB: 8192, Host: "esxi-02",
	})
	app := inv.VApps[0]
	app.DirectVMCount = 2
	app.DirectVMs = append(append([]string(nil), app.DirectVMs...), "db-01")
	app.DirectVMRefs = append(append([]string(nil), app.DirectVMRefs...), "VirtualMachine:prod-vm-db")
	limit, none, unlimited, reserved := int64(1000), int64(0), int64(-1), int64(128)
	app.Allocation = &vsphere.ResourceAllocation{
		CPULimitMHz: &limit, CPUReservationMHz: &none, CPUExpandable: true, CPULevel: "normal", CPUShares: 4000,
		MemLimitMB: &unlimited, MemReservationMB: &reserved, MemExpandable: true, MemLevel: "normal", MemShares: 163840,
	}
	app.StartOrder = []vsphere.VAppStartEntry{
		{Ref: "VirtualMachine:prod-vm-db", Name: "db-01", Order: 1, DelaySeconds: 10},
		{Ref: "VirtualMachine:prod-vm-1", Name: "app-01", Order: 2, DelaySeconds: 120},
	}
	inv.VApps = []vsphere.VApp{app}
	b.inventories["prod"] = &inv
	m := newTestModel(t, b, Options{Current: "prod"})
	press(t, m, "7", "enter")
	return m
}

func TestVAppWorkspaceShowsAllocationAndStartupOrder(t *testing.T) {
	m := vappWithAllocation(t)
	out := ansi.Strip(m.View())
	for _, want := range []string{
		"limit 1.0GHz · reservation none · shares normal · expandable",
		"limit unlimited · reservation 128M · shares normal · expandable",
		"1 db-01 (+10s) → 2 app-01",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q:\n%s", want, out)
		}
	}
	// Members follow the startup sequence, not the alphabet.
	dbLine, db := lineWith(out, "db-01   ")
	appLine, app := lineWith(out, "app-01  ")
	if dbLine < 0 || appLine < 0 || dbLine > appLine {
		t.Fatalf("db-01 (start 1) should be listed before app-01 (start 2):\n%s", out)
	}
	if !strings.Contains(db, " 1 ") || !strings.Contains(app, " 2 ") {
		t.Errorf("START column missing: db=%q app=%q", db, app)
	}
}

func TestVAppWorkspaceSaysAllocationWasNotRecorded(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "7", "enter")
	_, line := lineWith(m.View(), "Allocation")
	if !strings.Contains(line, "not recorded") {
		t.Fatalf("a vApp without allocation should say so, got %q", line)
	}
}
