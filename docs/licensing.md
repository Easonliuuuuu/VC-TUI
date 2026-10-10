# License metadata (opt-in)

Add `--include-licenses` to capture vSphere product, usage, expiration, and
assignment metadata for a licensing review. Collection is read-only, off by
default, and never stores license keys.

```sh
vsfleet assessment run --all-contexts --include-licenses
vsfleet assessment export latest --file estate.xlsx      # adds vLicense
vsfleet assessment export latest --format csv --file ./estate-csv
```

Without the flag, vsfleet makes no license API call and records no license
data. The export has no `vLicense` sheet or license coverage row. Absence
means not collected.

## What is collected

Each context makes two read-only API reads:

| Read | Evidence |
| --- | --- |
| `LicenseManager.licenses` | Name, edition, cost unit, total, used, expiration, features |
| `LicenseAssignmentManager.QueryAssignedLicenses` | Assigned entity ID, name, type (`host`, `cluster`, `vcenter`, `other`), and scope |

The history database stores those fields with the run ID, context, endpoint,
vCenter instance UUID, collection times, and API version. Features are sorted.
Free-text license labels are not collected.

Records receive ordinal IDs such as `license-001` after deterministic
sorting. These IDs are independent of keys and unstable across captures; use
them only within a run.

## License keys are never exposed

The collector uses keys in memory to join assignments, then discards them.
Its stored record type has no key field. Known keys are scrubbed from server
errors before storage. Keys cannot appear in history, JSON, CLI text, logs,
XLSX/CSV, demo data, or sharing profiles.

The RVTools-compatible `vLicense` sheet retains the `Key` column for column
alignment, with every cell set to `[redacted]`. There is no key-export option.

## Coverage: unavailable is not zero

Accounts without license privileges may receive an empty list without an
error. vsfleet treats zero records as unavailable because vCenter and ESXi
hold at least an evaluation license.

| Status | Meaning |
| --- | --- |
| `success` | License records and assignments were returned |
| `partial` | Records were returned but assignments were denied or unsupported, or licenses were visible only through assignments |
| `unavailable` | Access denied, unsupported server version, or zero usable records; never `empty` |
| `failed` | The context could not be reached |
| `not recorded` | License collection exists for the run, but this context has no record |

Statuses and reasons appear in `vsfleetCoverage` for `vLicense` and
`vsfleetLicenseAssignment`, in `assessment report`, and on capture stderr.
A requested but incomplete license collection makes the run `partial`;
`--fail-on-partial` catches it.

## Privileges

Both reads require `Global.Licenses`, normally granted at the vCenter root
folder. The built-in ReadOnly role lacks it. Assign a dedicated role with
ReadOnly plus this privilege to the capture account.

No write privileges or mutating license operations are used; the SOAP audit
checks this. Privilege behavior has only been checked with the synthetic
simulator and an observation of another tool. Confirm it on your vCenter and
version before relying on it.

## The export

`vLicense` follows RVTools header order, after `vMultiPath`:

`Name` · `Key` · `Labels` · `Cost Unit` · `Total` · `Used` · `Expiration Date` · `Features` · `VI SDK Server` · `VI SDK UUID` · `vsfleet Context`

| Field | Format |
| --- | --- |
| `Key` | Always `[redacted]` |
| `Labels` | Empty; not collected |
| `Total`, `Used` | Numeric |
| `Expiration Date` | UTC date cell in XLSX, RFC3339 in CSV; empty if absent |
| `Features` | Joined with `; ` |

`vsfleet Context` is the trailing extension used by other sheets.
`vsfleetLicenseAssignment` lists each entity's license name and edition and
exists only alongside `vLicense`.

Cost-unit, expiration, and feature values follow the API and synthetic fixture.
Their compatibility with real RVTools license rows remains unverified.

## Interpreting the numbers

`Used` and `Total` are aggregates in the license's cost unit, such as CPU
packages or VMs. `Used` is not a host count. vSphere omits zero use; an
exported `0` means none reported. Assignments identify the entities holding
the license; do not add them to its usage count.

This is a capture-time observation, not a compliance verdict, entitlement or
purchase record, contract interpretation, or quote. Reviewers must compare it
with their organization's entitlement records.

## Intended workflow: licensing review handoff

1. Capture with `assessment run --all-contexts --include-licenses` using an
   account granted `Global.Licenses`.
2. Check `vsfleetCoverage` or `assessment report`. Every context needs
   `vLicense` status `success`; `partial` or `unavailable` is incomplete.
3. Export XLSX/CSV for the reviewer. Re-export is offline, deterministic, and
   requires no credentials.
4. Compare products, usage, expiry, and assignments against entitlement records.

No downstream consumer has yet validated this workflow.

## Unresolved validation

- Real-vCenter and RVTools comparisons, including cost units, expiration, and features.
- Privilege and response behavior on each supported vSphere version.
- A named downstream licensing consumer reading the export.
