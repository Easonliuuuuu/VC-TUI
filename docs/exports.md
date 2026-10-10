# Inventory exports

## Export a stored run

Exports read stored evidence offline. The `rvtools` format writes XLSX;
`csv` writes one `<tab>.csv` per worksheet for tools such as `jq`, `awk`,
and `pandas`.

```sh
vsfleet assessment export latest --format rvtools --file ./estate.xlsx
vsfleet assessment export latest --format csv --file ./estate-csv/
```

Supply a `.xlsx` destination for `rvtools` or a directory for `csv`.
Existing destinations require `--force`. Unchanged evidence produces
byte-identical output. `vsfleetCoverage` records every sheet/context's
collection status, item count, and error.

## Source provenance (`vSource`)

`vSource` has one row per context with a captured vCenter/ESXi
`ServiceInstance` About record. Its RVTools columns are:

`Name`, `OS type`, `API type`, `API version`, `Version`, `Patch level`,
`Build`, `Fullname`, `Product name`, `Product version`, `Product line`,
`Vendor`, `VI SDK Server`, `VI SDK UUID`.

vsfleet appends `vsfleet Context`. All values are text, preserving patch
levels such as `00400`. `VI SDK Server` is the configured URL with scheme;
RVTools uses the host alone.

`vsfleetCoverage` marks each context `success` (one row), `failed`
(connection error), or `not recorded` (pre-schema-17 capture/imported
workbook). Unconnected contexts have no row; versions are never inferred.

