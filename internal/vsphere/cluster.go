package vsphere

import (
	"context"
	"sort"

	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

var computeResourceKinds = []string{"ComputeResource", "ClusterComputeResource"}

var clusterProps = []string{"name", "parent", "summary", "host", "customValue"}

// ListClusters returns the clusters in a vCenter. A host that is not in a
// cluster appears as a standalone compute resource and is reported with
// Standalone set, because operators still need to see where it lives.
func (c *Client) ListClusters(ctx context.Context) ([]Cluster, error) {
	idx, err := newIndex(ctx, c)
	if err != nil {
		return nil, err
	}
	return c.listClusters(ctx, idx)
}

func (c *Client) listClusters(ctx context.Context, idx *index) ([]Cluster, error) {
	var raw []mo.ComputeResource
	if err := retrieve(ctx, c, idx.root, computeResourceKinds, []string{"ComputeResource"}, clusterProps, &raw); err != nil {
		return nil, err
	}
	settings, err := c.clusterSettings(ctx, idx.root)
	if err != nil {
		return nil, err
	}
	out := make([]Cluster, 0, len(raw))
	refs := make([]types.ManagedObjectReference, 0, len(raw))
	values := make(map[types.ManagedObjectReference][]types.BaseCustomFieldValue, len(raw))
	for i := range raw {
		refs = append(refs, raw[i].Self)
		values[raw[i].Self] = raw[i].CustomValue
	}
	metadata := c.collectMetadata(ctx, refs, values)
	for i := range raw {
		m := &raw[i]
		cl := Cluster{
			Location:   idx.locate(c, m.Self, m.Name),
			ID:         m.Self.Value,
			Name:       m.Name,
			Standalone: m.Self.Type != "ClusterComputeResource",
			// The compute resource summary is authoritative when the server
			// fills it in; the member list is the fallback.
			Hosts: len(m.Host),
		}
		applyComputeSummary(&cl, m.Summary)
		if st, ok := settings[m.Self]; ok {
			cl.DRSEnabled = st.drs
			cl.HAEnabled = st.ha
		}
		cl.Metadata = metadata[m.Self]
		out = append(out, cl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// applyComputeSummary copies capacity from the summary through the base
// interface: a ClusterComputeResource returns *ClusterComputeResourceSummary,
// which embeds ComputeResourceSummary, so asserting the concrete base type
// would match standalone hosts only.
func applyComputeSummary(cl *Cluster, summary types.BaseComputeResourceSummary) {
	if summary == nil {
		return
	}
	if p, ok := summary.(*types.ComputeResourceSummary); ok && p == nil {
		return
	}
	if p, ok := summary.(*types.ClusterComputeResourceSummary); ok && p == nil {
		return
	}
	s := summary.GetComputeResourceSummary()
	if s == nil {
		return
	}
	cl.CPUCores = int32(s.NumCpuCores)
	cl.TotalCPUMHz = int64(s.TotalCpu)
	cl.TotalMemoryMB = s.TotalMemory / (1 << 20)
	cl.EffectiveHost = int(s.NumEffectiveHosts)
	if s.NumHosts > 0 {
		cl.Hosts = int(s.NumHosts)
	}
}

type clusterSetting struct{ drs, ha bool }

// clusterSettings reads DRS and HA state, which only exists on the cluster
// subtype and so cannot be part of the ComputeResource property set.
func (c *Client) clusterSettings(ctx context.Context, root types.ManagedObjectReference) (map[types.ManagedObjectReference]clusterSetting, error) {
	var raw []mo.ClusterComputeResource
	err := retrieve(ctx, c, root, []string{"ClusterComputeResource"}, []string{"ClusterComputeResource"}, []string{"configurationEx"}, &raw)
	if err != nil {
		return nil, err
	}
	out := make(map[types.ManagedObjectReference]clusterSetting, len(raw))
	for i := range raw {
		cfg, ok := raw[i].ConfigurationEx.(*types.ClusterConfigInfoEx)
		if !ok || cfg == nil {
			continue
		}
		var s clusterSetting
		if cfg.DrsConfig.Enabled != nil {
			s.drs = *cfg.DrsConfig.Enabled
		}
		if cfg.DasConfig.Enabled != nil {
			s.ha = *cfg.DasConfig.Enabled
		}
		out[raw[i].Self] = s
	}
	return out, nil
}
