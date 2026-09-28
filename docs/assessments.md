# Assessments and History

Assessments are explicit, read-only captures of VM, host, cluster, datastore,
and snapshot state. They are stored locally in SQLite and never sent back to
vCenter.

## Capture and inspect

```sh
vsfleet assessment run --all-contexts
vsfleet assessment run --all-contexts --browse-datastores
vsfleet assessment run --all-contexts \
  --label nightly --note "pre-change baseline" --pin
vsfleet assessment list
vsfleet assessment report latest
vsfleet vm history billing --all-observations
```

Use labels and notes to make recurring baselines easy to select. Pin a baseline
to protect it from retention cleanup; use `assessment update <run> --unpin`
before removing it.

## Compare runs

```sh
vsfleet assessment diff previous latest
vsfleet assessment diff nightly latest \
  --fail-on moved,vanished \
  --max-snapshot-age 30d \
  --require-complete
```

Diffs compare only vCenters collected successfully in both runs. An outage
therefore cannot masquerade as mass VM deletion. `assessment diff` returns exit
code `2` for a requested drift-policy violation; execution or selector errors
continue to return `1`.

The policy flags are command-line only so scheduled jobs are auditable from
their invocation:

```sh
vsfleet assessment run --all-contexts --label nightly
vsfleet assessment diff nightly latest \
  --fail-on appeared,vanished,moved \
  --fail-on snapshot-created,snapshot-removed \
  --max-snapshot-age 30d --require-complete -o json > drift.json
```

Use `--include-runtime` when volatile power, guest, IP, VMware Tools, or storage
fields should participate in a diff.

## Snapshots, trends, and reports

```sh
vsfleet assessment snapshots --older-than 30d
vsfleet assessment trends churn
vsfleet assessment trends snapshots --older-than 30d
vsfleet assessment trends capacity --kind all
vsfleet assessment report latest
```

Trends aggregate estate totals before context and resource drill-downs. By
default they use complete assessments; use `--include-partial` when partial
runs are intentionally part of the analysis.

`--context` scopes stored assessment reads, including report, snapshots, diff,
health/findings, readiness, orphans, exports, topology, network, decommission,
VM history, churn, snapshot trends, capacity trends, and capacity reports.
Names are matched case-insensitively after trimming whitespace. A requested
context that never appears in the selected stored run(s) is an error and exits
`1` rather than being reported as zero observations or silently widening to the
full estate; a blank `--context` value is likewise rejected. For a diff, a
context present on only one side remains a non-comparable coverage warning and
does not become an inferred appeared/vanished change. Scoped runs that did not
record a requested context are dropped from trends, and `--limit` then counts
only the assessments that contribute to the scoped trend. A context whose
collection succeeded but returned nothing is kept as a legitimate zero; a
context whose collection failed stays visible as a coverage warning. Stored
selectors use ledger context names and do not require current configuration.
Metadata and maintenance commands reject `--context` because they do not
produce context-scoped evidence. No JSON schema change or database migration is
involved.

### Capacity attribution and projection

```sh
vsfleet assessment capacity latest --since 30d --top 5
vsfleet assessment capacity --min-free 10 --min-free-bytes 500Gi -o json
```

Capacity growth is measured on used bytes (`capacity - free`), so a datastore
resize is recorded separately instead of being mistaken for VM growth. The
drill-down layers evidence by strength: `exact` uses complete datastore browse
file-size deltas mapped through VM disk backing paths; `inferred` uses a
single-datastore VM's committed-storage delta; and `split` uses per-disk
provisioned-capacity deltas when a VM spans datastores. Files that cannot be
resolved to a VM remain visible as file contributors. The synthetic
`unattributed` contributor is the residual needed to make the table sum to the
reported datastore growth.

The projection is a linear least-squares fit of used bytes over usable history.
It is `unknown` when fewer than three points, less than a day of span,
non-shrinking free space, or incomplete coverage prevents a defensible result.
It is `low-confidence` when history is sparse, the fit is weak, partial runs
are included, a datastore was resized, or pruning-like gaps are present.
Shared backing identities merge the same datastore across vCenters; local
datastores remain context-scoped. Blindness is retained in JSON and stderr
notes rather than being treated as zero growth.

