package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/vmware/govmomi/simulator"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/contextops"
	"github.com/easonliuuuuu/vsfleet/internal/session"
	"github.com/easonliuuuuu/vsfleet/internal/tui"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// fetchWholeInventory drives Backend.BeginInventory and every fetch group
// through to completion, assembling the whole bundle the way vsphere.Client
// itself does for a plain ListInventory. The interface itself never does
// this — it prioritizes one group and fetches the rest concurrently — but
// these tests want one full read to compare against a control connection,
// not to exercise that scheduling.
func fetchWholeInventory(ctx context.Context, backend tui.Backend, cc *config.Context) (*vsphere.Inventory, error) {
	handle, err := backend.BeginInventory(ctx, cc)
	if err != nil {
		return nil, err
	}
	inv := &vsphere.Inventory{Context: cc.Name}
	for _, g := range vsphere.AllGroups {
		inv.ApplyGroup(g, handle.FetchGroup(g, nil))
	}
	return inv, nil
}

// TestEditedContextTalksToTheNewVCenter pins the rule that a context's name is
// not its identity: editing a connected context to point somewhere else must
// reach the new vCenter, not keep answering from the connection the old
// endpoint left behind.
func TestEditedContextTalksToTheNewVCenter(t *testing.T) {
	a := startVCenter(t, func(m *simulator.Model) { m.Datacenter = 1; m.Machine = 2 })
	b := startVCenter(t, func(m *simulator.Model) { m.Datacenter = 1; m.Machine = 7 })

	cfg := newCfg(t)
	resolver := staticResolver()
	mgr := session.New(resolver)
	backend := tui.NewBackend(cfg, resolver, mgr, vsphere.ConnectOptions{Resolver: resolver})
	ctx := context.Background()

	in := contextops.Input{
		Name: "prod", Endpoint: a.URL, Username: "operator@vsphere.local",
		TLS:          config.TLSConfig{Mode: config.TLSThumbprint, Thumbprint: a.Thumbprint},
		Password:     testPassword,
		HavePassword: true,
		SetCurrent:   true,
	}
	if _, err := backend.SaveContext(ctx, in, true); err != nil {
		t.Fatalf("save the initial context: %v", err)
	}

	cc, err := cfg.Context("prod")
	if err != nil {
		t.Fatalf("context prod: %v", err)
	}
	first, err := fetchWholeInventory(ctx, backend, cc)
	if err != nil {
		t.Fatalf("inventory of the first vCenter: %v", err)
	}
	wantA := len(first.VMs) + len(first.Templates)

	// Edit the same context to point at an entirely different vCenter.
	in.Endpoint = b.URL
	in.TLS = config.TLSConfig{Mode: config.TLSThumbprint, Thumbprint: b.Thumbprint}
	in.Replace = true
	if _, err := backend.SaveContext(ctx, in, true); err != nil {
		t.Fatalf("save the edited context: %v", err)
	}

	cc, err = cfg.Context("prod")
	if err != nil {
		t.Fatalf("context prod after the edit: %v", err)
	}
	second, err := fetchWholeInventory(ctx, backend, cc)
	if err != nil {
		t.Fatalf("inventory after the edit: %v", err)
	}
	gotB := len(second.VMs) + len(second.Templates)

	// What the second vCenter actually holds, read over a connection that has
	// never been anywhere else.
	control := &config.Context{
		Name: "control", Endpoint: b.URL, Username: "operator@vsphere.local",
		Credential: cc.Credential,
		TLS:        config.TLSConfig{Mode: config.TLSThumbprint, Thumbprint: b.Thumbprint},
	}
	control.Normalize()
	controlSession, err := session.New(resolver).Connect(ctx, control)
	if err != nil {
		t.Fatalf("control connection to the second vCenter: %v", err)
	}
	controlInv, err := controlSession.Client().ListInventory(ctx)
	if err != nil {
		t.Fatalf("control inventory: %v", err)
	}
	wantB := len(controlInv.VMs) + len(controlInv.Templates)
	if wantA == wantB {
		t.Fatalf("the two simulated vCenters are indistinguishable (%d objects each): the test proves nothing", wantA)
	}
	if gotB != wantB {
		t.Errorf("after editing the endpoint the inventory has %d objects, want the new vCenter's %d (the old one had %d): the session was reused", gotB, wantB, wantA)
	}

	st, ok := backend.Status("prod")
	if !ok {
		t.Fatal("no session status for prod")
	}
	if st.Endpoint != cc.Endpoint {
		t.Errorf("session status endpoint = %q, want the edited %q", st.Endpoint, cc.Endpoint)
	}
}

