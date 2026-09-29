package vsphere

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// entityKinds are the managed object types walked to build the inventory path
// index. Everything a domain object can live inside has to appear here.
var entityKinds = []string{
	"Folder",
	"Datacenter",
	"ComputeResource",
	"ClusterComputeResource",
	"ResourcePool",
	"VirtualApp",
	"HostSystem",
	"VirtualMachine",
	"Datastore",
	"StoragePod",
	"Network",
	"DistributedVirtualPortgroup",
	"DistributedVirtualSwitch",
}

type entity struct {
	ref    types.ManagedObjectReference
	name   string
	parent *types.ManagedObjectReference
}

// index maps managed object references to their name and parent so inventory
// paths, datacenters and clusters can be resolved locally instead of with one
// round trip per object.
type index struct {
	byRef map[types.ManagedObjectReference]entity
	// root is the container-view root every retrieval for this client scopes
	// to — see resolveRoot. Every per-kind lister reuses it rather than each
	// resolving the configured datacenter over again.
	root types.ManagedObjectReference
	// portgroups maps a distributed port group's key — the value a VM NIC's
	// distributed-port backing reports as PortgroupKey — to its name. It is
	// filled from a dedicated port group retrieval so a NIC's network name
	// does not depend on the generic entity walk having reached, or typed,
	// the port group the way the backing's key expects.
	portgroups map[string]string
}

func newIndex(ctx context.Context, c *Client) (*index, error) {
	root, seed, err := resolveRoot(ctx, c)
	if err != nil {
		return nil, err
	}
	var ents []mo.ManagedEntity
	if err := retrieve(ctx, c, root, entityKinds, []string{"ManagedEntity"}, []string{"name", "parent"}, &ents); err != nil {
		return nil, fmt.Errorf("build inventory index: %w", err)
	}
	idx := &index{byRef: make(map[types.ManagedObjectReference]entity, len(ents)+len(seed)), root: root}
	for _, e := range seed {
		idx.byRef[e.ref] = e
	}
	for _, e := range ents {
		idx.byRef[e.Self] = entity{ref: e.Self, name: e.Name, parent: e.Parent}
	}
	idx.portgroups = make(map[string]string)
	// Best effort: the generic walk above already names most objects, and a
	// failure here only costs the port group fallback.
	var pgs []mo.DistributedVirtualPortgroup
	if err := retrieve(ctx, c, root, []string{"DistributedVirtualPortgroup"}, []string{"DistributedVirtualPortgroup"}, []string{"name", "key"}, &pgs); err == nil {
		for i := range pgs {
			pg := &pgs[i]
			if pg.Name == "" {
				continue
			}
			idx.portgroups[pg.Self.Value] = pg.Name
			if pg.Key != "" {
				idx.portgroups[pg.Key] = pg.Name
			}
		}
	}
	return idx, nil
}

// portgroupName resolves a distributed port group's name from the key a NIC
// backing reports. The entity walk is tried first, then the dedicated port
// group map, which is keyed by both the key and the managed object ID.
func (i *index) portgroupName(key string) string {
	if key == "" {
		return ""
	}
	if n := i.name(&types.ManagedObjectReference{Type: "DistributedVirtualPortgroup", Value: key}); n != "" {
		return n
	}
	return i.portgroups[key]
}

