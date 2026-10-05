package vsphere

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind names a class of inventory object. Search results and the interface's
// resource tabs are both keyed on it.
type Kind string

// Inventory kinds.
const (
	KindVM        Kind = "vm"
	KindTemplate  Kind = "template"
	KindHost      Kind = "host"
	KindCluster   Kind = "cluster"
	KindVApp      Kind = "vapp"
	KindDatastore Kind = "datastore"
	KindNetwork   Kind = "network"
	// KindResourcePool is capture-only: resource pools are persisted for
	// assessment exports, but are not a browsable inventory kind yet.
	KindResourcePool Kind = "resourcepool"
	// KindDVSwitch is capture-only: distributed switches are persisted for
	// assessment exports, but are not a browsable inventory kind yet.
	KindDVSwitch Kind = "dvswitch"
)

// AllKinds lists every browsable kind the inventory API can enumerate.
var AllKinds = []Kind{KindVM, KindTemplate, KindHost, KindCluster, KindDatastore, KindNetwork, KindVApp}

// ParseKind maps a user-supplied string onto a Kind, tolerating plurals.
// KindResourcePool is intentionally omitted because it is capture-only.
func ParseKind(s string) (Kind, error) {
	switch s {
	case "vm", "vms", "virtualmachine", "virtualmachines":
		return KindVM, nil
	case "template", "templates":
		return KindTemplate, nil
	case "host", "hosts", "esxi":
		return KindHost, nil
	case "cluster", "clusters":
		return KindCluster, nil
	case "vapp", "vapps", "virtualapp", "virtualapps":
		return KindVApp, nil
	case "datastore", "datastores", "ds":
		return KindDatastore, nil
	case "network", "networks", "portgroup", "portgroups":
		return KindNetwork, nil
	default:
		return "", fmt.Errorf("unknown resource kind %q (supported: vm, template, host, cluster, vapp, datastore, network)", s)
	}
}

// Location says where in the world an object lives. Every domain object embeds
// it, because with several vCenters in play "which one" is part of identity.
type Location struct {
	// Context is the vsfleet context name, i.e. which vCenter.
	Context string `json:"context"`
	// Datacenter is the vSphere datacenter the object belongs to.
	Datacenter string `json:"datacenter"`
	// Path is the full inventory path, e.g. /Taipei/vm/Templates/ubuntu.
	Path string `json:"path"`
}

// Metadata source states. An object records available, unavailable, denied or
// unsupported for each source. Partial and not_applicable describe a whole
// collection: some of its objects were readable and some were not, or it held
// no objects to read. NotRecorded is what a reader reports for evidence that
// carries no status at all, such as a capture taken before metadata existed.
// Only available is complete; every other state means a predicate on that
// source cannot be decided.
const (
	MetadataAvailable     = "available"
	MetadataUnavailable   = "unavailable"
	MetadataDenied        = "denied"
	MetadataUnsupported   = "unsupported"
	MetadataPartial       = "partial"
	MetadataNotApplicable = "not_applicable"
	MetadataNotRecorded   = "not_recorded"
)

// MetadataStatus normalizes a recorded source status, mapping the empty value
// written by older captures to MetadataNotRecorded.
func MetadataStatus(status string) string {
	if status == "" {
		return MetadataNotRecorded
	}
	return status
}

// Metadata is the normalized operator-owned metadata attached to an
// inventory object. Status is kept separately for tags and custom attributes
// because vCenter installations commonly expose one source but not the other.
// An unavailable source is never equivalent to an empty source.
type Metadata struct {
	Tags                   []Tag             `json:"tags"`
	CustomAttributes       []CustomAttribute `json:"custom_attributes"`
	TagsStatus             string            `json:"tags_status"`
	TagsError              string            `json:"tags_error,omitempty"`
	CustomAttributesStatus string            `json:"custom_attributes_status"`
	CustomAttributesError  string            `json:"custom_attributes_error,omitempty"`
}

// Tag is a tag attachment with enough identity to distinguish same-named
// tags in different categories.
type Tag struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CategoryID string `json:"category_id"`
	Category   string `json:"category"`
}

