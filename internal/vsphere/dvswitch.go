package vsphere

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

var dvSwitchProps = []string{"name", "parent", "uuid", "summary", "config", "portgroup", "customValue"}
var dvPortGroupProps = []string{"name", "parent", "key", "config"}

// ListDVSwitches returns distributed switches and their configuration-derived
// port groups. It is kept separate from ListInventory because distributed
// switch inventory is capture-only for now.
func (c *Client) ListDVSwitches(ctx context.Context) ([]DVSwitch, error) {
	idx, err := newIndex(ctx, c)
	if err != nil {
		return nil, err
	}
	return c.listDVSwitches(ctx, idx)
}

func (c *Client) listDVSwitches(ctx context.Context, idx *index) ([]DVSwitch, error) {
	var raw []mo.DistributedVirtualSwitch
	if err := retrieve(ctx, c, idx.root, []string{"DistributedVirtualSwitch"}, []string{"DistributedVirtualSwitch"}, dvSwitchProps, &raw); err != nil {
		return nil, err
	}

	byRef := make(map[types.ManagedObjectReference]int, len(raw))
	refs := make([]types.ManagedObjectReference, 0, len(raw))
	values := make(map[types.ManagedObjectReference][]types.BaseCustomFieldValue, len(raw))
	for i := range raw {
		refs = append(refs, raw[i].Self)
		values[raw[i].Self] = raw[i].CustomValue
	}
	metadata := c.collectMetadata(ctx, refs, values)
	out := make([]DVSwitch, 0, len(raw))
	for i := range raw {
		m := &raw[i]
		baseConfig := m.Config
		cfg := baseConfig.GetDVSConfigInfo()
		name := m.Name
		if cfg != nil && cfg.Name != "" {
			name = cfg.Name
		}
		mapped := DVSwitch{
			Location: idx.locate(c, m.Self, name),
			ID:       m.Self.Value,
			Name:     name,
			UUID:     m.Uuid,
		}
		mapped.Metadata = metadata[m.Self]
		if m.Summary.Name != "" {
			mapped.Name = m.Summary.Name
			mapped.Location = idx.locate(c, m.Self, mapped.Name)
		}
		if cfg != nil {
			if mapped.UUID == "" {
				mapped.UUID = cfg.Uuid
			}
			mapped.NumPorts = cfg.NumPorts
			mapped.MaxPorts = cfg.MaxPorts
			mapped.Description = cfg.Description
			mapped.Contact = cfg.Contact.Name
			mapped.ContactDetail = cfg.Contact.Contact
			mapped.Hosts = idx.names(m.Summary.HostMember)
			mapped.UplinkPorts = idx.names(cfg.UplinkPortgroup)
			mapped.Vendor = cfg.ProductInfo.Vendor
			mapped.Version = cfg.ProductInfo.Version
			if vmware, ok := baseConfig.(*types.VMwareDVSConfigInfo); ok {
				mapped.MaxMTU = vmware.MaxMtu
				if vmware.LinkDiscoveryProtocolConfig != nil {
					mapped.LinkDiscoveryProtocol = vmware.LinkDiscoveryProtocolConfig.Protocol
					mapped.LinkDiscoveryOperation = vmware.LinkDiscoveryProtocolConfig.Operation
				}
				mapped.LACPVersion = vmware.LacpApiVersion
			}
		}
		if mapped.UUID == "" {
			mapped.UUID = m.Summary.Uuid
		}
		if m.Summary.ProductInfo != nil {
			if mapped.Vendor == "" {
				mapped.Vendor = m.Summary.ProductInfo.Vendor
			}
			if mapped.Version == "" {
				mapped.Version = m.Summary.ProductInfo.Version
			}
		}
		if mapped.Description == "" {
			mapped.Description = m.Summary.Description
		}
		if mapped.Contact == "" && m.Summary.Contact != nil {
			mapped.Contact = m.Summary.Contact.Name
			mapped.ContactDetail = m.Summary.Contact.Contact
		}
		if len(mapped.Hosts) == 0 {
			mapped.Hosts = idx.names(m.Summary.HostMember)
		}
		if mapped.NumPorts == 0 {
			mapped.NumPorts = m.Summary.NumPorts
		}
		byRef[m.Self] = len(out)
		out = append(out, mapped)
	}

	var portGroups []mo.DistributedVirtualPortgroup
	if err := retrieve(ctx, c, idx.root, []string{"DistributedVirtualPortgroup"}, []string{"DistributedVirtualPortgroup"}, dvPortGroupProps, &portGroups); err != nil {
		return nil, err
	}
	for i := range portGroups {
		m := &portGroups[i]
		parent := m.Config.DistributedVirtualSwitch
		if parent == nil {
			continue
		}
		switchIndex, ok := byRef[*parent]
		if !ok {
			continue
		}
		out[switchIndex].PortGroups = append(out[switchIndex].PortGroups, mapDVPortGroup(m, out[switchIndex].Name))
	}

	for i := range out {
		sort.SliceStable(out[i].PortGroups, func(a, b int) bool {
			if out[i].PortGroups[a].Name != out[i].PortGroups[b].Name {
				return out[i].PortGroups[a].Name < out[i].PortGroups[b].Name
			}
			return out[i].PortGroups[a].Key < out[i].PortGroups[b].Key
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func mapDVPortGroup(m *mo.DistributedVirtualPortgroup, switchName string) DVPortGroup {
	cfg := m.Config
	p := DVPortGroup{
		ID:                m.Self.Value,
		Key:               cfg.Key,
		Name:              cfg.Name,
		Switch:            switchName,
		Type:              cfg.Type,
		BackingType:       cfg.BackingType,
		NumPorts:          cfg.NumPorts,
		Uplink:            cfg.Uplink != nil && *cfg.Uplink,
		AutoExpand:        boolPtrValue(cfg.AutoExpand),
		LogicalSwitchUUID: cfg.LogicalSwitchUuid,
		SegmentID:         cfg.SegmentId,
	}
	if p.Name == "" {
		p.Name = m.Name
	}
	if cfg.DefaultPortConfig != nil {
		if setting, ok := cfg.DefaultPortConfig.(*types.VMwareDVSPortSetting); ok {
			p.VLAN = dvsVLAN(setting.Vlan)
			p.Blocked = boolPolicy(setting.Blocked)
			p.IngressShaping = shapingPolicy(setting.InShapingPolicy)
			p.EgressShaping = shapingPolicy(setting.OutShapingPolicy)
			if setting.UplinkTeamingPolicy != nil {
				p.TeamingPolicy = stringPolicy(setting.UplinkTeamingPolicy.Policy)
				p.NotifySwitches = boolPolicy(setting.UplinkTeamingPolicy.NotifySwitches)
				p.Failback = invertedBoolPolicy(setting.UplinkTeamingPolicy.RollingOrder)
				if order := setting.UplinkTeamingPolicy.UplinkPortOrder; order != nil {
					p.ActiveUplinks = append([]string(nil), order.ActiveUplinkPort...)
					p.StandbyUplinks = append([]string(nil), order.StandbyUplinkPort...)
				}
			}
			if security := setting.SecurityPolicy; security != nil {
				p.Promiscuous = boolPolicy(security.AllowPromiscuous)
				p.MACChanges = boolPolicy(security.MacChanges)
				p.ForgedTransmits = boolPolicy(security.ForgedTransmits)
			}
			if security := setting.MacManagementPolicy; security != nil {
				p.Promiscuous = boolPtrValue(security.AllowPromiscuous)
				p.MACChanges = boolPtrValue(security.MacChanges)
				p.ForgedTransmits = boolPtrValue(security.ForgedTransmits)
			}
		}
	}
	return p
}

func shapingPolicy(policy *types.DVSTrafficShapingPolicy) *bool {
	if policy == nil {
		return nil
	}
	return boolPolicy(policy.Enabled)
}

func boolPolicy(policy *types.BoolPolicy) *bool {
	if policy == nil || policy.Value == nil {
		return nil
	}
	return boolPtr(*policy.Value)
}

func invertedBoolPolicy(policy *types.BoolPolicy) *bool {
	value := boolPolicy(policy)
	if value != nil {
		*value = !*value
	}
	return value
}

func stringPolicy(policy *types.StringPolicy) string {
	if policy == nil {
		return ""
	}
	return policy.Value
}

// dvsVLAN renders the effective VLAN specification in the compact form used
// by the inventory model and RVTools adapter.
func dvsVLAN(spec types.BaseVmwareDistributedVirtualSwitchVlanSpec) string {
	switch value := spec.(type) {
	case *types.VmwareDistributedVirtualSwitchVlanIdSpec:
		if value.VlanId == 0 {
			return ""
		}
		return strconv.Itoa(int(value.VlanId))
	case *types.VmwareDistributedVirtualSwitchTrunkVlanSpec:
		parts := make([]string, 0, len(value.VlanId))
		for _, r := range value.VlanId {
			if r.Start == r.End {
				parts = append(parts, strconv.Itoa(int(r.Start)))
			} else {
				parts = append(parts, fmt.Sprintf("%d-%d", r.Start, r.End))
			}
		}
		if len(parts) == 0 {
			return "trunk"
		}
		return "trunk " + strings.Join(parts, ",")
	case *types.VmwareDistributedVirtualSwitchPvlanSpec:
		if value.PvlanId == 0 {
			return ""
		}
		return fmt.Sprintf("pvlan %d", value.PvlanId)
	default:
		return ""
	}
}
