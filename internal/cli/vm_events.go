package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func newVMEventsCommand(a *App) *cobra.Command {
	var all bool
	var limit int
	cmd := &cobra.Command{Use: "events NAME_OR_UUID", Short: "Show a VM's live vCenter event log", Long: strings.TrimSpace(`
Show what vCenter logged for one VM: migrations, reconfigurations, renames,
snapshot tasks and failures, with who did them and when.

This reads vCenter live and is bounded by what vCenter still keeps, usually
the last 30 days. For changes across your stored assessments, which go back as
far as your history does, use "vsfleet vm history". Routine events (power,
guest and tools state, task progress) are hidden unless --all is given.

A VM that is no longer in inventory is found in stored history instead, and
its events are read under the managed object ID that history recorded for it.
That is how to see who deleted a VM, and when.`), Example: `  # What vCenter logged for a VM
  vsfleet vm events web-01

  # Include power, guest and other routine events
  vsfleet vm events web-01 --all

  # As JSON, for a script
  vsfleet vm events web-01 -o json`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		vms, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.VM, error) {
			return c.ListVMs(ctx)
		})
		if err != nil {
			reportFailures(a, failures)
			return err
		}
		// A vCenter that could not be listed may hold another VM by this
		// name, so say so even when the lookup found one.
		defer reportFailures(a, failures)
		var listing vsphere.VMEventListing
		vm, err := resolveVM(a, "vm", args[0], vms)
		var none *noMatchError
		switch {
		case err == nil:
			listing, err = vmEvents(cmd.Context(), a, vm.Context, vm.ID, limit)
		case errors.As(err, &none):
			listing, err = storedVMEvents(cmd.Context(), a, args[0], limit, err)
		}
		if err != nil {
			return err
		}
		read, hidden := len(listing.Events), 0
		if !all {
			kept := listing.Events[:0]
			for _, e := range listing.Events {
				if !e.Minor {
					kept = append(kept, e)
				}
			}
			hidden = len(listing.Events) - len(kept)
			listing.Events = kept
		}
		if a.json() {
			return writeJSON(a.out(), listing)
		}
		t := newTable(a.out(), "TIME", "EVENT", "BY", "RESULT", "DETAIL")
		for _, e := range listing.Events {
			t.row(e.Time.Local().Format("2006-01-02 15:04"), e.Label, dash(e.User), eventResult(e.Result), dash(e.DisplayDetail()))
		}
		t.flush()
		// --limit bounds what vCenter returns, before routine events are
		// hidden, so the notes count what was read, not what is shown.
		if hidden > 0 {
			fmt.Fprintf(a.errOut(), "\n%d of %d events read are routine and hidden (--all shows them)\n", hidden, read)
		}
		if listing.Truncated {
			fmt.Fprintf(a.errOut(), "\nread only the newest %d events; older ones were not read (raise --limit)\n", listing.Limit)
		}
		return nil
	}}
	cmd.Flags().BoolVar(&all, "all", false, "include routine power, guest, tools and task events")
	cmd.Flags().IntVar(&limit, "limit", vsphere.DefaultVMEventLimit, "most events to read from vCenter, routine ones included")
	return cmd
}

// vmEvents reads one VM's event log, identified by its managed object ID on
// the named context, reusing the session an earlier lookup opened.
func vmEvents(ctx context.Context, a *App, contextName, vmID string, limit int) (vsphere.VMEventListing, error) {
	cfg, err := a.Config()
	if err != nil {
		return vsphere.VMEventListing{}, err
	}
	cc, err := cfg.Context(contextName)
	if err != nil {
		return vsphere.VMEventListing{}, fmt.Errorf("context %q is not configured", contextName)
	}
	mgr := a.Sessions()
	opCtx, cancel, tracker := mgr.Operation(ctx)
	defer cancel()
	s, err := mgr.Connect(opCtx, cc)
	if err != nil {
		return vsphere.VMEventListing{}, mgr.TimeoutError(err, tracker)
	}
	listing, err := s.Client().VMEvents(opCtx, vmID, limit)
	return listing, mgr.TimeoutError(err, tracker)
}

