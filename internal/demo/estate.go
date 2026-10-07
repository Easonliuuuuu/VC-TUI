package demo

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// The estate is generated, not typed. Every per-entity attribute is derived
// from an FNV hash of the site and the entity's own name, so the result is
// identical on every run and on every Go version, and adding one VM cannot
// shift the attributes of any other. Nothing here reads the clock, the
// environment, or the disk.

const (
	gib = int64(1) << 30
	tib = int64(1) << 40
)

// demoNow is the moment the newest seeded assessment ran. Snapshot ages are
// measured back from it.
var demoNow = time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)

func hv(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

func pickN(n int, parts ...string) int { return int(hv(parts...) % uint64(n)) }

// frac returns a stable value in [0,1).
func frac(parts ...string) float64 { return float64(hv(parts...)%10000) / 10000 }

type clusterSpec struct {
	name       string
	letter     string
	hostPrefix string
	hosts      int
	vms        int
	role       string
	services   []string
	fixed      []string
	subnet     int
	datastores []string
	pools      []poolSpec
	folder     map[string]string
}

type poolSpec struct {
	name string
	svcs []string
}

type vappSpec struct {
	name, cluster, parent string
	svcs                  []string
	max                   int
	childPool             string
}

type dsSpec struct {
	name    string
	typ     string
	capTiB  int64 // 0 derives the capacity from what is stored there
	fill    float64
	browse  bool
	offline bool
	local   bool
	// sharedUUID presents the same LUN under two contexts.
	sharedUUID string
}

type siteSpec struct {
	ctx, dc, prefix string
	clusters        []clusterSpec
	datastores      []dsSpec
	templates       []string
	vapps           []vappSpec
	portgroups      []pgSpec
	storagePGs      []pgSpec
	// anchor names the hand-authored evidence layered onto the site.
	anchor string
	fastDS string
}

type pgSpec struct {
	name string
	vlan int
}

// estate is the finished per-site inventory plus the facts the history seeder
// needs to age it backwards.
type estate struct {
	inv *vsphere.Inventory
	// lateHost is a host first seen in the newest run.
	lateHost string
	// fastDS is a datastore that fills noticeably faster than the rest.
	fastDS string
	// vappOf maps a VM name to the vApp that owns it.
	vappOf map[string]string
	// extraFiles are browsable files no VM references (orphan evidence).
	extraFiles map[string][]vsphere.DatastoreFile
}

type placedVM struct {
	vm      vsphere.VM
	cluster *clusterSpec
	svc     string
}

func buildEstate(s siteSpec) *estate {
	inv := &vsphere.Inventory{Context: s.ctx}
	e := &estate{inv: inv, vappOf: map[string]string{}, extraFiles: map[string][]vsphere.DatastoreFile{}, fastDS: s.fastDS}
	loc := func(kind, path string) vsphere.Location {
		return vsphere.Location{Context: s.ctx, Datacenter: s.dc, Path: "/" + s.dc + "/" + kind + "/" + path}
	}

	// Networks first: NICs reference them.
	dvsUUID := s.ctx + "-dvs-uuid"
	dvs := vsphere.DVSwitch{
		Location: loc("network", "DVS-Production"), ID: s.ctx + "-dvs-1", Name: "DVS-Production", UUID: dvsUUID,
		Vendor: "VMware", Version: "8.0.3", NumPorts: 2048, MaxPorts: 4096, MaxMTU: 9000,
		UplinkPorts: []string{"DVS-Production-DVUplinks"}, LinkDiscoveryProtocol: "lldp", LinkDiscoveryOperation: "both", LACPVersion: "multipleLag",
	}
	storDVS := vsphere.DVSwitch{
		Location: loc("network", "DVS-Storage"), ID: s.ctx + "-dvs-2", Name: "DVS-Storage", UUID: s.ctx + "-dvs-storage-uuid",
		Vendor: "VMware", Version: "8.0.3", NumPorts: 256, MaxPorts: 4096, MaxMTU: 9000,
		UplinkPorts: []string{"DVS-Storage-DVUplinks"}, LinkDiscoveryProtocol: "lldp", LinkDiscoveryOperation: "listen",
	}
	pgNet := map[string]vsphere.Network{}
	addPG := func(sw *vsphere.DVSwitch, p pgSpec, n int) {
		id := fmt.Sprintf("%s-net-%d", s.ctx, len(inv.Networks)+1)
		vlan := fmt.Sprint(p.vlan)
		net := vsphere.Network{Location: loc("network", p.name), ID: id, Name: p.name, Type: "portgroup", Switch: sw.Name, VLAN: vlan, Accessible: true}
		inv.Networks = append(inv.Networks, net)
		pgNet[p.name] = net
		pg := vsphere.DVPortGroup{
			ID: fmt.Sprintf("%s-dvpg-%d", s.ctx, n), Key: fmt.Sprintf("dvportgroup-%d", n), Name: p.name, Switch: sw.Name, Type: "earlyBinding", BackingType: "standard",
			// The nested-ESXi lab network is the one that needs promiscuous
			// mode, and the switch views flag it.
			NumPorts: 128, VLAN: vlan, Promiscuous: boolValue(p.name == "lab-vlan-900"), MACChanges: boolValue(true), ForgedTransmits: boolValue(p.name != "dmz-vlan-400" && p.name != "dmz-web-vlan-410"),
			TeamingPolicy: "loadbalance_loadbased", NotifySwitches: boolValue(true), Failback: boolValue(true), IngressShaping: boolValue(false), EgressShaping: boolValue(false),
			Blocked: boolValue(false), AutoExpand: boolValue(true), ActiveUplinks: []string{"dvUplink1", "dvUplink2"},
		}
		if sw == &storDVS {
			// Storage and vMotion traffic fail over rather than balance.
			pg.TeamingPolicy, pg.ActiveUplinks, pg.StandbyUplinks = "failover_explicit", []string{"dvUplink1"}, []string{"dvUplink2"}
		}
		sw.PortGroups = append(sw.PortGroups, pg)
	}
	for i, p := range s.portgroups {
		addPG(&dvs, p, i+1)
	}
	for i, p := range s.storagePGs {
		addPG(&storDVS, p, len(s.portgroups)+i+1)
	}
	for i, name := range []string{"VM Network", "Management Network", "vMotion Network", "iSCSI-Legacy"} {
		inv.Networks = append(inv.Networks, vsphere.Network{
			Location: loc("network", name), ID: fmt.Sprintf("%s-net-%d", s.ctx, len(inv.Networks)+1), Name: name, Type: "standard", Switch: "vSwitch0", VLAN: fmt.Sprint(20 + i*10), Accessible: true,
		})
	}
	inv.Networks = append(inv.Networks, vsphere.Network{
		Location: loc("network", "nsx-seg-web"), ID: fmt.Sprintf("%s-net-%d", s.ctx, len(inv.Networks)+1), Name: "nsx-seg-web", Type: "opaque", Accessible: true,
	})

	// Hosts and clusters are placed empty; sizing follows the VMs placed on them.
	hostIdx := map[string][]int{}
	for ci := range s.clusters {
		c := &s.clusters[ci]
		for i := 0; i < c.hosts; i++ {
			name := fmt.Sprintf("%s-%02d", c.hostPrefix, i+1)
			inv.Hosts = append(inv.Hosts, vsphere.Host{
				Location: loc("host", c.name+"/"+name), ID: fmt.Sprintf("%s-host-%d", s.ctx, len(inv.Hosts)+1), Name: name, Cluster: c.name,
				PowerState: "poweredOn", ConnectionState: "connected",
			})
			hostIdx[c.name] = append(hostIdx[c.name], len(inv.Hosts)-1)
		}
		// One host per cluster of size >= 6 is in maintenance, and the last
		// host of one cluster is disconnected.
		if c.hosts >= 6 {
			h := &inv.Hosts[hostIdx[c.name][c.hosts-2]]
			h.InMaintenance, h.ConnectionState = true, "connected"
		}
	}
	if len(s.clusters) > 1 {
		c := s.clusters[1]
		inv.Hosts[hostIdx[c.name][c.hosts-1]].ConnectionState = "notResponding"
		// The final host of the second cluster joined the cluster after the
		// previous assessment.
		e.lateHost = inv.Hosts[hostIdx[c.name][c.hosts-3]].Name
	}
	usable := func(c *clusterSpec) []int {
		var out []int
		for _, i := range hostIdx[c.name] {
			if h := inv.Hosts[i]; !h.InMaintenance && h.ConnectionState == "connected" {
				out = append(out, i)
			}
		}
		return out
	}

	// Datastores.
	dsIdx := map[string]int{}
	for i, d := range s.datastores {
		dsIdx[d.name] = i
		inv.Datastores = append(inv.Datastores, vsphere.Datastore{
			Location: loc("datastore", d.name), ID: fmt.Sprintf("%s-ds-%d", s.ctx, i+1), Name: d.name, Type: d.typ, Accessible: !d.offline,
			Maintenance: "normal",
		})
	}

	// VMs.
	counter := map[string]int{}
	var placed []placedVM
	seq := 0
	for ci := range s.clusters {
		c := &s.clusters[ci]
		hosts := usable(c)
		fixed := 0
		for k := 0; k < c.vms; k++ {
			seq++
			var name, svc string
			switch {
			case k < 3:
				svc = "vcls"
				name = fmt.Sprintf("vCLS-%s-%d", strings.ToUpper(c.letter), k+1)
			case fixed < len(c.fixed):
				svc = "fixed"
				name = c.fixed[fixed]
				fixed++
			default:
				svc = c.services[(k-3-fixed)%len(c.services)]
				counter[svc]++
				name = fmt.Sprintf("%s-%02d", svc, counter[svc])
				if c.role == "vdi" {
					name = fmt.Sprintf("%s-%03d", svc, counter[svc])
				}
			}
			host := inv.Hosts[hosts[pickN(len(hosts), s.ctx, name, "host")]]
			vm := vsphere.VM{
				Location: loc("vm", name), ID: fmt.Sprintf("%s-vm-%d", s.ctx, seq), Name: name,
				InstanceUUID: guid(s.ctx, "inst", name), BIOSUUID: guid(s.ctx, "bios", name),
				Host: host.Name, Cluster: c.name,
			}
			fillVM(&vm, s, c, svc, k, dsIdx, pgNet, dvsUUID)
			seedMigrationVM(&vm)
			tweakVM(&vm, c, svc)
			placed = append(placed, placedVM{vm: vm, cluster: c, svc: svc})
		}
	}
	for i := range placed {
		vm := &placed[i].vm
		vm.Path = "/" + s.dc + "/vm" + vm.Folder + "/" + vm.Name
		inv.VMs = append(inv.VMs, *vm)
	}
	sort.SliceStable(inv.VMs, func(a, b int) bool { return inv.VMs[a].Name < inv.VMs[b].Name })

	// Templates.
	for i, name := range s.templates {
		ds := s.datastores[pickN(2, s.ctx, name)].name
		if i == 0 {
			ds = s.datastores[0].name
		}
		size := int64(16+pickN(4, name)*16) * gib
		tpl := vsphere.VM{
			Location: vsphere.Location{Context: s.ctx, Datacenter: s.dc, Path: "/" + s.dc + "/vm/Templates/" + name}, ID: fmt.Sprintf("%s-tpl-%d", s.ctx, i+1), Name: name,
			IsTemplate: true, CPU: int32(2 + 2*pickN(3, name)), MemoryMB: int64(4096 << pickN(3, name)), GuestOS: guestFor(name).os, GuestID: guestFor(name).id,
			Folder: "/Templates", Datastores: []string{ds}, StorageGB: float64(size / gib),
			Disks: []vsphere.VMDisk{disk(1, size, "["+ds+"] templates/"+name+".vmdk", 0)},
		}
		seedMigrationVM(&tpl)
		inv.Templates = append(inv.Templates, tpl)
	}

	e.finishVApps(s, placed, loc)
	e.applyAnchors(s)
	e.finishClusters(s, hostIdx)
	e.mountDatastores(s, hostIdx)
	e.finishDatastores(s)
	e.finishPools(s, placed, loc)
	e.finishHosts(s)
	dvs.Hosts = hostNames(inv.Hosts)
	storDVS.Hosts = dvs.Hosts
	e.wireSwitches(&dvs, &storDVS)
	inv.DVSwitches = []vsphere.DVSwitch{dvs, storDVS}
	return e
}

func hostNames(hosts []vsphere.Host) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = h.Name
	}
	return out
}

