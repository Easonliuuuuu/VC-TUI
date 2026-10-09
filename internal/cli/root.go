// Package cli implements the vsfleet command line. Every command is a thin
// wrapper over the configuration, session and inventory layers, which is what
// lets the terminal interface be a second front end rather than a second
// implementation.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
	"github.com/easonliuuuuu/vsfleet/internal/session"
	"github.com/easonliuuuuu/vsfleet/internal/version"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// App holds everything the commands share: where the configuration lives, how
// to resolve credentials, and the sessions opened so far.
type App struct {
	ConfigPath   string
	ContextNames []string
	AllContexts  bool
	Format       string
	Timeout      time.Duration
	HistoryPath  string
	// RefreshInterval is how often the terminal interface re-reads inventory
	// in the background. Zero takes the built-in default; negative turns it
	// off. It only reaches the interface, so it is registered on the two
	// commands that open one rather than on every command in the tree.
	RefreshInterval time.Duration

	In  io.Reader
	Out io.Writer
	Err io.Writer

	cfg       *config.Config
	resolver  *credentials.Resolver
	prompt    *credentials.Prompt
	mgr       *session.Manager
	history   *assessment.Store
	collector *assessment.Collector

	// upd is the release check for this run (see updates); updateDone closes
	// when a check started before the command has finished.
	upd        *updater
	updateDone chan struct{}
}

// Config loads the configuration once per process.
func (a *App) Config() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		return nil, err
	}
	a.cfg = cfg
	return cfg, nil
}

// Prompt returns the interactive credential prompt.
func (a *App) Prompt() *credentials.Prompt {
	if a.prompt == nil {
		a.prompt = &credentials.Prompt{In: a.in(), Out: a.errOut()}
	}
	return a.prompt
}

// Resolver returns the credential resolver. A keyring reference falls back to
// an interactive prompt when nothing is stored; the non-interactive sources —
// env, file and exec — never do, because they exist to run unattended.
func (a *App) Resolver() *credentials.Resolver {
	if a.resolver == nil {
		a.resolver = credentials.NewResolver(
			credentials.NewKeyring(),
			credentials.NewEnv(),
			credentials.NewFile(),
			credentials.NewExec(),
			a.Prompt(),
		)
	}
	return a.resolver
}

// Sessions returns the session manager.
func (a *App) Sessions() *session.Manager {
	if a.mgr == nil {
		m := session.New(a.Resolver())
		m.ConnectOptions.UserAgent = version.UserAgent()
		if a.Timeout > 0 {
			m.ConnectTimeout = a.Timeout
			m.ConnectOptions.DialTimeout = a.Timeout
		}
		a.mgr = m
	}
	return a.mgr
}

// ConnectOptions builds the options used for one-off connections.
func (a *App) ConnectOptions() vsphere.ConnectOptions {
	return vsphere.ConnectOptions{
		Resolver:    a.Resolver(),
		DialTimeout: a.Timeout,
		UserAgent:   version.UserAgent(),
	}
}

// History opens the local assessment database once for the process. Callers
// may treat an open failure as a feature warning: live inventory does not
// depend on historical storage being available.
func (a *App) History() (*assessment.Store, error) {
	if a.history != nil {
		return a.history, nil
	}
	s, err := assessment.Open(a.HistoryPath)
	if err != nil {
		return nil, err
	}
	a.history = s
	return s, nil
}

// Close releases resources opened by the application. It is deliberately a
// pure release: callers that only build a command tree can close it without
// causing any lazy accessor to run.
func (a *App) Close(ctx context.Context) error {
	var errs []error
	if a.mgr != nil {
		errs = append(errs, a.mgr.Close(ctx))
	}
	if a.history != nil {
		errs = append(errs, a.history.Close())
	}
	return errors.Join(errs...)
}

func (a *App) Collector() (*assessment.Collector, error) {
	if a.collector != nil {
		return a.collector, nil
	}
	s, err := a.History()
	if err != nil {
		return nil, err
	}
	a.collector = &assessment.Collector{Store: s, Manager: a.Sessions()}
	return a.collector, nil
}

func (a *App) Assessment() (*assessment.Service, error) {
	c, err := a.Collector()
	if err != nil {
		return nil, err
	}
	return &assessment.Service{Store: a.history, Collector: c}, nil
}

// Contexts resolves the contexts a command should act on, honouring --context
// and --all-contexts.
func (a *App) Contexts() ([]*config.Context, error) {
	cfg, err := a.Config()
	if err != nil {
		return nil, err
	}
	return cfg.Resolve(a.ContextNames, a.AllContexts)
}

// StoredContextNames returns the raw context-name selector for commands that
// scope stored/history evidence (assessment reports, trends, topology, ...)
// rather than resolving live contexts. It applies the same --all-contexts
// precedence as Contexts: when --all-contexts is set, any --context values
// are ignored so the full stored scope is used, instead of being validated
// or narrowed by --context.
func (a *App) StoredContextNames() []string {
	if a.AllContexts {
		return nil
	}
	return a.ContextNames
}

// SingleContext resolves exactly one context, for commands that cannot span
// several vCenters.
func (a *App) SingleContext(name string) (*config.Context, error) {
	cfg, err := a.Config()
	if err != nil {
		return nil, err
	}
	if name == "" && len(a.ContextNames) == 1 {
		name = a.ContextNames[0]
	}
	return cfg.Context(name)
}

