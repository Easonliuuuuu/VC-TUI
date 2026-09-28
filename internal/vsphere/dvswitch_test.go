package vsphere

import (
	"context"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/config"
)

func simulatorClient(t *testing.T, tune func(*simulator.Model)) *Client {
	t.Helper()
	model := simulator.VPX()
	if tune != nil {
		tune(model)
	}
	if err := model.Create(); err != nil {
		t.Fatalf("create simulator model: %v", err)
	}
	t.Cleanup(model.Remove)
	server := model.Service.NewServer()
	t.Cleanup(server.Close)
	gc, err := govmomi.NewClient(context.Background(), server.URL, true)
	if err != nil {
		t.Fatalf("connect to simulator: %v", err)
	}
	t.Cleanup(func() { _ = gc.Logout(context.Background()) })
	endpoint := server.URL.String()
	cc := &config.Context{Name: "sim", Endpoint: endpoint, Username: "user", TLS: config.TLSConfig{Mode: config.TLSInsecure}}
	cc.Normalize()
	return NewClientForTest(cc, gc)
}

func TestListDVSwitches(t *testing.T) {
	c := simulatorClient(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Cluster = 1
		m.ClusterHost = 2
		m.Portgroup = 3
		m.PortgroupNSX = 1
	})

	switches, err := c.ListDVSwitches(context.Background())
	if err != nil {
		t.Fatalf("ListDVSwitches: %v", err)
	}
	if len(switches) == 0 {
		t.Fatal("expected a distributed switch")
	}
	for _, sw := range switches {
		if sw.ID == "" || sw.Name == "" || sw.UUID == "" {
			t.Errorf("incomplete switch identity: %+v", sw)
		}
		for i := 1; i < len(sw.PortGroups); i++ {
			before, after := sw.PortGroups[i-1], sw.PortGroups[i]
			if before.Name > after.Name || (before.Name == after.Name && before.Key > after.Key) {
				t.Errorf("port groups are not sorted: %+v", sw.PortGroups)
			}
		}
	}
	if len(switches[0].PortGroups) < 2 {
		t.Fatalf("expected port groups attached to switch, got %+v", switches[0])
	}
}

func TestDVSVLAN(t *testing.T) {
	cases := []struct {
		name string
		spec types.BaseVmwareDistributedVirtualSwitchVlanSpec
		want string
	}{
		{name: "single", spec: &types.VmwareDistributedVirtualSwitchVlanIdSpec{VlanId: 120}, want: "120"},
		{name: "untagged", spec: &types.VmwareDistributedVirtualSwitchVlanIdSpec{}, want: ""},
		{name: "trunk", spec: &types.VmwareDistributedVirtualSwitchTrunkVlanSpec{VlanId: []types.NumericRange{{Start: 0, End: 100}, {Start: 200, End: 300}}}, want: "trunk 0-100,200-300"},
		{name: "pvlan", spec: &types.VmwareDistributedVirtualSwitchPvlanSpec{PvlanId: 205}, want: "pvlan 205"},
		{name: "nil", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dvsVLAN(tc.spec); got != tc.want {
				t.Fatalf("dvsVLAN() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMapDVPortGroupFailback(t *testing.T) {
	cases := []struct {
		name         string
		rollingOrder *bool
		want         *bool
	}{
		{name: "rolling order enabled disables failback", rollingOrder: boolPtr(true), want: boolPtr(false)},
		{name: "rolling order disabled enables failback", rollingOrder: boolPtr(false), want: boolPtr(true)},
		{name: "rolling order unreported keeps failback unreported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rollingOrder *types.BoolPolicy
			if tc.rollingOrder != nil {
				rollingOrder = &types.BoolPolicy{Value: tc.rollingOrder}
			}
			group := &mo.DistributedVirtualPortgroup{
				Config: types.DVPortgroupConfigInfo{
					DefaultPortConfig: &types.VMwareDVSPortSetting{
						UplinkTeamingPolicy: &types.VmwareUplinkPortTeamingPolicy{RollingOrder: rollingOrder},
					},
				},
			}

			got := mapDVPortGroup(group, "switch").Failback
			if got == nil || tc.want == nil {
				if got != nil || tc.want != nil {
					t.Fatalf("Failback = %v, want %v", got, tc.want)
				}
				return
			}
			if *got != *tc.want {
				t.Fatalf("Failback = %t, want %t", *got, *tc.want)
			}
		})
	}
}

func TestListNetworksEnrichesDistributedPortGroups(t *testing.T) {
	c := simulatorClient(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Portgroup = 2
	})
	networks, err := c.ListNetworks(context.Background())
	if err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	for _, network := range networks {
		if network.Type != "portgroup" {
			continue
		}
		if network.Switch == "" {
			t.Fatalf("distributed port group %q missing enrichment: %+v", network.Name, network)
		}
		return
	}
	t.Fatal("simulator returned no distributed port group")
}
