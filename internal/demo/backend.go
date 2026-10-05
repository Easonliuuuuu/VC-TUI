// Package demo provides deterministic, synthetic vCenter data for recording
// and presenting the terminal interface. It never opens a network connection
// or reads configuration and credentials from the operator's machine.
package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/contextops"
	"github.com/easonliuuuuu/vsfleet/internal/session"
	"github.com/easonliuuuuu/vsfleet/internal/tui"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Backend is a read-only TUI backend populated with sample inventory. It is
// intentionally separate from the production command so demo data can never
// be mistaken for a live vCenter.
type Backend struct {
	contexts    []*config.Context
	inventories map[string]*vsphere.Inventory
	estates     map[string]*estate
	failures    map[string]error
	diagnoses   map[string]*vsphere.Diagnosis
}

// NewBackend returns a stable three-vCenter estate: two healthy contexts with
// different routes and one unreachable disaster-recovery site. That shape
// demonstrates the central promise of vsfleet: healthy results remain useful
// when another vCenter is offline.
func NewBackend() *Backend {
	contexts := []*config.Context{
		newContext("prod-vc", "https://vcsa.prod.example", config.TransportConfig{Type: config.TransportDirect}),
		newContext("edge-vc", "https://vcsa.edge.example", config.TransportConfig{
			Type: config.TransportSOCKS5, Address: "127.0.0.1:1080", RemoteDNS: true,
		}),
		newContext("dr-site", "https://vcsa.dr.example", config.TransportConfig{
			Type: config.TransportHTTPProxy, Address: "10.24.0.8:3128",
		}),
	}

	prod, edge := buildEstate(prodSite()), buildEstate(edgeSite())
	return &Backend{
		contexts: contexts,
		inventories: map[string]*vsphere.Inventory{
			"prod-vc": prod.inv,
			"edge-vc": edge.inv,
		},
		estates: map[string]*estate{"prod-vc": prod, "edge-vc": edge},
		failures: map[string]error{
			"dr-site": errors.New("proxy 10.24.0.8:3128: connection refused"),
		},
		diagnoses: map[string]*vsphere.Diagnosis{
			"prod-vc": healthyDiagnosis(contexts[0], 84*time.Millisecond),
			"edge-vc": healthyDiagnosis(contexts[1], 127*time.Millisecond),
			"dr-site": failedDiagnosis(contexts[2]),
		},
	}
}

func newContext(name, endpoint string, route config.TransportConfig) *config.Context {
	cc := &config.Context{
		Name: name, Endpoint: endpoint, Username: "operator@vsphere.local",
		Transport: route, TLS: config.TLSConfig{Mode: config.TLSSystem},
	}
	cc.Normalize()
	return cc
}

// ListDatastoreDirectory implements the TUI's datastore browser extension
// from the demo estate's own file fixtures. The presentation promises it
// dials nothing, so the browser is answered by splitting the sample file
// paths into directories rather than by connecting anywhere.
func (b *Backend) ListDatastoreDirectory(_ context.Context, cc *config.Context, _, datastore, relative string) (vsphere.DatastoreListing, error) {
	files, err := b.demoFiles(cc, datastore)
	if err != nil {
		return vsphere.DatastoreListing{}, err
	}
	relative = strings.Trim(relative, "/")
	seen := map[string]bool{}
	var out vsphere.DatastoreListing
	for _, file := range files {
		_, path, ok := vsphere.SplitBrowsePath(file.Path)
		if !ok {
			continue
		}
		rest, inside := demoUnder(path, relative)
		if !inside {
			continue
		}
		name, _, isDir := strings.Cut(rest, "/")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		entry := vsphere.DatastoreEntry{
			Name: name,
			Path: "[" + datastore + "] " + strings.Trim(relative+"/"+name, "/"),
			Type: vsphere.DatastoreEntryFile,
		}
		if isDir {
			entry.Type = vsphere.DatastoreEntryFolder
		} else {
			entry.SizeBytes, entry.Modified = file.SizeBytes, file.Modified
		}
		out.Entries = append(out.Entries, entry)
	}
	sort.SliceStable(out.Entries, func(i, j int) bool {
		if (out.Entries[i].Type == vsphere.DatastoreEntryFolder) != (out.Entries[j].Type == vsphere.DatastoreEntryFolder) {
			return out.Entries[i].Type == vsphere.DatastoreEntryFolder
		}
		return out.Entries[i].Name < out.Entries[j].Name
	})
	return out, nil
}