// CustomAttribute is a vSphere custom field value. Key remains stable when a
// field is renamed, so historical comparisons do not mistake a rename for a
// remove/add pair.
type CustomAttribute struct {
	Key   int32  `json:"key"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// VM is a virtual machine or, when IsTemplate is set, a template. vSphere
// models both with the same managed object, and so does this package: the
// template views filter on IsTemplate rather than using a parallel type.
type VM struct {
	Location
	Metadata           Metadata `json:"metadata"`
	ID                 string   `json:"id"`
	InstanceUUID       string   `json:"instance_uuid,omitempty"`
	BIOSUUID           string   `json:"bios_uuid,omitempty"`
	GuestID            string   `json:"guest_id,omitempty"`
	Name               string   `json:"name"`
	PowerState         string   `json:"power_state"`
	ConnectionState    string   `json:"connection_state"`
	IsTemplate         bool     `json:"is_template"`
	CPU                int32    `json:"cpu"`
	MemoryMB           int64    `json:"memory_mb"`
	GuestOS            string   `json:"guest_os"`
	GuestState         string   `json:"guest_state"`
	ToolsState         string   `json:"tools_state"`
	ToolsVersion       string   `json:"tools_version,omitempty"`
	ToolsVersionStatus string   `json:"tools_version_status,omitempty"`
	IPAddress          string   `json:"ip_address"`
	// GuestHostName is the name VMware Tools reports for the guest (the
	// "DNS Name" on the vSphere Summary tab). It is what the guest believes
	// it is called, so it is not guaranteed to resolve from anywhere else,
	// and it is empty when Tools is not running or reports something that is
	// not a plain host name — see guestHostName.
	GuestHostName string   `json:"guest_hostname,omitempty"`
	Host          string   `json:"host"`
	Cluster       string   `json:"cluster"`
	Folder        string   `json:"folder"`
	Datastores    []string `json:"datastores"`
	StorageGB     float64  `json:"storage_gb"`
	Annotation    string   `json:"annotation"`
	// ConfigurationAvailable distinguishes a VM whose full configuration was
	// collected from one for which vSphere returned only summary properties.
	// Migration rules must treat false as missing evidence, not as an empty
	// configuration.
	ConfigurationAvailable       bool                  `json:"configuration_available,omitempty"`
	Firmware                     string                `json:"firmware,omitempty"`
	SecureBootEnabled            *bool                 `json:"secure_boot_enabled,omitempty"`
	CoresPerSocket               int32                 `json:"cores_per_socket,omitempty"`
	CPUSockets                   int32                 `json:"cpu_sockets,omitempty"`
	AutoCoresPerSocket           *bool                 `json:"auto_cores_per_socket,omitempty"`
	CPUAllocation                *VMResourceAllocation `json:"cpu_allocation,omitempty"`
	MemoryAllocation             *VMResourceAllocation `json:"memory_allocation,omitempty"`
	MemoryReservationLockedToMax *bool                 `json:"memory_reservation_locked_to_max,omitempty"`
	ManagedBy                    *VMManagedBy          `json:"managed_by,omitempty"`
	Disks                        []VMDisk              `json:"disks,omitempty"`
	NICs                         []VMNIC               `json:"nics,omitempty"`
	CDROMs                       []VMCDROM             `json:"cdroms,omitempty"`
	USBs                         []VMUSB               `json:"usbs,omitempty"`
	TPMs                         []VMTPM               `json:"tpms,omitempty"`
	PCIDevices                   []VMPCIDevice         `json:"pci_devices,omitempty"`
	Floppies                     []VMFloppy            `json:"floppies,omitempty"`
	Snapshots                    []VMSnapshot          `json:"snapshots,omitempty"`
	Partitions                   []VMPartition         `json:"partitions,omitempty"`
}

// VMResourceAllocation records the VM-level CPU or memory reservation and
// limit. vSphere uses -1 for an unlimited limit; pointers preserve the
// distinction between a default that was returned and unavailable evidence.
type VMResourceAllocation struct {
	Reservation *int64 `json:"reservation,omitempty"`
	Limit       *int64 `json:"limit,omitempty"`
}

// VMManagedBy identifies the vCenter extension that owns a VM lifecycle. It
// is deliberately metadata only; vsfleet does not infer vendor support.
type VMManagedBy struct {
	ExtensionKey string `json:"extension_key,omitempty"`
	Type         string `json:"type,omitempty"`
}

// VMTPM identifies a virtual TPM without persisting endorsement certificates.
type VMTPM struct {
	Key   int32  `json:"key"`
	Label string `json:"label,omitempty"`
}

// VMPCIDevice is a normalized host-device passthrough or vGPU attachment.
type VMPCIDevice struct {
	Key              int32  `json:"key"`
	Label            string `json:"label,omitempty"`
	BackingType      string `json:"backing_type,omitempty"`
	Address          string `json:"address,omitempty"`
	DeviceID         string `json:"device_id,omitempty"`
	SystemID         string `json:"system_id,omitempty"`
	VendorID         int32  `json:"vendor_id,omitempty"`
	AssignedID       string `json:"assigned_id,omitempty"`
	VGPU             string `json:"vgpu,omitempty"`
	MigrateSupported *bool  `json:"migrate_supported,omitempty"`
}

// VMFloppy is a normalized virtual floppy attachment. Its backing fields
// match CD-ROM/USB evidence so operators can see whether a legacy image or
// host device remains attached.
type VMFloppy struct {
	Key              int32  `json:"key"`
	Label            string `json:"label,omitempty"`
	Connected        *bool  `json:"connected,omitempty"`
	StartsConnected  *bool  `json:"starts_connected,omitempty"`
	BackingType      string `json:"backing_type,omitempty"`
	BackingPath      string `json:"backing_path,omitempty"`
	BackingDevice    string `json:"backing_device,omitempty"`
	BackingHost      string `json:"backing_host,omitempty"`
	BackingDatastore string `json:"backing_datastore,omitempty"`
	BackingObjectID  string `json:"backing_object_id,omitempty"`
	UseAutoDetect    *bool  `json:"use_auto_detect,omitempty"`
	Controller       string `json:"controller,omitempty"`
	ControllerLabel  string `json:"controller_label,omitempty"`
	UnitNumber       *int32 `json:"unit_number,omitempty"`
}

// VMPartition is one guest filesystem as VMware Tools reports it, which is
// the only source for it: vSphere knows how large a virtual disk is, but only
// the guest knows how much of it is used. A VM with no running Tools reports
// no partitions at all, so an empty slice is missing evidence rather than a
// machine with no filesystems — CapacityBytes of zero would be a lie, where
// absence is not. Callers distinguish the two through the run's collection
// status, not by counting rows.
type VMPartition struct {
	Path string `json:"path"`
	// DiskKeys names the virtual disks backing this filesystem, joining to
	// VMDisk.Key. VMware Tools reports the mapping only on vSphere 7.0 and
	// later, and a volume spanning several disks names each of them, so both
	// none and many are ordinary. Absence is not disk key zero, which is a
	// device a VM could really have.
	DiskKeys       []int32 `json:"disk_keys,omitempty"`
	CapacityBytes  int64   `json:"capacity_bytes"`
	FreeBytes      int64   `json:"free_bytes"`
	FilesystemType string  `json:"filesystem_type,omitempty"`
}

// UsedBytes is what the guest has consumed on this filesystem. vSphere
// reports capacity and free space, never used, and a guest that reports free
// space larger than capacity would otherwise produce a negative number.
func (p VMPartition) UsedBytes() int64 {
	if used := p.CapacityBytes - p.FreeBytes; used > 0 {
		return used
	}
	return 0
}

// VMDisk is one virtual disk from a VM's hardware configuration. Optional
// values remain nil when vSphere does not expose them for a backing type.
type VMDisk struct {
	Key                  int32  `json:"key"`
	Label                string `json:"label"`
	CapacityBytes        int64  `json:"capacity_bytes"`
	UUID                 string `json:"uuid,omitempty"`
	BackingType          string `json:"backing_type,omitempty"`
	BackingPath          string `json:"backing_path,omitempty"`
	Raw                  bool   `json:"raw"`
	DiskMode             string `json:"disk_mode,omitempty"`
	Sharing              string `json:"sharing,omitempty"`
	ThinProvisioned      *bool  `json:"thin_provisioned,omitempty"`
	EagerlyScrub         *bool  `json:"eagerly_scrub,omitempty"`
	Split                *bool  `json:"split,omitempty"`
	WriteThrough         *bool  `json:"write_through,omitempty"`
	SharesLevel          string `json:"shares_level,omitempty"`
	Shares               *int32 `json:"shares,omitempty"`
	Reservation          *int32 `json:"reservation,omitempty"`
	Limit                *int64 `json:"limit,omitempty"`
	Controller           string `json:"controller,omitempty"`
	ControllerLabel      string `json:"controller_label,omitempty"`
	UnitNumber           *int32 `json:"unit_number,omitempty"`
	SharedBus            string `json:"shared_bus,omitempty"`
	RawLUNID             string `json:"raw_lun_id,omitempty"`
	RawCompatibilityMode string `json:"raw_compatibility_mode,omitempty"`
}

// VMNIC is one virtual ethernet adapter and its guest-reported network data.
// UPTCompatible is the VMXNET3 UPT capability flag reported by vSphere; it is
// not evidence of a host-device binding. See VMPCIDevice for passthrough
// evidence. NetworkID is retained for joins when a display name is unavailable.
type VMNIC struct {
	Key             int32    `json:"key"`
	Label           string   `json:"label"`
	Adapter         string   `json:"adapter,omitempty"`
	Network         string   `json:"network,omitempty"`
	NetworkID       string   `json:"network_id,omitempty"`
	SwitchID        string   `json:"switch_id,omitempty"`
	MACAddress      string   `json:"mac_address,omitempty"`
	MACAddressType  string   `json:"mac_address_type,omitempty"`
	Connected       *bool    `json:"connected,omitempty"`
	StartsConnected *bool    `json:"starts_connected,omitempty"`
	UPTCompatible   *bool    `json:"upt_compatibility_enabled,omitempty"`
	IPv4            []string `json:"ipv4,omitempty"`
	IPv6            []string `json:"ipv6,omitempty"`
}

// VMCDROM is one virtual CD-ROM from a VM's hardware configuration. The
// connection flags are pointers because vSphere omits connectable state for
// devices where it cannot report a current state (for example, a powered-off
// VM); nil is therefore different from a known disconnected device.
type VMCDROM struct {
	Key              int32  `json:"key"`
	Label            string `json:"label"`
	Connected        *bool  `json:"connected,omitempty"`
	StartsConnected  *bool  `json:"starts_connected,omitempty"`
	BackingType      string `json:"backing_type,omitempty"`
	BackingPath      string `json:"backing_path,omitempty"`
	BackingDevice    string `json:"backing_device,omitempty"`
	BackingHost      string `json:"backing_host,omitempty"`
	BackingDatastore string `json:"backing_datastore,omitempty"`
	BackingObjectID  string `json:"backing_object_id,omitempty"`
	UseAutoDetect    *bool  `json:"use_auto_detect,omitempty"`
	Controller       string `json:"controller,omitempty"`
	ControllerLabel  string `json:"controller_label,omitempty"`
	UnitNumber       *int32 `json:"unit_number,omitempty"`
}

// VMUSB is one attached virtual USB device. USB controllers are intentionally
// not represented here: they are infrastructure for USB attachments, not
// devices an operator can accidentally leave connected to a guest.
type VMUSB struct {
	Key              int32    `json:"key"`
	Label            string   `json:"label"`
	Connected        *bool    `json:"connected,omitempty"`
	Vendor           int32    `json:"vendor,omitempty"`
	Product          int32    `json:"product,omitempty"`
	Family           []string `json:"family,omitempty"`
	Speed            []string `json:"speed,omitempty"`
	BackingType      string   `json:"backing_type,omitempty"`
	BackingPath      string   `json:"backing_path,omitempty"`
	BackingDevice    string   `json:"backing_device,omitempty"`
	BackingHost      string   `json:"backing_host,omitempty"`
	BackingDatastore string   `json:"backing_datastore,omitempty"`
	BackingObjectID  string   `json:"backing_object_id,omitempty"`
	UseAutoDetect    *bool    `json:"use_auto_detect,omitempty"`
	Controller       string   `json:"controller,omitempty"`
	ControllerLabel  string   `json:"controller_label,omitempty"`
	UnitNumber       *int32   `json:"unit_number,omitempty"`
}

// VMSnapshot is one read-only entry in a VM snapshot tree.
type VMSnapshot struct {
	ID          string    `json:"id"`
	NumericID   int32     `json:"numeric_id"`
	ParentID    string    `json:"parent_id,omitempty"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreateTime  time.Time `json:"create_time"`
	PowerState  string    `json:"power_state"`
	Quiesced    bool      `json:"quiesced"`
	Current     bool      `json:"current"`
}