func guid(parts ...string) string {
	a, b := hv(parts...), hv(append(parts, "b")...)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", uint32(a>>32), uint16(a>>16), uint16(a), uint16(b>>48), b&0xffffffffffff)
}

type guestInfo struct{ os, id string }

func guestFor(hint string) guestInfo {
	h := strings.ToLower(hint)
	switch {
	case strings.Contains(h, "windows-11"), strings.HasPrefix(h, "vdi"):
		return guestInfo{"Microsoft Windows 11 (64-bit)", "windows11_64Guest"}
	case strings.Contains(h, "windows-2025"):
		return guestInfo{"Microsoft Windows Server 2025", "windows2022srvNext_64Guest"}
	case strings.Contains(h, "windows-2022"), strings.Contains(h, "sql"), strings.Contains(h, "mssql"), strings.Contains(h, "ad-"), strings.Contains(h, "print"):
		return guestInfo{"Microsoft Windows Server 2022 (64-bit)", "windows2019srvNext_64Guest"}
	case strings.Contains(h, "windows"):
		return guestInfo{"Microsoft Windows Server 2019 (64-bit)", "windows2019srv_64Guest"}
	case strings.Contains(h, "rhel-8"):
		return guestInfo{"Red Hat Enterprise Linux 8 (64-bit)", "rhel8_64Guest"}
	case strings.Contains(h, "rhel"), strings.Contains(h, "oracle"), strings.Contains(h, "erp"), strings.Contains(h, "sap"):
		return guestInfo{"Red Hat Enterprise Linux 9 (64-bit)", "rhel9_64Guest"}
	case strings.Contains(h, "photon"), strings.Contains(h, "build-runner"), strings.HasPrefix(h, "vcls"):
		return guestInfo{"VMware Photon OS (64-bit)", "vmwarePhoton64Guest"}
	case strings.Contains(h, "debian"):
		return guestInfo{"Debian GNU/Linux 12 (64-bit)", "debian12_64Guest"}
	}
	return guestInfo{"Ubuntu Linux (64-bit)", "ubuntu64Guest"}
}