// resolveRoot returns the container-view root for a client's context: the
// vCenter-wide root folder when no datacenter is configured, or the named
// datacenter's own reference when Context.Datacenter is set. Scoping every
// retrieval directly to that reference — rather than fetching vCenter-wide
// and filtering locally — is what keeps an object outside the configured
// datacenter from ever being retrieved in the first place.
//
// A container view rooted at an object does not include the object itself,
// only its descendants, so seed carries the datacenter's own name and
// reference for the index to record directly; without it, every object's
// resolved path and datacenter would come up empty, since the ancestor walk
// (index.ancestors) has nowhere further to go once it reaches a reference
// the index never recorded.
func resolveRoot(ctx context.Context, c *Client) (root types.ManagedObjectReference, seed []entity, err error) {
	name := strings.TrimSpace(c.Context.Datacenter)
	if name == "" {
		return c.VIM().ServiceContent.RootFolder, nil, nil
	}
	var dcs []mo.Datacenter
	if err := retrieve(ctx, c, c.VIM().ServiceContent.RootFolder, []string{"Datacenter"}, []string{"Datacenter"}, []string{"name"}, &dcs); err != nil {
		return types.ManagedObjectReference{}, nil, fmt.Errorf("resolve datacenter %q: %w", name, err)
	}
	var match *mo.Datacenter
	count := 0
	for i := range dcs {
		if dcs[i].Name == name {
			match = &dcs[i]
			count++
		}
	}
	switch count {
	case 0:
		return types.ManagedObjectReference{}, nil, fmt.Errorf("datacenter %q not found", name)
	case 1:
		return match.Self, []entity{{ref: match.Self, name: match.Name}}, nil
	default:
		return types.ManagedObjectReference{}, nil, fmt.Errorf("datacenter %q is ambiguous: %d datacenters share that name", name, count)
	}
}

// ancestors returns the chain from the object up to, but not including, the
// root folder. The object itself is first.
func (i *index) ancestors(ref types.ManagedObjectReference) []entity {
	var chain []entity
	seen := make(map[types.ManagedObjectReference]bool)
	cur := &ref
	for cur != nil && !seen[*cur] {
		seen[*cur] = true
		e, ok := i.byRef[*cur]
		if !ok {
			break
		}
		chain = append(chain, e)
		cur = e.parent
	}
	return chain
}

// path renders the full inventory path, e.g. /Taipei/vm/Templates/ubuntu-24.
// leaf overrides the name of the object itself, which matters for the handful
// of types that report a display name only through their own summary.
func (i *index) path(ref types.ManagedObjectReference, leaf string) string {
	chain := i.ancestors(ref)
	if len(chain) == 0 {
		if leaf == "" {
			return ""
		}
		return "/" + leaf
	}
	if leaf != "" {
		chain[0].name = leaf
	}
	parts := make([]string, 0, len(chain))
	for n := len(chain) - 1; n >= 0; n-- {
		parts = append(parts, chain[n].name)
	}
	return "/" + strings.Join(parts, "/")
}

// datacenter returns the name of the datacenter an object belongs to.
func (i *index) datacenter(ref types.ManagedObjectReference) string {
	for _, e := range i.ancestors(ref) {
		if e.ref.Type == "Datacenter" {
			return e.name
		}
	}
	return ""
}

// name returns the display name of a reference, or "" when unknown.
func (i *index) name(ref *types.ManagedObjectReference) string {
	if ref == nil {
		return ""
	}
	return i.byRef[*ref].name
}

// names resolves a list of references, dropping any that are unknown.
func (i *index) names(refs []types.ManagedObjectReference) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if n := i.name(&r); n != "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// clusterOf returns the cluster a host belongs to, empty for a standalone host.
func (i *index) clusterOf(ref *types.ManagedObjectReference) string {
	if ref == nil {
		return ""
	}
	e, ok := i.byRef[*ref]
	if !ok {
		return ""
	}
	if e.parent != nil && e.parent.Type == "ClusterComputeResource" {
		return i.name(e.parent)
	}
	return ""
}

// computeResourceOf returns the first compute resource in an object's
// ancestor chain. vApps normally live below a cluster's root resource pool;
// walking the index keeps placement resolution local and also handles a
// standalone ComputeResource when a server exposes one.
func (i *index) computeResourceOf(ref *types.ManagedObjectReference) string {
	if ref == nil {
		return ""
	}
	for _, e := range i.ancestors(*ref) {
		if e.ref.Type == "ClusterComputeResource" || e.ref.Type == "ComputeResource" {
			return e.name
		}
	}
	return ""
}

