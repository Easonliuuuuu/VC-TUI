package cli

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/easonliuuuuu/vsfleet/internal/query"
	"github.com/easonliuuuuu/vsfleet/internal/session"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// contextFailure records that one vCenter could not answer. Listing commands
// print the rows they did get and report these separately.
type contextFailure struct {
	Context string
	Err     error
}

// gather queries every selected context concurrently and merges the results.
// A failure is attached to its context and never cancels the others.
func gather[T any](ctx context.Context, a *App, fn func(context.Context, *vsphere.Client) ([]T, error)) ([]T, []contextFailure, error) {
	contexts, err := a.Contexts()
	if err != nil {
		return nil, nil, err
	}
	mgr := a.Sessions()

	type result struct {
		items []T
		err   error
	}
	results := make([]result, len(contexts))

	var g errgroup.Group
	g.SetLimit(session.DefaultConcurrency)
	for i, cc := range contexts {
		g.Go(func() error {
			// One deadline covers connecting and the listing that follows: a
			// vCenter that connects quickly but then hangs enumerating must
			// still be cut off at --timeout, not left to run unbounded once
			// the connection itself succeeded.
			opCtx, cancel, tracker := mgr.Operation(ctx)
			defer cancel()
			s, err := mgr.Connect(opCtx, cc)
			if err != nil {
				results[i] = result{err: mgr.TimeoutError(err, tracker)}
				return nil
			}
			items, err := fn(opCtx, s.Client())
			results[i] = result{items: items, err: mgr.TimeoutError(err, tracker)}
			return nil
		})
	}
	_ = g.Wait()

	var (
		all      []T
		failures []contextFailure
	)
	for i, r := range results {
		if r.err != nil {
			failures = append(failures, contextFailure{Context: contexts[i].Name, Err: r.err})
			continue
		}
		all = append(all, r.items...)
	}
	if len(all) == 0 && len(failures) == len(contexts) && len(failures) > 0 {
		return nil, failures, fmt.Errorf("no context could be queried")
	}
	return all, failures, nil
}

// reportFailures prints the contexts that could not answer, after the rows
// that did. Partial results with a visible gap beat an empty screen.
func reportFailures(a *App, failures []contextFailure) {
	if len(failures) == 0 {
		return
	}
	fmt.Fprintln(a.errOut())
	for _, f := range failures {
		fmt.Fprintf(a.errOut(), "%s %s: %v\n", glyphFail, f.Context, f.Err)
	}
}

// multiContext reports whether output should carry a CONTEXT column.
func (a *App) multiContext() bool {
	if a.AllContexts || len(a.ContextNames) > 1 {
		return true
	}
	cfg, err := a.Config()
	if err != nil {
		return false
	}
	return len(a.ContextNames) == 0 && cfg.CurrentContext == "" && len(cfg.Contexts) > 1
}

// listFlags are shared by every inventory listing command.
type listFlags struct {
	filter   string
	where    []string
	wide     bool
	compiled query.Filter
}

func (f *listFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&f.filter, "filter", "f", "", "only show objects whose name contains this text")
	cmd.Flags().StringArrayVar(&f.where, "where", nil, "structured predicate (repeatable; predicates are ANDed)")
	cmd.Flags().BoolVar(&f.wide, "wide", false, "include tags and custom attributes")
}

func (f *listFlags) prepare(kinds ...vsphere.Kind) error {
	compiled, err := query.Parse(f.where, kinds)
	if err != nil {
		return fmt.Errorf("--where: %w", err)
	}
	f.compiled = compiled
	return nil
}

func (f *listFlags) matches(name string) bool {
	if f.filter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), strings.ToLower(f.filter))
}

func (f *listFlags) matchesObject(kind vsphere.Kind, object any, name string) bool {
	if !f.matches(name) {
		return false
	}
	if f.compiled.Empty() {
		return true
	}
	subject, err := query.SubjectFromObject(kind, object)
	return err == nil && f.compiled.Match(subject)
}

