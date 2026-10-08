package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// hostSubresourceCommand builds a "list" command that flattens one piece of
// per-host evidence (e.g. Host.HBAs) across every host, optionally narrowed
// to one host by name with --host. Every row carries its host's context and
// name, so JSON output identifies where each device lives even when several
// hosts (or vCenters) are listed at once.
func hostSubresourceCommand[T any](a *App, short string, headers []string, extract func(vsphere.Host) []T, row func(vsphere.Host, T) []string) *cobra.Command {
	var hostName string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   short,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hosts, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.Host, error) {
				return c.ListHostsWithConfig(ctx)
			})
			if err != nil {
				reportFailures(a, failures)
				return err
			}
			if hostName != "" {
				matches := filterByName(hosts, hostName, func(h vsphere.Host) string { return h.Name })
				host, rerr := exactlyOne(a, "host", hostName, matches,
					func(h vsphere.Host) string { return h.Context },
					func(h vsphere.Host) string { return h.Path },
					func(h vsphere.Host) string { return h.Name })
				if rerr != nil {
					reportFailures(a, failures)
					return rerr
				}
				hosts = []vsphere.Host{host}
			}
			if a.json() {
				type wrapped struct {
					Context string `json:"context"`
					Host    string `json:"host"`
					Detail  T      `json:"detail"`
				}
				var rows []wrapped
				for _, h := range hosts {
					for _, item := range extract(h) {
						rows = append(rows, wrapped{Context: h.Context, Host: h.Name, Detail: item})
					}
				}
				defer reportFailures(a, failures)
				return writeJSON(a.out(), rows)
			}
			multi := a.multiContext()
			hdrs := append([]string{"HOST"}, headers...)
			if multi {
				hdrs = append([]string{"CONTEXT"}, hdrs...)
			}
			t := newTable(a.out(), hdrs...)
			for _, h := range hosts {
				for _, item := range extract(h) {
					cells := append([]string{h.Name}, row(h, item)...)
					if multi {
						cells = append([]string{h.Context}, cells...)
					}
					t.row(cells...)
				}
			}
			t.flush()
			reportFailures(a, failures)
			return nil
		},
	}
	cmd.Flags().StringVar(&hostName, "host", "", "only include this host, by name")
	return cmd
}

// hostSubgroup builds a read-only, list-only command nested under "host" for
// one piece of storage or network evidence, e.g. "host hba list". path is
// the full command path ("host hba"), used only to generate an example —
// Use holds just this command's own name, the way cobra expects.
func hostSubgroup(name string, aliases []string, short, path string, list *cobra.Command) *cobra.Command {
	cmd := requireSubcommand(&cobra.Command{
		Use:     name,
		Aliases: aliases,
		Short:   short,
		Example: fmt.Sprintf(`  # List on the current context
  vsfleet %[1]s list

  # Narrow to one host
  vsfleet %[1]s list --host esxi-01`, path),
	})
	cmd.AddCommand(list)
	return cmd
}

func newHostHBAListCommand(a *App) *cobra.Command {
	cmd := hostSubresourceCommand(a, "List host bus adapters",
		[]string{"DEVICE", "TYPE", "STATUS", "MODEL", "DRIVER", "PROTOCOL"},
		func(h vsphere.Host) []vsphere.HostHBA { return h.HBAs },
		func(_ vsphere.Host, hba vsphere.HostHBA) []string {
			return []string{hba.Device, dash(hba.Type), dash(hba.Status), dash(hba.Model), dash(hba.Driver), dash(hba.StorageProtocol)}
		})
	cmd.Example = `  # HBAs on one host
  vsfleet host hba list --host esxi-01

  # Every HBA on the current context, as JSON
  vsfleet host hba list -o json`
	return cmd
}

