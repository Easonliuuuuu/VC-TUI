// Package tui implements the terminal user interface. It is deliberately the
// last layer built and the thinnest: everything it shows is available from the
// command line first, so the UI can be replaced without taking any behaviour
// with it.
package tui

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/contextops"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
	"github.com/easonliuuuuu/vsfleet/internal/perf"
	"github.com/easonliuuuuu/vsfleet/internal/session"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Backend is everything the interface needs from the rest of the program.
//
// It exists as an interface for one reason: a UI that can only be exercised
// against a real vCenter is a UI nobody refactors. With this seam the whole
// model — tabs, selection, filtering, failure rendering, and the context
// setup form — is driven in tests by a fake that answers instantly.
type Backend interface {
	// Contexts lists the configured vCenters in display order. It reflects
	// the configuration as it stands right now, so it changes after
	// SaveContext or RemoveContext without needing to be re-fetched.
	Contexts() []*config.Context
	// BeginInventory connects if necessary and builds one context's shared
	// path index (see vsphere.Client.NewIndex), returning a handle that
	// every FetchGroup call for this same load reuses it through rather
	// than rebuilding it once per group. This is what lets the model
	// prioritize the visible kind's group and retrieve the rest
	// concurrently — see loadPriorityGroup and loadRemainingGroups.
	BeginInventory(ctx context.Context, cc *config.Context) (InventoryHandle, error)
	// Status reports the connection state of one context. The second result
	// is false when no connection has been attempted yet.
	Status(name string) (session.Status, bool)
	// Diagnose walks the connection stages for one context, for the panel
	// that answers "why is this one red".
	Diagnose(ctx context.Context, cc *config.Context) *vsphere.Diagnosis

	// TestContext walks the connection for a context that has not been saved
	// yet, so the form can show whether it works before committing to it.
	TestContext(ctx context.Context, in contextops.Input) (*config.Context, *vsphere.Diagnosis)
	// SaveContext validates, optionally tests, and writes a context — adding
	// it or, when in.Replace is set, replacing the one of the same name.
	SaveContext(ctx context.Context, in contextops.Input, test bool) (*contextops.Result, error)
	// RemoveContext deletes a context and, when alsoCredential is set, its
	// stored password. It returns the context as it was just before removal.
	RemoveContext(ctx context.Context, name string, alsoCredential bool) (*config.Context, error)
	// DiscoverThumbprint fetches the certificate an endpoint presents,
	// without verifying it against any policy — the trust-on-first-use step
	// behind pinning a certificate that has never been seen before.
	DiscoverThumbprint(ctx context.Context, cc *config.Context) (sha256, sha1, subject string, notAfter time.Time, err error)
}

// InventoryHandle is one context's connected, index-built inventory
// operation, returned by Backend.BeginInventory. Every FetchGroup call made
// through it reuses the same shared vsphere.Index rather than rebuilding it,
// and is safe to call concurrently with itself for different groups on the
// same handle — which is exactly how the model drives it: the priority
// group alone first, then the rest together once it lands.
type InventoryHandle interface {
	// FetchGroup retrieves one group. partial, when non-nil, is called with
	// each page of objects as it arrives, carrying only that page — an
	// estate with thousands of virtual machines takes long enough to read
	// that showing the first hundreds while the rest are still coming is the
	// difference between a usable interface and a blank one. The complete
	// group is still returned, and is what the caller should trust: partial
	// pages are a preview, not an accumulation to be relied on.
	FetchGroup(group vsphere.FetchGroup, partial func(*vsphere.Inventory)) *vsphere.Inventory
}

// datastoreBrowserBackend is the live-query extension the datastore file
// browser needs. It is deliberately not part of Backend: browsing is the only
// thing in the interface that asks a vCenter a question outside an inventory
// load, and putting it on the interface would force every fake and the demo
// estate to answer it. Callers type-assert for it — the same bargain
// inventoryProgressBackend already makes — and a backend that does not
// implement it gets the action offered but disabled, with a reason, rather
// than silently missing.
//
// Both methods are read-only and bounded by the client; neither has any way to
// change anything on a datastore. datastoreID is vsphere.Datastore.ID, the
// managed object reference's value.
type datastoreBrowserBackend interface {
	// ListDatastoreDirectory lists exactly one directory. relative is the
	// path inside the datastore, empty for its root.
	ListDatastoreDirectory(ctx context.Context, cc *config.Context, datastoreID, datastore, relative string) (vsphere.DatastoreListing, error)
	// FindInDatastore searches the whole datastore recursively. It is only
	// ever called because an operator asked for it by name.
	FindInDatastore(ctx context.Context, cc *config.Context, datastoreID, datastore, pattern string) (vsphere.DatastoreListing, error)
}