// Host is an ESXi host.
type Host struct {
	Location
	Metadata        Metadata        `json:"metadata"`
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Cluster         string          `json:"cluster"`
	PowerState      string          `json:"power_state"`
	ConnectionState string          `json:"connection_state"`
	InMaintenance   bool            `json:"in_maintenance"`
	Vendor          string          `json:"vendor"`
	Model           string          `json:"model"`
	Version         string          `json:"version"`
	Build           string          `json:"build"`
	CPUCores        int32           `json:"cpu_cores"`
	CPUThreads      int32           `json:"cpu_threads"`
	CPUMHz          int32           `json:"cpu_mhz"`
	TotalCPUMHz     int64           `json:"total_cpu_mhz"`
	MemoryMB        int64           `json:"memory_mb"`
	CPUUsageMHz     int64           `json:"cpu_usage_mhz"`
	MemoryUsageMB   int64           `json:"memory_usage_mb"`
	VMCount         int             `json:"vm_count"`
	HBAs            []HostHBA       `json:"hbas,omitempty"`
	NICs            []HostNIC       `json:"nics,omitempty"`
	VSwitches       []HostVSwitch   `json:"vswitches,omitempty"`
	PortGroups      []HostPortGroup `json:"port_groups,omitempty"`
	VMKs            []HostVMKernel  `json:"vmks,omitempty"`
	// ProxySwitches are the host's memberships in distributed switches: which
	// physical NIC backs each of a switch's uplinks on this host.
	ProxySwitches []HostProxySwitch `json:"proxy_switches,omitempty"`
	Multipaths    []HostMultipath   `json:"multipaths,omitempty"`
}

