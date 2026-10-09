package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/contextops"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func newContextCommand(a *App) *cobra.Command {
	cmd := requireSubcommand(&cobra.Command{
		Use:     "context",
		Aliases: []string{"contexts", "ctx"},
		Short:   "Manage vCenter contexts",
		Long: strings.TrimSpace(`
A context names one vCenter and everything needed to reach it: endpoint,
credential reference, network route and TLS policy. Contexts are independent,
so a lab reached directly and a customer vCenter reached through a SOCKS5
proxy work side by side.

Passwords are never written to the configuration file. The file records a
reference; the secret lives in the OS keyring or comes from a prompt, an
environment variable, a file or a program at the moment it is needed.`),
		Example: `  # Add a context interactively
  vsfleet context add

  # Add one unattended, from a provisioning script
  vsfleet context add --name prod \
    --endpoint https://vcsa.example.internal \
    --username svc-ro@vsphere.local --password-stdin < secret.txt

  # See what is configured and which one is current
  vsfleet context list

  # Switch the current context, then confirm it works
  vsfleet context use prod
  vsfleet context test prod`,
	})
	cmd.AddCommand(
		newContextAddCommand(a),
		newContextListCommand(a),
		newContextUseCommand(a),
		newContextShowCommand(a),
		newContextRemoveCommand(a),
		newContextTestCommand(a),
	)
	return cmd
}

// contextFlags mirror every field of a context so that "context add" works
// unattended in a provisioning script as well as interactively.
type contextFlags struct {
	name               string
	endpoint           string
	username           string
	via                string
	viaMoRef           string
	credential         string
	datacenter         string
	transport          string
	proxyAddress       string
	proxyUsername      string
	proxyCredential    string
	remoteDNS          bool
	tlsMode            string
	thumbprint         string
	passwordStdin      bool
	proxyPasswordStdin bool
	skipTest           bool
	force              bool
	setCurrent         bool
}

func (f *contextFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "context name")
	fl.StringVar(&f.endpoint, "endpoint", "", "vCenter endpoint, e.g. https://vcsa.example.internal")
	fl.StringVar(&f.username, "username", "", "vCenter username, e.g. administrator@vsphere.local")
	fl.StringVar(&f.via, "via", "", "parent context this was added from (context add --force drops it unless --via is passed again)")
	fl.StringVar(&f.viaMoRef, "via-moref", "", "managed object reference of the VM this was added from")
	fl.StringVar(&f.credential, "credential", "", "credential reference: keyring:<key>, prompt, env:<VAR>, file:<path> or exec:<program>")
	fl.StringVar(&f.datacenter, "datacenter", "", "default datacenter for inventory queries")
	fl.StringVar(&f.transport, "transport", "", "network route: direct, socks5, http or https")
	fl.StringVar(&f.proxyAddress, "proxy-address", "", "proxy address for socks5/http/https, host:port")
	fl.StringVar(&f.proxyUsername, "proxy-username", "", "username for a proxy that requires authentication")
	fl.StringVar(&f.proxyCredential, "proxy-credential", "", "credential reference for the proxy password: keyring:<key>, env:<VAR>, file:<path> or exec:<program> (default keyring:<name>-proxy)")
	fl.BoolVar(&f.remoteDNS, "remote-dns", false, "resolve the vCenter hostname at the proxy (socks5 only; http and https always do)")
	fl.StringVar(&f.tlsMode, "tls", "", "certificate policy: system, thumbprint or insecure")
	fl.StringVar(&f.thumbprint, "thumbprint", "", "certificate fingerprint to pin; with --tls thumbprint and no value, the presented certificate is fetched")
	fl.BoolVar(&f.passwordStdin, "password-stdin", false, "read the password from standard input")
	fl.BoolVar(&f.proxyPasswordStdin, "proxy-password-stdin", false, "read the proxy password from standard input, after the vCenter password if both are piped in")
	fl.BoolVar(&f.skipTest, "no-test", false, "save without testing the connection")
	fl.BoolVar(&f.force, "force", false, "replace an existing context of the same name")
	fl.BoolVar(&f.setCurrent, "use", false, "make this the current context")
}

