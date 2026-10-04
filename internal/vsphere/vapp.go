package vsphere

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

var vappProps = []string{
	"customValue",
	"name",
	"parent",
	"parentVApp",
	"vm",
	"resourcePool",
	"childLink",
	"summary",
	"config",
	"overallStatus",
	"configStatus",
	// Only the startup sequence: vAppConfig also carries OVF sections and
	// product properties, which can be large and are not needed here.
	"vAppConfig.entityConfig",
}

// ListVApps returns vSphere VirtualApp containers in the client's configured
// datacenter scope. It is deliberately separate from ListClusters: a vApp is
// a logical workload container, not a compute resource.
func (c *Client) ListVApps(ctx context.Context) ([]VApp, error) {
	idx, err := newIndex(ctx, c)
	if err != nil {
		return nil, err
	}
	return c.listVApps(ctx, idx)
}

func (c *Client) listVApps(ctx context.Context, idx *index) ([]VApp, error) {
	var raw []mo.VirtualApp
	if err := retrieve(ctx, c, idx.root, []string{"VirtualApp"}, []string{"VirtualApp"}, vappProps, &raw); err != nil {
		return nil, err
	}
	out := make([]VApp, 0, len(raw))
	refs := make([]types.ManagedObjectReference, 0, len(raw))
	values := make(map[types.ManagedObjectReference][]types.BaseCustomFieldValue, len(raw))
	for i := range raw {
		refs = append(refs, raw[i].Self)
		values[raw[i].Self] = raw[i].CustomValue
	}
	metadata := c.collectMetadata(ctx, refs, values)
	for i := range raw {
		vapp := newVApp(c, idx, &raw[i])
		vapp.Metadata = metadata[raw[i].Self]
		out = append(out, vapp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func newVApp(c *Client, idx *index, m *mo.VirtualApp) VApp {
	parent := m.Parent
	parentVApp := m.ParentVApp
	if parentVApp == nil && parent != nil && parent.Type == "VirtualApp" {
		parentVApp = parent
	}

	childRefs := append([]types.ManagedObjectReference(nil), m.ResourcePool.ResourcePool...)
	for _, link := range m.ChildLink {
		childRefs = append(childRefs, link.Key)
	}
	childRefs = uniqueRefs(childRefs, m.Self)

	var childVApps, childPools []types.ManagedObjectReference
	for _, ref := range childRefs {
		switch ref.Type {
		case "VirtualApp":
			childVApps = append(childVApps, ref)
		case "ResourcePool":
			childPools = append(childPools, ref)
		}
	}

	loc := idx.locate(c, m.Self, m.Name)
	placement := idx.computeResourceOf(parent)
	if placement == "" {
		placement = idx.computeResourceOf(&m.Self)
	}
	cluster := idx.clusterFor(parent)
	if cluster == "" {
		cluster = idx.clusterFor(&m.Self)
	}
	directVMRefs := uniqueRefs(m.Vm)
	allocation := newResourceAllocation(m.Config, m.Summary)

	return VApp{
		Location:               loc,
		ID:                     m.Self.Value,
		Name:                   m.Name,
		Status:                 virtualAppStatus(m.Summary),
		OverallStatus:          string(m.OverallStatus),
		ConfigStatus:           string(m.ConfigStatus),
		ParentContainer:        idx.name(parent),
		ParentVApp:             idx.name(parentVApp),
		DirectVMCount:          len(directVMRefs),
		DirectVMs:              idx.names(directVMRefs),
		DirectVMRefs:           managedRefNames(directVMRefs),
		ChildVAppCount:         len(childVApps),
		ChildVApps:             idx.names(childVApps),
		ChildVAppRefs:          managedRefNames(childVApps),
		ChildResourcePoolCount: len(childPools),
		ChildResourcePools:     idx.names(childPools),
		ChildResourcePoolRefs:  managedRefNames(childPools),
		Cluster:                cluster,
		ComputeResource:        placement,
		Allocation:             &allocation,
		StartOrder:             vappStartOrder(idx, m.VAppConfig),
	}
}

// vappStartOrder lists the vApp's startup sequence in start order, then by
// name. A vApp whose configuration was not readable has none.
func vappStartOrder(idx *index, config *types.VAppConfigInfo) []VAppStartEntry {
	if config == nil || len(config.EntityConfig) == 0 {
		return nil
	}
	out := make([]VAppStartEntry, 0, len(config.EntityConfig))
	for _, e := range config.EntityConfig {
		entry := VAppStartEntry{
			Name:             e.Tag,
			Order:            e.StartOrder,
			DelaySeconds:     e.StartDelay,
			StartAction:      e.StartAction,
			StopAction:       e.StopAction,
			StopDelaySeconds: e.StopDelay,
			WaitForGuest:     e.WaitingForGuest != nil && *e.WaitingForGuest,
		}
		if e.Key != nil {
			entry.Ref = e.Key.Type + ":" + e.Key.Value
			if name := idx.name(e.Key); name != "" {
				entry.Name = name
			}
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// StartOrderText renders a startup sequence as its groups in order:
// "1 vapp-db (+10s) → 2 vapp-web, vapp-api". The delay is the wait after a
// group starts before the next one does, so the last group's is left out.
func StartOrderText(entries []VAppStartEntry) string {
	var b strings.Builder
	for i, e := range entries {
		switch {
		case i == 0:
			fmt.Fprintf(&b, "%d %s", e.Order, e.Name)
		case entries[i-1].Order == e.Order:
			b.WriteString(", " + e.Name)
		default:
			if d := entries[i-1].DelaySeconds; d > 0 {
				fmt.Fprintf(&b, " (+%ds)", d)
			}
			fmt.Fprintf(&b, " → %d %s", e.Order, e.Name)
		}
	}
	return b.String()
}

func virtualAppStatus(summary types.BaseResourcePoolSummary) string {
	vapp, ok := summary.(*types.VirtualAppSummary)
	if !ok || vapp == nil {
		return "unknown"
	}
	if vapp.VAppState != "" {
		return string(vapp.VAppState)
	}
	if vapp.Suspended != nil && *vapp.Suspended {
		return "suspended"
	}
	return "unknown"
}

func uniqueRefs(refs []types.ManagedObjectReference, exclude ...types.ManagedObjectReference) []types.ManagedObjectReference {
	seen := make(map[types.ManagedObjectReference]bool, len(refs)+len(exclude))
	for _, ref := range exclude {
		seen[ref] = true
	}
	out := make([]types.ManagedObjectReference, 0, len(refs))
	for _, ref := range refs {
		if ref.Type == "" || ref.Value == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

func managedRefNames(refs []types.ManagedObjectReference) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Type+":"+ref.Value)
	}
	sort.Strings(out)
	return out
}
