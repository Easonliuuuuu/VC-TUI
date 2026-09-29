package vsphere

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/config"
)

func TestWalkDevicesExtractsDisksAndGuestNetworks(t *testing.T) {
	thin, eager, split, writeThrough := true, false, false, true
	connected, starts := true, true
	uptCompatible := true
	controllerDesc := &types.Description{Label: "SCSI controller 0"}
	controller := &types.VirtualLsiLogicController{VirtualSCSIController: types.VirtualSCSIController{
		VirtualController:  types.VirtualController{VirtualDevice: types.VirtualDevice{Key: 100, DeviceInfo: controllerDesc}},
		SharedBus:          types.VirtualSCSISharing("noSharing"),
		ScsiCtlrUnitNumber: 7,
	}}
	disk := &types.VirtualDisk{VirtualDevice: types.VirtualDevice{
		Key: 101, DeviceInfo: &types.Description{Label: "Hard disk 1"}, ControllerKey: 100,
		UnitNumber: &[]int32{0}[0],
		Backing: &types.VirtualDiskFlatVer2BackingInfo{
			VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{FileName: "[datastore-1] app/app.vmdk"},
			DiskMode:                     "persistent", ThinProvisioned: &thin, EagerlyScrub: &eager, Split: &split, WriteThrough: &writeThrough,
			Uuid: "disk-uuid", Sharing: "noSharing",
		},
	}, CapacityInBytes: 8 << 30}
	card := types.VirtualEthernetCard{
		VirtualDevice: types.VirtualDevice{Key: 200, DeviceInfo: &types.Description{Label: "Network adapter 1"}, Connectable: &types.VirtualDeviceConnectInfo{Connected: connected, StartConnected: starts}},
		AddressType:   "assigned", MacAddress: "00:50:56:AA:BB:CC",
	}
	card.Backing = &types.VirtualEthernetCardNetworkBackingInfo{VirtualDeviceDeviceBackingInfo: types.VirtualDeviceDeviceBackingInfo{DeviceName: "VM Network"}, Network: &types.ManagedObjectReference{Type: "Network", Value: "network-1"}}
	card.UptCompatibilityEnabled = &uptCompatible
	nic := &types.VirtualVmxnet3{VirtualVmxnet: types.VirtualVmxnet{VirtualEthernetCard: card}}
	sriov := &types.VirtualSriovEthernetCard{VirtualEthernetCard: types.VirtualEthernetCard{
		VirtualDevice: types.VirtualDevice{Key: 201, DeviceInfo: &types.Description{Label: "Network adapter 2"}},
		AddressType:   "assigned", MacAddress: "00:50:56:AA:BB:DD",
	}}
	guest := &types.GuestInfo{Net: []types.GuestNicInfo{{DeviceConfigId: 200, Network: "guest-portgroup", MacAddress: "00:50:56:aa:bb:cc", IpAddress: []string{"192.0.2.20", "2001:db8::20"}}}}
	idx := &index{byRef: map[types.ManagedObjectReference]entity{{Type: "Network", Value: "network-1"}: {name: "VM Network"}}}

	disks, nics, _, _ := walkDevices([]types.BaseVirtualDevice{nic, sriov, disk, controller}, guest, idx)
	if len(disks) != 1 || len(nics) != 2 {
		t.Fatalf("devices = %d disks, %d nics; want one disk and two nics", len(disks), len(nics))
	}
	gotDisk := disks[0]
	if gotDisk.Label != "Hard disk 1" || gotDisk.Controller != "LSI Logic" || gotDisk.ControllerLabel != "SCSI controller 0" || gotDisk.SharedBus != "noSharing" || gotDisk.BackingPath == "" || gotDisk.UUID != "disk-uuid" {
		t.Errorf("disk normalization = %+v", gotDisk)
	}
	if gotDisk.ThinProvisioned == nil || !*gotDisk.ThinProvisioned || gotDisk.EagerlyScrub == nil || *gotDisk.EagerlyScrub {
		t.Errorf("disk optional flags = %+v", gotDisk)
	}
	gotNIC := nics[0]
	if gotNIC.Adapter != "Vmxnet3" || gotNIC.Network != "guest-portgroup" || gotNIC.NetworkID != "network-1" || gotNIC.MACAddress != "00:50:56:AA:BB:CC" {
		t.Errorf("nic normalization = %+v", gotNIC)
	}
	if !reflect.DeepEqual(gotNIC.IPv4, []string{"192.0.2.20"}) || !reflect.DeepEqual(gotNIC.IPv6, []string{"2001:db8::20"}) {
		t.Errorf("guest addresses = v4 %v, v6 %v", gotNIC.IPv4, gotNIC.IPv6)
	}
	if gotNIC.Connected == nil || !*gotNIC.Connected || gotNIC.StartsConnected == nil || !*gotNIC.StartsConnected {
		t.Errorf("nic connection flags = %+v", gotNIC)
	}
	if gotNIC.UPTCompatible == nil || !*gotNIC.UPTCompatible {
		t.Errorf("nic UPT compatibility = %+v", gotNIC.UPTCompatible)
	}
	if nics[1].Adapter != "SR-IOV" {
		t.Errorf("SR-IOV nic normalization = %+v", nics[1])
	}
}