## Deterministic exports

Exports read one persisted run and do not contact vCenter or open a live
session. The `rvtools` format is an XLSX workbook containing `vInfo`, `vCPU`,
`vMemory`, per-VM `vDisk`, `vPartition` and `vNetwork`, `vTools`, `vHost`,
`vHBA`, `vNIC`, `vSwitch`, `vPort`, `dvSwitch`, `dvPort`, `vSC+VMK`,
`vMultiPath`, `vCluster`, `vRP`, `vDatastore`, `vSnapshot`, `vHealth`, and
`vsfleetCoverage` sheets.

```sh
vsfleet assessment export latest --format rvtools --file ./estate.xlsx
vsfleet assessment export latest --format csv --file ./estate-csv/
```

The destination is required: a `.xlsx` file for `rvtools` or a directory for
`csv`. An existing destination needs `--force`. Re-exporting unchanged evidence
produces byte-identical output in either format. CSV creates one `<tab>.csv`
file per sheet for `jq`, `awk`, `pandas`, and source-control diffing.

Runs captured before VMware Tools version collection still populate the running
status column in `vTools`; version columns remain blank and the gap is recorded
on `vsfleetCoverage`.

### Health findings

`vsfleet health [RUN]` evaluates the evidence in a stored assessment without
contacting vCenter. Each finding includes a stable rule ID, category, severity,
object and vCenter context, measured evidence, and a recommendation. Categories
are `migration`, `availability`, `security`, `capacity`, and `hygiene`.

The defaults are a 30-day maximum snapshot age and 10% minimum free space for
datastores and guest filesystems. Use `--max-snapshot-age`,
`--min-datastore-free`, `--min-datastore-free-bytes`, `--min-guest-disk-free`, `--disable-rule`, and
`--severity` and `--category` to tune a run. `--wide` adds recommendations and
evidence to the table; JSON always includes them. `--fail-on-findings` returns
exit code 2 when a finding at or above the selected severity exists; invalid
selectors and other command errors return 1. `--list-rules` prints the rule
registry, including categories.

Snapshot age is measured from the assessment's own context finish time (falling
back to the run finish time), never from the current wall clock. Findings are
recomputed when read, but the thresholds are stamped into the `vHealth`
coverage message, so exporting unchanged evidence with the same options stays
reproducible. Rules that need inventory fields introduced after an older run
are marked `not-evaluated`, rather than making an empty tab look healthy. A
collector that failed, or a collection that was not recorded, makes the
affected rule `unknown`; a partially answered rule keeps real findings but
names its blind contexts. Incomplete evidence is never represented as a clean
pass.

Zombie-VMDK evidence is opt-in because it requires the vSphere
`Datastore.Browse` privilege and adds a bounded directory listing per
accessible datastore. Use `vsfleet assessment run --browse-datastores` and
then inspect `vsfleet assessment orphans [RUN]`. Orphan candidates are
estate-wide rather than name-matched within one vCenter: VMFS UUIDs, extents,
NFS exports, vVol IDs, and datastore URLs join observations where available.

Each candidate is classified as `verified-unreferenced`,
`suspected-unreferenced`, `referenced-other-context`, or
`unknown-incomplete-coverage`. Snapshot chains are matched in both directions,
and a truncated browse, failed relevant collection, or missing backing identity
prevents a verified verdict. `health --fail-on-findings --severity warning`
therefore fails only on verified orphans; low-confidence guesses are
informational. `assessment orphans -o json` exposes the paths, sizes,
timestamps, identity keys, references, and coverage reasons.

An empty candidate list is only a clean result when every datastore in the
assessment was fully browsed. `assessment orphans` reports the scan-coverage
state independently of the candidates: a run captured without
`--browse-datastores`, a failed or denied browse, or a truncated listing prints
`NOT EVALUATED`, names the affected datastores on stderr, and is exposed under
`coverage` in the JSON output even when `entries` is empty. Add
`--fail-on-unknown` to exit non-zero when any datastore was not fully browsed.

### Migration readiness

