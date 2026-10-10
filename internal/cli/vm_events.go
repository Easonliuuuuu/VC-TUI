package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

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
guest and tools state, task progress) are hidden unless --all is given.`), Example: `  # What vCenter logged for a VM
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
		vm, err := resolveVM(a, "vm", args[0], vms)
		if err != nil {
			reportFailures(a, failures)
			return err
		}
		// A vCenter that could not be listed may hold another VM by this
		// name, so say so even when the lookup found one.
		defer reportFailures(a, failures)
		listing, err := vmEvents(cmd.Context(), a, vm, limit)
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

// vmEvents reads one VM's event log on the vCenter it was found on, reusing
// the session the lookup opened.
func vmEvents(ctx context.Context, a *App, vm vsphere.VM, limit int) (vsphere.VMEventListing, error) {
	contexts, err := a.Contexts()
	if err != nil {
		return vsphere.VMEventListing{}, err
	}
	for _, cc := range contexts {
		if cc.Name != vm.Context {
			continue
		}
		mgr := a.Sessions()
		opCtx, cancel, tracker := mgr.Operation(ctx)
		defer cancel()
		s, err := mgr.Connect(opCtx, cc)
		if err != nil {
			return vsphere.VMEventListing{}, mgr.TimeoutError(err, tracker)
		}
		listing, err := s.Client().VMEvents(opCtx, vm.ID, limit)
		return listing, mgr.TimeoutError(err, tracker)
	}
	return vsphere.VMEventListing{}, fmt.Errorf("context %q is not configured", vm.Context)
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
