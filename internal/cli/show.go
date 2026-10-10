package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/humanize"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// filterByName narrows items to those whose name (case-insensitively)
// matches query. It is the first step of resolving "show NAME" and every
// "--host"/"--switch"/"--vm" style narrowing flag: the second step,
// cardinality, is exactlyOne.
func filterByName[T any](items []T, query string, nameOf func(T) string) []T {
	var out []T
	for _, item := range items {
		if strings.EqualFold(nameOf(item), query) {
			out = append(out, item)
		}
	}
	return out
}

// noMatchError is the error exactlyOne returns when nothing matched, so a
// caller with another place to look can tell it from an ambiguous match.
type noMatchError struct{ kind, query string }

func (e *noMatchError) Error() string { return fmt.Sprintf("no %s matched %q", e.kind, e.query) }

// exactlyOne requires matches to contain exactly one item, printing
// candidates and returning an actionable error otherwise. kind and query
// name the object being resolved (e.g. "host", "esxi-01") for the error
// text; contextOf/pathOf/nameOf describe each candidate for the table
// printed when the match is ambiguous.
func exactlyOne[T any](a *App, kind, query string, matches []T, contextOf, pathOf, nameOf func(T) string) (T, error) {
	var zero T
	switch len(matches) {
	case 0:
		return zero, &noMatchError{kind: kind, query: query}
	case 1:
		return matches[0], nil
	default:
		printCandidates(a.errOut(), matches, contextOf, pathOf, nameOf)
		return zero, fmt.Errorf("%s %q is ambiguous (%d matches); use --context to narrow", kind, query, len(matches))
	}
}

func printCandidates[T any](out io.Writer, matches []T, contextOf, pathOf, nameOf func(T) string) {
	fmt.Fprintln(out, "Candidates:")
	t := newTable(out, "CONTEXT", "PATH", "NAME")
	for _, m := range matches {
		t.row(contextOf(m), dash(pathOf(m)), nameOf(m))
	}
	t.flush()
}

func vmContextOf(v vsphere.VM) string { return v.Context }
func vmPathOf(v vsphere.VM) string    { return v.Path }
func vmNameOf(v vsphere.VM) string    { return v.Name }

// resolveVM finds exactly one VM (or template) matching query by name,
// instance UUID or BIOS UUID, mirroring "vm history NAME_OR_UUID".
func resolveVM(a *App, kind, query string, vms []vsphere.VM) (vsphere.VM, error) {
	matches := filterSlice(append([]vsphere.VM(nil), vms...), func(v vsphere.VM) bool {
		return strings.EqualFold(v.Name, query) ||
			(v.InstanceUUID != "" && v.InstanceUUID == query) ||
			(v.BIOSUUID != "" && v.BIOSUUID == query)
	})
	return exactlyOne(a, kind, query, matches, vmContextOf, vmPathOf, vmNameOf)
}

func newVMShowCommand(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show NAME_OR_UUID",
		Short: "Show one virtual machine's detailed evidence",
		Long: strings.TrimSpace(`
Show one virtual machine's full collected evidence: identity, hardware,
guest state and every disk, NIC, CD-ROM, USB device and snapshot vsfleet
collected for it.

The VM may be named or given by instance UUID or BIOS UUID; a UUID is the
stable choice when a name could collide.`),
		Example: `  # Show a VM on the current context
  vsfleet vm show web-01

  # Narrow to one vCenter when the name is ambiguous
  vsfleet vm show web-01 --context prod

  # By instance UUID, which survives a rename
  vsfleet vm show 5029c07a-1b3e-4d2f-9c11-8a7e6f0d4b52

  # As JSON, for a script
  vsfleet vm show web-01 -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vms, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.VM, error) {
				return c.ListVMs(ctx)
			})
			if err != nil {
				reportFailures(a, failures)
				return err
			}
			vm, rerr := resolveVM(a, "vm", args[0], vms)
			if rerr != nil {
				reportFailures(a, failures)
				return rerr
			}
			if a.json() {
				defer reportFailures(a, failures)
				return writeJSON(a.out(), vm)
			}
			printVMDetail(a.out(), vm)
			reportFailures(a, failures)
			return nil
		},
	}
}

func newTemplateShowCommand(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show NAME_OR_UUID",
		Short: "Show one VM template's detailed evidence",
		Long: strings.TrimSpace(`
Show one template's full collected evidence: identity, hardware and every
disk, NIC, CD-ROM and USB device vsfleet collected for it.`),
		Example: `  # Show a template on the current context
  vsfleet template show ubuntu-2404

  # Narrow to one vCenter when the name is ambiguous
  vsfleet template show ubuntu-2404 --context prod

  # As JSON, for a script
  vsfleet template show ubuntu-2404 -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tmpl, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.VM, error) {
				return c.ListTemplates(ctx)
			})
			if err != nil {
				reportFailures(a, failures)
				return err
			}
			vm, rerr := resolveVM(a, "template", args[0], tmpl)
			if rerr != nil {
				reportFailures(a, failures)
				return rerr
			}
			if a.json() {
				defer reportFailures(a, failures)
				return writeJSON(a.out(), vm)
			}
			printVMDetail(a.out(), vm)
			reportFailures(a, failures)
			return nil
		},
	}
}