// TotalCPU returns the host's total CPU capacity in MHz.
func (h Host) TotalCPU() int64 {
	if h.TotalCPUMHz > 0 {
		return h.TotalCPUMHz
	}
	if h.CPUCores > 0 && h.CPUMHz > 0 {
		return int64(h.CPUCores) * int64(h.CPUMHz)
	}
	return int64(h.CPUMHz)
}

// HostHBA is a normalized host bus adapter. WWNs are pointers because a
// transport can report a real zero value differently from an omitted one.
type HostHBA struct {
	Key             string `json:"key,omitempty"`
	Device          string `json:"device"`
	Bus             int32  `json:"bus"`
	Status          string `json:"status"`
	Model           string `json:"model"`
	Driver          string `json:"driver,omitempty"`
	PCI             string `json:"pci,omitempty"`
	StorageProtocol string `json:"storage_protocol,omitempty"`
	Type            string `json:"type"`
	WWNN            *int64 `json:"wwnn,omitempty"`
	WWPN            *int64 `json:"wwpn,omitempty"`
	IScsiName       string `json:"iscsi_name,omitempty"`
	IScsiAlias      string `json:"iscsi_alias,omitempty"`
}

// HostNIC is one physical NIC from a host's network configuration.
type HostNIC struct {
	Key         string `json:"key,omitempty"`
	Device      string `json:"device"`
	PCI         string `json:"pci,omitempty"`
	Driver      string `json:"driver,omitempty"`
	MAC         string `json:"mac,omitempty"`
	LinkSpeedMB *int32 `json:"link_speed_mb,omitempty"`
	Duplex      *bool  `json:"duplex,omitempty"`
	WakeOnLAN   bool   `json:"wake_on_lan"`
	Switch      string `json:"switch,omitempty"`
}

// HostVSwitch is one standard virtual switch and its effective security
// policy. Distributed switches are represented by the sibling DVSwitch type.
type HostVSwitch struct {
	Key             string   `json:"key,omitempty"`
	Name            string   `json:"name"`
	NumPorts        int32    `json:"num_ports"`
	FreePorts       int32    `json:"free_ports"`
	MTU             int32    `json:"mtu"`
	Uplinks         []string `json:"uplinks,omitempty"`
	Promiscuous     *bool    `json:"promiscuous,omitempty"`
	MACChanges      *bool    `json:"mac_changes,omitempty"`
	ForgedTransmits *bool    `json:"forged_transmits,omitempty"`
	TrafficShaping  *bool    `json:"traffic_shaping,omitempty"`
}

// HostPortGroup is one standard-switch port group and its effective policy.
type HostPortGroup struct {
	Key             string `json:"key,omitempty"`
	Name            string `json:"name"`
	Switch          string `json:"switch"`
	VLAN            int32  `json:"vlan"`
	Promiscuous     *bool  `json:"promiscuous,omitempty"`
	MACChanges      *bool  `json:"mac_changes,omitempty"`
	ForgedTransmits *bool  `json:"forged_transmits,omitempty"`
}

// DVSwitch is a distributed virtual switch and its configuration-derived
// port-group inventory. Runtime per-port state is intentionally not included:
// collecting it requires a mutating-capable govmomi package and is outside
// this read-only profile.
type DVSwitch struct {
	Location
	Metadata               Metadata      `json:"metadata"`
	ID                     string        `json:"id"`
	Name                   string        `json:"name"`
	UUID                   string        `json:"uuid,omitempty"`
	Vendor                 string        `json:"vendor,omitempty"`
	Version                string        `json:"version,omitempty"`
	Description            string        `json:"description,omitempty"`
	Contact                string        `json:"contact,omitempty"`
	ContactDetail          string        `json:"contact_detail,omitempty"`
	NumPorts               int32         `json:"num_ports"`
	MaxPorts               int32         `json:"max_ports"`
	MaxMTU                 int32         `json:"max_mtu"`
	Hosts                  []string      `json:"hosts,omitempty"`
	UplinkPorts            []string      `json:"uplink_ports,omitempty"`
	LinkDiscoveryProtocol  string        `json:"link_discovery_protocol,omitempty"`
	LinkDiscoveryOperation string        `json:"link_discovery_operation,omitempty"`
	LACPVersion            string        `json:"lacp_version,omitempty"`
	PortGroups             []DVPortGroup `json:"port_groups,omitempty"`
}

