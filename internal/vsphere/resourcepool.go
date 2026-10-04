package vsphere

import (
	"context"
	"sort"

	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

var resourcePoolProps = []string{"name", "parent", "owner", "vm", "config", "summary", "overallStatus", "configStatus", "customValue"}

// ListResourcePools returns resource pool configuration records. It is kept
// separate from ListInventory because resource pools are capture-only for
// the browsable inventory (see KindResourcePool), even though they are their
// own scriptable "resourcepool list" command.
func (c *Client) ListResourcePools(ctx context.Context) ([]ResourcePool, error) {
	idx, err := newIndex(ctx, c)
	if err != nil {
		return nil, err
	}
	return c.listResourcePools(ctx, idx)
}

func (c *Client) listResourcePools(ctx context.Context, idx *index) ([]ResourcePool, error) {
	var raw []mo.ResourcePool
	if err := retrieve(ctx, c, idx.root, []string{"ResourcePool"}, []string{"ResourcePool"}, resourcePoolProps, &raw); err != nil {
		return nil, err
	}

	out := make([]ResourcePool, 0, len(raw))
	refs := make([]types.ManagedObjectReference, 0, len(raw))
	values := make(map[types.ManagedObjectReference][]types.BaseCustomFieldValue, len(raw))
	for i := range raw {
		refs = append(refs, raw[i].Self)
		values[raw[i].Self] = raw[i].CustomValue
	}
	metadata := c.collectMetadata(ctx, refs, values)
	for i := range raw {
		m := &raw[i]
		// A ContainerView of ResourcePool also returns VirtualApp, which is a
		// subtype. vsfleet already models vApps as their own kind.
		if m.Self.Type != "ResourcePool" {
			continue
		}
		pool := newResourcePool(c, idx, m)
		pool.Metadata = metadata[m.Self]
		out = append(out, pool)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func newResourcePool(c *Client, idx *index, m *mo.ResourcePool) ResourcePool {
	parent := m.Parent
	root := parent == nil || parent.Type != "ResourcePool"
	pool := ResourcePool{
		Location:           idx.locate(c, m.Self, m.Name),
		ID:                 m.Self.Value,
		Name:               m.Name,
		Root:               root,
		Parent:             idx.name(parent),
		Owner:              idx.name(&m.Owner),
		Status:             string(m.OverallStatus),
		ConfigStatus:       string(m.ConfigStatus),
		ResourceAllocation: newResourceAllocation(m.Config, m.Summary),
	}
	pool.VMRefs = make([]string, 0, len(m.Vm))
	for _, ref := range m.Vm {
		if ref.Value != "" {
			pool.VMRefs = append(pool.VMRefs, ref.Value)
		}
	}
	sort.Strings(pool.VMRefs)
	return pool
}

// newResourceAllocation reads a pool's or vApp's allocation. A vApp's summary
// is a VirtualAppSummary, which embeds the ResourcePoolSummary that carries
// the configured memory.
func newResourceAllocation(config types.ResourceConfigSpec, summary types.BaseResourcePoolSummary) ResourceAllocation {
	cpu, mem := config.CpuAllocation, config.MemoryAllocation
	return ResourceAllocation{
		CPUReservationMHz:   cpu.Reservation,
		CPULimitMHz:         cpu.Limit,
		CPUOverheadLimitMHz: cpu.OverheadLimit,
		CPUExpandable:       cpu.ExpandableReservation != nil && *cpu.ExpandableReservation,
		CPUShares:           sharesValue(cpu.Shares),
		CPULevel:            string(sharesLevel(cpu.Shares)),
		MemConfiguredMB:     resourcePoolConfiguredMemory(summary),
		MemReservationMB:    mem.Reservation,
		MemLimitMB:          mem.Limit,
		MemOverheadLimitMB:  mem.OverheadLimit,
		MemExpandable:       mem.ExpandableReservation != nil && *mem.ExpandableReservation,
		MemShares:           sharesValue(mem.Shares),
		MemLevel:            string(sharesLevel(mem.Shares)),
	}
}

func resourcePoolConfiguredMemory(summary types.BaseResourcePoolSummary) int64 {
	switch v := summary.(type) {
	case *types.ResourcePoolSummary:
		if v != nil {
			return int64(v.ConfiguredMemoryMB)
		}
	case *types.VirtualAppSummary:
		if v != nil {
			return int64(v.ConfiguredMemoryMB)
		}
	}
	return 0
}

func sharesValue(shares *types.SharesInfo) int32 {
	if shares == nil {
		return 0
	}
	return shares.Shares
}

func sharesLevel(shares *types.SharesInfo) types.SharesLevel {
	if shares == nil {
		return ""
	}
	return shares.Level
}
