package vsphere

import (
	"context"
	"time"
)

// FetchGroup identifies one concurrent unit of inventory retrieval. VMs and
// templates share a single vSphere call and so are always fetched together
// as one group; every other kind is its own.
type FetchGroup string

// Fetch groups, in the order a sequential caller retrieves them.
const (
	GroupVMs      FetchGroup = "vms"
	GroupHosts    FetchGroup = "hosts"
	GroupClusters FetchGroup = "clusters"
	// GroupResourcePools is capture-only and deliberately absent from
	// AllGroups because the browsable inventory never requests it.
	GroupResourcePools FetchGroup = "resourcepools"
	// GroupDVSwitches is capture-only and deliberately absent from AllGroups
	// because the browsable inventory never requests it.
	GroupDVSwitches FetchGroup = "dvswitches"
	GroupVApps      FetchGroup = "vapps"
	GroupDatastores FetchGroup = "datastores"
	GroupNetworks   FetchGroup = "networks"
)

// AllGroups lists every fetch group ListInventory enumerates.
var AllGroups = []FetchGroup{GroupVMs, GroupHosts, GroupClusters, GroupVApps, GroupDatastores, GroupNetworks}

// Detail selects how much of each object a fetch retrieves. It exists
// because the two callers want genuinely different things: a listing shows a
// dozen scalar fields per VM, while a capture records everything it will
// later be asked to diff or export. Retrieving the second to satisfy the
// first is what made a large estate impossible to browse — see vmDetailProps.
type Detail int

const (
	// DetailSummary retrieves what a listing, a detail pane and a search
	// result read. VM.Disks, VM.NICs, VM.CDROMs, VM.USBs and VM.Snapshots come
	// back empty.
	DetailSummary Detail = iota
	// DetailFull additionally retrieves per-VM virtual devices, guest NIC
	// bindings and snapshot trees. This is what a capture records and what
	// the RVTools export reads back.
	DetailFull
)

// FetchOptions tune one call to FetchGroupWith.
type FetchOptions struct {
	// Detail selects the property set — see Detail. The zero value is
	// DetailSummary, so a caller that has not thought about it gets the
	// cheap retrieval rather than the expensive one.
	Detail Detail
	// PageSize bounds how many objects one retrieval page carries. Zero
	// means DefaultPageSize; a negative value asks for no paging at all,
	// which leaves the page size to the server.
	PageSize int
	// BrowseDatastoreFiles opts the datastore group into a bounded, read-only
	// HostDatastoreBrowser query. It is ignored for every other group.
	BrowseDatastoreFiles bool
	// FileInventory opts the datastore group into the all-file inventory
	// behind vFileInfo. nil (the default) never lists datastore files. It is
	// independent of BrowseDatastoreFiles and ignored for every other group.
	FileInventory *FileInventoryOptions
	// HostConfig opts the host group into the expensive config.storageDevice
	// and config.network properties. It is ignored for every other group.
	HostConfig bool
	// OnPartial, when set, is called with each page of results as it
	// arrives, carrying only that page's objects. It is called from the
	// goroutine driving the fetch, before FetchGroupWith returns, and the
	// complete group is still returned at the end — a caller showing pages
	// early replaces them with that authoritative result rather than
	// trusting its own accumulation.
	//
	// Only GroupVMs pages, and only when PageSize asks for it; every other
	// group is small enough that one page is the whole answer, and they call
	// OnPartial exactly once. An unpaged fetch calls it not at all for VMs,
	// since there are no pages to preview.
	OnPartial func(*Inventory)
}

func (o FetchOptions) pageSize() int32 {
	switch {
	case o.PageSize < 0:
		return 0
	case o.PageSize == 0:
		return DefaultPageSize
	default:
		return int32(o.PageSize)
	}
}

func (o FetchOptions) partial(inv *Inventory) {
	if o.OnPartial != nil {
		o.OnPartial(inv)
	}
}

// GroupFor returns the fetch group that populates kind. VMs and templates
// map onto the same group, since starting on either tab prioritizes the one
// vSphere call that answers both.
func GroupFor(k Kind) FetchGroup {
	switch k {
	case KindVM, KindTemplate:
		return GroupVMs
	case KindHost:
		return GroupHosts
	case KindCluster:
		return GroupClusters
	case KindResourcePool:
		return GroupResourcePools
	case KindDVSwitch:
		return GroupDVSwitches
	case KindVApp:
		return GroupVApps
	case KindDatastore:
		return GroupDatastores
	case KindNetwork:
		return GroupNetworks
	default:
		return ""
	}
}

// Index is the shared inventory path index built once per operation — see
// Client.NewIndex — and reused by every FetchGroup call that follows, so a
// caller retrieving several groups for one context pays for the index build
// once rather than once per group.
type Index struct{ idx *index }