func newContextAddCommand(a *App) *cobra.Command {
	var f contextFlags
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a vCenter context",
		Example: `  # Interactive wizard
  vsfleet context add

  # Unattended, password piped in and stored in the OS keyring
  vsfleet context add --name prod \
    --endpoint https://vcsa.example.internal \
    --username svc-ro@vsphere.local --password-stdin < secret.txt

  # Through a SOCKS5 proxy, resolving DNS at the proxy
  vsfleet context add --name customer-a \
    --endpoint https://vcsa.internal --username svc-ro@vsphere.local \
    --transport socks5 --proxy-address bastion.example.net:1080 --remote-dns

  # Pin the presented certificate instead of trusting the system store
  vsfleet context add --name lab --endpoint https://vcsa.lab \
    --username administrator@vsphere.local --tls thumbprint

  # Take the password from a secret manager at connect time
  vsfleet context add --name ci --endpoint https://vcsa.example.internal \
    --username svc-ro@vsphere.local --credential "exec:vault-read vsphere/ci"`,
		Long: `Add a vCenter context.

Run without flags for an interactive wizard. Pass --name, --endpoint and
--username to add a context unattended, which is what a provisioning script
wants. The password is never written to the configuration file: it goes to
the operating system keyring, and the file records only a reference to it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runContextAdd(cmd.Context(), a, &f)
		},
	}
	f.register(cmd)
	return cmd
}

func runContextAdd(ctx context.Context, a *App, f *contextFlags) error {
	cfg, err := a.Config()
	if err != nil {
		return err
	}
	interactive := f.name == "" || f.endpoint == "" || f.username == ""
	p := a.Prompt()
	if interactive && !p.Interactive() {
		return errors.New("context add needs --name, --endpoint and --username when standard input is not a terminal")
	}

	cc := &config.Context{
		Name:       f.name,
		Endpoint:   f.endpoint,
		Username:   f.username,
		Via:        f.via,
		ViaMoRef:   f.viaMoRef,
		Datacenter: f.datacenter,
		Transport: config.TransportConfig{
			Type:      f.transport,
			Address:   f.proxyAddress,
			Username:  f.proxyUsername,
			RemoteDNS: f.remoteDNS,
		},
		TLS: config.TLSConfig{Mode: f.tlsMode, Thumbprint: f.thumbprint},
	}

	// The credential references are parsed before any password is read, so a
	// reference that cannot hold a password rejects the request without first
	// consuming the operator's standard input.
	var credRef credentials.Ref
	if f.credential != "" {
		ref, err := credentials.ParseRef(f.credential)
		if err != nil {
			return err
		}
		credRef = ref
	}
	var proxyCredRef credentials.Ref
	if f.proxyCredential != "" {
		ref, err := credentials.ParseRef(f.proxyCredential)
		if err != nil {
			return err
		}
		proxyCredRef = ref
	}
	// env, file and exec name where the password lives; they do not store one.
	// Reading a password here and then dropping it silently — which is what
	// contextops.Save does for any non-keyring scheme — would leave an
	// operator believing a secret was saved that never was.
	if credRef.NonInteractive() && f.passwordStdin {
		return fmt.Errorf("--password-stdin cannot be combined with --credential %s: %s resolves the password when it is needed, so there is nothing to store", credRef, credRef.Scheme)
	}
	if proxyCredRef.NonInteractive() && f.proxyPasswordStdin {
		return fmt.Errorf("--proxy-password-stdin cannot be combined with --proxy-credential %s: %s resolves the password when it is needed, so there is nothing to store", proxyCredRef, proxyCredRef.Scheme)
	}

	var password string
	var havePassword bool
	if f.passwordStdin {
		s, err := p.ReadSecret("")
		if err != nil {
			return err
		}
		password, havePassword = s, true
	}
	var proxyPassword string
	var haveProxyPassword bool
	if f.proxyPasswordStdin {
		s, err := p.ReadSecret("")
		if err != nil {
			return err
		}
		proxyPassword, haveProxyPassword = s, true
	}

	if interactive {
		if err := runAddWizard(ctx, a, cc, f); err != nil {
			return err
		}
		if credRef.IsZero() {
			credRef = cc.Credential
		}
		// env, file and exec resolve the password themselves, and the
		// connection test below goes through them — which is the point: a
		// missing variable or a failing helper shows up now, not on the first
		// unattended run. A password typed here would be tested instead and
		// then thrown away.
		if !havePassword && !credRef.NonInteractive() {
			s, err := p.ReadSecret(fmt.Sprintf("Password for %s: ", cc.Username))
			if err != nil {
				return err
			}
			password, havePassword = s, true
		}
		if cc.Transport.Username != "" && !haveProxyPassword {
			s, err := p.ReadSecret(fmt.Sprintf("Password for proxy user %s: ", cc.Transport.Username))
			if err != nil {
				return err
			}
			proxyPassword, haveProxyPassword = s, true
		}
	}

	// A pinned context with no fingerprint yet: fetch what the server presents
	// so the operator can look at it before trusting it. This runs before
	// validation, which would otherwise reject the not-yet-known fingerprint.
	if cc.TLS.Mode == config.TLSThumbprint && cc.TLS.Thumbprint == "" {
		if err := discoverThumbprint(ctx, a, cc, interactive); err != nil {
			return err
		}
	}

	in := contextops.Input{
		Name: cc.Name, Endpoint: cc.Endpoint, Username: cc.Username, Via: cc.Via, ViaMoRef: cc.ViaMoRef, Datacenter: cc.Datacenter,
		Transport: cc.Transport, TLS: cc.TLS, Credential: credRef,
		Password: password, HavePassword: havePassword,
		ProxyCredential: proxyCredRef, ProxyPassword: proxyPassword, HaveProxyPassword: haveProxyPassword,
		Replace: f.force, SaveOnTestFailure: f.force, SetCurrent: f.setCurrent,
	}

	if !f.skipTest {
		fmt.Fprintf(a.errOut(), "Testing connection to %s ...\n", cc.Endpoint)
	}
	res, err := contextops.Save(ctx, cfg, a.Resolver(), a.ConnectOptions(), in, !f.skipTest)
	if !f.skipTest && res.Diagnosis != nil {
		if res.Diagnosis.OK() {
			fmt.Fprintf(a.errOut(), "%s Connected to %s in %s\n", glyphOK, res.Diagnosis.About.FullVersion(), humanDuration(res.Diagnosis.Latency))
		} else {
			printDiagnosis(a, res.Diagnosis, true)
			if f.force {
				fmt.Fprintln(a.errOut(), "Saving anyway because --force was given.")
			}
		}
	}
	if err != nil {
		if res != nil && res.Diagnosis != nil && !res.Diagnosis.OK() {
			return fmt.Errorf("connection test failed; fix the problem or pass --no-test to save anyway")
		}
		return err
	}
	if res.StoreWarning != nil {
		fmt.Fprintf(a.errOut(), "warning: %v\n", res.StoreWarning)
		fmt.Fprintf(a.errOut(), "vsfleet will ask for it on each run. To run unattended instead, add the context again with --force and\n--credential env:<VAR>, file:<path> or exec:<program>.\n")
	}
	fmt.Fprintf(a.out(), "Saved context %q to %s\n", res.Context.Name, cfg.Path())
	return nil
}

// runAddWizard fills in whatever the flags left empty. Editing TOML by hand is
// a fine second step, but it should not be the first thing a new user meets.
func runAddWizard(ctx context.Context, a *App, cc *config.Context, f *contextFlags) error {
	p := a.Prompt()
	out := a.errOut()
	fmt.Fprintln(out, "Add vCenter")
	fmt.Fprintln(out)

	ask := func(label, current string, required bool) (string, error) {
		for {
			prompt := "  " + label
			if current != "" {
				prompt += " [" + current + "]"
			}
			prompt += ": "
			v, err := p.ReadLine(prompt)
			if err != nil {
				return "", err
			}
			v = strings.TrimSpace(v)
			if v == "" {
				v = current
			}
			if v != "" || !required {
				return v, nil
			}
			fmt.Fprintln(out, "  (required)")
		}
	}
	yes := func(label string, def bool) (bool, error) {
		suffix := " [y/N]: "
		if def {
			suffix = " [Y/n]: "
		}
		v, err := p.ReadLine("  " + label + suffix)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		default:
			return false, nil
		}
	}
	choose := func(label string, options []string, def string) (string, error) {
		for {
			v, err := p.ReadLine(fmt.Sprintf("  %s (%s) [%s]: ", label, strings.Join(options, "/"), def))
			if err != nil {
				return "", err
			}
			v = strings.ToLower(strings.TrimSpace(v))
			if v == "" {
				return def, nil
			}
			for _, o := range options {
				if o == v {
					return v, nil
				}
			}
			fmt.Fprintf(out, "  choose one of: %s\n", strings.Join(options, ", "))
		}
	}

	var err error
	if cc.Name, err = ask("Name", cc.Name, true); err != nil {
		return err
	}
	if cc.Endpoint, err = ask("Endpoint", cc.Endpoint, true); err != nil {
		return err
	}
	if cc.Username, err = ask("Username", firstNonEmpty(cc.Username, "administrator@vsphere.local"), true); err != nil {
		return err
	}
	// A new context's password goes to the OS keyring by default. Where there
	// is none, find out now and let the operator choose where it comes from,
	// rather than taking the password, failing to store it, and downgrading
	// to a prompt on every run behind a warning. A password already piped in
	// with --password-stdin is a request for the keyring; leave it to Save.
	if f.credential == "" && !f.passwordStdin {
		if perr := a.Resolver().CheckAvailable(ctx, credentials.SchemeKeyring); perr != nil {
			if cc.Credential, err = chooseCredentialSource(ask, choose, out, perr); err != nil {
				return err
			}
		}
	}
	if f.transport == "" {
		if cc.Transport.Type, err = choose("Connection", []string{
			config.TransportDirect, config.TransportSOCKS5, config.TransportHTTPProxy, config.TransportHTTPSProxy,
		}, config.TransportDirect); err != nil {
			return err
		}
	}
	if cc.Transport.Type != config.TransportDirect {
		if cc.Transport.Address, err = ask("Proxy address", firstNonEmpty(cc.Transport.Address, "127.0.0.1:1080"), true); err != nil {
			return err
		}
		if cc.Transport.Type == config.TransportSOCKS5 && !f.remoteDNS {
			if cc.Transport.RemoteDNS, err = yes("Resolve DNS through the proxy?", true); err != nil {
				return err
			}
		}
		if cc.Transport.Username, err = ask("Proxy username (optional)", cc.Transport.Username, false); err != nil {
			return err
		}
	}
	if f.tlsMode == "" {
		if cc.TLS.Mode, err = choose("Certificate policy", []string{config.TLSSystem, config.TLSThumbprint, config.TLSInsecure}, config.TLSSystem); err != nil {
			return err
		}
	}
	if cc.Datacenter, err = ask("Default datacenter (optional)", cc.Datacenter, false); err != nil {
		return err
	}
	fmt.Fprintln(out)
	return nil
}

// chooseCredentialSource asks where a password comes from when the OS keyring
// cannot hold it. Every source that works without one is offered, and for
// env, file and exec only where the password lives is collected — never the
// password itself, which none of them stores. ask and choose are the wizard's
// own question helpers, so this step reads like the rest of it.
func chooseCredentialSource(
	ask func(label, current string, required bool) (string, error),
	choose func(label string, options []string, def string) (string, error),
	out io.Writer,
	reason error,
) (credentials.Ref, error) {
	fmt.Fprintf(out, "  No OS keyring is available here, so the password cannot be stored.\n")
	fmt.Fprintf(out, "  (%v)\n", reason)
	fmt.Fprintln(out, "  Where should the password come from?")
	fmt.Fprintln(out, "    prompt  ask on every run; nothing is stored")
	fmt.Fprintln(out, "    env     an environment variable")
	fmt.Fprintln(out, "    file    a file you protect, such as a mounted secret")
	fmt.Fprintln(out, "    exec    a helper program that prints it (vault, pass, op, ...)")
	scheme, err := choose("Password source", []string{
		credentials.SchemePrompt, credentials.SchemeEnv, credentials.SchemeFile, credentials.SchemeExec,
	}, credentials.SchemePrompt)
	if err != nil {
		return credentials.Ref{}, err
	}
	if scheme == credentials.SchemePrompt {
		return credentials.Ref{Scheme: credentials.SchemePrompt}, nil
	}
	label := map[string]string{
		credentials.SchemeEnv:  "Environment variable",
		credentials.SchemeFile: "Password file path",
		credentials.SchemeExec: "Helper program path",
	}[scheme]
	for {
		v, err := ask(label, "", true)
		if err != nil {
			return credentials.Ref{}, err
		}
		ref, err := credentials.ParseRef(scheme + ":" + v)
		if err != nil {
			fmt.Fprintf(out, "  %v\n", err)
			continue
		}
		return ref, nil
	}
}

// discoverThumbprint fetches the certificate a vCenter presents and, when a
// terminal is attached, shows it before pinning. Trust on first use is a
// decision the operator makes, not one the tool makes quietly.
func discoverThumbprint(ctx context.Context, a *App, cc *config.Context, interactive bool) error {
	sha256, sha1, subject, notAfter, err := vsphere.FetchThumbprint(ctx, cc, a.ConnectOptions())
	if err != nil {
		return fmt.Errorf("fetch certificate from %s: %w", cc.Endpoint, err)
	}
	out := a.errOut()
	fmt.Fprintf(out, "Certificate presented by %s\n", cc.Host())
	fl := newFields(out)
	fl.add("Subject", subject)
	fl.add("Expires", notAfter.Format(time.DateOnly))
	fl.add("SHA-256", sha256)
	fl.add("SHA-1", sha1)
	fl.flush()
	if interactive {
		v, err := a.Prompt().ReadLine("  Pin this certificate? [Y/n]: ")
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "y", "yes":
		default:
			return errors.New("certificate not pinned")
		}
	}
	cc.TLS.Thumbprint = sha256
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func newContextListCommand(a *App) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List configured contexts",
		Example: `  # Every context, with the current one marked
  vsfleet context list

  # As JSON
  vsfleet context list -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.Config()
			if err != nil {
				return err
			}
			if a.json() {
				return writeJSON(a.out(), cfg.Contexts)
			}
			if len(cfg.Contexts) == 0 {
				fmt.Fprintf(a.out(), "No contexts configured. Run \"vsfleet context add\".\n")
				return nil
			}
			t := newTable(a.out(), "", "NAME", "ENDPOINT", "USERNAME", "ROUTE", "TLS")
			for _, c := range cfg.Contexts {
				marker := " "
				if c.Name == cfg.CurrentContext {
					marker = "*"
				}
				t.row(marker, c.Name, c.Endpoint, c.Username, c.Transport.Describe(), c.TLS.Mode)
			}
			t.flush()
			return nil
		},
	}
}