func disk(n int, size int64, path string, unit int32) vsphere.VMDisk {
	return vsphere.VMDisk{
		Key: int32(2000 + n - 1), Label: fmt.Sprintf("Hard disk %d", n), CapacityBytes: size, BackingType: "FlatVer2", BackingPath: path,
		DiskMode: "persistent", ThinProvisioned: boolValue(true), Controller: "scsi0", ControllerLabel: "SCSI controller 0", UnitNumber: &unit,
	}
}

func fillVM(vm *vsphere.VM, s siteSpec, c *clusterSpec, svc string, k int, dsIdx map[string]int, pgNet map[string]vsphere.Network, dvsUUID string) {
	name := vm.Name
	r := func(part string) float64 { return frac(s.ctx, name, part) }

	// Guest.
	g := guestFor(svc)
	if svc == "fixed" {
		g = guestFor(name)
	}
	vm.GuestOS, vm.GuestID = g.os, g.id
	if svc == "vcls" {
		vm.CPU, vm.MemoryMB = 1, 128
	} else {
		vm.CPU, vm.MemoryMB = sizeFor(c.role, name, s.ctx)
	}

	// Power, connection, tools.
	switch p := r("power"); {
	case svc == "vcls" || p < 0.86:
		vm.PowerState = "poweredOn"
	case p < 0.98:
		vm.PowerState = "poweredOff"
	default:
		vm.PowerState = "suspended"
	}
	vm.ConnectionState = "connected"
	if c := r("conn"); c < 0.004 {
		vm.ConnectionState = "inaccessible"
	} else if c < 0.006 {
		vm.ConnectionState = "invalid"
	}
	vm.GuestState, vm.ToolsState, vm.ToolsVersionStatus = "notRunning", "guestToolsNotRunning", "guestToolsCurrent"
	if vm.PowerState == "poweredOn" {
		vm.GuestState = "running"
		if r("tools") > 0.03 {
			vm.ToolsState = "guestToolsRunning"
		}
		vm.ToolsVersion = "12352"
	}
	switch t := r("toolsver"); {
	case svc == "vcls":
		vm.ToolsVersionStatus = "guestToolsUnmanaged"
	case t < 0.10:
		vm.ToolsVersionStatus = "guestToolsNeedUpgrade"
		vm.ToolsVersion = "11365"
	case t < 0.125:
		vm.ToolsVersionStatus, vm.ToolsVersion = "guestToolsNotInstalled", ""
		vm.ToolsState = "guestToolsNotRunning"
	}
	if vm.ToolsState == "guestToolsRunning" && svc != "vcls" {
		// Generated addresses stop at .209; the evidence anchors use .211+.
		vm.IPAddress = fmt.Sprintf("%s.%d.%d", s.prefix, c.subnet*4+k/200, 10+k%200)
		vm.GuestHostName = fmt.Sprintf("%s.%s.example.test", strings.ToLower(name), s.ctx)
	}

	// Folder and notes.
	vm.Folder = c.folder[svc]
	if vm.Folder == "" {
		vm.Folder = c.folder["*"]
	}
	if svc == "vcls" {
		vm.Folder, vm.Annotation = "/vCLS", "vSphere Cluster Service agent. Managed by vCenter; do not modify."
		vm.ManagedBy = &vsphere.VMManagedBy{ExtensionKey: "com.vmware.vim.eam", Type: "cluster-agent"}
	}

	// Disks: a boot disk on the primary datastore, plus a data disk elsewhere
	// for database and file tiers.
	ds := c.datastores
	primary := ds[pickN(len(ds), s.ctx, name, "ds")]
	if vm.PowerState == "poweredOff" && r("legacy") < 0.18 {
		if arch := s.archive(); arch != "" {
			primary = arch
		}
	}
	sizes := diskSizes(c.role, name, s.ctx)
	if svc == "vcls" {
		sizes = []int64{2}
		primary = ds[pickN(len(ds), s.ctx, c.name, "vcls")]
	}
	used := map[string]bool{}
	for i, gb := range sizes {
		dsName := primary
		if i > 0 && len(ds) > 1 {
			dsName = ds[pickN(len(ds), s.ctx, name, "data", fmt.Sprint(i))]
		}
		used[dsName] = true
		path := fmt.Sprintf("[%s] %s/%s.vmdk", dsName, name, name)
		if i > 0 {
			path = fmt.Sprintf("[%s] %s/%s_%d.vmdk", dsName, name, name, i)
		}
		vm.Disks = append(vm.Disks, disk(i+1, gb*gib, path, int32(i)))
		vm.StorageGB += float64(gb)
	}
	for _, d := range vm.Disks {
		n, _, _ := vsphere.SplitDatastorePath(d.BackingPath)
		if used[n] {
			vm.Datastores = append(vm.Datastores, n)
			used[n] = false
		}
	}

	// NICs.
	pg := nicNetwork(c.role, svc, name, s.ctx, pgNet)
	mac := fmt.Sprintf("00:50:56:%02x:%02x:%02x", hv(s.ctx, name, "m1")%0xc0, hv(s.ctx, name, "m2")%256, hv(s.ctx, name, "m3")%256)
	nic := vsphere.VMNIC{
		Key: 4000, Label: "Network adapter 1", Adapter: "VirtualVmxnet3", Network: pg.Name, NetworkID: pg.ID, SwitchID: dvsUUID, MACAddress: mac, MACAddressType: "generated",
		Connected: boolValue(vm.PowerState == "poweredOn"), StartsConnected: boolValue(true), UPTCompatible: boolValue(false),
	}
	if vm.IPAddress != "" {
		nic.IPv4 = []string{vm.IPAddress}
	}
	vm.NICs = []vsphere.VMNIC{nic}
	if c.role == "db" || (c.role == "app" && r("nic2") < 0.15) {
		nic2 := nic
		nic2.Key, nic2.Label = 4001, "Network adapter 2"
		nic2.MACAddress = fmt.Sprintf("00:50:56:%02x:%02x:%02x", hv(s.ctx, name, "n1")%0xc0, hv(s.ctx, name, "n2")%256, hv(s.ctx, name, "n3")%256)
		nic2.IPv4 = nil
		vm.NICs = append(vm.NICs, nic2)
	}

	// Snapshots.
	if svc != "vcls" && c.role != "vdi" && r("snap") < 0.08 {
		days := 2 + int(r("snapage")*700)
		if r("snapage") < 0.5 {
			days = 2 + int(r("snapage")*60)
		}
		created := demoNow.Add(-time.Duration(days) * 24 * time.Hour).Add(time.Duration(pickN(86400, s.ctx, name, "st")) * time.Second)
		vm.Snapshots = []vsphere.VMSnapshot{{
			ID: "snapshot-" + fmt.Sprint(1+pickN(9000, name)), NumericID: int32(1 + pickN(90, name)), Name: "pre-change", Description: "Before scheduled maintenance",
			CreateTime: created, PowerState: vm.PowerState, Current: true,
		}}
	}
}

