package vsphere

import (
	"reflect"
	"sort"

	"github.com/vmware/govmomi/vim25/types"
)

// mapHostConfig flattens the two expensive HostSystem configuration
// properties into stable, JSON-friendly sub-objects. HostSystem.Config is nil
// for a disconnected or not-responding host, so absence is intentionally an
// empty set rather than an error or a panic.
func mapHostConfig(host *Host, config *types.HostConfigInfo) {
	if host == nil || config == nil {
		return
	}
	if storage := config.StorageDevice; storage != nil {
		host.HBAs = mapHostHBAs(storage.HostBusAdapter)
		host.Multipaths = mapHostMultipaths(storage, storage.MultipathInfo)
	}
	if network := config.Network; network != nil {
		host.NICs, host.VSwitches, host.PortGroups, host.VMKs = mapHostNetwork(network)
		host.ProxySwitches = mapHostProxySwitches(network)
	}
}

func mapHostHBAs(values []types.BaseHostHostBusAdapter) []HostHBA {
	type mapped struct {
		value HostHBA
		key   string
	}
	out := make([]mapped, 0, len(values))
	for _, value := range values {
		if value == nil || value.GetHostHostBusAdapter() == nil {
			continue
		}
		base := value.GetHostHostBusAdapter()
		mappedValue := HostHBA{
			Key:             base.Key,
			Device:          base.Device,
			Bus:             base.Bus,
			Status:          base.Status,
			Model:           base.Model,
			Driver:          base.Driver,
			PCI:             base.Pci,
			StorageProtocol: base.StorageProtocol,
			Type:            hostTypeName(value),
		}
		if mappedValue.StorageProtocol == "" {
			mappedValue.StorageProtocol = "scsi"
		}
		switch hba := value.(type) {
		case *types.HostFibreChannelHba:
			mappedValue.WWNN = nonzeroInt64(hba.NodeWorldWideName)
			mappedValue.WWPN = nonzeroInt64(hba.PortWorldWideName)
		case *types.HostInternetScsiHba:
			mappedValue.IScsiName = hba.IScsiName
			mappedValue.IScsiAlias = hba.IScsiAlias
		case *types.HostSerialAttachedHba,
			*types.HostBlockHba,
			*types.HostParallelScsiHba,
			*types.HostPcieHba,
			*types.HostRdmaHba:
			// The common adapter fields are the complete representation for
			// these transports.
		}
		out = append(out, mapped{value: mappedValue, key: base.Key})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].value.Device != out[j].value.Device {
			return out[i].value.Device < out[j].value.Device
		}
		return out[i].key < out[j].key
	})
	result := make([]HostHBA, len(out))
	for i := range out {
		result[i] = out[i].value
	}
	return result
}