func newContextUseCommand(a *App) *cobra.Command {
	return &cobra.Command{
		Use: "use <name>",
		Example: `  # Make prod the current context
  vsfleet context use prod`,
		Short: "Set the current context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.Config()
			if err != nil {
				return err
			}
			c, err := cfg.Context(args[0])
			if err != nil {
				return err
			}
			cfg.CurrentContext = c.Name
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(a.out(), "Current context is now %q\n", c.Name)
			return nil
		},
	}
}

func newContextShowCommand(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "Show one context in full",
		Example: `  # The current context
  vsfleet context show

  # A named context
  vsfleet context show prod

  # As JSON, to diff against expected configuration
  vsfleet context show prod -o json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			c, err := a.SingleContext(name)
			if err != nil {
				return err
			}
			if a.json() {
				return writeJSON(a.out(), c)
			}
			fmt.Fprintf(a.out(), "Context: %s\n", c.Name)
			f := newFields(a.out())
			f.add("Endpoint", c.Endpoint)
			f.add("Added from", c.Via)
			f.add("Username", c.Username)
			f.add("Credential", dash(c.Credential.String()))
			f.add("Datacenter", c.Datacenter)
			f.add("Route", c.Transport.Describe())
			if c.Transport.Username != "" {
				f.add("Proxy username", c.Transport.Username)
				f.add("Proxy credential", dash(c.Transport.Credential.String()))
			}
			f.add("TLS", c.TLS.Describe())
			f.add("Thumbprint", c.TLS.Thumbprint)
			f.flush()
			return nil
		},
	}
}

func newContextRemoveCommand(a *App) *cobra.Command {
	var alsoCredential bool
	cmd := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a context",
		Example: `  # Remove a context and invalidate its session
  vsfleet context remove lab`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.Config()
			if err != nil {
				return err
			}
			c, err := contextops.Remove(cfg, args[0])
			if err != nil {
				return err
			}
			if alsoCredential {
				if err := contextops.DeleteCredentials(cmd.Context(), a.Resolver(), c); err != nil {
					fmt.Fprintf(a.errOut(), "warning: could not remove stored password(s): %v\n", err)
				}
			}
			fmt.Fprintf(a.out(), "Removed context %q\n", c.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&alsoCredential, "with-credential", false, "also delete the stored password from the system keyring")
	return cmd
}

func newContextTestCommand(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "test [name]",
		Short: "Test the connection to a context",
		Example: `  # Test the current context
  vsfleet context test

  # Test a named context with a short timeout
  vsfleet context test prod --timeout 5s`,
		Long: `Test the connection to a context.

Every stage of the path is checked separately, because "cannot connect" is
not a diagnosis: a dead proxy, an expired password and a rotated certificate
all look the same from the outside.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			c, err := a.SingleContext(name)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			diag, client := vsphere.Diagnose(ctx, c, a.ConnectOptions())
			if client != nil {
				_ = client.Close(context.WithoutCancel(ctx))
			}
			if a.json() {
				if err := writeJSON(a.out(), diagnosisJSON(diag)); err != nil {
					return err
				}
				return diag.Err()
			}
			printContextTest(a, diag)
			return diag.Err()
		},
	}
}