func (s siteSpec) archive() string {
	for _, d := range s.datastores {
		if strings.HasPrefix(d.name, "nfs-archive") && !d.offline {
			return d.name
		}
	}
	return ""
}

func sizeFor(role, name, ctx string) (int32, int64) {
	i := pickN(4, ctx, name, "size")
	switch role {
	case "db":
		return []int32{8, 16, 16, 32}[i], []int64{64, 128, 256, 512}[i] << 10
	case "vdi":
		return 2, []int64{4, 8, 8, 16}[i] << 10
	case "mgmt":
		return []int32{2, 4, 4, 8}[i], []int64{8, 16, 16, 32}[i] << 10
	case "dmz":
		return []int32{2, 2, 4, 4}[i], []int64{4, 4, 8, 8}[i] << 10
	}
	return []int32{2, 4, 4, 8}[i], []int64{4, 8, 16, 32}[i] << 10
}

func diskSizes(role, name, ctx string) []int64 {
	i := pickN(4, ctx, name, "disk")
	switch role {
	case "db":
		return []int64{100, []int64{500, 1000, 2000, 4000}[i], []int64{200, 300, 500, 800}[i]}[:2+pickN(2, ctx, name, "n")]
	case "vdi":
		return []int64{80}
	case "dmz":
		return []int64{40}
	case "mgmt":
		return []int64{[]int64{40, 60, 100, 100}[i]}
	}
	if pickN(3, ctx, name, "n") == 0 {
		return []int64{60, []int64{100, 200, 300, 500}[i]}
	}
	return []int64{[]int64{40, 60, 80, 120}[i]}
}