// printVMDetail renders a VM or template's full evidence. It is shared by
// "vm show" and "template show" since both read the same vsphere.VM shape.
func printVMDetail(out io.Writer, v vsphere.VM) {
	f := newFields(out)
	f.add("Name", v.Name)
	f.add("Context", v.Context)
	f.add("Path", v.Path)
	f.add("ID", v.ID)
	f.add("Instance UUID", v.InstanceUUID)
	f.add("BIOS UUID", v.BIOSUUID)
	f.add("Power state", v.PowerState)
	f.add("Connection state", v.ConnectionState)
	f.add("Guest OS", v.GuestOS)
	f.add("Guest state", v.GuestState)
	f.add("CPU", i32toa(v.CPU))
	f.add("Memory", humanMB(v.MemoryMB))
	f.add("IP address", v.IPAddress)
	f.add("DNS name", v.GuestHostName)
	f.add("Host", v.Host)
	f.add("Cluster", v.Cluster)
	f.add("Folder", v.Folder)
	f.add("Datastores", strings.Join(v.Datastores, ", "))
	if v.StorageGB > 0 {
		f.add("Storage", fmt.Sprintf("%.1f GB", v.StorageGB))
	}
	f.add("Tools state", v.ToolsState)
	f.add("Tools version", v.ToolsVersion)
	f.add("Annotation", v.Annotation)
	f.add("Firmware", v.Firmware)
	if v.SecureBootEnabled != nil {
		f.add("Secure boot", yesNo(*v.SecureBootEnabled))
	}
	if v.Metadata.TagsStatus == "available" {
		f.add("Tags", dash(metadataTags(v.Metadata)))
	}
	if v.Metadata.CustomAttributesStatus == "available" {
		f.add("Custom attributes", dash(metadataAttributes(v.Metadata)))
	}
	f.flush()

	if len(v.Disks) > 0 {
		fmt.Fprintln(out, "\nDisks:")
		t := newTable(out, "LABEL", "CAPACITY", "CONTROLLER", "BACKING", "THIN")
		for _, d := range v.Disks {
			t.row(d.Label, humanBytes(d.CapacityBytes), dash(d.ControllerLabel), dash(d.BackingType), dashBoolPtr(d.ThinProvisioned))
		}
		t.flush()
	}
	if len(v.NICs) > 0 {
		fmt.Fprintln(out, "\nNICs:")
		t := newTable(out, "LABEL", "ADAPTER", "NETWORK", "MAC", "CONNECTED")
		for _, n := range v.NICs {
			t.row(n.Label, dash(n.Adapter), dash(n.Network), dash(n.MACAddress), dashBoolPtr(n.Connected))
		}
		t.flush()
	}
	if len(v.Snapshots) > 0 {
		fmt.Fprintln(out, "\nSnapshots:")
		t := newTable(out, "NAME", "CREATED", "POWER STATE", "CURRENT")
		for _, s := range v.Snapshots {
			t.row(s.Name, formatSnapshotTime(s.CreateTime), dash(s.PowerState), yesNo(s.Current))
		}
		t.flush()
	}
}