func TestWalkDevicesNormalizesCDROMAndUSBAndExcludesControllers(t *testing.T) {
	connected, starts, autodetect := true, true, false
	cdrom := &types.VirtualCdrom{VirtualDevice: types.VirtualDevice{
		Key: 401, DeviceInfo: &types.Description{Label: "CD/DVD drive 1"}, ControllerKey: 100,
		Connectable: &types.VirtualDeviceConnectInfo{Connected: connected, StartConnected: starts},
		Backing: &types.VirtualCdromIsoBackingInfo{VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
			FileName: "[datastore-1] images/installer.iso", Datastore: &types.ManagedObjectReference{Type: "Datastore", Value: "datastore-1"}, BackingObjectId: &[]string{"backing-1"}[0],
		}},
	}}
	usb := &types.VirtualUSB{VirtualDevice: types.VirtualDevice{
		Key: 501, DeviceInfo: &types.Description{Label: "USB device 1"}, ControllerKey: 300,
		Backing: &types.VirtualUSBRemoteHostBackingInfo{VirtualDeviceDeviceBackingInfo: types.VirtualDeviceDeviceBackingInfo{DeviceName: "vid:1234 pid:5678", UseAutoDetect: &autodetect}, Hostname: "esx-1"},
	}, Connected: true, Vendor: 0x1234, Product: 0x5678, Family: []string{"storage", "hid"}, Speed: []string{"high", "full"}}
	cdController := &types.VirtualIDEController{VirtualController: types.VirtualController{VirtualDevice: types.VirtualDevice{Key: 100, DeviceInfo: &types.Description{Label: "IDE controller 0"}}}}
	usbController := &types.VirtualUSBController{VirtualController: types.VirtualController{VirtualDevice: types.VirtualDevice{Key: 300, DeviceInfo: &types.Description{Label: "USB controller 0"}}}}

	disks, nics, cdroms, usbs := walkDevices([]types.BaseVirtualDevice{usb, usbController, cdrom, cdController}, nil, nil)
	if len(disks) != 0 || len(nics) != 0 || len(cdroms) != 1 || len(usbs) != 1 {
		t.Fatalf("normalized devices = %d disks, %d NICs, %d CD-ROMs, %d USBs", len(disks), len(nics), len(cdroms), len(usbs))
	}
	gotCD := cdroms[0]
	if gotCD.Label != "CD/DVD drive 1" || gotCD.BackingType != "iso" || gotCD.BackingPath == "" || gotCD.BackingDatastore != "datastore-1" || gotCD.BackingObjectID != "backing-1" {
		t.Errorf("CD-ROM normalization = %+v", gotCD)
	}
	if gotCD.Connected == nil || !*gotCD.Connected || gotCD.StartsConnected == nil || !*gotCD.StartsConnected || gotCD.Controller != "IDE" {
		t.Errorf("CD-ROM connection/controller = %+v", gotCD)
	}
	gotUSB := usbs[0]
	if gotUSB.Label != "USB device 1" || gotUSB.BackingType != "remoteHost" || gotUSB.BackingDevice != "vid:1234 pid:5678" || gotUSB.BackingHost != "esx-1" || gotUSB.Vendor != 0x1234 || gotUSB.Product != 0x5678 || gotUSB.Controller != "USB" {
		t.Errorf("USB normalization = %+v", gotUSB)
	}
	if gotUSB.Connected == nil || !*gotUSB.Connected || gotUSB.UseAutoDetect == nil || *gotUSB.UseAutoDetect || !reflect.DeepEqual(gotUSB.Family, []string{"hid", "storage"}) || !reflect.DeepEqual(gotUSB.Speed, []string{"full", "high"}) {
		t.Errorf("USB state/identity = %+v", gotUSB)
	}
}