func nicNetwork(role, svc, name, ctx string, pg map[string]vsphere.Network) vsphere.Network {
	pick := func(names ...string) vsphere.Network {
		for k := 0; k < len(names); k++ {
			if n, ok := pg[names[(pickN(len(names), ctx, name, "pg")+k)%len(names)]]; ok {
				return n
			}
		}
		for _, n := range pg {
			return n
		}
		return vsphere.Network{}
	}
	switch role {
	case "db":
		return pick("db-vlan-250", "backend-vlan-240")
	case "vdi":
		return pick("vdi-vlan-300", "vdi-mgmt-vlan-310")
	case "mgmt":
		return pick("mgmt-vlan-20", "monitoring-vlan-510")
	case "dmz":
		return pick("dmz-vlan-400", "dmz-web-vlan-410")
	}
	switch svc {
	case "web", "cdn", "gateway":
		return pick("web-vlan-110", "frontend-vlan-120")
	case "api", "auth":
		return pick("frontend-vlan-120", "api-vlan-140")
	}
	return pick("app-vlan-130", "backend-vlan-240", "frontend-vlan-120")
}

// tweakVM layers the configuration evidence the health rules look for onto a
// small, stable subset of VMs, so each rule has something to report.
func tweakVM(vm *vsphere.VM, c *clusterSpec, svc string) {
	r := func(part string) float64 { return frac(vm.Context, vm.Name, "tw", part) }
	if svc == "vcls" {
		return
	}
	if r("bios") < 0.10 {
		vm.Firmware = "bios"
	}
	if strings.Contains(vm.GuestOS, "Windows 11") {
		vm.SecureBootEnabled = boolValue(true)
		vm.TPMs = []vsphere.VMTPM{{Key: 11000, Label: "Virtual TPM"}}
	}
	if r("cps") < 0.05 {
		vm.CoresPerSocket = max(1, vm.CPU/2)
		vm.CPUSockets = vm.CPU / vm.CoresPerSocket
	}
	if c.role == "db" && r("resv") < 0.3 {
		res := int64(vm.MemoryMB)
		vm.MemoryAllocation = &vsphere.VMResourceAllocation{Reservation: &res, Limit: int64Value(-1)}
	}
	if r("cd") < 0.02 {
		ds := "iso-library-01"
		vm.CDROMs = []vsphere.VMCDROM{{Key: 3000, Label: "CD/DVD drive 1", Connected: boolValue(true), StartsConnected: boolValue(true), BackingType: "iso", BackingPath: "[" + ds + "] iso/installer.iso", BackingDatastore: ds}}
	}
	if r("usb") < 0.004 {
		vm.USBs = []vsphere.VMUSB{{Key: 7000, Label: "USB device 1", Connected: boolValue(true), Vendor: 0x0529, Product: 0x0001, BackingType: "hostDevice"}}
	}
	if r("flop") < 0.003 {
		vm.Floppies = []vsphere.VMFloppy{{Key: 8000, Label: "Floppy drive 1", Connected: boolValue(false)}}
	}
	if r("mac") < 0.01 && len(vm.NICs) > 0 {
		vm.NICs[0].MACAddressType = "manual"
	}
	if c.role == "db" && r("rdm") < 0.05 && len(vm.Disks) > 1 {
		vm.Disks[1].Raw, vm.Disks[1].BackingType, vm.Disks[1].RawLUNID = true, "RawDiskMappingVer1", "naa.60060160demo"+fmt.Sprintf("%04x", hv(vm.Name)%0xffff)
		vm.Disks[1].RawCompatibilityMode = "physicalMode"
	}
	if c.role == "db" && r("share") < 0.04 && len(vm.Disks) > 1 {
		vm.Disks[1].Sharing = "sharingMultiWriter"
	}
	if (c.role == "vdi" || svc == "worker") && r("gpu") < 0.01 {
		vm.PCIDevices = []vsphere.VMPCIDevice{{Key: 13000, Label: "PCI device 0", BackingType: "vgpu", VGPU: "grid_t4-4q", MigrateSupported: boolValue(true)}}
	}
}

