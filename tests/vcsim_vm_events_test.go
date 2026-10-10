//go:build integration

package tests

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func destroyVCSIMVM(t *testing.T, endpoint *simEndpoint, vmName string) {
	t.Helper()
	ctx := context.Background()
	u, err := url.Parse(endpoint.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/sdk"
	u.User = url.UserPassword(vcsimUsername, testPassword)
	client, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		t.Fatalf("connect to vcsim for destroy: %v", err)
	}
	defer client.Logout(ctx)
	finder := find.NewFinder(client.Client, true)
	dc, err := finder.Datacenter(ctx, "DC0")
	if err != nil {
		t.Fatalf("find datacenter: %v", err)
	}
	finder.SetDatacenter(dc)
	vm, err := finder.VirtualMachine(ctx, vmName)
	if err != nil {
		t.Fatalf("find VM %q: %v", vmName, err)
	}
	for _, op := range []func() error{
		func() error {
			task, err := vm.PowerOff(ctx)
			if err != nil {
				return err
			}
			return task.Wait(ctx)
		},
		func() error {
			task, err := vm.Destroy(ctx)
			if err != nil {
				return err
			}
			return task.Wait(ctx)
		},
	} {
		if err := op(); err != nil {
			t.Fatalf("destroy %q: %v", vmName, err)
		}
	}
}

// TestVCSIMVMEventsReadsADeletedVMThroughStoredHistory proves that a VM gone
// from inventory is still found by name through the assessments that saw it,
// and that its event log, which vCenter keeps, ends with the removal (#374).
func TestVCSIMVMEventsReadsADeletedVMThroughStoredHistory(t *testing.T) {
	fixture := fixtureHistory(t)
	r := newRunner(t)
	addFixtureContexts(t, r, fixture)
	historyDB := filepath.Join(t.TempDir(), "history.db")
	captureVCSIM(t, r, historyDB, false)
	const gone = "DC0_C0_RP0_VM1"
	destroyVCSIMVM(t, fixture.Endpoints["history"], gone)
	captureVCSIM(t, r, historyDB, false)

	read := func(args ...string) (vsphere.VMEventListing, string) {
		t.Helper()
		stdout, stderr, err := r.run("", append([]string{"--history-db", historyDB, "-o", "json", "vm", "events"}, args...)...)
		if err != nil {
			t.Fatalf("vsfleet vm events %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
		}
		var listing vsphere.VMEventListing
		if err := json.Unmarshal([]byte(stdout), &listing); err != nil {
			t.Fatalf("decode events: %v\n%s", err, stdout)
		}
		return listing, stderr
	}

	listing, stderr := read(gone, "--all")
	if !strings.Contains(stderr, "no longer in inventory") {
		t.Fatalf("stderr does not say the VM left inventory: %q", stderr)
	}
	if listing.Context != "history" || listing.VMID == "" || len(listing.Events) == 0 {
		t.Fatalf("listing = %+v, want events for the stored ID on the history context", listing)
	}
	last := listing.Events[len(listing.Events)-1]
	if last.Explains != vsphere.EventRemoved {
		t.Fatalf("last event = %+v, want the removal", last)
	}
	for _, e := range listing.Events {
		if strings.Contains(e.Message, "DC0_C0_RP0_VM0") && !strings.Contains(e.Message, gone) {
			t.Fatalf("event %q belongs to another VM", e.Message)
		}
	}

	// The stored managed object ID finds it too, and a VM still in
	// inventory is read without the note.
	if byID, _ := read(listing.VMID); len(byID.Events) != len(listing.Events)-countRoutine(listing.Events) {
		t.Fatalf("reading by stored ID gave %d events, want the %d that are not routine", len(byID.Events), len(listing.Events)-countRoutine(listing.Events))
	}
	if live, stderr := read("DC0_C0_RP0_VM0"); len(live.Events) == 0 || strings.Contains(stderr, "no longer in inventory") {
		t.Fatalf("live VM: %d events, stderr %q", len(live.Events), stderr)
	}

	_, stderr, err := r.run("", "--history-db", historyDB, "vm", "events", "no-such-vm")
	if err == nil || !strings.Contains(err.Error(), `no vm matched "no-such-vm"`) {
		t.Fatalf("unknown VM: err = %v, stderr %q", err, stderr)
	}
}

func countRoutine(events []vsphere.VMEvent) int {
	n := 0
	for _, e := range events {
		if e.Minor {
			n++
		}
	}
	return n
}
