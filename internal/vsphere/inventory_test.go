package vsphere_test

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// newSimulator starts a vcsim vCenter and returns a client wired to it,
// along with the fault injector for its underlying service — used by tests
// that need one specific call to misbehave without the rest of the
// simulator noticing.
func newSimulator(t *testing.T, tune func(*simulator.Model)) (*vsphere.Client, *simulator.FaultInjector) {
	t.Helper()
	model := simulator.VPX()
	if tune != nil {
		tune(model)
	}
	if err := model.Create(); err != nil {
		t.Fatalf("create simulator model: %v", err)
	}
	t.Cleanup(model.Remove)

	gc, endpoint := dialSimulator(t, model)
	return clientFor(gc, endpoint, ""), model.Service.FaultInjector()
}

// dialSimulator starts model's server and connects to it, without wrapping
// the connection in a vsphere.Client — for a test that needs more than one
// Client, at different Context.Datacenter scopes, against the same running
// simulator.
func dialSimulator(t *testing.T, model *simulator.Model) (*govmomi.Client, string) {
	t.Helper()
	server := model.Service.NewServer()
	t.Cleanup(server.Close)

	ctx := context.Background()
	gc, err := govmomi.NewClient(ctx, server.URL, true)
	if err != nil {
		t.Fatalf("connect to simulator: %v", err)
	}
	t.Cleanup(func() { _ = gc.Logout(context.Background()) })
	return gc, server.URL.String()
}

// clientFor wraps an existing simulator connection in a vsphere.Client
// scoped to datacenter ("" for the whole vCenter).
func clientFor(gc *govmomi.Client, endpoint, datacenter string) *vsphere.Client {
	cc := &config.Context{
		Name: "sim", Endpoint: endpoint, Username: "user", Datacenter: datacenter,
		TLS: config.TLSConfig{Mode: config.TLSInsecure},
	}
	cc.Normalize()
	return vsphere.NewClientForTest(cc, gc)
}

func TestListInventory(t *testing.T) {
	c, _ := newSimulator(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Cluster = 2
		m.ClusterHost = 3
		m.Machine = 4
		m.Datastore = 2
	})
	ctx := context.Background()

	inv, err := c.ListInventory(ctx)
	if err != nil {
		t.Fatalf("ListInventory: %v", err)
	}
	t.Logf("inventory: %s", inv.Counts())

	if len(inv.VMs) == 0 {
		t.Fatal("expected virtual machines")
	}
	if len(inv.Hosts) == 0 {
		t.Fatal("expected hosts")
	}
	if len(inv.Clusters) == 0 {
		t.Fatal("expected clusters")
	}
	if len(inv.Datastores) == 0 {
		t.Fatal("expected datastores")
	}
	if len(inv.Networks) == 0 {
		t.Fatal("expected networks")
	}

	vm := inv.VMs[0]
	t.Logf("vm: %+v", vm)
	if vm.Name == "" || vm.PowerState == "" {
		t.Errorf("vm is missing basic properties: %+v", vm)
	}
	if vm.Datacenter == "" {
		t.Errorf("vm %s has no datacenter", vm.Name)
	}
	if vm.Host == "" {
		t.Errorf("vm %s has no host", vm.Name)
	}
	if vm.Cluster == "" {
		t.Errorf("vm %s has no cluster", vm.Name)
	}
	if vm.CPU == 0 || vm.MemoryMB == 0 {
		t.Errorf("vm %s has no hardware: cpu=%d mem=%d", vm.Name, vm.CPU, vm.MemoryMB)
	}
	if vm.Path == "" {
		t.Errorf("vm %s has no inventory path", vm.Name)
	}

	host := inv.Hosts[0]
	t.Logf("host: %+v", host)
	if host.Cluster == "" {
		t.Errorf("host %s has no cluster", host.Name)
	}
	if host.CPUCores == 0 || host.MemoryMB == 0 {
		t.Errorf("host %s has no hardware summary: %+v", host.Name, host)
	}

	cl := inv.Clusters[0]
	t.Logf("cluster: %+v", cl)
	ds := inv.Datastores[0]
	t.Logf("datastore: %+v", ds)
	if ds.CapacityBytes == 0 {
		t.Errorf("datastore %s has no capacity", ds.Name)
	}
	t.Logf("network: %+v", inv.Networks[0])
}

func TestListVMsCapturesConnectionState(t *testing.T) {
	c, _ := newSimulator(t, nil)
	vms, err := c.ListVMs(context.Background())
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(vms) == 0 {
		t.Fatal("simulator produced no virtual machines")
	}
	if got := vms[0].ConnectionState; got != "connected" {
		t.Fatalf("VM connection state=%q, want connected", got)
	}
}