`vsfleet assessment findings [RUN]` is the assessment-prefixed equivalent of
`vsfleet health`. `vsfleet assessment readiness [RUN]` evaluates the same
stored evidence and returns `ready`, `blocked`, or `unknown`. Migration
findings at warning or critical severity are blockers; informational migration
findings are advisories. `--fail-on-blockers` returns exit code 2 when blockers
exist.

Readiness is deliberately conservative: a failed or missing collector yields
`unknown`, and a blind vCenter is named in the `Not evaluated` section. The
verdict can never say `ready` over evidence that was not collected.

Schema version 13 adds migration evidence for firmware and Secure Boot,
vTPM, CPU socket/core topology, VM CPU/memory reservations and limits, RDM and
shared-disk relationships, manually assigned MAC addresses, PCI/SR-IOV/vGPU
passthrough, legacy floppy devices, and vCenter extension ownership. The
`host-device-passthrough` rule requires PCI/vGPU or SR-IOV evidence; ordinary
VMXNET3 UPT compatibility does not block. RDM, shared-disk, vTPM, and
host-device passthrough findings are blocking warnings;
firmware, Secure Boot, topology, resource controls, manual MACs, floppies, and
extension ownership are informational advisories. Missing VM configuration
evidence remains `unknown`, including when no special device is present.

### VM decommission review

`vsfleet vm decommission-check NAME_OR_UUID [RUN]` combines the VM-level
strict-safety gates with the stored topology graph. It is an advisory report
only: it never performs a decommissioning action. Powered-on or suspended VMs,
snapshots, connected CD-ROM/USB devices, and inaccessible, orphaned,
disconnected, or invalid connection states produce blockers. Missing evidence
or unresolved dependencies produce `unknown`. A clean result is `no-blockers`
(schema version 2; earlier releases called this `ready`): it means the
collected technical gates passed and is not authorization to delete the VM.
Ownership, backup policy, and application dependencies remain `not_assessed`
advisories until those metadata sources are collected, and the verdict never
accounts for them. Use `--fail-on-blockers` for an automation gate; the command
returns exit code 2 only for a blocked verdict.

### Topology and dependency queries

The `topology`, `dependencies`, and `blast-radius` commands query the same
stored assessment ledger without contacting vCenter. They merge objects across
contexts only when strong identity evidence intersects: VM instance or BIOS
UUIDs, datastore backing identity, distributed switch and port-group keys, or
NSX identifiers. A display name is only a context-scoped fallback and is
marked `inferred`.

Confidence is explicit: `complete` means the required collections answered and
all returned relationships are resolved; `partial` means a collection was
blind, a reference was unresolved, or a network was reconstructed; `unknown`
means the subject cannot be resolved, every checked context is blind, or a
non-local datastore in schema 11 or later has no backing identity. A query
whose name matches distinct subjects returns all of them with an ambiguity
header; use `--context` to narrow it rather than guessing.

Network inventory is persisted beginning with inventory schema 12. Older runs
can still reconstruct network nodes from distributed switches, host port
groups, and VM NIC references, but those answers carry the schema reason and
are capped at `partial`.

### Cross-cluster network readiness

`vsfleet network compare SOURCE TARGET [RUN]` compares the networks reachable
from two named clusters without contacting vCenter. It uses distributed switch
port groups and standard host port groups from the stored assessment, including
VLAN, parent switch, MTU, teaming, security policy, uplinks, and host coverage.
Networks match by VLAN first and then by name. The JSON result separates
matched pairs, source-only mapping gaps, target-only networks, and field-level
differences. A source-only network is also resolved through the topology graph
so the VMs that would lose connectivity are listed with the attachment
evidence basis and confidence.

`vsfleet assessment network-readiness --source SOURCE --target TARGET [RUN]`
derives the migration verdict from that comparison. `ready` means no attached
VM would lose a source network and no hard mismatch was observed; `blocked`
means an attached mapping gap or VLAN, MTU, security, or host-coverage mismatch
was found; `unknown` means a cluster is unresolved or relevant collection
evidence is blind. Confidence is explicit (`complete`, `partial`, or `unknown`):
pre-schema-12 or reconstructed evidence is partial, while blind contexts always
force unknown. `--fail-on-blockers` returns exit code 2 for a blocked result.