func metadataTags(m vsphere.Metadata) string {
	parts := make([]string, 0, len(m.Tags))
	for _, tag := range m.Tags {
		label := tag.Name
		if tag.Category != "" {
			label = tag.Category + "/" + label
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ",")
}

func metadataAttributes(m vsphere.Metadata) string {
	parts := make([]string, 0, len(m.CustomAttributes))
	for _, attr := range m.CustomAttributes {
		parts = append(parts, attr.Name+"="+attr.Value)
	}
	return strings.Join(parts, ",")
}

func metadataFromSubject(s query.Subject) vsphere.Metadata {
	return vsphere.Metadata{Tags: s.Tags, CustomAttributes: s.CustomAttributes, TagsStatus: s.TagsStatus, CustomAttributesStatus: s.CustomStatus}
}

// metadataWarning describes a metadata source that was not read completely.
// A source that was never part of the evidence (empty status) is silent here;
// stored-capture readers report it as not recorded instead.
func metadataWarning(source, status, message string) (string, bool) {
	if status == "" || status == vsphere.MetadataAvailable || status == vsphere.MetadataNotApplicable {
		return "", false
	}
	label := strings.ReplaceAll(status, "_", " ")
	if message == "" {
		return fmt.Sprintf("metadata %s %s", source, label), true
	}
	return fmt.Sprintf("metadata %s %s: %s", source, label, message), true
}

func reportMetadataWarnings(a *App, values any) {
	rv := reflect.ValueOf(values)
	if rv.Kind() != reflect.Slice {
		return
	}
	seen := map[string]bool{}
	for i := 0; i < rv.Len(); i++ {
		m, ok := objectMetadata(rv.Index(i).Interface())
		if !ok {
			continue
		}
		for _, item := range [][3]string{{"tags", m.TagsStatus, m.TagsError}, {"custom attributes", m.CustomAttributesStatus, m.CustomAttributesError}} {
			source, status, message := item[0], item[1], item[2]
			if line, ok := metadataWarning(source, status, message); ok && !seen[line] {
				fmt.Fprintf(a.errOut(), "%s %s\n", glyphFail, line)
				seen[line] = true
			}
		}
	}
}

func objectMetadata(value any) (vsphere.Metadata, bool) {
	switch v := value.(type) {
	case vsphere.VM:
		return v.Metadata, true
	case vsphere.Host:
		return v.Metadata, true
	case vsphere.Cluster:
		return v.Metadata, true
	case vsphere.VApp:
		return v.Metadata, true
	case vsphere.Datastore:
		return v.Metadata, true
	case vsphere.Network:
		return v.Metadata, true
	case vsphere.ResourcePool:
		return v.Metadata, true
	case vsphere.DVSwitch:
		return v.Metadata, true
	default:
		return vsphere.Metadata{}, false
	}
}

// newInventoryCommands builds one command group per resource kind. They all
// share the same shape so that "vsfleet <kind> list" is predictable.
func newInventoryCommands(a *App) []*cobra.Command {
	vm := group("vm", []string{"vms", "virtualmachine"}, "Virtual machines", newVMListCommand(a))
	vm.AddCommand(newVMShowCommand(a))
	vm.AddCommand(newVMHistoryCommand(a))
	vm.AddCommand(newVMEventsCommand(a))
	vm.AddCommand(newVMDecommissionCheckCommand(a))
	tmpl := group("template", []string{"templates", "tpl"}, "VM templates", newTemplateListCommand(a))
	tmpl.AddCommand(newTemplateShowCommand(a))
	host := group("host", []string{"hosts", "esxi"}, "ESXi hosts", newHostListCommand(a))
	host.AddCommand(newHostShowCommand(a))
	host.AddCommand(hostSubgroup("hba", []string{"hbas"}, "Host bus adapters", "host hba", newHostHBAListCommand(a)))
	host.AddCommand(hostSubgroup("multipath", []string{"multipaths", "mp"}, "Host/LUN multipath aggregates", "host multipath", newHostMultipathListCommand(a)))
	host.AddCommand(hostSubgroup("pnic", []string{"pnics"}, "Physical NICs", "host pnic", newHostPNICListCommand(a)))
	host.AddCommand(hostSubgroup("vswitch", []string{"vswitches", "vss"}, "Standard virtual switches", "host vswitch", newHostVSwitchListCommand(a)))
	host.AddCommand(hostSubgroup("portgroup", []string{"portgroups", "pg"}, "Standard-switch port groups", "host portgroup", newHostPortGroupListCommand(a)))
	host.AddCommand(hostSubgroup("vmkernel", []string{"vmkernels", "vmk", "vmks"}, "VMkernel adapters", "host vmkernel", newHostVMKernelListCommand(a)))
	cluster := group("cluster", []string{"clusters"}, "Compute clusters", newClusterListCommand(a))
	cluster.AddCommand(newClusterShowCommand(a))
	vapp := group("vapp", []string{"vapps", "virtualapp"}, "vSphere vApps", newVAppListCommand(a))
	vapp.AddCommand(newVAppShowCommand(a))
	netw := group("network", []string{"networks", "portgroup"}, "Networks and port groups", newNetworkListCommand(a))
	netw.AddCommand(newNetworkShowCommand(a))
	netw.AddCommand(newNetworkCompareCommand(a))
	ds := group("datastore", []string{"datastores", "ds"}, "Datastores", newDatastoreListCommand(a))
	ds.AddCommand(newDatastoreShowCommand(a))
	ds.AddCommand(newDatastoreFilesCommand(a))
	dvswitch := group("dvswitch", []string{"dvswitches", "vds"}, "Distributed virtual switches", newDVSwitchListCommand(a))
	dvportgroup := group("dvportgroup", []string{"dvportgroups", "dvpg"}, "Distributed port groups", newDVPortGroupListCommand(a))
	resourcepool := group("resourcepool", []string{"resourcepools", "rp"}, "Resource pools", newResourcePoolListCommand(a))
	snapshot := group("snapshot", []string{"snapshots", "snap"}, "VM snapshots", newSnapshotListCommand(a))
	return []*cobra.Command{
		vm,
		tmpl,
		host,
		cluster,
		vapp,
		ds,
		netw,
		dvswitch,
		dvportgroup,
		resourcepool,
		snapshot,
	}
}

func newVMHistoryCommand(a *App) *cobra.Command {
	var allObservations, includeRuntime bool
	cmd := &cobra.Command{Use: "history NAME_OR_UUID", Short: "Show a VM's stored assessment timeline", Long: strings.TrimSpace(`
Show how one VM changed across every stored assessment.

The VM may be named or given by instance UUID; a UUID is the stable choice,
because it survives a rename. Reads stored evidence only and never contacts a
vCenter.`), Example: `  # Every recorded change for a VM
  vsfleet vm history web-01

  # By instance UUID, which survives a rename
  vsfleet vm history 5029c07a-1b3e-4d2f-9c11-8a7e6f0d4b52

  # Include unchanged captures, and volatile runtime fields
  vsfleet vm history web-01 --all-observations --include-runtime`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := a.History()
		if err != nil {
			return err
		}
		events, err := s.TimelineForContexts(cmd.Context(), args[0], a.StoredContextNames(), allObservations, includeRuntime)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return fmt.Errorf("no stored VM history matched %q", args[0])
		}
		if a.json() {
			return writeJSON(a.out(), events)
		}
		t := newTable(a.out(), "RUN", "DATE", "EVENT", "CONTEXT", "VM", "DETAIL")
		for _, e := range events {
			detail := ""
			if len(e.Changes) > 0 {
				parts := make([]string, len(e.Changes))
				for i, f := range e.Changes {
					parts[i] = f.Field + ":" + f.Before + "→" + f.After
				}
				detail = strings.Join(parts, " ")
			}
			if detail == "" && e.Observation != nil {
				detail = dash(e.Observation.VM.Host)
			}
			t.row(strconv.FormatInt(e.Run.ID, 10), e.Run.StartedAt.Local().Format("2006-01-02 15:04"), e.Kind, e.Context, e.Name, detail)
		}
		t.flush()
		return nil
	}}
	cmd.Flags().BoolVar(&allObservations, "all-observations", false, "include unchanged observations")
	cmd.Flags().BoolVar(&includeRuntime, "include-runtime", false, "include volatile runtime fields in modified events")
	return cmd
}