func TestListVApps(t *testing.T) {
	c, _ := newSimulator(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Cluster = 1
		m.ClusterHost = 1
		m.App = 1
		m.Machine = 2
	})

	vapps, err := c.ListVApps(context.Background())
	if err != nil {
		t.Fatalf("ListVApps: %v", err)
	}
	if len(vapps) != 1 {
		t.Fatalf("ListVApps returned %d vApps, want 1: %+v", len(vapps), vapps)
	}
	v := vapps[0]
	if v.Name == "" || v.ID == "" || v.Datacenter != "DC0" || v.Path == "" {
		t.Fatalf("vApp is missing location identity: %+v", v)
	}
	if v.Status == "" {
		t.Errorf("vApp status is empty: %+v", v)
	}
	if v.DirectVMCount != len(v.DirectVMs) || v.DirectVMCount == 0 {
		t.Errorf("direct VM membership = %d/%v, want non-empty and consistent", v.DirectVMCount, v.DirectVMs)
	}
	if v.Cluster == "" || v.ComputeResource == "" {
		t.Errorf("vApp placement is unresolved: %+v", v)
	}
}

// markTemplate powers off one VM and converts it to a template, so the split
// between ListVMs and ListTemplates is exercised rather than assumed.
func markTemplate(t *testing.T, c *vsphere.Client, name string) {
	t.Helper()
	ctx := context.Background()
	finder := find.NewFinder(c.VIM(), false)
	dc, err := finder.DefaultDatacenter(ctx)
	if err != nil {
		t.Fatalf("find default datacenter: %v", err)
	}
	finder.SetDatacenter(dc)
	vm, err := finder.VirtualMachine(ctx, name)
	if err != nil {
		t.Fatalf("find vm %s: %v", name, err)
	}
	task, err := vm.PowerOff(ctx)
	if err != nil {
		t.Fatalf("power off %s: %v", name, err)
	}
	if err := task.Wait(ctx); err != nil {
		t.Fatalf("power off %s: %v", name, err)
	}
	if err := vm.MarkAsTemplate(ctx); err != nil {
		t.Fatalf("mark %s as template: %v", name, err)
	}
}

func TestListTemplates(t *testing.T) {
	c, _ := newSimulator(t, nil)
	ctx := context.Background()

	before, err := c.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("simulator produced no virtual machines")
	}
	markTemplate(t, c, before[0].Name)

	vms, err := c.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	for _, vm := range vms {
		if vm.IsTemplate {
			t.Errorf("ListVMs returned template %s", vm.Name)
		}
	}
	tmpl, err := c.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	for _, vm := range tmpl {
		if !vm.IsTemplate {
			t.Errorf("ListTemplates returned non-template %s", vm.Name)
		}
	}
	if len(tmpl) != 1 {
		t.Fatalf("expected exactly 1 template, got %d", len(tmpl))
	}
	if tmpl[0].Name != before[0].Name {
		t.Errorf("template is %q, want %q", tmpl[0].Name, before[0].Name)
	}
	if len(vms) != len(before)-1 {
		t.Errorf("ListVMs returned %d vms, want %d", len(vms), len(before)-1)
	}
}