func newHostShowCommand(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show NAME",
		Short: "Show one ESXi host's detailed evidence",
		Long: strings.TrimSpace(`
Show one host's identity, hardware, connection state and capacity.

Storage and network subresources (HBAs, physical NICs, standard switches,
port groups, VMkernel adapters, multipath LUNs) are their own scriptable
commands rather than part of this output, e.g.
"vsfleet host hba list --host NAME", so that showing a host never costs more
than the host listing already does.`),
		Example: `  # Show a host on the current context
  vsfleet host show esxi-01

  # Narrow to one vCenter when the name is ambiguous
  vsfleet host show esxi-01 --context prod

  # As JSON, for a script
  vsfleet host show esxi-01 -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.Host, error) {
				return c.ListHosts(ctx)
			})
			if err != nil {
				reportFailures(a, failures)
				return err
			}
			matches := filterByName(hosts, args[0], func(h vsphere.Host) string { return h.Name })
			host, rerr := exactlyOne(a, "host", args[0], matches,
				func(h vsphere.Host) string { return h.Context },
				func(h vsphere.Host) string { return h.Path },
				func(h vsphere.Host) string { return h.Name })
			if rerr != nil {
				reportFailures(a, failures)
				return rerr
			}
			if a.json() {
				defer reportFailures(a, failures)
				return writeJSON(a.out(), host)
			}
			printHostDetail(a.out(), host)
			reportFailures(a, failures)
			return nil
		},
	}
}

func printHostDetail(out io.Writer, h vsphere.Host) {
	f := newFields(out)
	f.add("Name", h.Name)
	f.add("Context", h.Context)
	f.add("Path", h.Path)
	f.add("ID", h.ID)
	f.add("Cluster", h.Cluster)
	state := h.ConnectionState
	if h.InMaintenance {
		state += " (maintenance)"
	}
	f.add("Power state", h.PowerState)
	f.add("Connection state", state)
	f.add("Vendor", h.Vendor)
	f.add("Model", h.Model)
	f.add("Version", h.Version)
	f.add("Build", h.Build)
	f.add("CPU", fmt.Sprintf("%d cores / %d threads @ %s", h.CPUCores, h.CPUThreads, dash(mhz(int64(h.CPUMHz)))))
	f.add("Total CPU", dash(mhz(h.TotalCPU())))
	f.add("CPU usage", dash(mhz(h.CPUUsageMHz)))
	f.add("Memory", humanMB(h.MemoryMB))
	f.add("Memory usage", humanMB(h.MemoryUsageMB))
	f.add("VMs", itoa(h.VMCount))
	if h.Metadata.TagsStatus == "available" {
		f.add("Tags", dash(metadataTags(h.Metadata)))
	}
	if h.Metadata.CustomAttributesStatus == "available" {
		f.add("Custom attributes", dash(metadataAttributes(h.Metadata)))
	}
	f.flush()
	fmt.Fprintf(out, "\nRun \"vsfleet host hba|pnic|vswitch|portgroup|vmkernel|multipath list --host %s\" for storage and network subresource evidence.\n", h.Name)
}

func newClusterShowCommand(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show NAME",
		Short: "Show one compute cluster's detailed evidence",
		Example: `  # Show a cluster on the current context
  vsfleet cluster show prod-cluster

  # Narrow to one vCenter when the name is ambiguous
  vsfleet cluster show prod-cluster --context prod

  # As JSON, for a script
  vsfleet cluster show prod-cluster -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clusters, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.Cluster, error) {
				return c.ListClusters(ctx)
			})
			if err != nil {
				reportFailures(a, failures)
				return err
			}
			matches := filterByName(clusters, args[0], func(c vsphere.Cluster) string { return c.Name })
			cluster, rerr := exactlyOne(a, "cluster", args[0], matches,
				func(c vsphere.Cluster) string { return c.Context },
				func(c vsphere.Cluster) string { return c.Path },
				func(c vsphere.Cluster) string { return c.Name })
			if rerr != nil {
				reportFailures(a, failures)
				return rerr
			}
			if a.json() {
				defer reportFailures(a, failures)
				return writeJSON(a.out(), cluster)
			}
			printClusterDetail(a.out(), cluster)
			reportFailures(a, failures)
			return nil
		},
	}
}

