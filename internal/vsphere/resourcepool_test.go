package vsphere_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vmware/govmomi/simulator"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func TestFetchResourcePoolsIncludesRootsAndExcludesVApps(t *testing.T) {
	c, _ := newSimulator(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Cluster = 1
		m.ClusterHost = 2
		m.Pool = 2
		m.App = 1
		m.Machine = 2
	})
	idx, err := c.NewIndex(context.Background())
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}

	inv := c.FetchGroup(context.Background(), idx, vsphere.GroupResourcePools)
	if len(inv.Errors) != 0 {
		t.Fatalf("FetchGroup resourcepools: %v", inv.Errors)
	}
	if len(inv.ResourcePools) == 0 {
		t.Fatal("expected resource pools")
	}

	var roots, children int
	for _, pool := range inv.ResourcePools {
		if strings.Contains(pool.ID, "vapp") || strings.Contains(pool.Name, "vApp") {
			t.Errorf("virtual app returned as resource pool: %+v", pool)
		}
		if pool.Root {
			roots++
			if pool.Name != "Resources" {
				t.Errorf("root pool name=%q, want Resources", pool.Name)
			}
		} else {
			children++
		}
		if pool.Path == "" {
			t.Errorf("pool %q has no inventory path", pool.Name)
		}
		if pool.Owner == "" {
			t.Errorf("pool %q has no owner", pool.Name)
		}
		if len(pool.VMRefs) == 0 {
			continue
		}
		if pool.CPUReservationMHz == nil || pool.CPULimitMHz == nil || pool.MemReservationMB == nil || pool.MemLimitMB == nil {
			t.Errorf("pool %q allocation fields are unexpectedly nil: %+v", pool.Name, pool)
		}
	}
	if roots == 0 {
		t.Fatal("expected cluster root Resources pool")
	}
	if children == 0 {
		t.Fatal("expected child resource pools")
	}
}

func TestResourcePoolNilAllocationsAreSafe(t *testing.T) {
	c, _ := newSimulator(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Cluster = 1
		m.ClusterHost = 1
		m.Pool = 1
		m.Machine = 1
	})
	idx, err := c.NewIndex(context.Background())
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	inv := c.FetchGroup(context.Background(), idx, vsphere.GroupResourcePools)
	if len(inv.Errors) != 0 {
		t.Fatalf("FetchGroup resourcepools: %v", inv.Errors)
	}
	// The simulator can omit allocation pointers for a pool. The fetch must
	// still return a usable record instead of dereferencing a nil pointer.
	for _, pool := range inv.ResourcePools {
		_ = pool.CPUReservationMHz
		_ = pool.CPULimitMHz
		_ = pool.MemReservationMB
		_ = pool.MemLimitMB
	}
}

// The simulator reports no startup order, but a vApp's allocation comes from
// the same config property as a pool's, and the narrowed vAppConfig path must
// still decode.
func TestFetchVAppsReadsAllocation(t *testing.T) {
	c, _ := newSimulator(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Cluster = 1
		m.ClusterHost = 1
		m.App = 1
		m.Machine = 1
	})
	idx, err := c.NewIndex(context.Background())
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	inv := c.FetchGroup(context.Background(), idx, vsphere.GroupVApps)
	if len(inv.Errors) != 0 {
		t.Fatalf("FetchGroup vapps: %v", inv.Errors)
	}
	if len(inv.VApps) == 0 {
		t.Fatal("expected a vApp")
	}
	for _, v := range inv.VApps {
		if v.Allocation == nil || v.Allocation.CPULimitMHz == nil || v.Allocation.CPULevel == "" {
			t.Errorf("vApp %q allocation not read: %+v", v.Name, v.Allocation)
		}
		if v.OverallStatus == "" {
			t.Errorf("vApp %q has no overall status", v.Name)
		}
	}
}