// clusterFor returns only a ClusterComputeResource placement. A vApp can be
// hosted by a standalone ComputeResource, which belongs in ComputeResource
// but should not be mislabeled as a cluster.
func (i *index) clusterFor(ref *types.ManagedObjectReference) string {
	if ref == nil {
		return ""
	}
	for _, e := range i.ancestors(*ref) {
		if e.ref.Type == "ClusterComputeResource" {
			return e.name
		}
	}
	return ""
}

// folderPath returns a VM's folder relative to its datacenter, so that
// /Taipei/vm/Templates/Linux/ubuntu reads as /Templates/Linux.
func (i *index) folderPath(parent *types.ManagedObjectReference, datacenter string) string {
	if parent == nil {
		return "/"
	}
	full := i.path(*parent, "")
	if full == "" {
		return "/"
	}
	for _, root := range []string{"/" + datacenter + "/vm", "/" + datacenter + "/host", "/" + datacenter + "/datastore", "/" + datacenter + "/network"} {
		if full == root {
			return "/"
		}
		if strings.HasPrefix(full, root+"/") {
			return strings.TrimPrefix(full, root)
		}
	}
	return full
}

func (i *index) locate(c *Client, ref types.ManagedObjectReference, name string) Location {
	return Location{
		Context:    c.Context.Name,
		Datacenter: i.datacenter(ref),
		Path:       i.path(ref, name),
	}
}

// DefaultPageSize is how many objects one retrieval page carries. vCenter
// will happily answer for an entire estate in a single response, which on a
// large one means tens of thousands of objects arriving as one very large
// document that nothing can show until its last byte lands. Paging bounds
// both what is in flight at once and — through FetchOptions.OnPartial — how
// long the first rows take to appear.
const DefaultPageSize = 500

// retrieve enumerates every object of the given view kinds under root and
// loads the named properties into dst. propKinds are the types the property
// specification is written against, which is usually the common base type of
// the view kinds. root scopes the container view — see resolveRoot — so that
// an object outside it is never retrieved to begin with, rather than fetched
// and then filtered out locally.
//
// It is one call returning one complete answer, which is what the command
// line and the assessment collector want. A caller that would rather see
// each page as it arrives uses retrievePages.
func retrieve(ctx context.Context, c *Client, root types.ManagedObjectReference, viewKinds, propKinds, props []string, dst any) error {
	return withContainerView(ctx, c, root, viewKinds, func(v *view.ContainerView) error {
		if err := v.Retrieve(ctx, propKinds, props, dst); err != nil {
			return fmt.Errorf("retrieve %s properties: %w", strings.Join(propKinds, ","), err)
		}
		return nil
	})
}