// DVPortGroup is one distributed port group and its effective default port
// policy. It is not one runtime distributed port.
type DVPortGroup struct {
	ID                string   `json:"id"`
	Key               string   `json:"key"`
	Name              string   `json:"name"`
	Switch            string   `json:"switch"`
	Type              string   `json:"type,omitempty"`
	BackingType       string   `json:"backing_type,omitempty"`
	NumPorts          int32    `json:"num_ports"`
	VLAN              string   `json:"vlan,omitempty"`
	Uplink            bool     `json:"uplink"`
	Promiscuous       *bool    `json:"promiscuous,omitempty"`
	MACChanges        *bool    `json:"mac_changes,omitempty"`
	ForgedTransmits   *bool    `json:"forged_transmits,omitempty"`
	TeamingPolicy     string   `json:"teaming_policy,omitempty"`
	NotifySwitches    *bool    `json:"notify_switches,omitempty"`
	Failback          *bool    `json:"failback,omitempty"`
	IngressShaping    *bool    `json:"ingress_shaping,omitempty"`
	EgressShaping     *bool    `json:"egress_shaping,omitempty"`
	Blocked           *bool    `json:"blocked,omitempty"`
	AutoExpand        *bool    `json:"auto_expand,omitempty"`
	ActiveUplinks     []string `json:"active_uplinks,omitempty"`
	StandbyUplinks    []string `json:"standby_uplinks,omitempty"`
	LogicalSwitchUUID string   `json:"logical_switch_uuid,omitempty"`
	SegmentID         string   `json:"segment_id,omitempty"`
}

// HostProxySwitch is one host's membership in a distributed switch. Uplinks
// lists every uplink the switch defines on the host, in the switch's order,
// whether or not a physical NIC is assigned to it.
type HostProxySwitch struct {
	Key        string            `json:"key,omitempty"`
	Switch     string            `json:"switch"`
	SwitchUUID string            `json:"switch_uuid,omitempty"`
	MTU        int32             `json:"mtu,omitempty"`
	Uplinks    []HostProxyUplink `json:"uplinks,omitempty"`
}

// HostProxyUplink is one distributed-switch uplink on one host. NIC is the
// physical NIC device behind it, empty when none is assigned.
type HostProxyUplink struct {
	Name string `json:"name"`
	NIC  string `json:"nic,omitempty"`
}

// HostVMKernel is one host VMkernel adapter, including whether it came from
// the legacy service-console collection.
type HostVMKernel struct {
	Key       string `json:"key,omitempty"`
	Device    string `json:"device"`
	PortGroup string `json:"port_group,omitempty"`
	// DVSwitchUUID and DVPortGroupKey identify the distributed port group an
	// adapter is connected to. PortGroup is empty for those adapters: it only
	// names standard-switch port groups.
	DVSwitchUUID   string `json:"dv_switch_uuid,omitempty"`
	DVPortGroupKey string `json:"dv_portgroup_key,omitempty"`
	MAC            string `json:"mac,omitempty"`
	MTU            int32  `json:"mtu"`
	TSO            *bool  `json:"tso,omitempty"`
	Netstack       string `json:"netstack,omitempty"`
	DHCP           *bool  `json:"dhcp,omitempty"`
	IP             string `json:"ip,omitempty"`
	SubnetMask     string `json:"subnet_mask,omitempty"`
	ServiceConsole bool   `json:"service_console"`
}

// HostMultipath is one host/LUN aggregate. Individual paths are counted but
// deliberately not expanded into rows, keeping exports bounded on large SANs.
type HostMultipath struct {
	Key          string `json:"key,omitempty"`
	LUN          string `json:"lun"`
	DevicePath   string `json:"device_path,omitempty"`
	Policy       string `json:"policy,omitempty"`
	LocalDisk    *bool  `json:"local_disk,omitempty"`
	PathCount    int    `json:"path_count"`
	Active       int    `json:"active"`
	Standby      int    `json:"standby"`
	Dead         int    `json:"dead"`
	Disabled     int    `json:"disabled"`
	WorkingPaths int    `json:"working_paths"`
}

// Cluster is a compute cluster. Standalone hosts appear as a ComputeResource
// in vSphere and are reported here with Standalone set.
type Cluster struct {
	Location
	Metadata      Metadata `json:"metadata"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Standalone    bool     `json:"standalone"`
	Hosts         int      `json:"hosts"`
	EffectiveHost int      `json:"effective_hosts"`
	CPUCores      int32    `json:"cpu_cores"`
	TotalCPUMHz   int64    `json:"total_cpu_mhz"`
	TotalMemoryMB int64    `json:"total_memory_mb"`
	DRSEnabled    bool     `json:"drs_enabled"`
	HAEnabled     bool     `json:"ha_enabled"`
}

// ResourcePool is a vSphere resource-pool configuration record. It is
// persisted for assessment exports, but intentionally is not part of the
// browsable inventory vocabulary in AllKinds.
type ResourcePool struct {
	Location
	Metadata     Metadata `json:"metadata"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Root         bool     `json:"root"`
	Parent       string   `json:"parent"`
	Owner        string   `json:"owner"`
	Status       string   `json:"status"`
	ConfigStatus string   `json:"config_status"`
	VMRefs       []string `json:"vm_refs"`
	ResourceAllocation
}

