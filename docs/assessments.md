# Assessments and history

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

Label recurring baselines and pin those you want to keep. Retention cleanup
preserves pinned runs; use `assessment update <run> --unpin` before removing one.

## Compare runs

```sh
vsfleet assessment diff previous latest
vsfleet assessment diff nightly latest \
  --fail-on moved,vanished \
  --max-snapshot-age 30d \
  --require-complete
```

Diffs compare vCenters collected successfully in both runs, so an outage does
not appear as VM deletion. Exit code `2` means a requested drift-policy
violation; execution and selector errors return `1`.

Policy flags are command-line only. A scheduled job records its policy in the
invocation:

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

Trends show estate totals, then context and resource detail. They use complete
assessments by default; `--include-partial` includes partial runs.

### Scope stored evidence

`--context` scopes reports, snapshots, diffs, health/findings, readiness,
orphans, exports, topology, network, decommission checks, VM history, trends,
and capacity reports. Names come from the ledger, without current configuration,
and match case-insensitively after trimming whitespace.

- Blank names or names absent from every selected run return exit code `1`.
- A context on only one side of a diff produces a coverage warning, without
  inferred appeared/vanished changes.
- Trends skip runs that did not record the context. `--limit` counts contributing
  runs only.
- A successful empty collection counts as zero; a failed collection produces
  a coverage warning.

Metadata and maintenance commands reject `--context`.

### Capacity attribution and projection

```sh
vsfleet assessment capacity latest --since 30d --top 5
vsfleet assessment capacity --min-free 10 --min-free-bytes 500Gi -o json
```

Capacity growth uses used bytes (`capacity - free`) and records datastore
resizes separately. Contributors have an evidence level:

| Level | Basis |
| --- | --- |
| `exact` | Complete browse file-size deltas mapped through VM disk backing paths |
| `inferred` | Committed-storage delta for a VM on one datastore |
| `split` | Per-disk provisioned-capacity deltas for a VM spanning datastores |

Unresolved files remain file contributors. `unattributed` is the residual that
makes contributors sum to datastore growth.

Projection fits used bytes to usable history by linear least squares. It is
`unknown` with fewer than three points, under a day of history, non-shrinking
free space, or incomplete coverage. Sparse history, a weak fit, partial runs,
resizes, or pruning-like gaps produce `low-confidence`.

Shared backing identities merge datastores across vCenters; local datastores
stay context-scoped. JSON and stderr report missing coverage.

## Retention and recovery

```sh
vsfleet assessment prune --older-than 90d       # dry-run
vsfleet assessment prune --older-than 90d --execute
vsfleet assessment backup ./history-backup.db
vsfleet assessment restore ./history-backup.db --force
vsfleet assessment doctor
vsfleet assessment delete <run> --force
```

Capture, prune, backup, and restore use a fenced lease that permits one writer
at a time. Listing, diffing, and the TUI do not take it. Backups are consistent
SQLite snapshots; restore makes a safety copy before replacing the database.

The database defaults to `<user-config-dir>/vsfleet/history.db`. Override it
with `--history-db <path>` or `VSFLEET_HISTORY_DB`. It may contain operational
inventory, so protect it using normal host disk and backup controls.

<span id="bounds-and-impact"></span>
<span id="unknown-is-never-zero"></span>
<span id="sizing-signal"></span>
<span id="in-reports"></span>
<span id="validation-status"></span>

## VM performance history

See [vm performance history](performance.md) for collection, missing samples, and sizing signals.

<span id="health-findings"></span>

## Health and orphan checks

See [health and orphan checks](health.md) for health findings and orphan-disk evidence.

<span id="deterministic-exports"></span>
<span id="source-provenance-vsource"></span>
<span id="datastore-file-inventory-vfileinfo"></span>
<span id="importing-an-rvtools-export"></span>
<span id="rvtools-file-interoperability"></span>
<span id="guest-partitions-need-vmware-tools"></span>
<span id="resource-pools-are-export-evidence"></span>
<span id="host-storage-and-network-inventory"></span>
<span id="distributed-switch-inventory"></span>

## Inventory exports

See [inventory exports](exports.md) for XLSX and CSV workflows, worksheet coverage, and compatibility.

<span id="migration-readiness"></span>
<span id="vm-decommission-review"></span>
<span id="topology-and-dependency-queries"></span>
<span id="cross-cluster-network-readiness"></span>
<span id="destination-sizing-scenarios"></span>

## Migration and capacity planning

See [migration and capacity planning](planning.md) for migration readiness, dependencies, sizing, and decommission blockers.

<span id="metadata-exports-and-saved-reports"></span>
<span id="saved-report-definitions"></span>
<span id="workbook-sheet-and-sharing-profiles"></span>

## Tags and custom attributes

See [tags and custom attributes](metadata.md) for metadata exports and saved report definitions.

<span id="scoped-sharing-profiles-and-pseudonymization"></span>

## Sharing inventory

See [sharing inventory](sharing.md) for scoped export profiles and pseudonymization.