func group(name string, aliases []string, short string, sub *cobra.Command) *cobra.Command {
	cmd := requireSubcommand(&cobra.Command{
		Use:     name,
		Aliases: aliases,
		Short:   short,
		Example: fmt.Sprintf(`  # List on the current context
  vsfleet %[1]s list

  # List across every configured vCenter at once
  vsfleet %[1]s list --all-contexts

  # Narrow by name, as JSON
  vsfleet %[1]s list --context prod --filter web -o json`, name),
	})
	// The list subcommand is built by a shared helper that does not know its
	// kind, so the kind-specific examples are filled in here, where it does.
	sub.Example = fmt.Sprintf(`  # List on the current context
  vsfleet %[1]s list

  # List across every configured vCenter at once
  vsfleet %[1]s list --all-contexts

  # Narrow by name
  vsfleet %[1]s list --context prod --filter web

  # As JSON, for a script
  vsfleet %[1]s list --all-contexts -o json`, name)
	cmd.AddCommand(sub)
	return cmd
}

func listCommand(a *App, short string, run func(*cobra.Command, *listFlags) error) *cobra.Command {
	var f listFlags
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   short,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd, &f)
		},
	}
	f.register(cmd)
	return cmd
}

func newVMListCommand(a *App) *cobra.Command {
	return listCommand(a, "List virtual machines", func(cmd *cobra.Command, f *listFlags) error {
		if err := f.prepare(vsphere.KindVM); err != nil {
			return err
		}
		vms, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.VM, error) {
			return c.ListVMs(ctx)
		})
		if err != nil {
			reportFailures(a, failures)
			return err
		}
		reportMetadataWarnings(a, vms)
		vms = filterSlice(vms, func(v vsphere.VM) bool { return f.matchesObject(vsphere.KindVM, v, v.Name) })
		if a.json() {
			defer reportFailures(a, failures)
			return writeJSON(a.out(), vms)
		}
		headers := []string{"NAME", "STATE", "CPU", "RAM", "HOST", "IP"}
		if f.wide {
			headers = append(headers, "TAGS", "CUSTOM ATTRIBUTES")
		}
		multi := a.multiContext()
		if multi {
			headers = append([]string{"CONTEXT"}, headers...)
		}
		t := newTable(a.out(), headers...)
		for _, v := range vms {
			row := []string{v.Name, v.PowerState, i32toa(v.CPU), humanMB(v.MemoryMB), dash(v.Host), dash(v.IPAddress)}
			if f.wide {
				row = append(row, dash(metadataTags(v.Metadata)), dash(metadataAttributes(v.Metadata)))
			}
			if multi {
				row = append([]string{v.Context}, row...)
			}
			t.row(row...)
		}
		t.flush()
		reportFailures(a, failures)
		return nil
	})
}