// datastoreRelationshipBackend is the optional, operator-triggered lookup
// used by a datastore file's detail view. Keeping it separate from the
// directory browser lets lightweight fakes and custom backends remain useful;
// lack of this extension is rendered as unknown relationship evidence.
type datastoreRelationshipBackend interface {
	ListDatastoreVMReferences(ctx context.Context, cc *config.Context, datastoreID string) (vsphere.DatastoreReferenceListing, error)
}

// vmPerfBackend is the live-query extension behind the VM detail pane's
// charts. Like datastoreBrowserBackend it is optional and type-asserted: a
// backend without it shows the pane's properties with a note where the
// charts would be, rather than failing. The query is read-only and reads one
// VM's counters in one request.
type vmPerfBackend interface {
	VMPerfSeries(ctx context.Context, cc *config.Context, vm vsphere.VM, window time.Duration, interval int, now time.Time) (perf.SeriesSet, error)
}

// vmsPerfBackend is the live-query extension behind the vApp workspace's
// charts: the same counters as vmPerfBackend for a group of VMs, read in as
// few requests as vCenter allows. It is optional in the same way.
type vmsPerfBackend interface {
	VMsPerfSeries(ctx context.Context, cc *config.Context, vms []vsphere.VM, window time.Duration, interval int, now time.Time) ([]vsphere.VMSeriesResult, error)
}

// clockBackend lets a backend whose data is pinned to a fixed instant — the
// synthetic demo estate — supply that instant, so ages computed in the
// interface (a snapshot's, say) do not drift with the wall clock.
type clockBackend interface {
	Now() time.Time
}

// sessionBackend is the production Backend, over the same session manager,
// configuration and credential resolver the command line uses.
type sessionBackend struct {
	cfg  *config.Config
	res  *credentials.Resolver
	mgr  *session.Manager
	opts vsphere.ConnectOptions
}

// NewBackend returns a Backend backed by a live session manager and the
// configuration file, so saving or removing a context from the interface is
// the same write the command line makes.
func NewBackend(cfg *config.Config, res *credentials.Resolver, mgr *session.Manager, opts vsphere.ConnectOptions) Backend {
	return &sessionBackend{cfg: cfg, res: res, mgr: mgr, opts: opts}
}

func (b *sessionBackend) Contexts() []*config.Context { return b.cfg.Contexts }

func (b *sessionBackend) BeginInventory(ctx context.Context, cc *config.Context) (InventoryHandle, error) {
	return b.beginInventory(ctx, cc, nil)
}

// BeginInventoryWithProgress is the production-only extension used by the
// TUI. Keeping it outside Backend lets existing test and demo backends remain
// small while allowing the real connection path to report keyring,
// authentication and inventory stages in the pane that owns them.
func (b *sessionBackend) BeginInventoryWithProgress(ctx context.Context, cc *config.Context, report func(vsphere.Stage)) (InventoryHandle, error) {
	return b.beginInventory(ctx, cc, report)
}

func (b *sessionBackend) beginInventory(ctx context.Context, cc *config.Context, report func(vsphere.Stage)) (InventoryHandle, error) {
	// One watchdog covers connecting, building the index and every group
	// fetched through the handle: without a bound, a vCenter that connects
	// quickly but hangs enumerating would run for as long as the interface
	// itself does, since ctx here is the program's whole-run context.
	//
	// It bounds silence rather than duration. A fixed deadline cannot tell a
	// large estate from a stalled one, and on an estate of thousands of
	// virtual machines it reliably chose wrong: every load was cut off
	// mid-retrieval and the tab it was filling never populated. Connecting is
	// still bounded by --timeout, which session.Manager.Connect applies to
	// its own step. sessionInventoryHandle stops the watchdog once every
	// fetch group has landed — see its FetchGroup.
	opCtx, progress, cancel, tracker := b.mgr.StreamOperation(ctx)
	// StreamOperation already installed tracker.Report. Replace that reporter
	// with a fan-out so timeout errors retain their stage, reaching a new
	// stage counts as progress, and the TUI gets the same live information.
	opCtx = vsphere.WithStageReporter(opCtx, func(stage vsphere.Stage) {
		tracker.Report(stage)
		progress()
		if report != nil {
			report(stage)
		}
	})
	// Login rather than Connect: a load is how the operator asks for a
	// context, so it may undo a logout, and it replaces a session vCenter has
	// ended instead of reading NotAuthenticated from it on every reload.
	s, err := b.mgr.Login(opCtx, cc)
	if err != nil {
		cancel()
		return nil, b.mgr.StreamError(opCtx, err, tracker)
	}
	client := s.Client()
	if client == nil {
		cancel()
		return nil, fmt.Errorf("context %q is not connected", cc.Name)
	}
	idx, err := client.NewIndex(opCtx)
	if err != nil {
		cancel()
		return nil, b.mgr.StreamError(opCtx, err, tracker)
	}
	h := &sessionInventoryHandle{client: client, idx: idx, ctx: opCtx, progress: progress, cancel: cancel}
	h.remaining.Store(int32(len(vsphere.AllGroups)))
	return h, nil
}

