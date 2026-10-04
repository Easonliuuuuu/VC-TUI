package vsphere

import (
	"testing"

	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/config"
)

func TestNewVAppKeepsDirectAndNestedMembershipDistinct(t *testing.T) {
	dc := types.ManagedObjectReference{Type: "Datacenter", Value: "dc-1"}
	cr := types.ManagedObjectReference{Type: "ClusterComputeResource", Value: "domain-c1"}
	root := types.ManagedObjectReference{Type: "ResourcePool", Value: "resgroup-1"}
	app := types.ManagedObjectReference{Type: "VirtualApp", Value: "vapp-outer"}
	nested := types.ManagedObjectReference{Type: "VirtualApp", Value: "vapp-nested"}
	pool := types.ManagedObjectReference{Type: "ResourcePool", Value: "resgroup-child"}
	vm := types.ManagedObjectReference{Type: "VirtualMachine", Value: "vm-1"}

	idx := &index{byRef: map[types.ManagedObjectReference]entity{
		dc:     {ref: dc, name: "DC0"},
		cr:     {ref: cr, name: "compute-a", parent: &dc},
		root:   {ref: root, name: "Resources", parent: &cr},
		app:    {ref: app, name: "web-stack", parent: &root},
		nested: {ref: nested, name: "web-cache", parent: &app},
		pool:   {ref: pool, name: "web-pool", parent: &app},
		vm:     {ref: vm, name: "api-01", parent: &app},
	}}
	c := &Client{Context: &config.Context{Name: "prod"}}
	raw := &mo.VirtualApp{}
	raw.Self = app
	raw.Name = "web-stack"
	raw.Parent = &root
	raw.Vm = []types.ManagedObjectReference{vm, vm}
	raw.ResourcePool.ResourcePool = []types.ManagedObjectReference{nested, pool, nested}
	raw.ChildLink = []types.VirtualAppLinkInfo{{Key: nested}, {Key: pool}}
	raw.Summary = &types.VirtualAppSummary{VAppState: types.VirtualAppVAppStateStarted}

	got := newVApp(c, idx, raw)
	if got.Status != "started" || got.Datacenter != "DC0" || got.Cluster != "compute-a" || got.ComputeResource != "compute-a" {
		t.Fatalf("vApp identity/status/placement = %+v", got)
	}
	if got.DirectVMCount != 1 || len(got.DirectVMs) != 1 || got.DirectVMs[0] != "api-01" {
		t.Fatalf("direct VM membership = %+v", got)
	}
	if got.ChildVAppCount != 1 || len(got.ChildVApps) != 1 || got.ChildVApps[0] != "web-cache" {
		t.Fatalf("nested vApp membership = %+v", got)
	}
	if len(got.ChildVAppRefs) != 1 || got.ChildVAppRefs[0] != "VirtualApp:vapp-nested" {
		t.Fatalf("nested vApp references = %+v", got.ChildVAppRefs)
	}
	if got.ChildResourcePoolCount != 1 || len(got.ChildResourcePools) != 1 || got.ChildResourcePools[0] != "web-pool" {
		t.Fatalf("resource-pool membership = %+v", got)
	}
	if len(got.ChildResourcePoolRefs) != 1 || got.ChildResourcePoolRefs[0] != "ResourcePool:resgroup-child" {
		t.Fatalf("resource-pool references = %+v", got.ChildResourcePoolRefs)
	}
}