func newTemplateListCommand(a *App) *cobra.Command {
	return listCommand(a, "List VM templates", func(cmd *cobra.Command, f *listFlags) error {
		if err := f.prepare(vsphere.KindTemplate); err != nil {
			return err
		}
		tmpl, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.VM, error) {
			return c.ListTemplates(ctx)
		})
		if err != nil {
			reportFailures(a, failures)
			return err
		}
		reportMetadataWarnings(a, tmpl)
		tmpl = filterSlice(tmpl, func(v vsphere.VM) bool { return f.matchesObject(vsphere.KindTemplate, v, v.Name) })
		if a.json() {
			defer reportFailures(a, failures)
			return writeJSON(a.out(), tmpl)
		}
		headers := []string{"NAME", "OS", "CPU", "RAM", "DATASTORE", "FOLDER"}
		if f.wide {
			headers = append(headers, "TAGS", "CUSTOM ATTRIBUTES")
		}
		multi := a.multiContext()
		if multi {
			headers = append([]string{"CONTEXT"}, headers...)
		}
		t := newTable(a.out(), headers...)
		for _, v := range tmpl {
			row := []string{v.Name, dash(v.GuestOS), i32toa(v.CPU), humanMB(v.MemoryMB), dash(strings.Join(v.Datastores, ",")), dash(v.Folder)}
			if f.wide {
				row = append(row, dash(metadataTags(v.Metadata)), dash(metadataAttributes(v.Metadata)))
			}
			if multi {
				row = append([]string{v.Context}, row...)
			}
			t.row(row...)
		}
		t.flush()
		reportFailures(a, failures)
		return nil
	})
}