// TestListInventoryIsPartialOnOneKindFailing proves the core claim behind
// the "limited-permission accounts" work: a context that can list most
// resource kinds but is denied on one still returns everything it could
// read, rather than the whole thing coming back empty.
//
// vcsim has no real privilege enforcement — AuthorizationManager stores
// permissions but nothing checks them against other calls — so this reaches
// for the fault injector instead: SkipCount/MaxCount target one specific
// RetrievePropertiesEx call by its position in the sequence ListInventory
// always makes in the same order (the path index, then VMs, hosts,
// clusters, datastores, networks). The skip count below (7, not the 4 that
// sequence alone would suggest) is calibrated against what the test
// actually observes rather than derived from that list — some other
// RetrievePropertiesEx call happens first, and which one does not change
// the point of the test — so if ListInventory's own call sequence ever
// changes, expect this to need recalibrating the same way: run it, see
// which kind actually got denied, adjust.
func TestListInventoryIsPartialOnOneKindFailing(t *testing.T) {
	c, faults := newSimulator(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Cluster = 1
		m.ClusterHost = 2
		m.Machine = 2
		m.Datastore = 2
	})
	faults.AddRule(&simulator.FaultInjectionRule{
		MethodName:  "RetrievePropertiesEx",
		ObjectType:  "*",
		ObjectName:  "*",
		Probability: 1,
		SkipCount:   7,
		MaxCount:    1,
		Enabled:     true,
		FaultType:   simulator.FaultTypeNoPermission,
		Message:     "permission denied: datastores",
	})

	inv, err := c.ListInventory(context.Background())
	if err != nil {
		t.Fatalf("ListInventory returned a top-level error instead of a partial result: %v", err)
	}

	if len(inv.VMs) == 0 {
		t.Error("VMs came back empty despite only datastores being denied")
	}
	if len(inv.Hosts) == 0 {
		t.Error("hosts came back empty despite only datastores being denied")
	}
	if len(inv.Clusters) == 0 {
		t.Error("clusters came back empty despite only datastores being denied")
	}
	if len(inv.Networks) == 0 {
		t.Error("networks (listed after datastores) came back empty despite only datastores being denied")
	}
	if len(inv.Datastores) != 0 {
		t.Errorf("datastores = %v, want none — the denied kind should not have partial results either", inv.Datastores)
	}

	msg, ok := inv.ErrorFor(vsphere.KindDatastore)
	if !ok {
		t.Fatalf("Errors does not record a failure for datastores: %+v", inv.Errors)
	}
	if !strings.Contains(msg, "permission denied") {
		t.Errorf("datastore error = %q, want it to mention the injected fault", msg)
	}
	for _, kind := range []vsphere.Kind{vsphere.KindVM, vsphere.KindTemplate, vsphere.KindHost, vsphere.KindCluster, vsphere.KindNetwork} {
		if _, failed := inv.ErrorFor(kind); failed {
			t.Errorf("Errors records a failure for %s, which was never denied", kind)
		}
	}
}

// TestListInventoryReportsStages covers the progress side of issue #28: a
// caller that attaches a stage reporter via WithStageReporter sees each
// phase of enumeration as ListInventory reaches it, in order, which is what
// lets a caller show live status or name what was in flight when a deadline
// cut the operation off.
func TestListInventoryReportsStages(t *testing.T) {
	c, _ := newSimulator(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Cluster = 1
		m.ClusterHost = 1
		m.Machine = 1
		m.Datastore = 1
	})

	var mu sync.Mutex
	var stages []vsphere.Stage
	ctx := vsphere.WithStageReporter(context.Background(), func(s vsphere.Stage) {
		mu.Lock()
		defer mu.Unlock()
		stages = append(stages, s)
	})

	if _, err := c.ListInventory(ctx); err != nil {
		t.Fatalf("ListInventory: %v", err)
	}

	want := []vsphere.Stage{
		vsphere.StageLoadingIndex,
		vsphere.StageLoadingVMs,
		vsphere.StageLoadingHosts,
		vsphere.StageLoadingClusters,
		vsphere.StageLoadingVApps,
		vsphere.StageLoadingDatastores,
		vsphere.StageLoadingNetworks,
	}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
}

// TestStageTrackerReflectsListInventoryProgress covers the other half of the
// same mechanism: a StageTracker, which is what a timed-out operation's error
// message names the last stage from, ends up holding the final stage
// ListInventory reached.
func TestStageTrackerReflectsListInventoryProgress(t *testing.T) {
	c, _ := newSimulator(t, nil)
	tracker := &vsphere.StageTracker{}
	ctx := vsphere.WithStageReporter(context.Background(), tracker.Report)

	if _, err := c.ListInventory(ctx); err != nil {
		t.Fatalf("ListInventory: %v", err)
	}
	if got := tracker.Current(); got != vsphere.StageLoadingNetworks {
		t.Fatalf("tracker.Current() = %q, want %q", got, vsphere.StageLoadingNetworks)
	}
}

// twoDatacenterModel builds a vcsim model with two independent datacenters,
// DC0 and DC1, each with its own cluster, host, VM and datastore — vcsim
// names everything under a datacenter with that datacenter's name as a
// prefix (DC0_H0, DC0_H0_VM0, ...), which is what the tests below use to
// tell one datacenter's objects apart from the other's.
func twoDatacenterModel(t *testing.T) *simulator.Model {
	t.Helper()
	model := simulator.VPX()
	model.Datacenter = 2
	model.Cluster = 1
	model.ClusterHost = 1
	model.App = 1
	model.Machine = 1
	model.Datastore = 1
	if err := model.Create(); err != nil {
		t.Fatalf("create simulator model: %v", err)
	}
	t.Cleanup(model.Remove)
	return model
}