// FindInDatastore implements the recursive half of the same extension. The
// pattern is matched as a glob against the leaf name, which is close enough
// to what a vCenter does for a demonstration.
func (b *Backend) FindInDatastore(_ context.Context, cc *config.Context, _, datastore, pattern string) (vsphere.DatastoreListing, error) {
	files, err := b.demoFiles(cc, datastore)
	if err != nil {
		return vsphere.DatastoreListing{}, err
	}
	var out vsphere.DatastoreListing
	for _, file := range files {
		_, path, ok := vsphere.SplitBrowsePath(file.Path)
		if !ok {
			continue
		}
		name := path
		if i := strings.LastIndexByte(path, '/'); i >= 0 {
			name = path[i+1:]
		}
		if matched, _ := filepath.Match(strings.ToLower(pattern), strings.ToLower(name)); !matched {
			continue
		}
		out.Entries = append(out.Entries, vsphere.DatastoreEntry{
			Name:      name,
			Path:      file.Path,
			Type:      vsphere.DatastoreEntryFile,
			SizeBytes: file.SizeBytes,
			Modified:  file.Modified,
		})
	}
	return out, nil
}

// ListDatastoreVMReferences implements the live relationship extension from
// the same deterministic inventory used by the demo browser.
func (b *Backend) ListDatastoreVMReferences(_ context.Context, cc *config.Context, datastoreID string) (vsphere.DatastoreReferenceListing, error) {
	if cc == nil {
		return vsphere.DatastoreReferenceListing{}, errors.New("no context selected")
	}
	inv, ok := b.inventories[cc.Name]
	if !ok || inv == nil {
		return vsphere.DatastoreReferenceListing{}, fmt.Errorf("demo inventory for %q not found", cc.Name)
	}
	var datastoreName string
	for _, ds := range inv.Datastores {
		if ds.ID == datastoreID {
			datastoreName = ds.Name
			break
		}
	}
	if datastoreName == "" {
		return vsphere.DatastoreReferenceListing{}, fmt.Errorf("demo datastore %q not found", datastoreID)
	}
	out := vsphere.DatastoreReferenceListing{}
	vms := append(append([]vsphere.VM(nil), inv.VMs...), inv.Templates...)
	out.TotalVMs = len(vms)
	out.CheckedVMs = len(vms)
	for _, vm := range vms {
		for _, disk := range vm.Disks {
			name, _, ok := vsphere.SplitDatastorePath(disk.BackingPath)
			if !ok || !strings.EqualFold(name, datastoreName) {
				continue
			}
			out.References = append(out.References, vsphere.DatastoreVMReference{
				Context: cc.Name, VMID: vm.ID, VMName: vm.Name, Template: vm.IsTemplate,
				DiskLabel: disk.Label, BackingPath: disk.BackingPath,
			})
		}
	}
	return out, nil
}

// NetworkTopology implements the TUI's switch-view extension from the demo
// estate's own switches and host network configuration. VM counts come from
// the sample VMs' adapters, which is what Network.vm reflects on a vCenter.
func (b *Backend) NetworkTopology(_ context.Context, cc *config.Context) (*vsphere.NetworkTopology, error) {
	if cc == nil {
		return nil, errors.New("no context selected")
	}
	if err := b.failures[cc.Name]; err != nil {
		return nil, err
	}
	inv, ok := b.inventories[cc.Name]
	if !ok || inv == nil {
		return nil, fmt.Errorf("demo inventory for %q not found", cc.Name)
	}
	topo := &vsphere.NetworkTopology{
		Context:    cc.Name,
		Switches:   append([]vsphere.DVSwitch(nil), inv.DVSwitches...),
		Hosts:      append([]vsphere.Host(nil), inv.Hosts...),
		NetworkVMs: make(map[string]int, len(inv.Networks)),
	}
	for _, n := range inv.Networks {
		topo.NetworkVMs[n.ID] = 0
	}
	for _, vm := range inv.VMs {
		seen := map[string]bool{}
		for _, nic := range vm.NICs {
			if nic.NetworkID != "" && !seen[nic.NetworkID] {
				seen[nic.NetworkID] = true
				topo.NetworkVMs[nic.NetworkID]++
			}
		}
	}
	return topo, nil
}

// demoUnder reports the part of path inside dir, and whether it is there at
// all.
func demoUnder(path, dir string) (string, bool) {
	if dir == "" {
		return path, true
	}
	if !strings.HasPrefix(path, dir+"/") {
		return "", false
	}
	return strings.TrimPrefix(path, dir+"/"), true
}

func (b *Backend) demoFiles(cc *config.Context, datastore string) ([]vsphere.DatastoreFile, error) {
	if cc == nil {
		return nil, errors.New("no context selected")
	}
	inv, ok := b.inventories[cc.Name]
	if !ok || inv == nil {
		return nil, fmt.Errorf("demo inventory for %q not found", cc.Name)
	}
	for _, ds := range inv.Datastores {
		if strings.EqualFold(ds.Name, datastore) {
			return ds.Files, nil
		}
	}
	return nil, fmt.Errorf("demo datastore %q not found", datastore)
}