func newHostListCommand(a *App) *cobra.Command {
	return listCommand(a, "List ESXi hosts", func(cmd *cobra.Command, f *listFlags) error {
		if err := f.prepare(vsphere.KindHost); err != nil {
			return err
		}
		hosts, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.Host, error) {
			return c.ListHosts(ctx)
		})
		if err != nil {
			reportFailures(a, failures)
			return err
		}
		reportMetadataWarnings(a, hosts)
		hosts = filterSlice(hosts, func(h vsphere.Host) bool { return f.matchesObject(vsphere.KindHost, h, h.Name) })
		if a.json() {
			defer reportFailures(a, failures)
			return writeJSON(a.out(), hosts)
		}
		headers := []string{"NAME", "CLUSTER", "STATE", "CPU", "RAM", "VMS", "VERSION"}
		if f.wide {
			headers = append(headers, "TAGS", "CUSTOM ATTRIBUTES")
		}
		multi := a.multiContext()
		if multi {
			headers = append([]string{"CONTEXT"}, headers...)
		}
		t := newTable(a.out(), headers...)
		for _, h := range hosts {
			state := h.ConnectionState
			if h.InMaintenance {
				state += " (maintenance)"
			}
			row := []string{h.Name, dash(h.Cluster), state, i32toa(h.CPUCores) + " cores", humanMB(h.MemoryMB), itoa(h.VMCount), dash(h.Version)}
			if f.wide {
				row = append(row, dash(metadataTags(h.Metadata)), dash(metadataAttributes(h.Metadata)))
			}
			if multi {
				row = append([]string{h.Context}, row...)
			}
			t.row(row...)
		}
		t.flush()
		reportFailures(a, failures)
		return nil
	})
}

func newClusterListCommand(a *App) *cobra.Command {
	return listCommand(a, "List compute clusters", func(cmd *cobra.Command, f *listFlags) error {
		if err := f.prepare(vsphere.KindCluster); err != nil {
			return err
		}
		clusters, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.Cluster, error) {
			return c.ListClusters(ctx)
		})
		if err != nil {
			reportFailures(a, failures)
			return err
		}
		reportMetadataWarnings(a, clusters)
		clusters = filterSlice(clusters, func(c vsphere.Cluster) bool { return f.matchesObject(vsphere.KindCluster, c, c.Name) })
		if a.json() {
			defer reportFailures(a, failures)
			return writeJSON(a.out(), clusters)
		}
		headers := []string{"NAME", "DATACENTER", "HOSTS", "CPU", "RAM", "DRS", "HA"}
		if f.wide {
			headers = append(headers, "TAGS", "CUSTOM ATTRIBUTES")
		}
		multi := a.multiContext()
		if multi {
			headers = append([]string{"CONTEXT"}, headers...)
		}
		t := newTable(a.out(), headers...)
		for _, c := range clusters {
			name := c.Name
			if c.Standalone {
				name += " (standalone)"
			}
			row := []string{name, dash(c.Datacenter), itoa(c.Hosts), dash(mhz(c.TotalCPUMHz)), humanMB(c.TotalMemoryMB), onOff(c.DRSEnabled), onOff(c.HAEnabled)}
			if f.wide {
				row = append(row, dash(metadataTags(c.Metadata)), dash(metadataAttributes(c.Metadata)))
			}
			if multi {
				row = append([]string{c.Context}, row...)
			}
			t.row(row...)
		}
		t.flush()
		reportFailures(a, failures)
		return nil
	})
}

func newVAppListCommand(a *App) *cobra.Command {
	return listCommand(a, "List vSphere vApps", func(cmd *cobra.Command, f *listFlags) error {
		if err := f.prepare(vsphere.KindVApp); err != nil {
			return err
		}
		vapps, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.VApp, error) {
			return c.ListVApps(ctx)
		})
		if err != nil {
			reportFailures(a, failures)
			return err
		}
		reportMetadataWarnings(a, vapps)
		vapps = filterSlice(vapps, func(v vsphere.VApp) bool { return f.matchesObject(vsphere.KindVApp, v, v.Name) })
		if a.json() {
			defer reportFailures(a, failures)
			return writeJSON(a.out(), vapps)
		}
		headers := []string{"NAME", "STATUS", "VMS", "CHILDREN", "DATACENTER", "PATH"}
		if f.wide {
			headers = append(headers, "TAGS", "CUSTOM ATTRIBUTES")
		}
		multi := a.multiContext()
		if multi {
			headers = append([]string{"CONTEXT"}, headers...)
		}
		t := newTable(a.out(), headers...)
		for _, v := range vapps {
			children := fmt.Sprintf("%d vApp / %d pool", v.ChildVAppCount, v.ChildResourcePoolCount)
			row := []string{v.Name, dash(v.Status), itoa(v.DirectVMCount), children, dash(v.Datacenter), dash(v.Path)}
			if f.wide {
				row = append(row, dash(metadataTags(v.Metadata)), dash(metadataAttributes(v.Metadata)))
			}
			if multi {
				row = append([]string{v.Context}, row...)
			}
			t.row(row...)
		}
		t.flush()
		reportFailures(a, failures)
		return nil
	})
}

