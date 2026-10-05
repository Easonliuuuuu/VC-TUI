package vsphere

import (
	"context"
	"sort"

	"github.com/vmware/govmomi/vim25/mo"
)

// NetworkTopology is what the terminal interface's switch views read: the
// distributed switches, every host's network configuration, and how many
// VMs each network has attached. It is a separate read, made only while a
// network view is on screen, because host network configuration is too heavy
// to fetch on every inventory load of a large estate.
//
// A partial result is a valid result, the same way it is for Inventory:
// Errors records which part could not be read.
type NetworkTopology struct {
	Context  string     `json:"context"`
	Switches []DVSwitch `json:"switches"`
	// Hosts carry identity, cluster, state and network configuration only;
	// their storage and summary fields are empty.
	Hosts []Host `json:"hosts"`
	// NetworkVMs counts the VMs attached to each network, by the network's
	// managed object ID. A network missing from the map was not counted.
	NetworkVMs map[string]int   `json:"network_vms"`
	Errors     []InventoryError `json:"errors,omitempty"`
}

// ErrorFor reports why one part of the topology could not be read.
func (t *NetworkTopology) ErrorFor(kind Kind) (string, bool) {
	for _, e := range t.Errors {
		if e.Kind == kind {
			return e.Message, true
		}
	}
	return "", false
}

var hostNetworkProps = []string{
	"name",
	"parent",
	"runtime.powerState",
	"runtime.connectionState",
	"runtime.inMaintenanceMode",
	"config.network",
}

// NetworkTopology reads the network topology of the whole vCenter. Every
// query is a read of configuration properties; nothing is changed.
func (c *Client) NetworkTopology(ctx context.Context) (*NetworkTopology, error) {
	idx, err := newIndex(ctx, c)
	if err != nil {
		return nil, err
	}
	out := &NetworkTopology{Context: c.Context.Name, NetworkVMs: map[string]int{}}
	fail := func(kind Kind, err error) {
		out.Errors = append(out.Errors, InventoryError{Kind: kind, Message: err.Error()})
	}
	if switches, err := c.listDVSwitches(ctx, idx); err != nil {
		fail(KindDVSwitch, err)
	} else {
		out.Switches = switches
	}
	if hosts, err := c.listHostNetworks(ctx, idx); err != nil {
		fail(KindHost, err)
	} else {
		out.Hosts = hosts
	}
	var networks []mo.Network
	if err := retrieve(ctx, c, idx.root, networkKinds, []string{"Network"}, []string{"vm"}, &networks); err != nil {
		fail(KindNetwork, err)
	} else {
		for i := range networks {
			out.NetworkVMs[networks[i].Self.Value] = len(networks[i].Vm)
		}
	}
	return out, nil
}

// listHostNetworks is listHostsWith narrowed to config.network: the switch
// views have no use for storage devices, which are the heavier half of the
// host configuration.
func (c *Client) listHostNetworks(ctx context.Context, idx *index) ([]Host, error) {
	var raw []mo.HostSystem
	if err := retrieve(ctx, c, idx.root, []string{"HostSystem"}, []string{"HostSystem"}, hostNetworkProps, &raw); err != nil {
		return nil, err
	}
	out := make([]Host, 0, len(raw))
	for i := range raw {
		out = append(out, newHostWithConfig(c, idx, &raw[i], true))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