// storedVMEvents reads the events of a VM that is no longer in inventory but
// that vCenter still logs events for, such as its removal and who did it. The
// VM is found the way "vsfleet vm history" finds it, and its events are read
// under each managed object ID that history recorded for it, on each vCenter.
// notFound is returned when history does not know the VM either.
func storedVMEvents(ctx context.Context, a *App, query string, limit int, notFound error) (vsphere.VMEventListing, error) {
	// Looking must not create an empty history database just to find nothing.
	if a.history == nil {
		path := a.HistoryPath
		if path == "" {
			path, _ = assessment.DefaultPath()
		}
		if _, err := os.Stat(path); err != nil {
			return vsphere.VMEventListing{}, notFound
		}
	}
	s, err := a.History()
	if err != nil {
		return vsphere.VMEventListing{}, fmt.Errorf("%w (stored history: %v)", notFound, err)
	}
	stored, err := s.TimelineForContexts(ctx, query, a.StoredContextNames(), true, false)
	var ambiguous *assessment.AmbiguousVMError
	switch {
	case errors.As(err, &ambiguous):
		return vsphere.VMEventListing{}, err
	case err != nil:
		return vsphere.VMEventListing{}, fmt.Errorf("%w (stored history: %v)", notFound, err)
	}
	targets := storedEventTargets(stored)
	if len(targets) == 0 {
		return vsphere.VMEventListing{}, notFound
	}
	return readStoredTargets(ctx, a, query, limit, targets)
}

// eventTarget is one VM to read events for: a managed object ID on one
// vCenter, which any of the contexts that reach that vCenter can read.
type eventTarget struct {
	vmID     string
	contexts []string
}

// storedEventTargets lists the managed object IDs a VM's stored history
// recorded, one per vCenter. Contexts that reach the same vCenter share a
// target, so its event log is read once.
func storedEventTargets(stored []assessment.VMHistoryEvent) []eventTarget {
	type key struct{ vcenter, vmID string }
	byKey := map[key]*eventTarget{}
	var found []*eventTarget
	for _, e := range stored {
		o := e.Observation
		if o == nil || o.VM.ID == "" {
			continue
		}
		k := key{o.VCenterID, o.VM.ID}
		if o.VCenterID == "" {
			// Without a vCenter identity only the same context is known to
			// reach the same vCenter.
			k.vcenter = "context:" + o.Context
		}
		t := byKey[k]
		if t == nil {
			t = &eventTarget{vmID: o.VM.ID}
			byKey[k] = t
			found = append(found, t)
		}
		if !slices.Contains(t.contexts, o.Context) {
			t.contexts = append(t.contexts, o.Context)
		}
	}
	out := make([]eventTarget, len(found))
	for i, t := range found {
		sort.Strings(t.contexts)
		out[i] = *t
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].contexts[0] != out[j].contexts[0] {
			return out[i].contexts[0] < out[j].contexts[0]
		}
		return out[i].vmID < out[j].vmID
	})
	return out
}

// readStoredTargets reads each target through the first of its contexts that
// is configured and merges the events, oldest first. A target that cannot be
// read is reported and skipped while another could be read, because the rest
// of the log is still what was asked for.
func readStoredTargets(ctx context.Context, a *App, query string, limit int, targets []eventTarget) (vsphere.VMEventListing, error) {
	cfg, err := a.Config()
	if err != nil {
		return vsphere.VMEventListing{}, err
	}
	ids := make([]string, len(targets))
	for i, t := range targets {
		ids[i] = t.vmID + " on " + t.contexts[0]
	}
	fmt.Fprintf(a.errOut(), "%q is no longer in inventory; reading events under its stored ID: %s\n", query, strings.Join(ids, ", "))

	merged := vsphere.VMEventListing{Limit: limit}
	var failures []contextFailure
	read := 0
	for _, t := range targets {
		i := slices.IndexFunc(t.contexts, func(name string) bool {
			_, err := cfg.Context(name)
			return err == nil
		})
		if i < 0 {
			failures = append(failures, contextFailure{Context: t.contexts[0], Err: fmt.Errorf("context is not configured, so %s cannot be read", t.vmID)})
			continue
		}
		listing, err := vmEvents(ctx, a, t.contexts[i], t.vmID, limit)
		if err != nil {
			failures = append(failures, contextFailure{Context: t.contexts[i], Err: err})
			continue
		}
		// One target keeps the listing's own context and ID; several have none
		// in common, and each event carries its context.
		if read++; read == 1 {
			merged.Context, merged.VMID = listing.Context, listing.VMID
		} else {
			merged.Context, merged.VMID = "", ""
		}
		merged.Limit = listing.Limit
		merged.Truncated = merged.Truncated || listing.Truncated
		merged.Events = append(merged.Events, listing.Events...)
	}
	reportFailures(a, failures)
	if read == 0 {
		return merged, fmt.Errorf("could not read events for any stored ID of %q", query)
	}
	vsphere.SortVMEvents(merged.Events)
	return merged, nil
}

func eventResult(result string) string {
	switch result {
	case vsphere.ResultOK:
		return glyphOK
	case vsphere.ResultFailed:
		return glyphFail + " failed"
	default:
		return "-"
	}
}
