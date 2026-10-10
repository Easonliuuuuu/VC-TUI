package vsphere

import (
	"context"
	"strings"
	"testing"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
)

func TestMapHostProxySwitchesPairsUplinksWithNICs(t *testing.T) {
	network := &types.HostNetworkInfo{ProxySwitch: []types.HostProxySwitch{{
		DvsUuid: "uuid-prod", DvsName: "dvs-prod", Key: "proxy-1", Mtu: 9000,
		UplinkPort: []types.KeyValue{{Key: "10", Value: "uplink1"}, {Key: "11", Value: "uplink2"}},
		Spec: types.HostProxySwitchSpec{Backing: &types.DistributedVirtualSwitchHostMemberPnicBacking{
			PnicSpec: []types.DistributedVirtualSwitchHostMemberPnicSpec{{PnicDevice: "vmnic2", UplinkPortKey: "10"}},
		}},
	}}}
	got := mapHostProxySwitches(network)
	if len(got) != 1 {
		t.Fatalf("proxy switches = %d, want 1", len(got))
	}
	ps := got[0]
	if ps.Switch != "dvs-prod" || ps.SwitchUUID != "uuid-prod" || ps.MTU != 9000 {
		t.Fatalf("switch identity = %+v", ps)
	}
	want := []HostProxyUplink{{Name: "uplink1", NIC: "vmnic2"}, {Name: "uplink2"}}
	if len(ps.Uplinks) != len(want) {
		t.Fatalf("uplinks = %+v, want %+v", ps.Uplinks, want)
	}
	for i := range want {
		if ps.Uplinks[i] != want[i] {
			t.Errorf("uplink %d = %+v, want %+v", i, ps.Uplinks[i], want[i])
		}
	}
}

func TestMapHostVMKernelRecordsDistributedPortGroup(t *testing.T) {
	vmk := mapHostVMKernel(types.HostVirtualNic{Device: "vmk1", Spec: types.HostVirtualNicSpec{
		Mtu:                    9000,
		DistributedVirtualPort: &types.DistributedVirtualSwitchPortConnection{SwitchUuid: "uuid-prod", PortgroupKey: "dvportgroup-7"},
	}}, false)
	if vmk.PortGroup != "" || vmk.DVSwitchUUID != "uuid-prod" || vmk.DVPortGroupKey != "dvportgroup-7" {
		t.Fatalf("vmk = %+v", vmk)
	}
}

func TestMapHostVMKernelRecordsIPv6(t *testing.T) {
	vmk := mapHostVMKernel(types.HostVirtualNic{Device: "vmk0", Spec: types.HostVirtualNicSpec{
		Ip: &types.HostIpConfig{IpV6Config: &types.HostIpConfigIpV6AddressConfiguration{IpV6Address: []types.HostIpConfigIpV6Address{
			{IpAddress: "fe80::250:56ff:fe6a:1", PrefixLength: 64},
			{IpAddress: "2001:db8::10", PrefixLength: 64},
		}}},
	}}, false)
	if got := strings.Join(vmk.IPv6, ","); got != "fe80::250:56ff:fe6a:1/64,2001:db8::10/64" {
		t.Fatalf("IPv6 = %s", got)
	}
	if got := vmk.Address(); got != "2001:db8::10" {
		t.Errorf("Address() = %s, want the global address", got)
	}
	vmk.IPv6 = vmk.IPv6[:1]
	if got := vmk.Address(); got != "fe80::250:56ff:fe6a:1" {
		t.Errorf("Address() = %s, want the link-local address when it is the only one", got)
	}
	vmk.IP = "10.0.0.5"
	if got := vmk.Address(); got != "10.0.0.5" {
		t.Errorf("Address() = %s, want IPv4 first", got)
	}
}

func TestNetworkTopologyReadsSwitchesHostsAndVMCounts(t *testing.T) {
	c := simulatorClient(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Cluster = 1
		m.ClusterHost = 2
		m.Portgroup = 2
	})
	topo, err := c.NetworkTopology(context.Background())
	if err != nil {
		t.Fatalf("NetworkTopology: %v", err)
	}
	if len(topo.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", topo.Errors)
	}
	if len(topo.Switches) == 0 || len(topo.Switches[0].PortGroups) == 0 {
		t.Fatalf("expected a distributed switch with port groups: %+v", topo.Switches)
	}
	if len(topo.Hosts) == 0 {
		t.Fatal("expected hosts")
	}
	for _, h := range topo.Hosts {
		if h.Name == "" || len(h.NICs) == 0 || len(h.VSwitches) == 0 {
			t.Errorf("host %q is missing network configuration: %+v", h.Name, h)
		}
		if len(h.HBAs) != 0 || len(h.Multipaths) != 0 {
			t.Errorf("host %q read storage configuration the switch views do not need", h.Name)
		}
	}
	// vcsim never fills Network.vm, so the counts themselves are zero here;
	// what is checked is that every network was counted at all. Real counts
	// were checked against vCenter 8.0.3.
	networks, err := c.ListNetworks(context.Background())
	if err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	for _, n := range networks {
		if _, ok := topo.NetworkVMs[n.ID]; !ok {
			t.Errorf("network %s (%s) has no VM count", n.Name, n.ID)
		}
	}
}