func (e *estate) finishClusters(s siteSpec, hostIdx map[string][]int) {
	inv := e.inv
	// Size each host from what runs on it, so utilisation is believable.
	type load struct {
		vcpu int64
		mem  int64
		n    int
	}
	loads := map[string]*load{}
	for _, vm := range inv.VMs {
		l := loads[vm.Host]
		if l == nil {
			l = &load{}
			loads[vm.Host] = l
		}
		l.n++
		if vm.PowerState == "poweredOn" {
			l.vcpu += int64(vm.CPU)
			l.mem += vm.MemoryMB
		}
	}
	coreTiers := []int32{16, 24, 32, 48, 64, 96, 128}
	memTiers := []int64{128, 192, 256, 384, 512, 768, 1024, 1536, 2048}
	models := [][2]string{{"Dell Inc.", "PowerEdge R750"}, {"Dell Inc.", "PowerEdge R760"}, {"HPE", "ProLiant DL380 Gen11"}}
	versions := [][2]string{{"7.0.3", "23307199"}, {"8.0.2", "23305546"}, {"8.0.3", "24022515"}, {"8.0.3", "24022515"}}
	for i := range inv.Hosts {
		h := &inv.Hosts[i]
		l := loads[h.Name]
		if l == nil {
			l = &load{}
		}
		cores := coreTiers[len(coreTiers)-1]
		for _, c := range coreTiers {
			if int64(c)*4 >= l.vcpu {
				cores = c
				break
			}
		}
		memGB := memTiers[len(memTiers)-1]
		for _, m := range memTiers {
			if m*1024*7/10 >= l.mem {
				memGB = m
				break
			}
		}
		if h.Cluster == "mgmt" || strings.Contains(h.Cluster, "mgmt") {
			cores, memGB = max(cores, 32), max(memGB, 384)
		}
		m := models[pickN(len(models), s.ctx, h.Name, "model")]
		v := versions[pickN(len(versions), s.ctx, h.Name, "ver")]
		h.Vendor, h.Model, h.Version, h.Build = m[0], m[1], v[0], v[1]
		h.CPUCores, h.CPUThreads, h.CPUMHz = cores, cores*2, 2400
		h.TotalCPUMHz = int64(cores) * 2400
		h.MemoryMB = memGB * 1024
		h.VMCount = l.n
		h.CPUUsageMHz = int64(float64(h.TotalCPUMHz) * (0.18 + 0.5*frac(s.ctx, h.Name, "cpu")))
		h.MemoryUsageMB = min(l.mem*9/10+int64(20480), h.MemoryMB*92/100)
		if h.ConnectionState != "connected" {
			h.CPUUsageMHz, h.MemoryUsageMB = 0, 0
			h.PowerState = "unknown"
		}
	}
	for ci := range s.clusters {
		c := &s.clusters[ci]
		cl := vsphere.Cluster{
			Location: vsphere.Location{Context: s.ctx, Datacenter: s.dc, Path: "/" + s.dc + "/host/" + c.name}, ID: fmt.Sprintf("%s-cluster-%d", s.ctx, ci+1), Name: c.name,
			DRSEnabled: c.role != "dmz", HAEnabled: true,
		}
		busiest := ""
		var busiestUse float64
		for _, i := range hostIdx[c.name] {
			h := inv.Hosts[i]
			if use := float64(h.MemoryUsageMB) / float64(max(h.MemoryMB, 1)); use > busiestUse {
				busiest, busiestUse = h.Name, use
			}
			cl.Hosts++
			if h.ConnectionState == "connected" && !h.InMaintenance {
				cl.EffectiveHost++
			}
			cl.CPUCores += h.CPUCores
			cl.TotalCPUMHz += h.TotalCPUMHz
			cl.TotalMemoryMB += h.MemoryMB
			if h.ConnectionState == "connected" && !h.InMaintenance {
				// What vSphere reports as effective is the healthy hosts'
				// capacity less the hypervisor's own share.
				cl.EffectiveCPUMHz += h.TotalCPUMHz * 92 / 100
				cl.EffectiveMemoryMB += h.MemoryMB * 94 / 100
			}
		}
		clusterHealth(&cl, c, ci, s, busiest)
		inv.Clusters = append(inv.Clusters, cl)
	}
}