func TestWalkMigrationDevicesNormalizesSpecialHardware(t *testing.T) {
	connected := true
	tpm := &types.VirtualTPM{VirtualDevice: types.VirtualDevice{Key: 601, DeviceInfo: &types.Description{Label: "TPM"}}}
	pci := &types.VirtualPCIPassthrough{VirtualDevice: types.VirtualDevice{
		Key: 602, DeviceInfo: &types.Description{Label: "GPU"},
		Backing: &types.VirtualPCIPassthroughDeviceBackingInfo{
			VirtualDeviceDeviceBackingInfo: types.VirtualDeviceDeviceBackingInfo{DeviceName: "0000:65:00.0"},
			Id:                             "65:00.0", DeviceId: "1db6", SystemId: "host-1", VendorId: 0x10de,
		},
	}}
	floppy := &types.VirtualFloppy{VirtualDevice: types.VirtualDevice{
		Key: 603, DeviceInfo: &types.Description{Label: "Floppy"},
		Connectable: &types.VirtualDeviceConnectInfo{Connected: connected, StartConnected: connected},
		Backing:     &types.VirtualFloppyImageBackingInfo{VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{FileName: "[ds] app/boot.img"}},
	}}
	vmiop := &types.VirtualPCIPassthrough{VirtualDevice: types.VirtualDevice{
		Key: 604, DeviceInfo: &types.Description{Label: "vGPU"},
		Backing: &types.VirtualPCIPassthroughVmiopBackingInfo{Vgpu: "grid_v100-4q"},
	}}
	tpms, pciDevices, floppies := walkMigrationDevices([]types.BaseVirtualDevice{floppy, pci, tpm, vmiop})
	if len(tpms) != 1 || tpms[0].Key != 601 || tpms[0].Label != "TPM" {
		t.Fatalf("TPM evidence = %+v", tpms)
	}
	if len(pciDevices) != 2 || pciDevices[0].BackingType != "device" || pciDevices[0].Address != "65:00.0" || pciDevices[0].DeviceID != "1db6" || pciDevices[0].VendorID != 0x10de {
		t.Fatalf("PCI evidence = %+v", pciDevices)
	}
	if pciDevices[1].BackingType != "vmiop" || pciDevices[1].VGPU != "grid_v100-4q" {
		t.Fatalf("vGPU evidence = %+v", pciDevices[1])
	}
	if len(floppies) != 1 || floppies[0].BackingType != "image" || floppies[0].BackingPath != "[ds] app/boot.img" || floppies[0].Connected == nil || !*floppies[0].Connected {
		t.Fatalf("floppy evidence = %+v", floppies)
	}
}

func TestNewVMCopiesMigrationConfiguration(t *testing.T) {
	secure, auto, locked := true, false, true
	cores := int32(2)
	cpuReservation, cpuLimit := int64(100), int64(500)
	memReservation, memLimit := int64(256), int64(-1)
	m := &mo.VirtualMachine{Config: &types.VirtualMachineConfigInfo{
		GuestId: "ubuntu64Guest", Firmware: "efi", ManagedBy: &types.ManagedByInfo{ExtensionKey: "com.example", Type: "appliance"},
		Hardware:                     types.VirtualHardware{NumCPU: 4, NumCoresPerSocket: &cores, AutoCoresPerSocket: &auto, Device: []types.BaseVirtualDevice{}},
		BootOptions:                  &types.VirtualMachineBootOptions{EfiSecureBootEnabled: &secure},
		CpuAllocation:                &types.ResourceAllocationInfo{Reservation: &cpuReservation, Limit: &cpuLimit},
		MemoryAllocation:             &types.ResourceAllocationInfo{Reservation: &memReservation, Limit: &memLimit},
		MemoryReservationLockedToMax: &locked,
	}}
	vm := newVM(&Client{Context: &config.Context{Name: "prod"}}, &index{}, m)
	if !vm.ConfigurationAvailable || vm.GuestID != "ubuntu64Guest" || vm.Firmware != "efi" || vm.SecureBootEnabled == nil || !*vm.SecureBootEnabled || vm.CoresPerSocket != 2 || vm.CPUSockets != 2 || vm.AutoCoresPerSocket == nil || vm.CPUAllocation == nil || *vm.CPUAllocation.Reservation != 100 || vm.MemoryAllocation == nil || *vm.MemoryAllocation.Limit != -1 || vm.ManagedBy == nil || vm.ManagedBy.ExtensionKey != "com.example" || vm.MemoryReservationLockedToMax == nil || !*vm.MemoryReservationLockedToMax {
		t.Fatalf("VM migration configuration = %+v", vm)
	}
}