// printContextTest renders the compact summary of a connection test.
func printContextTest(a *App, d *vsphere.Diagnosis) {
	out := a.out()
	fmt.Fprintf(out, "Context: %s\n", d.Context)
	f := newFields(out)
	f.add("Endpoint", d.Endpoint)
	f.add("Route", d.Route)
	f.add("DNS", dnsSummary(d))
	f.add("TLS", tlsSummary(d))
	f.add("Auth", stageSummary(d, "Authentication"))
	if d.About.Version != "" {
		f.add("vCenter", d.About.FullVersion())
	}
	if d.Latency > 0 {
		f.add("Latency", humanDuration(d.Latency))
	}
	f.flush()
	if d.OK() {
		fmt.Fprintln(out, "Connection successful.")
		return
	}
	fmt.Fprintln(out)
	printDiagnosis(a, d, false)
}

func stage(d *vsphere.Diagnosis, name string) *vsphere.Check {
	for i := range d.Checks {
		if d.Checks[i].Name == name {
			return &d.Checks[i]
		}
	}
	return nil
}

func stageSummary(d *vsphere.Diagnosis, name string) string {
	c := stage(d, name)
	switch {
	case c == nil:
		return ""
	case c.Status == vsphere.CheckPass:
		return "OK"
	case c.Status == vsphere.CheckSkip:
		return "not reached"
	default:
		return "failed"
	}
}

func dnsSummary(d *vsphere.Diagnosis) string {
	c := stage(d, "DNS resolution")
	if c == nil {
		return ""
	}
	if c.Status == vsphere.CheckSkip && c.Detail == "resolved at the proxy" {
		return "Remote"
	}
	if c.Status == vsphere.CheckPass {
		return "Local (" + c.Detail + ")"
	}
	return stageSummary(d, "DNS resolution")
}

func tlsSummary(d *vsphere.Diagnosis) string {
	c := stage(d, "TLS certificate")
	switch {
	case c == nil:
		return ""
	case c.Status == vsphere.CheckPass:
		return "Verified (" + d.TLS + ")"
	case c.Status == vsphere.CheckSkip:
		return c.Detail
	default:
		return "failed"
	}
}