// clusterHealth gives each cluster the HA, DRS and EVC settings its role
// suggests, with the evidence the cluster workspace is built to surface: the
// second cluster has lost a host and can no longer cover a failure, and the
// management cluster runs with admission control off. The alarms behind a
// yellow or red status name busiest, the member host using the most memory.
func clusterHealth(cl *vsphere.Cluster, c *clusterSpec, ci int, s siteSpec, busiest string) {
	cl.OverallStatus = "green"
	cl.AlarmsRead = true
	if c.role != "dmz" {
		cl.EVCMode = "intel-icelake"
		cl.DRSBehavior = "fullyAutomated"
		if c.role == "db" {
			cl.DRSBehavior = "partiallyAutomated"
		}
		cl.DRSScore = int32(78 + pickN(20, s.ctx, c.name, "drs"))
	}
	ha := &vsphere.ClusterHA{
		HostMonitoring: "enabled", VMMonitoring: "vmMonitoringOnly", AdmissionControl: true,
		Policy: vsphere.HAPolicyResources, CPUReservePct: 25, MemReservePct: 25, FailoverLevel: 1,
		CPUFailoverPct: int32(30 + pickN(30, s.ctx, c.name, "cpu-fo")), MemFailoverPct: int32(28 + pickN(25, s.ctx, c.name, "mem-fo")),
	}
	switch {
	case ci == 1:
		ha.MemFailoverPct = 11
		cl.OverallStatus = "red"
		cl.ConfigIssues = []string{"Insufficient vSphere HA failover resources"}
		cl.Alarms = []vsphere.Alarm{
			{Name: "Insufficient vSphere HA failover resources", Status: "red", Entity: cl.Name, EntityType: "ClusterComputeResource"},
			{Name: "Host memory usage", Status: "yellow", Entity: busiest, EntityType: "HostSystem"},
		}
	case c.role == "mgmt":
		ha.AdmissionControl = false
		cl.OverallStatus = "yellow"
		cl.Alarms = []vsphere.Alarm{{Name: "Host memory usage", Status: "yellow", Entity: busiest, EntityType: "HostSystem"}}
	case c.role == "dmz":
		ha.Policy, ha.CPUReservePct, ha.MemReservePct, ha.CPUFailoverPct, ha.MemFailoverPct = vsphere.HAPolicyHostFailures, 0, 0, 0, 0
		ha.CurrentFailoverLevel = 1
		ha.VMMonitoring = "vmMonitoringDisabled"
	}
	cl.HA = ha
}

