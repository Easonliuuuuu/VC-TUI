# Migration and capacity planning

## Migration readiness

`vsfleet assessment findings [RUN]` is equivalent to `vsfleet health`.
`vsfleet assessment readiness [RUN]` returns `ready`, `blocked`, or
`unknown` from stored evidence. Warning/critical migration findings block;
informational findings are advisories. `--fail-on-blockers` exits 2 for blockers.

Failed or missing collectors force `unknown`, with blind vCenters named in
`Not evaluated`.

Migration configuration evidence starts with schema 13:

| Evidence | Finding severity |
| --- | --- |
| RDM, shared disks, vTPM, PCI/SR-IOV/vGPU passthrough | Blocking warning |
| Firmware/Secure Boot, CPU socket/core topology, CPU/memory reservations and limits, manual MACs, floppies, extension ownership | Informational advisory |

`host-device-passthrough` requires PCI/vGPU or SR-IOV evidence; ordinary
VMXNET3 UPT compatibility does not block. Missing VM configuration is
`unknown`, even if no special device is present.

## VM decommission review

`vsfleet vm decommission-check NAME_OR_UUID [RUN]` combines VM safety gates
and stored topology. It reports only and performs no decommissioning action.

- `blocked`: powered-on/suspended VM, snapshots, connected CD-ROM/USB, or
  inaccessible, orphaned, disconnected, or invalid connection state.
- `unknown`: missing evidence or unresolved dependencies.
- `no-blockers`: collected technical gates passed. This schema-2 verdict
  (formerly `ready`) does not authorize deletion.

Ownership, backup policy, and application dependencies remain `not_assessed`
advisories until their metadata sources are collected; the verdict excludes
them. `--fail-on-blockers` exits 2 only for `blocked`.

## Topology and dependency queries

`topology`, `dependencies`, and `blast-radius` query stored evidence
offline. Cross-context merges require matching VM instance/BIOS UUIDs,
datastore backing identity, distributed switch/port-group keys, or NSX IDs.
Names are context-scoped fallbacks marked `inferred`.

| Confidence | Meaning |
| --- | --- |
| `complete` | Required collections answered; all relationships resolved |
| `partial` | Blind collection, unresolved reference, or reconstructed network |
| `unknown` | Unresolved subject, all checked contexts blind, or non-local datastore without backing identity in schema 11+ |

Ambiguous names return every matching subject with an ambiguity header.
Narrow with `--context`.

Pre-schema-12 runs reconstruct networks from switches, host port groups, and VM
NIC references. Results name the schema gap and remain at most `partial`.

## Cross-cluster network readiness

`vsfleet network compare SOURCE TARGET [RUN]` compares cluster-reachable
networks offline: VLAN, parent switch, MTU, teaming, security, uplinks, and
host coverage. Matching uses VLAN, then name. JSON separates matched pairs,
source-only gaps, target-only networks, and field differences. Source-only
gaps list affected VMs using topology attachment evidence and confidence.

`vsfleet assessment network-readiness --source SOURCE --target TARGET [RUN]`
derives a verdict:

| Verdict | Meaning |
| --- | --- |
| `ready` | No attached VM loses a source network; no hard mismatch |
| `blocked` | Attached mapping gap or VLAN, MTU, security, or host-coverage mismatch |
| `unknown` | Unresolved cluster or blind relevant collection |

Confidence is `complete`, `partial`, or `unknown`. Reconstructed or
pre-schema-12 evidence is partial; blind contexts force unknown.
`--fail-on-blockers` exits 2 for blocked results.

## Destination sizing scenarios

`vsfleet assessment sizing [RUN]` checks whether stored VMs fit a proposed
destination. It reads one assessment offline and changes nothing. The result
is `allocation-based`, without a compatibility claim for any hypervisor or
cloud platform.

```sh
vsfleet assessment sizing pre-migration --cluster cluster-a --cluster cluster-b \
  --hosts 3 --host-cores 48 --host-ram-gib 768 --datastore-capacity 60TiB \
  --rdm-capacity 2TiB --cpu-ratio 4 --growth-pct 20 --ha-host-failures 1
```

### Inputs

Required: `--hosts`, `--host-cores` (usable per host), `--host-ram-gib`,
and `--datastore-capacity` (usable, e.g. `40TiB`). Scope containing RDMs
also requires `--rdm-capacity`.

| Option | Default |
| --- | --- |
| `--cpu-ratio` (vCPU per usable core) | 1 |
| `--memory-ratio` | 1 |
| `--growth-pct` | 0 |
| `--ha-host-failures` | 1 (N-1); 0 disables |

Scope uses `--context`/`--all-contexts` and repeatable
`--cluster NAME|CONTEXT/NAME`. Templates are excluded; powered-off VMs need
`--include-powered-off`. Output lists excluded VMs and reasons, every input
as provided/defaulted/missing, run ID, capture time, scope, and generation time.

### Calculations

Output prints each formula with its numbers:

| Dimension | Required | Supply |
| --- | --- | --- |
| `cpu` (vCPU) | sum(vCPU) x (1 + growth/100) | (hosts - failures) x cores x cpu-ratio |
| `memory` (GiB) | sum(configured memory) x (1 + growth/100) / memory-ratio | (hosts - failures) x RAM per host |
| `memory-reservation` (GiB) | sum(VM memory reservations) | (hosts - failures) x RAM per host |
| `datastore-capacity` (GiB) | sum(provisioned datastore-backed disk) x (1 + growth/100) | target datastore capacity |
| `rdm-capacity` (GiB) | sum(RDM LUN capacity) x (1 + growth/100) | target RDM capacity |

Datastore-backed provisioned disks, RDM LUN capacity, and guest filesystem use
are separate. Guest use is split into datastore-backed, RDM-backed, and
unattributed rows. Shared disks count once; datastore backing identity (VMFS
UUID, extents, NFS remote, vVol, URL) and RDM LUN identity deduplicate across
contexts. Verdicts use provisioned size. Committed use is shown, including
whether it alone fits; guest use is informational.

### Verdicts

Each dimension is `fit`, `insufficient`, or `unknown`. Overall precedence is
insufficient > unknown > fit.

`unknown` is produced by a missing target input, a partial or failed source
collection (a run that is not `complete`, or a context whose VM or datastore
collection did not answer), VMs with no recorded allocation or disk
configuration, disks on a datastore missing from the stored inventory,
same-named datastores in several contexts with no matching backing identity, and
a shared-bus RDM with no LUN identity. Missing evidence leaves dimensions that
are already `insufficient` unchanged. `--fail-unless-fit` returns exit code 2
unless every dimension is `fit`.

Output shows oversubscription, memory reservations, HA allowance, and storage
assumptions (full provisioned size, no thin-provisioning or deduplication
credit). CPU reservations in MHz are reported without comparison because
target core frequency is not an input. Scoped migration blocker/advisory counts
and unevaluated rule counts accompany the result. Run network readiness
separately with a source/target cluster mapping.

### Limits

Sizing uses configured allocations without performance history or rightsizing.
It assumes homogeneous hosts and uniform growth, including RDM LUN size. It
omits per-host placement, NUMA, fragmentation, storage performance, licensing,
and overhead.