func mapHostNetwork(network *types.HostNetworkInfo) ([]HostNIC, []HostVSwitch, []HostPortGroup, []HostVMKernel) {
	switchNames := make(map[string]string, len(network.Vswitch))
	for _, sw := range network.Vswitch {
		switchNames[sw.Key] = sw.Name
	}
	pnicNames := make(map[string]string, len(network.Pnic))
	for _, pnic := range network.Pnic {
		pnicNames[pnic.Key] = pnic.Device
	}

	nics := make([]HostNIC, 0, len(network.Pnic))
	for _, pnic := range network.Pnic {
		nic := HostNIC{
			Key:       pnic.Key,
			Device:    pnic.Device,
			PCI:       pnic.Pci,
			Driver:    pnic.Driver,
			MAC:       pnic.Mac,
			WakeOnLAN: pnic.WakeOnLanSupported,
		}
		if pnic.LinkSpeed != nil {
			nic.LinkSpeedMB = int32Ptr(pnic.LinkSpeed.SpeedMb)
			nic.Duplex = boolPtr(pnic.LinkSpeed.Duplex)
		}
		for _, sw := range network.Vswitch {
			for _, key := range sw.Pnic {
				if key == pnic.Key {
					nic.Switch = sw.Name
					break
				}
			}
			if nic.Switch != "" {
				break
			}
		}
		nics = append(nics, nic)
	}
	sort.SliceStable(nics, func(i, j int) bool {
		if nics[i].Device != nics[j].Device {
			return nics[i].Device < nics[j].Device
		}
		return nics[i].Key < nics[j].Key
	})

	switches := make([]HostVSwitch, 0, len(network.Vswitch))
	for _, sw := range network.Vswitch {
		uplinks := make([]string, 0, len(sw.Pnic))
		for _, key := range sw.Pnic {
			if device := pnicNames[key]; device != "" {
				uplinks = append(uplinks, device)
			} else {
				uplinks = append(uplinks, key)
			}
		}
		mapped := HostVSwitch{
			Key:       sw.Key,
			Name:      sw.Name,
			NumPorts:  sw.NumPorts,
			FreePorts: sw.NumPortsAvailable,
			MTU:       sw.Mtu,
			Uplinks:   uplinks,
		}
		if policy := sw.Spec.Policy; policy != nil {
			mapped.Promiscuous, mapped.MACChanges, mapped.ForgedTransmits = securityPolicy(policy.Security)
			if policy.ShapingPolicy != nil {
				mapped.TrafficShaping = boolPtrValue(policy.ShapingPolicy.Enabled)
			}
		}
		switches = append(switches, mapped)
	}
	sort.SliceStable(switches, func(i, j int) bool {
		if switches[i].Name != switches[j].Name {
			return switches[i].Name < switches[j].Name
		}
		return switches[i].Key < switches[j].Key
	})

	ports := make([]HostPortGroup, 0, len(network.Portgroup))
	for _, port := range network.Portgroup {
		mapped := HostPortGroup{
			Key:    port.Key,
			Name:   port.Spec.Name,
			Switch: port.Spec.VswitchName,
			VLAN:   port.Spec.VlanId,
		}
		if mapped.Switch == "" {
			mapped.Switch = switchNames[port.Vswitch]
		}
		mapped.Promiscuous, mapped.MACChanges, mapped.ForgedTransmits = securityPolicy(port.ComputedPolicy.Security)
		ports = append(ports, mapped)
	}
	sort.SliceStable(ports, func(i, j int) bool {
		if ports[i].Name != ports[j].Name {
			return ports[i].Name < ports[j].Name
		}
		return ports[i].Key < ports[j].Key
	})

	vmks := make([]HostVMKernel, 0, len(network.Vnic)+len(network.ConsoleVnic))
	for _, value := range network.Vnic {
		vmks = append(vmks, mapHostVMKernel(value, false))
	}
	for _, value := range network.ConsoleVnic {
		vmks = append(vmks, mapHostVMKernel(value, true))
	}
	sort.SliceStable(vmks, func(i, j int) bool {
		if vmks[i].Device != vmks[j].Device {
			return vmks[i].Device < vmks[j].Device
		}
		return vmks[i].Key < vmks[j].Key
	})
	return nics, switches, ports, vmks
}

func mapHostVMKernel(value types.HostVirtualNic, serviceConsole bool) HostVMKernel {
	mapped := HostVMKernel{
		Key:            value.Key,
		Device:         value.Device,
		PortGroup:      value.Portgroup,
		MAC:            value.Spec.Mac,
		MTU:            value.Spec.Mtu,
		TSO:            value.Spec.TsoEnabled,
		Netstack:       value.Spec.NetStackInstanceKey,
		ServiceConsole: serviceConsole,
	}
	if mapped.PortGroup == "" {
		mapped.PortGroup = value.Spec.Portgroup
	}
	if port := value.Spec.DistributedVirtualPort; port != nil {
		mapped.DVSwitchUUID = port.SwitchUuid
		mapped.DVPortGroupKey = port.PortgroupKey
	}
	if ip := value.Spec.Ip; ip != nil {
		mapped.DHCP = boolPtr(ip.Dhcp)
		mapped.IP = ip.IpAddress
		mapped.SubnetMask = ip.SubnetMask
	}
	return mapped
}