// mountDatastores mounts each cluster's datastores on its hosts, a local
// datastore only on the host it is named after. The first cluster's host in
// maintenance came back without its last datastore: the kind of gap the
// cluster Storage page exists to show.
func (e *estate) mountDatastores(s siteSpec, hostIdx map[string][]int) {
	local := map[string]bool{}
	for _, d := range s.datastores {
		local[d.name] = d.local
	}
	for ci := range s.clusters {
		c := &s.clusters[ci]
		for _, i := range hostIdx[c.name] {
			h := &e.inv.Hosts[i]
			var mounts []string
			for _, ds := range c.datastores {
				if local[ds] && ds != "local-"+h.Name {
					continue
				}
				mounts = append(mounts, ds)
			}
			if ci == 0 && h.InMaintenance && len(mounts) > 1 {
				mounts = mounts[:len(mounts)-1]
			}
			sort.Strings(mounts)
			h.Datastores = mounts
		}
	}
}

func (e *estate) finishDatastores(s siteSpec) {
	inv := e.inv
	stored := map[string]int64{}
	for _, vm := range append(append([]vsphere.VM(nil), inv.VMs...), inv.Templates...) {
		for _, d := range vm.Disks {
			if n, _, ok := vsphere.SplitDatastorePath(d.BackingPath); ok {
				stored[n] += d.CapacityBytes * 6 / 10
			}
		}
	}
	for i, spec := range s.datastores {
		ds := &inv.Datastores[i]
		fill := spec.fill
		natural := spec.fill == 0
		if natural {
			fill = 0.42 + 0.4*frac(s.ctx, spec.name, "fill")
		}
		used := stored[spec.name]
		capBytes := spec.capTiB * tib
		switch {
		case capBytes == 0:
			capBytes = max((int64(float64(used)/fill/float64(tib))+1)*tib, 2*tib)
			if !natural {
				used = int64(float64(capBytes) * fill)
			}
		default:
			used = max(used, int64(float64(capBytes)*fill))
		}
		ds.CapacityBytes, ds.FreeBytes = capBytes, capBytes-used
		switch {
		case spec.sharedUUID != "":
			ds.Backing = vsphere.DatastoreBacking{VMFSUUID: spec.sharedUUID, Extents: []string{"naa.demo.6000"}}
		case spec.typ == "NFS":
			ds.Backing = vsphere.DatastoreBacking{URL: "ds:///nfs/" + s.ctx + "/" + spec.name + "/"}
		default:
			ds.Backing = vsphere.DatastoreBacking{VMFSUUID: "demo-vmfs-" + s.ctx + "-" + spec.name, Extents: []string{"naa.demo." + fmt.Sprintf("%04x", hv(s.ctx, spec.name)%0xffff)}, Local: spec.local}
		}
		if spec.browse {
			ds.BrowseStatus = "success"
			ds.Files = e.filesFor(spec.name)
		}
	}
}

// filesFor lists the VMDKs the VMs and templates on a browsable datastore own,
// plus a snapshot delta for every VM that carries a snapshot.
func (e *estate) filesFor(ds string) []vsphere.DatastoreFile {
	var files []vsphere.DatastoreFile
	for _, vm := range append(append([]vsphere.VM(nil), e.inv.VMs...), e.inv.Templates...) {
		for _, d := range vm.Disks {
			if n, _, ok := vsphere.SplitDatastorePath(d.BackingPath); ok && n == ds {
				files = append(files, vsphere.DatastoreFile{Path: d.BackingPath, SizeBytes: d.CapacityBytes * 6 / 10})
				if len(vm.Snapshots) > 0 && strings.HasSuffix(d.BackingPath, "/"+vm.Name+".vmdk") {
					files = append(files, vsphere.DatastoreFile{Path: strings.TrimSuffix(d.BackingPath, ".vmdk") + "-000001.vmdk", SizeBytes: 4 * gib})
				}
			}
		}
	}
	for i := range files {
		files[i].Modified = demoNow.Add(-time.Duration(10+pickN(120, files[i].Path)) * 24 * time.Hour)
		if strings.Contains(files[i].Path, "] finance01/finance01.vmdk") {
			files[i].SizeBytes, files[i].Modified = 420*gib, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
		}
	}
	files = append(files, e.extraFiles[ds]...)
	sort.SliceStable(files, func(a, b int) bool { return files[a].Path < files[b].Path })
	return files
}