// sessionInventoryHandle is the production InventoryHandle. It owns the
// per-operation deadline BeginInventory created and cancels it itself once
// every fetch group has been retrieved — exactly len(vsphere.AllGroups)
// FetchGroup calls, always, since the model issues one for every group on
// every load — so the caller never has to track that lifecycle separately.
type sessionInventoryHandle struct {
	client   *vsphere.Client
	idx      *vsphere.Index
	ctx      context.Context
	progress func()
	cancel   context.CancelFunc

	remaining atomic.Int32
}

func (h *sessionInventoryHandle) FetchGroup(group vsphere.FetchGroup, partial func(*vsphere.Inventory)) *vsphere.Inventory {
	inv := h.client.FetchGroupWith(h.ctx, h.idx, group, vsphere.FetchOptions{
		// The interface renders none of what DetailFull adds — no disks, NICs,
		// CD-ROMs, USBs, or snapshot trees — and those properties dominate what
		// a virtual machine costs to retrieve. A capture still records them;
		// see vsphere.Detail.
		Detail: vsphere.DetailSummary,
		OnPartial: func(page *vsphere.Inventory) {
			// A page arriving is the proof of life the watchdog is waiting
			// for, whether or not anybody is showing it.
			h.progress()
			if partial != nil {
				partial(page)
			}
		},
	})
	if h.remaining.Add(-1) == 0 {
		h.cancel()
	}
	return inv
}

// ListDatastoreDirectory implements datastoreBrowserBackend.
func (b *sessionBackend) ListDatastoreDirectory(ctx context.Context, cc *config.Context, datastoreID, datastore, relative string) (vsphere.DatastoreListing, error) {
	return browse(ctx, b, cc, func(client *vsphere.Client, opCtx context.Context) (vsphere.DatastoreListing, error) {
		return client.ListDatastoreDirectory(opCtx, datastoreID, datastore, relative)
	})
}

// FindInDatastore implements datastoreBrowserBackend.
func (b *sessionBackend) FindInDatastore(ctx context.Context, cc *config.Context, datastoreID, datastore, pattern string) (vsphere.DatastoreListing, error) {
	return browse(ctx, b, cc, func(client *vsphere.Client, opCtx context.Context) (vsphere.DatastoreListing, error) {
		return client.FindInDatastore(opCtx, datastoreID, datastore, pattern)
	})
}

func (b *sessionBackend) ListDatastoreVMReferences(ctx context.Context, cc *config.Context, datastoreID string) (vsphere.DatastoreReferenceListing, error) {
	return browseReference(ctx, b, cc, func(client *vsphere.Client, opCtx context.Context) (vsphere.DatastoreReferenceListing, error) {
		return client.ListDatastoreVMReferences(opCtx, datastoreID)
	})
}

// browse runs one datastore browser query on a connected session.
//
// It takes its own operation context rather than reusing an inventory
// handle's: sessionInventoryHandle cancels its context as soon as the last
// fetch group lands, so a browse borrowing it would fail on a context that was
// cancelled minutes ago. The connection itself is shared — Manager.Connect
// reuses the live session — so this costs a query, not a login.
func browse(ctx context.Context, b *sessionBackend, cc *config.Context, query func(*vsphere.Client, context.Context) (vsphere.DatastoreListing, error)) (vsphere.DatastoreListing, error) {
	return liveQuery(ctx, b, cc, query)
}

func browseReference(ctx context.Context, b *sessionBackend, cc *config.Context, query func(*vsphere.Client, context.Context) (vsphere.DatastoreReferenceListing, error)) (vsphere.DatastoreReferenceListing, error) {
	return liveQuery(ctx, b, cc, query)
}