// NewIndex returns the shared path index for c's context, scoped to its
// configured datacenter (see resolveRoot). ListInventory builds one and
// walks every group sequentially; a caller wanting groups individually —
// prioritized, or fetched concurrently — builds one and calls FetchGroup
// against it directly instead, reusing the same Index for each.
//
// An index built less than IndexTTL ago is returned as it stands rather than
// walked again. The index is immutable once built, so sharing one between
// concurrent fetches is safe.
func (c *Client) NewIndex(ctx context.Context) (*Index, error) {
	c.idxMu.Lock()
	defer c.idxMu.Unlock()
	if c.idx != nil && time.Since(c.idxAt) < IndexTTL {
		return c.idx, nil
	}
	reportStage(ctx, StageLoadingIndex)
	idx, err := newIndex(ctx, c)
	if err != nil {
		return nil, err
	}
	c.idx, c.idxAt = &Index{idx: idx}, time.Now()
	return c.idx, nil
}

// FetchGroup retrieves one group's objects using idx, filling in only the
// Inventory fields that group owns. A failure to list is recorded in the
// returned Inventory.Errors rather than as a Go error — the same convention
// ListInventory uses for a kind a limited-permission account cannot see —
// so a caller applying several groups' results (see Inventory.ApplyGroup)
// never has to special-case a group that failed outright versus one that
// simply had nothing to report.
func (c *Client) FetchGroup(ctx context.Context, idx *Index, group FetchGroup) *Inventory {
	return c.FetchGroupWith(ctx, idx, group, FetchOptions{Detail: DetailFull, PageSize: -1})
}

// FetchGroupWith is FetchGroup with the detail level and paging under the
// caller's control. The terminal interface uses it to retrieve a browsable
// summary in pages it can show as they land; the assessment collector uses
// FetchGroup, which is this with everything turned up.
func (c *Client) FetchGroupWith(ctx context.Context, idx *Index, group FetchGroup, opts FetchOptions) *Inventory {
	inv := &Inventory{Context: c.Context.Name}
	fail := func(err error, kinds ...Kind) {
		for _, k := range kinds {
			inv.Errors = append(inv.Errors, InventoryError{Kind: k, Message: err.Error()})
		}
	}
	split := func(dst *Inventory, vms []VM) {
		for _, vm := range vms {
			if vm.IsTemplate {
				dst.Templates = append(dst.Templates, vm)
			} else {
				dst.VMs = append(dst.VMs, vm)
			}
		}
	}
	switch group {
	case GroupVMs:
		reportStage(ctx, StageLoadingVMs)
		page := func(vms []VM) {
			part := &Inventory{Context: c.Context.Name}
			split(part, vms)
			opts.partial(part)
		}
		if vms, err := c.listVMsWith(ctx, idx.idx, opts, page); err != nil {
			fail(err, KindVM, KindTemplate)
		} else {
			split(inv, vms)
		}
	case GroupHosts:
		reportStage(ctx, StageLoadingHosts)
		if hosts, err := c.listHostsWith(ctx, idx.idx, opts.HostConfig); err != nil {
			fail(err, KindHost)
		} else {
			inv.Hosts = hosts
			opts.partial(inv.Slice(group))
		}
	case GroupClusters:
		reportStage(ctx, StageLoadingClusters)
		if clusters, err := c.listClusters(ctx, idx.idx); err != nil {
			fail(err, KindCluster)
		} else {
			inv.Clusters = clusters
			opts.partial(inv.Slice(group))
		}
	case GroupResourcePools:
		if resourcePools, err := c.listResourcePools(ctx, idx.idx); err != nil {
			fail(err, KindResourcePool)
		} else {
			inv.ResourcePools = resourcePools
			opts.partial(inv.Slice(group))
		}
	case GroupDVSwitches:
		if switches, err := c.listDVSwitches(ctx, idx.idx); err != nil {
			fail(err, KindDVSwitch)
		} else {
			inv.DVSwitches = switches
			opts.partial(inv.Slice(group))
		}
	case GroupVApps:
		reportStage(ctx, StageLoadingVApps)
		if vapps, err := c.listVApps(ctx, idx.idx); err != nil {
			fail(err, KindVApp)
		} else {
			inv.VApps = vapps
			opts.partial(inv.Slice(group))
		}
	case GroupDatastores:
		reportStage(ctx, StageLoadingDatastores)
		if datastores, err := c.listDatastoresWith(ctx, idx.idx, opts.BrowseDatastoreFiles, opts.FileInventory); err != nil {
			fail(err, KindDatastore)
		} else {
			inv.Datastores = datastores
			opts.partial(inv.Slice(group))
		}
	case GroupNetworks:
		reportStage(ctx, StageLoadingNetworks)
		if networks, err := c.listNetworks(ctx, idx.idx); err != nil {
			fail(err, KindNetwork)
		} else {
			inv.Networks = networks
			opts.partial(inv.Slice(group))
		}
	}
	return inv
}