// TestRemovedContextClosesItsSession pins the other half: a context that is
// gone from the configuration must not leave a live, logged-in client behind.
func TestRemovedContextClosesItsSession(t *testing.T) {
	vc := startVCenter(t, func(m *simulator.Model) { m.Datacenter = 1; m.Machine = 2 })

	cfg := newCfg(t)
	resolver := staticResolver()
	mgr := session.New(resolver)
	backend := tui.NewBackend(cfg, resolver, mgr, vsphere.ConnectOptions{Resolver: resolver})
	ctx := context.Background()

	in := contextops.Input{
		Name: "prod", Endpoint: vc.URL, Username: "operator@vsphere.local",
		TLS:          config.TLSConfig{Mode: config.TLSThumbprint, Thumbprint: vc.Thumbprint},
		Password:     testPassword,
		HavePassword: true,
		SetCurrent:   true,
	}
	if _, err := backend.SaveContext(ctx, in, true); err != nil {
		t.Fatalf("save: %v", err)
	}
	cc, err := cfg.Context("prod")
	if err != nil {
		t.Fatalf("context prod: %v", err)
	}
	if _, err := fetchWholeInventory(ctx, backend, cc); err != nil {
		t.Fatalf("inventory: %v", err)
	}

	if _, err := backend.RemoveContext(ctx, "prod", false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if st, ok := backend.Status("prod"); ok && st.State == session.Connected {
		t.Errorf("removed context still has a connected session: %+v", st)
	}
}

// connectedProd saves a "prod" context against vc and reads it once, so the
// manager holds a live session for it.
func connectedProd(t *testing.T, vc *vcenter) (*session.Manager, tui.Backend, *config.Context) {
	t.Helper()
	cfg := newCfg(t)
	resolver := staticResolver()
	mgr := session.New(resolver)
	backend := tui.NewBackend(cfg, resolver, mgr, vsphere.ConnectOptions{Resolver: resolver})
	ctx := context.Background()
	in := contextops.Input{
		Name: "prod", Endpoint: vc.URL, Username: "operator@vsphere.local",
		TLS:          config.TLSConfig{Mode: config.TLSThumbprint, Thumbprint: vc.Thumbprint},
		Password:     testPassword,
		HavePassword: true,
		SetCurrent:   true,
	}
	if _, err := backend.SaveContext(ctx, in, true); err != nil {
		t.Fatalf("save: %v", err)
	}
	cc, err := cfg.Context("prod")
	if err != nil {
		t.Fatalf("context prod: %v", err)
	}
	if _, err := fetchWholeInventory(ctx, backend, cc); err != nil {
		t.Fatalf("inventory: %v", err)
	}
	return mgr, backend, cc
}

// endSessionOnServer ends prod's vCenter session behind the manager's back,
// the way an idle timeout, a vCenter restart or an administrator would.
func endSessionOnServer(t *testing.T, mgr *session.Manager, cc *config.Context) {
	t.Helper()
	s, err := mgr.Connect(context.Background(), cc)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Close logs out on the server but leaves the manager's record alone, so
	// the manager still believes the session is live.
	if err := s.Client().Close(context.Background()); err != nil {
		t.Fatalf("end the session on the server: %v", err)
	}
}

// TestReloadLogsInAgainAfterTheServerEndsTheSession pins the recovery from a
// session vCenter ended on its own: the manager still believes it is
// connected, and before the fix every reload read NotAuthenticated from it
// for as long as the process ran.
func TestReloadLogsInAgainAfterTheServerEndsTheSession(t *testing.T) {
	vc := startVCenter(t, func(m *simulator.Model) { m.Datacenter = 1; m.Machine = 2 })
	mgr, backend, cc := connectedProd(t, vc)
	endSessionOnServer(t, mgr, cc)

	inv, err := fetchWholeInventory(context.Background(), backend, cc)
	if err != nil {
		t.Fatalf("reload after the session ended: %v", err)
	}
	if len(inv.Errors) > 0 || len(inv.VMs) == 0 {
		t.Fatalf("reload should log in again and read the inventory, got %d VMs and errors %v", len(inv.VMs), inv.Errors)
	}
}

// TestLiveQueryLogsInAgainAfterTheServerEndsTheSession is the same recovery
// for reads outside an inventory load, such as an open VM's charts.
func TestLiveQueryLogsInAgainAfterTheServerEndsTheSession(t *testing.T) {
	vc := startVCenter(t, func(m *simulator.Model) { m.Datacenter = 1; m.Machine = 2 })
	mgr, backend, cc := connectedProd(t, vc)
	endSessionOnServer(t, mgr, cc)

	topo, ok := backend.(interface {
		NetworkTopology(context.Context, *config.Context) (*vsphere.NetworkTopology, error)
	})
	if !ok {
		t.Fatal("the session backend should answer network topology")
	}
	if _, err := topo.NetworkTopology(context.Background(), cc); err != nil {
		t.Fatalf("a live read after the session ended should log in again: %v", err)
	}
}

// TestLogoutHoldsUntilAnExplicitLogin pins the contexts screen's "log out":
// the session ends, nothing implicit logs back in, and an explicit load does.
func TestLogoutHoldsUntilAnExplicitLogin(t *testing.T) {
	vc := startVCenter(t, func(m *simulator.Model) { m.Datacenter = 1; m.Machine = 2 })
	mgr, backend, cc := connectedProd(t, vc)
	ctx := context.Background()

	if err := mgr.Logout(ctx, cc); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if st, _ := backend.Status("prod"); st.State == session.Connected {
		t.Fatalf("after logout the session is still %v", st.State)
	}
	if _, err := mgr.Connect(ctx, cc); !errors.Is(err, session.ErrLoggedOut) {
		t.Fatalf("an implicit connect after logout should refuse, got %v", err)
	}
	inv, err := fetchWholeInventory(ctx, backend, cc)
	if err != nil || len(inv.Errors) > 0 || len(inv.VMs) == 0 {
		t.Fatalf("an explicit load should log in again: err %v, errors %v", err, inv)
	}
	if st, _ := backend.Status("prod"); st.State != session.Connected {
		t.Errorf("after logging in again the session is %v", st.State)
	}
}