// Contexts implements tui.Backend.
func (b *Backend) Contexts() []*config.Context { return b.contexts }

// BeginInventory implements tui.Backend. The demo estate has each context's
// whole Inventory assembled up front, so the "handle" it hands back simply
// slices that fixed Inventory into the group tui.InventoryHandle.FetchGroup
// asks for, rather than actually connecting to anything.
func (b *Backend) BeginInventory(_ context.Context, cc *config.Context) (tui.InventoryHandle, error) {
	if err := b.failures[cc.Name]; err != nil {
		return nil, err
	}
	inv, ok := b.inventories[cc.Name]
	if !ok {
		return nil, fmt.Errorf("demo inventory for %q not found", cc.Name)
	}
	return demoInventoryHandle{inv: inv}, nil
}

// demoInventoryHandle answers each fetch group from the demo estate's
// pre-built Inventory, so recording or presenting the interface never opens
// a real connection.
type demoInventoryHandle struct{ inv *vsphere.Inventory }

// FetchGroup implements tui.InventoryHandle. The virtual-machine group arrives
// in DefaultPageSize pages, the way a real vCenter's initial sync does, so the
// presentation streams a large estate in instead of showing it all at once;
// every other group is one page. Slices are copied so the fixture cannot be
// mutated through a result.
func (h demoInventoryHandle) FetchGroup(group vsphere.FetchGroup, partial func(*vsphere.Inventory)) *vsphere.Inventory {
	full := h.inv.Slice(group)
	out := &vsphere.Inventory{Context: full.Context}
	out.ApplyGroup(group, full)
	if group == vsphere.GroupVMs {
		out.VMs = append([]vsphere.VM(nil), full.VMs...)
		out.Templates = append([]vsphere.VM(nil), full.Templates...)
	}
	if partial == nil {
		return out
	}
	if group != vsphere.GroupVMs {
		partial(h.inv.Slice(group))
		return out
	}
	for start := 0; start < len(full.VMs) || start == 0; start += vsphere.DefaultPageSize {
		page := &vsphere.Inventory{Context: full.Context}
		end := min(start+vsphere.DefaultPageSize, len(full.VMs))
		page.VMs = append([]vsphere.VM(nil), full.VMs[start:end]...)
		if end == len(full.VMs) {
			page.Templates = append([]vsphere.VM(nil), full.Templates...)
		}
		partial(page)
		if end == len(full.VMs) {
			break
		}
	}
	return out
}

// Status implements tui.Backend.
func (b *Backend) Status(name string) (session.Status, bool) {
	d := b.diagnoses[name]
	if d == nil {
		return session.Status{}, false
	}
	return session.Status{Name: name, Latency: d.Latency}, true
}

// Diagnose implements tui.Backend.
func (b *Backend) Diagnose(_ context.Context, cc *config.Context) *vsphere.Diagnosis {
	return b.diagnoses[cc.Name]
}

func demoResources(contextName, vcenterID, kind string, values any) []assessment.ResourceObservation {
	var resources []assessment.ResourceObservation
	switch typed := values.(type) {
	case []vsphere.Host:
		for _, value := range typed {
			resources = append(resources, makeDemoResource(contextName, vcenterID, kind, value.ID, value.Name, value))
		}
	case []vsphere.Cluster:
		for _, value := range typed {
			resources = append(resources, makeDemoResource(contextName, vcenterID, kind, value.ID, value.Name, value))
		}
	case []vsphere.ResourcePool:
		for _, value := range typed {
			resources = append(resources, makeDemoResource(contextName, vcenterID, kind, value.ID, value.Name, value))
		}
	case []vsphere.Datastore:
		for _, value := range typed {
			resources = append(resources, makeDemoResource(contextName, vcenterID, kind, value.ID, value.Name, value))
		}
	case []vsphere.DVSwitch:
		for _, value := range typed {
			resources = append(resources, makeDemoResource(contextName, vcenterID, kind, value.ID, value.Name, value))
		}
	case []vsphere.VApp:
		for _, value := range typed {
			resources = append(resources, makeDemoResource(contextName, vcenterID, kind, value.ID, value.Name, value))
		}
	case []vsphere.Network:
		for _, value := range typed {
			resources = append(resources, makeDemoResource(contextName, vcenterID, kind, value.ID, value.Name, value))
		}
	}
	return resources
}

