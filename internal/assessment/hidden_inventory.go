package assessment

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// CollectionPartial marks a collection that returned no error but is known to
// be missing objects the rest of the capture still references.
const CollectionPartial = "partial"

// morefKey matches the managed-object keys vCenter hands out for networks. An
// opaque (NSX) network id is not one, and is not enumerated with them, so it
// never counts as a dangling reference.
var morefKey = regexp.MustCompile(`^(dvportgroup|network)-\d+$`)

// markHiddenInventory demotes host, network and dvswitch collections that
// look complete but are contradicted by the rest of the same capture.
//
// vCenter answers a NoAccess override with silence, not a fault: the property
// collector simply leaves the object out. A collection that lists nothing, or
// lists fewer objects than are referenced, would otherwise read as a confirmed
// answer, and every rule that gates on it would report a clean result.
func markHiddenInventory(r *ContextResult) {
	hosts := decodeResources[vsphere.Host](r, "host")
	clusters := decodeResources[vsphere.Cluster](r, "cluster")
	networks := decodeResources[vsphere.Network](r, "network")
	switches := decodeResources[vsphere.DVSwitch](r, "dvswitch")

	hostNames, hostsByCluster := map[string]bool{}, map[string]int{}
	for _, h := range hosts {
		hostNames[strings.ToLower(h.Name)] = true
		hostsByCluster[strings.ToLower(h.Cluster)]++
	}
	networkKeys, networkNames := map[string]bool{}, map[string]bool{}
	for _, n := range networks {
		networkKeys[strings.ToLower(n.ID)] = true
		networkNames[strings.ToLower(n.Name)] = true
	}
	switchUUIDs := map[string]bool{}
	for _, s := range switches {
		switchUUIDs[strings.ToLower(s.UUID)] = true
	}

	var hiddenHost, hiddenNetwork, hiddenSwitch string
	for _, c := range clusters {
		if !c.Standalone && c.Hosts > hostsByCluster[strings.ToLower(c.Name)] {
			hiddenHost = fmt.Sprintf("cluster %q has %d host(s) but only %d were returned", c.Name, c.Hosts, hostsByCluster[strings.ToLower(c.Name)])
			break
		}
	}
	for _, o := range r.VMs {
		vm := o.VM
		if hiddenHost == "" {
			switch {
			case vm.Host != "" && !hostNames[strings.ToLower(vm.Host)]:
				hiddenHost = fmt.Sprintf("VM %q runs on host %q, which was not returned", vm.Name, vm.Host)
			case vm.Host == "" && vm.PowerState == "poweredOn":
				hiddenHost = fmt.Sprintf("powered-on VM %q reports no host", vm.Name)
			}
		}
		for _, nic := range vm.NICs {
			if hiddenNetwork == "" && morefKey.MatchString(nic.NetworkID) && !networkKeys[strings.ToLower(nic.NetworkID)] && !networkNames[strings.ToLower(nic.Network)] {
				hiddenNetwork = fmt.Sprintf("VM %q uses network %q (%s), which was not returned", vm.Name, nic.Network, nic.NetworkID)
			}
			if hiddenSwitch == "" && nic.SwitchID != "" && !switchUUIDs[strings.ToLower(nic.SwitchID)] {
				hiddenSwitch = fmt.Sprintf("VM %q uses distributed switch %s, which was not returned", vm.Name, nic.SwitchID)
			}
		}
	}

	demote := func(kind, reason string) {
		if reason == "" {
			return
		}
		for i := range r.Collections {
			c := &r.Collections[i]
			if c.Kind == kind && Successful(c.Status) {
				c.Status = CollectionPartial
				c.Error = "possible permission-hidden inventory: " + reason
			}
		}
	}
	demote("host", hiddenHost)
	demote("network", hiddenNetwork)
	demote("dvswitch", hiddenSwitch)
}

func decodeResources[T any](r *ContextResult, kind string) []T {
	var out []T
	for _, c := range r.Collections {
		if c.Kind != kind || !Successful(c.Status) {
			continue
		}
		for _, res := range c.Resources {
			var v T
			if json.Unmarshal(res.Payload, &v) == nil {
				out = append(out, v)
			}
		}
	}
	return out
}