func printClusterDetail(out io.Writer, c vsphere.Cluster) {
	f := newFields(out)
	f.add("Name", c.Name)
	f.add("Context", c.Context)
	f.add("Path", c.Path)
	f.add("ID", c.ID)
	f.add("Datacenter", c.Datacenter)
	f.add("Standalone", yesNo(c.Standalone))
	f.add("Hosts", itoa(c.Hosts))
	f.add("Effective hosts", itoa(c.EffectiveHost))
	f.add("CPU cores", i32toa(c.CPUCores))
	f.add("Total CPU", dash(mhz(c.TotalCPUMHz)))
	f.add("Total memory", humanMB(c.TotalMemoryMB))
	f.add("DRS", onOff(c.DRSEnabled))
	f.add("HA", onOff(c.HAEnabled))
	if c.Metadata.TagsStatus == "available" {
		f.add("Tags", dash(metadataTags(c.Metadata)))
	}
	if c.Metadata.CustomAttributesStatus == "available" {
		f.add("Custom attributes", dash(metadataAttributes(c.Metadata)))
	}
	f.flush()
}

func newVAppShowCommand(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show NAME",
		Short: "Show one vApp's detailed evidence",
		Example: `  # Show a vApp on the current context
  vsfleet vapp show payments

  # Narrow to one vCenter when the name is ambiguous
  vsfleet vapp show payments --context prod

  # As JSON, for a script
  vsfleet vapp show payments -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vapps, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.VApp, error) {
				return c.ListVApps(ctx)
			})
			if err != nil {
				reportFailures(a, failures)
				return err
			}
			matches := filterByName(vapps, args[0], func(v vsphere.VApp) string { return v.Name })
			vapp, rerr := exactlyOne(a, "vapp", args[0], matches,
				func(v vsphere.VApp) string { return v.Context },
				func(v vsphere.VApp) string { return v.Path },
				func(v vsphere.VApp) string { return v.Name })
			if rerr != nil {
				reportFailures(a, failures)
				return rerr
			}
			if a.json() {
				defer reportFailures(a, failures)
				return writeJSON(a.out(), vapp)
			}
			printVAppDetail(a.out(), vapp)
			reportFailures(a, failures)
			return nil
		},
	}
}

func printVAppDetail(out io.Writer, v vsphere.VApp) {
	f := newFields(out)
	f.add("Name", v.Name)
	f.add("Context", v.Context)
	f.add("Path", v.Path)
	f.add("ID", v.ID)
	f.add("Status", v.Status)
	f.add("Datacenter", v.Datacenter)
	f.add("Cluster", v.Cluster)
	f.add("Compute resource", v.ComputeResource)
	f.add("Parent container", v.ParentContainer)
	f.add("Parent vApp", v.ParentVApp)
	f.add("Direct VMs", strings.Join(v.DirectVMs, ", "))
	f.add("Child vApps", strings.Join(v.ChildVApps, ", "))
	f.add("Child resource pools", strings.Join(v.ChildResourcePools, ", "))
	if a := v.Allocation; a != nil {
		f.add("CPU allocation", humanize.Allocation(a.CPULimitMHz, a.CPUReservationMHz, a.CPUExpandable, a.CPULevel, a.CPUShares, humanize.MHz))
		f.add("Memory allocation", humanize.Allocation(a.MemLimitMB, a.MemReservationMB, a.MemExpandable, a.MemLevel, a.MemShares, humanize.MB))
	}
	f.add("Startup order", vsphere.StartOrderText(v.StartOrder))
	if v.Metadata.TagsStatus == "available" {
		f.add("Tags", dash(metadataTags(v.Metadata)))
	}
	if v.Metadata.CustomAttributesStatus == "available" {
		f.add("Custom attributes", dash(metadataAttributes(v.Metadata)))
	}
	f.flush()
}

func newDatastoreShowCommand(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show NAME",
		Short: "Show one datastore's detailed evidence",
		Long: strings.TrimSpace(`
Show one datastore's identity, capacity and backing storage.

Files are not browsed here; use "vsfleet datastore files list NAME" for that,
so showing a datastore never costs more than the datastore listing already
does.`),
		Example: `  # Show a datastore on the current context
  vsfleet datastore show nvme-01

  # Narrow to one vCenter when the name is ambiguous
  vsfleet datastore show nvme-01 --context prod

  # As JSON, for a script
  vsfleet datastore show nvme-01 -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stores, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.Datastore, error) {
				return c.ListDatastores(ctx)
			})
			if err != nil {
				reportFailures(a, failures)
				return err
			}
			matches := filterByName(stores, args[0], func(d vsphere.Datastore) string { return d.Name })
			ds, rerr := exactlyOne(a, "datastore", args[0], matches,
				func(d vsphere.Datastore) string { return d.Context },
				func(d vsphere.Datastore) string { return d.Path },
				func(d vsphere.Datastore) string { return d.Name })
			if rerr != nil {
				reportFailures(a, failures)
				return rerr
			}
			if a.json() {
				defer reportFailures(a, failures)
				return writeJSON(a.out(), ds)
			}
			printDatastoreDetail(a.out(), ds)
			reportFailures(a, failures)
			return nil
		},
	}
}