func newHostMultipathListCommand(a *App) *cobra.Command {
	cmd := hostSubresourceCommand(a, "List host/LUN multipath aggregates",
		[]string{"LUN", "POLICY", "PATHS", "ACTIVE", "STANDBY", "DEAD", "DISABLED"},
		func(h vsphere.Host) []vsphere.HostMultipath { return h.Multipaths },
		func(_ vsphere.Host, mp vsphere.HostMultipath) []string {
			return []string{mp.LUN, dash(mp.Policy), itoa(mp.PathCount), itoa(mp.Active), itoa(mp.Standby), itoa(mp.Dead), itoa(mp.Disabled)}
		})
	cmd.Example = `  # Multipath LUNs on one host
  vsfleet host multipath list --host esxi-01

  # Every LUN on the current context, as JSON
  vsfleet host multipath list -o json`
	return cmd
}

func newHostPNICListCommand(a *App) *cobra.Command {
	cmd := hostSubresourceCommand(a, "List physical NICs",
		[]string{"DEVICE", "MAC", "LINK SPEED", "DUPLEX", "SWITCH"},
		func(h vsphere.Host) []vsphere.HostNIC { return h.NICs },
		func(_ vsphere.Host, n vsphere.HostNIC) []string {
			return []string{n.Device, dash(n.MAC), linkSpeed(n.LinkSpeedMB), duplexLabel(n.Duplex), dash(n.Switch)}
		})
	cmd.Example = `  # Physical NICs on one host
  vsfleet host pnic list --host esxi-01

  # Every physical NIC on the current context, as JSON
  vsfleet host pnic list -o json`
	return cmd
}

func newHostVSwitchListCommand(a *App) *cobra.Command {
	cmd := hostSubresourceCommand(a, "List standard virtual switches",
		[]string{"NAME", "PORTS", "FREE", "MTU", "UPLINKS"},
		func(h vsphere.Host) []vsphere.HostVSwitch { return h.VSwitches },
		func(_ vsphere.Host, vs vsphere.HostVSwitch) []string {
			return []string{vs.Name, i32toa(vs.NumPorts), i32toa(vs.FreePorts), i32toa(vs.MTU), dash(strings.Join(vs.Uplinks, ","))}
		})
	cmd.Example = `  # Standard switches on one host
  vsfleet host vswitch list --host esxi-01

  # Every standard switch on the current context, as JSON
  vsfleet host vswitch list -o json`
	return cmd
}

func newHostPortGroupListCommand(a *App) *cobra.Command {
	cmd := hostSubresourceCommand(a, "List standard-switch port groups",
		[]string{"NAME", "SWITCH", "VLAN"},
		func(h vsphere.Host) []vsphere.HostPortGroup { return h.PortGroups },
		func(_ vsphere.Host, pg vsphere.HostPortGroup) []string {
			return []string{pg.Name, dash(pg.Switch), i32toa(pg.VLAN)}
		})
	cmd.Example = `  # Port groups on one host
  vsfleet host portgroup list --host esxi-01

  # Every standard-switch port group on the current context, as JSON
  vsfleet host portgroup list -o json`
	return cmd
}

func newHostVMKernelListCommand(a *App) *cobra.Command {
	cmd := hostSubresourceCommand(a, "List VMkernel adapters",
		[]string{"DEVICE", "PORT GROUP", "IP", "MAC", "MTU"},
		func(h vsphere.Host) []vsphere.HostVMKernel { return h.VMKs },
		func(_ vsphere.Host, vmk vsphere.HostVMKernel) []string {
			return []string{vmk.Device, dash(vmk.PortGroup), dash(vmk.Address()), dash(vmk.MAC), i32toa(vmk.MTU)}
		})
	cmd.Example = `  # VMkernel adapters on one host
  vsfleet host vmkernel list --host esxi-01

  # Every VMkernel adapter on the current context, as JSON
  vsfleet host vmkernel list -o json`
	return cmd
}

func linkSpeed(mb *int32) string {
	if mb == nil {
		return "-"
	}
	return itoa(int(*mb)) + " Mb"
}

func duplexLabel(full *bool) string {
	if full == nil {
		return "-"
	}
	if *full {
		return "full"
	}
	return "half"
}
