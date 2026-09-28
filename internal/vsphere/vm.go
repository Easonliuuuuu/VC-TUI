package vsphere

import (
	"context"
	"net"
	"sort"
	"strings"

	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// vmSummaryProps are the properties every listing needs: what a table row,
// a detail pane and a search result are built from. All of them are scalars
// or short lists, so their cost per VM is roughly constant.
var vmSummaryProps = []string{
	"customValue",
	"name",
	"parent",
	"config.template",
	"config.uuid",
	"config.instanceUuid",
	"config.guestId",
	"config.guestFullName",
	"config.annotation",
	"config.hardware.numCPU",
	"config.hardware.memoryMB",
	"runtime.powerState",
	"runtime.connectionState",
	"runtime.host",
	"guest.ipAddress",
	"guest.hostName",
	"guest.guestState",
	"guest.toolsRunningStatus",
	"guest.toolsVersion",
	"guest.toolsVersionStatus2",
	"summary.storage.committed",
	"datastore",
}

// vmDetailProps are the three properties that carry per-VM collections
// rather than scalars, and they dominate the cost of retrieving a VM:
// config.hardware.device alone returns every disk, NIC, controller and
// virtual peripheral the machine has. Nothing in a listing reads them —
// VM.Disks, VM.NICs, VM.CDROMs, VM.USBs and VM.Snapshots exist for the
// assessment ledger and the RVTools export — so a listing that asked for them
// anyway was paying for an estate's worth of device inventory to show a name
// and a power state.
var vmDetailProps = []string{
	"config.hardware.device",
	"config.hardware.numCoresPerSocket",
	"config.hardware.autoCoresPerSocket",
	"config.cpuAllocation",
	"config.memoryAllocation",
	"config.bootOptions",
	"config.firmware",
	"config.managedBy",
	"config.memoryReservationLockedToMax",
	"guest.net",
	"guest.disk",
	"snapshot",
}

// vmProps is the full property set: everything a capture records.
var vmProps = append(append([]string{}, vmSummaryProps...), vmDetailProps...)

// propsFor returns the VM properties one detail level needs.
func propsFor(detail Detail) []string {
	if detail == DetailSummary {
		return vmSummaryProps
	}
	return vmProps
}

// ListVMs returns the virtual machines in a vCenter, excluding templates.
func (c *Client) ListVMs(ctx context.Context) ([]VM, error) {
	all, err := c.listAllVMs(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, vm := range all {
		if !vm.IsTemplate {
			out = append(out, vm)
		}
	}
	return out, nil
}

func (c *Client) listAllVMs(ctx context.Context) ([]VM, error) {
	idx, err := newIndex(ctx, c)
	if err != nil {
		return nil, err
	}
	return c.listVMs(ctx, idx)
}

func (c *Client) listVMs(ctx context.Context, idx *index) ([]VM, error) {
	return c.listVMsWith(ctx, idx, FetchOptions{Detail: DetailFull}, nil)
}

// listVMsWith retrieves the virtual machines under idx.root at one detail
// level, calling onPage with each page's machines as it arrives. The pages
// are sorted individually so a caller showing them as they land is showing
// something ordered; the complete slice is sorted again before it is
// returned, since sorted pages do not make a sorted whole.
func (c *Client) listVMsWith(ctx context.Context, idx *index, opts FetchOptions, onPage func([]VM)) ([]VM, error) {
	props := propsFor(opts.Detail)
	pageSize := opts.pageSize()
	if pageSize == 0 {
		// Nobody asked for pages, so nobody is waiting to see one. Take the
		// plain single-call retrieval the command line has always used.
		var raw []mo.VirtualMachine
		if err := retrieve(ctx, c, idx.root, []string{"VirtualMachine"}, []string{"VirtualMachine"}, props, &raw); err != nil {
			return nil, err
		}
		return newVMs(ctx, c, idx, raw), nil
	}
	var out []VM
	err := retrievePages(ctx, c, idx.root, []string{"VirtualMachine"}, []string{"VirtualMachine"}, props, pageSize, func(page []types.ObjectContent) error {
		var raw []mo.VirtualMachine
		if err := mo.LoadObjectContent(page, &raw); err != nil {
			return err
		}
		vms := newVMs(ctx, c, idx, raw)
		out = append(out, vms...)
		if onPage != nil {
			onPage(vms)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortVMs(out)
	return out, nil
}

func newVMs(ctx context.Context, c *Client, idx *index, raw []mo.VirtualMachine) []VM {
	out := make([]VM, 0, len(raw))
	refs := make([]types.ManagedObjectReference, 0, len(raw))
	values := make(map[types.ManagedObjectReference][]types.BaseCustomFieldValue, len(raw))
	for i := range raw {
		refs = append(refs, raw[i].Self)
		values[raw[i].Self] = raw[i].CustomValue
	}
	metadata := c.collectMetadata(ctx, refs, values)
	for i := range raw {
		vm := newVM(c, idx, &raw[i])
		vm.Metadata = metadata[raw[i].Self]
		out = append(out, vm)
	}
	sortVMs(out)
	return out
}

func sortVMs(vms []VM) {
	sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
}

// guestHostName accepts a Tools-reported host name only when it is a plain
// DNS-style name. The value is chosen by whoever controls the guest, and it
// ends up on a terminal and in an ssh(1) argument list, so anything that
// could be read as an option ("-oProxyCommand=..."), contains whitespace or
// control characters, or is not a legal name at all is dropped rather than
// escaped: an empty result just means "no name to offer".
func guestHostName(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" || len(name) > 253 {
		return ""
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			default:
				return ""
			}
		}
	}
	return name
}

func newVM(c *Client, idx *index, m *mo.VirtualMachine) VM {
	loc := idx.locate(c, m.Self, m.Name)
	if loc.Datacenter == "" && m.Parent != nil {
		// The index is reused for a while (see IndexTTL), so a VM created
		// since it was built is not in it and resolves to nothing. Its parent
		// folder almost certainly is, and folders are far more stable than
		// their contents, so place the VM through its parent rather than
		// showing a machine with no datacenter and no path at all.
		loc.Datacenter = idx.datacenter(*m.Parent)
		loc.Path = idx.path(*m.Parent, "") + "/" + m.Name
	}
	vm := VM{
		Location:        loc,
		ID:              m.Self.Value,
		Name:            m.Name,
		PowerState:      string(m.Runtime.PowerState),
		ConnectionState: string(m.Runtime.ConnectionState),
		Host:            idx.name(m.Runtime.Host),
		Cluster:         idx.clusterOf(m.Runtime.Host),
		Folder:          idx.folderPath(m.Parent, loc.Datacenter),
		Datastores:      idx.names(m.Datastore),
	}
	if cfg := m.Config; cfg != nil {
		vm.ConfigurationAvailable = true
		vm.IsTemplate = cfg.Template
		vm.BIOSUUID = cfg.Uuid
		vm.InstanceUUID = cfg.InstanceUuid
		vm.GuestID = cfg.GuestId
		vm.GuestOS = cfg.GuestFullName
		vm.Annotation = strings.TrimSpace(cfg.Annotation)
		vm.CPU = cfg.Hardware.NumCPU
		vm.MemoryMB = int64(cfg.Hardware.MemoryMB)
		vm.Disks, vm.NICs, vm.CDROMs, vm.USBs = walkDevices(cfg.Hardware.Device, m.Guest, idx)
		vm.CoresPerSocket = 1
		if cfg.Hardware.NumCoresPerSocket != nil && *cfg.Hardware.NumCoresPerSocket > 0 {
			vm.CoresPerSocket = *cfg.Hardware.NumCoresPerSocket
		}
		vm.CPUSockets = (vm.CPU + vm.CoresPerSocket - 1) / vm.CoresPerSocket
		vm.AutoCoresPerSocket = clonePtr(cfg.Hardware.AutoCoresPerSocket)
		vm.Firmware = strings.TrimSpace(cfg.Firmware)
		if cfg.BootOptions != nil {
			vm.SecureBootEnabled = clonePtr(cfg.BootOptions.EfiSecureBootEnabled)
		}
		vm.CPUAllocation = resourceAllocation(cfg.CpuAllocation)
		vm.MemoryAllocation = resourceAllocation(cfg.MemoryAllocation)
		vm.MemoryReservationLockedToMax = clonePtr(cfg.MemoryReservationLockedToMax)
		if cfg.ManagedBy != nil {
			vm.ManagedBy = &VMManagedBy{ExtensionKey: strings.TrimSpace(cfg.ManagedBy.ExtensionKey), Type: strings.TrimSpace(cfg.ManagedBy.Type)}
		}
		vm.TPMs, vm.PCIDevices, vm.Floppies = walkMigrationDevices(cfg.Hardware.Device)
	}
	if snap := m.Snapshot; snap != nil {
		vm.Snapshots = flattenSnapshots(snap.RootSnapshotList, "", snap.CurrentSnapshot)
	}
	if g := m.Guest; g != nil {
		vm.IPAddress = g.IpAddress
		vm.GuestHostName = guestHostName(g.HostName)
		vm.GuestState = g.GuestState
		vm.ToolsState = g.ToolsRunningStatus
		vm.ToolsVersion = g.ToolsVersion
		vm.ToolsVersionStatus = g.ToolsVersionStatus2
		vm.Partitions = guestPartitions(g.Disk)
		if vm.GuestOS == "" {
			vm.GuestOS = g.GuestFullName
		}
	}
	if s := m.Summary.Storage; s != nil {
		vm.StorageGB = float64(s.Committed) / (1 << 30)
	}
	return vm
}

func resourceAllocation(in *types.ResourceAllocationInfo) *VMResourceAllocation {
	if in == nil {
		return nil
	}
	return &VMResourceAllocation{Reservation: clonePtr(in.Reservation), Limit: clonePtr(in.Limit)}
}

// guestPartitions converts what VMware Tools reports about the guest's
// filesystems. A disk with no path is dropped: RVTools keys vPartition rows on
// it, and a blank one cannot be joined to anything downstream.
func guestPartitions(disks []types.GuestDiskInfo) []VMPartition {
	if len(disks) == 0 {
		return nil
	}
	out := make([]VMPartition, 0, len(disks))
	for _, d := range disks {
		path := strings.TrimSpace(d.DiskPath)
		if path == "" {
			continue
		}
		out = append(out, VMPartition{
			Path:           path,
			DiskKeys:       guestDiskKeys(d.Mappings),
			CapacityBytes:  d.Capacity,
			FreeBytes:      d.FreeSpace,
			FilesystemType: strings.TrimSpace(d.FilesystemType),
		})
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// guestDiskKeys maps a guest filesystem back to the virtual disks behind it.
// It is the join RVTools' vPartition "Disk Key" column exists for, letting a
// reader tie a filesystem's consumed space to the disk that has to be sized
// for it. Tools only reports the mapping on vSphere 7.0 and later, so an
// empty result is an older estate rather than an unbacked filesystem.
func guestDiskKeys(mappings []types.GuestInfoVirtualDiskMapping) []int32 {
	if len(mappings) == 0 {
		return nil
	}
	out := make([]int32, 0, len(mappings))
	for _, m := range mappings {
		out = append(out, m.Key)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// walkDevices normalizes the full hardware device list in one pass. Summary
// fetches never provide this list; full VM captures use it to persist all
// device evidence without a second retrieval.
func walkDevices(devices []types.BaseVirtualDevice, guest *types.GuestInfo, idx *index) ([]VMDisk, []VMNIC, []VMCDROM, []VMUSB) {
	controllers := make(map[int32]deviceController)
	for _, device := range devices {
		if device == nil {
			continue
		}
		if controller, ok := deviceControllerFor(device); ok {
			controllers[device.GetVirtualDevice().Key] = controller
		}
	}
	guestByKey := make(map[int32]types.GuestNicInfo)
	guestByMAC := make(map[string]types.GuestNicInfo)
	if guest != nil {
		for _, nic := range guest.Net {
			if nic.DeviceConfigId != 0 {
				guestByKey[nic.DeviceConfigId] = nic
			}
			if mac := normalizeMAC(nic.MacAddress); mac != "" {
				guestByMAC[mac] = nic
			}
		}
	}
	var disks []VMDisk
	var nics []VMNIC
	var cdroms []VMCDROM
	var usbs []VMUSB
	for _, device := range devices {
		if device == nil {
			continue
		}
		switch d := device.(type) {
		case *types.VirtualDisk:
			disks = append(disks, newVMDisk(d, controllers[d.ControllerKey]))
		case *types.VirtualEthernetCard:
			nics = append(nics, newVMNIC(d, "Ethernet", guestByKey, guestByMAC, idx))
		case *types.VirtualE1000, *types.VirtualE1000e, *types.VirtualPCNet32,
			*types.VirtualSriovEthernetCard, *types.VirtualVmxnet, *types.VirtualVmxnet2,
			*types.VirtualVmxnet3:
			// All concrete ethernet card types embed VirtualEthernetCard but do
			// not share a Go type, so normalize them through their base device.
			nics = append(nics, newVMNICFromDevice(device, guestByKey, guestByMAC, idx))
		case *types.VirtualCdrom:
			cdroms = append(cdroms, newVMCDROM(d, controllers[d.ControllerKey]))
		case *types.VirtualUSB:
			usbs = append(usbs, newVMUSB(d, controllers[d.ControllerKey]))
		}
	}
	sort.Slice(disks, func(i, j int) bool { return disks[i].Key < disks[j].Key })
	sort.Slice(nics, func(i, j int) bool { return nics[i].Key < nics[j].Key })
	sort.Slice(cdroms, func(i, j int) bool { return cdroms[i].Key < cdroms[j].Key })
	sort.Slice(usbs, func(i, j int) bool { return usbs[i].Key < usbs[j].Key })
	return disks, nics, cdroms, usbs
}

// walkMigrationDevices extracts special devices whose presence can constrain
// a migration. It intentionally omits TPM certificate material and keeps the
// result stable for assessment diffs and exports.
func walkMigrationDevices(devices []types.BaseVirtualDevice) ([]VMTPM, []VMPCIDevice, []VMFloppy) {
	var tpms []VMTPM
	var pci []VMPCIDevice
	var floppies []VMFloppy
	for _, device := range devices {
		if device == nil {
			continue
		}
		switch d := device.(type) {
		case *types.VirtualTPM:
			tpms = append(tpms, VMTPM{Key: d.Key, Label: deviceLabel(d.DeviceInfo)})
		case *types.VirtualPCIPassthrough:
			pci = append(pci, newVMPCIDevice(d))
		case *types.VirtualFloppy:
			floppies = append(floppies, newVMFloppy(d))
		}
	}
	sort.Slice(tpms, func(i, j int) bool { return tpms[i].Key < tpms[j].Key })
	sort.Slice(pci, func(i, j int) bool { return pci[i].Key < pci[j].Key })
	sort.Slice(floppies, func(i, j int) bool { return floppies[i].Key < floppies[j].Key })
	return tpms, pci, floppies
}

func newVMPCIDevice(device *types.VirtualPCIPassthrough) VMPCIDevice {
	out := VMPCIDevice{Key: device.Key, Label: deviceLabel(device.DeviceInfo)}
	switch b := device.Backing.(type) {
	case *types.VirtualPCIPassthroughDeviceBackingInfo:
		out.BackingType, out.Address, out.DeviceID, out.SystemID, out.VendorID = "device", b.Id, b.DeviceId, b.SystemId, int32(b.VendorId)
	case *types.VirtualPCIPassthroughDynamicBackingInfo:
		out.BackingType, out.AssignedID = "dynamic", b.AssignedId
	case *types.VirtualPCIPassthroughVmiopBackingInfo:
		out.BackingType, out.VGPU, out.MigrateSupported = "vmiop", b.Vgpu, clonePtr(b.MigrateSupported)
	case *types.VirtualPCIPassthroughDvxBackingInfo:
		out.BackingType = "dvx"
	case *types.VirtualPCIPassthroughPluginBackingInfo:
		out.BackingType = "plugin"
	default:
		out.BackingType = "unknown"
	}
	return out
}

func newVMFloppy(device *types.VirtualFloppy) VMFloppy {
	out := VMFloppy{Key: device.Key, Label: deviceLabel(device.DeviceInfo), UnitNumber: clonePtr(device.UnitNumber)}
	if device.Connectable != nil {
		out.Connected, out.StartsConnected = clonePtr(&device.Connectable.Connected), clonePtr(&device.Connectable.StartConnected)
	}
	b := normalizeBacking(device.Backing)
	out.BackingType, out.BackingPath, out.BackingDevice = b.typeName, b.path, b.device
	out.BackingHost, out.BackingDatastore, out.BackingObjectID, out.UseAutoDetect = b.host, b.datastore, b.objectID, b.useAutoDetect
	return out
}

type deviceController struct {
	typeName, label, sharedBus string
	unitNumber                 *int32
}

func deviceControllerFor(device types.BaseVirtualDevice) (deviceController, bool) {
	base := device.GetVirtualDevice()
	label := ""
	if base.DeviceInfo != nil {
		label = base.DeviceInfo.GetDescription().Label
	}
	switch c := device.(type) {
	case *types.VirtualSCSIController:
		return deviceController{"SCSI", label, string(c.SharedBus), cloneInt32(c.ScsiCtlrUnitNumber)}, true
	case *types.VirtualBusLogicController:
		return deviceController{"BusLogic", label, string(c.SharedBus), cloneInt32(c.ScsiCtlrUnitNumber)}, true
	case *types.VirtualLsiLogicController:
		return deviceController{"LSI Logic", label, string(c.SharedBus), cloneInt32(c.ScsiCtlrUnitNumber)}, true
	case *types.VirtualLsiLogicSASController:
		return deviceController{"LSI Logic SAS", label, string(c.SharedBus), cloneInt32(c.ScsiCtlrUnitNumber)}, true
	case *types.VirtualIDEController:
		return deviceController{"IDE", label, "", nil}, true
	case *types.VirtualSATAController:
		return deviceController{"SATA", label, "", nil}, true
	case *types.VirtualAHCIController:
		return deviceController{"AHCI", label, "", nil}, true
	case *types.VirtualNVMEController:
		return deviceController{"NVMe", label, c.SharedBus, nil}, true
	case *types.VirtualUSBController:
		return deviceController{"USB", label, "", nil}, true
	case *types.VirtualUSBXHCIController:
		return deviceController{"USB 3", label, "", nil}, true
	default:
		return deviceController{}, false
	}
}

// normalizedBacking contains the identity vSphere exposes for a device's
// backing. File-backed devices use Path/Datastore/ObjectID; host-backed
// devices use Device/Host. Keeping all variants in one shape means the JSON
// ledger remains useful without persisting govmomi's polymorphic objects.
type normalizedBacking struct {
	typeName, path, device, host, datastore, objectID string
	useAutoDetect                                     *bool
}

func normalizeBacking(backing types.BaseVirtualDeviceBackingInfo) normalizedBacking {
	var out normalizedBacking
	switch b := backing.(type) {
	case *types.VirtualCdromAtapiBackingInfo:
		out.typeName, out.device, out.useAutoDetect = "atapi", b.DeviceName, clonePtr(b.UseAutoDetect)
	case *types.VirtualCdromIsoBackingInfo:
		out.typeName = "iso"
		out.file(b.VirtualDeviceFileBackingInfo)
	case *types.VirtualCdromPassthroughBackingInfo:
		out.typeName, out.device, out.useAutoDetect = "passthrough", b.DeviceName, clonePtr(b.UseAutoDetect)
	case *types.VirtualCdromRemoteAtapiBackingInfo:
		out.typeName, out.device, out.useAutoDetect = "remoteAtapi", b.DeviceName, clonePtr(b.UseAutoDetect)
	case *types.VirtualCdromRemotePassthroughBackingInfo:
		out.typeName, out.device, out.useAutoDetect = "remotePassthrough", b.DeviceName, clonePtr(b.UseAutoDetect)
	case *types.VirtualUSBUSBBackingInfo:
		out.typeName, out.device, out.useAutoDetect = "usb", b.DeviceName, clonePtr(b.UseAutoDetect)
	case *types.VirtualUSBRemoteClientBackingInfo:
		out.typeName, out.device, out.host, out.useAutoDetect = "remoteClient", b.DeviceName, b.Hostname, clonePtr(b.UseAutoDetect)
	case *types.VirtualUSBRemoteHostBackingInfo:
		out.typeName, out.device, out.host, out.useAutoDetect = "remoteHost", b.DeviceName, b.Hostname, clonePtr(b.UseAutoDetect)
	case *types.VirtualFloppyDeviceBackingInfo:
		out.typeName, out.device, out.useAutoDetect = "device", b.DeviceName, clonePtr(b.UseAutoDetect)
	case *types.VirtualFloppyImageBackingInfo:
		out.typeName = "image"
		out.file(b.VirtualDeviceFileBackingInfo)
	case *types.VirtualFloppyRemoteDeviceBackingInfo:
		out.typeName, out.device, out.useAutoDetect = "remoteDevice", b.DeviceName, clonePtr(b.UseAutoDetect)
	}
	// Preserve useful backing fields even for newer or unknown subclasses.
	if out.path == "" {
		if b, ok := backing.(types.BaseVirtualDeviceFileBackingInfo); ok {
			out.file(*b.GetVirtualDeviceFileBackingInfo())
		}
	}
	if out.device == "" {
		if b, ok := backing.(types.BaseVirtualDeviceDeviceBackingInfo); ok {
			device := b.GetVirtualDeviceDeviceBackingInfo()
			out.device, out.useAutoDetect = device.DeviceName, clonePtr(device.UseAutoDetect)
		}
	}
	return out
}

func (b *normalizedBacking) file(info types.VirtualDeviceFileBackingInfo) {
	b.path, b.datastore = info.FileName, refValue(info.Datastore)
	if info.BackingObjectId != nil {
		b.objectID = *info.BackingObjectId
	}
}

func newVMCDROM(device *types.VirtualCdrom, controller deviceController) VMCDROM {
	out := VMCDROM{
		Key: device.Key, Label: deviceLabel(device.DeviceInfo),
		Controller: controller.typeName, ControllerLabel: controller.label,
		UnitNumber: clonePtr(device.UnitNumber),
	}
	if device.Connectable != nil {
		out.Connected, out.StartsConnected = clonePtr(&device.Connectable.Connected), clonePtr(&device.Connectable.StartConnected)
	}
	b := normalizeBacking(device.Backing)
	out.BackingType, out.BackingPath, out.BackingDevice = b.typeName, b.path, b.device
	out.BackingHost, out.BackingDatastore, out.BackingObjectID, out.UseAutoDetect = b.host, b.datastore, b.objectID, b.useAutoDetect
	return out
}

func newVMUSB(device *types.VirtualUSB, controller deviceController) VMUSB {
	connected := device.Connected
	out := VMUSB{
		Key: device.Key, Label: deviceLabel(device.DeviceInfo), Connected: &connected,
		Vendor: device.Vendor, Product: device.Product,
		Family: append([]string(nil), device.Family...), Speed: append([]string(nil), device.Speed...),
		Controller: controller.typeName, ControllerLabel: controller.label,
		UnitNumber: clonePtr(device.UnitNumber),
	}
	b := normalizeBacking(device.Backing)
	out.BackingType, out.BackingPath, out.BackingDevice = b.typeName, b.path, b.device
	out.BackingHost, out.BackingDatastore, out.BackingObjectID, out.UseAutoDetect = b.host, b.datastore, b.objectID, b.useAutoDetect
	sort.Strings(out.Family)
	sort.Strings(out.Speed)
	return out
}

func newVMDisk(d *types.VirtualDisk, controller deviceController) VMDisk {
	out := VMDisk{Key: d.Key, Label: deviceLabel(d.DeviceInfo), CapacityBytes: d.CapacityInBytes, Controller: controller.typeName, ControllerLabel: controller.label, SharedBus: controller.sharedBus, UnitNumber: clonePtr(d.UnitNumber)}
	if out.CapacityBytes == 0 && d.CapacityInKB > 0 {
		out.CapacityBytes = d.CapacityInKB * 1024
	}
	if d.Shares != nil {
		out.Shares = cloneInt32(d.Shares.Shares)
		out.SharesLevel = string(d.Shares.Level)
	}
	if d.StorageIOAllocation != nil {
		out.Limit = clonePtr(d.StorageIOAllocation.Limit)
		out.Reservation = clonePtr(d.StorageIOAllocation.Reservation)
		if d.StorageIOAllocation.Shares != nil {
			out.Shares = cloneInt32(d.StorageIOAllocation.Shares.Shares)
			out.SharesLevel = string(d.StorageIOAllocation.Shares.Level)
		}
	}
	switch b := d.Backing.(type) {
	case *types.VirtualDiskFlatVer1BackingInfo:
		out.BackingType, out.BackingPath, out.DiskMode = "flatVer1", b.FileName, b.DiskMode
		out.Split, out.WriteThrough = clonePtr(b.Split), clonePtr(b.WriteThrough)
	case *types.VirtualDiskFlatVer2BackingInfo:
		out.BackingType, out.BackingPath, out.DiskMode, out.UUID, out.Sharing = "flatVer2", b.FileName, b.DiskMode, b.Uuid, b.Sharing
		out.ThinProvisioned, out.EagerlyScrub = clonePtr(b.ThinProvisioned), clonePtr(b.EagerlyScrub)
		out.Split, out.WriteThrough = clonePtr(b.Split), clonePtr(b.WriteThrough)
	case *types.VirtualDiskSparseVer1BackingInfo:
		out.BackingType, out.BackingPath, out.DiskMode = "sparseVer1", b.FileName, b.DiskMode
		out.Split, out.WriteThrough = clonePtr(b.Split), clonePtr(b.WriteThrough)
	case *types.VirtualDiskSparseVer2BackingInfo:
		out.BackingType, out.BackingPath, out.DiskMode, out.UUID = "sparseVer2", b.FileName, b.DiskMode, b.Uuid
		out.Split, out.WriteThrough = clonePtr(b.Split), clonePtr(b.WriteThrough)
	case *types.VirtualDiskSeSparseBackingInfo:
		out.BackingType, out.BackingPath, out.DiskMode, out.UUID = "seSparse", b.FileName, b.DiskMode, b.Uuid
		out.WriteThrough = clonePtr(b.WriteThrough)
	case *types.VirtualDiskRawDiskMappingVer1BackingInfo:
		out.BackingType, out.BackingPath, out.DiskMode, out.UUID = "rdm", b.FileName, b.DiskMode, b.Uuid
		out.Raw, out.RawLUNID, out.RawCompatibilityMode, out.Sharing = true, b.LunUuid, b.CompatibilityMode, b.Sharing
	case *types.VirtualDiskRawDiskVer2BackingInfo:
		out.BackingType, out.BackingPath, out.UUID, out.Sharing = "rawVer2", b.DescriptorFileName, b.Uuid, b.Sharing
		out.Raw = true
	case *types.VirtualDiskPartitionedRawDiskVer2BackingInfo:
		out.BackingType, out.BackingPath, out.UUID, out.Sharing = "rawPartitioned", b.DescriptorFileName, b.Uuid, b.Sharing
		out.Raw = true
	}
	if out.BackingPath == "" {
		if b, ok := d.Backing.(types.BaseVirtualDeviceFileBackingInfo); ok {
			out.BackingPath = b.GetVirtualDeviceFileBackingInfo().FileName
		}
	}
	return out
}

func newVMNICFromDevice(device types.BaseVirtualDevice, byKey map[int32]types.GuestNicInfo, byMAC map[string]types.GuestNicInfo, idx *index) VMNIC {
	switch d := device.(type) {
	case *types.VirtualE1000:
		return newVMNIC(&d.VirtualEthernetCard, "E1000", byKey, byMAC, idx)
	case *types.VirtualE1000e:
		return newVMNIC(&d.VirtualEthernetCard, "E1000e", byKey, byMAC, idx)
	case *types.VirtualPCNet32:
		return newVMNIC(&d.VirtualEthernetCard, "PCNet32", byKey, byMAC, idx)
	case *types.VirtualSriovEthernetCard:
		return newVMNIC(&d.VirtualEthernetCard, "SR-IOV", byKey, byMAC, idx)
	case *types.VirtualVmxnet:
		return newVMNIC(&d.VirtualEthernetCard, "Vmxnet", byKey, byMAC, idx)
	case *types.VirtualVmxnet2:
		return newVMNIC(&d.VirtualEthernetCard, "Vmxnet2", byKey, byMAC, idx)
	case *types.VirtualVmxnet3:
		return newVMNIC(&d.VirtualEthernetCard, "Vmxnet3", byKey, byMAC, idx)
	default:
		return VMNIC{}
	}
}

func newVMNIC(card *types.VirtualEthernetCard, adapter string, byKey map[int32]types.GuestNicInfo, byMAC map[string]types.GuestNicInfo, idx *index) VMNIC {
	out := VMNIC{Key: card.Key, Label: deviceLabel(card.DeviceInfo), Adapter: adapter, MACAddress: card.MacAddress, MACAddressType: card.AddressType}
	if card.Connectable != nil {
		out.Connected, out.StartsConnected = clonePtr(&card.Connectable.Connected), clonePtr(&card.Connectable.StartConnected)
	}
	if card.UptCompatibilityEnabled != nil {
		out.UPTCompatible = clonePtr(card.UptCompatibilityEnabled)
	}
	if card.MacAddress != "" {
		if nic, ok := byKey[card.Key]; ok {
			applyGuestNIC(&out, nic)
		} else if nic, ok := byMAC[normalizeMAC(card.MacAddress)]; ok {
			applyGuestNIC(&out, nic)
		}
	}
	if nic, ok := byKey[card.Key]; ok {
		applyGuestNIC(&out, nic)
	}
	applyNetworkBacking(&out, card.Backing, idx)
	return out
}

func applyGuestNIC(out *VMNIC, nic types.GuestNicInfo) {
	if nic.Network != "" {
		out.Network = nic.Network
	}
	if out.MACAddress == "" {
		out.MACAddress = nic.MacAddress
	}
	if out.Connected == nil {
		out.Connected = clonePtr(&nic.Connected)
	}
	for _, address := range nic.IpAddress {
		classifyAddress(out, address)
	}
	if nic.IpConfig != nil {
		for _, address := range nic.IpConfig.IpAddress {
			classifyAddress(out, address.IpAddress)
		}
	}
	sort.Strings(out.IPv4)
	sort.Strings(out.IPv6)
	out.IPv4 = uniqueStrings(out.IPv4)
	out.IPv6 = uniqueStrings(out.IPv6)
}

func applyNetworkBacking(out *VMNIC, backing types.BaseVirtualDeviceBackingInfo, idx *index) {
	switch b := backing.(type) {
	case *types.VirtualEthernetCardNetworkBackingInfo:
		out.NetworkID = refValue(b.Network)
		if out.Network == "" && b.DeviceName != "" {
			out.Network = b.DeviceName
		}
		if out.Network == "" && idx != nil {
			out.Network = idx.name(b.Network)
		}
	case *types.VirtualEthernetCardLegacyNetworkBackingInfo:
		if out.Network == "" {
			out.Network = b.DeviceName
		}
	case *types.VirtualEthernetCardDistributedVirtualPortBackingInfo:
		out.NetworkID, out.SwitchID = b.Port.PortgroupKey, b.Port.SwitchUuid
		if out.Network == "" && idx != nil && b.Port.PortgroupKey != "" {
			ref := types.ManagedObjectReference{Type: "DistributedVirtualPortgroup", Value: b.Port.PortgroupKey}
			out.Network = idx.name(&ref)
		}
	case *types.VirtualEthernetCardOpaqueNetworkBackingInfo:
		out.NetworkID = b.OpaqueNetworkId
		if out.Network == "" {
			out.Network = b.OpaqueNetworkId
		}
	}
}

func deviceLabel(info types.BaseDescription) string {
	if info == nil {
		return ""
	}
	return info.GetDescription().Label
}

func classifyAddress(out *VMNIC, address string) {
	ip := net.ParseIP(strings.TrimSpace(address))
	if ip == nil {
		return
	}
	if ip.To4() != nil {
		out.IPv4 = append(out.IPv4, ip.String())
		return
	}
	out.IPv6 = append(out.IPv6, ip.String())
}

func normalizeMAC(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), ":", ""), "-", ""))
}

func refValue(ref *types.ManagedObjectReference) string {
	if ref == nil {
		return ""
	}
	return ref.Value
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func clonePtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt32(value int32) *int32 { return &value }

func flattenSnapshots(tree []types.VirtualMachineSnapshotTree, parent string, current *types.ManagedObjectReference) []VMSnapshot {
	var out []VMSnapshot
	for i := range tree {
		n := tree[i]
		id := n.Snapshot.Value
		out = append(out, VMSnapshot{
			ID: id, NumericID: n.Id, ParentID: parent, Name: n.Name,
			Description: n.Description, CreateTime: n.CreateTime,
			PowerState: string(n.State), Quiesced: n.Quiesced,
			Current: current != nil && current.Value == id,
		})
		out = append(out, flattenSnapshots(n.ChildSnapshotList, id, current)...)
	}
	return out
}
