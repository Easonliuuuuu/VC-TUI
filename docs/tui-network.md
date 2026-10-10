# Networks and clusters

Use the Networks tab to inspect port groups, switch wiring, and VLAN coverage.
Host and cluster inspectors show how those resources fit together.

## Networks and switches

The Networks tab (`6`) groups port groups by distributed switch, standard
switch name across hosts, and opaque (NSX) networks. Distributed uplink port
groups are omitted.

| Column | Meaning |
|---|---|
| `VLAN` | VLAN, trunk range, or PVLAN; `none` for untagged standard port groups |
| `VMS` | Attached VMs; `-` until wiring loads |
| `NOTES` | Switch/port-group findings: `promiscuous`, `standby on 1 host`, `on 1 of 3 hosts`, `unused` |
| `VMK` | VMkernel adapters |
| `HOSTS` | Hosts on switch; `joined/total` when cluster hosts are missing |

`space` folds a switch; `t` toggles the tree and plain list. Filtering keeps
matching port groups under their switch and unfolds matching switches.

`Enter` on a port group shows teaming, active/standby uplinks, security policy,
and VMkernel adapters. On a switch, it opens two pages:

| Key | Page | Controls and content |
|---|---|---|
| `0` | Overview | Properties, uplink-by-host grid, findings, port groups. Identical host uplinks share a row. `▲` marks speeds differing from the majority; `down` means no link. |
| `1` | Wiring | Physical NICs → uplinks → switch → port groups, with NIC counts across hosts. `j`/`k` select a port group; heavy links show active uplinks, dotted links standby. `Enter` opens it. |

Narrow terminals reduce the wiring diagram's labels and columns.

Findings name the host and affected component:

| Finding | Consequence |
|---|---|
| Cluster host missing from switch | VMs using the switch cannot move there |
| Uplink missing a physical NIC | One fewer traffic path |
| NIC has no link | Traffic has failed over or has no path |
| Uplink speeds differ across hosts | Throughput depends on placement |
| Port group has no working uplink or only standby | Traffic is down or has no failover left |
| VMkernel MTU exceeds switch MTU | Jumbo-frame traffic such as vMotion/vSAN fails |
| Promiscuous mode allowed | VMs can see other VMs' traffic |
| Standard port-group VLAN, switch MTU, or port-group presence differs across hosts | Networking or vMotion depends on placement |

Port groups load with inventory. Wiring loads separately while Networks or a
switch workspace is open, and after network inventory reloads. It adds hosts,
uplinks, NICs, VMkernel adapters, and VM counts. `reading wiring…` marks a
pending read; failures show a reason. These reads need no privileges beyond
[ordinary inventory](configuration.md#vsphere-permissions).

## VLAN map

Press `v` on Networks to map VLANs across the vCenters in scope; `a` includes
all loaded vCenters. Each vCenter is a column. Untagged networks come first,
trunks last.

| Cell | Meaning |
|---|---|
| `● 288` | Present, with 288 VMs |
| `vmk 56` | Present, with 56 VMkernel adapters and no VMs |
| `○` | Present, unused |
| `·` | Absent |
| `4/6` | Four of six clusters can reach it |

Standard and distributed port groups sharing a VLAN share a row. Notes flag
VLANs found on one vCenter or with no shared name across vCenters (`names
differ`). `Enter` lists clusters, attached VMs, and trunks carrying the VLAN.

Press `p` to pick source and target clusters for a live
[network comparison](planning.md). Matching uses VLAN first, preferring the
same name, then name. The picker suggests a same-named cluster at another
site as the target.

The target column marks missing networks `✕`: a blocker when VMs use them,
an advisory when unused. Matched networks show the target name and differences
in MTU, teaming, or security. `Enter` lists source VMs on that network;
`x` clears the pair.

## Host network page

A host inspector offers `0` Summary and `1` Network when topology is
available. Network connects each physical NIC to its uplink, switch, port
groups, local VMs, and VMkernel adapters. Missing cluster switches get their
own line; unclaimed NICs appear last. `j`/`k` select a switch, `Enter`
opens it, and `Esc` returns to the host.

The page uses the same on-demand topology as switch views. It flags missing
cluster switches, unassigned uplinks, down NICs, MTU mismatches, and unequal
speeds. A linked NIC claimed by no switch means the cable is connected, but
the host has not joined a switch using it.

## Cluster workspace

`Enter` on a cluster starts on Summary. Choose pages with `0`–`2`;
`←`/`→` changes cluster while keeping the page.

| Key | Page | Shows |
|---|---|---|
| `0` | Summary | Live health/configuration issues; HA, monitoring, failover capacity/admission control; effective hosts and EVC; capacity, DRS, contents |
| `1` | Hosts & VMs | Host state, CPU/memory use, VM count. `space` expands a host's VMs; `Enter` opens its Hosts-tab row. |
| `2` | Storage | Datastore mounts, free space, and hosts missing shared mounts |

Summary marks fields `✓`, `▲`, or `✕` for conditions such as insufficient
HA failover capacity, disabled admission control, unavailable hosts, manual
DRS, or missing mounts. Capacity shows used/total. Effective capacity subtracts
unavailable hosts and hypervisor overhead; it has no usage percentage because
host usage includes the hypervisor. Standalone hosts omit Resilience, EVC,
and DRS.

Storage puts incomplete shared mounts first and names missing hosts: VMs
cannot run or restart there. A datastore mounted by exactly one host is
treated as local and is not flagged. The datastore inspector's Hosts field
also lists mount coverage by cluster. For files, use the
[datastore browser](tui-storage.md); for network coverage, use the
[switch workspace](#networks-and-switches).

Cluster pages use already-loaded inventory. Older captures and imported
RVTools workbooks lack health, HA, DRS, EVC, and mount evidence; fields show
`-` and Storage reports unread mounts.
