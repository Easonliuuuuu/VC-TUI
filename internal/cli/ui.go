package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/tui"
	"github.com/easonliuuuuu/vsfleet/internal/uistate"
	"github.com/easonliuuuuu/vsfleet/internal/version"
)

// addRefreshFlag registers --refresh on a command that opens the interface.
// It takes the command rather than its flag set so that this package does not
// need to import pflag directly, which would promote it from an indirect
// dependency to a direct one for the sake of one parameter type.
func addRefreshFlag(cmd *cobra.Command, a *App) {
	cmd.Flags().DurationVar(&a.RefreshInterval, "refresh", 0,
		"how often to re-read inventory in the background (0 for the default of "+
			tui.DefaultRefreshInterval.String()+", negative to only read when asked)")
}

func newUICommand(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Browse every vCenter in one terminal interface",
		Example: `  # Open the interface on the current context
  vsfleet ui

  # Open it across the whole estate
  vsfleet ui --all-contexts

  # Re-read inventory every five minutes instead of the default
  vsfleet ui --refresh 5m

  # Only read when asked
  vsfleet ui --refresh -1`,
		Long: `Open the terminal interface.

The numbered tabs show one resource kind at a time for the vCenter in scope
or, with --all-contexts, for the whole estate at once. Press "c" for the
contexts screen, which lists every configured vCenter whether or not it is
reachable and is where you switch, add, edit and remove them.

The pane renders before credentials are requested. Keyring authentication runs
in the background. If interactive input is needed at startup, the pane remains
usable and marks the context as credentials required; selecting or reloading it
opens a masked password overlay inside the interface. Untouched contexts remain
disconnected until selected.

	"/" filters the table in front of you and "tab" widens the same query into
	every vCenter and every kind at once, which is "vsfleet search" without
	leaving the interface. A vCenter that has not answered is reported as not
	searched rather than quietly narrowing the result.

	"H" opens the historical Changes screen. It compares the newest two explicit
	assessments, lets you choose another baseline or target, and captures the
	vCenter in scope with "n" — every vCenter shown when --all-contexts (or "a")
	is on.

With no contexts configured yet, this opens straight into the setup form
instead of asking you to run "vsfleet context add" first.

The context, resource tab and sort order are remembered between runs, so
closing vsfleet and opening it again picks up where you left off. --context
overrides the remembered context for this run without changing what gets
remembered next time.

The first run, and the first run after each upgrade, opens with a short
welcome animation while the remembered vCenter loads underneath. Any key
skips it; set VSFLEET_NO_WELCOME=1 to turn it off.

The interface shows nothing the command line cannot: it is a faster way to
ask the same questions, not a second implementation of them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUI(a, cmd)
		},
	}
	addRefreshFlag(cmd, a)
	return cmd
}

// runUI is also what a bare "vsfleet" runs: the terminal interface is the
// product's front door, and "vsfleet ui" is kept as an explicit, memorable
// alias for it.
func runUI(a *App, cmd *cobra.Command) error {
	cfg, err := a.Config()
	if err != nil {
		return err
	}

	remembered := uistate.Load("")
	current := cfg.CurrentContext
	if remembered.Context != "" {
		if _, err := cfg.Context(remembered.Context); err == nil {
			current = remembered.Context
		}
	}

	// The contexts screen is a switcher, so it always lists every context.
	// --context and --all-contexts choose where the cursor starts and how
	// wide the initial scope is, not what exists. An explicit --context
	// overrides the remembered one for this run only.
	if len(a.ContextNames) > 0 {
		if len(cfg.Contexts) == 0 {
			return fmt.Errorf("context %q does not exist (no contexts are configured yet)", a.ContextNames[0])
		}
		if _, err := cfg.Context(a.ContextNames[0]); err != nil {
			return err
		}
		current = a.ContextNames[0]
	}

	// A background load's credential prompt cannot read the password from
	// os.Stdin the way the command line does: Bubble Tea already owns the
	// terminal, and a second reader racing it for the same input is what let
	// a keystroke meant for the password field reach a global shortcut
	// instead (issue #26). The coordinator answers "prompt" references
	// through the interface itself, so it replaces the resolver's usual
	// prompt provider for the lifetime of this run. Automatic credential
	// resolution starts only after the first pane has rendered, and startup
	// never opens the overlay without a subsequent explicit selection/reload.
	coordinator := tui.NewPromptCoordinator()
	a.Resolver().SetProvider(coordinator)

	backend := tui.NewBackend(cfg, a.Resolver(), a.Sessions(), a.ConnectOptions())
	assessmentService, historyErr := a.Assessment()
	if historyErr != nil {
		fmt.Fprintf(a.errOut(), "warning: historical assessments unavailable: %v\n", historyErr)
	}
	release := version.Release()
	snap, runErr := tui.Run(cmd.Context(), backend, tui.Options{
		Current:     current,
		AllContexts: a.AllContexts,
		Kind:        remembered.Kind,
		Sort:        remembered.Sort,

		RefreshInterval:  a.RefreshInterval,
		Credentials:      coordinator,
		In:               a.in(),
		Out:              a.out(),
		Assessment:       assessmentService,
		SSHUser:          cfg.SSH.User,
		SSHVMUser:        cfg.SSH.VMUser,
		SSHHostUser:      cfg.SSH.HostUser,
		SSHRoutes:        cfg.SSH.Routes,
		SSHUsers:         remembered.SSHUsers,
		SSHIdentityFiles: remembered.SSHIdentityFiles,
		SSHDestinations:  remembered.SSHDestinations,
		Welcome:          welcomeFor(remembered.WelcomedVersion, release, len(cfg.Contexts) > 0, os.Getenv),
	})
	// A clean run is the only one worth remembering: a program that never
	// really started (no TTY, say) has nothing truthful to say about where
	// the cursor was.
	if runErr == nil {
		if err := uistate.Save("", uistate.State{Context: snap.Context, Kind: snap.Kind, Sort: snap.Sort, SSHUsers: snap.SSHUsers, SSHIdentityFiles: snap.SSHIdentityFiles, SSHDestinations: snap.SSHDestinations, WelcomedVersion: release}); err != nil {
			fmt.Fprintf(a.errOut(), "warning: could not remember the last screen (%v)\n", err)
		}
	}
	return runErr
}