// liveQuery runs one read-only question outside an inventory load on a
// connected session, under its own operation context — see browse.
func liveQuery[T any](ctx context.Context, b *sessionBackend, cc *config.Context, query func(*vsphere.Client, context.Context) (T, error)) (T, error) {
	var zero T
	opCtx, cancel, tracker := b.mgr.Operation(ctx)
	defer cancel()
	for attempt := 0; ; attempt++ {
		s, err := b.mgr.Connect(opCtx, cc)
		if err != nil {
			return zero, b.mgr.TimeoutError(err, tracker)
		}
		client := s.Client()
		if client == nil {
			return zero, fmt.Errorf("context %q is not connected", cc.Name)
		}
		out, err := query(client, opCtx)
		// A session vCenter has ended fails every request, but nothing was
		// wrong with this one: log in again and ask once more.
		if attempt == 0 && vsphere.IsSessionLost(err) {
			b.mgr.Drop(cc.Name, client)
			continue
		}
		return out, err
	}
}

// Logout implements logoutBackend.
func (b *sessionBackend) Logout(ctx context.Context, cc *config.Context) error {
	return b.mgr.Logout(ctx, cc)
}

// VMPerfSeries implements vmPerfBackend.
func (b *sessionBackend) VMPerfSeries(ctx context.Context, cc *config.Context, vm vsphere.VM, window time.Duration, interval int, now time.Time) (perf.SeriesSet, error) {
	return liveQuery(ctx, b, cc, func(client *vsphere.Client, opCtx context.Context) (perf.SeriesSet, error) {
		return client.VMPerfSeries(opCtx, vm, window, interval, now)
	})
}

// NetworkTopology implements networkTopologyBackend.
func (b *sessionBackend) NetworkTopology(ctx context.Context, cc *config.Context) (*vsphere.NetworkTopology, error) {
	return liveQuery(ctx, b, cc, func(client *vsphere.Client, opCtx context.Context) (*vsphere.NetworkTopology, error) {
		return client.NetworkTopology(opCtx)
	})
}

// VMsPerfSeries implements vmsPerfBackend.
func (b *sessionBackend) VMsPerfSeries(ctx context.Context, cc *config.Context, vms []vsphere.VM, window time.Duration, interval int, now time.Time) ([]vsphere.VMSeriesResult, error) {
	return liveQuery(ctx, b, cc, func(client *vsphere.Client, opCtx context.Context) ([]vsphere.VMSeriesResult, error) {
		return client.VMsPerfSeries(opCtx, vms, window, interval, now)
	})
}

func (b *sessionBackend) Status(name string) (session.Status, bool) { return b.mgr.Status(name) }

func (b *sessionBackend) Diagnose(ctx context.Context, cc *config.Context) *vsphere.Diagnosis {
	d, client := vsphere.Diagnose(ctx, cc, b.opts)
	if client != nil {
		// The diagnosis owns its own connection; the session manager's
		// long-lived one is untouched by it.
		_ = client.Close(context.WithoutCancel(ctx))
	}
	return d
}

func (b *sessionBackend) TestContext(ctx context.Context, in contextops.Input) (*config.Context, *vsphere.Diagnosis) {
	return contextops.Test(ctx, in, b.opts)
}

func (b *sessionBackend) SaveContext(ctx context.Context, in contextops.Input, test bool) (*contextops.Result, error) {
	res, err := contextops.Save(ctx, b.cfg, b.res, b.opts, in, test)
	if err != nil {
		return res, err
	}
	// Editing a context leaves a session connected to what its name used to
	// mean. Manager.Connect would notice the configuration changed, but not a
	// password stored under an unchanged credential reference — and either way
	// the old login is worth ending now rather than at exit. A failure to log
	// out politely is not the operator's problem: the context is saved, and
	// the session is gone from the manager regardless.
	if in.Replace {
		_ = b.mgr.Forget(ctx, res.Context.Name)
	}
	return res, nil
}

func (b *sessionBackend) RemoveContext(ctx context.Context, name string, alsoCredential bool) (*config.Context, error) {
	cc, err := contextops.Remove(b.cfg, name)
	if err != nil {
		return nil, err
	}
	// A context that is gone from the configuration must not keep a login open
	// on the vCenter, nor keep reporting a status under a name that no longer
	// exists.
	_ = b.mgr.Forget(ctx, cc.Name)
	if alsoCredential {
		if err := contextops.DeleteCredentials(ctx, b.res, cc); err != nil {
			return cc, fmt.Errorf("context %q removed, but could not delete its stored password(s): %w", cc.Name, err)
		}
	}
	return cc, nil
}

func (b *sessionBackend) DiscoverThumbprint(ctx context.Context, cc *config.Context) (sha256, sha1, subject string, notAfter time.Time, err error) {
	return vsphere.FetchThumbprint(ctx, cc, b.opts)
}