// TestListInventoryScopesToConfiguredDatacenter covers issue #29's
// datacenter-scoping requirement: a context with Context.Datacenter set only
// ever sees that datacenter's objects, across every resource kind, and
// nothing from the other one configured in the same vCenter.
func TestListInventoryScopesToConfiguredDatacenter(t *testing.T) {
	model := twoDatacenterModel(t)
	gc, endpoint := dialSimulator(t, model)
	c := clientFor(gc, endpoint, "DC0")

	inv, err := c.ListInventory(context.Background())
	if err != nil {
		t.Fatalf("ListInventory: %v", err)
	}
	if len(inv.VMs) == 0 || len(inv.Hosts) == 0 || len(inv.Clusters) == 0 || len(inv.VApps) == 0 ||
		len(inv.Datastores) == 0 || len(inv.Networks) == 0 {
		t.Fatalf("expected every kind to have at least one DC0 object, got %s", inv.Counts())
	}

	check := func(kind string, names []string) {
		t.Helper()
		for _, n := range names {
			if strings.HasPrefix(n, "DC1") {
				t.Errorf("%s leaked a DC1 object into a DC0-scoped inventory: %q", kind, n)
			}
		}
	}
	var vmNames, hostNames, clusterNames, vappNames, dsNames, netNames []string
	for _, vm := range inv.VMs {
		vmNames = append(vmNames, vm.Name)
		if vm.Datacenter != "DC0" {
			t.Errorf("vm %s has datacenter %q, want DC0", vm.Name, vm.Datacenter)
		}
	}
	for _, h := range inv.Hosts {
		hostNames = append(hostNames, h.Name)
	}
	for _, cl := range inv.Clusters {
		clusterNames = append(clusterNames, cl.Name)
	}
	for _, v := range inv.VApps {
		vappNames = append(vappNames, v.Name)
		if v.Datacenter != "DC0" {
			t.Errorf("vApp %s has datacenter %q, want DC0", v.Name, v.Datacenter)
		}
	}
	for _, ds := range inv.Datastores {
		dsNames = append(dsNames, ds.Name)
	}
	for _, n := range inv.Networks {
		netNames = append(netNames, n.Name)
	}
	check("vm", vmNames)
	check("host", hostNames)
	check("cluster", clusterNames)
	check("vapp", vappNames)
	check("datastore", dsNames)
	check("network", netNames)

	// The single-kind listers thread the same scoping through idx.root, not
	// only ListInventory's own path — "vsfleet host list" must be scoped the
	// same way "vsfleet ui" is.
	hosts, err := c.ListHosts(context.Background())
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	for _, h := range hosts {
		if strings.HasPrefix(h.Name, "DC1") {
			t.Errorf("ListHosts leaked a DC1 host into a DC0-scoped client: %q", h.Name)
		}
	}
}

// TestListInventoryUnknownDatacenterFails checks the other acceptance
// criterion: a missing or misspelled datacenter is an explicit configuration
// error, never a silent fall back to the vCenter-wide root.
func TestListInventoryUnknownDatacenterFails(t *testing.T) {
	model := twoDatacenterModel(t)
	gc, endpoint := dialSimulator(t, model)
	c := clientFor(gc, endpoint, "does-not-exist")

	_, err := c.ListInventory(context.Background())
	if err == nil {
		t.Fatal("ListInventory should have failed: the configured datacenter does not exist")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should name the missing datacenter, got: %v", err)
	}
}

// TestListInventoryAmbiguousDatacenterFails covers the other configuration
// error the issue calls for: vSphere only requires a datacenter's name to be
// unique within its parent folder, not across the whole vCenter, so two
// datacenters can legitimately share a name. Resolving one by name alone
// must fail rather than silently pick one of them.
func TestListInventoryAmbiguousDatacenterFails(t *testing.T) {
	model := twoDatacenterModel(t)
	gc, endpoint := dialSimulator(t, model)
	ctx := context.Background()

	root := object.NewRootFolder(gc.Client)
	extra, err := root.CreateFolder(ctx, "Extra")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	if _, err := extra.CreateDatacenter(ctx, "DC0"); err != nil {
		t.Fatalf("create duplicate datacenter: %v", err)
	}

	c := clientFor(gc, endpoint, "DC0")
	if _, err := c.ListInventory(ctx); err == nil {
		t.Fatal("ListInventory should have failed: two datacenters share the name DC0")
	} else if !strings.Contains(err.Error(), "DC0") || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should say the datacenter reference is ambiguous and name it, got: %v", err)
	}
}