A [same-vCenter comparison](#rvtools-file-interoperability) matched every
RVTools column name, column order and value except `VI SDK Server`.

Runs captured before VMware Tools version collection still populate the running
status column in `vTools`; version columns remain blank and the gap is recorded
on `vsfleetCoverage`.

## Datastore file inventory (vFileInfo)

`vFileInfo` lists datastore files. Capture is opt-in because browsing large
estates costs time, storage, and an additional privilege, and exposes names.

```sh
vsfleet assessment run --all-contexts --datastore-file-inventory
vsfleet assessment export latest --format rvtools --file ./estate.xlsx
```

### Capture and limits

Default assessments never browse datastores. `--datastore-file-inventory`
and `--browse-datastores` are independent. Orphan browsing collects only
VMDKs, without file types, up to 10,000 files per datastore. File inventory
never changes orphan/health verdicts, even when complete. Limit flags without
`--datastore-file-inventory` are errors.

File inventory requires read-only `Datastore.Browse` and reads no file
contents. Limits are:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--file-inventory-max-files` | 100,000 (max 1,000,000) | rows kept per datastore; more is recorded as `truncated` |
| `--file-inventory-max-total-files` | 500,000 (max 5,000,000) | rows kept per context; datastores not reached are `skipped` |
| `--file-inventory-timeout` | 10m | time for one datastore before it is `failed`; each context gets three times this at most |

Each datastore uses one recursive search. Its full result enters memory, then
is sorted and capped, so truncation is independent of server order.

### Read coverage before files

| Datastore status | Meaning |
| --- | --- |
| `complete` | Listed to the end |
| `truncated` | Row limit reached; only a prefix retained |
| `denied` | Missing `Datastore.Browse` |
| `failed` | Error or timeout |
| `skipped` | Context row/time budget exhausted |
| `unavailable` | Inaccessible datastore or no browser |

Only `complete` can establish that a file is absent. Coverage appears in:

- `vsfleetCoverage`: a `vFileInfo` row per context (`success`, `empty`,
  `partial`, `failed`, `not recorded`), plus a `vFileInfo/<datastore>`
  row per requested datastore with status, count, and reason. Unrequested or
  pre-schema-19 inventory is `not recorded`; failed datastore collection is
  `failed`.
- Human output: capture prints incomplete datastores uppercase and warns on
  stderr; export repeats each warning.
- JSON: capture/export `file_inventory` lists each context/datastore's
  `status`, `files`, and `message`.

`vFileInfo` is always written. Without file rows it contains one explanatory
`Friendly Path Name` row, not a file. This may mean unrequested, denied,
failed, skipped, or unavailable inventory; it does not prove empty storage.

Export cannot fill coverage gaps. Rows sort by context, datastore, and path;
RVTools follows browse order. Compare by keys, not row position.

### Columns and compatibility

The first eight columns follow RVTools `-GetFileInfo`:

| Column | Value |
| --- | --- |
| `Friendly Path Name` | Folder: `[datastore] folder/` |
| `File Name` | Bare filename |
| `File Type` | vSphere `FileInfo` subclass |
| `File Size in bytes` | Number formatted `#,##0` |
| `Path` | Folder: `[datastore] folder/` |
| `Internal Sort Column` | `Path` + `File Name` |
| `VI SDK Server` | Source endpoint |
| `VI SDK UUID` | Source identity |

Other columns are text. File subclasses include `FileInfo`, `VmDiskFileInfo`,
`VmConfigFileInfo`, `VmLogFileInfo`, `VmNvramFileInfo`,
`VmSnapshotFileInfo`, `IsoImageFileInfo`, and `TemplateConfigFileInfo`.
vsfleet appends `Datastore`, `Datastore ID`, `Datacenter`, and
`vsfleet Context` to distinguish repeated datastore names.

Verified conventions:

- Compared on 2026-09-29 against an RVTools `-GetFileInfo` export of a
  vCenter 8.0.3 lab with three datastores (VMFS and NFS): the same 97 files, with
  identical `Friendly Path Name`, `File Name`, `File Type`, `Path`,
  `Internal Sort Column` and cell types. Sizes differed only for disks of a
  running VM that grew between the two captures.
- `-flat.vmdk`: neither lists a separate row; the descriptor `.vmdk` row carries
  the full disk size (0 for an empty thin disk).
- Folders are excluded, including subfolders reported as plain `FileInfo`.
  Files in hidden directories such as `.dvsData` are included.
- Files at the datastore root use the folder `[datastore]` with no trailing
  space, so their `Internal Sort Column` is `[datastore]name`, as RVTools writes
  it.
- The later [same-vCenter comparison](#rvtools-file-interoperability) listed
  the same files, including those at the datastore root, with matching names,
  types and paths; only `VI SDK Server` differed.
- Not yet compared: vSAN, vVols and datastores with very large file counts.

### Size and privacy

Paths can reveal VM, application, project, backup, snapshot, personal, and
customer names and storage layout. Export warns on stderr when files are
present. Treat the workbook and history database as sensitive.

Stored rows use roughly 100 to 200 bytes per file. Defaults permit 500,000 files
per context per capture (tens of megabytes). XLSX permits 1,048,575 data rows
per sheet; CSV has no such limit. CSV preserves names starting with `=`,
`+`, `-`, or `@`, which spreadsheet applications may evaluate.
Schedule file inventory only with appropriate database/export protection.

## Importing an RVTools export

`vsfleet import rvtools` loads compatible workbooks into assessment history
for `diff`, `vm history`, `trends`, and `topology`. Missing worksheets and
columns become explicit coverage gaps, never confirmed zeroes. See [Importing RVTools
exports](commands.md#importing-rvtools-exports).

## RVTools file interoperability

The `rvtools` profile provides these 25 RVTools worksheet layouts:

`vInfo` · `vCPU` · `vMemory` · `vDisk` · `vPartition` · `vNetwork` · `vCD` · `vUSB` ·
`vSnapshot` · `vTools` · `vSource` · `vRP` · `vCluster` · `vHost` · `vHBA` · `vNIC` · `vSwitch` ·
`vPort` · `dvSwitch` · `dvPort` · `vSC_VMK` · `vDatastore` · `vMultiPath` ·
`vFileInfo` · `vHealth`

An opt-in twenty-sixth worksheet, `vLicense`, is written only for a capture
taken with `--include-licenses`, with the license key column redacted. See
[License metadata](licensing.md); a default export has no such sheet.

Workbooks also include vsfleet's `vsfleetCoverage` and
[`vsfleetPerformance`](performance.md#in-reports) sheets. Opt-in
[`vsfleetMetadata`](metadata.md#workbook-sheet-and-sharing-profiles) and
[sharing profiles](sharing.md) add further vsfleet sheets.

Compatibility covers these names and columns only. Pipelines requiring other
worksheets are unsupported.

`vsfleet compatibility report` lists every column's type, unit, and empty-cell
conditions from the exporter definitions. It runs without configuration,
keyring, or vCenter:

```sh
vsfleet compatibility report --sheet vPartition
vsfleet compatibility report -o json | jq
```

Same-vCenter comparisons used RVTools 4.8.1.4
([#207](https://github.com/Easonliuuuuu/vsfleet/issues/207), captures about a
minute apart) and RVTools 4.8.2.1
([#221](https://github.com/Easonliuuuuu/vsfleet/issues/221), captures 15 seconds
apart on 2026-10-10). The later audit used vsfleet `a844cc9` and a read-only
account on a nested vCenter/ESXi 8.0.3 lab. Both RVTools versions had the same
sheet names and order, headers and order, and cell types. The later comparison
found no regressions against the earlier findings.

`vSource` matched every RVTools column name, column order and value except
`VI SDK Server`. `vFileInfo` listed the same files, including those at the
datastore root, with matching names, types and paths; only `VI SDK Server`
differed. pandas `read_excel` and openpyxl `read_only` read both workbooks
cleanly. Tags, custom attributes and license values were not validated: the
lab had no tags or custom attributes, and the account lacked `Global.Licenses`.
These comparisons cover the lab's shared columns, not every RVTools field or
downstream workflow. Other versions, estates and collector settings may differ.

Observed value differences:

| Sheet / column | vsfleet value | RVTools value |
| --- | --- | --- |
| VM sheets, `Folder` | Path relative to the datacenter's `vm` folder: `/` or `/Discovered virtual machine` | Includes the datacenter: `/DC-Lab` or `/DC-Lab/Discovered virtual machine` |
| RVTools-named sheets, `VI SDK Server` | Configured endpoint URL, for example `https://192.168.150.10` | Host without URL scheme, for example `192.168.150.10` |
| `vHost`, `ESX Version` | Version only, for example `8.0.3` | Product name, version and build, for example `VMware ESXi 8.0.3 build-24784735` |
| `vHost`, `# VMs total` | Counts host VM references, including templates | Excludes templates; the comparison found 4 versus vsfleet's 5 |
| `vTools`, `Tools` | VMware Tools running status (`GuestInfo.toolsRunningStatus`), for example `guestToolsNotRunning` | Tools installation status, for example `toolsNotInstalled` |
| `vHBA`, `Type` | vSphere adapter type, for example `HostBlockHba` | Human-readable label, for example `Block SCSI` |
| `dvPort`, `VLAN` on a switch's uplink port group | Reported trunk range, for example `trunk 0-4094` | Empty |
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

## Guest partitions need VMware Tools

`vPartition` contains guest filesystem paths, capacity, consumed, and free
space from VMware Tools. Powered-off VMs or VMs without running Tools produce
no rows; virtual disk size does not establish guest usage.

`Disk Key` joins filesystems to `vDisk`. Tools provides this mapping on
vSphere 7.0+; older estates leave it blank. Volumes spanning disks list each key.

`vsfleetCoverage` marks incomplete guest filesystem coverage `partial` and
names the shortfall (for example, 18 of 40 VMs reported filesystems). Captures
predating this sheet are `not recorded`.

## Resource pools are export evidence

`vRP` lists resource pools in the datacenter scope, including root
`Resources` pools, then vApps. vApps share resource-pool allocation fields;
their `VMs`/`vCPUs` count direct members. Live pools are also available
through `vsfleet resourcepool list`.

The sheet covers identity and configured CPU/memory allocation, excluding
volatile or unavailable RVTools QuickStats.

Pre-schema-20 runs omit vApps with the coverage message `capture predates
vApp inventory; vApps are not listed`. Failed `vapp` collection makes
`vRP` partial with its error.

## Host storage and network inventory

Host sheets include storage adapters (`vHBA`), host/LUN path-state counts
(`vMultiPath`), physical NICs (`vNIC`), standard switches (`vSwitch`),
port groups (`vPort`), and VMkernel/legacy service-console adapters
(`vSC_VMK`). Assessment capture reads `HostSystem.config.storageDevice`
and `HostSystem.config.network`; search, host listing, and TUI use summaries.

Schema-14+ `vMultiPath` rows record local-storage evidence. Local disks are
excluded from redundancy findings; unknown locality remains unresolved.
Inventory schema version 15 renames the persisted VM NIC direct-path field to
`upt_compatibility_enabled`; the old `direct_path_io` value in schemas 12 to 14
rows was UPT compatibility mislabeled and is no longer read as passthrough
evidence.

## Distributed switch inventory

Assessment capture stores distributed switches and port groups from vSphere
`config`/`summary`. `dvSwitch` covers identity, MTU, hosts, uplinks,
discovery, LACP, and product information. `dvPort` has one row per port group
with default VLAN, teaming, security, and shaping policy; runtime ports are
outside the profile. Captures before inventory schema 10 mark both sheets
`not recorded` in `vsfleetCoverage`.

`vsfleet network list` includes parent switch and VLAN for distributed port
groups without an assessment.