func TestVMPropertiesIncludeDeviceWalk(t *testing.T) {
	for _, property := range []string{"config.guestId", "config.hardware.device", "config.hardware.numCoresPerSocket", "config.hardware.autoCoresPerSocket", "config.cpuAllocation", "config.memoryAllocation", "config.bootOptions", "config.firmware", "config.managedBy", "config.memoryReservationLockedToMax", "guest.net", "guest.disk", "guest.toolsVersion", "guest.toolsVersionStatus2"} {
		found := false
		for _, candidate := range vmProps {
			if candidate == property {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("vmProps does not include %q: %v", property, vmProps)
		}
	}
}

func TestNewVMCopiesToolsVersion(t *testing.T) {
	m := &mo.VirtualMachine{
		Guest: &types.GuestInfo{
			ToolsRunningStatus:  "guestToolsRunning",
			ToolsVersion:        "12352",
			ToolsVersionStatus2: "guestToolsCurrent",
		},
	}
	vm := newVM(&Client{Context: &config.Context{Name: "prod"}}, &index{}, m)
	if vm.ToolsState != "guestToolsRunning" {
		t.Errorf("tools state = %q", vm.ToolsState)
	}
	if vm.ToolsVersion != "12352" {
		t.Errorf("tools version = %q", vm.ToolsVersion)
	}
	if vm.ToolsVersionStatus != "guestToolsCurrent" {
		t.Errorf("tools version status = %q", vm.ToolsVersionStatus)
	}
}

func TestNewVMCopiesGuestPartitions(t *testing.T) {
	m := &mo.VirtualMachine{
		Guest: &types.GuestInfo{
			ToolsRunningStatus: "guestToolsRunning",
			Disk: []types.GuestDiskInfo{
				// Out of order, and one with no path: vPartition rows are
				// keyed on the path downstream, so a blank one is dropped and
				// the rest sort so repeated exports stay byte-identical.
				{DiskPath: "/var", Capacity: 4 << 30, FreeSpace: 1 << 30, FilesystemType: "xfs"},
				{Capacity: 1 << 30},
				{DiskPath: "/", Capacity: 8 << 30, FreeSpace: 2 << 30, FilesystemType: "ext4"},
			},
		},
	}
	vm := newVM(&Client{Context: &config.Context{Name: "prod"}}, &index{}, m)
	want := []VMPartition{
		{Path: "/", CapacityBytes: 8 << 30, FreeBytes: 2 << 30, FilesystemType: "ext4"},
		{Path: "/var", CapacityBytes: 4 << 30, FreeBytes: 1 << 30, FilesystemType: "xfs"},
	}
	if !reflect.DeepEqual(vm.Partitions, want) {
		t.Errorf("partitions = %+v, want %+v", vm.Partitions, want)
	}
}

// The mapping back to virtual disks is what lets a sizing tool tie consumed
// space to the disk it has to provision. Tools reports it only on vSphere 7.0
// and later, so a partition without one is an older estate, not a filesystem
// with no disk behind it — and a spanned volume names every disk it crosses.
func TestNewVMCopiesGuestPartitionDiskKeys(t *testing.T) {
	m := &mo.VirtualMachine{
		Guest: &types.GuestInfo{
			ToolsRunningStatus: "guestToolsRunning",
			Disk: []types.GuestDiskInfo{
				{DiskPath: "/", Capacity: 8 << 30, Mappings: []types.GuestInfoVirtualDiskMapping{{Key: 2000}}},
				// Out of order, so repeated exports stay byte-identical.
				{DiskPath: "/data", Capacity: 4 << 30, Mappings: []types.GuestInfoVirtualDiskMapping{{Key: 2003}, {Key: 2001}}},
				{DiskPath: "/legacy", Capacity: 1 << 30},
			},
		},
	}
	vm := newVM(&Client{Context: &config.Context{Name: "prod"}}, &index{}, m)
	want := [][]int32{{2000}, {2001, 2003}, nil}
	for i, part := range vm.Partitions {
		if !reflect.DeepEqual(part.DiskKeys, want[i]) {
			t.Errorf("%s disk keys = %v, want %v", part.Path, part.DiskKeys, want[i])
		}
	}
}

// A guest without running Tools reports nothing. That must stay nil rather
// than becoming a zero-capacity partition, which would read downstream as a
// full disk rather than as an unanswered one.
func TestNewVMLeavesPartitionsNilWithoutGuestData(t *testing.T) {
	for _, tc := range []struct {
		name  string
		guest *types.GuestInfo
	}{
		{name: "no guest at all", guest: nil},
		{name: "tools not running", guest: &types.GuestInfo{ToolsRunningStatus: "guestToolsNotRunning"}},
		{name: "only unnamed disks", guest: &types.GuestInfo{Disk: []types.GuestDiskInfo{{Capacity: 1 << 30}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm := newVM(&Client{Context: &config.Context{Name: "prod"}}, &index{}, &mo.VirtualMachine{Guest: tc.guest})
			if vm.Partitions != nil {
				t.Errorf("partitions = %+v, want nil", vm.Partitions)
			}
		})
	}
}

func TestVMPartitionUsedBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		part VMPartition
		want int64
	}{
		{name: "capacity minus free", part: VMPartition{CapacityBytes: 8 << 30, FreeBytes: 2 << 30}, want: 6 << 30},
		{name: "unsized partition", part: VMPartition{}, want: 0},
		// Tools has been seen reporting free space above capacity; consumed
		// must not go negative and skew a sizing total.
		{name: "free exceeds capacity", part: VMPartition{CapacityBytes: 1 << 30, FreeBytes: 2 << 30}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.part.UsedBytes(); got != tc.want {
				t.Errorf("UsedBytes() = %d, want %d", got, tc.want)
			}
		})
	}
}