func newDatastoreListCommand(a *App) *cobra.Command {
	return listCommand(a, "List datastores", func(cmd *cobra.Command, f *listFlags) error {
		if err := f.prepare(vsphere.KindDatastore); err != nil {
			return err
		}
		stores, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.Datastore, error) {
			return c.ListDatastores(ctx)
		})
		if err != nil {
			reportFailures(a, failures)
			return err
		}
		reportMetadataWarnings(a, stores)
		stores = filterSlice(stores, func(d vsphere.Datastore) bool { return f.matchesObject(vsphere.KindDatastore, d, d.Name) })
		if a.json() {
			defer reportFailures(a, failures)
			return writeJSON(a.out(), stores)
		}
		headers := []string{"NAME", "TYPE", "CAPACITY", "FREE", "USED", "DATACENTER"}
		if f.wide {
			headers = append(headers, "TAGS", "CUSTOM ATTRIBUTES")
		}
		multi := a.multiContext()
		if multi {
			headers = append([]string{"CONTEXT"}, headers...)
		}
		t := newTable(a.out(), headers...)
		for _, d := range stores {
			used := "-"
			if d.CapacityBytes > 0 {
				used = fmt.Sprintf("%.0f%%", d.UsedPercent())
			}
			row := []string{d.Name, dash(d.Type), humanBytes(d.CapacityBytes), humanBytes(d.FreeBytes), used, dash(d.Datacenter)}
			if f.wide {
				row = append(row, dash(metadataTags(d.Metadata)), dash(metadataAttributes(d.Metadata)))
			}
			if multi {
				row = append([]string{d.Context}, row...)
			}
			t.row(row...)
		}
		t.flush()
		reportFailures(a, failures)
		return nil
	})
}

func newNetworkListCommand(a *App) *cobra.Command {
	return listCommand(a, "List networks and port groups", func(cmd *cobra.Command, f *listFlags) error {
		if err := f.prepare(vsphere.KindNetwork); err != nil {
			return err
		}
		nets, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.Network, error) {
			return c.ListNetworks(ctx)
		})
		if err != nil {
			reportFailures(a, failures)
			return err
		}
		reportMetadataWarnings(a, nets)
		nets = filterSlice(nets, func(n vsphere.Network) bool { return f.matchesObject(vsphere.KindNetwork, n, n.Name) })
		if a.json() {
			defer reportFailures(a, failures)
			return writeJSON(a.out(), nets)
		}
		headers := []string{"NAME", "TYPE", "SWITCH", "VLAN", "DATACENTER", "ACCESSIBLE"}
		if f.wide {
			headers = append(headers, "TAGS", "CUSTOM ATTRIBUTES")
		}
		multi := a.multiContext()
		if multi {
			headers = append([]string{"CONTEXT"}, headers...)
		}
		t := newTable(a.out(), headers...)
		for _, n := range nets {
			row := []string{n.Name, n.Type, dash(n.Switch), dash(n.VLAN), dash(n.Datacenter), yesNo(n.Accessible)}
			if f.wide {
				row = append(row, dash(metadataTags(n.Metadata)), dash(metadataAttributes(n.Metadata)))
			}
			if multi {
				row = append([]string{n.Context}, row...)
			}
			t.row(row...)
		}
		t.flush()
		reportFailures(a, failures)
		return nil
	})
}

func filterSlice[T any](in []T, keep func(T) bool) []T {
	out := in[:0]
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

func mhz(v int64) string {
	switch {
	case v <= 0:
		return ""
	case v >= 1000:
		return fmt.Sprintf("%.1f GHz", float64(v)/1000)
	default:
		return fmt.Sprintf("%d MHz", v)
	}
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
