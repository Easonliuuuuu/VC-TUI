package demo

import (
	"fmt"
	"strings"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

type locFunc func(kind, path string) vsphere.Location

func (e *estate) vmByName(name string) *vsphere.VM {
	for i := range e.inv.VMs {
		if e.inv.VMs[i].Name == name {
			return &e.inv.VMs[i]
		}
	}
	return nil
}

func vmRef(id string) string { return "VirtualMachine:" + id }

// finishVApps groups generated VMs into the vApps the site declares. A VM
// belongs to at most one vApp, and only to one in its own cluster.
func (e *estate) finishVApps(s siteSpec, placed []placedVM, loc locFunc) {
	byName := map[string]int{}
	for _, spec := range s.vapps {
		var members []*vsphere.VM
		n := 0
		if spec.max > 0 {
			n = 3 + pickN(max(1, spec.max-2), s.ctx, spec.name, "n")
		}
		for i := range e.inv.VMs {
			vm := &e.inv.VMs[i]
			if len(members) >= n {
				break
			}
			if vm.Cluster != spec.cluster || e.vappOf[vm.Name] != "" || vm.ManagedBy != nil {
				continue
			}
			for _, p := range placed {
				if p.vm.Name == vm.Name && contains(spec.svcs, p.svc) {
					members = append(members, vm)
					e.vappOf[vm.Name] = spec.name
					break
				}
			}
		}
		letter := ""
		for _, c := range s.clusters {
			if c.name == spec.cluster {
				letter = c.letter
			}
		}
		id := fmt.Sprintf("%s-vapp-%d", s.ctx, len(e.inv.VApps)+1)
		v := vsphere.VApp{
			ID: id, Name: spec.name, Status: "started", Cluster: spec.cluster, ComputeResource: spec.cluster,
			ParentContainer: spec.cluster + "/Resources",
		}
		if pickN(6, s.ctx, spec.name, "st") == 0 || spec.max == 0 {
			v.Status = "stopped"
		}
		path := spec.cluster + "/Resources/" + spec.name
		if spec.parent != "" {
			pi := byName[spec.parent]
			parent := &e.inv.VApps[pi]
			path = strings.TrimPrefix(parent.Path, "/"+s.dc+"/host/") + "/" + spec.name
			v.ParentContainer, v.ParentVApp = spec.parent, spec.parent
			parent.ChildVApps = append(parent.ChildVApps, spec.name)
			parent.ChildVAppRefs = append(parent.ChildVAppRefs, "VirtualApp:"+id)
			parent.ChildVAppCount++
		}
		v.Location = loc("host", path)
		for _, m := range members {
			v.DirectVMs = append(v.DirectVMs, m.Name)
			v.DirectVMRefs = append(v.DirectVMRefs, vmRef(m.ID))
		}
		v.DirectVMCount = len(members)
		v.OverallStatus, v.ConfigStatus = "green", "green"
		v.Allocation = demoVAppAllocation(s.ctx, spec.name, members)
		v.StartOrder = demoVAppStartOrder(members)
		if spec.childPool != "" {
			v.ChildResourcePools = []string{spec.childPool}
			v.ChildResourcePoolRefs = []string{"ResourcePool:" + poolID(s.ctx, letter, spec.childPool)}
			v.ChildResourcePoolCount = 1
		}
		byName[spec.name] = len(e.inv.VApps)
		e.inv.VApps = append(e.inv.VApps, v)
	}
	e.sizeVAppLimits()
}

// sizeVAppLimits sets each capped vApp's CPU limit from every vCPU beneath
// it, nested vApps included, since a vApp's limit covers them too. Sized from
// direct members alone, a parent's members could sum past its limit, which
// real vSphere never allows.
func (e *estate) sizeVAppLimits() {
	cpus := map[string]int64{}
	for _, vm := range e.inv.VMs {
		cpus[vmRef(vm.ID)] = int64(vm.CPU)
	}
	byID := map[string]*vsphere.VApp{}
	for i := range e.inv.VApps {
		byID["VirtualApp:"+e.inv.VApps[i].ID] = &e.inv.VApps[i]
	}
	var treeCPUs func(v *vsphere.VApp) int64
	treeCPUs = func(v *vsphere.VApp) int64 {
		n := int64(0)
		for _, ref := range v.DirectVMRefs {
			n += cpus[ref]
		}
		for _, ref := range v.ChildVAppRefs {
			if child := byID[ref]; child != nil {
				n += treeCPUs(child)
			}
		}
		return n
	}
	for i := range e.inv.VApps {
		v := &e.inv.VApps[i]
		if a := v.Allocation; a != nil && a.CPULimitMHz != nil && *a.CPULimitMHz > 0 {
			a.CPULimitMHz = int64Value(max(1000, treeCPUs(v)*2000))
		}
	}
}

// demoVAppAllocation gives about half the vApps a CPU limit and a memory
// reservation, so the dashboard shows both a capped and an uncapped vApp.
func demoVAppAllocation(ctx, name string, members []*vsphere.VM) *vsphere.ResourceAllocation {
	var mem int64
	var cpus int64
	for _, m := range members {
		mem += m.MemoryMB
		cpus += int64(m.CPU)
	}
	a := &vsphere.ResourceAllocation{
		CPUReservationMHz: int64Value(0), CPULimitMHz: int64Value(-1), CPUExpandable: true, CPUShares: 4000, CPULevel: "normal",
		MemConfiguredMB: mem, MemReservationMB: int64Value(0), MemLimitMB: int64Value(-1), MemExpandable: true, MemShares: 163840, MemLevel: "normal",
	}
	if pickN(2, ctx, name, "cap") == 0 && cpus > 0 {
		// 2 GHz per vCPU on 2.4 GHz cores: a busy member brings the vApp
		// close to its cap.
		a.CPULimitMHz = int64Value(max(1000, cpus*2000))
		a.MemReservationMB = int64Value(mem / 4)
	}
	return a
}

// demoVAppStartOrder starts the first member alone, then the rest together,
// the usual database-then-application shape.
func demoVAppStartOrder(members []*vsphere.VM) []vsphere.VAppStartEntry {
	out := make([]vsphere.VAppStartEntry, 0, len(members))
	for i, m := range members {
		order, delay := int32(2), int32(120)
		if i == 0 {
			order, delay = 1, 60
		}
		out = append(out, vsphere.VAppStartEntry{
			Ref: vmRef(m.ID), Name: m.Name, Order: order, DelaySeconds: delay,
			StartAction: "powerOn", StopAction: "guestShutdown", StopDelaySeconds: 120, WaitForGuest: i == 0,
		})
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func poolID(ctx, letter, name string) string { return ctx + "-pool-" + letter + "-" + name }

// finishPools builds each cluster's root pool and its child pools. VMs that a
// vApp owns are excluded, as they are in vSphere.
func (e *estate) finishPools(s siteSpec, placed []placedVM, loc locFunc) {
	for _, c := range s.clusters {
		inPool := map[string]bool{}
		type memb struct {
			refs []string
			mem  int64
		}
		children := make([]memb, len(c.pools))
		var root memb
		for _, p := range placed {
			if p.cluster.name != c.name || e.vappOf[p.vm.Name] != "" {
				continue
			}
			vm := e.vmByName(p.vm.Name)
			hit := false
			for i, ps := range c.pools {
				if contains(ps.svcs, p.svc) {
					children[i].refs = append(children[i].refs, vm.ID)
					children[i].mem += vm.MemoryMB
					hit = true
					break
				}
			}
			if !hit {
				root.refs = append(root.refs, vm.ID)
				root.mem += vm.MemoryMB
			}
			inPool[vm.Name] = true
		}
		var totalMem int64
		for _, cl := range e.inv.Clusters {
			if cl.Name == c.name {
				totalMem = cl.TotalMemoryMB
			}
		}
		e.inv.ResourcePools = append(e.inv.ResourcePools, vsphere.ResourcePool{
			Location: loc("host", c.name+"/Resources"), ID: fmt.Sprintf("%s-pool-root-%s", s.ctx, c.letter), Name: "Resources", Root: true, Owner: c.name,
			Status: "green", ConfigStatus: "green", VMRefs: root.refs,
			ResourceAllocation: vsphere.ResourceAllocation{
				CPUReservationMHz: int64Value(0), CPULimitMHz: int64Value(-1), CPUExpandable: true, CPUShares: 4000, CPULevel: "normal",
				MemConfiguredMB: totalMem, MemReservationMB: int64Value(0), MemLimitMB: int64Value(-1), MemExpandable: true, MemShares: 4000, MemLevel: "normal",
			},
		})
		for i, ps := range c.pools {
			limitMem := children[i].mem * 3 / 2
			e.inv.ResourcePools = append(e.inv.ResourcePools, vsphere.ResourcePool{
				Location: loc("host", c.name+"/Resources/"+ps.name), ID: poolID(s.ctx, c.letter, ps.name), Name: ps.name, Parent: "Resources", Owner: c.name,
				Status: "green", ConfigStatus: "green", VMRefs: children[i].refs,
				ResourceAllocation: vsphere.ResourceAllocation{
					CPUReservationMHz: int64Value(int64(500 * (1 + pickN(8, s.ctx, ps.name)))), CPULimitMHz: int64Value(int64(8000 * (1 + pickN(6, s.ctx, ps.name)))), CPUExpandable: true,
					CPUShares: []int32{1000, 2000, 4000}[pickN(3, ps.name)], CPULevel: []string{"low", "normal", "high"}[pickN(3, ps.name)],
					MemConfiguredMB: children[i].mem, MemReservationMB: int64Value(children[i].mem / 4), MemLimitMB: int64Value(max(limitMem, 4096)), MemExpandable: true,
					MemShares: 2000, MemLevel: "normal",
				},
			})
		}
	}
}

func setBoot(vm *vsphere.VM, ds string, sizeGiB int64) {
	vm.Disks[0].CapacityBytes = sizeGiB * gib
	vm.Disks[0].BackingPath = fmt.Sprintf("[%s] %s/%s.vmdk", ds, vm.Name, vm.Name)
	vm.StorageGB, vm.Datastores = 0, nil
	seen := map[string]bool{}
	for _, d := range vm.Disks {
		vm.StorageGB += float64(d.CapacityBytes / gib)
		if n, _, ok := vsphere.SplitDatastorePath(d.BackingPath); ok && !seen[n] {
			seen[n] = true
			vm.Datastores = append(vm.Datastores, n)
		}
	}
}

func forceOn(vm *vsphere.VM, ip string) {
	vm.PowerState, vm.ConnectionState, vm.GuestState = "poweredOn", "connected", "running"
	vm.ToolsState, vm.ToolsVersionStatus, vm.ToolsVersion = "guestToolsRunning", "guestToolsCurrent", "12352"
	vm.IPAddress = ip
	if len(vm.NICs) > 0 {
		vm.NICs[0].IPv4, vm.NICs[0].Connected = []string{ip}, boolValue(true)
	}
	vm.GuestHostName = strings.ToLower(vm.Name) + "." + vm.Context + ".example.test"
}

// applyAnchors layers the hand-authored evidence the health workstream and the
// scenarios depend on onto the generated estate, by name.
//
// The four-way coupling internal/demo/backend_test.go asserts lives here: prod
// nvme-01 and edge san-prod-01 present the same LUN; prod nvme-01 holds an
// unreferenced finance01.vmdk that an edge VM does reference (evidence in
// another context), and a lost+found VMDK nothing references anywhere.
func (e *estate) applyAnchors(s siteSpec) {
	switch s.anchor {
	case "prod":
		if vm := e.vmByName("api-01"); vm != nil {
			forceOn(vm, s.prefix+".0.211")
			setBoot(vm, "nvme-01", 80)
			vm.Annotation = "customer API"
			vm.Snapshots = []vsphere.VMSnapshot{{ID: "snapshot-11", NumericID: 11, Name: "pre-upgrade", CreateTime: demoNow.Add(-9 * 24 * time.Hour), PowerState: "poweredOn", Current: true}}
		}
		if vm := e.vmByName("postgres-01"); vm != nil {
			forceOn(vm, s.prefix+".8.221")
			vm.Annotation = "primary database"
		}
		if vm := e.vmByName("build-runner-03"); vm != nil {
			vm.PowerState, vm.ConnectionState, vm.GuestState = "poweredOff", "orphaned", "notRunning"
			vm.ToolsState, vm.IPAddress, vm.GuestHostName = "guestToolsNotRunning", "", ""
			vm.Snapshots = nil
			setBoot(vm, "nvme-01", 120)
		}
		if len(e.inv.Templates) > 0 {
			t := &e.inv.Templates[0]
			t.Disks[0].BackingPath, t.Datastores = "[nvme-01] templates/"+t.Name+".vmdk", []string{"nvme-01"}
		}
		e.extraFiles["nvme-01"] = []vsphere.DatastoreFile{
			{Path: "[nvme-01] lost+found/orphan.vmdk", SizeBytes: 20 * gib, Modified: demoNow.Add(-40 * 24 * time.Hour)},
			{Path: "[nvme-01] finance01/finance01.vmdk", SizeBytes: 420 * gib, Modified: time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)},
		}
	case "edge":
		if vm := e.vmByName("finance01"); vm != nil {
			forceOn(vm, s.prefix+".4.231")
			vm.Disks = vm.Disks[:1]
			vm.Disks[0].CapacityBytes = 420 * gib
			vm.Disks[0].BackingPath = "[san-prod-01] finance01/finance01.vmdk"
			vm.Snapshots, vm.Datastores, vm.StorageGB = nil, []string{"san-prod-01"}, 420
			vm.Annotation = "finance ledger"
		}
		if vm := e.vmByName("api-01"); vm != nil {
			forceOn(vm, s.prefix+".0.211")
			vm.Annotation = "customer API"
		}
		if vm := e.vmByName("postgres-01"); vm != nil {
			forceOn(vm, s.prefix+".4.221")
			vm.Annotation = "primary database"
		}
	}
}

// Demo wiring faults, one of each kind the switch views look for. They are
// named by host, so they land only on the production site's hosts.
const (
	slowUplinkHost   = "esxi-a-07"   // DVS-Production dvUplink2 at 10G
	unassignedHost   = "esxi-b-04"   // DVS-Production dvUplink2 with no NIC
	notOnStorageHost = "esxi-db-08"  // in db-cluster but not on DVS-Storage
	deadLinkHost     = "esxi-dmz-02" // DVS-Storage dvUplink1 has no link
)

// wireSwitches gives every host its distributed-switch uplinks and a vMotion
// VMkernel adapter on DVS-Storage: vmnic2 and vmnic3 behind DVS-Production,
// vmnic4 and vmnic5 behind DVS-Storage, with the faults above.
func (e *estate) wireSwitches(prod, storage *vsphere.DVSwitch) {
	vmotionKey := ""
	for _, pg := range storage.PortGroups {
		if strings.HasPrefix(pg.Name, "vmotion-") {
			vmotionKey = pg.Key
		}
	}
	var storageHosts []string
	for i := range e.inv.Hosts {
		h := &e.inv.Hosts[i]
		n := i + 1
		key := e.inv.Context + "-" + h.Name
		for j := 2; j < 6; j++ {
			speed := int32Value(25000)
			switch {
			case h.Name == slowUplinkHost && j == 3:
				speed = int32Value(10000)
			case h.Name == deadLinkHost && j == 4:
				speed = nil
			}
			nic := vsphere.HostNIC{Key: fmt.Sprintf("%s-pnic-%d", key, j), Device: fmt.Sprintf("vmnic%d", j), PCI: fmt.Sprintf("0000:%02x:00.0", 0x18+j), Driver: "i40en",
				MAC: fmt.Sprintf("00:50:56:aa:%02x:%02x", n%256, j), LinkSpeedMB: speed}
			if speed != nil {
				nic.Duplex = boolValue(true)
			}
			h.NICs = append(h.NICs, nic)
		}
		second := "vmnic3"
		if h.Name == unassignedHost {
			second = ""
		}
		h.ProxySwitches = []vsphere.HostProxySwitch{{Key: key + "-proxy-prod", Switch: prod.Name, SwitchUUID: prod.UUID, MTU: prod.MaxMTU,
			Uplinks: []vsphere.HostProxyUplink{{Name: "dvUplink1", NIC: "vmnic2"}, {Name: "dvUplink2", NIC: second}}}}
		h.PortGroups = append(h.PortGroups, vsphere.HostPortGroup{Key: key + "-port-vm", Name: "VM Network", Switch: "vSwitch0", Promiscuous: boolValue(false), MACChanges: boolValue(true), ForgedTransmits: boolValue(true)})
		if h.Name == notOnStorageHost {
			continue
		}
		storageHosts = append(storageHosts, h.Name)
		h.ProxySwitches = append(h.ProxySwitches, vsphere.HostProxySwitch{Key: key + "-proxy-storage", Switch: storage.Name, SwitchUUID: storage.UUID, MTU: storage.MaxMTU,
			Uplinks: []vsphere.HostProxyUplink{{Name: "dvUplink1", NIC: "vmnic4"}, {Name: "dvUplink2", NIC: "vmnic5"}}})
		if vmotionKey != "" {
			h.VMKs = append(h.VMKs, vsphere.HostVMKernel{Key: key + "-vmk1", Device: "vmk1", DVSwitchUUID: storage.UUID, DVPortGroupKey: vmotionKey,
				MAC: fmt.Sprintf("00:50:56:ab:%02x:02", n%256), MTU: 9000, TSO: boolValue(true), Netstack: "vmotion", DHCP: boolValue(false),
				IP: fmt.Sprintf("172.16.30.%d", n), SubnetMask: "255.255.255.0"})
		}
	}
	storage.Hosts = storageHosts
}

// finishHosts adds the per-host network and storage configuration the host
// detail pane and the host-configuration sheets read.
func (e *estate) finishHosts(s siteSpec) {
	for i := range e.inv.Hosts {
		h := &e.inv.Hosts[i]
		n := i + 1
		key := s.ctx + "-" + h.Name
		fc := pickN(2, s.ctx, h.Name, "hba") == 0
		hba := vsphere.HostHBA{Key: key + "-hba", Device: "vmhba0", Bus: 3, Status: "online", Model: "QLogic 2692", Driver: "qlnativefc", PCI: "0000:5e:00.0", StorageProtocol: "fc", Type: "HostFibreChannelHba",
			WWNN: int64Value(0x50014380242a0000 + int64(n)), WWPN: int64Value(0x50014380242b0000 + int64(n))}
		if !fc {
			hba = vsphere.HostHBA{Key: key + "-hba", Device: "vmhba1", Bus: 4, Status: "online", Model: "Broadcom 57508", Driver: "bnxtroce", PCI: "0000:af:00.0", StorageProtocol: "iscsi", Type: "HostInternetScsiHba",
				IScsiName: "iqn.2026-09.example:" + s.ctx + ":" + h.Name, IScsiAlias: h.Name + ".example.internal"}
		}
		h.HBAs = []vsphere.HostHBA{hba}
		for j := 0; j < 2; j++ {
			h.NICs = append(h.NICs, vsphere.HostNIC{Key: fmt.Sprintf("%s-pnic-%d", key, j), Device: fmt.Sprintf("vmnic%d", j), PCI: fmt.Sprintf("0000:%02x:00.0", 0x18+j), Driver: "i40en",
				MAC: fmt.Sprintf("00:50:56:aa:%02x:%02x", n%256, j), LinkSpeedMB: int32Value(25000), Duplex: boolValue(true), Switch: "vSwitch0"})
		}
		h.VSwitches = []vsphere.HostVSwitch{{Key: key + "-switch", Name: "vSwitch0", NumPorts: 128, FreePorts: int32(118 + pickN(8, key)), MTU: 1500, Uplinks: []string{"vmnic0", "vmnic1"},
			Promiscuous: boolValue(false), MACChanges: boolValue(true), ForgedTransmits: boolValue(true), TrafficShaping: boolValue(false)}}
		h.PortGroups = []vsphere.HostPortGroup{{Key: key + "-port", Name: "Management Network", Switch: "vSwitch0", VLAN: 20, Promiscuous: boolValue(false), MACChanges: boolValue(true), ForgedTransmits: boolValue(true)}}
		h.VMKs = []vsphere.HostVMKernel{{Key: key + "-vmk", Device: "vmk0", PortGroup: "Management Network", MAC: fmt.Sprintf("00:50:56:ab:%02x:01", n%256), MTU: 1500, TSO: boolValue(true),
			Netstack: "defaultTcpipStack", DHCP: boolValue(false), IP: fmt.Sprintf("%s.250.%d", s.prefix, n), SubnetMask: "255.255.255.0"}}
		paths, active, standby := 4, 2, 2
		if pickN(30, s.ctx, h.Name, "path") == 0 {
			paths, active, standby = 1, 1, 0
		}
		for l := 0; l < 2; l++ {
			h.Multipaths = append(h.Multipaths, vsphere.HostMultipath{Key: fmt.Sprintf("%s-lun-%d", key, l), LUN: fmt.Sprintf("naa.60060160.demo.%04x%02x", hv(s.ctx, "lun")%0xffff, l),
				DevicePath: fmt.Sprintf("/vmfs/devices/disks/naa.60060160.demo.%04x%02x", hv(s.ctx, "lun")%0xffff, l), Policy: "VMW_PSP_RR", LocalDisk: boolValue(false),
				PathCount: paths, Active: active, Standby: standby, WorkingPaths: active})
		}
	}
}