func TestNewVAppReadsAllocationAndStartOrder(t *testing.T) {
	app := types.ManagedObjectReference{Type: "VirtualApp", Value: "vapp-1"}
	db := types.ManagedObjectReference{Type: "VirtualMachine", Value: "vm-db"}
	web := types.ManagedObjectReference{Type: "VirtualMachine", Value: "vm-web"}
	idx := &index{byRef: map[types.ManagedObjectReference]entity{
		app: {ref: app, name: "shop"},
		db:  {ref: db, name: "shop-db"},
		web: {ref: web, name: "shop-web"},
	}}
	limit, none, unlimited, reserved := int64(1000), int64(0), int64(-1), int64(128)
	yes, no := true, false
	raw := &mo.VirtualApp{}
	raw.Self, raw.Name = app, "shop"
	raw.OverallStatus, raw.ConfigStatus = types.ManagedEntityStatusGreen, types.ManagedEntityStatusGray
	raw.Config.CpuAllocation = types.ResourceAllocationInfo{Limit: &limit, Reservation: &none, ExpandableReservation: &yes, Shares: &types.SharesInfo{Level: types.SharesLevelNormal, Shares: 4000}}
	raw.Config.MemoryAllocation = types.ResourceAllocationInfo{Limit: &unlimited, Reservation: &reserved, ExpandableReservation: &no, Shares: &types.SharesInfo{Level: types.SharesLevelHigh, Shares: 327680}}
	// A vApp's summary is a VirtualAppSummary, not a ResourcePoolSummary.
	raw.Summary = &types.VirtualAppSummary{ResourcePoolSummary: types.ResourcePoolSummary{ConfiguredMemoryMB: 512}}
	raw.VAppConfig = &types.VAppConfigInfo{EntityConfig: []types.VAppEntityConfigInfo{
		{Key: &web, Tag: "web-tag", StartOrder: 2, StartDelay: 120, StartAction: "powerOn", StopAction: "guestShutdown", StopDelay: 30},
		{Key: &db, Tag: "db-tag", StartOrder: 1, StartDelay: 10, StartAction: "powerOn", StopAction: "powerOff", WaitingForGuest: &yes},
		{Tag: "orphan", StartOrder: 3},
	}}

	got := newVApp(&Client{Context: &config.Context{Name: "prod"}}, idx, raw)
	if got.OverallStatus != "green" || got.ConfigStatus != "gray" {
		t.Fatalf("status colours = %q/%q", got.OverallStatus, got.ConfigStatus)
	}
	a := got.Allocation
	if a == nil {
		t.Fatal("allocation not read")
	}
	if *a.CPULimitMHz != 1000 || *a.CPUReservationMHz != 0 || !a.CPUExpandable || a.CPULevel != "normal" || a.CPUShares != 4000 {
		t.Errorf("CPU allocation = %+v", a)
	}
	if *a.MemLimitMB != -1 || *a.MemReservationMB != 128 || a.MemExpandable || a.MemLevel != "high" || a.MemConfiguredMB != 512 {
		t.Errorf("memory allocation = %+v", a)
	}
	want := []VAppStartEntry{
		{Ref: "VirtualMachine:vm-db", Name: "shop-db", Order: 1, DelaySeconds: 10, StartAction: "powerOn", StopAction: "powerOff", WaitForGuest: true},
		{Ref: "VirtualMachine:vm-web", Name: "shop-web", Order: 2, DelaySeconds: 120, StartAction: "powerOn", StopAction: "guestShutdown", StopDelaySeconds: 30},
		{Name: "orphan", Order: 3},
	}
	if len(got.StartOrder) != len(want) {
		t.Fatalf("start order = %+v", got.StartOrder)
	}
	for i := range want {
		if got.StartOrder[i] != want[i] {
			t.Errorf("start order[%d] = %+v, want %+v", i, got.StartOrder[i], want[i])
		}
	}
	if text := StartOrderText(got.StartOrder); text != "1 shop-db (+10s) → 2 shop-web (+120s) → 3 orphan" {
		t.Errorf("StartOrderText = %q", text)
	}
}

func TestStartOrderTextGroupsMembersThatStartTogether(t *testing.T) {
	got := StartOrderText([]VAppStartEntry{
		{Name: "db", Order: 1, DelaySeconds: 60},
		{Name: "api", Order: 2, DelaySeconds: 120},
		{Name: "web", Order: 2, DelaySeconds: 120},
	})
	if want := "1 db (+60s) → 2 api, web"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if StartOrderText(nil) != "" {
		t.Fatal("no entries should render nothing")
	}
}