func makeDemoResource(contextName, vcenterID, kind, id, name string, value any) assessment.ResourceObservation {
	payload, _ := json.Marshal(value)
	return assessment.ResourceObservation{Context: contextName, VCenterID: vcenterID, Kind: kind, ID: id, Name: name, Payload: payload}
}

func int64Value(value int64) *int64 { return &value }
func int32Value(value int32) *int32 { return &value }
func boolValue(value bool) *bool    { return &value }

func seedMigrationVM(vm *vsphere.VM) {
	if vm == nil {
		return
	}
	zero := int64(0)
	unlimited := int64(-1)
	secureBoot, autoCores, locked := false, false, false
	vm.ConfigurationAvailable = true
	vm.Firmware = "efi"
	vm.SecureBootEnabled = &secureBoot
	vm.CoresPerSocket = 1
	vm.CPUSockets = vm.CPU
	vm.AutoCoresPerSocket = &autoCores
	vm.CPUAllocation = &vsphere.VMResourceAllocation{Reservation: &zero, Limit: &unlimited}
	vm.MemoryAllocation = &vsphere.VMResourceAllocation{Reservation: &zero, Limit: &unlimited}
	vm.MemoryReservationLockedToMax = &locked
}

// The remaining methods satisfy the TUI backend contract. The presentation is
// deliberately read-only so a recording cannot imply that sample contexts can
// be changed or saved.
func (b *Backend) TestContext(context.Context, contextops.Input) (*config.Context, *vsphere.Diagnosis) {
	return nil, nil
}

func (b *Backend) SaveContext(context.Context, contextops.Input, bool) (*contextops.Result, error) {
	return nil, errors.New("the presentation is read-only")
}

func (b *Backend) RemoveContext(context.Context, string, bool) (*config.Context, error) {
	return nil, errors.New("the presentation is read-only")
}

func (b *Backend) DiscoverThumbprint(context.Context, *config.Context) (string, string, string, time.Time, error) {
	return "", "", "", time.Time{}, errors.New("the presentation is read-only")
}

func healthyDiagnosis(cc *config.Context, latency time.Duration) *vsphere.Diagnosis {
	return &vsphere.Diagnosis{
		Context: cc.Name, Endpoint: cc.Endpoint, Route: cc.Transport.Describe(), TLS: cc.TLS.Describe(), Latency: latency,
		Checks: []vsphere.Check{
			{Name: "Configuration valid", Status: vsphere.CheckPass, Detail: cc.Endpoint},
			{Name: "Credential available", Status: vsphere.CheckPass, Detail: "keyring:" + cc.Name},
			{Name: "Route configured", Status: vsphere.CheckPass, Detail: cc.Transport.Describe()},
			{Name: "DNS resolution", Status: vsphere.CheckPass, Detail: "10.20.0.15"},
			{Name: "TCP connection", Status: vsphere.CheckPass, Detail: "connected"},
			{Name: "TLS handshake", Status: vsphere.CheckPass, Detail: "certificate verified"},
			{Name: "Authentication", Status: vsphere.CheckPass, Detail: "VMware vCenter Server 8.0.3"},
			{Name: "API access", Status: vsphere.CheckPass, Detail: "inventory readable"},
		},
	}
}

func failedDiagnosis(cc *config.Context) *vsphere.Diagnosis {
	failure := errors.New("dial tcp 10.24.0.8:3128: connection refused")
	return &vsphere.Diagnosis{
		Context: cc.Name, Endpoint: cc.Endpoint, Route: cc.Transport.Describe(), TLS: cc.TLS.Describe(),
		Checks: []vsphere.Check{
			{Name: "Configuration valid", Status: vsphere.CheckPass, Detail: cc.Endpoint},
			{Name: "Credential available", Status: vsphere.CheckPass, Detail: "keyring:" + cc.Name},
			{Name: "Route configured", Status: vsphere.CheckPass, Detail: cc.Transport.Describe()},
			{Name: "Proxy reachable", Status: vsphere.CheckFail, Err: failure},
			{Name: "DNS resolution", Status: vsphere.CheckSkip},
			{Name: "TCP connection", Status: vsphere.CheckSkip},
			{Name: "TLS handshake", Status: vsphere.CheckSkip},
			{Name: "Authentication", Status: vsphere.CheckSkip},
			{Name: "API access", Status: vsphere.CheckSkip},
		},
	}
}

// snapshotTotal counts the snapshots the demo VMs carry, so the demo ledger
// records the snapshot collection the way a live capture does.
func snapshotTotal(observations []assessment.Observation) int {
	n := 0
	for _, o := range observations {
		n += len(o.VM.Snapshots)
	}
	return n
}

func snapshotStatus(observations []assessment.Observation) string {
	if snapshotTotal(observations) == 0 {
		return "empty"
	}
	return "success"
}