### Importing an RVTools export

The export profile has an inverse: `vsfleet import rvtools` reads an
RVTools-compatible workbook back into assessment history so that `diff`,
`vm history`, `trends` and `topology` can work on estates vsfleet did not
capture itself. Missing worksheets and columns become explicit coverage gaps,
never confirmed zeroes. See [Importing RVTools
exports](commands.md#importing-rvtools-exports).

### RVTools file interoperability

The `rvtools` export profile renders twenty-three worksheet layouts used by RVTools
exports, so a downstream tool that reads those worksheet names and columns can
consume the corresponding parts of a vsfleet export:

`vInfo` · `vCPU` · `vMemory` · `vDisk` · `vPartition` · `vNetwork` · `vCD` · `vUSB` · `vTools` ·
`vHost` · `vHBA` · `vNIC` · `vSwitch` · `vPort` · `dvSwitch` · `dvPort` ·
`vSC+VMK` · `vMultiPath` · `vCluster` · `vRP` · `vDatastore` · `vSnapshot` ·
`vHealth`

Compatibility is limited to the listed worksheet names and columns. Other
worksheets are outside this export profile, so a downstream pipeline that
requires them is not supported. This is an interoperability export, not RVTools
and not a replacement for it.

`vsfleet compatibility report` prints the full column-level reference: every
worksheet, each column's type and unit, and when a cell is left empty. It is
generated from the same definitions the exporter writes from, so it cannot
drift from the workbook, and it needs no configuration, no keyring and no
vCenter:

```sh
vsfleet compatibility report --sheet vPartition
vsfleet compatibility report -o json | jq
```

It describes what vsfleet emits and what those values mean. The following
differences were observed against an RVTools 4.8.1.4 export of the same
vCenter, captured about a minute apart ([comparison](https://github.com/Easonliuuuuu/vsfleet/issues/207)).
Other RVTools versions and collector settings may differ. Pipelines that
compare values under a shared header should account for these differences:

| Sheet / column | vsfleet value | RVTools 4.8.1.4 value |
| --- | --- | --- |
| VM sheets, `Folder` | Path relative to the datacenter's `vm` folder: `/` or `/Discovered virtual machine` | Includes the datacenter: `/DC-Lab` or `/DC-Lab/Discovered virtual machine` |
| RVTools-named sheets, `VI SDK Server` | Configured endpoint URL, for example `https://192.168.150.10` | Host without URL scheme, for example `192.168.150.10` |
| `vHost`, `ESX Version` | Version only, for example `8.0.3` | Product name, version and build, for example `VMware ESXi 8.0.3 build-24784735` |
| `vHost`, `# VMs total` | Counts host VM references, including templates | Excludes templates; the comparison found 4 versus vsfleet's 5 |
| `vTools`, `Tools` | VMware Tools **running status** (`GuestInfo.toolsRunningStatus`), for example `guestToolsNotRunning` | Tools **installation status**, for example `toolsNotInstalled` |
| `vHBA`, `Type` | vSphere adapter type, for example `HostBlockHba` | Human-readable label, for example `Block SCSI` |
| `vSnapshot`, `Date / time` | UTC; XLSX displays a date without a zone and CSV uses RFC3339 with `Z` | Collector-local time without a zone |
| `vInfo` `In Use MiB`; `vPartition` capacity, consumed, free MiB and `Free %`; `vDatastore` capacity, in-use, free MiB and `Free %`; `vHost` CPU and memory usage `%` | Decimal values are retained (vPartition `Free %` is rounded to two decimals) | Integer values |

The `vTools` values describe different states: a Tools installation can exist
while its service is not running. Treat `Tools` as a different field when
mapping an RVTools pipeline. For `vSnapshot`, convert the UTC value to the
collector's local zone before comparing wall-clock times.

vsfleet is a personal open-source project, not an official Dell Technologies
product, and is not sponsored, endorsed, or supported by Dell Technologies. Its
export interoperability was independently implemented without RVTools source
code or non-public documentation. RVTools is a Dell Technologies product;
references here describe export-file interoperability only.

### Guest partitions need VMware Tools

`vPartition` reports what the guest sees: filesystem paths, capacity, consumed
and free space. Only VMware Tools inside the guest can measure that — vSphere
knows how large a virtual disk is, never how much of it the guest has used. A
powered-off VM, or one whose Tools are not running, therefore contributes no
`vPartition` rows at all.

Each row also carries the `Disk Key` of the virtual disk behind the
filesystem, which joins to the column of the same name in `vDisk` — that is
how a sizing tool ties consumed space to the disk it has to provision. VMware
Tools only reports the mapping on vSphere 7.0 and later, so the cell is empty
on older estates, and a volume spanning several disks lists each of them.

Because a short tab would otherwise be indistinguishable from a small estate,
`vsfleetCoverage` marks the tab `partial` and names the shortfall — for
example `18 of 40 VMs reported guest filesystems; the rest had no running
VMware Tools`. Captures taken before this tab existed are marked
`not recorded` rather than empty.

### Resource pools are export evidence

`vRP` includes every resource pool in the datacenter scope, including each
cluster or standalone host's root `Resources` pool. vApps are reported on their
own terms and are not duplicated as resource-pool rows. Resource pools are
captured for the export and assessment ledger, not made into a browsable TUI
tab or CLI inventory noun.

The tab covers resource-pool identity and CPU/memory allocation configuration.
This read-only capture does not request additional volatile or unavailable
runtime fields, so it leaves them out rather than guessing values.

### Host storage and network inventory

The host-scoped sheets add storage adapters (`vHBA`), one aggregate row per
host/LUN with path-state counts (`vMultiPath`), physical NICs (`vNIC`),
standard virtual switches (`vSwitch`), standard port groups (`vPort`), and
VMkernel or legacy service-console adapters (`vSC+VMK`). They are collected
from `HostSystem.config.storageDevice` and `HostSystem.config.network` during
assessment capture. Search, host listing, and the TUI keep their summary fetch;
the host configuration properties are deliberately not added to those paths.
Inventory schema version 14 adds tri-state local-storage evidence to each
`vMultiPath` row. Local disks are excluded from redundancy findings; unknown
locality remains unresolved rather than being treated as healthy shared storage.
Inventory schema version 15 renames the persisted VM NIC direct-path field to
`upt_compatibility_enabled`; the old `direct_path_io` value in schema 12–14
rows was UPT compatibility mislabeled and is no longer read as passthrough
evidence.

### Distributed switch inventory

Assessment capture records distributed virtual switches and their distributed
port groups from the vSphere `config` and `summary` properties. The `dvSwitch`
sheet includes switch identity, MTU, host membership, uplinks, link discovery,
LACP and product information. The `dvPort` sheet has one row per distributed
port group and its default VLAN, teaming, security and shaping policy; it is
not one row per runtime distributed port. Per-port runtime state is outside
this read-only profile. Captures before inventory schema 10 mark both sheets
`not recorded` in `vsfleetCoverage`.

The network browsing path also enriches distributed port groups with their
parent switch and VLAN, so `vsfleet network list` answers that common join
without requiring an assessment capture.

The `vsfleetCoverage` sheet records every tab and vCenter in the run with its
collection status, item count, and any error. A partial estate is reported as
partial rather than handed over as if it were whole.

## Retention and recovery

```sh
vsfleet assessment prune --older-than 90d       # dry-run
vsfleet assessment prune --older-than 90d --execute
vsfleet assessment backup ./history-backup.db
vsfleet assessment restore ./history-backup.db --force
vsfleet assessment doctor
vsfleet assessment delete <run> --force
```

Only one mutating operation may write the history database at a time. Capture,
prune, backup, and restore use a fenced lease; listing, diffing, and opening the
TUI never take that lease. Backups are consistent SQLite snapshots. Restore
creates an automatic pre-restore safety copy before replacing the active
database.

The database defaults to `<user-config-dir>/vsfleet/history.db`. Override it
with `--history-db <path>` or `VSFLEET_HISTORY_DB`. It may contain operational
inventory, so protect it using normal host disk and backup controls.