func (a *App) in() io.Reader {
	if a.In != nil {
		return a.In
	}
	return os.Stdin
}

func (a *App) out() io.Writer {
	if a.Out != nil {
		return a.Out
	}
	return os.Stdout
}

func (a *App) errOut() io.Writer {
	if a.Err != nil {
		return a.Err
	}
	return os.Stderr
}

func (a *App) json() bool { return a.Format == FormatJSON }

func (a *App) checkFormat() error {
	switch a.Format {
	case FormatTable, FormatJSON, "":
		return nil
	default:
		return fmt.Errorf("unknown output format %q (supported: table, json)", a.Format)
	}
}

func rejectStoredContextSelection(cmd *cobra.Command, operation string) error {
	if cmd.Flags().Changed("context") {
		return fmt.Errorf("--context is not supported for assessment %s", operation)
	}
	return nil
}

// NewRootCommand builds the vsfleet command tree.
func NewRootCommand(a *App) *cobra.Command {
	root := &cobra.Command{
		Use:   "vsfleet",
		Short: "Read-only vSphere estate assessment and diagnostics across every vCenter",
		Long: `vsfleet treats every vCenter as a named context, the way kubectl treats
clusters. Each context carries its own endpoint, credential reference,
network route and TLS policy, so a lab reached directly and a customer
vCenter reached through a SOCKS5 proxy work side by side in one process.

Every command is strictly read-only: vsfleet never powers a VM on or off,
reverts a snapshot, alters inventory or changes a network. It is safe to point
at production.`,
		Example: `  # Open the terminal interface on the current context
  vsfleet

  # Try everything against sample data, with no vCenter and nothing written
  vsfleet demo

  # Add a vCenter and make it current
  vsfleet context add --name prod --endpoint https://vcsa.example.internal --username svc-ro@vsphere.local --use

  # List VMs on one context, then on every context at once
  vsfleet vm list --context prod
  vsfleet vm list --all-contexts

  # Find an object when you do not know which vCenter holds it
  vsfleet search ubuntu-golden --all-contexts

  # Capture history, then compare the two most recent captures
  vsfleet assessment run --all-contexts --label nightly
  vsfleet assessment diff`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.String(),
		// A bare "vsfleet" opens the terminal interface: that is the product,
		// not the subcommand tree. Arguments stay rejected so a genuine typo
		// like "vsfleet statas" still reports "unknown command" instead of
		// quietly launching the interface with an argument it ignores — and
		// rejectUnknownArgs, unlike cobra.NoArgs, offers the near miss.
		Args: rejectUnknownArgs,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.checkFormat(); err != nil {
				return err
			}
			a.startUpdateCheck(cmd)
			return nil
		},
		PersistentPostRun: func(cmd *cobra.Command, _ []string) {
			a.finishUpdateCheck(cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUI(a, cmd)
		},
	}
	root.SetOut(a.out())
	root.SetErr(a.errOut())
	root.SetIn(a.in())

	f := root.PersistentFlags()
	f.StringVar(&a.ConfigPath, "config", "", "path to the configuration file (default: platform user config directory)")
	f.StringSliceVar(&a.ContextNames, "context", nil, "context to act on, repeatable (default: the current context)")
	f.BoolVar(&a.AllContexts, "all-contexts", false, "act on every configured context")
	f.StringVarP(&a.Format, "output", "o", FormatTable, "output format: table or json")
	f.DurationVar(&a.Timeout, "timeout", 30*time.Second, "per-vCenter timeout")
	f.StringVar(&a.HistoryPath, "history-db", "", "path to the local assessment database")
	// A bare "vsfleet" opens the interface, so the interface's own flag
	// belongs here too — on root's own flags, not the persistent set, so
	// "vsfleet vm list --refresh" is still the error it should be.
	addRefreshFlag(root, a)

	registerCommandGroups(root)
	root.AddCommand(
		inGroup(groupStart, newUICommand(a)),
		inGroup(groupStart, newDemoCommand(a)),
		inGroup(groupStart, newContextCommand(a)),
		inGroup(groupStart, newStatusCommand(a)),
		inGroup(groupInventory, newSearchCommand(a)),
		inGroup(groupHistory, newImportCommand(a)),
		inGroup(groupHistory, newAssessmentCommand(a)),
		inGroup(groupHistory, newHealthCommand(a)),
		inGroup(groupAnalysis, newTopologyCommand(a)),
		inGroup(groupAnalysis, newDependenciesCommand(a)),
		inGroup(groupAnalysis, newBlastRadiusCommand(a)),
		inGroup(groupAnalysis, newCompatibilityCommand(a)),
		inGroup(groupDiagnose, newDoctorCommand(a)),
	)
	for _, cmd := range newInventoryCommands(a) {
		root.AddCommand(inGroup(groupInventory, cmd))
	}
	return root
}

// Execute runs the command tree and reports failures on stderr.
func Execute(ctx context.Context) int {
	a := &App{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}
	root := NewRootCommand(a)
	defer func() {
		_ = a.Close(ctx)
	}()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(a.errOut(), "vsfleet: %v\n", err)
		var coded interface{ ExitCode() int }
		if errors.As(err, &coded) {
			return coded.ExitCode()
		}
		return 1
	}
	return 0
}