func printDatastoreDetail(out io.Writer, d vsphere.Datastore) {
	f := newFields(out)
	f.add("Name", d.Name)
	f.add("Context", d.Context)
	f.add("Path", d.Path)
	f.add("ID", d.ID)
	f.add("Datacenter", d.Datacenter)
	f.add("Type", d.Type)
	f.add("Accessible", yesNo(d.Accessible))
	f.add("Maintenance", d.Maintenance)
	f.add("Capacity", humanBytes(d.CapacityBytes))
	f.add("Free", humanBytes(d.FreeBytes))
	if d.CapacityBytes > 0 {
		f.add("Used", fmt.Sprintf("%.0f%%", d.UsedPercent()))
	}
	f.add("Backing URL", d.Backing.URL)
	f.add("VMFS UUID", d.Backing.VMFSUUID)
	f.add("NAS remote", d.Backing.NASRemote)
	if d.Backing.Local {
		f.add("Local", yesNo(true))
	}
	if d.Metadata.TagsStatus == "available" {
		f.add("Tags", dash(metadataTags(d.Metadata)))
	}
	if d.Metadata.CustomAttributesStatus == "available" {
		f.add("Custom attributes", dash(metadataAttributes(d.Metadata)))
	}
	f.flush()
	fmt.Fprintf(out, "\nRun \"vsfleet datastore files list %s\" to browse its contents.\n", d.Name)
}

func newNetworkShowCommand(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show NAME",
		Short: "Show one network or port group's detailed evidence",
		Example: `  # Show a network on the current context
  vsfleet network show prod-vlan-200

  # Narrow to one vCenter when the name is ambiguous
  vsfleet network show prod-vlan-200 --context prod

  # As JSON, for a script
  vsfleet network show prod-vlan-200 -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nets, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.Network, error) {
				return c.ListNetworks(ctx)
			})
			if err != nil {
				reportFailures(a, failures)
				return err
			}
			matches := filterByName(nets, args[0], func(n vsphere.Network) string { return n.Name })
			net, rerr := exactlyOne(a, "network", args[0], matches,
				func(n vsphere.Network) string { return n.Context },
				func(n vsphere.Network) string { return n.Path },
				func(n vsphere.Network) string { return n.Name })
			if rerr != nil {
				reportFailures(a, failures)
				return rerr
			}
			if a.json() {
				defer reportFailures(a, failures)
				return writeJSON(a.out(), net)
			}
			printNetworkDetail(a.out(), net)
			reportFailures(a, failures)
			return nil
		},
	}
}

func printNetworkDetail(out io.Writer, n vsphere.Network) {
	f := newFields(out)
	f.add("Name", n.Name)
	f.add("Context", n.Context)
	f.add("Path", n.Path)
	f.add("ID", n.ID)
	f.add("Datacenter", n.Datacenter)
	f.add("Type", n.Type)
	f.add("Switch", n.Switch)
	f.add("VLAN", n.VLAN)
	f.add("Accessible", yesNo(n.Accessible))
	if n.Metadata.TagsStatus == "available" {
		f.add("Tags", dash(metadataTags(n.Metadata)))
	}
	if n.Metadata.CustomAttributesStatus == "available" {
		f.add("Custom attributes", dash(metadataAttributes(n.Metadata)))
	}
	f.flush()
}

func formatSnapshotTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func dashBoolPtr(b *bool) string {
	if b == nil {
		return "-"
	}
	return yesNo(*b)
}