// ResourceAllocation is the CPU and memory allocation a resource pool or a
// vApp (a resource pool subtype) hands out to everything inside it. Its JSON
// fields are flattened into the owner's payload. A nil reservation or limit
// means vSphere did not expose one; a limit of -1 is unlimited.
type ResourceAllocation struct {
	CPUReservationMHz   *int64 `json:"cpu_reservation_mhz,omitempty"`
	CPULimitMHz         *int64 `json:"cpu_limit_mhz,omitempty"`
	CPUOverheadLimitMHz *int64 `json:"cpu_overhead_limit_mhz,omitempty"`
	CPUExpandable       bool   `json:"cpu_expandable"`
	CPUShares           int32  `json:"cpu_shares"`
	CPULevel            string `json:"cpu_level"`
	MemConfiguredMB     int64  `json:"mem_configured_mb"`
	MemReservationMB    *int64 `json:"mem_reservation_mb,omitempty"`
	MemLimitMB          *int64 `json:"mem_limit_mb,omitempty"`
	MemOverheadLimitMB  *int64 `json:"mem_overhead_limit_mb,omitempty"`
	MemExpandable       bool   `json:"mem_expandable"`
	MemShares           int32  `json:"mem_shares"`
	MemLevel            string `json:"mem_level"`
}

// VApp is a logical vSphere application container. Membership fields contain
// only direct children; nested vApps and resource pools are represented
// separately so a detail view never mistakes descendants for direct members.
//
// Status is the vApp's run state (started, stopped, ...); OverallStatus and
// ConfigStatus are the colours vSphere reports for it as a resource pool.
// Allocation and StartOrder are absent from captures before inventory
// schema 20, so a nil Allocation means "not recorded", never "no limits".
type VApp struct {
	Location
	Metadata               Metadata            `json:"metadata"`
	ID                     string              `json:"id"`
	Name                   string              `json:"name"`
	Status                 string              `json:"status"`
	OverallStatus          string              `json:"overall_status,omitempty"`
	ConfigStatus           string              `json:"config_status,omitempty"`
	ParentContainer        string              `json:"parent_container"`
	ParentVApp             string              `json:"parent_vapp,omitempty"`
	DirectVMCount          int                 `json:"direct_vm_count"`
	DirectVMs              []string            `json:"direct_vms"`
	DirectVMRefs           []string            `json:"direct_vm_refs,omitempty"`
	ChildVAppCount         int                 `json:"child_vapp_count"`
	ChildVApps             []string            `json:"child_vapps"`
	ChildVAppRefs          []string            `json:"child_vapp_refs,omitempty"`
	ChildResourcePoolCount int                 `json:"child_resource_pool_count"`
	ChildResourcePools     []string            `json:"child_resource_pools"`
	ChildResourcePoolRefs  []string            `json:"child_resource_pool_refs,omitempty"`
	Cluster                string              `json:"cluster"`
	ComputeResource        string              `json:"compute_resource"`
	Allocation             *ResourceAllocation `json:"allocation,omitempty"`
	StartOrder             []VAppStartEntry    `json:"start_order,omitempty"`
}

// VAppStartEntry is one member's place in a vApp's startup and shutdown
// sequence. Members with the same Order start together; a lower Order starts
// first, and DelaySeconds is the wait before the next group starts.
type VAppStartEntry struct {
	Ref              string `json:"ref"`
	Name             string `json:"name"`
	Order            int32  `json:"order"`
	DelaySeconds     int32  `json:"delay_seconds"`
	StartAction      string `json:"start_action,omitempty"`
	StopAction       string `json:"stop_action,omitempty"`
	StopDelaySeconds int32  `json:"stop_delay_seconds"`
	WaitForGuest     bool   `json:"wait_for_guest"`
}

// Datastore is a backing store for VM files.
type Datastore struct {
	Location
	Metadata        Metadata         `json:"metadata"`
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Type            string           `json:"type"`
	Accessible      bool             `json:"accessible"`
	CapacityBytes   int64            `json:"capacity_bytes"`
	FreeBytes       int64            `json:"free_bytes"`
	Maintenance     string           `json:"maintenance"`
	Files           []DatastoreFile  `json:"files,omitempty"`
	BrowseStatus    string           `json:"browse_status,omitempty"`
	BrowseError     string           `json:"browse_error,omitempty"`
	Backing         DatastoreBacking `json:"backing,omitempty"`
	BrowseTruncated bool             `json:"browse_truncated,omitempty"`
	// FileInventory is the opt-in all-file inventory behind the vFileInfo
	// export. nil means the capture never requested it, which must never be
	// read as "no files". Orphan analysis does not consume it.
	FileInventory *DatastoreFileInventory `json:"file_inventory,omitempty"`
}

// DatastoreBacking identifies the storage a datastore is presented from, so
// the same LUN or NFS export reached through two vCenters under two display
// names can be recognized as one thing. Every field is optional: a vCenter
// that will not answer leaves it empty, which is evidence of blindness, not
// of difference. Local is deliberate — a local datastore is never shared, so
// it must never join two contexts together.
type DatastoreBacking struct {
	URL       string   `json:"url,omitempty"`
	VMFSUUID  string   `json:"vmfs_uuid,omitempty"`
	Extents   []string `json:"extents,omitempty"`
	NASRemote string   `json:"nas_remote,omitempty"`
	VVolID    string   `json:"vvol_id,omitempty"`
	Local     bool     `json:"local,omitempty"`
}

// DatastoreFile is read-only metadata returned by an opt-in datastore browser
// query. Path is the canonical [datastore] relative path used to join files to
// VM disk backing paths; content is never downloaded.
type DatastoreFile struct {
	Path      string    `json:"path"`
	SizeBytes int64     `json:"size_bytes"`
	Modified  time.Time `json:"modified,omitempty"`
}

// DatastoreEntryType distinguishes the two things a directory listing can
// contain. It is a string rather than a bool so a listing renders the same way
// it reads — "folder" and "file", never "isDir: true".
type DatastoreEntryType string