// retrievePages is retrieve, delivered a page at a time: it calls onPage with
// each page of raw property content as the server produces it, so a caller
// can decode and show a page's worth of objects while the rest is still
// coming. pageSize bounds how many objects a page carries.
//
// It reads through the property collector's update mechanism rather than
// RetrieveProperties, for two reasons. It is the only paging interface
// govmomi exposes without reaching into vim25/methods, which this program
// deliberately cannot import — see TestNoMutationCapablePackageIsImported.
// And it needs a collector of its own regardless: the shared default
// collector permits one caller at a time, and the interface fetches several
// resource kinds for one vCenter concurrently.
//
// Only the initial synchronization is read. The filter reports every object
// in the view as it stands, in pages, and the walk stops as soon as the
// server says the set is no longer truncated — this is a paged read, not a
// subscription to what happens next.
func retrievePages(ctx context.Context, c *Client, root types.ManagedObjectReference, viewKinds, propKinds, props []string, pageSize int32, onPage func([]types.ObjectContent) error) error {
	return withContainerView(ctx, c, root, viewKinds, func(v *view.ContainerView) error {
		collector, err := property.DefaultCollector(c.VIM()).Create(ctx)
		if err != nil {
			return fmt.Errorf("create property collector: %w", err)
		}
		defer func() {
			// Destroying the collector releases its filter with it, on a
			// background context so a cancelled read still cleans up after
			// itself rather than leaving state on the server.
			_ = collector.Destroy(context.WithoutCancel(ctx))
		}()

		ref := v.Reference()
		spec := types.PropertyFilterSpec{
			ObjectSet: []types.ObjectSpec{{
				Obj:       ref,
				Skip:      types.NewBool(true),
				SelectSet: []types.BaseSelectionSpec{&types.TraversalSpec{Type: ref.Type, Path: "view"}},
			}},
		}
		for _, kind := range propKinds {
			ps := types.PropertySpec{Type: kind}
			if len(props) == 0 {
				ps.All = types.NewBool(true)
			} else {
				ps.PathSet = props
			}
			spec.PropSet = append(spec.PropSet, ps)
		}
		if _, err := collector.CreateFilter(ctx, types.CreateFilter{Spec: spec}); err != nil {
			return fmt.Errorf("retrieve %s properties: %w", strings.Join(propKinds, ","), err)
		}

		opts := &property.WaitOptions{Options: &types.WaitOptions{MaxObjectUpdates: pageSize}}
		var pageErr error
		err = collector.WaitForUpdatesEx(ctx, opts, func(updates []types.ObjectUpdate) bool {
			if len(updates) > 0 {
				if pageErr = onPage(objectContents(updates)); pageErr != nil {
					return true
				}
			}
			// Stop as soon as the server has nothing further to hand over.
			// Asking again at that point would be waiting for the estate to
			// change, which is a different question than the one asked here.
			return !opts.Truncated
		})
		if pageErr != nil {
			return pageErr
		}
		if err != nil {
			return fmt.Errorf("retrieve %s properties: %w", strings.Join(propKinds, ","), err)
		}
		return nil
	})
}

// withContainerView creates the container view both retrieval paths walk and
// destroys it afterwards, whatever fn did.
func withContainerView(ctx context.Context, c *Client, root types.ManagedObjectReference, viewKinds []string, fn func(*view.ContainerView) error) error {
	m := view.NewManager(c.VIM())
	v, err := m.CreateContainerView(ctx, root, viewKinds, true)
	if err != nil {
		return fmt.Errorf("create container view: %w", err)
	}
	defer func() {
		// Destroying the view uses a background context so that a cancelled
		// request still releases the server-side object.
		_ = v.Destroy(context.WithoutCancel(ctx))
	}()
	return fn(v)
}

// objectContents reshapes one page of property-collector updates into the
// ObjectContent that mo.LoadObjectContent decodes, so both retrieval paths
// feed the same decoder and produce identical objects.
func objectContents(updates []types.ObjectUpdate) []types.ObjectContent {
	out := make([]types.ObjectContent, 0, len(updates))
	for _, u := range updates {
		content := types.ObjectContent{Obj: u.Obj, MissingSet: u.MissingSet}
		for _, change := range u.ChangeSet {
			if change.Val == nil {
				continue
			}
			content.PropSet = append(content.PropSet, types.DynamicProperty{Name: change.Name, Val: change.Val})
		}
		out = append(out, content)
	}
	return out
}

// ListInventory enumerates everything in one vCenter, one fetch group after
// another over one shared Index. The individual List functions exist for
// callers that need only one kind; a caller that wants groups prioritized
// and retrieved concurrently instead — the terminal interface — calls
// NewIndex and FetchGroup directly rather than through here. This is what
// the CLI's own listing commands and the cross-context search use, and is
// built on exactly the same primitives they are.
//
// It only fails outright when the path index itself cannot be built — every
// object's inventory path depends on it, so nothing else is usable either.
// A kind that fails to list on its own (a limited-permission account missing
// one privilege, say) is recorded in Inventory.Errors and does not stop the
// rest from being enumerated.
func (c *Client) ListInventory(ctx context.Context) (*Inventory, error) {
	idx, err := c.NewIndex(ctx)
	if err != nil {
		return nil, err
	}
	inv := &Inventory{Context: c.Context.Name}
	for _, group := range AllGroups {
		inv.ApplyGroup(group, c.FetchGroup(ctx, idx, group))
	}
	return inv, nil
}
