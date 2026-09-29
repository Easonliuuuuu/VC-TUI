# License metadata (opt-in)

vsfleet can record vSphere license **metadata** as part of an assessment so a
licensing review has the product, usage, expiration and host-assignment
evidence in one handoff. It is off by default, read-only, and never handles
license keys.

```sh
vsfleet assessment run --all-contexts --include-licenses
vsfleet assessment export latest --file estate.xlsx      # adds vLicense
vsfleet assessment export latest --format csv --file ./estate-csv
```

Without `--include-licenses` a capture makes no license call, stores nothing
about licenses, and its export has no `vLicense` sheet and no license row in
`vsfleetCoverage`. The absence means **not collected**; it is never a
statement that the estate has no licenses.

## What is collected

Two read-only vSphere API reads per context:

| Read | Purpose |
| --- | --- |
| `LicenseManager.licenses` property | License records: name, edition, cost unit, total, used, expiration, features |
| `LicenseAssignmentManager.QueryAssignedLicenses` | Which entities (hosts, clusters, the vCenter itself) hold which license |

Persisted per license, in the local history database with the run and context
that produced it (run ID, context, endpoint, vCenter instance UUID, collection
start and finish times, and the vSphere API version):

- display name, edition key, cost unit
- total and used counts
- expiration timestamp, when vSphere reports one
- feature names, sorted
- the entities assigned the license: ID, display name, type
  (`host`, `cluster`, `vcenter`, `other`) and scope

Deliberately **not** persisted: the license key, and license labels (free text
outside the review need). License records get an ordinal ID (`license-001`)
assigned after a deterministic sort. It is not derived from the key and is not
stable across captures, so do not use it to track a license over time.

## License keys are never exposed

A license key is a credential-like secret. vsfleet keeps it out of every
surface:

- The vSphere layer maps wire values into a record type that has no key field.
  The key is used in memory only to join an assignment to its license, then
  discarded.
- Nothing key-bearing is written to the history database, JSON output, CLI text,
  logs, XLSX or CSV exports, demo data, or sharing profiles. Error text from the
  server is scrubbed of known keys before it is stored.
- The `vLicense` sheet keeps the RVTools `Key` column so column positions match,
  but every cell is the fixed marker `[redacted]`.

There is no key-export option. If exact `vLicense` key interoperability is ever
required, it needs a separate, deliberate design: an explicit gate, a decision
about where keys may live, and its own validation path. It is not implemented
and must not be added by relaxing the default outputs.

## Coverage: unavailable is not zero

License data needs a privilege many read-only accounts lack, and vSphere can
answer such an account with an empty list rather than an error. An RVTools
`vLicense` worksheet exported by a read-only vCenter account has been observed
with headers and no rows and no warning. vsfleet refuses to reproduce that
false reassurance. Each context's license collection ends in one of:

| Status | Meaning |
| --- | --- |
| `success` | Licenses were returned, and assignments answered |
| `partial` | Licenses were returned but assignments could not be read (denied, or not offered by this server, such as a standalone ESXi host), or licenses were visible only through assignments |
| `unavailable` | Nothing usable: access denied, an unsupported vSphere version, or **zero records returned**. Zero records is never reported as `empty`, because every vCenter and ESXi host holds at least an evaluation license |
| `failed` | The context could not be reached |
| `not recorded` | The run has license collection but this context has no license record |

The status and the reason (`permission denied ... Global.Licenses`,
`unsupported: ...`) appear in `vsfleetCoverage` for both `vLicense` and
`vsfleetLicenseAssignment`, in `assessment report`, and on stderr during the
capture. A run that requested licenses and did not fully obtain them is
recorded as `partial`, so `--fail-on-partial` catches it.

## Privileges

Both reads need the vSphere privilege **`Global.Licenses`**, normally granted
at the vCenter root folder. The built-in read-only role does not include it.
Create a dedicated role with that single privilege plus read-only, and assign it
to the account used for the capture. No write privilege is required; vsfleet
does not call `AddLicense`, `RemoveLicense`, `UpdateAssignedLicense` or any
other mutating license operation, which the read-only SOAP audit enforces.

The exact privilege requirement per vSphere version has been established only
against the synthetic simulator and one observation of another tool's behavior;
confirm it against your vCenter before relying on it.

## The export

`vLicense` follows the RVTools 4.8 header order and is placed after
`vMultiPath`:

`Name` · `Key` · `Labels` · `Cost Unit` · `Total` · `Used` · `Expiration Date` · `Features` · `VI SDK Server` · `VI SDK UUID` · `vsfleet Context`

The trailing `vsfleet Context` column is the same vsfleet addition every sheet
carries. `Key` is always `[redacted]` and `Labels` is always empty (not
collected). `Total` and `Used` are numbers; `Expiration Date` is a date cell in
XLSX (UTC) and RFC3339 in CSV, empty when vSphere reports no expiration.
`Features` are joined with `; `.

`vsfleetLicenseAssignment` is a vsfleet extension listing, per entity, the
license name and edition it holds. It exists only alongside `vLicense`.

Value formats for `Cost Unit`, `Expiration Date` and `Features` follow the
vSphere API and a synthetic fixture; they have not yet been compared against
real license rows from RVTools or a real vCenter. Do not claim value-level
compatibility until that comparison exists (see
[Unresolved validation](#unresolved-validation)).

## Interpreting the numbers

- **Used** and **Total** are aggregate counts vSphere reports for a license in
  its own cost unit (for example CPU packages, or VMs). `Used` is not a host
  count, and vSphere omits it when zero, so `0` means none reported.
- **Assignments** name the hosts, clusters or vCenter that hold each license
  at capture time. Several hosts on one license explain its `Used` count; they
  are the evidence for it, not a second count to add up.
- Everything is a point-in-time observation of what vSphere says is installed
  and assigned. It is **not** a compliance determination, an entitlement or
  purchase record, a contract interpretation or a quote. A licensing review
  must compare it with the organization's own entitlement records.

## Intended workflow: licensing review handoff

1. A vSphere administrator captures with a dedicated `Global.Licenses` account:
   `assessment run --all-contexts --include-licenses`.
2. Check `vsfleetCoverage` (or `assessment report`): every context should show
   `vLicense` as `success`. `partial` or `unavailable` means the evidence is
   incomplete, whatever the sheet looks like.
3. Export XLSX or CSV and hand it to the licensing reviewer. Re-exporting the
   same run is offline and byte-for-byte deterministic, and needs no
   credentials.
4. The reviewer compares products, usage, expiration dates and host
   assignments with their entitlement records. Any conclusion about
   compliance, true-up or cost is theirs; vsfleet supplies only the
   observations.

No downstream tool or reviewer has been named or tested yet. Until one has
consumed the export, this describes the intended workflow, not a validated one.

## Unresolved validation

- Comparison of `vLicense` values against a real vCenter with real license
  rows, including `Cost Unit`, `Expiration Date` and `Features` formats.
- Confirmation of the required privilege and of behavior on each supported
  vSphere version.
- A named downstream licensing consumer reading the export.