const (
	DatastoreEntryFile   DatastoreEntryType = "file"
	DatastoreEntryFolder DatastoreEntryType = "folder"
)

// DatastoreEntry is one row of an interactive directory listing.
//
// It is deliberately not DatastoreFile. That type is assessment evidence: it
// exists to be persisted and joined to VM disk backing paths, so it carries
// only what an orphan analysis needs and only ever describes a VMDK. This one
// is a transient view of one directory as an operator is looking at it, so it
// carries the leaf name and the file/folder distinction that a browser has to
// render, and is never written to a capture.
type DatastoreEntry struct {
	Name      string
	Path      string
	Type      DatastoreEntryType
	SizeBytes int64
	Modified  time.Time
}

// DatastoreVMReference identifies a VM or template disk attached to a
// datastore. It is returned by an operator-triggered relationship lookup and
// is never persisted as part of the normal datastore inventory.
type DatastoreVMReference struct {
	Context     string
	VMID        string
	VMName      string
	Template    bool
	DiskLabel   string
	BackingPath string
}

// DatastoreReferenceListing reports the relationship lookup together with
// coverage. An empty References slice is authoritative only when CheckedVMs
// equals TotalVMs and Problems is empty.
type DatastoreReferenceListing struct {
	References []DatastoreVMReference
	TotalVMs   int
	CheckedVMs int
	Problems   []string
}

func (r DatastoreReferenceListing) Complete() bool {
	return r.TotalVMs == r.CheckedVMs && len(r.Problems) == 0
}

// DatastoreListing is one answered browser query together with its own
// provenance. Truncated is a field rather than something folded into the
// entries because a partial result that reads as a complete one is precisely
// the failure this browser exists to avoid: "the file is not on this
// datastore" and "the file is not in the first thousand results" are different
// answers.
type DatastoreListing struct {
	Entries   []DatastoreEntry
	Truncated bool
}

// UsedBytes is capacity minus free space.
func (d Datastore) UsedBytes() int64 {
	if d.CapacityBytes <= 0 {
		return 0
	}
	return d.CapacityBytes - d.FreeBytes
}

// UsedPercent is the fraction of the datastore in use, 0 when unknown.
func (d Datastore) UsedPercent() float64 {
	if d.CapacityBytes <= 0 {
		return 0
	}
	return float64(d.UsedBytes()) / float64(d.CapacityBytes) * 100
}