// mapHostProxySwitches maps each distributed switch the host belongs to. An
// uplink's name comes from UplinkPort, which pairs the uplink port key with
// the name the switch gives it; the physical NIC comes from the backing
// spec, which pairs a NIC with the uplink port key it is assigned to.
func mapHostProxySwitches(network *types.HostNetworkInfo) []HostProxySwitch {
	out := make([]HostProxySwitch, 0, len(network.ProxySwitch))
	for _, ps := range network.ProxySwitch {
		nicByPort := map[string]string{}
		if backing, ok := ps.Spec.Backing.(*types.DistributedVirtualSwitchHostMemberPnicBacking); ok {
			for _, spec := range backing.PnicSpec {
				if spec.UplinkPortKey != "" {
					nicByPort[spec.UplinkPortKey] = spec.PnicDevice
				}
			}
		}
		mapped := HostProxySwitch{Key: ps.Key, Switch: ps.DvsName, SwitchUUID: ps.DvsUuid, MTU: ps.Mtu}
		for _, port := range ps.UplinkPort {
			mapped.Uplinks = append(mapped.Uplinks, HostProxyUplink{Name: port.Value, NIC: nicByPort[port.Key]})
		}
		out = append(out, mapped)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Switch != out[j].Switch {
			return out[i].Switch < out[j].Switch
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func mapHostMultipaths(storage *types.HostStorageDeviceInfo, info *types.HostMultipathInfo) []HostMultipath {
	if storage == nil || info == nil {
		return nil
	}
	type lunEvidence struct {
		lun       types.ScsiLun
		localDisk *bool
	}
	luns := make(map[string]lunEvidence, len(storage.ScsiLun)*3)
	for _, raw := range storage.ScsiLun {
		if raw == nil || raw.GetScsiLun() == nil {
			continue
		}
		lun := raw.GetScsiLun()
		evidence := lunEvidence{lun: *lun}
		if disk, ok := raw.(*types.HostScsiDisk); ok {
			evidence.localDisk = disk.LocalDisk
		}
		luns[lun.Key] = evidence
		if lun.Uuid != "" {
			luns[lun.Uuid] = evidence
		}
		if lun.CanonicalName != "" {
			luns[lun.CanonicalName] = evidence
		}
	}
	out := make([]HostMultipath, 0, len(info.Lun))
	for _, value := range info.Lun {
		resolved := luns[value.Lun]
		lun := resolved.lun
		name := lun.DisplayName
		if name == "" {
			name = lun.CanonicalName
		}
		if name == "" {
			name = lun.DeviceName
		}
		mapped := HostMultipath{
			Key:        value.Key,
			LUN:        name,
			DevicePath: lun.DeviceName,
			Policy:     multipathPolicy(value.Policy),
			LocalDisk:  resolved.localDisk,
			PathCount:  len(value.Path),
		}
		for _, path := range value.Path {
			state := path.State
			if state == "" {
				state = path.PathState
			}
			switch state {
			case "active":
				mapped.Active++
			case "standby":
				mapped.Standby++
			case "dead":
				mapped.Dead++
			case "disabled":
				mapped.Disabled++
			}
			if path.IsWorkingPath != nil && *path.IsWorkingPath {
				mapped.WorkingPaths++
			}
		}
		out = append(out, mapped)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LUN != out[j].LUN {
			return out[i].LUN < out[j].LUN
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func multipathPolicy(value types.BaseHostMultipathInfoLogicalUnitPolicy) string {
	if value == nil {
		return ""
	}
	switch policy := value.(type) {
	case *types.HostMultipathInfoFixedLogicalUnitPolicy:
		return policy.HostMultipathInfoLogicalUnitPolicy.Policy
	case *types.HostMultipathInfoHppLogicalUnitPolicy:
		return policy.HostMultipathInfoLogicalUnitPolicy.Policy
	case *types.HostMultipathInfoLogicalUnitPolicy:
		return policy.Policy
	default:
		return value.GetHostMultipathInfoLogicalUnitPolicy().Policy
	}
}

func securityPolicy(policy *types.HostNetworkSecurityPolicy) (*bool, *bool, *bool) {
	if policy == nil {
		return nil, nil, nil
	}
	return policy.AllowPromiscuous, policy.MacChanges, policy.ForgedTransmits
}

func hostTypeName(value any) string {
	t := reflect.TypeOf(value)
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}

func nonzeroInt64(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

func int32Ptr(value int32) *int32 { return &value }
func boolPtr(value bool) *bool    { return &value }

func boolPtrValue(value *bool) *bool {
	if value == nil {
		return nil
	}
	return boolPtr(*value)
}