// guest.hostName is chosen by whoever controls the guest and is later handed
// to ssh(1), so only plain DNS-style names may survive collection.
func TestGuestHostName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ubuntu2204", "ubuntu2204"},
		{"web-01.corp.example.com", "web-01.corp.example.com"},
		{"  web-01.corp.example.com.  ", "web-01.corp.example.com"},
		{"db_1.lab", "db_1.lab"},
		{"", ""},
		{"-oProxyCommand=touch", ""},
		{"-web", ""},
		{"web-", ""},
		{"a..b", ""},
		{"has space", ""},
		{"evil\x1b[2J", ""},
		{"user@host", ""},
		{strings.Repeat("a", 64), ""},
		{strings.Repeat("a.", 130), ""},
	} {
		if got := guestHostName(tc.in); got != tc.want {
			t.Errorf("guestHostName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewVMCopiesGuestHostName(t *testing.T) {
	m := &mo.VirtualMachine{Guest: &types.GuestInfo{HostName: "ubuntu2204", IpAddress: "192.0.2.10"}}
	vm := newVM(&Client{Context: &config.Context{Name: "prod"}}, &index{}, m)
	if vm.GuestHostName != "ubuntu2204" || vm.IPAddress != "192.0.2.10" {
		t.Errorf("hostname/ip = %q/%q", vm.GuestHostName, vm.IPAddress)
	}
	m.Guest.HostName = "-oProxyCommand=x"
	if vm := newVM(&Client{Context: &config.Context{Name: "prod"}}, &index{}, m); vm.GuestHostName != "" {
		t.Errorf("unsafe hostname kept: %q", vm.GuestHostName)
	}
}

func TestApplyNetworkBackingResolvesDistributedPortgroupName(t *testing.T) {
	backing := &types.VirtualEthernetCardDistributedVirtualPortBackingInfo{
		Port: types.DistributedVirtualSwitchPortConnection{PortgroupKey: "dvportgroup-1005", SwitchUuid: "50 1c aa"},
	}
	// The generic entity walk knows nothing about the port group; only the
	// dedicated port group map does.
	idx := &index{byRef: map[types.ManagedObjectReference]entity{}, portgroups: map[string]string{"dvportgroup-1005": "dvpg-vlan100"}}
	var nic VMNIC
	applyNetworkBacking(&nic, backing, idx)
	if nic.Network != "dvpg-vlan100" || nic.NetworkID != "dvportgroup-1005" || nic.SwitchID != "50 1c aa" {
		t.Fatalf("nic = %+v", nic)
	}

	// Resolved through the entity walk when that has it.
	idx = &index{byRef: map[types.ManagedObjectReference]entity{
		{Type: "DistributedVirtualPortgroup", Value: "dvportgroup-1005"}: {name: "walk-name"},
	}}
	nic = VMNIC{}
	applyNetworkBacking(&nic, backing, idx)
	if nic.Network != "walk-name" {
		t.Fatalf("nic = %+v", nic)
	}
}
