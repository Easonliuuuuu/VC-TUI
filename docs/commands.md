# CLI reference

<span id="cli-guide"></span>

Run `vsfleet` to open the terminal UI, or add a subcommand for scripts.
[Persistent flags](#persistent-flags) set context, timeout, configuration,
history, and output.

## Common commands

| Command | Purpose |
|---|---|
| `vsfleet` / `vsfleet ui` | Open the terminal UI |
| `vsfleet demo` | Open the terminal UI on sample data, with no vCenter and nothing written |
| `vsfleet context add` | Add a vCenter context via the wizard or flags |
| `vsfleet context list` | List configured contexts |
| `vsfleet context show [name]` | Show endpoint, route, and TLS settings |
| `vsfleet context use <name>` | Select the current context |
| `vsfleet context test <name>` | Test connectivity and authentication |
| `vsfleet context remove <name>` | Remove a context and invalidate its session |
| `vsfleet status` | Check selected contexts |
| `vsfleet doctor [context...]` | Diagnose the connection stage by stage |
| `vsfleet health [run]` | Assess the estate described by a stored assessment |
| `vsfleet topology <kind> <name> [run]` | Show containment and attachments for a stored subject |
| `vsfleet dependencies <kind> <name> [run]` | Show what a stored subject depends on |
| `vsfleet blast-radius <kind> <name> [run]` | Show stored subjects affected by a dependency |
| `vsfleet assessment findings [run]` | Show structured health findings for a stored assessment |
| `vsfleet assessment inventory [run]` | Query inventory stored in an assessment |
| `vsfleet assessment orphans [run]` | Explain estate-wide browsed VMDK orphan evidence |
| `vsfleet assessment capacity [run]` | Attribute datastore growth and project free-space thresholds |
| `vsfleet assessment readiness [run]` | Return a migration-readiness verdict |
| `vsfleet assessment sizing [run]` | Test whether stored VMs fit a proposed destination (allocation based, offline) |
| `vsfleet network compare <source-cluster> <target-cluster> [run]` | Compare stored network reachability and policy between clusters |
| `vsfleet assessment network-readiness --source <cluster> --target <cluster> [run]` | Return a cross-cluster network-readiness verdict |
| `vsfleet search <text>` | Search every vCenter at once |
| `vsfleet <kind> list` | List VMs, templates, hosts, clusters, vApps, datastores, or networks |
| `vsfleet <kind> show <name>` | Show one VM, template, host, cluster, vApp, datastore, or network in full detail |
| `vsfleet host hba\|pnic\|vswitch\|portgroup\|vmkernel\|multipath list [--host <name>]` | List one host storage/network subresource, across every host or narrowed to one |
| `vsfleet dvswitch list` | List distributed virtual switches |
| `vsfleet dvportgroup list [--switch <name>]` | List distributed port groups, across every switch or narrowed to one |
| `vsfleet resourcepool list` | List resource pools |
| `vsfleet snapshot list [--vm <name>]` | List VM snapshots, across every VM or narrowed to one |
| `vsfleet datastore files list <datastore> [path]` | List one datastore directory |
| `vsfleet datastore files find <datastore> <pattern>` | Recursively search a datastore for a name or pattern |
| `vsfleet vm history <name-or-uuid>` | Show a VM's stored assessment timeline |
| `vsfleet vm events <name-or-uuid> [--all]` | Show a VM's live vCenter event log: migrations, reconfigurations, snapshots and failures, with who and when; task results come from the VM's task history |
| `vsfleet vm decommission-check <name-or-uuid> [run]` | Review stored evidence before decommissioning a VM |
| `vsfleet assessment ...` | Capture and compare historical observations |
| `vsfleet assessment export --profile <name> [--pseudonymize --pseudonymize-key-file <file>] [--preview]` | Export a scoped `sizing-summary` or `full-inventory` workbook, optionally pseudonymized; see [scoped sharing profiles](sharing.md) |
| `vsfleet assessment metadata [run] [--kind ...] [--source tag\|custom] [--where ...] [--format csv]` | Export stored tags and custom attributes in a fixed long-format schema; see [metadata exports](metadata.md#export-metadata) |
| `vsfleet assessment metadata-report <definition> [run] [--base <run>]` | Run a saved tag/custom-attribute report against an assessment, optionally listing membership changes since `--base` |
| `vsfleet import rvtools <file.xlsx>` | Import an RVTools-compatible export as a new offline assessment run |
| `vsfleet compatibility report` | Describe every worksheet and column the export writes |

## Inventory and search

Resource kinds include `vm`, `template`, `host`, `cluster`, `vapp`,
`datastore`, `network`, `dvswitch`, and `resourcepool`. Use `--filter` /
`-f` for text matching, repeatable `--where` for predicates, and `--wide`
for metadata columns:

```sh
vsfleet host list --context prod --filter esxi-07
vsfleet datastore list --all-contexts -f nvme
vsfleet vapp list --all-contexts
vsfleet vm list --where 'tag=Production' --where 'cpu>=8' --wide
vsfleet datastore list --where 'free_percent<15'

vsfleet search ubuntu --all-contexts
vsfleet search --tag migration-wave-2 --wide
vsfleet search nvme --kind datastore --limit 20
```

`--where` uses `field operator value`, with `=`, `!=`, `<`, `<=`, `>` and
`>=`. Repeat the flag to AND predicates. Values may be quoted when they contain
spaces. Built-in fields are case-insensitive; metadata values are exact and
case-sensitive. Use `tag=Production` for any category,
`tag.Environment=Production` for a category-qualified tag, and
`custom.environment=prod` (or `custom.#123=prod`) for custom attributes.
Numeric comparisons are numeric rather than lexical. Missing or unavailable
metadata never matches, including a negative predicate.

Stored captures are queried offline with the same evaluator:

```sh
vsfleet assessment inventory latest --where 'custom.environment=prod' --wide
vsfleet assessment findings latest --where 'tag=PCI' -o json
```

JSON includes normalized metadata, source status, and object/context
provenance. Scalar inventory remains usable when metadata is `unavailable`
(read failed), `denied` (permission/session rejected), or `unsupported`
(service absent). Older captures report `not_recorded`; only `available`
is complete. See [metadata exports and saved reports](metadata.md).

Results from healthy contexts remain available when another context fails. The
failure is reported separately with its context and diagnostic information.

## Object detail and infrastructure subresources

`vsfleet <kind> show <name>` prints identity, provenance, and configuration.
Ambiguous names return candidates; use `--context` to narrow them:

```sh
vsfleet vm show web-01
vsfleet vm show 5029c07a-1b3e-4d2f-9c11-8a7e6f0d4b52   # by instance UUID
vsfleet host show esxi-01 --context prod
vsfleet datastore show nvme-01 -o json
```

Query host HBAs, NICs, switches, port groups, VMkernel adapters, and
multipath LUNs across a context or select one host with `--host`:

```sh
vsfleet host hba list --host esxi-01
vsfleet host vswitch list
vsfleet host multipath list --host esxi-01 -o json
```

Distributed switch, resource pool and snapshot evidence work the same way:

```sh
vsfleet dvswitch list
vsfleet dvportgroup list --switch dvs-prod
vsfleet resourcepool list --wide
vsfleet snapshot list --vm web-01
```

## Datastore file browsing

`datastore files list` reads one directory lazily; `find` performs a bounded
recursive search and reports incomplete results. Both are read-only:

```sh
vsfleet datastore files list nvme-01
vsfleet datastore files list nvme-01 vm/web-01/
vsfleet datastore files find nvme-01 '*.vmdk'
vsfleet datastore files find nvme-01 orphan.vmdk --limit 20 -o json
```

VMDK results include known VM/template references and confidence from the
latest assessment. Ambiguous datastore names are refused; narrow the context.
See [datastore browsing](tui-storage.md#datastore-file-browser) for relationship
and coverage limits.

## Importing RVTools exports

Import an RVTools-compatible XLSX file as a new assessment without
configuration, credentials, or a vCenter connection:

```sh
# Preview what would be imported without writing anything
vsfleet import rvtools estate.xlsx --dry-run

# Import it, labelled, with an explicit capture time
vsfleet import rvtools estate.xlsx --label pre-migration --captured-at 2026-01-01T00:00:00Z

# Interpret timezone-free capture and snapshot timestamps from a Pacific collector
vsfleet import rvtools estate.xlsx --timezone America/Los_Angeles

# Once imported, every stored-evidence command works on it like a live capture
vsfleet assessment list
vsfleet vm history web-01
vsfleet assessment diff pre-migration wave-1
```

Imported runs show source `rvtools-import`. Their notes record filename,
SHA-256 fingerprint, capture time/source, parser profile `rvtools-v3`, and
recognized/ignored worksheets.

### What is read

Worksheets match by name and columns by header; extra or reordered columns
are allowed:

| Worksheet | Becomes |
|---|---|
| `vInfo` | VMs and their identity (required) |
| `vCPU`, `vMemory` | VM CPU and memory, when `vInfo` lacks the column; a disagreement with `vInfo` is warned about and `vInfo` wins |
| `vDisk`, `vNetwork`, `vTools`, `vPartition`, `vSnapshot` | per-VM disks, NICs, Tools state, guest filesystems and snapshots |
| `vHost`, `vCluster`, `vDatastore` | the host, cluster and datastore collections |
| `vSwitch`, `vPort`, `vHBA`, `vNIC`, `vSC+VMK` / `vSC_VMK` | host configuration, joined by `Object ID` or a unique host name scoped to the same vCenter in the workbook |
| `vMultiPath` | host multipaths, joined by a unique `Host` name scoped to the same vCenter in the workbook; `Object ID` is not used for the host join |
| `dvSwitch`, `dvPort` | distributed switches and their port groups |
| `vCD`, `vUSB` | per-VM CD-ROM and USB devices |
| `vMetaData` | capture time, when its timestamp can be interpreted |

Dry runs list ignored worksheets and recognized, ignored, and missing
columns. Mapped headers accept vsfleet and RVTools spellings, including
`Shared Bus`/`SharedBus`, `Switch`/`DVS`, `Max Ports`/`# Max ports`,
`Port`/`Port group`, `Allow Promiscuous`/`Promiscuous mode`, and
`Policy`/`Teaming policy`. `Rolling Order` maps to `Failback`.

In `vMultiPath`, RVTools' `Object ID` identifies the datastore. Hosts join
through `vHost` using `Host` and `VI SDK UUID`, narrowed by `Datacenter`
and `Cluster` if supplied. Ambiguous/missing hosts are warned and skipped.
This also applies to vsfleet exports. RVTools lists datastore-backed disks;
vsfleet exports every collected host/LUN, so counts may differ.

The LUN label uses `Display name`, falling back to `Disk`; `Disk` remains
the device ID. RVTools path counts cover only supplied `Path 1`–`Path 8`
slots and states. Locality and working-path count remain unknown. vsfleet's
own aggregate counts, locality, and working-path columns are preserved.

### Missing evidence is never good news

Missing workbook evidence reduces confidence; it cannot improve a verdict.

- Absent worksheets are `unavailable`. Present worksheets with no rows are
  empty only when required columns exist: `vHost` needs `# Cores`,
  `# Memory`, `Speed`; `vCluster` needs `NumCpuCores`, `TotalCpu`,
  `TotalMemory`; `vDatastore` needs `Capacity MiB`, `Free MiB`.
- Missing VM `CPUs`, `Memory`, or `In Use MiB` is a coverage gap, never zero.
- Unsupported health evidence yields unknown/not-evaluated. Imports claim
  schema `2` by default, `3` with `vTools`, `4` with `vPartition`, and
  `5` with its `Disk Key` column.
- Resource pools and networks are always unavailable: `vRP` lacks VM
  membership, and networks lack managed-object IDs.
- Host devices require an ID or unique same-workbook host name in the same
  vCenter UUID. Ambiguous joins are skipped and reported.
- Without `VI SDK UUID`, the endpoint is the reported vCenter identity.

### Identity

VM matching uses managed-object ID, instance UUID, BIOS UUID, then display
name. Same-named VMs across vCenters stay distinct; renames preserve history
when stronger identity exists. Duplicate identities retain both rows but prevent
dependent rows from joining either. Repeated host, cluster, datastore, or
distributed-switch `Object ID` makes that context's collection unavailable.

Host networking joins by ID or a unique same-workbook host name/vCenter UUID.
Port groups join by context, datacenter, and switch name only when unique.

`--context-map KEY=NAME` renames a reconstructed context without touching
`config.toml`; an imported run's contexts are independent of it.

### Trends and partial runs

Imports are always `partial` because resource pools and networks are
unavailable. Diff, VM history, findings, and topology use them with coverage
warnings. Trends require `--include-partial`:

```sh
vsfleet assessment trends snapshots --include-partial
vsfleet assessment trends capacity --include-partial
```

### Capture time

Capture time comes from `--captured-at`, then usable `vMetaData`, workbook
properties, or import time. `--timezone` accepts an IANA zone for timezone-free
`vMetaData` and `vSnapshot` values; timezone-free metadata is skipped without
it. Stored cell values preserve seconds and honor the 1900/1904 date system.

History, diff, and trends use capture time. The run notes its source and import
time. Filenames are never parsed for dates.

### Repeating an import

Repeated fingerprints warn on stderr with the previous run ID but still
create a new run. `--allow-duplicate` silences the warning.

### Safety

Workbooks are size-limited before opening, with bounded decompression and row
counts. Formulas are never evaluated and external links never followed. Failed
imports leave no run.

## Topology and dependencies

These queries read stored assessments without contacting vCenter:

```sh
vsfleet topology vm app01 latest
vsfleet dependencies vm app01 --depth 2
vsfleet dependencies network prod-vlan-210 -o json | jq
vsfleet blast-radius datastore ds-prod-01 --all-contexts
vsfleet network compare cluster-prod cluster-dr -o json | jq '.differences, .mapping_gaps'
vsfleet assessment network-readiness --source cluster-prod --target cluster-dr --fail-on-blockers
```

`topology` shows containment and attachments; `dependencies` follows what
a subject uses; `blast-radius` follows those edges backwards. Use
`--context` to narrow ambiguous names. Results include confidence,
provenance, unresolved references, and coverage gaps. Older reconstructed
network evidence is partial.

`network compare` compares two clusters' network mappings and policies;
`network-readiness` derives `ready`, `blocked`, or `unknown`. Blind
collections force unknown. See [planning](planning.md) for joins, blockers,
and confidence rules.

For diagnostics, `doctor` checks a live connection, `assessment doctor`
checks history storage, and `health`/`assessment findings` check stored
estate evidence.

### VM decommission checks

The offline report returns `no-blockers`, `blocked`, or `unknown`, using
`latest` by default. Pass a run ID/label for reproducibility and `--context`
for ambiguous names. `--fail-on-blockers` returns exit code 2 only for blockers.
It performs no VM action; `no-blockers` is not permission to delete.
Ownership, backup policy, and application dependencies are `not_assessed`.
See [decommission review](planning.md#vm-decommission-review) for the gates.

```sh
vsfleet vm decommission-check legacy-db01 latest
vsfleet vm decommission-check legacy-db01 --context prod -o json
vsfleet vm decommission-check legacy-db01 --fail-on-blockers
```

## Export compatibility

`vsfleet compatibility report` describes what the `rvtools` export profile
writes: every worksheet, every column, each cell's type and unit, and when a
cell is left empty.

```sh
vsfleet compatibility report                      # every worksheet
vsfleet compatibility report --sheet vHBA         # one of them
vsfleet compatibility report -o json | jq         # for a pipeline
```

The report is generated from exporter definitions and runs without
configuration, keyrings, or vCenter. It describes vsfleet output and observed
differences from RVTools, not the complete RVTools schema. See
[interoperability limits](exports.md#rvtools-file-interoperability).

## Assessments

Capture read-only evidence locally, then query it offline. Browsing needs
`Datastore.Browse`, adds collection time, and is disabled by default:

```sh
vsfleet assessment run --all-contexts --browse-datastores
vsfleet health latest
vsfleet assessment orphans latest
vsfleet assessment capacity latest
```

Collection options are independent:

| Flag | Evidence | Extra privilege |
| --- | --- | --- |
| `--browse-datastores` | Bounded VM disk-file checks for zombie/orphan VMDKs | `Datastore.Browse` |
| `--datastore-file-inventory` | File names/sizes for `vFileInfo`; exports paths | `Datastore.Browse` |
| `--include-licenses` | Products, usage, expiration, assignments; keys excluded | `Global.Licenses` |

File inventory limits are `--file-inventory-max-files`,
`--file-inventory-max-total-files`, and `--file-inventory-timeout`. Limit
flags do not enable collection. See [export limits/privacy](exports.md#datastore-file-inventory-vfileinfo)
and [licenses](licensing.md).

Without browsing, zombie-VMDK health is `not-evaluated`. Orphan reports expose
coverage in JSON and name unbrowsed, failed, denied, or truncated datastores on
stderr. Use `--confidence`, `--min-size`, and `--fail-on-unknown`; see
[health and orphan checks](health.md) for confidence and exit semantics.

Capacity reports accept `--since 30d`, `--datastore`, `--top`, and
`--min-free-bytes`. The larger of the absolute and percentage free-space floors
binds. See [capacity attribution](assessments.md#capacity-attribution-and-projection)
for exact/inferred/split contributions and unknown projections.

## Exit codes

Use exit codes to gate scheduled jobs:

| Code | Meaning |
|---|---|
| `0` | The command did what was asked |
| `1` | vsfleet could not do its job: bad configuration, an unreachable estate, an unreadable database |
| `2` | The tool worked and the estate did not pass: `assessment diff` policy violations, `health --fail-on-findings`, or `assessment readiness --fail-on-blockers` |
| `3` | A capture stored evidence from some contexts but not all; only with `assessment run --fail-on-partial` |

Partial captures exit `0` unless `--fail-on-partial` is set.

## JSON output

Use `-o json` for automation:

```sh
vsfleet vm list --all-contexts -o json
vsfleet search nvme --kind datastore -o json | jq
```

## Persistent flags

| Option | Description |
|---|---|
| `--context <name>` | Scope a command to one or more named contexts |
| `--all-contexts` | Target every configured context |
| `--config <path>` | Override the TOML configuration path |
| `--history-db <path>` | Override the SQLite assessment database |
| `--timeout <duration>` | Per-vCenter request timeout (default `30s`) |
| `-o, --output table\|json` | Select human or machine-readable output |
| `--refresh <duration>` | Set or disable background TUI polling |

## Finding your way around

Use `-h` / `--help` at any level for the installed binary's complete command
reference and examples:

```sh
vsfleet --help                          # grouped by the job being done
vsfleet assessment --help               # what is under a command group
vsfleet assessment diff --help          # flags, arguments, and examples
vsfleet assessment trends capacity -h   # four levels deep, same thing
```

`vsfleet --help` groups the tree rather than listing it alphabetically:
getting started, inventory and search, assessment and history, analysis, and
diagnostics.

Unknown commands exit non-zero and suggest close matches:

```console
$ vsfleet assessment lst
vsfleet: unknown command "lst" for "vsfleet assessment"

Did you mean this?
	list

Run 'vsfleet assessment --help' for the available commands.
```

Shell completion covers commands, flags, and the closed argument vocabularies
such as the `KIND` accepted by `topology`, `dependencies`, and `blast-radius`:

```sh
vsfleet completion bash --help   # and zsh, fish, powershell
```
