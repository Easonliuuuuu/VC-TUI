package vsphere

import (
	"context"
	"sort"

	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

var hostProps = []string{
	"customValue",
	"name",
	"parent",
	"runtime.powerState",
	"runtime.connectionState",
	"runtime.inMaintenanceMode",
	"summary.hardware",
	"summary.quickStats",
	"summary.config.product",
	"vm",
	"datastore",
}

var hostConfigProps = []string{"config.storageDevice", "config.network"}

// ListHosts returns the ESXi hosts in a vCenter.
func (c *Client) ListHosts(ctx context.Context) ([]Host, error) {
	idx, err := newIndex(ctx, c)
	if err != nil {
		return nil, err
	}
	return c.listHosts(ctx, idx)
}

func (c *Client) listHosts(ctx context.Context, idx *index) ([]Host, error) {
	return c.listHostsWith(ctx, idx, false)
}

// ListHostsWithConfig returns the ESXi hosts in a vCenter together with the
// storage and network subresources (HBAs, pNICs, standard switches, port
// groups, VMkernel adapters, multipath LUNs) config.storageDevice and
// config.network carry. It costs more per host than ListHosts, so callers
// that only need summary fields should use that instead.
func (c *Client) ListHostsWithConfig(ctx context.Context) ([]Host, error) {
	idx, err := newIndex(ctx, c)
	if err != nil {
		return nil, err
	}
	return c.listHostsWith(ctx, idx, true)
}

func (c *Client) listHostsWith(ctx context.Context, idx *index, withConfig bool) ([]Host, error) {
	var raw []mo.HostSystem
	props := hostProps
	if withConfig {
		props = append(append([]string(nil), props...), hostConfigProps...)
	}
	if err := retrieve(ctx, c, idx.root, []string{"HostSystem"}, []string{"HostSystem"}, props, &raw); err != nil {
		return nil, err
	}
	out := make([]Host, 0, len(raw))
	refs := make([]types.ManagedObjectReference, 0, len(raw))
	values := make(map[types.ManagedObjectReference][]types.BaseCustomFieldValue, len(raw))
	for i := range raw {
		refs = append(refs, raw[i].Self)
		values[raw[i].Self] = raw[i].CustomValue
	}
	metadata := c.collectMetadata(ctx, refs, values)
	for i := range raw {
		host := newHostWithConfig(c, idx, &raw[i], withConfig)
		host.Metadata = metadata[raw[i].Self]
		out = append(out, host)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func newHostWithConfig(c *Client, idx *index, m *mo.HostSystem, withConfig bool) Host {
	h := Host{
		Location:        idx.locate(c, m.Self, m.Name),
		ID:              m.Self.Value,
		Name:            m.Name,
		PowerState:      string(m.Runtime.PowerState),
		ConnectionState: string(m.Runtime.ConnectionState),
		VMCount:         len(m.Vm),
	}
	if len(m.Datastore) > 0 {
		h.Datastores = idx.names(m.Datastore)
	}
	if m.Runtime.InMaintenanceMode {
		h.InMaintenance = true
	}
	if m.Parent != nil && m.Parent.Type == "ClusterComputeResource" {
		h.Cluster = idx.name(m.Parent)
	}
	if hw := m.Summary.Hardware; hw != nil {
		h.Vendor = hw.Vendor
		h.Model = hw.Model
		h.CPUCores = int32(hw.NumCpuCores)
		h.CPUThreads = int32(hw.NumCpuThreads)
		h.CPUMHz = hw.CpuMhz
		h.TotalCPUMHz = int64(hw.NumCpuCores) * int64(hw.CpuMhz)
		h.MemoryMB = hw.MemorySize / (1 << 20)
	}
	if p := m.Summary.Config.Product; p != nil {
		h.Version = p.Version
		h.Build = p.Build
	}
	qs := m.Summary.QuickStats
	if qs.OverallCpuUsage != 0 {
		h.CPUUsageMHz = int64(qs.OverallCpuUsage)
	}
	if qs.OverallMemoryUsage != 0 {
		h.MemoryUsageMB = int64(qs.OverallMemoryUsage)
	}
	if withConfig {
		mapHostConfig(&h, m.Config)
	}
	return h
}