// Network is a port group or network the VMs attach to.
type Network struct {
	Location
	Metadata   Metadata `json:"metadata"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Switch     string   `json:"switch,omitempty"`
	VLAN       string   `json:"vlan,omitempty"`
	Accessible bool     `json:"accessible"`
}

// Inventory is everything enumerated from one vCenter at one moment.
//
// A partial result is a valid result: ListInventory keeps going after one
// kind fails to list — a limited-permission account that can see VMs but not
// Datastores is a normal shape, not a reason to discard everything it could
// read. Errors records what did not come back; every kind missing from it
// enumerated cleanly, even if empty.
type Inventory struct {
	Context       string           `json:"context"`
	VMs           []VM             `json:"vms"`
	Templates     []VM             `json:"templates"`
	Hosts         []Host           `json:"hosts"`
	Clusters      []Cluster        `json:"clusters"`
	ResourcePools []ResourcePool   `json:"resource_pools"`
	DVSwitches    []DVSwitch       `json:"dv_switches"`
	VApps         []VApp           `json:"vapps"`
	Datastores    []Datastore      `json:"datastores"`
	Networks      []Network        `json:"networks"`
	Errors        []InventoryError `json:"errors,omitempty"`
}

// InventoryError is one resource kind ListInventory could not enumerate.
type InventoryError struct {
	Kind    Kind   `json:"kind"`
	Message string `json:"message"`
}

// ErrorFor returns the reason kind failed to list, or false if it enumerated
// cleanly (which includes kinds this Inventory never attempted).
func (i *Inventory) ErrorFor(kind Kind) (string, bool) {
	for _, e := range i.Errors {
		if e.Kind == kind {
			return e.Message, true
		}
	}
	return "", false
}

// Slice extracts one fetch group's share of i as its own Inventory — the
// inverse of ApplyGroup, and the counterpart a Backend whose data is already
// fully assembled (a fixed test fixture, a demo estate) uses to answer
// InventoryHandle.FetchGroup without a real per-group retrieval behind it.
func (i *Inventory) Slice(group FetchGroup) *Inventory {
	part := &Inventory{Context: i.Context}
	switch group {
	case GroupVMs:
		part.VMs, part.Templates = i.VMs, i.Templates
	case GroupHosts:
		part.Hosts = i.Hosts
	case GroupClusters:
		part.Clusters = i.Clusters
	case GroupResourcePools:
		part.ResourcePools = i.ResourcePools
	case GroupDVSwitches:
		part.DVSwitches = i.DVSwitches
	case GroupVApps:
		part.VApps = i.VApps
	case GroupDatastores:
		part.Datastores = i.Datastores
	case GroupNetworks:
		part.Networks = i.Networks
	}
	for _, e := range i.Errors {
		if GroupFor(e.Kind) == group {
			part.Errors = append(part.Errors, e)
		}
	}
	return part
}

// ApplyGroup folds one FetchGroup's result into i: on success it replaces
// i's fields for that group's kinds with part's; on failure it leaves them
// exactly as they were. i.Errors is updated for the group's kinds either
// way, replacing whatever i previously recorded for them.
//
// This is what lets a refresh that fails for one kind keep showing the last
// data that kind did have while every other kind still updates normally —
// stale-while-revalidate applied per kind, the same promise ListInventory
// already made per context. It is also what a first-ever load uses to
// assemble the whole Inventory: called once per group against a blank
// Inventory, "leave it as it was" and "replace it" agree, since a blank
// field either way is blank.
func (i *Inventory) ApplyGroup(group FetchGroup, part *Inventory) {
	failed := len(part.Errors) > 0
	kept := i.Errors[:0]
	for _, e := range i.Errors {
		if GroupFor(e.Kind) != group {
			kept = append(kept, e)
		}
	}
	i.Errors = append(kept, part.Errors...)
	if failed {
		return
	}
	switch group {
	case GroupVMs:
		i.VMs = part.VMs
		i.Templates = part.Templates
	case GroupHosts:
		i.Hosts = part.Hosts
	case GroupClusters:
		i.Clusters = part.Clusters
	case GroupResourcePools:
		i.ResourcePools = part.ResourcePools
	case GroupDVSwitches:
		i.DVSwitches = part.DVSwitches
	case GroupVApps:
		i.VApps = part.VApps
	case GroupDatastores:
		i.Datastores = part.Datastores
	case GroupNetworks:
		i.Networks = part.Networks
	}
}

// MergeGroup folds one page of a fetch group into i, appending to what that
// group already holds rather than replacing it — which is what ApplyGroup
// does, and what a group's completed result should still do.
//
// Pages arrive in the server's traversal order, so each merge re-sorts the
// slices it touched. A list that grows while somebody is reading it has to
// stay in order, or the row under the cursor moves for no reason visible on
// screen.
func (i *Inventory) MergeGroup(group FetchGroup, part *Inventory) {
	if part == nil {
		return
	}
	switch group {
	case GroupVMs:
		i.VMs = sortByName(append(i.VMs, part.VMs...), func(v VM) string { return v.Name })
		i.Templates = sortByName(append(i.Templates, part.Templates...), func(v VM) string { return v.Name })
	case GroupHosts:
		i.Hosts = sortByName(append(i.Hosts, part.Hosts...), func(h Host) string { return h.Name })
	case GroupClusters:
		i.Clusters = sortByName(append(i.Clusters, part.Clusters...), func(c Cluster) string { return c.Name })
	case GroupResourcePools:
		i.ResourcePools = sortByName(append(i.ResourcePools, part.ResourcePools...), func(r ResourcePool) string { return r.Name })
	case GroupDVSwitches:
		i.DVSwitches = sortByName(append(i.DVSwitches, part.DVSwitches...), func(d DVSwitch) string { return d.Name })
	case GroupVApps:
		i.VApps = sortByName(append(i.VApps, part.VApps...), func(v VApp) string { return v.Name })
	case GroupDatastores:
		i.Datastores = sortByName(append(i.Datastores, part.Datastores...), func(d Datastore) string { return d.Name })
	case GroupNetworks:
		i.Networks = sortByName(append(i.Networks, part.Networks...), func(n Network) string { return n.Name })
	}
}

func sortByName[T any](items []T, name func(T) string) []T {
	sort.SliceStable(items, func(a, b int) bool { return name(items[a]) < name(items[b]) })
	return items
}

// Counts renders a one-line summary, used by status output and by the
// interface's message line.
func (i *Inventory) Counts() string {
	return strings.Join([]string{
		plural(len(i.VMs), "VM"),
		plural(len(i.Templates), "template"),
		plural(len(i.Hosts), "host"),
		plural(len(i.Clusters), "cluster"),
		plural(len(i.VApps), "vApp"),
		plural(len(i.Datastores), "datastore"),
		plural(len(i.Networks), "network"),
	}, ", ")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// MetadataTally accumulates one metadata source's per-object states into a
// collection-level status: available only when every object is, partial when
// some are, and otherwise the most actionable failure -- a privilege grant,
// then a missing service, then a retry.
type MetadataTally struct {
	Total, Available int
	counts           map[string]int
	firstError       string
}

var metadataFailurePriority = []string{MetadataDenied, MetadataUnsupported, MetadataUnavailable, MetadataNotRecorded}

// Add records one object's status for the source.
func (t *MetadataTally) Add(status, message string) {
	status = MetadataStatus(status)
	t.Total++
	if status == MetadataAvailable {
		t.Available++
		return
	}
	if t.counts == nil {
		t.counts = map[string]int{}
	}
	t.counts[status]++
	if t.firstError == "" {
		t.firstError = message
	}
}

// FirstError is the first failure message recorded.
func (t MetadataTally) FirstError() string { return t.firstError }

// Breakdown lists the failure states with their object counts, most
// actionable first, e.g. "1 denied, 2 unavailable".
func (t MetadataTally) Breakdown() string {
	var parts []string
	seen := map[string]bool{}
	for _, status := range metadataFailurePriority {
		if n := t.counts[status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, strings.ReplaceAll(status, "_", " ")))
			seen[status] = true
		}
	}
	var other []string
	for status := range t.counts {
		if !seen[status] {
			other = append(other, status)
		}
	}
	sort.Strings(other)
	for _, status := range other {
		parts = append(parts, fmt.Sprintf("%d %s", t.counts[status], strings.ReplaceAll(status, "_", " ")))
	}
	return strings.Join(parts, ", ")
}

// Result returns the collection-level status and a description of the gap.
func (t MetadataTally) Result() (string, string) {
	if t.Total == 0 {
		return MetadataNotApplicable, ""
	}
	if t.Available == t.Total {
		return MetadataAvailable, ""
	}
	message := t.firstError
	if message == "" {
		message = "source not readable"
	}
	if t.Available > 0 {
		return MetadataPartial, fmt.Sprintf("%d of %d objects: %s", t.Total-t.Available, t.Total, message)
	}
	for _, status := range metadataFailurePriority {
		if t.counts[status] > 0 {
			return status, message
		}
	}
	return MetadataUnavailable, message
}
